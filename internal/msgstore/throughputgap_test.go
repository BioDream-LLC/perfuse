package msgstore

import (
	"context"
	"testing"
	"time"
)

// A feed going quiet has to be visible in the throughput series.
//
// This is the thing the dashboard's main chart exists for. It could not do it: the SQL groups by bucket, so only buckets containing
// messages came back, and a feed that stopped twenty minutes ago was not a flat line at zero - it was simply missing from the data.
// The chart drew the last busy stretch and stopped, which looks like a healthy feed that happens to end at the right edge.
//
// It also made a single message unplottable. One bucket has no width, so the area chart drew nothing while the tile beside it read
// "4 messages". Two numbers disagreeing on one page is how somebody decides the whole console is unreliable.
func TestAGapInTrafficAppearsAsZeros(t *testing.T) {
	s := open(t)

	now := time.Now().UTC()

	// Traffic forty minutes ago, then silence.
	old := now.Add(-40 * time.Minute)
	for i := 0; i < 3; i++ {
		record(t, s, sample("feed", Delivered, old.Add(time.Duration(i)*time.Second)))
	}

	buckets, err := s.Throughput(context.Background(), "", now.Add(-time.Hour), time.Minute, "")
	if err != nil {
		t.Fatalf("throughput failed: %v", err)
	}

	// About sixty one-minute buckets across an hour. Exactness is not the point; covering the window is.
	if len(buckets) < 55 {
		t.Fatalf("got %d buckets for a one-hour window at one-minute resolution, want roughly 60. "+
			"A series that only contains busy periods cannot show a feed going quiet", len(buckets))
	}

	// The busy bucket is there.
	var busy int
	for _, b := range buckets {
		if b.Total > 0 {
			busy++
		}
	}
	if busy == 0 {
		t.Fatal("no bucket contains the messages that were recorded")
	}

	// And so is the silence after it. This is the assertion the old behaviour could not satisfy.
	var trailingZeros int
	for i := len(buckets) - 1; i >= 0 && buckets[i].Total == 0; i-- {
		trailingZeros++
	}
	if trailingZeros < 20 {
		t.Errorf("only %d empty buckets after the last message; forty minutes of silence should be visible as zeros",
			trailingZeros)
	}

	// Every bucket must carry a timestamp, or a chart has nothing to place it against.
	for i, b := range buckets {
		if b.Start.IsZero() {
			t.Errorf("bucket %d has no start time", i)

			break
		}
	}

	// Strictly increasing, since a chart drawn out of order is worse than no chart.
	for i := 1; i < len(buckets); i++ {
		if !buckets[i].Start.After(buckets[i-1].Start) {
			t.Errorf("bucket %d does not come after %d", i, i-1)

			break
		}
	}
}

// TestASingleMessageProducesAPlottableSeries is the case that was on screen.
//
// Four messages had arrived, the tile said four, and the chart was blank with a y-axis scaled to five - so it had the data and drew
// nothing. One point has no width.
func TestASingleMessageProducesAPlottableSeries(t *testing.T) {
	s := open(t)

	now := time.Now().UTC()
	record(t, s, sample("feed", Delivered, now.Add(-30*time.Second)))

	buckets, err := s.Throughput(context.Background(), "", now.Add(-time.Hour), time.Minute, "")
	if err != nil {
		t.Fatalf("throughput failed: %v", err)
	}

	if len(buckets) < 2 {
		t.Fatalf("one message produced %d bucket(s); a chart cannot draw a line through a single point, "+
			"so the panel appears empty while the message count beside it does not", len(buckets))
	}

	var total int64
	for _, b := range buckets {
		total += b.Total
	}
	if total != 1 {
		t.Errorf("the series totals %d, want 1", total)
	}
}
