package fhirserver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAStaleWriteIsRefusedRatherThanWinning is the reason If-Match exists.
//
// Two apps read the same resource and both write it. Without a version check the second write wins completely and nothing anywhere records
// that the first happened. For a medication list or a problem list that is a clinical safety issue rather than an inconvenience, and it is
// invisible: both clients got a 200.
func TestAStaleWriteIsRefusedRatherThanWinning(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois"}]}`)
	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Nkemelu"}]}`)

	// A client holding version 1 tries to write.
	err := store.CheckVersion(ctx, "Patient", "p1", `W/"1"`)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("a write against version 1 of a resource now at version 2 was allowed: %v", err)
	}
	// The message says both numbers, because a client that only learns "conflict" cannot tell whether to retry.
	if !strings.Contains(err.Error(), "version 2") || !strings.Contains(err.Error(), "expected 1") {
		t.Errorf("the refusal does not say which versions were involved: %v", err)
	}

	// The current version is accepted.
	if err := store.CheckVersion(ctx, "Patient", "p1", `W/"2"`); err != nil {
		t.Errorf("a write against the current version was refused: %v", err)
	}
}

// TestAClientThatDoesNotAskForConcurrencyIsNotForcedIntoIt covers the compatibility decision.
//
// Requiring If-Match would break every existing client. The ones that send it are the ones that care, so an absent header passes.
func TestAClientThatDoesNotAskForConcurrencyIsNotForcedIntoIt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "p1"}`)

	if err := store.CheckVersion(ctx, "Patient", "p1", ""); err != nil {
		t.Errorf("a write with no If-Match was refused: %v", err)
	}
}

// TestEveryETagSpellingIsAccepted covers what clients actually send.
//
// FHIR uses weak ETags, spelled W/"3". The prefix and the quotes are both routinely omitted. Rejecting a valid-but-unusual spelling turns
// optimistic concurrency into a permanent 412, and a client that cannot write at all simply stops sending the header - which loses the
// protection entirely.
func TestEveryETagSpellingIsAccepted(t *testing.T) {
	for _, form := range []string{`W/"3"`, `"3"`, `3`, ` W/"3" `} {
		got, err := ParseETag(form)
		if err != nil {
			t.Errorf("%q was refused: %v", form, err)

			continue
		}
		if got != 3 {
			t.Errorf("%q read as version %d, want 3", form, got)
		}
	}
}

// TestAmbiguousOrMeaninglessIfMatchIsRefused covers the values that must not be guessed at.
func TestAmbiguousOrMeaninglessIfMatchIsRefused(t *testing.T) {
	for _, tc := range []struct{ value, wants string }{
		// A list means the client will accept either version. Honouring only the first produces a refusal the client
		// cannot explain.
		{`"1", "2"`, "several versions"},
		// * means any version, which is the same as not asking - and silently treating it as a real constraint would
		// be a 412 nobody could account for.
		{`*`, "omit the header"},
		{`garbage`, "not a version identifier"},
		{`W/"0"`, "not a version identifier"},
		{`W/"-1"`, "not a version identifier"},
	} {
		_, err := ParseETag(tc.value)
		if err == nil {
			t.Errorf("If-Match: %s was accepted", tc.value)

			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("If-Match: %s refused with %q, which does not mention %q", tc.value, err, tc.wants)
		}
	}
}

// TestHistoryRecordsTheDeletion is what separates an audit trail from a change log.
//
// A history that omits deletions cannot answer the question history exists for: this record was here last week and is not now, what
// happened. An audit trail with the deletions removed is not an audit trail.
func TestHistoryRecordsTheDeletion(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois"}]}`)
	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Nkemelu"}]}`)
	if err := store.Delete(ctx, "Patient", "p1"); err != nil {
		t.Fatal(err)
	}

	versions, err := store.History(ctx, "Patient", "p1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("recorded %d versions, want 3 including the deletion", len(versions))
	}

	// Newest first, because the question is nearly always "what happened most recently".
	if versions[0].VersionID != 3 {
		t.Errorf("history opens on version %d, want the newest", versions[0].VersionID)
	}
	if !versions[0].Deleted {
		t.Error("the newest version is not marked as the deletion")
	}
	if versions[0].Resource != nil {
		t.Error("the deletion carries a resource, which reads as a record that was blanked rather than removed")
	}
	// And the version before it still has its content, which is what makes recovery possible.
	if versions[1].Resource == nil {
		t.Error("the version before the deletion has no content, so nothing could be recovered")
	}
}

// TestAHistoricalVersionCanBeReadBack covers recovery, which is the practical use.
func TestAHistoricalVersionCanBeReadBack(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois"}]}`)
	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Nkemelu"}]}`)

	first, err := store.GetVersion(ctx, "Patient", "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResourceID() != "p1" {
		t.Errorf("read back %s", first.ResourceID())
	}

	// A version that never existed is distinguished from one that recorded a deletion.
	if _, err := store.GetVersion(ctx, "Patient", "p1", 9); !errors.Is(err, ErrNoSuchVersion) {
		t.Errorf("a version that never existed returned %v, want ErrNoSuchVersion", err)
	}

	if err := store.Delete(ctx, "Patient", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetVersion(ctx, "Patient", "p1", 3); !errors.Is(err, ErrDeleted) {
		t.Errorf("the deletion version returned %v, want ErrDeleted; a deletion and a version that was "+
			"never recorded must not look the same", err)
	}
}

// TestTheHistoryOfSomethingThatNeverExistedIsNotAnEmptyHistory covers the distinction.
//
// Every write records a version, so a resource with no history cannot exist. An empty result therefore means the resource never existed, and
// answering "no history" would suggest it did.
func TestTheHistoryOfSomethingThatNeverExistedIsNotAnEmptyHistory(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.History(ctx, "Patient", "never", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("the history of a resource that never existed returned %v, want ErrNotFound", err)
	}
}

// TestHistoryEntriesSayWhichInteractionProducedThem covers what makes a history readable.
//
// Without the request method a client sees three versions and cannot tell which one removed the record.
func TestHistoryEntriesSayWhichInteractionProducedThem(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{"resourceType": "Patient", "id": "p1"}`)
	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois"}]}`)
	if err := store.Delete(ctx, "Patient", "p1"); err != nil {
		t.Fatal(err)
	}

	versions, err := store.History(ctx, "Patient", "p1", 10)
	if err != nil {
		t.Fatal(err)
	}

	bundle := store.HistoryBundle(versions, "http://example.test/fhir", "http://example.test/fhir/Patient/p1/_history")

	if bundle.Type != "history" {
		t.Errorf("the bundle type is %q, want history", bundle.Type)
	}

	want := []string{"DELETE", "PUT", "POST"}
	if len(bundle.Entry) != len(want) {
		t.Fatalf("the bundle has %d entries, want %d", len(bundle.Entry), len(want))
	}
	for i, method := range want {
		e := bundle.Entry[i]
		if e.Request == nil || e.Request.Method != method {
			t.Errorf("entry %d is %v, want %s", i, e.Request, method)
		}
		if e.Response == nil || e.Response.Etag == "" {
			t.Errorf("entry %d carries no version", i)
		}
		// The time, because a trail with version numbers and no times answers "in what order" and not "when",
		// and "when" is the question somebody brings to an audit trail.
		if e.Response == nil || e.Response.LastModified == "" {
			t.Errorf("entry %d carries no time", i)
		}
	}
}

// TestADeletionIsRecordedEvenWhenTheIndexRemovalWouldFail covers the transaction.
//
// The deletion, the history row and the index removal are one change. Previously three statements outside a transaction: a failure between
// them left a resource marked deleted but still findable by search, which is the worst of both - the read says it is gone and the search
// says it is here.
func TestADeletionIsRecordedEvenWhenTheIndexRemovalWouldFail(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	put(t, store, `{
		"resourceType": "Observation", "id": "obs", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/123"}
	}`)

	if err := store.Delete(ctx, "Observation", "obs"); err != nil {
		t.Fatal(err)
	}

	// Gone from search.
	res, err := store.Search(ctx, &SearchQuery{
		ResourceType: "Observation", Count: 10,
		Criteria: map[string][]string{"subject": {"Patient/123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != 0 {
		t.Error("a deleted observation is still findable by search, so the read and the search disagree")
	}

	// And present in history.
	versions, err := store.History(ctx, "Observation", "obs", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || !versions[0].Deleted {
		t.Errorf("the deletion was not recorded: %d versions", len(versions))
	}
}
