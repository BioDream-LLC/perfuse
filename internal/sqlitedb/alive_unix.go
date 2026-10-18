//go:build !windows

package sqlitedb

import "syscall"

// processAlive reports whether a process with this id exists on this machine.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
