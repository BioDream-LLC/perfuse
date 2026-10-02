package fhirserver

import (
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// resourceFrom parses a literal for a launch-context test.
func resourceFrom(t *testing.T, body string) fhir.Resource {
	t.Helper()

	r, err := fhir.UnmarshalResource([]byte(body))
	if err != nil {
		t.Fatalf("the test resource is not valid FHIR: %v", err)
	}

	return r
}

// TestAnEncounterContextNarrowsWithinAPatient is the feature.
//
// An app launched from a specific visit should see that visit. Without this, a token carrying an encounter context was no narrower than one
// carrying only a patient, so an app launched from today's appointment could read last year's results.
func TestAnEncounterContextNarrowsWithinAPatient(t *testing.T) {
	caller := &Caller{Patient: "p1", Encounter: "enc-today"}

	thisVisit := resourceFrom(t, `{
		"resourceType": "Observation", "id": "o1", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p1"},
		"encounter": {"reference": "Encounter/enc-today"}
	}`)
	lastYear := resourceFrom(t, `{
		"resourceType": "Observation", "id": "o2", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p1"},
		"encounter": {"reference": "Encounter/enc-last-year"}
	}`)

	if !permitsResource(caller, thisVisit) {
		t.Error("an observation from the launch encounter was refused")
	}
	if permitsResource(caller, lastYear) {
		t.Error("an observation from a different encounter of the same patient was permitted")
	}
}

// TestAnEncounterContextDoesNotReplaceThePatientCheck is the way this could have widened access.
//
// A token carrying an encounter but no patient is not a licence to read every patient's encounter of that id, and a token carrying both must
// satisfy both. Written as two independent narrowings rather than a choice, because "encounter instead of patient" is the shape that turns a
// narrower grant into a wider one.
func TestAnEncounterContextDoesNotReplaceThePatientCheck(t *testing.T) {
	caller := &Caller{Patient: "p1", Encounter: "enc-today"}

	// Another patient's observation, attached to the same encounter id. Contrived only in how neat it is; encounter ids
	// from a source system's sequence collide across patients exactly as resource ids do.
	otherPatient := resourceFrom(t, `{
		"resourceType": "Observation", "id": "o3", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p2"},
		"encounter": {"reference": "Encounter/enc-today"}
	}`)

	if permitsResource(caller, otherPatient) {
		t.Error("an observation belonging to another patient was permitted because its encounter matched")
	}
}

// TestAnEncounterScopedTokenCanStillReadThePatient covers the one deliberate fail-open.
//
// A Patient has no encounter, and neither does a Practitioner or an Organization. Refusing them would make an encounter-scoped app unable to
// read the patient whose visit it was launched from, which is the first thing it does. The patient context is what constrains those types.
func TestAnEncounterScopedTokenCanStillReadThePatient(t *testing.T) {
	caller := &Caller{Patient: "p1", Encounter: "enc-today"}

	patient := resourceFrom(t, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois"}]}`)
	if !permitsResource(caller, patient) {
		t.Error("an encounter-scoped token cannot read the patient whose visit it was launched from")
	}

	// And it still cannot read a different patient, so the fail-open above is bounded by the patient check.
	other := resourceFrom(t, `{"resourceType": "Patient", "id": "p2"}`)
	if permitsResource(caller, other) {
		t.Error("an encounter-scoped token read a different patient")
	}
}

// TestTheEncounterItselfIsSubjectToTheEncounterContext closes a gap the field lookup would leave.
//
// An Encounter has no encounter field, so reading it through the generic lookup would report "no encounter" and permit it - which would let an
// encounter-scoped token read every encounter in the hospital, the exact thing it is restricted from.
func TestTheEncounterItselfIsSubjectToTheEncounterContext(t *testing.T) {
	caller := &Caller{Encounter: "enc-today"}

	own := resourceFrom(t, `{"resourceType": "Encounter", "id": "enc-today", "status": "finished"}`)
	if !permitsResource(caller, own) {
		t.Error("the launch encounter itself was refused")
	}

	other := resourceFrom(t, `{"resourceType": "Encounter", "id": "enc-other", "status": "finished"}`)
	if permitsResource(caller, other) {
		t.Error("an encounter-scoped token read a different encounter")
	}
}

// TestAResourceWithABlankEncounterIsRefused covers the distinction that makes the restriction mean anything.
//
// Present-but-empty is not the same as having no encounter concept. A resource that can carry an encounter and does not is refused, because an
// app launched from one visit reading results attached to no visit would make the restriction decorative - every resource with a blank field
// would pass.
func TestAResourceWithABlankEncounterIsRefused(t *testing.T) {
	caller := &Caller{Patient: "p1", Encounter: "enc-today"}

	blank := resourceFrom(t, `{
		"resourceType": "Observation", "id": "o4", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p1"},
		"encounter": {"reference": ""}
	}`)

	if permitsResource(caller, blank) {
		t.Error("a resource whose encounter is present but empty was permitted, which would make the " +
			"restriction decorative")
	}
}

// TestASearchIsNarrowedToTheEncounter covers the search side.
func TestASearchIsNarrowedToTheEncounter(t *testing.T) {
	srv := &Server{}
	caller := &Caller{Patient: "p1", Encounter: "enc-today"}

	q := &SearchQuery{ResourceType: "Observation", Criteria: map[string][]string{}}
	if err := srv.enforceSearchContext(caller, q); err != nil {
		t.Fatal(err)
	}

	if got := q.Criteria["encounter"]; len(got) != 1 || got[0] != "enc-today" {
		t.Errorf("the search was not narrowed to the encounter: %v", q.Criteria)
	}
	// And the patient narrowing still happened, so one did not replace the other.
	if len(q.Criteria["patient"]) != 1 || q.Criteria["patient"][0] != "p1" {
		t.Errorf("the patient narrowing was lost: %v", q.Criteria)
	}
}

// TestASearchNamingAnotherEncounterIsRefused matches how the patient case behaves, and for the same reason.
//
// Silently returning one encounter's results as another's is a wrong clinical answer, and silently returning nothing looks like an encounter
// with no results.
func TestASearchNamingAnotherEncounterIsRefused(t *testing.T) {
	srv := &Server{}
	caller := &Caller{Encounter: "enc-today"}

	q := &SearchQuery{
		ResourceType: "Observation",
		Criteria:     map[string][]string{"encounter": {"Encounter/enc-other"}},
	}

	if err := srv.enforceSearchContext(caller, q); err == nil {
		t.Error("a search naming a different encounter was accepted")
	}
}

// TestASearchForATypeWithNoEncounterParameterIsNotRefused covers the asymmetry with the patient rule.
//
// The patient narrowing refuses a type it cannot narrow, because serving it hands a patient-scoped token a whole table. The encounter narrowing
// does not, because Patient is one of those types and an encounter-scoped app must read the patient whose visit it was launched from.
func TestASearchForATypeWithNoEncounterParameterIsNotRefused(t *testing.T) {
	srv := &Server{}
	caller := &Caller{Patient: "p1", Encounter: "enc-today"}

	q := &SearchQuery{ResourceType: "Patient", Criteria: map[string][]string{}}
	if err := srv.enforceSearchContext(caller, q); err != nil {
		t.Errorf("a Patient search was refused for an encounter-scoped token: %v", err)
	}
	if len(q.Criteria["_id"]) != 1 || q.Criteria["_id"][0] != "p1" {
		t.Errorf("the patient narrowing did not apply: %v", q.Criteria)
	}
}
