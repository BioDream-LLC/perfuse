package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/biodream-llc/perfuse/internal/winservice"
)

// serveParent is the context every long-running command shuts down from.
//
// A package-level variable rather than a parameter threaded through every command, because it is written exactly once -
// in main, before any command runs - and read from a single place in each long-running command. Nothing is concurrent at
// the point it is set.
//
// It exists so there is one shutdown path rather than two. Under the Windows service manager, stop arrives as a control
// message rather than a signal, and a second shutdown path would be the one that never gets exercised: the console path
// is what a developer runs all day, and the service path is what a hospital runs in production. The one that matters
// would be the untested one.
var serveParent = context.Background()

// shutdownContext returns a context cancelled when this process is asked to stop.
//
// On Windows under the service manager, serveParent is already cancelled by the stop message and the signal watch adds
// nothing. Elsewhere it is Ctrl-C or SIGTERM. Both are wired here so a caller does not need to know which it is.
func shutdownContext() (context.Context, func()) {
	// SIGTERM is deliberately not named here. It is spelled os.Interrupt plus the platform's terminate signal in
	// platform files, because syscall.SIGTERM exists as a value on Windows but is never delivered - so naming it
	// directly compiles everywhere and works only on Unix, which is exactly the kind of thing that looks correct in
	// review and fails in production.
	return signal.NotifyContext(serveParent, shutdownSignals()...)
}

// runUnderServiceManagerIfNeeded runs fn as a Windows service when Windows started it that way.
//
// Reports whether it handled the run. False means this is an ordinary console process and the caller should carry on.
//
// Asked rather than configured, because the same executable and the same command line have to work both ways: a person
// runs perfuse serve to try it, and the service manager runs the identical line in production. A flag would be one more
// thing to get wrong, and getting it wrong is silent in one direction - a service that does not connect to the manager
// is killed after thirty seconds with nothing explaining why.
func runUnderServiceManagerIfNeeded(fn func() error, stderr io.Writer) (bool, error) {
	isService, err := winservice.IsService()
	if err != nil {
		// Not fatal: fall through to the console path. Failing to ask is not the same as the answer being no, but a
		// console run is the safer assumption - it logs to somewhere a person can see.
		fmt.Fprintf(stderr, "could not determine whether this is a service, continuing as a console process: %v\n", err)
		return false, nil
	}
	if !isService {
		return false, nil
	}

	name := os.Getenv("PERFUSE_SERVICE_NAME")
	if name == "" {
		name = winservice.DefaultName
	}

	runErr := winservice.Run(name, func(ctx context.Context) error {
		// The service manager's stop becomes the parent of the ordinary shutdown context, so everything downstream
		// drains exactly as it does on Ctrl-C.
		serveParent = ctx
		return fn()
	})

	return true, runErr
}
