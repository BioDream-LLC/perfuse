package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/queue"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

// flakySender fails until told to recover, which is what a receiver being
// restarted looks like from here.
type flakySender struct {
	mu        sync.Mutex
	down      bool
	delivered []string
}

func (f *flakySender) Send(ctx context.Context, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return fmt.Errorf("connection refused")
	}
	m, err := hl7.Parse(raw)
	if err != nil {
		return err
	}
	f.delivered = append(f.delivered, m.ControlID())
	return nil
}

func (f *flakySender) Describe() string { return "flaky" }
func (f *flakySender) Close() error     { return nil }

func (f *flakySender) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *flakySender) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.delivered...)
}

func queueTestStore(t *testing.T) (*queue.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "q.db")
	pool, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	s, err := queue.NewStore(pool.Write, pool.Read)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func queuedChannel(t *testing.T, sender Sender, qs *queue.Store, maxDepth int) *Channel {
	t.Helper()

	cfg := &config.Channel{
		Name:   "adt",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{
			Name:    "registry",
			Type:    config.DestinationMLLP,
			Address: "127.0.0.1:1",
			Timeout: 2 * time.Second,
			Retry:   config.Retry{Attempts: 1},
			Queue: &config.QueueConfig{
				Enabled:    true,
				Backoff:    5 * time.Millisecond,
				MaxBackoff: 10 * time.Millisecond,
				MaxDepth:   maxDepth,
			},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return sender, nil
	}, quiet())
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	queues := NewQueues(qs, quiet())
	ch.SetQueues(queues)
	t.Cleanup(queues.Stop)
	return ch
}

func adtMessage(controlID, event string) []byte {
	return []byte(fmt.Sprintf(
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000-0500||ADT^%s^ADT_%s|%s|P|2.5.1\r"+
			"EVN|%s|20260819075900-0500\r"+
			"PID|1||MRN7^^^SITEA^MR||Frost^Ivy^L||19910228|F\r",
		event, event, controlID, event))
}

func TestAQueuedMessageIsAcknowledgedAsAccepted(t *testing.T) {
	qs, _ := queueTestStore(t)
	sender := &flakySender{down: true}
	ch := queuedChannel(t, sender, qs, 0)

	ack, err := ch.handle(context.Background(), adtMessage("M1", "A01"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// The bytes are committed to disk before the acknowledgement is written, so AA
	// is a promise we can keep. Reporting an error would make a working
	// store-and-forward queue look like a fault and invite the sender to resend
	// what we already hold.
	parsed, err := hl7.Parse(ack)
	if err != nil {
		t.Fatal(err)
	}
	msa, ok := parsed.Segment("MSA", 1)
	if !ok {
		t.Fatal("no MSA in the acknowledgement")
	}
	if code := msa.Field(1).String(); code != "AA" {
		t.Errorf("acknowledgement = %q, want AA for a durably queued message", code)
	}

	blocked, err := qs.Blocked(context.Background(), "adt", "registry")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("the message should be in the queue")
	}
}

func TestNothingIsLostWhileADestinationIsDown(t *testing.T) {
	qs, _ := queueTestStore(t)
	sender := &flakySender{down: true}
	ch := queuedChannel(t, sender, qs, 0)
	ctx := context.Background()

	// Five messages arrive during the outage.
	for i := 1; i <= 5; i++ {
		if _, err := ch.handle(ctx, adtMessage(fmt.Sprintf("M%d", i), "A01")); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}

	if sender.got() != nil {
		t.Fatalf("nothing should have been delivered yet, got %v", sender.got())
	}

	// The receiver comes back.
	sender.setDown(false)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(sender.got()) < 5 {
		time.Sleep(5 * time.Millisecond)
	}

	got := sender.got()
	if len(got) != 5 {
		t.Fatalf("delivered %d of 5 after recovery: %v", len(got), got)
	}
	// Order is the point. A receiving system told about a discharge before the
	// admission it belongs to has a patient it has never heard of.
	if fmt.Sprint(got) != "[M1 M2 M3 M4 M5]" {
		t.Errorf("delivered out of order: %v", got)
	}
}

func TestALaterMessageDoesNotOvertakeAQueuedOne(t *testing.T) {
	qs, _ := queueTestStore(t)
	sender := &flakySender{down: true}
	ch := queuedChannel(t, sender, qs, 0)
	ctx := context.Background()

	// The admission fails and goes to the queue.
	if _, err := ch.handle(ctx, adtMessage("ADMIT", "A01")); err != nil {
		t.Fatal(err)
	}

	// The receiver recovers before the discharge arrives, so the direct path would
	// now succeed. It must not be taken: delivering the discharge first would tell
	// the receiver about a patient it never admitted.
	sender.setDown(false)

	if _, err := ch.handle(ctx, adtMessage("DISCHARGE", "A03")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(sender.got()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}

	got := sender.got()
	if len(got) != 2 {
		t.Fatalf("delivered %d of 2: %v", len(got), got)
	}
	if got[0] != "ADMIT" {
		t.Errorf("the discharge overtook the admission: %v", got)
	}
}

func TestTheQueueSurvivesAChannelRestart(t *testing.T) {
	qs, _ := queueTestStore(t)
	ctx := context.Background()

	// First channel: the destination is down, so everything queues.
	down := &flakySender{down: true}
	first := queuedChannel(t, down, qs, 0)
	for i := 1; i <= 3; i++ {
		if _, err := first.handle(ctx, adtMessage(fmt.Sprintf("M%d", i), "A01")); err != nil {
			t.Fatal(err)
		}
	}
	if first.queues != nil {
		first.queues.Stop()
	}

	if down.got() != nil {
		t.Fatalf("nothing should have been delivered: %v", down.got())
	}

	// A second channel over the same queue, as a restart would produce. The rows
	// were always going to survive; the question is whether anything looks at
	// them without a new message arriving to trigger it.
	up := &flakySender{}
	second := queuedChannel(t, up, qs, 0)
	second.resumeQueues(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(up.got()) < 3 {
		time.Sleep(5 * time.Millisecond)
	}

	got := up.got()
	if len(got) != 3 {
		t.Fatalf("a restart recovered %d of 3 queued messages: %v", len(got), got)
	}
	if fmt.Sprint(got) != "[M1 M2 M3]" {
		t.Errorf("recovered out of order: %v", got)
	}
}

func TestAFullQueueIsRefusedHonestly(t *testing.T) {
	qs, _ := queueTestStore(t)
	sender := &flakySender{down: true}
	ch := queuedChannel(t, sender, qs, 2)
	ctx := context.Background()

	// Fill it.
	for i := 1; i <= 2; i++ {
		if _, err := ch.handle(ctx, adtMessage(fmt.Sprintf("M%d", i), "A01")); err != nil {
			t.Fatal(err)
		}
	}

	// The next one must be refused rather than accepted, because we are no longer
	// promising to deliver it and filling the disk is not a better outcome.
	ack, err := ch.handle(ctx, adtMessage("M3", "A01"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	parsed, _ := hl7.Parse(ack)
	msa, ok := parsed.Segment("MSA", 1)
	if !ok {
		t.Fatal("no MSA")
	}
	if code := msa.Field(1).String(); code == "AA" {
		t.Error("a message that could not be queued must not be acknowledged as accepted")
	}
	if text := msa.Field(3).String(); text == "" {
		t.Error("the acknowledgement should say what went wrong")
	}
}

func TestQueueingIsOffUnlessAskedFor(t *testing.T) {
	qs, _ := queueTestStore(t)
	ctx := context.Background()

	cfg := &config.Channel{
		Name:   "adt",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{
			Name:    "registry",
			Type:    config.DestinationMLLP,
			Address: "127.0.0.1:1",
			Timeout: time.Second,
			Retry:   config.Retry{Attempts: 1},
			// No queue block.
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	sender := &flakySender{down: true}
	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return sender, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	queues := NewQueues(qs, quiet())
	ch.SetQueues(queues)
	defer queues.Stop()

	ack, err := ch.handle(ctx, adtMessage("M1", "A01"))
	if err != nil {
		t.Fatal(err)
	}

	// A queue changes what an acknowledgement means and how ordering behaves
	// during an outage. Turning that on without being asked would be a surprise.
	parsed, _ := hl7.Parse(ack)
	msa, _ := parsed.Segment("MSA", 1)
	if code := msa.Field(1).String(); code == "AA" {
		t.Error("without a queue, a failed delivery should not be acknowledged as accepted")
	}
	if blocked, _ := qs.Blocked(ctx, "adt", "registry"); blocked {
		t.Error("nothing should have been queued")
	}
}

func TestQueueConfigIsCheckedAtLoad(t *testing.T) {
	cases := []struct {
		name string
		q    config.QueueConfig
		want string
	}{
		{"negative attempts", config.QueueConfig{Enabled: true, MaxAttempts: -1}, "max_attempts"},
		{"ceiling below floor", config.QueueConfig{
			Enabled: true, Backoff: time.Minute, MaxBackoff: time.Second,
		}, "shorter than"},
		{"absurd ceiling", config.QueueConfig{
			Enabled: true, MaxBackoff: 6 * time.Hour,
		}, "recovered receiver"},
		{"negative depth", config.QueueConfig{Enabled: true, MaxDepth: -1}, "max_depth"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Channel{
				Name:   "adt",
				Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
				Destinations: []config.Destination{{
					Name: "registry", Type: config.DestinationMLLP,
					Address: "127.0.0.1:1", Queue: &tc.q,
				}},
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !contains(err.Error(), tc.want) {
				t.Errorf("want an error mentioning %q, got: %v", tc.want, err)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
