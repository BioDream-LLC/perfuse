//go:build !windows

package vfs

import (
	"errors"
	"syscall"
)

// isCrossDevice reports whether a rename failed because the two paths are on different filesystems.
//
// Worth distinguishing because the message is completely different from any other rename failure. "Invalid cross-device
// link" tells somebody nothing; naming the two directories and saying they must share a filesystem is actionable.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}
