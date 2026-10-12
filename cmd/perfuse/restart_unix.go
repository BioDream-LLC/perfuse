//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// replaceProcess replaces this process with a new copy of the same program, arguments and environment. It returns only if
// that failed.
func replaceProcess() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restarting: finding this program: %w", err)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("restarting: starting %s again: %w", exe, err)
	}
	return nil
}
