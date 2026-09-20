package msgstore

import (
	"context"
	"testing"
	"time"
)

// TestASlowReadDoesNotBlockWrites is the scaling question that actually matters.
//
// SQLite in WAL mode lets readers run alongside a writer. But the Go pool is
// limited to a single connection, which serialises everything at a level above
// SQLite: a dashboard query scanning a large message table holds the only
// connection, and every arriving message waits behind it.
//
// That failure mode is indistinguishable from the one the Mirth forums describe
// as "processing stalls each morning" — which is exactly when somebody opens the
// dashboard to look at the overnight batch.
func TestASlowReadDoesNotBlockWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}

	store, cleanup := splitPoolStore(t)
	defer cleanup()

	rec := NewRecorder(store, quietBenchLogger())
	ctx := context.Background()

	// Enough messages that a full scan takes measurable time.
	const seed = 4000
	for i := range seed {
		rec.RecordMessage(ctx, benchRecord("busy", i))
	}

	// Time a write with nothing else happening, as the baseline.
	quietStart := time.Now()
	rec.RecordMessage(ctx, benchRecord("busy", seed+1))
	quiet := time.Since(quietStart)

	// Now start a query that reads everything, and time a write against it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 8 {
			// Search forces a scan of the payloads rather than an index lookup,
			// which is what a message browser search really does.
			_, _, err := store.List(ctx, Query{Channel: "busy", Search: "Vestavia", Limit: seed})
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()

	// Give the reader a moment to get the connection.
	time.Sleep(20 * time.Millisecond)

	busyStart := time.Now()
	rec.RecordMessage(ctx, benchRecord("busy", seed+2))
	busy := time.Since(busyStart)

	<-done

	t.Logf("write with the store idle: %s", quiet.Round(time.Microsecond))
	t.Logf("write while a search scans %d messages: %s", seed, busy.Round(time.Microsecond))
	if quiet > 0 {
		t.Logf("slowdown: %.1fx", float64(busy)/float64(quiet))
	}

	// A write is on the acknowledgement path. Blocking it behind a report is how a
	// sender's timeout becomes our outage, so the tolerance here is generous but
	// not unlimited.
	limit := 10 * time.Millisecond
	if busy > limit {
		t.Errorf("a write took %s while a report was running, over the %s limit: "+
			"reads and writes are competing for the same connection, so opening the "+
			"dashboard slows down message delivery", busy.Round(time.Millisecond), limit)
	}
}

// TestSharedConnectionIsMeasurablyWorse records why the split exists.
//
// It is here so that anyone tempted to simplify back to one pool can see the
// cost first. The assertion is only that the shared configuration is slower,
// not by how much, because the ratio depends on the machine.
func TestSharedConnectionIsMeasurablyWorse(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}

	measure := func(t *testing.T, store *Store) time.Duration {
		rec := NewRecorder(store, quietBenchLogger())
		ctx := context.Background()

		const seed = 4000
		for i := range seed {
			rec.RecordMessage(ctx, benchRecord("busy", i))
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 8 {
				if _, _, err := store.List(ctx, Query{
					Channel: "busy", Search: "Vestavia", Limit: seed,
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		time.Sleep(20 * time.Millisecond)

		start := time.Now()
		rec.RecordMessage(ctx, benchRecord("busy", seed+1))
		elapsed := time.Since(start)

		<-done
		return elapsed
	}

	shared, closeShared := benchStore(t)
	sharedTime := measure(t, shared)
	closeShared()

	split, closeSplit := splitPoolStore(t)
	splitTime := measure(t, split)
	closeSplit()

	t.Logf("one shared connection: %s", sharedTime.Round(time.Microsecond))
	t.Logf("split read and write pools: %s", splitTime.Round(time.Microsecond))

	if splitTime >= sharedTime {
		t.Errorf("splitting the pools did not help: shared %s, split %s",
			sharedTime.Round(time.Microsecond), splitTime.Round(time.Microsecond))
	}
}
