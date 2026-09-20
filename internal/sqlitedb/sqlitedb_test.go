package sqlitedb

import (
	"context"
	"path/filepath"
	"sync"
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

func TestAReadDoesNotBlockAWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}

	db := openTemp(t)
	ctx := context.Background()

	if _, err := db.Write.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	// Enough rows that a full scan takes real time.
	tx, err := db.Write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20000 {
		if _, err := tx.Exec(`INSERT INTO t (v) VALUES (?)`,
			"a moderately long value to make the scan cost something, row"+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Baseline.
	start := time.Now()
	if _, err := db.Write.ExecContext(ctx, `INSERT INTO t (v) VALUES ('quiet')`); err != nil {
		t.Fatal(err)
	}
	quiet := time.Since(start)

	// Now hammer the read pool with scans and time a write against them.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var n int
				_ = db.Read.QueryRowContext(ctx,
					`SELECT count(*) FROM t WHERE v LIKE '%moderately%'`).Scan(&n)
			}
		}()
	}
	time.Sleep(30 * time.Millisecond)

	start = time.Now()
	if _, err := db.Write.ExecContext(ctx, `INSERT INTO t (v) VALUES ('busy')`); err != nil {
		t.Fatal(err)
	}
	busy := time.Since(start)

	close(stop)
	wg.Wait()

	t.Logf("write with the database idle: %s", quiet.Round(time.Microsecond))
	t.Logf("write with four scans running: %s", busy.Round(time.Microsecond))

	// The write is on the acknowledgement path. It is allowed to be slower under
	// load, but not by an order of magnitude, which is what a shared single
	// connection produced.
	if busy > 25*time.Millisecond {
		t.Errorf("a write took %s while reports were running: the pools are still competing",
			busy.Round(time.Millisecond))
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
