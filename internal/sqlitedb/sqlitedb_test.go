package sqlitedb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenGivesSeparatePools(t *testing.T) {
	db := openTemp(t)

	if db.Write == db.Read {
		t.Fatal("the write and read handles should be different pools")
	}
	if got := db.Write.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("the write pool should hold one connection, got %d", got)
	}
	if got := db.Readers(); got < minReaders {
		t.Errorf("the read pool has %d connections, want at least %d", got, minReaders)
	}
}

func TestReadersAreBounded(t *testing.T) {
	// A very large read pool is not better. Each connection has its own page
	// cache, and a burst of expensive reports would then compete for memory
	// instead of for the disk.
	for _, requested := range []int{0, 1, 100, -5} {
		db, err := Open(filepath.Join(t.TempDir(), "x.db"), Options{Readers: requested})
		if err != nil {
			t.Fatal(err)
		}
		got := db.Readers()
		if got < minReaders || got > maxReaders {
			t.Errorf("Readers=%d produced a pool of %d, outside [%d,%d]",
				requested, got, minReaders, maxReaders)
		}
		db.Close()
	}
}

func TestWALIsEnabled(t *testing.T) {
	// Without WAL a reader and a writer block each other in SQLite itself, and
	// splitting the pools would achieve nothing.
	db := openTemp(t)

	var mode string
	if err := db.Write.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestTheReadPoolRefusesWrites(t *testing.T) {
	db := openTemp(t)

	if _, err := db.Write.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}

	// Opened read-only on purpose. A query that quietly became a write would run
	// on a pool sized for concurrency, and several of those would collide.
	if _, err := db.Read.Exec(`INSERT INTO t (v) VALUES ('x')`); err == nil {
		t.Error("the read pool should refuse a write")
	}
}

func TestReadsSeeCommittedWrites(t *testing.T) {
	db := openTemp(t)

	if _, err := db.Write.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write.Exec(`INSERT INTO t (v) VALUES ('hello')`); err != nil {
		t.Fatal(err)
	}

	// WAL gives readers a snapshot, so the thing to confirm is that a committed
	// write is visible rather than the reader sitting on a stale snapshot forever.
	var v string
	if err := db.Read.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "hello" {
		t.Errorf("the reader sees %q", v)
	}
}

// A write must succeed while a read is in progress, which is the whole reason for two pools.
//
// Asserted as an outcome, not a latency. Two earlier versions of this test measured how long a write took
// while scans ran - first against an absolute 25ms, then as a ratio to an idle write - and both failed on a
// GitHub runner with nothing wrong. The ratio was the more careful mistake: it assumed a slow disk scales
// both measurements equally, when the baseline is taken on an idle disk and the loaded figure on a saturated
// one. A throttled disk with no spare IOPS degrades far more under saturation than a fast one, so CI saw 33x
// where this machine sees 0.8x. That is real I/O contention and no connection pool can prevent it, so the
// number was never evidence about Perfuse.
//
// What the two pools actually guarantee is that a write is not refused or stalled by a reader holding the
// database. With a rollback journal or a single shared connection, a write attempted while a read
// transaction is open waits for the busy timeout and then fails SQLITE_BUSY. With WAL and a separate write
// pool it simply succeeds. That is a binary outcome and it is the same on any hardware.
//
// The settings that produce it are each asserted on their own by TestOpenGivesSeparatePools, TestWALIsEnabled
// and TestTheReadPoolRefusesWrites. This test is the one that proves the combination delivers the behaviour
// they exist for.
func TestAWriteSucceedsWhileAReadIsOpen(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if _, err := db.Write.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 500 {
		if _, err := db.Write.ExecContext(ctx, `INSERT INTO t (v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}

	// A read transaction, held open. This is the state a long report or an export puts the database in.
	rtx, err := db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("beginning a read transaction: %v", err)
	}
	defer func() { _ = rtx.Rollback() }()

	var n int
	if err := rtx.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("reading inside the transaction: %v", err)
	}
	if n != 500 {
		t.Fatalf("the read transaction sees %d rows, want 500", n)
	}

	// The write is on the acknowledgement path, so being refused here would mean refusing a message because
	// somebody was running a report. A generous timeout: this asserts the write is not blocked at all, and
	// anything under the busy timeout would still be a pass in SQLite's eyes, so the bound has to be well
	// below it to mean anything.
	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if _, err := db.Write.ExecContext(writeCtx, `INSERT INTO t (v) VALUES ('written during a read')`); err != nil {
		t.Fatalf("a write was refused while a read transaction was open, which is what two pools and WAL "+
			"exist to prevent: %v", err)
	}

	// And it is durably there, visible to a new reader.
	var after int
	if err := db.Read.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 501 {
		t.Errorf("after the write a new reader sees %d rows, want 501", after)
	}
}

func TestMemoryDatabaseSharesOnePool(t *testing.T) {
	// An in-memory database belongs to its connection. A second pool would be a
	// second, empty database, and a test against it would pass for the wrong
	// reason.
	db, err := Open(":memory:", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if db.Read != db.Write {
		t.Fatal("an in-memory database must share one handle")
	}

	if _, err := db.Write.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Read.Exec(`INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Errorf("the shared handle should accept a write: %v", err)
	}
}

func TestCloseIsSafeForAMemoryDatabase(t *testing.T) {
	db, err := Open(":memory:", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Both handles are the same pool, and closing it twice would report an error
	// that means nothing.
	if err := db.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	if _, err := Open("  ", Options{}); err == nil {
		t.Error("an empty path should be refused")
	}
}

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
