package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/store"
)

// handleAlerts returns what is wrong now and what recently recovered.
//
// Both, because "why did the overnight batch look odd" is usually answered by an
// alert that fired at three and cleared at half past, and an interface that only
// shows current state cannot answer it at all.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Alerts == nil {
		s.fail(w, r, http.StatusNotImplemented, "alerting is not enabled on this server")
		return
	}

	firing := s.Alerts.Firing()
	if firing == nil {
		firing = []alerts.Alert{}
	}
	resolved := s.Alerts.Resolved()
	if resolved == nil {
		resolved = []alerts.Alert{}
	}

	critical, warning := 0, 0
	for _, a := range firing {
		if a.Severity == alerts.Critical {
			critical++
			continue
		}
		warning++
	}

	s.ok(w, map[string]any{
		"firing":   firing,
		"resolved": resolved,
		"critical": critical,
		"warning":  warning,
		"rules":    s.Alerts.Rules(),
		// Reported so the interface can say "nothing wrong" with authority rather
		// than leaving an empty list ambiguous between healthy and not checked.
		"enabled": true,
	})
}

// handleAcknowledgeAlert suppresses notification for one alert without hiding it.
func (s *Server) handleAcknowledgeAlert(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Alerts == nil {
		s.fail(w, r, http.StatusNotImplemented, "alerting is not enabled")
		return
	}

	var body struct {
		Key string `json:"key"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Key == "" {
		s.fail(w, r, http.StatusBadRequest, "name the alert to acknowledge")
		return
	}

	if !s.Alerts.Acknowledge(body.Key, sess.Username) {
		s.fail(w, r, http.StatusNotFound,
			"that alert is not currently firing; it may have resolved itself")
		return
	}

	// Recorded, because acknowledging an alert is a decision that something can
	// wait, and the next person needs to know who made it.
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "alert.acknowledge",
		Target:   body.Key,
		Detail:   "notification suppressed while the condition persists",
		IP:       clientIP(r),
	})

	s.ok(w, map[string]string{"status": "acknowledged"})
}
