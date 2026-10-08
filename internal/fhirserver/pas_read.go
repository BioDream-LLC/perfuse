package fhirserver

// Reading a PAS answer by id. A PAS Subscription with id-only (or empty) content tells the provider which response changed and
// not what it says: the notification's focus is Bundle/<id>, and the provider fetches it with its own credentials. PAS answers
// live in pas_responses rather than the FHIR store, so the ordinary read would say they do not exist; these reads find them.
//
//	GET /Bundle/<id>                     the latest PAS Response Bundle
//	GET /Bundle/<id>/_history/<version>  the Bundle as that notification sent it
//	GET /ClaimResponse/<id>              the ClaimResponse inside the latest Bundle
//
// A token bound to one patient (a SMART patient launch) is not how a provider system asks a payer about its requests, and a PAS
// Bundle names its member in its own Patient entry rather than as a store Patient, so such a token is told nothing exists,
// exactly as for any other record outside its patient.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// pasRead answers a read of a PAS response, reporting false when the request is not one (PAS is off, the type is not Bundle
// or ClaimResponse, or no PAS response has that id), so the ordinary read carries on.
func (s *Server) pasRead(w http.ResponseWriter, r *http.Request, resourceType, id string, version int) bool {
	if s.PAS == nil || (resourceType != "Bundle" && resourceType != "ClaimResponse") || id == "" {
		return false
	}
	if version > 0 && resourceType != "Bundle" {
		return false
	}
	if err := s.pasReady(r.Context()); err != nil {
		return false
	}
	bundle, current, err := s.pasResponse(r.Context(), id, version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if version > 0 && current > 0 {
			s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
				fmt.Sprintf("%s/%s has no version %d", resourceType, id, version))
			return true
		}
		return false
	case err != nil:
		s.internalError(w, r, err)
		return true
	}
	if caller := CallerFrom(r.Context()); caller != nil && caller.Patient != "" {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			fmt.Sprintf("%s/%s does not exist", resourceType, id))
		return true
	}
	if version == 0 {
		version = current
	}
	out := bundle
	if resourceType == "ClaimResponse" {
		out = nil
		for _, e := range asSliceAny(bundle["entry"]) {
			if res := asMapAny(asMapAny(e)["resource"]); res["resourceType"] == "ClaimResponse" {
				out = res
				break
			}
		}
		if out == nil {
			return false
		}
	}
	w.Header().Set("ETag", ETag(version))
	s.writeJSON(w, http.StatusOK, out)
	return true
}

// pasResponse loads one version of a PAS response Bundle (0 for the latest) and says which version is current.
func (s *Server) pasResponse(ctx context.Context, id string, version int) (map[string]any, int, error) {
	var current int
	if err := s.Store.db.QueryRowContext(ctx, `SELECT version FROM pas_requests WHERE id = ?`, id).Scan(&current); err != nil {
		return nil, 0, err
	}
	if version == 0 {
		version = current
	}
	var raw string
	if err := s.Store.db.QueryRowContext(ctx, `SELECT response FROM pas_responses WHERE id = ? AND version = ?`, id, version).
		Scan(&raw); err != nil {
		return nil, current, err
	}
	var bundle map[string]any
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, current, err
	}
	return bundle, current, nil
}

// pasVersion reads a version path value for pasRead; anything that is not a whole number from 1 is left to the ordinary read,
// which explains the mistake.
func pasVersionOf(raw string) int {
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return -1
	}
	return v
}
