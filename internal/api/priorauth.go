package api

import (
	"net/http"
	"strconv"

	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/priorauth"
	"github.com/biodream-llc/perfuse/internal/store"
)

// priorAuthView is what the prior authorization screen shows: the requests this tenant's PAS destinations submitted,
// newest first, with the payer's latest answer to each.
type priorAuthView struct {
	// On is false when the server keeps no record (it was started without one).
	On       bool                `json:"on"`
	Requests []priorauth.Tracked `json:"requests"`
	Pending  int                 `json:"pending"`
	Approved int                 `json:"approved"`
	Denied   int                 `json:"denied"`
}

// handlePriorAuths is GET /api/prior-auth?limit=N.
func (s *Server) handlePriorAuths(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	out := priorAuthView{Requests: []priorauth.Tracked{}}
	if engine.PriorAuths == nil {
		s.ok(w, out)
		return
	}
	out.On = true
	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	// From the session, never from the request. On a single-tenant server requests carry no tenant.
	tid := ""
	if s.Runtimes != nil {
		tid = string(sess.TenantID)
	}
	all := engine.PriorAuths.List(tid, 0)
	for _, t := range all {
		switch {
		case t.Response == nil || t.Response.Decision == priorauth.DecisionPended:
			out.Pending++
		case t.Response.Decision == priorauth.DecisionDenied:
			out.Denied++
		case t.Response.Decision == priorauth.DecisionApproved || t.Response.Decision == priorauth.DecisionPartial ||
			t.Response.Decision == priorauth.DecisionModified:
			out.Approved++
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	out.Requests = all
	s.ok(w, out)
}
