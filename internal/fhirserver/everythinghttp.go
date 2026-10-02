package fhirserver

import (
	"errors"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// handleEverything serves Patient/{id}/$everything.
//
// GET and POST both, because the specification defines the operation on both and a client choosing POST to keep a patient identifier
// out of a URL - and out of every proxy log between here and there - is doing the right thing.
func (s *Server) handleEverything(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid",
			"$everything needs a patient: call it as Patient/{id}/$everything")

		return
	}

	// The access decision comes before the read, so the wrong patient gets a plain 403 rather than an empty bundle.
	//
	// An empty result would be the safe outcome but the wrong message: a caller cannot tell "you may not see this" from "this
	// person has no record", and the second invites them to retry, escalate, or conclude the data was lost.
	caller := CallerFrom(r.Context())
	if caller != nil && caller.Patient != "" && caller.Patient != id {
		s.writeOutcome(w, r, http.StatusForbidden, "error", "forbidden",
			"this token is limited to one patient, and that is not the patient whose record was requested")

		return
	}

	if err := r.ParseForm(); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid",
			"the query string could not be read: "+err.Error())

		return
	}

	req, err := ParseEverything(id, r.Form)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())

		return
	}

	result, err := s.Store.Everything(r.Context(), req, caller)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found",
				"there is no Patient with id "+id)

			return
		}

		// Over the ceiling is the caller's problem to narrow, not a server fault, so it is a 4xx and says how.
		s.writeOutcome(w, r, http.StatusRequestEntityTooLarge, "error", "too-costly", err.Error())

		return
	}

	bundle := s.Store.SearchBundle(result, s.BaseURL, "Patient", "")
	bundle.SetResourceID("everything")

	// The self link names the operation rather than a plain Patient search, because a bundle that says it came from
	// /Patient cannot be replayed to get the same thing back.
	bundle.Link = []fhir.BundleLink{{
		Relation: "self",
		URL:      s.BaseURL + "/Patient/" + id + "/$everything",
	}}

	s.writeResource(w, r, http.StatusOK, bundle)
}
