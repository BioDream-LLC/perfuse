package api

// The reviewer queue for Da Vinci PAS: the prior authorization requests waiting for a person, each read whole, and the
// decision made on it. Decisions go through the FHIR server's own $decide rules, so a decision made here and one made over
// FHIR cannot differ, and the provider's PAS subscription hears about both.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/store"
)

func (s *Server) pasOn(w http.ResponseWriter, r *http.Request) bool {
	if !s.PAS.PASEnabled() {
		s.fail(w, r, http.StatusNotFound, "Da Vinci PAS is not enabled on this server; start it with -fhir -pas")
		return false
	}
	return true
}

func (s *Server) handlePASCases(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.pasOn(w, r) {
		return
	}
	cases, err := s.PAS.Cases(r.Context(), r.URL.Query().Get("all") == "", 200)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if cases == nil {
		cases = []fhirserver.PASCase{}
	}
	s.ok(w, map[string]any{"cases": cases})
}

func (s *Server) handlePASCase(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.pasOn(w, r) {
		return
	}
	id := r.PathValue("id")
	c, err := s.PAS.Case(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.fail(w, r, http.StatusNotFound, "no prior authorization request "+id)
		return
	}
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	// Who read a member's request is asked about afterwards, so it is recorded.
	_ = s.storeFor(sess).Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "pas.read", Target: id, IP: clientIP(r)})
	s.ok(w, c)
}

func (s *Server) handlePASDecide(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.pasOn(w, r) {
		return
	}
	var rv fhirserver.PASReview
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&rv); err != nil {
		s.fail(w, r, http.StatusBadRequest, "the body is not a decision: "+err.Error())
		return
	}
	id := r.PathValue("id")
	out, err := s.PAS.Review(r.Context(), id, rv)
	var bad fhirserver.ErrBadReview
	switch {
	case errors.As(err, &bad):
		s.fail(w, r, http.StatusBadRequest, bad.Error())
		return
	case errors.Is(err, sql.ErrNoRows):
		s.fail(w, r, http.StatusNotFound, "no prior authorization request "+id)
		return
	case errors.Is(err, fhirserver.ErrNotPended):
		s.fail(w, r, http.StatusConflict, "nothing in this request is pended: it has already been decided")
		return
	case err != nil:
		s.failErr(w, r, err)
		return
	}
	_ = s.storeFor(sess).Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "pas.decide", Target: id,
		Detail: fmt.Sprintf("%s %v", rv.Decision, rv.Items), IP: clientIP(r)})
	s.ok(w, map[string]any{"response": out})
}
