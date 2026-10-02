package fhirserver

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The HTTP surface for history, versioned reads, and optimistic concurrency.

// handleTypeHistory answers GET /{type}/_history.
func (s *Server) handleTypeHistory(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")

	if s.conceptMapIntercept(w, r, resourceType) || s.valueSetIntercept(w, r, resourceType) {
		return
	}

	if !supportedType(resourceType) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			fmt.Sprintf("resource type %q is not supported", resourceType))

		return
	}

	// The launch context is checked before anything is read, and a type-wide history is refused outright for a caller
	// restricted to one patient.
	//
	// Not filtered, refused. A history bundle names every resource that changed, so filtering it to one patient would
	// still leak the shape of the rest - a caller could tell how much activity it was not being shown by the gaps in the
	// version numbers. And a partial audit trail presented as an audit trail is worse than a refusal.
	if caller := CallerFrom(r.Context()); callerLimitedToOnePatient(caller) {
		s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden",
			"this token is limited to one patient, and a type-wide history describes every patient; "+
				"read the history of a single resource instead")

		return
	}

	limit := s.historyLimit(r)

	versions, err := s.Store.TypeHistory(r.Context(), resourceType, limit)
	if err != nil {
		s.internalError(w, r, err)

		return
	}

	self := fmt.Sprintf("%s/%s/_history", strings.TrimRight(s.BaseURL, "/"), resourceType)
	s.writeJSON(w, http.StatusOK, s.Store.HistoryBundle(versions, s.BaseURL, self))
}

// handleInstanceHistory answers GET /{type}/{id}/_history.
func (s *Server) handleInstanceHistory(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")

	if s.conceptMapIntercept(w, r, resourceType) || s.valueSetIntercept(w, r, resourceType) {
		return
	}
	id := r.PathValue("id")

	if !supportedType(resourceType) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			fmt.Sprintf("resource type %q is not supported", resourceType))

		return
	}

	limit := s.historyLimit(r)

	versions, err := s.Store.History(r.Context(), resourceType, id, limit)
	if errors.Is(err, ErrNotFound) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			fmt.Sprintf("%s/%s does not exist", resourceType, id))

		return
	}
	if err != nil {
		s.internalError(w, r, err)

		return
	}

	// Launch context checked against the resource rather than the history.
	//
	// The check needs a resource to look at, and a deleted one has none - so the most recent version that has content is
	// used. Without that, deleting a resource would make its history readable by anyone, which is the wrong direction for
	// a deletion to move a permission.
	if !s.historyPermitted(r, versions) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			fmt.Sprintf("%s/%s does not exist", resourceType, id))

		return
	}

	self := fmt.Sprintf("%s/%s/%s/_history", strings.TrimRight(s.BaseURL, "/"), resourceType, id)
	s.writeJSON(w, http.StatusOK, s.Store.HistoryBundle(versions, s.BaseURL, self))
}

// handleVersionRead answers GET /{type}/{id}/_history/{vid}.
func (s *Server) handleVersionRead(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")
	id := r.PathValue("id")
	raw := r.PathValue("vid")

	if !supportedType(resourceType) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			fmt.Sprintf("resource type %q is not supported", resourceType))

		return
	}

	versionID, err := strconv.Atoi(raw)
	if err != nil || versionID < 1 {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid",
			fmt.Sprintf("%q is not a version identifier; these are whole numbers from 1", raw))

		return
	}

	resource, err := s.Store.GetVersion(r.Context(), resourceType, id, versionID)
	switch {
	case errors.Is(err, ErrNoSuchVersion):
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			fmt.Sprintf("%s/%s has no version %d", resourceType, id, versionID))

		return
	case errors.Is(err, ErrDeleted):
		// 410 for a version that recorded a deletion, matching what a current read of a deleted resource says. The
		// version exists and there is deliberately nothing in it.
		s.writeOutcome(w, r, http.StatusGone, fhir.SeverityError, "deleted",
			fmt.Sprintf("version %d of %s/%s is the deletion", versionID, resourceType, id))

		return
	case err != nil:
		s.internalError(w, r, err)

		return
	}

	// The same launch-context rule as a current read, and the same wording as a genuine miss.
	//
	// A versioned read would otherwise be a way straight past the context check: an app refused the current Patient could
	// ask for version 1 of it and be handed the record.
	if caller := CallerFrom(r.Context()); !permitsResource(caller, resource) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			fmt.Sprintf("%s/%s has no version %d", resourceType, id, versionID))

		return
	}

	w.Header().Set("ETag", ETag(versionID))
	s.writeJSON(w, http.StatusOK, resource)
}

// historyLimit reads _count from a history request.
func (s *Server) historyLimit(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("_count"))
	if raw == "" {
		return DefaultCount
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return DefaultCount
	}
	if n > MaxCount {
		n = MaxCount
	}

	return n
}

// historyPermitted says whether the caller may see this resource's history.
//
// Judged on the most recent version that has content. A deletion carries none, so judging on the newest version alone would let a deletion
// make a history world-readable - a permission moving the wrong way in response to a destructive act.
//
// A history with no readable content at all is refused. That is a resource whose every version this build cannot parse, and guessing that
// the caller may see it is the wrong way to fail.
func (s *Server) historyPermitted(r *http.Request, versions []Version) bool {
	caller := CallerFrom(r.Context())
	if !callerLimitedToOnePatient(caller) {
		return true
	}

	for _, v := range versions {
		if v.Resource == nil {
			continue
		}

		return permitsResource(caller, v.Resource)
	}

	return false
}

// callerLimitedToOnePatient says whether this token carries a launch context restricting it to one patient.
//
// A named helper rather than the field test inline, and not because it is long. hasPatientContext already exists and asks a different
// question entirely - whether a resource *type* can be narrowed by patient - and the two read identically at a call site. Writing this one
// out is what stops the wrong one being used where it compiles and passes.
func callerLimitedToOnePatient(caller *Caller) bool {
	return caller != nil && caller.Patient != ""
}
