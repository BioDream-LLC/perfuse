package tefca

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An audit trail that vanishes on restart.
//
// TEFCA treats recording every exchange as a condition of participation, and the records have to exist when somebody
// asks - typically months later, prompted by a complaint or an investigation. An in-memory log satisfies every test,
// works perfectly in a demonstration, and loses everything the first time the process restarts. Nothing fails and
// nobody is told.
//
// That is worse than having no audit feature, because the presence of one is relied upon.

func TestTheAuditTrailSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "tefca.jsonl")

	log, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	p, err := NewParticipant(testConfig(), &log.AuditLog)
	if err != nil {
		t.Fatal(err)
	}
	_ = p

	// Recorded through the persistent log, which is what a real participant would hold.
	log.Record(TEFCAAudit{
		Direction: "outbound", ExchangeType: "query", Purpose: PurposeTreatment,
		PatientID: "P12345", RequestingOrg: "Example Hospital", Success: true,
	})
	log.Record(TEFCAAudit{
		Direction: "outbound", ExchangeType: "notification", Purpose: PurposeTreatment,
		PatientID: "P67890", RequestingOrg: "Example Hospital", Success: false,
		ErrorDetail: "the recipient did not accept it",
	})

	if err := log.LastError(); err != nil {
		t.Fatalf("the entries were not written: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// The restart.
	reopened, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	entries := reopened.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if len(entries) != 2 {
		t.Fatalf("after a restart the trail holds %d entries, want 2", len(entries))
	}

	// The details have to survive, not just the count. An entry with no patient and no purpose answers nothing.
	byType := map[string]TEFCAAudit{}
	for _, e := range entries {
		byType[e.ExchangeType] = e
	}

	q, ok := byType["query"]
	if !ok {
		t.Fatal("the query exchange did not survive")
	}
	if q.PatientID != "P12345" || q.Purpose != PurposeTreatment || q.RequestingOrg != "Example Hospital" {
		t.Errorf("the query entry lost detail: %+v", q)
	}
	if !q.Success {
		t.Error("a successful exchange came back as a failure")
	}

	n := byType["notification"]
	if n.Success {
		t.Error("a failed exchange came back as a success")
	}
	if !strings.Contains(n.ErrorDetail, "did not accept") {
		t.Errorf("the failure reason did not survive: %q", n.ErrorDetail)
	}
}

// The file must not be readable by everybody.
//
// An audit trail is itself sensitive: the fact that a named hospital queried a named patient is a disclosure, and a
// world-readable file of those is a worse leak than the exchanges it records.
func TestTheAuditFileIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tefca.jsonl")

	log, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	log.Record(TEFCAAudit{ExchangeType: "query", Purpose: PurposeTreatment, PatientID: "P1"})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the audit file is mode %o; it names patients and who asked about them", mode)
	}
}

// A partial final line must not make the whole trail unreadable.
//
// The last line of an append-only file can be a partial write from a process that died mid-entry. Refusing to start
// because of it would mean one unlucky crash makes the audit trail permanently unreadable, so the line is skipped and
// the loss is counted rather than hidden.
func TestAPartialFinalLineDoesNotDestroyTheTrail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tefca.jsonl")

	log, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	log.Record(TEFCAAudit{ExchangeType: "query", Purpose: PurposeTreatment, PatientID: "P1", Success: true})
	log.Record(TEFCAAudit{ExchangeType: "delivery", Purpose: PurposeTreatment, Success: true})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// A process that died halfway through writing.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"timestamp":"2026-08-2`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	reopened, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatalf("a partial line made the whole trail unreadable: %v", err)
	}
	defer reopened.Close()

	entries := reopened.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if len(entries) != 2 {
		t.Errorf("%d entries survived a partial final line, want 2", len(entries))
	}

	// The loss has to be visible. A site whose trail is partly unreadable needs to know.
	if reopened.Skipped() != 1 {
		t.Errorf("%d unreadable lines were reported, want 1", reopened.Skipped())
	}
}

// Appending after a restart must not overwrite what was there.
func TestEntriesAppendAcrossRestartsRatherThanReplacing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tefca.jsonl")

	for i := 0; i < 3; i++ {
		log, err := OpenAuditLog(path, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		log.Record(TEFCAAudit{ExchangeType: "query", Purpose: PurposeTreatment, Success: true})
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
	}

	final, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()

	entries := final.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if len(entries) != 3 {
		t.Errorf("three restarts each recording one exchange produced %d entries", len(entries))
	}
}

// The file keeps everything even when memory does not.
//
// The questions auditors ask are about last year; memory holds weeks. A retention window that dropped entries from the
// file as well would make the log useless for its actual purpose.
func TestTheFileKeepsEntriesTheMemoryWindowHasDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tefca.jsonl")

	// A one-nanosecond memory window, so anything recorded is immediately outside it.
	log, err := OpenAuditLog(path, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	log.Record(TEFCAAudit{
		ExchangeType: "query", Purpose: PurposeTreatment, PatientID: "P1", Success: true,
	})

	time.Sleep(2 * time.Millisecond)

	// Memory has dropped it.
	if got := len(log.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))); got != 0 {
		t.Errorf("memory holds %d entries with a one-nanosecond window", got)
	}

	// The file has not.
	all, err := log.ReadAll(context.Background(), time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("the file holds %d entries, want 1 — the record is gone", len(all))
	}
	if all[0].PatientID != "P1" {
		t.Errorf("the entry lost its patient: %+v", all[0])
	}
}

// A log with no path must be refused rather than silently discarding.
func TestAnAuditLogWithNoPathIsRefused(t *testing.T) {
	if _, err := OpenAuditLog("", time.Hour); err == nil {
		t.Fatal("an audit log with nowhere to write was created")
	}
}

// A failure to write must be reported, not swallowed.
//
// A site whose audit file has become unwritable is out of compliance and needs to know today, not when somebody asks
// for records and finds a gap.
func TestAFailureToWriteIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tefca.jsonl")

	log, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Closed underneath, which is what a full disk or a revoked permission looks like from here.
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	if err := log.RecordErr(TEFCAAudit{ExchangeType: "query", Purpose: PurposeTreatment}); err == nil {
		t.Fatal("writing to a closed audit log reported success")
	}

	// And the entry is still in memory, because an exchange that happened is a fact regardless of whether the file
	// took it.
	if got := len(log.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))); got != 1 {
		t.Errorf("the entry was lost from memory as well as from the file (%d held)", got)
	}
}

// Entries out of order in the file must be sorted on load.
//
// A file that has been concatenated or restored from parts may not be in order, and an audit view that jumps about in
// time is one nobody trusts.
func TestEntriesAreSortedOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tefca.jsonl")

	now := time.Now().UTC()
	lines := strings.Join([]string{
		`{"timestamp":"` + now.Format(time.RFC3339Nano) + `","exchangeType":"third","purpose":"treatment"}`,
		`{"timestamp":"` + now.Add(-2*time.Minute).Format(time.RFC3339Nano) + `","exchangeType":"first","purpose":"treatment"}`,
		`{"timestamp":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","exchangeType":"second","purpose":"treatment"}`,
	}, "\n") + "\n"

	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	log, err := OpenAuditLog(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	entries := log.Query(now.Add(-time.Hour), now.Add(time.Hour))
	if len(entries) != 3 {
		t.Fatalf("%d entries loaded, want 3", len(entries))
	}
	for i, want := range []string{"first", "second", "third"} {
		if entries[i].ExchangeType != want {
			t.Errorf("entry %d is %q, want %q — the trail is out of order", i, entries[i].ExchangeType, want)
		}
	}
}

// Reading a range from a file that does not exist must give an empty result, not an error.
//
// A site that has made no exchanges yet has no file, and reporting that as a failure would make a fresh installation
// look broken.
func TestReadingAnAbsentAuditFileIsNotAnError(t *testing.T) {
	log := &PersistentAuditLog{path: filepath.Join(t.TempDir(), "never-written.jsonl")}

	got, err := log.ReadAll(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("reading an absent audit file failed: %v", err)
	}
	if got == nil {
		t.Error("the result is nil rather than an empty slice")
	}
}
