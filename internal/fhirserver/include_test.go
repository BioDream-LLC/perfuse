package fhirserver

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// includeFixture stores an observation, its patient, and its encounter, plus a decoy sharing an id across types.
func includeFixture(t *testing.T) *Store {
	t.Helper()

	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "123", "name": [{"family": "Dubois"}]}`)
	put(t, store, `{"resourceType": "Encounter", "id": "enc1", "status": "finished"}`)
	// A Location sharing the Patient's id, which is how an include crosses into the wrong record when the type is not
	// carried. Location rather than Group because it is a type this server implements and a genuine permitted target of
	// subject, so the decoy is one a real feed could produce rather than one invented for the test.
	put(t, store, `{"resourceType": "Location", "id": "123", "status": "active"}`)

	put(t, store, `{
		"resourceType": "Observation", "id": "obs1", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/123"},
		"encounter": {"reference": "Encounter/enc1"}
	}`)

	return store
}

// searchWith runs a search with the given raw query parameters.
func searchWith(t *testing.T, store *Store, resourceType string, params map[string][]string) *SearchResult {
	t.Helper()

	q, err := ParseSearch(resourceType, params)
	if err != nil {
		t.Fatalf("the query was refused: %v", err)
	}

	res, err := store.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

// TestIncludeReturnsTheReferencedResource is the reason this exists.
//
// A SMART app that fetches thirty observations and then thirty patients one at a time is not slow, it is broken: the round trips defeat the
// page size, and several client libraries assume the included resources are present and render nothing when they are not.
func TestIncludeReturnsTheReferencedResource(t *testing.T) {
	store := includeFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:subject"},
	})

	if len(res.Resources) != 1 {
		t.Fatalf("matched %d observations, want 1", len(res.Resources))
	}
	if len(res.Included) != 1 {
		t.Fatalf("included %d resources, want 1: %v", len(res.Included), describe(res.Included))
	}
	if got := res.Included[0].ResourceTypeName() + "/" + res.Included[0].ResourceID(); got != "Patient/123" {
		t.Errorf("included %s, want Patient/123", got)
	}
}

// TestAnIncludeDoesNotCrossIntoAnotherType is the defect from the reference index, in its new hiding place.
//
// The fixture holds a Group with the same id as the Patient. An include that matched on the bare id would pull in the Group as well, and a
// client rendering "the subject of this observation" would show a group where a person belongs.
func TestAnIncludeDoesNotCrossIntoAnotherType(t *testing.T) {
	store := includeFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:subject"},
	})

	for _, r := range res.Included {
		if r.ResourceTypeName() == "Location" {
			t.Errorf("the include pulled in Location/%s, which merely shares an id with the real subject",
				r.ResourceID())
		}
	}
}

// TestIncludedResourcesAreMarkedAsIncluded covers the distinction a client depends on.
//
// The search mode is how a client tells what it asked for from what came along. Marking an included Patient as a match makes a search for
// observations appear to have returned a patient, and a client counting matches or rendering a result list shows it as one.
func TestIncludedResourcesAreMarkedAsIncluded(t *testing.T) {
	store := includeFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:subject"},
	})

	bundle := store.SearchBundle(res, "http://example.test/fhir", "Observation", "_include=Observation:subject")

	modes := map[string]string{}
	for _, e := range bundle.Entry {
		if e.Resource == nil || e.Search == nil {
			t.Fatal("a bundle entry has no resource or no search mode")
		}
		modes[e.Resource.ResourceTypeName()] = e.Search.Mode
	}

	if modes["Observation"] != "match" {
		t.Errorf("the observation is marked %q, want match", modes["Observation"])
	}
	if modes["Patient"] != "include" {
		t.Errorf("the included patient is marked %q, want include", modes["Patient"])
	}
	// Total counts matches only. A client pages on it, so counting included resources there makes the last page arrive
	// early and the client stop before it has everything.
	if bundle.Total == nil || *bundle.Total != 1 {
		t.Errorf("the bundle total counts included resources, so paging would end early: %v", bundle.Total)
	}
}

// TestRevIncludeFindsWhatPointsBack covers the other direction, which is how a patient summary is built in one request.
func TestRevIncludeFindsWhatPointsBack(t *testing.T) {
	store := includeFixture(t)

	res := searchWith(t, store, "Patient", map[string][]string{
		"_id":         {"123"},
		"_revinclude": {"Observation:subject"},
	})

	if len(res.Resources) != 1 {
		t.Fatalf("matched %d patients, want 1", len(res.Resources))
	}
	if len(res.Included) != 1 {
		t.Fatalf("reverse-included %d resources, want 1: %v", len(res.Included), describe(res.Included))
	}
	if got := res.Included[0].ResourceID(); got != "obs1" {
		t.Errorf("reverse-included %s, want obs1", got)
	}
}

// TestARevIncludeDoesNotCrossIntoAnotherType is the same protection in reverse.
//
// A reverse include restricted to Location must not pull in an observation whose subject is the Patient of the same id.
func TestARevIncludeDoesNotCrossIntoAnotherType(t *testing.T) {
	store := includeFixture(t)

	// Built directly rather than through ParseSearch, which is the point: the protection has to live in the resolver
	// rather than in whatever the HTTP layer happens to allow through.
	res, err := store.Search(context.Background(), &SearchQuery{
		ResourceType: "Patient", Count: 10,
		Criteria: map[string][]string{"_id": {"123"}},
		Includes: []IncludeSpec{{
			SourceType: "Observation", Param: "subject", TargetType: "Location", Reverse: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The include names Location as the target and the page holds a Patient, so nothing should be pulled in.
	if len(res.Included) != 0 {
		t.Errorf("a reverse include restricted to Location returned %d resources for a Patient page: %v",
			len(res.Included), describe(res.Included))
	}
}

// TestAnUnsupportedIncludeIsRefused covers the decision not to ignore one.
//
// An ignored _include produces a bundle that is valid, has the right total, and is missing exactly what the client asked for. A client that
// assumes the included resources are present then renders an empty screen with no error anywhere to explain it.
func TestAnUnsupportedIncludeIsRefused(t *testing.T) {
	// :iterate is implemented now, so it must be accepted rather than refused - in both spellings, since client libraries
	// write it on the value as well as on the parameter name.
	for _, value := range []string{"Observation:subject:iterate", "Observation:subject"} {
		spec, err := ParseInclude(value, false)
		if err != nil {
			t.Errorf("_include=%s was refused: %v", value, err)

			continue
		}
		wantIterate := strings.Contains(value, ":iterate")
		if spec.Iterate != wantIterate {
			t.Errorf("_include=%s parsed Iterate=%v, want %v", value, spec.Iterate, wantIterate)
		}
		if spec.Param != "subject" {
			t.Errorf("_include=%s parsed the parameter as %q, so stripping :iterate damaged the value",
				value, spec.Param)
		}
	}

	for _, tc := range []struct {
		value string
		wants string
	}{
		{"Observation", "Type:parameter"},
		{"Observation:nonsense", "no reference parameter"},
		{"Nonsense:subject", "no reference parameters"},
		{"Observation:subject:Practitioner", "does not point at"},
		{"Observation:subject:Patient:extra", "at most"},
	} {
		_, err := ParseInclude(tc.value, false)
		if err == nil {
			t.Errorf("_include=%s was accepted", tc.value)

			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("_include=%s was refused with %q, which does not mention %q",
				tc.value, err.Error(), tc.wants)
		}
	}
}

// TestAValidIncludeNamesItsTargetType keeps the refusals above from being a blanket refusal.
func TestAValidIncludeNamesItsTargetType(t *testing.T) {
	spec, err := ParseInclude("Observation:subject:Patient", false)
	if err != nil {
		t.Fatal(err)
	}
	if spec.SourceType != "Observation" || spec.Param != "subject" || spec.TargetType != "Patient" {
		t.Errorf("parsed as %+v", spec)
	}
	if spec.Reverse {
		t.Error("an _include was parsed as a reverse include")
	}
}

// TestAnIncludedResourceIsNotSentTwice covers the deduplication.
//
// A duplicate entry is not merely wasteful: a client building a map by id processes it twice, and one building a list shows it twice. The
// case arises whenever two matched resources share a subject, which for observations is the normal case.
func TestAnIncludedResourceIsNotSentTwice(t *testing.T) {
	store := includeFixture(t)

	// A second observation on the same patient.
	put(t, store, `{
		"resourceType": "Observation", "id": "obs2", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/123"}
	}`)

	res := searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:subject"},
	})

	if len(res.Resources) != 2 {
		t.Fatalf("matched %d observations, want 2", len(res.Resources))
	}
	if len(res.Included) != 1 {
		t.Errorf("the shared patient was included %d times: %v", len(res.Included), describe(res.Included))
	}
}

// TestAMatchIsNotAlsoAnInclude covers the other deduplication direction.
//
// A reverse include from a patient search can find an observation that is itself a match in a different query, and including a resource
// that is already in the bundle as a match would give it two entries with two different modes.
func TestAMatchIsNotAlsoAnInclude(t *testing.T) {
	store := includeFixture(t)

	// Patient/123 matched, and also the target of the observation's subject - so a forward include on the observation
	// would find it. Here the patient is the match, so the include must not duplicate it.
	res, err := store.Search(context.Background(), &SearchQuery{
		ResourceType: "Patient", Count: 10,
		Criteria: map[string][]string{"_id": {"123"}},
		Includes: []IncludeSpec{{SourceType: "Patient", Param: "subject"}},
	})
	if err != nil {
		// Patient has no subject parameter, so this is expected to yield nothing rather than error.
		t.Fatal(err)
	}
	for _, r := range res.Included {
		if r.ResourceTypeName() == "Patient" && r.ResourceID() == "123" {
			t.Error("the matched patient was also returned as an included resource")
		}
	}
}

// TestTheCapabilityStatementOnlyPromisesIncludesThisServerHonours covers the statement being generated.
//
// A capability statement that overstates is worse than none: a client trusts it, builds a query from it, and gets an error the statement
// said would not happen. So the advertised list is generated from the same table the resolver uses.
func TestTheCapabilityStatementOnlyPromisesIncludesThisServerHonours(t *testing.T) {
	for _, value := range includeOptions("Observation") {
		if _, err := ParseInclude(value, false); err != nil {
			t.Errorf("the capability statement advertises _include=%s, which is refused: %v", value, err)
		}
	}

	for _, value := range revIncludeOptions("Patient") {
		if _, err := ParseInclude(value, true); err != nil {
			t.Errorf("the capability statement advertises _revinclude=%s, which is refused: %v", value, err)
		}
	}

	// And it is not empty, since a generated list that produced nothing would pass the loops above without proving
	// anything.
	if len(includeOptions("Observation")) == 0 {
		t.Error("no includes are advertised for Observation")
	}
	if len(revIncludeOptions("Patient")) == 0 {
		t.Error("no reverse includes are advertised for Patient")
	}
}

// describe names resources for a failure message.
//
// Types and ids only, deliberately: a test failure message is a thing people paste into tickets, and these are patient records.
func describe(rs []fhir.Resource) string {
	var names []string
	for _, r := range rs {
		names = append(names, r.ResourceTypeName()+"/"+r.ResourceID())
	}

	return strings.Join(names, ", ")
}

// TestAnIncludeRestrictedToOneTypeSkipsTheOthers is the cross-type protection, tested where it lives.
//
// A plant showed the earlier test of this name proved nothing: its decoy Location was never referenced by anything, so the resolver was
// never offered a candidate of the wrong type. The check only matters when a reference genuinely points at a type the include excludes.
func TestAnIncludeRestrictedToOneTypeSkipsTheOthers(t *testing.T) {
	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois"}]}`)
	put(t, store, `{"resourceType": "Location", "id": "l1", "status": "active"}`)

	// One observation subject to a Patient, one to a Location. Both legitimate: subject permits either.
	put(t, store, `{
		"resourceType": "Observation", "id": "obs-person", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p1"}
	}`)
	put(t, store, `{
		"resourceType": "Observation", "id": "obs-place", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Location/l1"}
	}`)

	res := searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:subject:Patient"},
	})

	if len(res.Resources) != 2 {
		t.Fatalf("matched %d observations, want 2", len(res.Resources))
	}
	if len(res.Included) != 1 {
		t.Fatalf("included %d resources, want only the patient: %v", len(res.Included), describe(res.Included))
	}
	if got := res.Included[0].ResourceTypeName(); got != "Patient" {
		t.Errorf("an include restricted to Patient returned a %s", got)
	}
}

// TestAnAmbiguousReferenceIsNotResolvedToSeveralCandidates covers the case with no correct answer.
//
// A reference written as a bare id names no type. Where the parameter can point at only one type there is nothing to guess. Where it can
// point at several - subject may be a Patient, a Group or a Location - resolving all of them would put two candidate subjects in the bundle
// for one observation, and a client showing "the subject of this result" would pick whichever came first. That is a wrong clinical answer
// rather than a missing one.
func TestAnAmbiguousReferenceIsNotResolvedToSeveralCandidates(t *testing.T) {
	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "123", "name": [{"family": "Dubois"}]}`)
	put(t, store, `{"resourceType": "Location", "id": "123", "status": "active"}`)

	// A subject with no type, which is what a bundle relying on fullUrl produces.
	put(t, store, `{
		"resourceType": "Observation", "id": "obs", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "123"}
	}`)

	res := searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:subject"},
	})

	if len(res.Included) != 0 {
		t.Errorf("an untyped reference on a parameter with several possible targets was resolved to %d "+
			"candidates: %v", len(res.Included), describe(res.Included))
	}

	// But the same reference on a parameter with exactly one possible target does resolve, because there is nothing to
	// guess. Without this the change above would be a blanket refusal to follow untyped references.
	res = searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:patient"},
	})

	if len(res.Included) != 1 {
		t.Fatalf("an untyped reference on patient, which can only be a Patient, resolved to %d: %v",
			len(res.Included), describe(res.Included))
	}
	if got := res.Included[0].ResourceTypeName(); got != "Patient" {
		t.Errorf("resolved to a %s", got)
	}
}

// TestTwoIncludesResolvingToOneResourceSendItOnce covers the deduplication where it can actually fire.
//
// A plant showed the earlier duplicate test proved nothing: the index query is DISTINCT, so two matched observations sharing a subject
// already yield one row. The map matters when two different includes resolve to the same resource, and patient and subject pointing at the
// same Patient is a query somebody writes without thinking about it.
func TestTwoIncludesResolvingToOneResourceSendItOnce(t *testing.T) {
	store := includeFixture(t)

	res := searchWith(t, store, "Observation", map[string][]string{
		"_include": {"Observation:patient", "Observation:subject"},
	})

	patients := 0
	for _, r := range res.Included {
		if r.ResourceTypeName() == "Patient" {
			patients++
		}
	}
	if patients != 1 {
		t.Errorf("the patient appears %d times across two includes that resolve to it: %v",
			patients, describe(res.Included))
	}
}

// TestABadIncludeIsRefusedThroughTheQueryParser covers the refusal on the path a request actually takes.
//
// A plant showed the earlier refusal test only exercised ParseInclude directly, so treating the error as "skip this include" inside
// ParseSearch broke nothing. That is the form the bug would really take: the client's include is dropped, the bundle comes back valid and
// complete-looking, and the resources it asked for are missing.
func TestABadIncludeIsRefusedThroughTheQueryParser(t *testing.T) {
	_, err := ParseSearch("Observation", map[string][]string{
		"_include": {"Observation:nonsense"},
	})
	if err == nil {
		t.Fatal("a search naming an unsupported include was accepted, so the include would be silently dropped")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("the refusal does not name the parameter: %v", err)
	}

	// And a good one still parses, so the above is not a blanket refusal of includes.
	q, err := ParseSearch("Observation", map[string][]string{
		"_include": {"Observation:subject"},
	})
	if err != nil {
		t.Fatalf("a valid include was refused: %v", err)
	}
	if len(q.Includes) != 1 {
		t.Errorf("the include was not recorded on the query: %+v", q.Includes)
	}
}
