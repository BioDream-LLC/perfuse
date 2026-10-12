package api

import (
	"net/http"
	"testing"
	"time"
)

func TestRestartFromTheConsole(t *testing.T) {
	h := newHarness(t)

	if rec := h.do("admin", http.MethodPost, "/api/server/restart", nil); rec.Code != http.StatusNotImplemented {
		t.Errorf("a server that cannot restart itself answered %d, want 501", rec.Code)
	}

	asked := make(chan struct{}, 1)
	h.server.Restart = func() { asked <- struct{}{} }
	if rec := h.do("editor", http.MethodPost, "/api/server/restart", nil); rec.Code != http.StatusForbidden {
		t.Errorf("an editor restarted the server: %d", rec.Code)
	}
	rec := h.do("admin", http.MethodPost, "/api/server/restart", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart answered %d: %s", rec.Code, rec.Body)
	}
	select {
	case <-asked:
	case <-time.After(3 * time.Second):
		t.Fatal("the restart was answered but never asked for")
	}
}
