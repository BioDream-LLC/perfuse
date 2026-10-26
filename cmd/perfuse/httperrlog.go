package main

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"strings"
)

// httpErrorLog gives net/http's own error lines to Perfuse's log instead of stderr, where they bypassed the log's format.
//
// One kind is not a problem at all: a connection opened and closed before a TLS handshake ("TLS handshake error from
// ADDR: EOF"). Network load balancers, Kubernetes TCP probes and port scanners all do it, and behind an AWS NLB it was two
// lines a second. Those go to debug; every other line (a real handshake failure: an old TLS version, a bad certificate)
// stays a warning.
func httpErrorLog(l *slog.Logger) *log.Logger {
	return log.New(httpErrWriter{l}, "", 0)
}

type httpErrWriter struct{ l *slog.Logger }

func (w httpErrWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(bytes.TrimPrefix(p, []byte("http: "))))
	level := slog.LevelWarn
	if strings.HasPrefix(msg, "TLS handshake error from ") && (strings.HasSuffix(msg, ": EOF") ||
		strings.HasSuffix(msg, "connection reset by peer")) {
		level = slog.LevelDebug
	}
	w.l.Log(context.Background(), level, "http server", "error", msg)
	return len(p), nil
}
