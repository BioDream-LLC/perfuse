package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

func openQueue(t *testing.T) *Store {
	t.Helper()
	pool, err := sqlitedb.Open(filepath.Join(t.TempDir(), "q.db"), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })

	s, err := NewStore(pool.Write, pool.Read)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func item(dest, controlID string) Item {
	return Item{
		Channel:     "adt",
		Destination: dest,
		ControlID:   controlID,
		MessageType: "ADT^A01",
		Raw:         []byte("MSH|^~\\&|S|F|R|F|20260819||ADT^A01|" + controlID + "|P|2.5.1\r"),
		Reason:      "the receiver refused the connection",
	}
}

func TestEnqueueAndDrainInOrder(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	for _, id := range []string{"M1", "M2", "M3"} {
		if _, err := s.Enqueue(ctx, item("registry", id)); err != nil {
			t.Fatal(err)
		}
	}

	// Order is the whole point. Draining out of order would tell a receiving
	// system about a discharge before the admission it belongs to.
	var got []string
	for range 3 {
		it, _, err := s.Next(ctx, "adt", "registry", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if it == nil {
			t.Fatal("Next returned nothing while items were pending")
		}
		got = append(got, it.ControlID)
		if err := s.Succeeded(ctx, it.ID, 1); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(got) != "[M1 M2 M3]" {
		t.Errorf("drained in the wrong order: %v", got)
	}
}

func TestNextIgnoresOtherDestinations(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	if _, err := s.Enqueue(ctx, item("registry", "R1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, item("archive", "A1")); err != nil {
		t.Fatal(err)
	}

	// One destination being down must not hold up another. They are independent
	// receivers with independent queues.
	it, _, err := s.Next(ctx, "adt", "archive", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if it == nil || it.ControlID != "A1" {
		t.Fatalf("want A1 from the archive queue, got %v", it)
	}
}

func TestNextRespectsBackoff(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	id, err := s.Enqueue(ctx, item("registry", "M1"))
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := s.Retry(ctx, id, 1, future, "connection refused"); err != nil {
		t.Fatal(err)
	}

	if it, _, err := s.Next(ctx, "adt", "registry", time.Now()); err != nil {
		t.Fatal(err)
	} else if it != nil {
		t.Error("an item backing off should not be returned yet")
	}

	// But it must come back once its time arrives, or the queue would stall
	// forever on a single failure.
	if it, _, err := s.Next(ctx, "adt", "registry", future.Add(time.Second)); err != nil {
		t.Fatal(err)
	} else if it == nil {
		t.Error("the item should be due after its backoff")
	}
}

// TestABackingOffHeadIsNotOvertaken pins the bug that an end-to-end test caught
// and the unit tests had missed.
//
// The first version of Next selected the oldest pending item whose next attempt
// was due. That reads as sensible and is badly wrong: a message backing off after
// a failure is not due, so it was skipped, and a later message that had never been
// tried was returned instead and went out first. The queue preserved order right
// up until the first failure — exactly when order begins to matter.
func TestABackingOffHeadIsNotOvertaken(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	head, _ := s.Enqueue(ctx, item("registry", "ADMIT"))
	if _, err := s.Enqueue(ctx, item("registry", "DISCHARGE")); err != nil {
		t.Fatal(err)
	}

	// The admission failed and is waiting. The discharge behind it is due now.
	if err := s.Retry(ctx, head, 1, time.Now().Add(time.Hour), "refused"); err != nil {
		t.Fatal(err)
	}

	it, wait, err := s.Next(ctx, "adt", "registry", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if it != nil {
		t.Fatalf("Next returned %q while the head of the queue was still waiting: "+
			"delivering it would tell the receiver about a discharge for a patient "+
			"it never admitted", it.ControlID)
	}
	if wait <= 0 {
		t.Error("Next should report how long the head has left to wait")
	}
}

func TestBlockedIgnoresBackoff(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	id, _ := s.Enqueue(ctx, item("registry", "M1"))
	if err := s.Retry(ctx, id, 1, time.Now().Add(time.Hour), "refused"); err != nil {
		t.Fatal(err)
	}

	// This is the ordering guarantee. An item waiting an hour is still ahead in
	// the queue, so the destination stays blocked and the next message queues
	// behind it rather than overtaking it.
	blocked, err := s.Blocked(ctx, "adt", "registry")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("a destination with an item backing off must still report blocked, " +
			"or a later message would be delivered before an earlier one")
	}
}

func TestBlockedIsFalseOnceDrained(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	id, _ := s.Enqueue(ctx, item("registry", "M1"))
	if err := s.Succeeded(ctx, id, 1); err != nil {
		t.Fatal(err)
	}

	blocked, err := s.Blocked(ctx, "adt", "registry")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("a drained queue should not block the direct path")
	}
}

func TestEnqueueRefusesAnEmptyMessage(t *testing.T) {
	s := openQueue(t)
	bad := item("registry", "M1")
	bad.Raw = nil
	// It would drain successfully and deliver nothing, which is worse than
	// refusing it.
	if _, err := s.Enqueue(context.Background(), bad); err == nil {
		t.Error("an empty payload should be refused")
	}
}

func TestDepthReportsWhatAnAlertNeeds(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	old := item("registry", "OLD")
	old.EnqueuedAt = time.Now().Add(-2 * time.Hour)
	if _, err := s.Enqueue(ctx, old); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"M2", "M3"} {
		if _, err := s.Enqueue(ctx, item("registry", id)); err != nil {
			t.Fatal(err)
		}
	}
	dead, _ := s.Enqueue(ctx, item("registry", "DEAD"))
	if err := s.GaveUp(ctx, dead, 5, "gave up"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Depth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one destination, got %d", len(rows))
	}
	r := rows[0]
	if r.Pending != 3 {
		t.Errorf("pending = %d, want 3", r.Pending)
	}
	if r.Failed != 1 {
		t.Errorf("failed = %d, want 1", r.Failed)
	}
	// Age is the number worth alerting on: depth alone cannot tell a busy queue
	// that is draining from a small one stuck since Tuesday.
	if r.OldestSeconds < 3600 {
		t.Errorf("oldest = %.0fs, want at least an hour", r.OldestSeconds)
	}
}

func TestRetryNowResetsTheAttemptCount(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	id, _ := s.Enqueue(ctx, item("registry", "M1"))
	if err := s.Retry(ctx, id, 4, time.Now().Add(time.Hour), "refused"); err != nil {
		t.Fatal(err)
	}

	n, err := s.RetryNow(ctx, id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("RetryNow affected %d rows, want 1", n)
	}

	it, _, err := s.Next(ctx, "adt", "registry", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if it == nil {
		t.Fatal("the item should be due immediately after a manual retry")
	}
	// An operator retrying by hand has usually just fixed something. Counting the
	// failures from before the fix would abandon the message on its next attempt.
	if it.Attempts != 0 {
		t.Errorf("attempts = %d, want 0: a manual retry starts the budget again", it.Attempts)
	}
}

func TestRetryNowCanRevivEveryFailedItem(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	for _, id := range []string{"M1", "M2", "M3"} {
		qid, _ := s.Enqueue(ctx, item("registry", id))
		if err := s.GaveUp(ctx, qid, 5, "receiver down"); err != nil {
			t.Fatal(err)
		}
	}

	// The alternative in other engines is restarting the channel, which also
	// interrupts everything that was working.
	n, err := s.RetryNow(ctx, 0, "adt", "registry")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("revived %d items, want 3", n)
	}
}

func TestSkipAndDrainRecordWhoDecided(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	id, _ := s.Enqueue(ctx, item("registry", "M1"))
	if _, err := s.Skip(ctx, id, "testuser"); err != nil {
		t.Fatal(err)
	}

	items, _, err := s.List(ctx, Query{State: Skipped})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want one skipped item, got %d", len(items))
	}
	// Skipped and failed are deliberately different states: one is the system
	// giving up and the other is a person deciding, and conflating them loses who
	// is accountable.
	if items[0].State != Skipped {
		t.Errorf("state = %q", items[0].State)
	}
	if items[0].LastError != "skipped by testuser" {
		t.Errorf("the decision should record who made it, got %q", items[0].LastError)
	}
}

func TestDrainClearsADestination(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	for i := range 5 {
		if _, err := s.Enqueue(ctx, item("registry", fmt.Sprintf("M%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Enqueue(ctx, item("archive", "KEEP")); err != nil {
		t.Fatal(err)
	}

	n, err := s.Drain(ctx, "adt", "registry", "testuser")
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("drained %d, want 5", n)
	}

	// Draining one destination must not touch another.
	blocked, _ := s.Blocked(ctx, "adt", "archive")
	if !blocked {
		t.Error("draining registry should not have emptied archive")
	}
}

func TestRemoveWillNotDeleteAPendingMessage(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	id, _ := s.Enqueue(ctx, item("registry", "M1"))

	// Deleting a message that has neither been delivered nor explicitly
	// abandoned would lose it with no record. It has to be skipped first, which
	// records that somebody chose to.
	n, err := s.Remove(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("a pending message should not be deletable")
	}

	if _, err := s.Skip(ctx, id, "testuser"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Remove(ctx, id); err != nil || n != 1 {
		t.Errorf("an abandoned message should be deletable: %d, %v", n, err)
	}
}

func TestPurgeLeavesPendingWorkAlone(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	done, _ := s.Enqueue(ctx, item("registry", "DONE"))
	if err := s.Succeeded(ctx, done, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, item("registry", "WAITING")); err != nil {
		t.Fatal(err)
	}

	n, err := s.Purge(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
	if blocked, _ := s.Blocked(ctx, "adt", "registry"); !blocked {
		t.Error("purge removed a pending message")
	}
}

func TestPendingDestinationsIsWhatMakesARestartRecover(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	if _, err := s.Enqueue(ctx, item("registry", "M1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, item("archive", "M2")); err != nil {
		t.Fatal(err)
	}
	drained, _ := s.Enqueue(ctx, item("quiet", "M3"))
	if err := s.Succeeded(ctx, drained, 1); err != nil {
		t.Fatal(err)
	}

	rows, err := s.PendingDestinations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Without this, the rows survive a restart but nothing looks at them until
	// the next message happens to arrive — which for an overnight outage means the
	// backlog sits untouched until morning.
	if len(rows) != 2 {
		t.Fatalf("want two destinations with work waiting, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Destination == "quiet" {
			t.Error("a fully drained destination should not need a worker")
		}
	}
}

func TestListDoesNotReturnPayloads(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	if _, err := s.Enqueue(ctx, item("registry", "M1")); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d items=%d", total, len(items))
	}
	// A queue page showing a hundred messages does not need a hundred payloads,
	// and shipping them puts clinical content into a response that needed counts.
	if len(items[0].Raw) != 0 {
		t.Error("List should not include the payload")
	}
	if items[0].Size == 0 {
		t.Error("List should still report the size")
	}
}

func TestErrorTextIsBounded(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	id, _ := s.Enqueue(ctx, item("registry", "M1"))
	huge := make([]byte, 100_000)
	for i := range huge {
		huge[i] = 'x'
	}
	// A receiver returning a megabyte of HTML instead of an acknowledgement should
	// not put a megabyte into every queue row.
	if err := s.Retry(ctx, id, 1, time.Now(), string(huge)); err != nil {
		t.Fatal(err)
	}
	items, _, _ := s.List(ctx, Query{})
	if len(items[0].LastError) > 2100 {
		t.Errorf("the stored error is %d bytes", len(items[0].LastError))
	}
}

// --- worker ----------------------------------------------------------------

type fakeSender struct {
	mu       sync.Mutex
	sent     [][]byte
	failFor  int
	failures int
	err      error
}

func (f *fakeSender) Send(ctx context.Context, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures < f.failFor {
		f.failures++
		if f.err != nil {
			return f.err
		}
		return errors.New("connection refused")
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	f.sent = append(f.sent, cp)
	return nil
}

func (f *fakeSender) Describe() string { return "fake" }

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeSender) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, raw := range f.sent {
		// The control id is the ninth field; close enough to read it crudely.
		out = append(out, controlIDOf(string(raw)))
	}
	return out
}

func controlIDOf(msg string) string {
	fields := splitPipes(msg)
	if len(fields) > 9 {
		return fields[9]
	}
	return ""
}

func splitPipes(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '|' {
			out = append(out, cur)
			cur = ""
			continue
		}
		if r == '\r' || r == '\n' {
			break
		}
		cur += string(r)
	}
	return append(out, cur)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWorkerDrainsInOrder(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()
	sender := &fakeSender{}

	for _, id := range []string{"M1", "M2", "M3", "M4", "M5"} {
		if _, err := s.Enqueue(ctx, item("registry", id)); err != nil {
			t.Fatal(err)
		}
	}

	w := &Worker{
		Store: s, Channel: "adt", Destination: "registry",
		Sender: sender, Log: quiet(),
		Policy: Policy{Backoff: time.Millisecond, MaxBackoff: time.Millisecond},
		Idle:   5 * time.Millisecond,
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(runCtx) }()

	waitFor(t, func() bool { return sender.count() == 5 })
	cancel()
	<-done

	if got := fmt.Sprint(sender.order()); got != "[M1 M2 M3 M4 M5]" {
		t.Errorf("delivered out of order: %v", got)
	}
}

func TestWorkerRetriesThenSucceeds(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()
	sender := &fakeSender{failFor: 3}

	if _, err := s.Enqueue(ctx, item("registry", "M1")); err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Store: s, Channel: "adt", Destination: "registry",
		Sender: sender, Log: quiet(),
		Policy: Policy{Backoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond},
		Idle:   2 * time.Millisecond,
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(runCtx) }()

	waitFor(t, func() bool { return sender.count() == 1 })
	cancel()
	<-done

	items, _, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].State != Delivered {
		t.Errorf("state = %q, want delivered", items[0].State)
	}
	if items[0].Attempts != 4 {
		t.Errorf("attempts = %d, want 4 (three failures then success)", items[0].Attempts)
	}
}

func TestWorkerGivesUpAndMovesOn(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()

	// Fails the first two sends, which are both attempts on M1, then succeeds.
	sender := &fakeSender{failFor: 2}

	if _, err := s.Enqueue(ctx, item("registry", "M1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, item("registry", "M2")); err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Store: s, Channel: "adt", Destination: "registry",
		Sender: sender, Log: quiet(),
		Policy: Policy{MaxAttempts: 2, Backoff: time.Millisecond, MaxBackoff: time.Millisecond},
		Idle:   2 * time.Millisecond,
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(runCtx) }()

	// A message that cannot be delivered must not block the queue forever. After
	// its budget is spent the queue has to move on, or one bad message stops a
	// feed indefinitely.
	waitFor(t, func() bool { return sender.count() == 1 })
	cancel()
	<-done

	if got := sender.order(); len(got) != 1 || got[0] != "M2" {
		t.Errorf("want M2 delivered after M1 was abandoned, got %v", got)
	}

	items, _, _ := s.List(ctx, Query{State: Failed})
	if len(items) != 1 || items[0].ControlID != "M1" {
		t.Errorf("want M1 recorded as failed, got %v", items)
	}
}

func TestWorkerKeepsTryingWhenMaxAttemptsIsZero(t *testing.T) {
	s := openQueue(t)
	ctx := context.Background()
	sender := &fakeSender{failFor: 6}

	if _, err := s.Enqueue(ctx, item("registry", "M1")); err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Store: s, Channel: "adt", Destination: "registry",
		Sender: sender, Log: quiet(),
		// Zero means never give up, which is a legitimate choice for a feed that
		// must not lose anything.
		Policy: Policy{MaxAttempts: 0, Backoff: time.Millisecond, MaxBackoff: time.Millisecond},
		Idle:   time.Millisecond,
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(runCtx) }()

	waitFor(t, func() bool { return sender.count() == 1 })
	cancel()
	<-done

	items, _, _ := s.List(ctx, Query{})
	if items[0].Attempts < 7 {
		t.Errorf("attempts = %d, want at least 7", items[0].Attempts)
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	p := Policy{Backoff: time.Second, MaxBackoff: 10 * time.Second}.normalise()

	for attempt, want := range map[int]time.Duration{
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 10 * time.Second, // capped
		9: 10 * time.Second,
	} {
		if got := p.delay(attempt); got != want {
			t.Errorf("delay(%d) = %s, want %s", attempt, got, want)
		}
	}

	// A queue retrying for days must not overflow into a negative duration and
	// start hammering the receiver.
	if got := p.delay(200); got != 10*time.Second {
		t.Errorf("delay(200) = %s, want the ceiling", got)
	}
}

func TestPolicyDefaultsAreSane(t *testing.T) {
	p := Policy{}.normalise()
	if p.Backoff != DefaultBackoff || p.MaxBackoff != DefaultMaxBackoff {
		t.Errorf("defaults = %s/%s", p.Backoff, p.MaxBackoff)
	}
	// A ceiling below the first delay would silently cut the first wait short.
	p = Policy{Backoff: time.Minute, MaxBackoff: time.Second}.normalise()
	if p.MaxBackoff < p.Backoff {
		t.Error("the ceiling should be raised to at least the first delay")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the queue to drain")
}
