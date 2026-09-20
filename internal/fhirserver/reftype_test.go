package fhirserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// newTestStore is an in-memory store, for tests about indexing and search rather than about HTTP.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// One connection, because ":memory:" gives each connection its own database - so a pool would have the writes and
	// the reads land in different places and the tests would fail for a reason that has nothing to do with them.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, fhir.R5)
	if err != nil {
		t.Fatal(err)
	}

	return store
}

// openTestStore is a store on disk, for tests that need it to survive being closed and reopened.
func openTestStore(t *testing.T, path string) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	store, err := NewStore(db, fhir.R5)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}

	return store
}

func closeTestStore(t *testing.T, s *Store) {
	t.Helper()

	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
}

// put stores a resource given as JSON, failing the test rather than returning an error.
func put(t *testing.T, s *Store, body string) {
	t.Helper()

	r, err := fhir.UnmarshalResource([]byte(body))
	if err != nil {
		t.Fatalf("the test resource is not valid FHIR: %v", err)
	}
	if _, err := s.Put(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

// TestAReferenceSearchDistinguishesTheTargetType is the defect this column exists for.
//
// The index held a bare id, so Patient/123 and Group/123 were the same row and a search for one could return the other. A reference
// search returning another subject's records as the requested subject's is the worst answer this server has available - it is not a
// missing feature, it is a wrong clinical answer delivered with a 200.
func TestAReferenceSearchDistinguishesTheTargetType(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Two observations whose subjects share an id and differ in type. Contrived only in how tidy it is: shared numeric
	// ids across resource types are the normal case when ids come from a source system's sequence.
	put(t, store, `{
		"resourceType": "Observation", "id": "obs-patient", "status": "final",
		"code": {"coding": [{"system": "http://loinc.org", "code": "8867-4"}]},
		"subject": {"reference": "Patient/123"}
	}`)
	put(t, store, `{
		"resourceType": "Observation", "id": "obs-group", "status": "final",
		"code": {"coding": [{"system": "http://loinc.org", "code": "8867-4"}]},
		"subject": {"reference": "Group/123"}
	}`)

	// Type-qualified: exactly one.
	res, err := store.Search(ctx, &SearchQuery{
		ResourceType: "Observation",
		// An explicit page size, because zero is a page size of zero rather than "no preference" - the caller
		// supplies the default, which is what keeps ParseSearch a pure function of the URL.
		Count:    10,
		Criteria: map[string][]string{"subject": {"Patient/123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != 1 {
		t.Fatalf("a search for subject=Patient/123 returned %d observations, want 1; a reference search that "+
			"cannot tell a Patient from a Group returns another subject's records as this one's",
			len(res.Resources))
	}
	if got := res.Resources[0].ResourceID(); got != "obs-patient" {
		t.Errorf("returned %s, want obs-patient", got)
	}

	// The other type, to prove the first result was not luck or ordering.
	res, err = store.Search(ctx, &SearchQuery{
		ResourceType: "Observation",
		// An explicit page size, because zero is a page size of zero rather than "no preference" - the caller
		// supplies the default, which is what keeps ParseSearch a pure function of the URL.
		Count:    10,
		Criteria: map[string][]string{"subject": {"Group/123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != 1 || res.Resources[0].ResourceID() != "obs-group" {
		t.Fatalf("a search for subject=Group/123 did not return only obs-group: %d results", len(res.Resources))
	}
}

// TestAnUnqualifiedReferenceSearchMatchesAnyType keeps the other half of the specification.
//
// FHIR defines both forms. subject=123 is unqualified and matches on id alone, which is what a client sending a bare id expects and what
// a good deal of client code actually sends. Narrowing it to one type would break those clients silently, by returning fewer results
// rather than an error.
func TestAnUnqualifiedReferenceSearchMatchesAnyType(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{
		"resourceType": "Observation", "id": "obs-patient", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/123"}
	}`)
	put(t, store, `{
		"resourceType": "Observation", "id": "obs-group", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Group/123"}
	}`)

	res, err := store.Search(ctx, &SearchQuery{
		ResourceType: "Observation",
		// An explicit page size, because zero is a page size of zero rather than "no preference" - the caller
		// supplies the default, which is what keeps ParseSearch a pure function of the URL.
		Count:    10,
		Criteria: map[string][]string{"subject": {"123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != 2 {
		t.Errorf("an unqualified search for subject=123 returned %d, want both; the unqualified form matches "+
			"on id alone by definition", len(res.Resources))
	}
}

// TestEveryReferenceFormFindsTheResource covers the spellings real clients send.
//
// All four are the same reference. A server that matches only the form its own examples use is a server that works in testing and fails
// at a hospital, and this is the exact bug found earlier today with ?patient=Patient/123 returning an empty bundle and a 200.
func TestEveryReferenceFormFindsTheResource(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{
		"resourceType": "Observation", "id": "obs", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/123"}
	}`)

	for _, form := range []string{
		"123",
		"Patient/123",
		"http://example.org/fhir/Patient/123",
		"Patient/123/_history/2",
	} {
		res, err := store.Search(ctx, &SearchQuery{
			ResourceType: "Observation",
			Count:        10,
			Criteria:     map[string][]string{"subject": {form}},
		})
		if err != nil {
			t.Errorf("%s: %v", form, err)

			continue
		}
		if len(res.Resources) != 1 {
			t.Errorf("subject=%s found %d observations, want 1", form, len(res.Resources))
		}
	}
}

// TestAReferenceWithNoTypeIsNotGuessed covers the failure direction chosen deliberately.
//
// A bundle relying on fullUrl carries references with no type. Recording a guess would be the same defect in a new place: a reference
// recorded as pointing at a Patient when it does not is worse than one recorded as pointing at nothing, because the first is trusted.
func TestAReferenceWithNoTypeIsNotGuessed(t *testing.T) {
	if got := referenceType(&fhir.Reference{Reference: "123"}); got != "" {
		t.Errorf("a bare reference was recorded as pointing at %q", got)
	}
	// The path segment before an id is not always a type.
	if got := referenceType(&fhir.Reference{Reference: "http://host/fhir/123"}); got != "" {
		t.Errorf("a URL path segment was recorded as a resource type: %q", got)
	}
	// But the Type field is honoured when the string does not carry one, because that is a statement rather than a guess.
	if got := referenceType(&fhir.Reference{Reference: "123", Type: "Patient"}); got != "Patient" {
		t.Errorf("an explicit Type was ignored: %q", got)
	}
}

// TestAnOlderDatabaseIsReindexedNotLeftAmbiguous covers the migration.
//
// CREATE TABLE IF NOT EXISTS does nothing to an existing table, so a store written by an earlier build keeps the old shape. Leaving the
// new column empty on those rows has no safe meaning: read as "matches nothing" a type-qualified search silently stops finding older
// resources, and read as "matches anything" it returns the wrong type - which is the defect being fixed. So the rows are re-derived.
func TestAnOlderDatabaseIsReindexedNotLeftAmbiguous(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "fhir.db")

	// A store in the current shape, with a resource in it.
	first := openTestStore(t, path)
	put(t, first, `{
		"resourceType": "Observation", "id": "obs", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/123"}
	}`)

	// Now simulate the older shape: drop the column's contents as an upgrade from a build that never wrote them.
	if _, err := first.db.ExecContext(ctx, `UPDATE fhir_search SET ref_type = NULL`); err != nil {
		t.Fatal(err)
	}
	closeTestStore(t, first)

	// Reopening runs the migration path. The column already exists here, so the reindex is called directly - what is
	// being proven is that a reindex restores the types, not the ALTER itself.
	second := openTestStore(t, path)
	defer closeTestStore(t, second)

	if err := second.reindexAll(ctx); err != nil {
		t.Fatal(err)
	}

	res, err := second.Search(ctx, &SearchQuery{
		ResourceType: "Observation",
		// An explicit page size, because zero is a page size of zero rather than "no preference" - the caller
		// supplies the default, which is what keeps ParseSearch a pure function of the URL.
		Count:    10,
		Criteria: map[string][]string{"subject": {"Patient/123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != 1 {
		t.Errorf("after reindexing, a type-qualified search found %d resources, want 1; a resource written by "+
			"an earlier build is no longer findable", len(res.Resources))
	}
}

// TestAReindexRemovesRowsTheResourceNoLongerJustifies proves the index is re-derived rather than added to.
//
// A plant is why this exists. Deleting the clear-out step in reindexAll broke nothing, because the migration test only checked that the
// correct rows appeared - and they do, alongside the stale ones. What a reindex promises is that the index describes the resources and
// nothing else, and an index row nobody can account for is how a resource stays findable by a value it no longer has.
func TestAReindexRemovesRowsTheResourceNoLongerJustifies(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{
		"resourceType": "Observation", "id": "obs", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/123"}
	}`)

	// A row no current rule would produce: the shape an older build with different indexing rules leaves behind.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO fhir_search (resource_type, resource_id, param, value, system, ref_type)
		 VALUES ('Observation', 'obs', 'subject', '999', NULL, 'Patient')`); err != nil {
		t.Fatal(err)
	}

	// Findable by the wrong subject, which is the state being corrected.
	before, err := store.Search(ctx, &SearchQuery{
		ResourceType: "Observation", Count: 10,
		Criteria: map[string][]string{"subject": {"Patient/999"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Resources) != 1 {
		t.Fatalf("the stale row was not in effect, so this test would prove nothing: found %d",
			len(before.Resources))
	}

	if err := store.reindexAll(ctx); err != nil {
		t.Fatal(err)
	}

	after, err := store.Search(ctx, &SearchQuery{
		ResourceType: "Observation", Count: 10,
		Criteria: map[string][]string{"subject": {"Patient/999"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Resources) != 0 {
		t.Error("after reindexing, the observation is still findable by a subject it does not have")
	}

	// And it is still findable by the subject it does have, so the reindex rebuilt rather than merely deleted.
	kept, err := store.Search(ctx, &SearchQuery{
		ResourceType: "Observation", Count: 10,
		Criteria: map[string][]string{"subject": {"Patient/123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Resources) != 1 {
		t.Error("the reindex removed the correct rows along with the stale one")
	}
}
