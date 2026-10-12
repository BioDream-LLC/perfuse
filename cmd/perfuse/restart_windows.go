//go:build windows

package main

// replaceProcess does nothing on Windows: serveUntilDone runs serve again in this process.
func replaceProcess() error { return nil }
