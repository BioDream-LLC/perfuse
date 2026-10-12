package main

import (
	"errors"
	"io"
)

// errRestart is what cmdServe returns after a restart was asked for from the console and the server has shut down cleanly.
var errRestart = errors.New("restart requested")

// restartRequests carries a console's request to restart. Buffered by one, so asking twice while the first is under way is
// not an error and does not block the request that asked.
var restartRequests = make(chan struct{}, 1)

// requestRestart asks the running server to restart.
func requestRestart() {
	select {
	case restartRequests <- struct{}{}:
	default:
	}
}

// serveUntilDone runs serve, and starts it again each time it stops for a restart.
//
// A restart reads every setting again, which is the point of it: settings marked "needs a restart" are read only at start.
// On Linux and macOS the process replaces itself with a fresh copy of the same program and arguments (same process id, so
// systemd, launchd and a container runtime see nothing happen). Windows has no exec, so there serve runs again in the same
// process, which also keeps a Windows service under the service manager.
func serveUntilDone(args []string, stdout, stderr io.Writer) error {
	for {
		err := cmdServe(args, stdout, stderr)
		if !errors.Is(err, errRestart) {
			return err
		}
		if err := replaceProcess(); err != nil {
			return err
		}
	}
}
