package fhirserver

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Patient/$everything: the whole record for one person.
//
// This is how a patient app, or a clinician's viewer, or a records request gets a person's chart in one call rather than by knowing
// which resource types to ask for. $export already existed for bulk work across a population; this is the single-patient case, and the
// two answer different questions.

// maxEverythingResources bounds one response.
//
// Over the ceiling the operation refuses and says how to narrow, rather than returning the first slice quietly.
//
// Refusing rather than truncating, for the same reason _include does: a truncated record is indistinguishable from a complete one, and
// this is the call somebody makes when they want the whole chart. A clinician who believes they are looking at a complete record and is
// not is worse off than one who got an error, because the error can be acted on and the silence cannot. Somebody deciding on a
// medication from a list that stopped early is the case that matters.
const maxEverythingResources = 5000

// everythingParams are the parameters this operation accepts.
//
// Anything else is refused. That is the standing rule for search parameters here, and it matters more than usual on this operation:
// silently ignoring a filter returns more of a person's record than was asked for. Ignoring an unrecognised _since or start would hand
// back the entire chart to a caller who asked for one week of it.
var everythingParams = map[string]bool{
	"_type":  true,
	"_since": true,
	"_count": true,
}

// EverythingRequest is a parsed $everything call.
type EverythingRequest struct {
	// PatientID is whose record is wanted.
	PatientID string

	// Types restricts the result, from _type. Empty means every type in the compartment.
	Types []string

	// Since restricts to resources changed at or after this instant, from _since.
	Since string

	// Count bounds the response. Zero uses the ceiling.
	Count int
}

// ParseEverything reads the query parameters for $everything.
//
// start and end are named in the specification and are not implemented here. They are refused explicitly rather than ignored, because
// a caller who asks for a date range and receives the whole record has been given more than they asked for and has no way to know.
func ParseEverything(patientID string, values map[string][]string) (*EverythingRequest, error) {
	req := &EverythingRequest{PatientID: patientID}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		switch {
		case everythingParams[name]:
		case name == "start" || name == "end":
			return nil, fmt.Errorf("%s is part of the $everything specification but is not implemented by this server, "+
				"so it has been refused rather than ignored: ignoring it would return the whole record to a caller who "+
				"asked for part of it. Use _since to restrict by when a resource last changed", name)
		default:
			return nil, fmt.Errorf("%s is not a parameter of $everything: the supported ones are _type, _since and _count", name)
		}
	}

	for _, raw := range values["_type"] {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if !inCompartment(t) {
				return nil, fmt.Errorf("%s is not a type this server holds in the patient compartment: %s",
					t, strings.Join(CompartmentTypes(), ", "))
			}
			req.Types = append(req.Types, t)
		}
	}

	if vals := values["_since"]; len(vals) > 0 {
		if len(vals) > 1 {
			return nil, fmt.Errorf("_since was given more than once, and two lower bounds cannot both be meant")
		}
		if _, err := time.Parse(time.RFC3339, vals[0]); err != nil {
			return nil, fmt.Errorf("_since must be an instant such as 2026-08-23T09:00:00Z, and %q is not one", vals[0])
		}
		req.Since = vals[0]
	}

	if vals := values["_count"]; len(vals) > 0 {
		n, err := strconv.Atoi(vals[0])
		if err != nil || n < 0 {
			return nil, fmt.Errorf("_count must be a non-negative number, got %q", vals[0])
		}
		req.Count = n
	}

	return req, nil
}

// CompartmentTypes returns the resource types this server can attribute to a patient, sorted.
//
// Derived from the search parameter registry rather than written out again, so a type added to search cannot be silently left out of a
// patient's record here. Two lists and one of them updated is how a records request quietly comes back incomplete.
//
// Sorted because it is user-visible, and because Go maps range randomly.
func CompartmentTypes() []string {
	out := []string{"Patient"}

	for resourceType := range SearchParams {
		if resourceType == "Patient" {
			continue
		}
		if hasPatientContext(resourceType) {
			out = append(out, resourceType)
		}
	}
	sort.Strings(out)

	return out
}

// containsType reports whether a type is named in a list.
func containsType(types []string, want string) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}

	return false
}

// inCompartment reports whether a type can belong to a patient's record.
func inCompartment(resourceType string) bool {
	for _, t := range CompartmentTypes() {
		if t == resourceType {
			return true
		}
	}

	return false
}

// patientParamFor returns the parameter naming the patient on a type.
//
// Both spellings exist because FHIR defines patient as a narrower alias of subject, and a given resource type may index either. Picking
// the one the type actually supports means the query matches what was indexed instead of returning nothing.
func patientParamFor(resourceType string) string {
	params := SearchParams[resourceType]

	for _, want := range patientContextParams {
		for _, p := range params {
			if p == want {
				return want
			}
		}
	}

	return ""
}

// Everything gathers one patient's record.
//
// The Patient itself is included, because a record without the person it belongs to is not much of a record.
//
// Access control is the caller's responsibility before this is invoked, and is also applied here to every resource gathered. Both,
// deliberately: the outer check is the one that returns a clear 403 for the wrong patient, and this one is what stops a resource whose
// subject disagrees with the search index from leaving the building. They use the same permitsResource as every other read, so there is
// one definition of who may see what.
func (s *Store) Everything(ctx context.Context, req *EverythingRequest, caller *Caller) (*SearchResult, error) {
	patient, err := s.Get(ctx, "Patient", req.PatientID)
	if err != nil {
		return nil, err
	}

	types := req.Types
	if len(types) == 0 {
		types = CompartmentTypes()
	}

	ceiling := maxEverythingResources
	if req.Count > 0 && req.Count < ceiling {
		ceiling = req.Count
	}

	result := &SearchResult{}
	seen := map[string]bool{}

	add := func(r fhir.Resource) error {
		key := r.ResourceTypeName() + "/" + r.ResourceID()
		if seen[key] {
			return nil
		}

		// The same check every other read goes through. A resource that a patient-scoped caller may not see is not
		// included, even though the search that found it was already narrowed - because agreeing with itself is not the
		// same as being right, and this is the boundary that matters.
		if !permitsResource(caller, r) {
			return nil
		}

		if len(result.Resources) >= ceiling {
			return fmt.Errorf("this patient's record holds more than %d resources, which is more than one response "+
				"should carry. Narrow it with _type or _since, or use $export for the whole record: returning the "+
				"first %d would look exactly like a complete record and cannot be told apart from one",
				ceiling, ceiling)
		}

		seen[key] = true
		result.Resources = append(result.Resources, r)

		return nil
	}

	// The patient first, so the record reads in the order somebody would want it - but only when the caller's _type asks for it.
	//
	// Including it regardless would be friendlier and would also mean _type did not do what it says. Handing back a type that was
	// not requested is the same fault as dropping a filter that was: in both cases the response does not match the question, and the
	// caller has no way to know. A client that wants the patient names Patient in _type.
	if len(req.Types) == 0 || containsType(req.Types, "Patient") {
		if err := add(patient); err != nil {
			return nil, err
		}
	}

	for _, resourceType := range types {
		if resourceType == "Patient" {
			continue
		}

		param := patientParamFor(resourceType)
		if param == "" {
			// Should be unreachable, since CompartmentTypes is derived from the same registry. Refused rather than
			// skipped: silently omitting a type is how a record comes back short.
			return nil, fmt.Errorf("%s is in the patient compartment but has no patient or subject parameter, "+
				"so this server cannot tell which of its resources belong to a patient", resourceType)
		}

		q := &SearchQuery{
			ResourceType: resourceType,
			Criteria:     map[string][]string{param: {req.PatientID}},
			Count:        ceiling + 1,
		}
		if req.Since != "" {
			q.Criteria["_lastUpdated"] = []string{"ge" + req.Since}
		}

		found, err := s.Search(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("reading %s for this patient: %w", resourceType, err)
		}

		for _, r := range found.Resources {
			if err := add(r); err != nil {
				return nil, err
			}
		}
	}

	result.Total = len(result.Resources)
	result.Count = len(result.Resources)

	return result, nil
}
