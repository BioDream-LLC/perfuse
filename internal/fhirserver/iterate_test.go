package fhirserver

import (
	"context"
	"strings"
	"testing"
)

// iterateFixture builds a two-hop chain: Observation -> Encounter -> Patient.
//
// The Patient is reachable only by following a reference held by the Encounter, which is itself only present because it was
// included. That second hop is what :iterate exists for and cannot be expressed any other way in one request.
//
// The observation deliberately does *not* name the patient directly. My first version of this fixture used
// Patient.managingOrganization for the second hop, which is not an indexed search parameter - so the test failed for a reason that
// had nothing to do with :iterate. The path has to be one the index actually holds.
func iterateFixture(t *testing.T) *Store {
	t.Helper()

	st := newTestStore(t)

	put(t, st, `{"resourceType":"Patient","id":"p1","name":[{"family":"Dubois"}]}`)
	put(t, st, `{"resourceType":"Encounter","id":"e1","status":"finished",
		"subject":{"reference":"Patient/p1"}}`)
	put(t, st, `{"resourceType":"Observation","id":"o1","status":"final",
		"code":{"coding":[{"code":"8867-4"}]},
		"encounter":{"reference":"Encounter/e1"}}`)

	// An unrelated patient, so a passing test cannot be passing because there is only one of everything.
	put(t, st, `{"resourceType":"Patient","id":"p2","name":[{"family":"Elsewhere"}]}`)

	return st
}

// included returns the type/id of every included resource.
func included(t *testing.T, st *Store, q *SearchQuery) []string {
	t.Helper()

	q.Count = 50

	res, err := st.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	inc, err := st.Include(context.Background(), res.Resources, q.Includes)
	if err != nil {
		t.Fatalf("include failed: %v", err)
	}

	out := make([]string, 0, len(inc))
	for _, r := range inc {
		out = append(out, r.ResourceTypeName()+"/"+r.ResourceID())
	}

	return out
}

// TestIterateFollowsASecondHop is the feature.
func TestIterateFollowsASecondHop(t *testing.T) {
	st := iterateFixture(t)

	got := included(t, st, &SearchQuery{
		ResourceType: "Observation",
		Criteria:     map[string][]string{},
		Includes: []IncludeSpec{
			// First hop, plain: the observation's encounter.
			{SourceType: "Observation", Param: "encounter"},
			// Second hop, iterating: that encounter's patient.
			{SourceType: "Encounter", Param: "subject", Iterate: true},
		},
	})

	if !has(got, "Encounter/e1") {
		t.Errorf("the first hop is missing: %v", got)
	}
	if !has(got, "Patient/p1") {
		t.Errorf("the second hop was not followed, which is what :iterate is for: %v", got)
	}
	if has(got, "Patient/p2") {
		t.Errorf("an unrelated patient was included: %v", got)
	}
}

// TestWithoutIterateTheSecondHopIsNotFollowed is the contrast that makes the test above mean something.
//
// Without this, the previous test would pass just as well if :iterate were ignored and everything were being included by accident.
func TestWithoutIterateTheSecondHopIsNotFollowed(t *testing.T) {
	st := iterateFixture(t)

	got := included(t, st, &SearchQuery{
		ResourceType: "Observation",
		Criteria:     map[string][]string{},
		Includes: []IncludeSpec{
			{SourceType: "Observation", Param: "encounter"},
			// Same spec, not iterating. The encounter is not on the page, so this finds nothing.
			{SourceType: "Encounter", Param: "subject"},
		},
	})

	if !has(got, "Encounter/e1") {
		t.Errorf("the plain include stopped working: %v", got)
	}
	if has(got, "Patient/p1") {
		t.Errorf("the second hop was followed without :iterate, so the modifier does nothing: %v", got)
	}
}

// TestIterateAppliesAtTheFirstLevelToo covers the case a client most naturally writes.
//
// A client that writes only _include:iterate=Observation:subject expects its patient. Treating :iterate as "second level only"
// would return nothing and look like the patient does not exist.
func TestIterateAppliesAtTheFirstLevelToo(t *testing.T) {
	st := iterateFixture(t)

	got := included(t, st, &SearchQuery{
		ResourceType: "Observation",
		Criteria:     map[string][]string{},
		Includes: []IncludeSpec{
			{SourceType: "Observation", Param: "encounter", Iterate: true},
		},
	})

	if !has(got, "Encounter/e1") {
		t.Errorf("a lone :iterate include returned nothing at the first level: %v", got)
	}
}

// TestACycleTerminates covers the shape that would otherwise loop forever.
//
// The cycle is made from two specs pointing back at each other rather than from a self-referential resource: from a Patient,
// _revinclude:iterate finds its Encounters, and _include:iterate on those Encounters finds the Patient again. Follow that naively
// and it never stops.
//
// The seen set is what makes it terminate, and a test is the only way to know that rather than to believe it. If it does not
// terminate this times out instead of failing, which is why the package is run with an explicit -timeout.
func TestACycleTerminates(t *testing.T) {
	st := newTestStore(t)

	put(t, st, `{"resourceType":"Patient","id":"c-p1","name":[{"family":"Loop"}]}`)
	put(t, st, `{"resourceType":"Encounter","id":"c-e1","status":"finished",
		"subject":{"reference":"Patient/c-p1"}}`)
	put(t, st, `{"resourceType":"Encounter","id":"c-e2","status":"planned",
		"subject":{"reference":"Patient/c-p1"}}`)

	got := included(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{"_id": {"c-p1"}},
		Includes: []IncludeSpec{
			// Patient -> its Encounters.
			{SourceType: "Encounter", Param: "subject", Reverse: true, Iterate: true},
			// Encounter -> its Patient, which is where we started.
			{SourceType: "Encounter", Param: "subject", Iterate: true},
		},
	})

	// Both encounters are reached.
	for _, want := range []string{"Encounter/c-e1", "Encounter/c-e2"} {
		if !has(got, want) {
			t.Errorf("%s was not reached: %v", want, got)
		}
	}

	// The patient we started from is a match rather than an include, so it must not be included again. A duplicate entry is
	// not merely wasteful - a client building a list shows the patient twice.
	if has(got, "Patient/c-p1") {
		t.Errorf("the searched-for patient came back as an include as well as a match: %v", got)
	}

	// And nothing appears twice, which is what the seen set is for.
	counts := map[string]int{}
	for _, g := range got {
		counts[g]++
	}
	for key, n := range counts {
		if n > 1 {
			t.Errorf("%s appears %d times in the included set", key, n)
		}
	}
}

// TestIterateIsRecognisedOnTheParameterName covers the spelling FHIR actually specifies.
//
// The modifier belongs on the parameter - _include:iterate=Type:param - and the older :recurse spelling is still emitted by some
// libraries. Refusing the spelling a client's library produces achieves nothing.
func TestIterateIsRecognisedOnTheParameterName(t *testing.T) {
	for _, key := range []string{"_include:iterate", "_include:recurse"} {
		q, err := ParseSearch("Observation", map[string][]string{
			key:      {"Observation:subject"},
			"_count": {"10"},
		})
		if err != nil {
			t.Errorf("%s was refused: %v", key, err)

			continue
		}
		if len(q.Includes) != 1 {
			t.Errorf("%s produced %d includes, want 1", key, len(q.Includes))

			continue
		}
		if !q.Includes[0].Iterate {
			t.Errorf("%s did not set Iterate, so it was treated as a plain include - "+
				"which returns a subset while looking complete", key)
		}
	}

	// And the reverse form keeps both properties.
	q, err := ParseSearch("Patient", map[string][]string{
		"_revinclude:iterate": {"Observation:subject"},
		"_count":              {"10"},
	})
	if err != nil {
		t.Fatalf("_revinclude:iterate was refused: %v", err)
	}
	if len(q.Includes) != 1 || !q.Includes[0].Iterate || !q.Includes[0].Reverse {
		t.Errorf("_revinclude:iterate parsed as %+v, want both Iterate and Reverse", q.Includes)
	}
}

// TestTheCapabilityStatementNoLongerDeniesIterate keeps the advertised behaviour honest.
//
// The statement used to list _include:iterate as unimplemented. A client reads that and does not try, which is exactly as bad as
// claiming something that does not work - it is wrong in the other direction.
func TestTheCapabilityStatementNoLongerDeniesIterate(t *testing.T) {
	_, h := newTestServer(t)

	rec := do(t, h, "GET", "/metadata", nil)
	body := rec.Body.String()

	// The check is on the "Not implemented" half of the sentence specifically. Searching the whole document for the string
	// would pass as soon as it appeared anywhere, including in the list of things that do not work.
	if idx := strings.Index(body, "Not implemented:"); idx >= 0 {
		if strings.Contains(body[idx:], "_include:iterate") {
			t.Error("the capability statement still lists _include:iterate as unimplemented")
		}
	}
	if !strings.Contains(body, "_include:iterate") {
		t.Error("the capability statement does not mention _include:iterate at all; " +
			"a client has no way to know it can use it")
	}
}
