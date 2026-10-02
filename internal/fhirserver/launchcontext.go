package fhirserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// SMART launch context.
//
// A patient-facing app is launched for one patient and given a token carrying that patient's id. The scopes say what kinds
// of resource it may touch; the context says whose. Enforcing the first and not the second gives an app scoped to
// patient/Observation.read the ability to read every observation in the hospital, which is the whole database.
//
// This was deliberately not implemented and the discovery document deliberately did not claim it, because telling an app it
// can rely on a context that is not enforced is worse than saying nothing - the app scopes itself to one patient, and
// receives everyone.

// patientContextParams are the search parameters that name the patient a resource belongs to.
//
// Both spellings, because FHIR defines patient as a narrower alias of subject and different clients send different ones. A
// server honouring only one would let a request through by using the other, which is the shape of most access-control bugs.
var patientContextParams = []string{"patient", "subject"}

// hasPatientContext reports whether a resource type can be narrowed to one patient.
//
// Read from the search parameters this server actually supports rather than a separate list, so a type added there cannot
// be silently left unprotected here. That is the failure this arrangement is designed against: two lists, one updated.
func hasPatientContext(resourceType string) bool {
	params, ok := SearchParams[resourceType]
	if !ok {
		return false
	}
	for _, p := range params {
		for _, want := range patientContextParams {
			if p == want {
				return true
			}
		}
	}

	return false
}

// refIDFromString pulls the logical id out of a reference written as a string.
//
// The store already has referenceID for a *fhir.Reference, and this is deliberately a second name rather than a second
// implementation of the same thing: search criteria arrive as strings, and giving them their own function keeps one rule in
// one place instead of two that can drift.
//
// A reference may be "Patient/123", a bare "123", or an absolute URL ending in the id. All three appear in real messages, and
// comparing the raw string would let "Patient/123" past a check looking for "123" - or the reverse, which fails open.
//
// Versioned and contained forms are handled because a comparison that mistakes a version number for an id fails in the
// direction that matters: /Patient/123/_history/4 would yield "4", which matches nobody, and a search narrowed to patient
// "4" returns an empty result that reads as "this patient has no records".
func refIDFromString(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}

	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.Index(ref, "/_history/"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}

	return ref
}

// subjectOf returns the patient id a resource belongs to, and whether one was found.
//
// Read from the marshalled JSON rather than by type switch over every resource struct. A type switch would need a case per
// resource type and would silently return "no subject" for one nobody had added - and "no subject" is the answer that lets a
// resource through, so the failure would be open rather than closed.
func subjectOf(r fhir.Resource) (string, bool) {
	if r == nil {
		return "", false
	}

	// A Patient is its own subject. Stated rather than left to the field lookup below, which would find nothing and
	// report no subject.
	if r.ResourceTypeName() == "Patient" {
		return r.ResourceID(), r.ResourceID() != ""
	}

	raw, err := json.Marshal(r)
	if err != nil {
		return "", false
	}

	var tree map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tree); err != nil {
		return "", false
	}

	for _, field := range patientContextParams {
		blob, ok := tree[field]
		if !ok {
			continue
		}
		var ref struct {
			Reference string `json:"reference"`
		}
		if err := json.Unmarshal(blob, &ref); err != nil {
			continue
		}
		if id := refIDFromString(ref.Reference); id != "" {
			return id, true
		}
	}

	return "", false
}

// enforceSearchContext narrows a search to the caller's patient, or refuses it.
//
// Narrowing rather than refusing an unscoped search, because a patient app asking for its own observations without repeating
// the patient parameter is behaving correctly - the context is what the parameter would say.
//
// A search naming a different patient is refused rather than quietly narrowed. Silently returning another patient's records
// as though they were the requested ones would be worse than either alternative, and silently returning nothing would look
// like the patient has no observations.
func (s *Server) enforceSearchContext(caller *Caller, q *SearchQuery) error {
	if caller == nil {
		return nil
	}

	// The encounter narrowing is applied first and separately, because it applies whether or not there is a patient
	// context and must not be reachable only through the patient branch below.
	if err := narrowSearchToEncounter(caller, q); err != nil {
		return err
	}

	if caller.Patient == "" {
		return nil
	}

	if !hasPatientContext(q.ResourceType) {
		// A type with no patient parameter cannot be narrowed. Refused rather than served, because serving it
		// would hand a patient-context token the whole table - and Practitioner or Organization is exactly the
		// sort of type where that reads as harmless until somebody notices it lists every referring physician.
		//
		// The exception is Patient, handled below: a patient app legitimately reads its own demographics.
		if q.ResourceType != "Patient" {
			return fmt.Errorf("this token is limited to one patient, and %s cannot be narrowed to a "+
				"patient, so it cannot be searched with this token", q.ResourceType)
		}
	}

	if q.ResourceType == "Patient" {
		// The context patient's own record, by id. Any other identifier or name criteria are still applied, so a
		// search that would not have matched still does not.
		if given, ok := q.Criteria["_id"]; ok {
			for _, v := range given {
				if v != caller.Patient {
					return fmt.Errorf("this token is limited to patient %s", caller.Patient)
				}
			}

			return nil
		}
		q.Criteria["_id"] = []string{caller.Patient}

		return nil
	}

	for _, param := range patientContextParams {
		given, ok := q.Criteria[param]
		if !ok {
			continue
		}
		for _, v := range given {
			if refIDFromString(v) != caller.Patient {
				return fmt.Errorf("this token is limited to patient %s", caller.Patient)
			}
		}

		return nil
	}

	// Nothing given, so the context supplies it.
	q.Criteria["patient"] = []string{caller.Patient}

	return nil
}

// permitsResource reports whether the caller's context allows this resource.
//
// Used after a read and before a write. For a read the answer has to be a 404 rather than a 403, which is why this returns a
// bool and lets the caller choose the status: telling an app that a record exists but belongs to somebody else is itself a
// disclosure, and for clinical data it is the disclosure that matters - "does this hospital hold a record for this person"
// is the question a stalker asks.
func permitsResource(caller *Caller, r fhir.Resource) bool {
	if caller == nil {
		return true
	}

	// The patient check first, and never skipped because an encounter is present.
	//
	// A token carrying an encounter but no patient is not a licence to read every patient's encounter of that id, and a
	// token carrying both must satisfy both. Written as two independent narrowings rather than a choice, because
	// "encounter instead of patient" is the shape that turns a narrower grant into a wider one.
	if caller.Patient != "" {
		subject, ok := subjectOf(r)
		if !ok {
			// No subject found. Refused, because the alternative fails open: a resource type this function
			// has not been taught about would be readable by every patient-context token.
			return false
		}
		if subject != caller.Patient {
			return false
		}
	}

	if caller.Encounter != "" && !permitsEncounter(caller.Encounter, r) {
		return false
	}

	return true
}

// permitsEncounter reports whether a resource belongs to the encounter a token was launched from.
//
// A resource with no encounter field at all is permitted, and that is the one place here that fails open deliberately. A Patient has no
// encounter, and neither does a Practitioner or an Organization - refusing them would make an encounter-scoped app unable to read the patient
// whose visit it was launched from, which is the first thing it does. The patient check above is what constrains those.
//
// A resource that has an encounter field and does not match is refused. That is the case the restriction is for: an app launched from today's
// visit should not read last year's.
func permitsEncounter(want string, r fhir.Resource) bool {
	encounter, present := encounterOf(r)
	if !present {
		return true
	}

	// Present but empty means the sender did not record an encounter, which is not the same as the resource having no
	// such concept. Refused, because an app launched from one visit reading results attached to no visit is the
	// behaviour that makes the restriction meaningless - every resource with a blank field would pass.
	return encounter == want
}

// encounterOf reads a resource's encounter reference.
//
// Reported as (value, present) so a resource that has no encounter concept is distinguishable from one whose encounter is blank. Those need
// opposite answers and a bare string cannot express the difference.
//
// Read from marshalled JSON for the same reason subjectOf is: a type switch returns "no encounter" for a type nobody taught it about, and that
// is precisely the answer that lets a resource through.
func encounterOf(r fhir.Resource) (string, bool) {
	if r == nil {
		return "", false
	}

	// An Encounter is its own encounter, which the field lookup below would miss entirely - and missing it would let an
	// encounter-scoped token read every encounter in the hospital.
	if r.ResourceTypeName() == "Encounter" {
		return r.ResourceID(), true
	}

	raw, err := json.Marshal(r)
	if err != nil {
		// Unreadable. Reported as present-and-unmatchable rather than absent, so it fails closed.
		return "", true
	}

	var probe struct {
		Encounter *struct {
			Reference string `json:"reference"`
		} `json:"encounter"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", true
	}
	if probe.Encounter == nil {
		return "", false
	}

	return refIDFromString(probe.Encounter.Reference), true
}

// There is deliberately no helper here for writing the refusal.
//
// The first version had one, and it wrote "was not found" where a genuine miss writes "does not exist" - so the helper whose
// entire purpose was to be indistinguishable from a real miss was the thing that made the two distinguishable. The handlers
// now write the same message they already write for an absent resource, which is the only way to be sure they match.

// narrowSearchToEncounter constrains a search to the caller's encounter context.
//
// Applied to types that have an encounter parameter and skipped for those that do not, which is the opposite of how the patient narrowing
// handles that case - and deliberately so. A type with no patient parameter is refused, because serving it hands a patient-scoped token a whole
// table. A type with no encounter parameter is served, because Patient itself is one of them and an encounter-scoped app must be able to read the
// patient whose visit it was launched from. The patient context is what constrains those types.
func narrowSearchToEncounter(caller *Caller, q *SearchQuery) error {
	if caller.Encounter == "" {
		return nil
	}

	if !supportsParam(q.ResourceType, "encounter") {
		return nil
	}

	given, ok := q.Criteria["encounter"]
	if !ok {
		// The context supplies it, as it does for the patient.
		q.Criteria["encounter"] = []string{caller.Encounter}

		return nil
	}

	// A search naming a different encounter is refused rather than narrowed, for the same reason as the patient: silently
	// returning one encounter's results as another's is a wrong clinical answer, and silently returning nothing looks like
	// an encounter with no results.
	for _, v := range given {
		if refIDFromString(v) != caller.Encounter {
			return fmt.Errorf("this token is limited to encounter %s", caller.Encounter)
		}
	}

	return nil
}

// supportsParam reports whether a resource type declares a search parameter.
func supportsParam(resourceType, param string) bool {
	for _, p := range SearchParams[resourceType] {
		if p == param {
			return true
		}
	}

	return false
}
