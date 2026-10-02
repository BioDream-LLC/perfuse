//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals are the signals that mean stop.
//
// SIGTERM is what an init system, a container runtime and kill all send. Ctrl-C is what a person sends.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
