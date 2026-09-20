package fhirserver

import (
	"context"
	"strings"
	"testing"
)

// chainFixture stores two patients with different names and an observation for each.
func chainFixture(t *testing.T) *Store {
	t.Helper()

	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois", "given": ["Anne"]}]}`)
	put(t, store, `{"resourceType": "Patient", "id": "p2", "name": [{"family": "Nkemelu", "given": ["Robert"]}]}`)
	// A Location sharing an id with a patient, so a chain that ignored the target type would cross into it.
	put(t, store, `{"resourceType": "Location", "id": "p1", "status": "active", "name": "Ward 4"}`)

	put(t, store, `{
		"resourceType": "Observation", "id": "obs1", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p1"}
	}`)
	put(t, store, `{
		"resourceType": "Observation", "id": "obs2", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p2"}
	}`)
	// An observation about the Location, which a chain through subject must not confuse with the patient's.
	put(t, store, `{
		"resourceType": "Observation", "id": "obs3", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Location/p1"}
	}`)

	return store
}

// TestAChainedSearchFindsByAPropertyOfTheTarget is the feature.
//
// "Observations for patients named Dubois" is how the question is naturally asked. A client that cannot chain has to search patients, collect
// the ids and search observations once per id, which is the same N+1 problem _include solves on the response side.
func TestAChainedSearchFindsByAPropertyOfTheTarget(t *testing.T) {
	store := chainFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"patient.family": {"Dubois"},
	})

	if len(res.Resources) != 1 {
		t.Fatalf("found %d observations, want 1: %v", len(res.Resources), describe(res.Resources))
	}
	if got := res.Resources[0].ResourceID(); got != "obs1" {
		t.Errorf("found %s, want obs1", got)
	}
}

// TestAChainThroughAnAmbiguousParameterIsRefused covers the case with no correct answer.
//
// subject can point at a Patient, a Group or a Location, and those have different parameters - family means nothing to a Location. Resolving
// against each in turn and unioning the results would answer a question the client did not ask, and the union would look valid.
func TestAChainThroughAnAmbiguousParameterIsRefused(t *testing.T) {
	_, err := ParseSearch("Observation", map[string][]string{"subject.family": {"Dubois"}})
	if err == nil {
		t.Fatal("a chain through an ambiguous parameter was accepted")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	// And it says how to write it properly, because otherwise the client is stuck.
	if !strings.Contains(err.Error(), "subject:Type.family") {
		t.Errorf("the refusal does not show the qualified form: %v", err)
	}
}

// TestAnExplicitlyQualifiedChainWorks is the other half of the refusal above.
func TestAnExplicitlyQualifiedChainWorks(t *testing.T) {
	store := chainFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"subject:Patient.family": {"Dubois"},
	})

	if len(res.Resources) != 1 {
		t.Fatalf("found %d observations, want 1: %v", len(res.Resources), describe(res.Resources))
	}
	if got := res.Resources[0].ResourceID(); got != "obs1" {
		t.Errorf("found %s, want obs1", got)
	}
}

// TestAChainDoesNotCrossIntoAnotherTypeSharingAnID is the reference defect in its third hiding place.
//
// The fixture holds Location/p1 beside Patient/p1, and an observation about each. A chain that constrained the outer query by bare id would
// return both.
func TestAChainDoesNotCrossIntoAnotherTypeSharingAnID(t *testing.T) {
	store := chainFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"patient.family": {"Dubois"},
	})

	for _, r := range res.Resources {
		if r.ResourceID() == "obs3" {
			t.Error("the chain matched the observation about Location/p1, which merely shares an id with " +
				"the patient the chain found")
		}
	}
}

// TestAChainThatMatchesNothingReturnsAnOrdinaryEmptyResult covers the shape of the empty answer.
//
// Expressed as a criterion nothing can match rather than by returning early, so the total and the links are built the same way as for any
// other query. A special case here is how paging metadata ends up wrong for one query shape.
func TestAChainThatMatchesNothingReturnsAnOrdinaryEmptyResult(t *testing.T) {
	store := chainFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"patient.family": {"NobodyHasThisName"},
	})

	if len(res.Resources) != 0 {
		t.Errorf("a chain matching no patients returned %d observations", len(res.Resources))
	}
	if res.Total != 0 {
		t.Errorf("the total is %d, want 0", res.Total)
	}
}

// TestAnUnsupportedChainedParameterIsRefusedWhenTheQueryIsRead covers where the refusal happens.
//
// The same treatment as any other unsupported parameter, and for the same reason: silently dropping it changes what the query means, and the
// client gets a larger result set than it asked for while believing the condition was applied.
func TestAnUnsupportedChainedParameterIsRefusedWhenTheQueryIsRead(t *testing.T) {
	for _, tc := range []struct{ key, wants string }{
		{"patient.nonsense", "does not support the search parameter"},
		{"nonsense.family", "cannot chain through"},
		{"patient.", "names no parameter after the dot"},
		{"patient:Practitioner.family", "does not point at"},
	} {
		_, err := ParseSearch("Observation", map[string][]string{tc.key: {"x"}})
		if err == nil {
			t.Errorf("%s was accepted", tc.key)

			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("%s refused with %q, which does not mention %q", tc.key, err, tc.wants)
		}
	}
}

// TestAChainAndAnExplicitReferenceBothApply covers the interaction between the two.
//
// FHIR ORs repeated values within one parameter, so a client sending both a chain and a direct reference on the same parameter gets the union.
// Assigning rather than appending would make one silently replace the other, and which one won would depend on map iteration order.
func TestAChainAndAnExplicitReferenceBothApply(t *testing.T) {
	store := chainFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"patient.family": {"Dubois"},
		"patient":        {"Patient/p2"},
	})

	found := map[string]bool{}
	for _, r := range res.Resources {
		found[r.ResourceID()] = true
	}

	if !found["obs1"] || !found["obs2"] {
		t.Errorf("the chain and the direct reference did not both apply; found %v", describe(res.Resources))
	}
}

// TestAChainMatchingTooManyTargetsIsRefusedRatherThanTruncated covers the bound.
//
// A chain resolved against the first thousand of ten thousand patients returns part of the right answer, and nothing about the response says
// it is incomplete. That is worse than a refusal, because the client cannot tell.
func TestAChainMatchingTooManyTargetsIsRefusedRatherThanTruncated(t *testing.T) {
	if testing.Short() {
		t.Skip("stores more than a thousand resources")
	}

	store := newTestStore(t)

	// One more than the bound, all sharing a family name.
	for i := 0; i <= maxChainedMatches; i++ {
		put(t, store, `{"resourceType": "Patient", "id": "p`+itoa(i)+`", "name": [{"family": "Common"}]}`)
	}

	q, err := ParseSearch("Observation", map[string][]string{"patient.family": {"Common"}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Search(context.Background(), q)
	if err == nil {
		t.Fatal("a chain matching more than the bound was resolved anyway, so the result would be a subset " +
			"with nothing to say so")
	}
	if !strings.Contains(err.Error(), "narrow the chained condition") {
		t.Errorf("the refusal does not say what to do about it: %v", err)
	}
}

// itoa is strconv.Itoa, kept local so the test file does not import strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}

	return string(digits)
}
