package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/biodream-llc/perfuse/internal/identity"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Finding a message by an identifier, whatever format it arrived in.
//
// POST rather than GET, for the same reason the expression search is a POST and one more. A
// patient's MRN or name in a query string is written into every proxy log and browser history
// between here and the server, and those are not places anybody has decided may hold patient
// identifiers. A body is not a secret either, but it is not logged by default.
//
// Viewer, matching the expression search. It returns no payloads and reads only messages a
// viewer can already open one at a time.
type identitySearchRequest struct {
	// Term is what the person has: an MRN, a name, a date of birth, an accession.
	Term string `json:"term"`

	// Kind narrows to one sort of identifier. Empty searches all of them.
	Kind string `json:"kind,omitempty"`

	Channel string `json:"channel,omitempty"`

	// Since and Until are RFC 3339, or empty.
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`

	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// identitySearchResponse is what came back.
type identitySearchResponse struct {
	// Matches is never nil, so a client doing .length on it does not crash on the search that
	// found nothing - which is the common case and the one nobody tests by hand.
	Matches []identityMatchJSON `json:"matches"`
	Total   int                 `json:"total"`

	// Indexed reports whether identity indexing is switched on at all.
	//
	// Carried on every response because without it an empty result is ambiguous between "no
	// message mentions that patient" and "nothing has ever been indexed here", and those lead
	// somebody to opposite conclusions. The console shows a different message for each.
	Indexed bool `json:"indexed"`

	// Kinds lists what can be searched, so the console builds its filter from the server
	// rather than from a copy of the list that drifts.
	Kinds []string `json:"kinds"`
}

type identityMatchJSON struct {
	Message msgstore.Message `json:"message"`

	// MatchedKind and MatchedValue say why this message is in the list. A page of otherwise
	// identical ADT messages gives no clue otherwise, and "it matched the account number, not
	// the MRN" changes what somebody does next.
	MatchedKind  string `json:"matchedKind"`
	MatchedValue string `json:"matchedValue"`
}

func (s *Server) handleIdentitySearch(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}
	if !s.requireMessages(w, r) {
		return
	}

	var req identitySearchRequest
	if !s.decode(w, r, &req) {
		return
	}

	// Validated against the list rather than passed through. An unknown kind would otherwise
	// match nothing and read as "this patient has no messages", which is a clinical conclusion
	// drawn from a typo in a field name.
	if req.Kind != "" {
		known := false
		for _, k := range msgstore.IdentityKinds() {
			if string(k) == req.Kind {
				known = true
				break
			}
		}
		if !known {
			s.fail(w, r, http.StatusBadRequest,
				"kind must be one of "+kindList()+", or empty to search all of them")
			return
		}
	}

	q := msgstore.IdentitySearch{
		Term:    req.Term,
		Kind:    identity.Kind(req.Kind),
		Channel: req.Channel,
		Limit:   req.Limit,
		Offset:  req.Offset,
	}

	// A malformed timestamp is refused rather than ignored, because silently dropping a bound
	// widens the search and the result would be read as covering the range that was asked for.
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

	res, err := rt.Messages.SearchByIdentity(r.Context(), q)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Recorded before the response is written, and recorded whatever the outcome.
	//
	// This is a search for a person by name or by medical record number, which is the access
	// an audit asks about by name: who looked up whom, and when. A log entry saying only that
	// somebody ran a patient search cannot answer that, so the term is written down - which
	// means the audit trail itself holds patient identifiers, deliberately and by the same
	// reasoning that requires the trail to exist.
	//
	// A search that found nothing is recorded too. Somebody working through a list of names to
	// see which ones this server has heard of is doing exactly what an audit is looking for,
	// and recording only the successful attempts would hide it.
	s.auditIdentitySearch(r, sess, q, res.Total)

	out := identitySearchResponse{
		Matches: make([]identityMatchJSON, 0, len(res.Matches)),
		Total:   res.Total,
		Indexed: res.Indexed,
		Kinds:   kindStrings(),
	}
	for _, m := range res.Matches {
		out.Matches = append(out.Matches, identityMatchJSON{
			Message:      m.Message,
			MatchedKind:  string(m.MatchedKind),
			MatchedValue: m.MatchedValue,
		})
	}
	s.ok(w, out)
}

// auditIdentitySearch records who searched for which patient.
func (s *Server) auditIdentitySearch(r *http.Request, sess *store.Session, q msgstore.IdentitySearch, found int) {
	kind := q.Kind
	if kind == "" {
		kind = "any"
	}
	detail := "kind=" + string(kind) + " found=" + strconv.Itoa(found)
	if q.Channel != "" {
		detail += " channel=" + q.Channel
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "message.identity.search",

		// The term goes in Target, which is the field that answers "what was accessed".
		Target: q.Term,
		Detail: detail,
		IP:     clientIP(r),
	})
}

// kindStrings is the searchable kinds as JSON strings.
func kindStrings() []string {
	kinds := msgstore.IdentityKinds()
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}

// kindList renders the kinds for an error message.
func kindList() string {
	out := ""
	for i, k := range kindStrings() {
		switch {
		case i == 0:
			out = k
		default:
			out += ", " + k
		}
	}
	return out
}
