package api

import (
	"net/http"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// handleRestart restarts the server from the console, so a setting that takes effect at a restart can be applied without
// anybody reaching the machine.
//
// The answer goes out before anything stops: the request that asked would otherwise be cut off by the shutdown it caused
// and report a failure for a restart that worked. Channels then finish the message in flight, exactly as they do when the
// service is stopped, and the server comes back with the same program and arguments.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Restart == nil {
		s.fail(w, r, http.StatusNotImplemented, "this server cannot restart itself; restart the service where it runs")
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "server.restart", IP: clientIP(r)})
	s.writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "pending": s.pendingRestartSettings()})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	restart := s.Restart
	time.AfterFunc(300*time.Millisecond, restart)
}
