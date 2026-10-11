//go:build !windows

package api

import (
	"errors"
	"syscall"
)

// isRefused and isUnreachable read a failed connect. Separate per system because Windows reports these with its own
// Winsock codes, which syscall.ECONNREFUSED and friends do not match there.
func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

func isUnreachable(err error) bool {
	return errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH)
}
