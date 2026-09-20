// Package sqlitedb opens SQLite the way a server needs it: one connection for
// writes and several for reads.
//
// The reason is a failure mode that only appears once a deployment has been
// running for a while. SQLite in WAL mode lets readers run alongside a writer,
// but Go's connection pool sits above SQLite, so a pool of one serialises
// everything regardless of what the database would allow. A message browser
// search scanning the payload column then holds the only connection, and every
// arriving message waits behind it.
//
// Measured on this machine, a write takes 104µs with the store idle and 2.6ms
// while a search scans four thousand messages: twenty-five times slower. That
// scales with the table, so at a few hundred thousand stored messages a report
// turns into seconds of delivery latency, and a write is on the acknowledgement
// path. The sender sees a timeout, resends, and somebody spends a morning
// looking for a problem in the network.
//
// Splitting the pools costs nothing and removes the interaction: reports get
// their own connections, and the writer keeps one to itself because SQLite
// permits exactly one writer anyway.
package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

// DB holds the two pools. Both point at the same file.
type DB struct {
	// Write is limited to a single connection, because SQLite takes one writer at
	// a time and queuing beats "database is locked".
	Write *sql.DB

	// Read carries several connections. WAL lets them run while a write is in
	// progress, so a report does not stall delivery.
	Read *sql.DB

	// Path is the file, or ":memory:".
	Path string

	memory bool
}

// Options tune the pools. The defaults suit a server.
type Options struct {
	// Readers is the size of the read pool. Defaults to the number of CPUs,
	// bounded to a sensible range: below two there is no point splitting, and far
	// above that a burst of expensive reports would compete for memory rather
	// than for the disk.
	Readers int

	// BusyTimeoutMS is how long a statement waits for a lock before failing.
	// Defaults to 5000. A brief lock should be a wait, not an error.
	BusyTimeoutMS int

	// ForeignKeys enables enforcement. Defaults to on.
	ForeignKeys *bool
}

const (
	defaultBusyTimeoutMS = 5000
	minReaders           = 2
	maxReaders           = 8
)

// Open prepares both pools for the database at path.
//
// ":memory:" is supported for tests, and is deliberately given a single shared
// connection: an in-memory database belongs to its connection, so a second one
// would silently be a different, empty database. That is the kind of thing that
// makes a test pass for the wrong reason.
func Open(path string, opts Options) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("sqlitedb: no path")
	}

	if opts.BusyTimeoutMS <= 0 {
		opts.BusyTimeoutMS = defaultBusyTimeoutMS
	}
	if opts.Readers <= 0 {
		opts.Readers = runtime.NumCPU()
	}
	opts.Readers = min(max(opts.Readers, minReaders), maxReaders)

	foreignKeys := true
	if opts.ForeignKeys != nil {
		foreignKeys = *opts.ForeignKeys
	}

	if isMemory(path) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetConnMaxLifetime(0)
		if err := db.PingContext(context.Background()); err != nil {
			db.Close()
			return nil, err
		}
		// Both handles are the same pool. Closing is idempotent below.
		return &DB{Write: db, Read: db, Path: path, memory: true}, nil
	}

	write, err := sql.Open("sqlite", dsn(path, opts.BusyTimeoutMS, foreignKeys, false))
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	write.SetConnMaxLifetime(0)

	// WAL is set on the write handle before any reader connects. It is a
	// persistent property of the file, so setting it once is enough, but it has to
	// happen first: a reader opening a rollback-journal database would take a lock
	// that blocks the change.
	if err := write.PingContext(context.Background()); err != nil {
		write.Close()
		return nil, err
	}

	read, err := sql.Open("sqlite", dsn(path, opts.BusyTimeoutMS, foreignKeys, true))
	if err != nil {
		write.Close()
		return nil, err
	}
	read.SetMaxOpenConns(opts.Readers)
	read.SetMaxIdleConns(opts.Readers)
	read.SetConnMaxLifetime(0)

	if err := read.PingContext(context.Background()); err != nil {
		write.Close()
		read.Close()
		return nil, err
	}

	return &DB{Write: write, Read: read, Path: path}, nil
}

// Close releases both pools.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	var first error
	if d.Write != nil {
		if err := d.Write.Close(); err != nil {
			first = err
		}
	}
	// When the two are the same handle, as they are for an in-memory database,
	// closing twice would report an error that means nothing.
	if d.Read != nil && !d.memory {
		if err := d.Read.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Readers reports the size of the read pool, for logging and for the metrics
// page. Somebody diagnosing slow reports wants to know this number.
func (d *DB) Readers() int {
	if d == nil || d.Read == nil {
		return 0
	}
	return d.Read.Stats().MaxOpenConnections
}

func dsn(path string, busyMS int, foreignKeys, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyMS))
	if foreignKeys {
		q.Add("_pragma", "foreign_keys(1)")
	}
	if readOnly {
		// The read pool is opened read-only so a query that was meant to be a
		// query cannot quietly become a write on a connection sized for
		// concurrency. SQLite would allow several of those to collide.
		q.Set("mode", "ro")
	}
	return "file:" + path + "?" + q.Encode()
}

func isMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, "mode=memory")
}
