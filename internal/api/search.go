package api

import (
	"net/http"
	"time"

	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Searching stored traffic by content.
//
// POST /api/messages/search rather than a GET with a query string. The body carries an expression
// that may contain quotes, brackets and comparison operators, and while all of that can be
// percent-encoded, an expression somebody is iterating on belongs in a body where it is not being
// mangled by intermediaries or truncated in a proxy log.
//
// Viewer. It reads stored messages, which a viewer can already do one at a time, and returns no
// payloads. It compiles an expression but does not run anything configurable - the expression is
// evaluated against messages, not used to change a channel.

type searchRequest struct {
	// Where is the expression, in the same language channel filters use.
	Where string `json:"where"`

	Channel     string `json:"channel,omitempty"`
	MessageType string `json:"messageType,omitempty"`
	Outcome     string `json:"outcome,omitempty"`

	// Since and Until are RFC 3339, or empty.
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`

	// Examine bounds how many messages are parsed; Limit bounds how many matches come back.
	Examine int `json:"examine,omitempty"`
	Limit   int `json:"limit,omitempty"`
}

func (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}

	var req searchRequest
	if !s.decode(w, r, &req) {
		return
	}

	q := msgstore.ExpressionSearch{
		Where:       req.Where,
		Channel:     req.Channel,
		MessageType: req.MessageType,
		Outcome:     msgstore.Outcome(req.Outcome),
		Examine:     req.Examine,
		Limit:       req.Limit,
	}

	// A malformed timestamp is refused rather than ignored. Silently dropping a time bound would
	// widen the search without saying so, and somebody would read the result as covering the range
	// they asked for.
	if req.Since != "" {
		t, err := time.Parse(time.RFC3339, req.Since)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest,
				"since is not a valid timestamp; use RFC 3339, for example 2026-08-19T14:00:00Z")
			return
		}
		q.Since = t
	}
	if req.Until != "" {
		t, err := time.Parse(time.RFC3339, req.Until)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest,
				"until is not a valid timestamp; use RFC 3339, for example 2026-08-19T18:00:00Z")
			return
		}
		q.Until = t
	}

	res, err := rt.Messages.SearchByExpression(r.Context(), q)
	if err != nil {
		// A bad expression is the user's input, not a server fault, and the message from the
		// parser is the most useful thing anybody could be told at this point.
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	s.ok(w, res)
}
