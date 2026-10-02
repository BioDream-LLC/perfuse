//go:build windows

// Package winservice runs Perfuse as a Windows service.
//
// Hospitals run Windows, and on Windows "runs on startup, survives logout, restarts after a crash" means a service. An
// integration engine that has to be started by hand from a console window is not something a hospital can put into
// production: the first time somebody logs off the server, patient data stops moving.
//
// This is behind a build tag rather than guarded at run time, so nothing here is compiled into the Linux or macOS binary
// and golang.org/x/sys/windows is never a dependency of those builds.
package winservice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// DefaultName is the service name used when none is given.
const DefaultName = "Perfuse"

// DefaultDisplayName is what appears in the services list.
const DefaultDisplayName = "Perfuse Integration Engine"

// IsService reports whether this process was started by the service manager.
//
// Asked rather than assumed, because the same executable has to work both ways: run from a console during setup and
// under the service manager in production. Getting this wrong in either direction is bad - a console run that tries to
// talk to the service manager exits immediately, and a service that does not connect is killed after thirty seconds
// with nothing useful logged.
func IsService() (bool, error) {
	return svc.IsWindowsService()
}

// Run runs work under the service manager, stopping it when Windows asks.
//
// work is given a context that is cancelled on stop or shutdown, and is expected to return when it is done. It is the
// same function the console path runs, so there is one shutdown path rather than two.
func Run(name string, work func(context.Context) error) error {
	h := &handler{work: work}
	if err := svc.Run(name, h); err != nil {
		return fmt.Errorf("the service could not run: %w", err)
	}
	return h.err
}

// handler adapts a function to the service control protocol.
type handler struct {
	work func(context.Context) error
	err  error
}

// Execute responds to the service manager.
func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	// StartPending first, with a wait hint. Windows kills a service that does not report Running quickly enough, and
	// a channel opening a database and binding listeners can legitimately take a few seconds.
	status <- svc.Status{State: svc.StartPending, WaitHint: 20000}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.work(ctx) }()

	// Stop and shutdown only. Pause and continue are deliberately not accepted: pausing an integration engine would
	// mean holding a half-open TCP connection with a sending system waiting for an acknowledgement it will never get,
	// which is worse for the sender than being refused outright.
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-done:
			// Exited on its own, which for a service means something failed. Reported through the exit code so the
			// service manager can restart it if it has been configured to.
			h.err = err
			status <- svc.Status{State: svc.StopPending}
			if err != nil {
				return false, 1
			}
			return false, 0

		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus

			case svc.Stop, svc.Shutdown:
				// StopPending with a generous hint. Shutting down means draining what is in flight - finishing the
				// message currently being written, acknowledging what has been delivered - and Windows will
				// terminate the process if it is not told to wait.
				status <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				cancel()

				select {
				case err := <-done:
					h.err = err
				case <-time.After(25 * time.Second):
					// Reported rather than hidden. A shutdown that did not finish means something in flight was
					// dropped, and that is worth knowing after the fact.
					h.err = fmt.Errorf("the service did not shut down within 25 seconds; work may have been dropped")
				}

				if h.err != nil {
					return false, 1
				}
				return false, 0
			}
		}
	}
}

// InstallOptions describes a service to be installed.
type InstallOptions struct {
	// Name is the service name. Defaults to Perfuse.
	Name string

	// DisplayName appears in the services list.
	DisplayName string

	// Args are passed to the executable on start, after the serve subcommand.
	Args []string

	// Description appears in the service properties.
	Description string
}

// Install registers the service with Windows.
//
// The executable is not copied anywhere. Installing from wherever the binary already lives means there is one copy on
// disk, and an upgrade is replacing that file rather than replacing it and hoping the service is pointing at the same
// one.
func Install(opts InstallOptions) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("this executable's own path could not be determined: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}

	name := opts.Name
	if name == "" {
		name = DefaultName
	}
	display := opts.DisplayName
	if display == "" {
		display = DefaultDisplayName
	}
	description := opts.Description
	if description == "" {
		description = "Routes and transforms clinical messages between systems."
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("the service manager could not be reached; this needs an administrator command prompt: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	// Refused rather than replaced. Silently reconfiguring an existing service would discard settings somebody may
	// have changed deliberately - the account it runs as, its recovery actions, its dependencies.
	if existing, err := m.OpenService(name); err == nil {
		_ = existing.Close()
		return fmt.Errorf("a service named %q already exists; remove it first with: %s service remove", name, filepath.Base(exe))
	}

	cfg := mgr.Config{
		DisplayName: display,
		Description: description,
		// Automatic, because the reason to install a service is to survive a reboot without anybody logging in.
		StartType: mgr.StartAutomatic,
	}

	s, err := m.CreateService(name, exe, cfg, opts.Args...)
	if err != nil {
		return fmt.Errorf("the service could not be created: %w", err)
	}
	defer func() { _ = s.Close() }()

	// Restart on failure, three times, then leave it alone.
	//
	// Endless restarts would hide a configuration error as an unexplained gap in the message flow: the engine would
	// come up, fail to bind a port, exit, and repeat all night. Three attempts covers a transient cause - a database
	// not yet up after a reboot - and then stops so that somebody has to look.
	recovery := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := s.SetRecoveryActions(recovery, 3600); err != nil {
		// Not fatal. The service is installed and will run; it just will not restart itself. Said plainly rather
		// than swallowed, because somebody expecting automatic recovery should know they did not get it.
		return fmt.Errorf("the service was installed but its restart-on-failure actions could not be set: %w", err)
	}

	return nil
}

// Remove unregisters the service.
func Remove(name string) error {
	if name == "" {
		name = DefaultName
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("the service manager could not be reached; this needs an administrator command prompt: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("no service named %q is installed", name)
	}
	defer func() { _ = s.Close() }()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("the service could not be removed: %w", err)
	}

	return nil
}

// Status describes an installed service.
type Status struct {
	Name        string
	DisplayName string
	State       string
	StartType   string
	Command     string
}

// Query reports on an installed service.
func Query(name string) (*Status, error) {
	if name == "" {
		name = DefaultName
	}

	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("the service manager could not be reached: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(name)
	if err != nil {
		return nil, fmt.Errorf("no service named %q is installed", name)
	}
	defer func() { _ = s.Close() }()

	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	st, err := s.Query()
	if err != nil {
		return nil, err
	}

	return &Status{
		Name:        name,
		DisplayName: cfg.DisplayName,
		State:       stateName(st.State),
		StartType:   startTypeName(cfg.StartType),
		Command:     cfg.BinaryPathName,
	}, nil
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continuing"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown (%d)", s)
	}
}

func startTypeName(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "automatic"
	case mgr.StartManual:
		return "manual"
	case mgr.StartDisabled:
		return "disabled"
	default:
		return fmt.Sprintf("unknown (%d)", t)
	}
}
