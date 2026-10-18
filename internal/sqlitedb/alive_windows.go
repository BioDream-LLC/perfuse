//go:build windows

package sqlitedb

import "os"

// processAlive reports whether a process with this id exists on this machine.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
