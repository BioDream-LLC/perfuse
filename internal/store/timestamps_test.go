package store

import (
	"context"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Old rows are rewritten to a fixed width, so history sorts correctly too.
//
// Writing new rows fixed-width fixes the future. It does not fix an audit log that already contains
// variable-width timestamps, and an audit trail is consulted precisely when somebody is reconstructing
// past events - so leaving history unfixed would mean the defect survived in exactly the data it
// mattered for.
//
// The rows planted here are the shape the defect produced: fractions with trailing zeros dropped, so
// text comparison put the earlier entry last.
func TestMigrationNormalisesExistingTimestampWidths(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	// Written directly, bypassing Audit, so they carry the old variable-width form regardless of what
	// the current code would produce.
	//
	// Deliberately inserted in the wrong text order: ".12345Z" sorts above ".123456Z" as text, which is
	// the whole defect, so before the migration a newest-first query returns them reversed.
	planted := []struct{ at, action string }{
		{"2026-08-25T20:56:33Z", "first"},           // no fraction at all
		{"2026-08-25T20:56:33.12345Z", "second"},    // 123450us, trailing zero dropped
		{"2026-08-25T20:56:33.123456Z", "third"},    // 123456us
		{"2026-08-25T20:56:33.5Z", "fourth"},        // 500ms
		{"2026-08-25T20:56:34.000000001Z", "fifth"}, // already the target width
	}
	for _, p := range planted {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO audit (at, username, action, tenant_id) VALUES (?, 'someone', ?, 'main')`,
			p.at, p.action); err != nil {
			t.Fatal(err)
		}
	}

	// Re-running the migration is what a real upgrade does, and it must be safe to run over rows that
	// are already correct.
	if err := s.applyMigration(ctx, 99, migrationNamed(t, "fixed-width-timestamps")); err != nil {
		t.Fatalf("applying the normalising migration: %v", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT at, action FROM audit ORDER BY at ASC, id ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	var order []string
	for rows.Next() {
		var at, action string
		if err := rows.Scan(&at, &action); err != nil {
			t.Fatal(err)
		}
		if len(at) != 30 {
			t.Errorf("%s is %d characters after the migration, want 30", at, len(at))
		}
		if _, err := dbtime.Parse(at); err != nil {
			t.Errorf("%s no longer parses as a timestamp: %v", at, err)
		}
		order = append(order, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Oldest first, which is what the planted instants actually are.
	want := []string{"first", "second", "third", "fourth", "fifth"}
	if len(order) != len(want) {
		t.Fatalf("got %d rows, want %d", len(order), len(want))
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("sorting the migrated text gives %v, want %v", order, want)
		}
	}
}

// Running it twice changes nothing, because an upgrade may be interrupted and retried.
func TestNormalisingMigrationIsIdempotent(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.Audit(ctx, AuditEntry{Username: "someone", Action: "login"}); err != nil {
		t.Fatal(err)
	}

	last := migrationNamed(t, "fixed-width-timestamps")
	var before string
	if err := s.db.QueryRowContext(ctx, `SELECT at FROM audit LIMIT 1`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := s.applyMigration(ctx, 99, last); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	var after string
	if err := s.db.QueryRowContext(ctx, `SELECT at FROM audit LIMIT 1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("running the migration again changed %s into %s", before, after)
	}
}

// And the ordering property end to end, through Audit and ListAudit rather than through SQL.
//
// This is the test that was failing intermittently before the format changed. Repeated, because one
// pass proved nothing when roughly one adjacent pair in ten was affected.
func TestAuditOrderIsStableAcrossManyWrites(t *testing.T) {
	for attempt := 0; attempt < 200; attempt++ {
		s := open(t)
		ctx := context.Background()

		for _, action := range []string{"one", "two", "three", "four", "five"} {
			if err := s.Audit(ctx, AuditEntry{Username: "someone", Action: action}); err != nil {
				t.Fatal(err)
			}
			// No sleep. Writing as fast as possible is the condition that exposed the defect, because it
			// puts every entry in the same fraction of a second.
		}

		got, err := s.ListAudit(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 {
			t.Fatalf("attempt %d: got %d entries, want 5", attempt, len(got))
		}
		if got[0].Action != "five" || got[4].Action != "one" {
			actions := make([]string, len(got))
			for i, e := range got {
				actions[i] = e.Action
			}
			t.Fatalf("attempt %d: newest-first order is %v, want five..one", attempt, actions)
		}
		if !got[0].At.After(got[4].At) && !got[0].At.Equal(got[4].At) {
			t.Errorf("attempt %d: the newest entry is not the latest instant", attempt)
		}
		_ = time.Now
	}
}

// migrationNamed finds a migration by name.
//
// These tests used to reach for migrations[len(migrations)-1], which was the normalising migration only for as long as nothing was
// appended after it. Adding an unrelated table made both of them silently test the wrong migration, and they failed with a message
// about timestamp widths - which sends the next reader to the timestamp code rather than to the index.
func migrationNamed(t *testing.T, name string) migration {
	t.Helper()

	for _, m := range migrations {
		if m.name == name {
			return m
		}
	}

	t.Fatalf("no migration named %q", name)

	return migration{}
}
