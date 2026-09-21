package msgstore

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestASlowReadDoesNotBlockWrites is the scaling question that actually matters.
//
// SQLite in WAL mode lets readers run alongside a writer. But a Go pool limited to a
// single connection serialises everything at a level above SQLite: a dashboard query
// scanning a large message table holds the only connection, and every arriving message
// waits behind it.
//
// That failure mode is indistinguishable from the one the Mirth forums describe as
// "processing stalls each morning" — which is exactly when somebody opens the dashboard
// to look at the overnight batch.
//
// # Asserted as an ordering, not a duration
//
// This test used to require the write to finish inside 10ms. It failed on a GitHub
// runner at 21.5ms, where the idle write alone took 3.8ms: the slowdown was 5.7x, well
// within reason, but the absolute figure was over the line. A throttled disk makes every
// number larger and the limit was calibrated on a fast one.
//
// A ratio does not fix it either, which was tried in internal/sqlitedb and failed for a
// subtler reason: the baseline is measured on an idle disk and the loaded figure on a
// saturated one, so a disk with no spare IOPS degrades far more under load than a fast
// one and the ratio is not hardware-independent at all.
//
// What the split pool guarantees is an ordering. A long read and a write are started
// together; with two pools the write completes while the read is still going, and with
// one shared connection the write cannot even begin until the read has finished and
// released it. Which of the two finishes first is decided by the architecture, not by how
// fast the machine is, and both are measured on the same machine at the same moment.
func TestASlowReadDoesNotBlockWrites(t *testing.T) {
	store, cleanup := splitPoolStore(t)
	defer cleanup()

	rec := NewRecorder(store, quietBenchLogger())
	ctx := context.Background()

	// Enough messages that a full scan takes long enough for the ordering to be
	// unambiguous rather than a coin toss.
	const seed = 4000
	for i := range seed {
		rec.RecordMessage(ctx, benchRecord("busy", i))
	}

	readHolding := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan struct{})

	go func() {
		defer close(readDone)

		// An explicit transaction, so the connection is held for the whole time rather
		// than returned to the pool between statements.
		//
		// The first version of this ran eight separate List calls, which released the
		// connection after each one. A write only had to wait for one scan and still
		// finished first, so the test passed with the pools deliberately merged - it was
		// asserting nothing. Found by breaking it on purpose.
		tx, err := store.querier().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Error(err)
			close(readHolding)
			return
		}
		defer func() { _ = tx.Rollback() }()

		// Search forces a scan of the payloads rather than an index lookup, which is what
		// a message browser search really does.
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM messages WHERE channel = 'busy' AND raw LIKE '%Vestavia%'`).Scan(&n); err != nil {
			t.Error(err)
			close(readHolding)
			return
		}

		close(readHolding)
		<-releaseRead
	}()

	<-readHolding

	// The write must not need the connection the report is holding. A short deadline, so a
	// write that has to queue behind the reader fails here rather than waiting for it.
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err := store.Record(writeCtx, &Message{
		Channel: "busy", ControlID: "DURING-REPORT", MessageType: "ADT",
		TriggerEvent: "A01", Outcome: Delivered, ReceivedAt: time.Now().UTC(),
	})
	elapsed := time.Since(start)

	close(releaseRead)
	<-readDone

	t.Logf("write while a report held a read transaction: %s", elapsed.Round(time.Microsecond))

	// A write is on the acknowledgement path. Blocking it behind a report is how a
	// sender's timeout becomes our outage.
	if err != nil {
		t.Fatalf("a write was blocked while a report held a read transaction: reads and writes are "+
			"competing for the same connection, so opening the dashboard delays message delivery: %v", err)
	}

	// And the write is really there, so this cannot pass by not writing anything. The
	// total is checked rather than the returned page, because List clamps Limit to
	// MaxLimit and a page is never longer than 1000 however many messages exist.
	_, total, err := store.List(ctx, Query{Channel: "busy", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != seed+1 {
		t.Errorf("the store holds %d messages, want %d: the write under test did not land", total, seed+1)
	}
}
