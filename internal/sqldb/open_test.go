package sqldb

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOpenAcceptsAWorkingDatabase guards the other direction. Without it, a ping that
// rejected everything would pass the test above.
func TestOpenAcceptsAWorkingDatabase(t *testing.T) {
	db, err := Open("sqlite", filepath.Join(t.TempDir(), "ok.db"), 2, 5*time.Second)
	if err != nil {
		t.Fatalf("Open rejected a usable SQLite file: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("the returned pool does not work: %v", err)
	}
}

// TestOpenActuallyAppliesThePragmas goes through Open and reads the settings back off
// the live connection.
//
// This test exists because the unit test below did not catch a deliberate break.
// Removing the withSQLitePragmas call from Open left every other test in this file
// passing: they prove the helper computes the right DSN, not that anything uses it. A
// correct function nobody calls is the same defect as a wrong one, and it is invisible
// to a test that calls the function directly.
func TestOpenActuallyAppliesThePragmas(t *testing.T) {
	db, err := Open("sqlite", filepath.Join(t.TempDir(), "applied.db"), 2, 3*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("reading journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode is %q, not WAL: a concurrent reader will be blocked by a writer", mode)
	}

	var busy int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("reading busy_timeout: %v", err)
	}
	if busy != 3000 {
		t.Errorf("busy_timeout is %d, not the 3000ms the timeout argument asked for: "+
			"a concurrent write will return SQLITE_BUSY instead of waiting", busy)
	}
}

// TestSQLiteGetsWALAndABusyTimeout checks the pragmas this package went without while
// two others in the tree set them.
//
// Their absence is invisible until two connections write at once, when SQLite returns
// SQLITE_BUSY rather than waiting. That surfaced as an intermittent CI failure, which
// is the most expensive way to find it.
func TestSQLiteGetsWALAndABusyTimeout(t *testing.T) {
	got := pragmas(t, withSQLitePragmas("file:/tmp/x.db", 5*time.Second))

	for _, want := range []string{"journal_mode(WAL)", "busy_timeout(5000)"} {
		if !hasPragma(got, want) {
			t.Errorf("the DSN is missing %s, so concurrent access will fail rather than wait: %v", want, got)
		}
	}
}

// TestABarePathBecomesAURL covers the case that would otherwise silently skip the
// pragmas: a plain filesystem path is a valid SQLite DSN and has nowhere to put a
// query string.
func TestABarePathBecomesAURL(t *testing.T) {
	got := withSQLitePragmas("/var/lib/perfuse/staging.db", time.Second)

	if !strings.HasPrefix(got, "file:") {
		t.Errorf("a bare path was not turned into a file: URL, so the pragmas are not applied: %s", got)
	}
	if !strings.Contains(got, "/var/lib/perfuse/staging.db") {
		t.Errorf("the path was lost: %s", got)
	}
}

// TestCallerPragmasAreNotOverridden matters because modernc's driver applies repeated
// _pragma values in order, so appending unconditionally would quietly beat a
// deliberate choice rather than conflicting with it visibly.
func TestCallerPragmasAreNotOverridden(t *testing.T) {
	got := pragmas(t, withSQLitePragmas("file:x.db?_pragma=busy_timeout(99)", 5*time.Second))

	if hasPragma(got, "busy_timeout(5000)") {
		t.Errorf("the caller's busy_timeout was overridden: %v", got)
	}
	if !hasPragma(got, "busy_timeout(99)") {
		t.Errorf("the caller's busy_timeout was lost: %v", got)
	}
	// The one they did not set is still added.
	if !hasPragma(got, "journal_mode(WAL)") {
		t.Errorf("setting one pragma suppressed the other: %v", got)
	}
}

// TestAnUnparseableQueryIsLeftAlone records the deliberate choice to return the DSN
// untouched rather than discard a query string this function cannot read.
func TestAnUnparseableQueryIsLeftAlone(t *testing.T) {
	const bad = "file:x.db?%zz"

	if got := withSQLitePragmas(bad, time.Second); got != bad {
		t.Errorf("an unparseable query was rewritten, losing what the caller set: %s", got)
	}
}

// pragmas parses a DSN and returns its _pragma values, decoded.
//
// Asserting on the raw string would be asserting on url.Values escaping, which encodes
// parentheses. The driver decodes them - checked by opening a real database and reading
// journal_mode and busy_timeout back - so the escaping is not the thing under test.
func pragmas(t *testing.T, dsn string) []string {
	t.Helper()

	_, query, _ := strings.Cut(dsn, "?")
	v, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("the DSN this function produced is not parseable: %s", dsn)
	}
	return v["_pragma"]
}

func hasPragma(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}
