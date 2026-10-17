package fhirserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// PatientMatcher answers Patient/$match from a patient index.
type PatientMatcher interface {
	// Match returns the people who could be the patient described, best first, each as a Patient resource with a score
	// (0 to 1) and a grade: certain, probable or possible.
	Match(ctx context.Context, patient json.RawMessage, count int) ([]PatientMatch, error)
}

// PatientMatch is one candidate.
type PatientMatch struct {
	Patient map[string]any
	Score   float64
	Grade   string
}

const matchGradeURL = "http://hl7.org/fhir/StructureDefinition/match-grade"

// registerMatch adds Patient/$match (FHIR's operation, as IHE PDQm ITI-119 and MPI clients use it).
func (s *Server) registerMatch(mux *http.ServeMux) {
	mux.HandleFunc("POST /Patient/$match", s.handlePatientMatch)
}

func (s *Server) handlePatientMatch(w http.ResponseWriter, r *http.Request) {
	if s.Match == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server keeps no patient index (serve -mpi), so there is nothing to match against")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", "the request could not be read")
		return
	}
	var params struct {
		ResourceType string `json:"resourceType"`
		Parameter    []struct {
			Name         string          `json:"name"`
			Resource     json.RawMessage `json:"resource"`
			ValueBoolean *bool           `json:"valueBoolean"`
			ValueInteger *int            `json:"valueInteger"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal(body, &params); err != nil || params.ResourceType != "Parameters" {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", "$match takes a Parameters resource with a resource parameter holding a Patient")
		return
	}
	var patient json.RawMessage
	onlyCertain, count := false, 10
	for _, p := range params.Parameter {
		switch p.Name {
		case "resource":
			patient = p.Resource
		case "onlyCertainMatches":
			if p.ValueBoolean != nil {
				onlyCertain = *p.ValueBoolean
			}
		case "count":
			if p.ValueInteger != nil && *p.ValueInteger > 0 {
				count = *p.ValueInteger
			}
		}
	}
	if len(patient) == 0 {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "required", "$match needs the resource parameter: the Patient to match")
		return
	}
	matches, err := s.Match.Match(r.Context(), patient, count)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())
		return
	}
	entries := []any{}
	for _, m := range matches {
		if onlyCertain && m.Grade != "certain" {
			continue
		}
		entries = append(entries, map[string]any{
			"fullUrl":  s.entryURL(m.Patient),
			"resource": m.Patient,
			"search": map[string]any{"mode": "match", "score": m.Score,
				"extension": []any{map[string]any{"url": matchGradeURL, "valueCode": m.Grade}}},
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resourceType": "Bundle", "type": "searchset", "total": len(entries), "entry": entries})
}
