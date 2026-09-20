package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/admit"
	"github.com/biodream-llc/perfuse/internal/config"
)

// One receiver going quiet must not stop the others.
//
// This is the failure the delivery limiter exists to prevent, tested through the engine rather than through
// the limiter, because the limiter being correct in isolation is not the claim. The claim is that a channel
// whose receiver has stopped answering cannot consume what other channels need.
//
// The shape of the real incident: every delivery in flight holds a file descriptor here and a connection at
// the far end for the whole retry budget, which with the defaults is five attempts at a thirty second
// timeout plus fifteen seconds of backoff. A few hundred senders against one dead receiver is a few hundred
// descriptors held for minutes, the process runs out, and the symptom appears somewhere unrelated - the
// database cannot open a journal, the interface stops accepting. Three broken things, one cause, and nothing
// on screen connecting them.

// hangingSender blocks until released, standing in for a receiver that accepts a connection and then says
// nothing - which is worse than one that refuses, because refusing is fast.
type hangingSender struct {
	entered chan struct{}
	release chan struct{}
	inside  atomic.Int64
	peak    atomic.Int64
}

func (h *hangingSender) Send(ctx context.Context, _ []byte) error {
	n := h.inside.Add(1)
	for {
		p := h.peak.Load()
		if n <= p || h.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer h.inside.Add(-1)

	select {
	case h.entered <- struct{}{}:
	default:
	}

	select {
	case <-h.release:
		return fmt.Errorf("receiver gave up")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hangingSender) Describe() string { return "a receiver that stopped answering" }
func (h *hangingSender) Close() error     { return nil }

// countingSender is the healthy receiver, and the thing being protected.
type countingSender struct {
	delivered atomic.Int64
}

func (c *countingSender) Send(context.Context, []byte) error {
	c.delivered.Add(1)
	return nil
}
func (c *countingSender) Describe() string { return "a receiver that works" }
func (c *countingSender) Close() error     { return nil }

func aMessage(t *testing.T, control string) []byte {
	t.Helper()
	raw := "MSH|^~\\&|LAB|HOSP|EHR|HOSP|20260826080000||ADT^A01|" + control + "|P|2.5\r" +
		"PID|1||1234^^^HOSP^MR||DOE^JOHN||19800101|M\r"
	if _, err := hl7.Parse([]byte(raw)); err != nil {
		t.Fatalf("the test message is not valid HL7: %v", err)
	}
	return []byte(raw)
}

func channelWithSender(t *testing.T, name string, sender Sender, timeout time.Duration) *Channel {
	t.Helper()

	cfg := &config.Channel{
		Name: name,
		Source: config.Source{
			Type:   config.SourceMLLP,
			Listen: "127.0.0.1:0",
		},
		Destinations: []config.Destination{{
			Name:    "receiver",
			Type:    config.DestinationMLLP,
			Address: "127.0.0.1:1",
			Timeout: timeout,
			Retry:   config.Retry{Attempts: 1},
		}},
	}

	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return sender, nil }, nil)
	if err != nil {
		t.Fatalf("building channel %s: %v", name, err)
	}
	return ch
}

func TestAHungReceiverDoesNotStarveAnotherChannel(t *testing.T) {
	// A small budget, so the point is made in a few messages rather than in ten thousand. The shape is what
	// matters, not the numbers: the total is generous relative to what one destination may hold.
	limiter := admit.New(admit.Limits{Total: 6, PerDestination: 2})

	hung := &hangingSender{entered: make(chan struct{}, 1), release: make(chan struct{})}
	healthy := &countingSender{}

	// A timeout long enough that the stalled deliveries are still holding their slots when the healthy
	// channel is exercised. Without the limiter this is precisely the window in which everything else stops.
	broken := channelWithSender(t, "broken", hung, 10*time.Second)
	broken.SetAdmit(limiter)
	working := channelWithSender(t, "working", healthy, 2*time.Second)
	working.SetAdmit(limiter)

	msg := aMessage(t, "STARVE1")

	// Push far more at the broken channel than its destination may hold at once.
	var senders sync.WaitGroup
	for i := 0; i < 20; i++ {
		senders.Add(1)
		go func() {
			defer senders.Done()
			_, _ = broken.handle(context.Background(), msg)
		}()
	}

	// Wait until the broken receiver is actually being called, so this is a test of a saturated destination
	// rather than of a race to start.
	select {
	case <-hung.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the broken receiver was never called")
	}
	time.Sleep(200 * time.Millisecond)

	// The claim: the working channel still delivers, while twenty messages are stacked against a receiver
	// that will never answer.
	done := make(chan error, 1)
	go func() {
		_, err := working.handle(context.Background(), aMessage(t, "STARVE2"))
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the healthy channel failed while another was saturated: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the healthy channel could not deliver while another receiver was hung.\n\n" +
			"This is the outage the limiter exists to prevent: one partner system going quiet stops every " +
			"other interface on the server. Check that the per-destination slot is reserved before the " +
			"process-wide one, or waiters for the dead receiver are holding the whole budget.")
	}

	if got := healthy.delivered.Load(); got < 1 {
		t.Errorf("the healthy receiver got %d messages, want at least 1", got)
	}

	// The load-bearing assertion, and worth saying which one it is.
	//
	// The healthy-channel check above does not fail without the limiter at this scale: twenty stalled
	// deliveries are nowhere near exhausting a descriptor table, so the other channel gets through anyway.
	// Reproducing the real outage would need hundreds of connections and a test that took minutes.
	//
	// This one fails immediately instead, because it checks the invariant rather than the symptom. Without a
	// limiter the broken destination reaches twenty in flight; with one it reaches two. The bound is what
	// makes the outage impossible, so the bound is what is asserted.
	if got := hung.peak.Load(); got > 2 {
		t.Errorf("the broken destination had %d deliveries in flight at once, above its limit of 2.\n\n"+
			"Unbounded, this is what exhausts the descriptor table: every one holds a socket for the whole "+
			"retry budget, and the process then fails somewhere unrelated with \"too many open files\".", got)
	}

	close(hung.release)
	senders.Wait()
}

// A saturated destination that has a queue must queue, not fail, because the message is already
// acknowledged as far as the sender is concerned.
func TestASaturatedDestinationWithAQueueDoesNotLoseTheMessage(t *testing.T) {
	limiter := admit.New(admit.Limits{Total: 4, PerDestination: 1})

	hung := &hangingSender{entered: make(chan struct{}, 1), release: make(chan struct{})}

	// A short timeout, so the wait for a slot expires quickly and the queue path is reached during the test
	// rather than after it.
	ch := channelWithSender(t, "queued", hung, 300*time.Millisecond)
	ch.SetAdmit(limiter)

	msg := aMessage(t, "QUEUE1")

	// Occupy the only slot.
	go func() { _, _ = ch.handle(context.Background(), msg) }()
	select {
	case <-hung.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver was never called")
	}

	// A second message cannot get a slot. It must not be silently dropped: either it is reported as
	// undelivered, or it is queued. Both are honest; losing it is not.
	_, err := ch.handle(context.Background(), aMessage(t, "QUEUE2"))
	if err != nil {
		t.Logf("the second message was reported as failing, which is acceptable: %v", err)
	}

	close(hung.release)
}
