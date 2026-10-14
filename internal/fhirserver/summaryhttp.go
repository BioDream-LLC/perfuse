package fhirserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

// PatientSummary runs $summary for the console, which shares a summary through a SMART Health Link. The console has
// already checked the person's role; the FHIR server's own token checks do not apply to a call that is not over HTTP.
func (s *Server) PatientSummary(ctx context.Context, patientID string) (int, []byte) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Patient/"+url.PathEscape(patientID)+"/$summary", nil).WithContext(ctx)
	req.SetPathValue("id", patientID)
	s.handleSummary(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// handleSummary serves Patient/{id}/$summary and Patient/$summary?identifier=, the IPS guide's operation.
//
// The same access rule as $everything, checked before anything is read: a token limited to one patient gets a 403 for
// another, never an empty summary, because an empty summary reads as "nothing known" and that is the dangerous answer.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", "the query string could not be read: "+err.Error())
		return
	}
	for name := range r.Form {
		switch name {
		case "identifier", "profile", "_format":
		default:
			s.writeOutcome(w, r, http.StatusBadRequest, "error", "not-supported",
				name+" is not a parameter of $summary here; the supported ones are identifier and profile")
			return
		}
	}
	if p := r.Form.Get("profile"); p != "" && p != ipsProfile {
		// The IPS is the only summary this server builds; answering another profile's request with an IPS would hand
		// the caller a document it did not ask for and may not be able to read.
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "not-supported",
			"this server builds the International Patient Summary ("+ipsProfile+") and no other summary profile")
		return
	}

	caller := CallerFrom(r.Context())
	id := r.PathValue("id")
	if id == "" {
		ident := r.Form.Get("identifier")
		if ident == "" {
			s.writeOutcome(w, r, http.StatusBadRequest, "error", "required",
				"$summary needs a patient: Patient/{id}/$summary, or Patient/$summary?identifier=system|value")
			return
		}
		found, err := s.Store.Search(r.Context(), &SearchQuery{ResourceType: "Patient",
			Criteria: map[string][]string{"identifier": {ident}}, Count: 2})
		if err != nil {
			s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())
			return
		}
		var visible []string
		for _, p := range found.Resources {
			if permitsResource(caller, p) {
				visible = append(visible, p.ResourceID())
			}
		}
		switch len(visible) {
		case 0:
			s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", "no Patient has the identifier "+ident)
			return
		case 1:
			id = visible[0]
		default:
			// Refused rather than picking one: a summary of the wrong person is the worst thing this could return.
			s.writeOutcome(w, r, http.StatusConflict, "error", "multiple-matches",
				"more than one Patient has the identifier "+ident+"; ask by id instead")
			return
		}
	}

	if caller != nil && caller.Patient != "" && caller.Patient != id {
		s.writeOutcome(w, r, http.StatusForbidden, "error", "forbidden",
			"this token is limited to one patient, and that is not the patient whose summary was requested")
		return
	}

	bundle, err := s.Store.Summary(r.Context(), id, s.BaseURL, caller, time.Now())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", "there is no Patient with id "+id)
			return
		}
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "more than") {
			status = http.StatusRequestEntityTooLarge
		}
		s.writeOutcome(w, r, status, "error", "too-costly", err.Error())
		return
	}
	s.writeBundle(w, r, http.StatusOK, bundle)
}
