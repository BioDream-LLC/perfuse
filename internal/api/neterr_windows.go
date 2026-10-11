//go:build windows

package api

import (
	"errors"
	"syscall"
)

// The Winsock codes for a refused connection and an unreachable host or network (winerror.h).
const (
	wsaeConnRefused syscall.Errno = 10061
	wsaeHostUnreach syscall.Errno = 10065
	wsaeNetUnreach  syscall.Errno = 10051
)

// isRefused and isUnreachable read a failed connect. Windows reports these with its own Winsock codes, which
// syscall.ECONNREFUSED and friends do not match there.
func isRefused(err error) bool { return errors.Is(err, wsaeConnRefused) }

func isUnreachable(err error) bool {
	return errors.Is(err, wsaeHostUnreach) || errors.Is(err, wsaeNetUnreach)
}
