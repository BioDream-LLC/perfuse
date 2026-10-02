package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/winservice"
)

// cmdService installs, removes and reports on the Windows service.
//
// Hospitals run Windows, and on Windows a thing that must survive a reboot and a logout is a service. Doing it here
// rather than telling people to use a third-party wrapper keeps the promise the rest of this project makes: one
// executable, nothing to install alongside it.
func cmdService(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		serviceUsage(stderr)
		return fmt.Errorf("service needs a subcommand: install, remove or status")
	}

	switch args[0] {
	case "install":
		return cmdServiceInstall(args[1:], stdout)
	case "remove", "uninstall":
		return cmdServiceRemove(args[1:], stdout)
	case "status":
		return cmdServiceStatus(args[1:], stdout)
	case "help", "--help", "-h":
		serviceUsage(stdout)
		return nil
	default:
		serviceUsage(stderr)
		return fmt.Errorf("unknown service subcommand %q", args[0])
	}
}

func serviceUsage(w io.Writer) {
	fmt.Fprint(w, `perfuse service - run Perfuse as a Windows service

  perfuse service install [flags]   register the service so it starts with Windows
  perfuse service remove  [flags]   unregister it
  perfuse service status  [flags]   report whether it is installed and running

Install flags:
  -name         service name (default Perfuse)
  -display      name shown in the services list
  -channels     channel directory the service should serve
  -db           database file the service should use
  -addr         address to listen on
  -oidc         OpenID Connect configuration file

Installing needs an administrator command prompt. The service runs the same
serve command you would run by hand, with the flags you give here.

Paths are made absolute before being stored, because a service starts in
C:\Windows\System32 and a relative path would resolve somewhere nobody
intended - most likely to an empty channel directory, which starts cleanly
and moves no messages at all.
`)
}

func cmdServiceInstall(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	name := fs.String("name", winservice.DefaultName, "service name")
	display := fs.String("display", winservice.DefaultDisplayName, "name shown in the services list")
	channels := fs.String("channels", "", "channel directory")
	db := fs.String("db", "", "database file")
	addr := fs.String("addr", "", "address to listen on")
	oidc := fs.String("oidc", "", "OpenID Connect configuration file")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Assembled as the arguments the service manager will pass on start. This is the same serve command somebody
	// would type, which means a service that will not start can be diagnosed by running that line by hand.
	svcArgs := []string{"serve"}

	pairs := []struct {
		flag  string
		value string
		path  bool
	}{
		{"-channels", *channels, true},
		{"-db", *db, true},
		{"-addr", *addr, false},
		{"-oidc", *oidc, true},
	}

	for _, p := range pairs {
		if strings.TrimSpace(p.value) == "" {
			continue
		}
		value := p.value
		if p.path {
			abs, err := absolutePath(value)
			if err != nil {
				return err
			}
			value = abs
		}
		svcArgs = append(svcArgs, p.flag, value)
	}

	if *channels == "" {
		// Refused rather than defaulted. A service with no channel directory starts, reports healthy, and moves
		// nothing - which looks exactly like a working installation until somebody notices no messages have arrived.
		return fmt.Errorf("service install needs -channels, the directory holding the channel files; " +
			"a service without it would start cleanly and move no messages")
	}

	opts := winservice.InstallOptions{
		Name:        *name,
		DisplayName: *display,
		Args:        svcArgs,
	}

	if err := winservice.Install(opts); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "installed the service %q\n", *name)
	fmt.Fprintf(stdout, "  it will run: perfuse %s\n", strings.Join(svcArgs, " "))
	fmt.Fprintf(stdout, "\nstart it with:  sc start %s\n", *name)
	fmt.Fprintf(stdout, "check it with:  perfuse service status -name %s\n", *name)

	return nil
}

func cmdServiceRemove(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("service remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", winservice.DefaultName, "service name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := winservice.Remove(*name); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "removed the service %q\n", *name)
	fmt.Fprintf(stdout, "\nThe channel files and the database were left alone.\n")
	return nil
}

func cmdServiceStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("service status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", winservice.DefaultName, "service name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := winservice.Query(*name)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "name          %s\n", st.Name)
	fmt.Fprintf(stdout, "display name  %s\n", st.DisplayName)
	fmt.Fprintf(stdout, "state         %s\n", st.State)
	fmt.Fprintf(stdout, "start type    %s\n", st.StartType)
	fmt.Fprintf(stdout, "command       %s\n", st.Command)

	return nil
}

// absolutePath makes a path absolute, explaining why if it cannot.
//
// A service does not inherit the working directory it was installed from: it starts in the system directory. A relative
// -channels would resolve there, find nothing, and start a healthy engine with no channels in it.
func absolutePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("%q could not be made absolute, and a service cannot use a relative path: %w", p, err)
	}
	return abs, nil
}
