package fhirserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// exportFixture prepares a store with an export manager and a few resources.
func exportFixture(t *testing.T) (*Store, *ExportManager) {
	t.Helper()

	store := newTestStore(t)

	manager, err := NewExportManager(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}

	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Dubois"}]}`)
	put(t, store, `{"resourceType": "Patient", "id": "p2", "name": [{"family": "Nkemelu"}]}`)
	put(t, store, `{
		"resourceType": "Observation", "id": "o1", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p1"}
	}`)
	put(t, store, `{
		"resourceType": "Observation", "id": "o2", "status": "final",
		"code": {"coding": [{"code": "8867-4"}]},
		"subject": {"reference": "Patient/p2"}
	}`)

	return store, manager
}

// waitForExport polls a job until it stops running.
//
// Polls rather than sleeps, because a fixed sleep is either flaky or slow and on a loaded machine it is both.
func waitForExport(t *testing.T, m *ExportManager, id string) *ExportJob {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := m.Job(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != ExportRunning {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("the export did not finish within ten seconds")

	return nil
}

// TestAnExportProducesOneFilePerTypeThatHasAnything is the operation working.
func TestAnExportProducesOneFilePerTypeThatHasAnything(t *testing.T) {
	_, manager := exportFixture(t)

	started, err := manager.Start(context.Background(), ExportRequest{RequestURL: "/$export"})
	if err != nil {
		t.Fatal(err)
	}

	job := waitForExport(t, manager, started.ID)
	if job.Status != ExportComplete {
		t.Fatalf("the export did not complete: %s %s", job.Status, job.Error)
	}

	got := map[string]int{}
	for _, f := range job.Files {
		got[f.ResourceType] = f.Count
	}

	if got["Patient"] != 2 {
		t.Errorf("exported %d patients, want 2", got["Patient"])
	}
	if got["Observation"] != 2 {
		t.Errorf("exported %d observations, want 2", got["Observation"])
	}

	// Types with nothing in them produce no file at all.
	//
	// The specification says to omit them and the reason is worth keeping: a manifest listing twelve files of which nine
	// are empty makes a client fetch nine times for nothing, and an empty file cannot be told apart from a type whose
	// export failed.
	if _, ok := got["Immunization"]; ok {
		t.Error("a type with no resources produced a file")
	}
}

// TestAnExportFileIsOneResourcePerLine covers the format.
//
// A stored resource may be indented, and an indented resource in an NDJSON file breaks the format in a way visible only to the consumer -
// which is to say, after it has been handed over.
func TestAnExportFileIsOneResourcePerLine(t *testing.T) {
	_, manager := exportFixture(t)

	started, err := manager.Start(context.Background(), ExportRequest{RequestURL: "/$export"})
	if err != nil {
		t.Fatal(err)
	}
	waitForExport(t, manager, started.ID)

	body, err := manager.File(context.Background(), started.ID, "Patient")
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("the file has %d lines, want 2", len(lines))
	}
	for i, line := range lines {
		if strings.Contains(line, "\n") || strings.HasPrefix(line, " ") {
			t.Errorf("line %d is not compact", i)
		}
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			t.Errorf("line %d is not one JSON object: %q", i, line)
		}
	}
}

// TestAPatientLimitedExportContainsOnlyThatPatient is the security property.
//
// A token restricted to one patient asking for everything must not receive everything. Narrowed in the query rather than filtered afterwards,
// so another patient's records are never loaded at all - a filter applied after the fact is one refactor away from being skipped.
func TestAPatientLimitedExportContainsOnlyThatPatient(t *testing.T) {
	_, manager := exportFixture(t)

	started, err := manager.Start(context.Background(), ExportRequest{
		RequestURL: "/Patient/$export",
		PatientID:  "p1",
	})
	if err != nil {
		t.Fatal(err)
	}

	job := waitForExport(t, manager, started.ID)
	if job.Status != ExportComplete {
		t.Fatalf("the export did not complete: %s", job.Error)
	}

	patients, err := manager.File(context.Background(), started.ID, "Patient")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(patients, "Nkemelu") {
		t.Error("a single-patient export contains another patient's record")
	}
	if !strings.Contains(patients, "Dubois") {
		t.Error("a single-patient export does not contain the patient it was for")
	}

	observations, err := manager.File(context.Background(), started.ID, "Observation")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(observations, `"o2"`) {
		t.Error("a single-patient export contains an observation about another patient")
	}
	if !strings.Contains(observations, `"o1"`) {
		t.Error("a single-patient export is missing the patient's own observation")
	}
}

// TestAnExportNarrowedByTypeExportsOnlyThat covers _type.
func TestAnExportNarrowedByTypeExportsOnlyThat(t *testing.T) {
	_, manager := exportFixture(t)

	started, err := manager.Start(context.Background(), ExportRequest{
		RequestURL: "/$export?_type=Patient",
		Types:      []string{"Patient"},
	})
	if err != nil {
		t.Fatal(err)
	}

	job := waitForExport(t, manager, started.ID)
	if len(job.Files) != 1 || job.Files[0].ResourceType != "Patient" {
		t.Errorf("a Patient-only export produced %d files: %+v", len(job.Files), job.Files)
	}
}

// TestAnUnsupportedTypeIsRefusedBeforeTheJobStarts keeps a doomed job from being accepted.
//
// Accepting it would give the client a 202 and a URL to poll, and the failure would arrive minutes later as a 500 - when it could have been
// a 400 immediately.
func TestAnUnsupportedTypeIsRefusedBeforeTheJobStarts(t *testing.T) {
	_, manager := exportFixture(t)

	_, err := manager.Start(context.Background(), ExportRequest{Types: []string{"Nonsense"}})
	if err == nil {
		t.Fatal("an export of a type this server does not hold was accepted")
	}
	if !strings.Contains(err.Error(), "Nonsense") {
		t.Errorf("the refusal does not name the type: %v", err)
	}
}

// TestTheManifestTimeIsWhenTheExportWasRequested covers a subtle correctness point.
//
// A consumer uses transactionTime as the _since of its next export. Reporting the completion time would silently skip every resource written
// while the export was running, and the gap would never be noticed because each export looks complete on its own.
func TestTheManifestTimeIsWhenTheExportWasRequested(t *testing.T) {
	_, manager := exportFixture(t)

	before := time.Now().UTC()

	started, err := manager.Start(context.Background(), ExportRequest{RequestURL: "/$export"})
	if err != nil {
		t.Fatal(err)
	}
	job := waitForExport(t, manager, started.ID)

	if job.RequestedAt.After(job.CompletedAt) {
		t.Errorf("the request time %v is after the completion time %v", job.RequestedAt, job.CompletedAt)
	}
	if job.RequestedAt.Before(before.Add(-time.Minute)) {
		t.Errorf("the request time %v is implausible", job.RequestedAt)
	}
}

// TestDeletingAnExportRemovesItsFiles covers the residue.
//
// The files hold patient data. Files without a job are data nothing will serve and nothing will clean up, which is the worst possible residue -
// so both go in one transaction.
func TestDeletingAnExportRemovesItsFiles(t *testing.T) {
	ctx := context.Background()
	_, manager := exportFixture(t)

	started, err := manager.Start(ctx, ExportRequest{RequestURL: "/$export"})
	if err != nil {
		t.Fatal(err)
	}
	waitForExport(t, manager, started.ID)

	if err := manager.Delete(ctx, started.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Job(ctx, started.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the job survived deletion: %v", err)
	}
	if _, err := manager.File(ctx, started.ID, "Patient"); !errors.Is(err, ErrNotFound) {
		t.Error("the export file survived deletion, so patient data is left with nothing to serve or clean it")
	}

	// Deleting twice is an error rather than silently fine, because a client deleting an export it does not own should
	// not be told it succeeded.
	if err := manager.Delete(ctx, started.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a job that is already gone returned %v", err)
	}
}

// TestAJobInterruptedByARestartIsFailedNotLeftRunning covers the state that cannot be recovered.
//
// Nothing recorded how far a running job got, so it cannot be resumed. Leaving it as running means a client polls forever against work nobody
// is doing, which looks like the server hanging.
func TestAJobInterruptedByARestartIsFailedNotLeftRunning(t *testing.T) {
	ctx := context.Background()
	store, manager := exportFixture(t)

	// A job stuck in running, as a killed process leaves one.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO fhir_export_jobs (id, status, requested_at, request_url)
		 VALUES ('stuck', ?, ?, '/$export')`,
		ExportRunning, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	// A second manager over the same store is what a restart looks like.
	if _, err := NewExportManager(ctx, store); err != nil {
		t.Fatal(err)
	}

	job, err := manager.Job(ctx, "stuck")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != ExportFailed {
		t.Errorf("an interrupted job is %q, so a client would poll it forever", job.Status)
	}
	if !strings.Contains(job.Error, "restarted") {
		t.Errorf("the failure does not say what happened: %q", job.Error)
	}
}

// TestExportIdentifiersAreNotGuessable covers what actually guards an export.
//
// The status URL is the only thing standing between a client and the files. A sequential identifier would let anyone holding one enumerate
// every other export on the server.
func TestExportIdentifiersAreNotGuessable(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 100; i++ {
		id := newExportID()
		if len(id) < 32 {
			t.Fatalf("an export identifier is only %d characters", len(id))
		}
		if seen[id] {
			t.Fatalf("two export identifiers collided within a hundred: %s", id)
		}
		seen[id] = true
	}
}

// TestAnIndentedStoredResourceIsCompactedOnExport covers the compaction where it can actually matter.
//
// A plant showed the format test proved nothing about this: the store writes compact JSON, so nothing in a normal fixture is indented and
// removing the compaction changed no output. The case arises with content written by another build, by hand, or by a restore - and an indented
// resource spread over twelve lines makes twelve invalid NDJSON records out of one valid resource.
func TestAnIndentedStoredResourceIsCompactedOnExport(t *testing.T) {
	ctx := context.Background()
	store, manager := exportFixture(t)

	indented := "{\n  \"resourceType\": \"Patient\",\n  \"id\": \"pretty\",\n  \"name\": [\n    {\n      \"family\": \"Indented\"\n    }\n  ]\n}"

	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO fhir_resources (resource_type, resource_id, version_id, last_updated, deleted, content)
		 VALUES ('Patient', 'pretty', 1, ?, 0, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), indented); err != nil {
		t.Fatal(err)
	}

	started, err := manager.Start(ctx, ExportRequest{Types: []string{"Patient"}, RequestURL: "/$export"})
	if err != nil {
		t.Fatal(err)
	}
	waitForExport(t, manager, started.ID)

	body, err := manager.File(ctx, started.ID, "Patient")
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("the file has %d lines for three patients, so an indented resource was written across "+
			"several: %d", len(lines), len(lines))
	}

	found := false
	for _, line := range lines {
		if strings.Contains(line, "Indented") {
			found = true
			if strings.Contains(line, "  ") {
				t.Errorf("the indented resource was not compacted: %q", line)
			}
		}
	}
	if !found {
		t.Error("the indented resource is missing from the export")
	}
}
