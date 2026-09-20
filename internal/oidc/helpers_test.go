package oidc

import (
	"net"
	"strconv"
	"testing"
)

// freePort asks the kernel for an unused port.
func freePort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()

	return strconv.Itoa(addr.Port)
}
