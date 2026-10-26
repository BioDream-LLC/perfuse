package main

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A connection closed before its handshake (a load balancer's probe) must not be logged as a warning; a real handshake
// failure must still be, through the same log as everything else.
func TestHTTPErrorsGoToTheLog(t *testing.T) {
	buf := &lockedBuffer{}
	l := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ErrorLog = httpErrorLog(l)
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")

	// The probe: open, close.
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	// A real failure: a client offering only TLS 1.0, which the server refuses.
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS10, MinVersion: tls.VersionTLS10})
	if err == nil {
		c.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "level=WARN") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	out := buf.String()
	if strings.Contains(out, ": EOF") {
		t.Errorf("a probe's closed connection was logged above debug:\n%s", out)
	}
	if !strings.Contains(out, `level=WARN msg="http server"`) || !strings.Contains(out, "TLS handshake error") {
		t.Errorf("the refused TLS 1.0 handshake was not logged as a warning:\n%s", out)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
