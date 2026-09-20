//go:build !windows

// Package winservice runs Perfuse as a Windows service.
//
// This file is the everywhere-else half. It exists so that cmd/perfuse can call into this package without wrapping every
// call site in a build tag: the subcommand is always compiled, and on Linux or macOS it explains that services are a
// Windows idea rather than failing to exist.
//
// Refusing with a clear sentence is better than hiding the subcommand. Somebody following Windows instructions on the
// wrong machine gets told what is actually going on, and the equivalent for their platform.
package winservice

import (
	"context"
	"errors"
	"runtime"
)

// DefaultName is the service name used when none is given.
const DefaultName = "Perfuse"

// DefaultDisplayName is what appears in the services list.
const DefaultDisplayName = "Perfuse Integration Engine"

// errNotWindows explains the refusal, including what to use instead.
func errNotWindows() error {
	switch runtime.GOOS {
	case "linux":
		return errors.New("Windows services only exist on Windows; on Linux use a systemd unit")
	case "darwin":
		return errors.New("Windows services only exist on Windows; on macOS use a launchd plist")
	default:
		return errors.New("Windows services only exist on Windows")
	}
}

// IsService reports whether this process was started by the service manager.
//
// Always false here, so the ordinary console path runs.
func IsService() (bool, error) { return false, nil }

// Run runs work under the service manager.
func Run(string, func(context.Context) error) error { return errNotWindows() }

// InstallOptions describes a service to be installed.
type InstallOptions struct {
	Name        string
	DisplayName string
	Args        []string
	Description string
}

// Install registers the service with Windows.
func Install(InstallOptions) error { return errNotWindows() }

// Remove unregisters the service.
func Remove(string) error { return errNotWindows() }

// Status describes an installed service.
type Status struct {
	Name        string
	DisplayName string
	State       string
	StartType   string
	Command     string
}

// Query reports on an installed service.
func Query(string) (*Status, error) { return nil, errNotWindows() }
