//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// launchedByDoubleClick reports whether Explorer started this process rather than a command prompt.
//
// A console program started from Explorer gets a console of its own, so it is the only process attached to it. Started
// from cmd or PowerShell, the shell is attached as well. GetConsoleProcessList counts them; that count is the whole test.
// It is the documented way to tell the two apart, and it needs no guess about parent process names.
func launchedByDoubleClick() bool {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	if proc.Find() != nil {
		return false
	}
	var ids [2]uint32
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))

	return n == 1
}

// openURL opens url in the default browser.
//
// rundll32 with url.dll's FileProtocolHandler rather than "cmd /c start", because start treats an & in a URL as a command
// separator and opens a console window of its own.
func openURL(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// holdWindowOpen keeps a double-clicked console open after a failure, because Windows closes it the moment the process
// exits and the error would vanish unread.
func holdWindowOpen() {
	print("\nPress Enter to close this window.")
	var b [1]byte
	_, _ = syscall.Read(syscall.Stdin, b[:])
}
