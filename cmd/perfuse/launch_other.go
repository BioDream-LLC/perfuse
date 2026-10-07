//go:build !windows

package main

import "errors"

// launchedByDoubleClick is Windows-only. On macOS and Linux a release binary is run from a terminal, where perfuse with no
// arguments means "show me the usage", and there is no reliable way to tell a file manager's launch from a typed one.
func launchedByDoubleClick() bool { return false }

func openURL(string) error { return errors.New("opening a browser is only done on Windows") }

func holdWindowOpen() {}
