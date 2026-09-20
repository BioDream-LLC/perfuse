//go:build windows

package vfs

import (
	"errors"
	"syscall"
)

// errNotSameDevice is ERROR_NOT_SAME_DEVICE, which Windows returns when a move crosses volumes.
const errNotSameDevice syscall.Errno = 17

// isCrossDevice reports whether a rename failed because the two paths are on different volumes.
func isCrossDevice(err error) bool {
	return errors.Is(err, errNotSameDevice)
}
