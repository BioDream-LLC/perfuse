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

// TestSustainedWriteRate is a test rather than a benchmark so it runs in CI and fails
// if the store gets materially slower.
//
// It asserts a ratio, not a rate. The first version required 100 msg/s absolute, and
// failed on a GitHub runner that managed 76 - not because anything had regressed, but
// because a shared, throttled disk is several times slower than a local SSD and varies
// with whoever else is on the machine. An absolute floor low enough to survive that is
// too low to detect a real regression, so the test was simultaneously noisy and blind.
//
// So the same database on the same disk is measured twice in the same process: once
// doing the cheapest possible insert, then once through the recorder. Both scale with
// the hardware, so the ratio between them does not, and it is the ratio that reveals
// the regressions this test was written for - a transaction per statement, an fsync per
// row, a dropped index turning a write into a scan.
func TestSustainedWriteRate(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}

	store, cleanup := benchStore(t)
	defer cleanup()

	ctx := context.Background()
	const count = 500

	// The baseline: how fast this disk accepts a single trivial row, committed
	// individually, which is the same durability the recorder is paying for.
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE ratebaseline (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	baseStart := time.Now()
	for i := range count {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO ratebaseline (v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	baseline := time.Since(baseStart)

	rec := NewRecorder(store, quietBenchLogger())
	start := time.Now()
	for i := range count {
		rec.RecordMessage(ctx, benchRecord("rate", i))
	}
	elapsed := time.Since(start)

	rate := float64(count) / elapsed.Seconds()
	baseRate := float64(count) / baseline.Seconds()
	ratio := elapsed.Seconds() / baseline.Seconds()

	// Logged rather than asserted, so the absolute figure is still visible in CI output
	// for anyone watching the trend, without deciding whether the build passes.
	t.Logf("recorded %d messages in %s: %.0f msg/s (this disk does %.0f bare inserts/s; ratio %.1fx)",
		count, elapsed.Round(time.Millisecond), rate, baseRate, ratio)

	// A recorded message is one message row plus its deliveries, so a small multiple of
	// one bare insert is expected.
	//
	// What this ceiling catches, measured rather than assumed:
	//
	//	healthy                          2.0x - 2.9x over eight runs
	//	20 redundant queries per message  4.0x   not caught
	//	100 redundant queries per message 12.5x  caught
	//
	// So it catches severe regressions and not moderate ones. Catching the 4.0x case
	// would need a ceiling near 3.5x, which is 20% above the worst healthy reading and
	// would flake on a shared runner. The logged figures above are how a moderate
	// regression gets noticed; this assertion is a backstop against a catastrophe.
	//
	// PRAGMA synchronous=FULL was tried as a way to simulate one and moved the ratio not
	// at all on macOS, which is worth knowing before reaching for it again.
	const maxRatio = 8
	if ratio > maxRatio {
		t.Errorf("recording a message costs %.1fx a bare insert on this same disk, above the %.0fx ceiling; "+
			"the store is the slowest thing on the delivery path, so this is the channel's ceiling too",
			ratio, float64(maxRatio))
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
