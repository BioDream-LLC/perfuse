package fhirserver

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// everythingStore builds two patients with records, so a leak between them is detectable.
//
// Two patients rather than one, deliberately. A compartment test with a single patient in the database passes whether the query filters
// by patient or ignores the filter entirely, and the second of those is a records breach.
func everythingStore(t *testing.T) *Store {
	t.Helper()

	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"alice", "bob"} {
		p := &fhir.Patient{
			Identifier: []fhir.Identifier{{System: "http://sitea.example.org/mrn", Value: "MRN-" + id}},
			Name:       []fhir.HumanName{{Family: id}},
		}
		p.SetResourceID(id)
		if _, err := s.Put(ctx, p); err != nil {
			t.Fatalf("storing patient %s: %v", id, err)
		}

		e := &fhir.Encounter{
			Status:  "in-progress",
			Subject: &fhir.Reference{Reference: "Patient/" + id},
		}
		e.SetResourceID("enc-" + id)
		if _, err := s.Put(ctx, e); err != nil {
			t.Fatalf("storing encounter for %s: %v", id, err)
		}

		o := &fhir.Observation{
			Status:  "final",
			Code:    &fhir.CodeableConcept{Text: "haemoglobin"},
			Subject: &fhir.Reference{Reference: "Patient/" + id},
		}
		o.SetResourceID("obs-" + id)
		if _, err := s.Put(ctx, o); err != nil {
			t.Fatalf("storing observation for %s: %v", id, err)
		}
	}

	return s
}

// The record must contain the patient and the patient's resources, and nothing belonging to anyone else.
func TestEverythingReturnsOnePatientsRecord(t *testing.T) {
	s := everythingStore(t)

	res, err := s.Everything(context.Background(), &EverythingRequest{PatientID: "alice"}, nil)
	if err != nil {
		t.Fatalf("$everything failed: %v", err)
	}

	got := map[string]bool{}
	for _, r := range res.Resources {
		got[r.ResourceTypeName()+"/"+r.ResourceID()] = true
	}

	// The patient itself, because a record without the person it belongs to is not a record.
	for _, want := range []string{"Patient/alice", "Encounter/enc-alice", "Observation/obs-alice"} {
		if !got[want] {
			t.Errorf("the record is missing %s: got %s", want, describe(res.Resources))
		}
	}

	// And nothing of bob's. This is the assertion that a single-patient fixture could not make.
	for _, unwanted := range []string{"Patient/bob", "Encounter/enc-bob", "Observation/obs-bob"} {
		if got[unwanted] {
			t.Errorf("%s belongs to another patient and was returned: got %s", unwanted, describe(res.Resources))
		}
	}

	if res.Total != len(res.Resources) {
		t.Errorf("total %d does not match the %d resources returned", res.Total, len(res.Resources))
	}
}

// A patient-scoped caller must not read another patient's record even by asking directly.
func TestEverythingRefusesResourcesOutsideAPatientScopedToken(t *testing.T) {
	s := everythingStore(t)

	// A token launched for alice, asking for bob. The handler returns 403 for this, and the store must also decline to
	// hand anything over, because two checks that agree are how a mistake in one of them stays harmless.
	res, err := s.Everything(context.Background(),
		&EverythingRequest{PatientID: "bob"},
		&Caller{Patient: "alice"})
	if err != nil {
		t.Fatalf("$everything failed: %v", err)
	}

	if len(res.Resources) != 0 {
		t.Errorf("a token scoped to alice received %d resource(s) from bob's record: %s", len(res.Resources), describe(res.Resources))
	}
}

// _type must narrow, and must be refused when it names something outside the compartment.
func TestEverythingTypeFilter(t *testing.T) {
	s := everythingStore(t)

	req, err := ParseEverything("alice", map[string][]string{"_type": {"Observation"}})
	if err != nil {
		t.Fatalf("_type=Observation was refused: %v", err)
	}

	res, err := s.Everything(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("$everything failed: %v", err)
	}

	for _, r := range res.Resources {
		if r.ResourceTypeName() != "Observation" {
			t.Errorf("_type=Observation returned a %s: %s", r.ResourceTypeName(), describe(res.Resources))
		}
	}
	if len(res.Resources) == 0 {
		t.Error("_type=Observation returned nothing, though the patient has one")
	}
}

// Parameters this operation does not implement must be refused, never ignored.
//
// start and end are the dangerous pair: a caller asking for one week of a record and silently receiving all thirty years has been given
// more than they asked for, and has no way to tell.
func TestEverythingRefusesParametersItDoesNotImplement(t *testing.T) {
	tests := []struct {
		name   string
		params map[string][]string
		expect string
	}{
		{"start is not implemented", map[string][]string{"start": {"2026-01-01"}}, "refused rather than ignored"},
		{"end is not implemented", map[string][]string{"end": {"2026-01-01"}}, "refused rather than ignored"},
		{"an invented parameter", map[string][]string{"_everything": {"yes"}}, "not a parameter"},
		{"a type outside the compartment", map[string][]string{"_type": {"Nonsense"}}, "patient compartment"},
		{"_since must be an instant", map[string][]string{"_since": {"last tuesday"}}, "instant"},
		{"_since cannot be given twice", map[string][]string{"_since": {"2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"}}, "more than once"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEverything("alice", tc.params)
			if err == nil {
				t.Fatal("this was accepted, and an ignored filter on a patient record returns more than was asked for")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("the refusal does not mention %q: %v", tc.expect, err)
			}
		})
	}
}

// Over the ceiling the operation must refuse, not return a partial record that looks complete.
func TestEverythingRefusesRatherThanTruncating(t *testing.T) {
	s := everythingStore(t)

	// A ceiling of one, via _count, with three resources in the record.
	req, err := ParseEverything("alice", map[string][]string{"_count": {"1"}})
	if err != nil {
		t.Fatalf("_count=1 was refused: %v", err)
	}

	_, err = s.Everything(context.Background(), req, nil)
	if err == nil {
		t.Fatal("a record larger than the ceiling was returned truncated. " +
			"A partial record is indistinguishable from a complete one, and somebody reads it as the whole chart")
	}
	if !strings.Contains(err.Error(), "_type") && !strings.Contains(err.Error(), "export") {
		t.Errorf("the refusal does not say how to get the record: %v", err)
	}
}

// An absent patient is a 404, not an empty record.
func TestEverythingOnAnUnknownPatientIsNotFound(t *testing.T) {
	s := everythingStore(t)

	_, err := s.Everything(context.Background(), &EverythingRequest{PatientID: "nobody"}, nil)
	if err == nil {
		t.Fatal("an unknown patient produced a record rather than an error, " +
			"so a typo in an id looks like a person with nothing wrong with them")
	}
}

// The compartment must be derived from the search registry, so a type added to search is not silently left out of records.
func TestCompartmentTypesComeFromTheSearchRegistry(t *testing.T) {
	got := CompartmentTypes()

	found := map[string]bool{}
	for _, t := range got {
		found[t] = true
	}

	if !found["Patient"] {
		t.Error("Patient is not in its own compartment, so $everything would omit the person")
	}

	// Every type that search can narrow to a patient must be reachable, or a records request comes back short.
	for resourceType := range SearchParams {
		if resourceType == "Patient" {
			continue
		}
		if hasPatientContext(resourceType) && !found[resourceType] {
			t.Errorf("%s can be narrowed to a patient by search but is not in the compartment, "+
				"so it would be missing from a record", resourceType)
		}
	}

	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("CompartmentTypes is not sorted: %q before %q", got[i-1], got[i])

			break
		}
	}
}
