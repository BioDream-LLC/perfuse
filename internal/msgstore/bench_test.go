package msgstore

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	_ "modernc.org/sqlite"
)

// These measure the write ceiling of the message store, because it is the thing
// on the delivery path that touches a disk and therefore the thing that decides
// how fast a channel can go.
//
// The number that matters is not the peak. It is whether the store can absorb a
// realistic burst without becoming the bottleneck: an interface feed from a
// mid-sized hospital runs at tens of messages a second with morning peaks of a
// few hundred, and a lab results feed after an overnight batch can arrive as
// thousands at once.

func benchRecord(channel string, i int) engine.MessageRecord {
	raw := []byte(fmt.Sprintf(
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000-0500||ADT^A01^ADT_A01|B%d|P|2.5.1\r"+
			"EVN|A01|20260819075900-0500\r"+
			"PID|1||MRN%d^^^SITEA^MR||Frost^Ivy^L||19910228|F|||4 Elm Rd^^Vestavia^AL^35216\r"+
			"PV1|1|I|ICU^7^01^SITEA||||7001^Shaw^Sam|||MED\r", i, i))

	return engine.MessageRecord{
		Channel:      channel,
		ReceivedAt:   time.Now(),
		ControlID:    fmt.Sprintf("B%d", i),
		MessageType:  "ADT",
		TriggerEvent: "A01",
		Sender:       "SITEA",
		Outcome:      engine.Delivered,
		AckCode:      "AA",
		Raw:          raw,
		Segments:     4,
		Duration:     2 * time.Millisecond,
		Deliveries: []engine.DeliveryRecord{{
			Destination: "registry",
			Status:      "delivered",
			Attempts:    1,
			Duration:    time.Millisecond,
		}},
	}
}

func BenchmarkRecordMessage(b *testing.B) {
	store, cleanup := benchStore(b)
	defer cleanup()

	rec := NewRecorder(store, quietBenchLogger())
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		rec.RecordMessage(ctx, benchRecord("bench", i))
	}
	b.StopTimer()

	perOp := b.Elapsed().Seconds() / float64(b.N)
	if perOp > 0 {
		b.ReportMetric(1/perOp, "msg/s")
	}
}

// BenchmarkRecordMessageConcurrent is the one that matters, because a real
// deployment has several channels recording at once and the store is shared.
func BenchmarkRecordMessageConcurrent(b *testing.B) {
	for _, writers := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("writers=%d", writers), func(b *testing.B) {
			store, cleanup := benchStore(b)
			defer cleanup()

			rec := NewRecorder(store, quietBenchLogger())
			ctx := context.Background()

			b.ResetTimer()
			var wg sync.WaitGroup
			per := b.N / writers
			if per == 0 {
				per = 1
			}
			for w := range writers {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					channel := fmt.Sprintf("channel-%d", w)
					for i := range per {
						rec.RecordMessage(ctx, benchRecord(channel, i))
					}
				}(w)
			}
			wg.Wait()
			b.StopTimer()

			total := float64(per * writers)
			if secs := b.Elapsed().Seconds(); secs > 0 {
				b.ReportMetric(total/secs, "msg/s")
			}
		})
	}
}

// TestSustainedWriteRate is a test rather than a benchmark so it runs in CI and
// fails if the store gets materially slower. The floor is set low enough not to
// be flaky on a loaded machine and high enough to catch a regression that would
// matter, such as an index being dropped or a transaction per statement.
func TestSustainedWriteRate(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}

	store, cleanup := benchStore(t)
	defer cleanup()

	rec := NewRecorder(store, quietBenchLogger())
	ctx := context.Background()

	const count = 500
	start := time.Now()
	for i := range count {
		rec.RecordMessage(ctx, benchRecord("rate", i))
	}
	elapsed := time.Since(start)

	rate := float64(count) / elapsed.Seconds()
	t.Logf("recorded %d messages in %s: %.0f msg/s", count, elapsed.Round(time.Millisecond), rate)

	const floor = 100
	if rate < floor {
		t.Errorf("the message store manages %.0f msg/s, below the %d floor; "+
			"it is the slowest thing on the delivery path so this is the channel's ceiling too",
			rate, floor)
	}

	// And the messages are actually there. A fast write that lost rows would be
	// worse than a slow one.
	list, _, err := store.List(ctx, Query{Channel: "rate", Limit: count + 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != count {
		t.Errorf("stored %d of %d messages", len(list), count)
	}
}

// --- helpers ---------------------------------------------------------------

// benchStore opens an on-disk database with the same pragmas production uses.
// An in-memory store would measure the wrong thing entirely: the question is what
// happens when a message has to reach a disk before the acknowledgement goes out.
func benchStore(tb testing.TB) (*Store, func()) {
	tb.Helper()

	path := filepath.Join(tb.TempDir(), "bench.db")
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		tb.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	s, err := NewStore(db)
	if err != nil {
		db.Close()
		tb.Fatal(err)
	}
	return s, func() { db.Close() }
}

func quietBenchLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// splitPoolStore opens the store the way serve does: one connection for writes
// and several for reports.
func splitPoolStore(tb testing.TB) (*Store, func()) {
	tb.Helper()

	pool, err := sqlitedb.Open(filepath.Join(tb.TempDir(), "split.db"), sqlitedb.Options{})
	if err != nil {
		tb.Fatal(err)
	}
	s, err := NewStoreWithReader(pool.Write, pool.Read)
	if err != nil {
		pool.Close()
		tb.Fatal(err)
	}
	return s, func() { pool.Close() }
}
