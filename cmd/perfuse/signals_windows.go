//go:build windows

package main

import "os"

// shutdownSignals are the signals that mean stop.
//
// Interrupt only. syscall.SIGTERM is defined as a value on Windows but is never delivered by anything, so listing it
// would read as handling a case that cannot happen - and would suggest a graceful stop was covered when it was not.
//
// A Windows service is stopped through the service control manager instead, which is handled in internal/winservice and
// arrives as the cancellation of serveParent rather than as a signal.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
