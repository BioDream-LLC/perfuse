package msgstore

import (
	"context"
	"sync"
	"testing"
	"time"
)

// withDeliveries builds a message whose destinations had specific outcomes.
func withDeliveries(channel string, at time.Time, outcomes map[string]DeliveryStatus) *Message {
	m := sample(channel, Delivered, at)
	m.Deliveries = nil
	for dest, status := range outcomes {
		m.Deliveries = append(m.Deliveries, Delivery{Destination: dest, Status: status, Attempts: 1, DurationMS: 5})
	}

	return m
}

// strandOf finds one strand in the flow.
func strandOf(t *testing.T, f *Flow, channel, destination string) Strand {
	t.Helper()

	for _, c := range f.Channels {
		if c.Channel != channel {
			continue
		}
		for _, s := range c.Strands {
			if s.Destination == destination {
				return s
			}
		}
		t.Fatalf("channel %s has no strand to %s; it has %d strands", channel, destination, len(c.Strands))
	}

	t.Fatalf("no channel %s in the flow; it has %d channels", channel, len(f.Channels))

	return Strand{}
}

// A strand that stopped must be present and at zero, not missing.
//
// This is the whole point of the map. Drag back to the moment a feed went quiet and the strand should go dark - which requires the strand
// to still be there, drawn flat at zero. GROUP BY returns only buckets containing rows, so without filling the window a stopped strand
// simply is not in the data, and absent draws as nothing. Nothing on screen looks like a destination that was never configured, which is
// the opposite of the conclusion somebody needs to reach.
func TestFlowShowsAStrandGoingDark(t *testing.T) {
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute)

	// Two destinations busy together five minutes ago.
	for i := 0; i < 3; i++ {
		record(t, s, withDeliveries("adt-inbound", base.Add(-5*time.Minute).Add(time.Duration(i)*time.Second),
			map[string]DeliveryStatus{"archive": DeliveryDelivered, "pharmacy": DeliveryDelivered}))
	}

	// Then only one of them, now. The pharmacy strand has gone dark.
	record(t, s, withDeliveries("adt-inbound", base,
		map[string]DeliveryStatus{"archive": DeliveryDelivered}))

	flow, err := s.Flow(context.Background(), "", base.Add(-30*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	pharmacy := strandOf(t, flow, "adt-inbound", "pharmacy")
	archive := strandOf(t, flow, "adt-inbound", "archive")

	// Both strands cover the same window, or one of them cannot be compared against the other on a shared time axis.
	if len(pharmacy.Buckets) != len(archive.Buckets) {
		t.Fatalf("strands have different lengths: pharmacy %d, archive %d", len(pharmacy.Buckets), len(archive.Buckets))
	}
	if len(pharmacy.Buckets) < 25 {
		t.Fatalf("the window was not filled: got %d buckets over 30 minutes", len(pharmacy.Buckets))
	}

	// The pharmacy strand was busy earlier and is at zero at the end. Both halves matter: busy proves it existed, zero proves the
	// stopping is visible rather than inferred from an absence.
	var busy, quiet bool
	for _, b := range pharmacy.Buckets {
		if b.Total > 0 {
			busy = true
		}
	}
	if !busy {
		t.Error("the pharmacy strand never shows any traffic, so there is nothing to go dark")
	}

	last := pharmacy.Buckets[len(pharmacy.Buckets)-1]
	if last.Total == 0 {
		quiet = true
	}
	if !quiet {
		t.Errorf("the pharmacy strand is still busy in the final bucket: %+v", last)
	}

	// And the archive strand is still going, or this is a quiet site rather than a broken strand.
	if archive.Buckets[len(archive.Buckets)-1].Total == 0 {
		t.Error("the archive strand is also quiet, so the test cannot distinguish one strand stopping from everything stopping")
	}
}

// A channel that received messages and delivered nothing must still appear.
//
// That state - traffic arriving, nothing leaving - is the one somebody is hunting for. Building the channel list from deliveries alone
// would omit exactly the channel in trouble.
func TestFlowKeepsAChannelThatDeliveredNothing(t *testing.T) {
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute)

	m := sample("orders", Unparseable, base)
	m.Deliveries = nil
	record(t, s, m)

	flow, err := s.Flow(context.Background(), "", base.Add(-10*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, c := range flow.Channels {
		if c.Channel == "orders" {
			found = true
			if len(c.Received) == 0 {
				t.Error("the channel appears but its received series is empty")
			}

			// Empty, not nil: this crosses to a browser, where a null is an exception during render and a blank console.
			if c.Strands == nil {
				t.Error("strands is nil rather than an empty list")
			}
		}
	}
	if !found {
		t.Errorf("a channel that delivered nothing is missing from the flow; got %d channels", len(flow.Channels))
	}
}

// Strands must come back in a stable order.
//
// Go maps range randomly. A flow map whose strands swap places between polls is unreadable - a line moves out from under the pointer
// while somebody is following it.
func TestFlowOrdersChannelsAndStrands(t *testing.T) {
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute)

	record(t, s, withDeliveries("zeta", base, map[string]DeliveryStatus{"zulu": DeliveryDelivered, "alpha": DeliveryDelivered, "mike": DeliveryDelivered}))
	record(t, s, withDeliveries("alpha", base, map[string]DeliveryStatus{"one": DeliveryDelivered}))

	for attempt := 0; attempt < 5; attempt++ {
		flow, err := s.Flow(context.Background(), "", base.Add(-10*time.Minute), time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		if len(flow.Channels) != 2 || flow.Channels[0].Channel != "alpha" || flow.Channels[1].Channel != "zeta" {
			t.Fatalf("channels are not sorted: %v", names(flow))
		}

		want := []string{"alpha", "mike", "zulu"}
		got := flow.Channels[1].Strands
		if len(got) != len(want) {
			t.Fatalf("got %d strands, want %d", len(got), len(want))
		}
		for i, w := range want {
			if got[i].Destination != w {
				t.Fatalf("strand %d is %s, want %s", i, got[i].Destination, w)
			}
		}
	}
}

// names lists the channels in a flow for a failure message.
func names(f *Flow) []string {
	out := make([]string, 0, len(f.Channels))
	for _, c := range f.Channels {
		out = append(out, c.Channel)
	}

	return out
}

// An empty store must return an empty map, not a null one.
func TestFlowOnAnEmptyStore(t *testing.T) {
	s := open(t)

	flow, err := s.Flow(context.Background(), "", time.Now().Add(-time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if flow.Channels == nil {
		t.Error("channels is nil rather than an empty list, which reaches a browser as null and blanks the panel")
	}
	if len(flow.Channels) != 0 {
		t.Errorf("an empty store produced %d channels", len(flow.Channels))
	}
}

// A sub-second bucket must not crash. The API passes durations straight through and a caller can send bucket=1ms.
// int64(time.Millisecond.Seconds()) truncates to 0, which is division-by-zero in the SQL and an infinite loop in fillBuckets.
func TestFlowSubSecondBucketDoesNotCrash(t *testing.T) {
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute)
	record(t, s, sample("adt", Delivered, base))

	// bucket=1ms → seconds=0 after int64(d.Seconds()). This must not panic or return an error.
	flow, err := s.Flow(context.Background(), "", base.Add(-time.Minute), time.Millisecond)
	if err != nil {
		t.Fatalf("sub-second bucket caused an error: %v", err)
	}
	if flow == nil {
		t.Fatal("sub-second bucket returned nil flow")
	}
}

// SQL injection must not be possible via tenantID. The API layer passes query parameters directly.
func TestFlowSQLInjectionViaTenantID(t *testing.T) {
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute)

	// Record a message in the "main" tenant.
	record(t, s, sample("adt", Delivered, base))

	// Attempt SQL injection via tenantID.
	flow, err := s.Flow(context.Background(), "x' OR '1'='1", base.Add(-time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// If injection worked, we'd see the message. The injected tenant should see nothing.
	if len(flow.Channels) != 0 {
		t.Errorf("SQL injection widened the result set: got %d channels, want 0", len(flow.Channels))
	}
}

// Concurrent access must not race. The store is backed by *sql.DB which is safe, but the Flow method builds
// local maps that must not be shared.
func TestFlowConcurrentAccess(t *testing.T) {
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute)

	// Seed some data.
	for i := 0; i < 10; i++ {
		record(t, s, withDeliveries("adt", base.Add(time.Duration(i)*time.Second),
			map[string]DeliveryStatus{"archive": DeliveryDelivered}))
	}

	// Hit the store concurrently from 20 goroutines.
	const goroutines = 20
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := s.Flow(context.Background(), "", base.Add(-5*time.Minute), time.Minute)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Flow call failed: %v", err)
	}
}
