package admit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The property this package exists for: one hung destination must not stop any other.
//
// This is the failure that turns a partner system going quiet into an outage. Every delivery holds a
// descriptor for the whole retry budget, so a few hundred senders against one dead receiver exhausts the
// process - and the symptom appears somewhere unrelated, as "too many open files" from the database or the
// web interface.
//
// A total limit alone does not fix it. It moves it: the dead receiver's deliveries consume the whole budget
// and every other channel is blocked by a receiver it does not send to. So the test to write is not "does
// the limit hold" but "can a healthy destination still be reached while a broken one is saturated".
func TestAHungDestinationDoesNotBlockAHealthyOne(t *testing.T) {
	// A tight total, so a naive implementation is certain to exhaust it.
	c := New(Limits{Total: 4, PerDestination: 2})

	hung := make(chan struct{})
	var held sync.WaitGroup

	// Saturate the broken destination, and hold the slots as a stalled delivery would.
	for i := 0; i < 2; i++ {
		release, err := c.Acquire(context.Background(), "dead-receiver", time.Second)
		if err != nil {
			t.Fatalf("filling the broken destination failed at %d: %v", i, err)
		}
		held.Add(1)
		go func() {
			defer held.Done()
			<-hung
			release()
		}()
	}

	// More senders arrive for the same broken destination. These must not eat the total budget while they
	// wait, which is what happens if the total is reserved before the per-destination slot.
	var waiters sync.WaitGroup
	for i := 0; i < 20; i++ {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			release, err := c.Acquire(context.Background(), "dead-receiver", 300*time.Millisecond)
			if err == nil {
				release()
			}
		}()
	}

	// Give the crowd time to pile up against the broken destination.
	time.Sleep(100 * time.Millisecond)

	// The whole point: a different destination is still reachable.
	release, err := c.Acquire(context.Background(), "healthy-receiver", 2*time.Second)
	if err != nil {
		t.Fatalf("a healthy destination could not be reached while another was saturated: %v\n\n"+
			"This is the defect the package exists to prevent. If the total budget is reserved before the "+
			"per-destination slot, waiters for a broken receiver hold the budget while they wait, and one "+
			"bad partner blocks every interface on the server.", err)
	}
	release()

	close(hung)
	held.Wait()
	waiters.Wait()
}

// The per-destination limit is actually enforced.
func TestPerDestinationLimitHolds(t *testing.T) {
	c := New(Limits{PerDestination: 3})

	var releases []func()
	for i := 0; i < 3; i++ {
		release, err := c.Acquire(context.Background(), "d", time.Second)
		if err != nil {
			t.Fatalf("acquire %d failed: %v", i, err)
		}
		releases = append(releases, release)
	}

	// The fourth must wait, and time out rather than being admitted.
	if _, err := c.Acquire(context.Background(), "d", 100*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("a fourth delivery was admitted past a limit of 3: err=%v", err)
	}

	// Releasing one lets the next in.
	releases[0]()
	release, err := c.Acquire(context.Background(), "d", time.Second)
	if err != nil {
		t.Fatalf("a slot did not come free after a release: %v", err)
	}
	release()

	for _, r := range releases[1:] {
		r()
	}
}

// The total limit is enforced across different destinations.
func TestTotalLimitHoldsAcrossDestinations(t *testing.T) {
	c := New(Limits{Total: 2, PerDestination: 5})

	a, err := c.Acquire(context.Background(), "one", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Acquire(context.Background(), "two", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Acquire(context.Background(), "three", 100*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("a third delivery was admitted past a total of 2: err=%v", err)
	}

	a()
	c2, err := c.Acquire(context.Background(), "three", time.Second)
	if err != nil {
		t.Fatalf("a slot did not come free after a release: %v", err)
	}
	c2()
	b()
}

// A refusal must say which limit was reached, because the two call for different actions: raise the total,
// or find out why one receiver is slow.
func TestRefusalSaysWhichLimitWasReached(t *testing.T) {
	perDest := New(Limits{PerDestination: 1})
	release, err := perDest.Acquire(context.Background(), "slow-lab", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = perDest.Acquire(context.Background(), "slow-lab", 50*time.Millisecond)
	if err == nil || !contains(err.Error(), "slow-lab") {
		t.Errorf("a per-destination refusal does not name the destination: %v", err)
	}
	release()

	total := New(Limits{Total: 1})
	release2, err := total.Acquire(context.Background(), "anything", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = total.Acquire(context.Background(), "other", 50*time.Millisecond)
	if err == nil || !contains(err.Error(), "process-wide") {
		t.Errorf("a total refusal does not say it was the process-wide limit: %v", err)
	}
	release2()
}

// Releasing twice must not free a slot that was never held, or the limit drifts upward over time until it
// is not a limit.
func TestReleaseIsIdempotent(t *testing.T) {
	c := New(Limits{Total: 1, PerDestination: 1})

	release, err := c.Acquire(context.Background(), "d", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	release()

	// Exactly one slot should be available, not three.
	first, err := c.Acquire(context.Background(), "d", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), "d", 50*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Error("releasing more than once raised the effective limit")
	}
	first()
}

// A cancelled context must not leave a slot held, or the budget leaks away under load.
func TestCancellingDoesNotLeakASlot(t *testing.T) {
	c := New(Limits{Total: 1, PerDestination: 1})

	held, err := c.Acquire(context.Background(), "d", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Acquire(ctx, "d", 5*time.Second)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	if err := <-done; err == nil {
		t.Fatal("a cancelled acquire reported success")
	}

	held()

	// The slot is free, which it would not be if the cancelled attempt had kept it.
	got, err := c.Acquire(context.Background(), "d", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("the slot leaked when an acquire was cancelled: %v", err)
	}
	got()
}

// Zero limits mean no limit, so an installation that needs none pays nothing.
func TestZeroMeansUnlimited(t *testing.T) {
	c := New(Limits{})
	var releases []func()
	for i := 0; i < 200; i++ {
		release, err := c.Acquire(context.Background(), "d", time.Millisecond)
		if err != nil {
			t.Fatalf("an unlimited controller refused at %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	for _, r := range releases {
		r()
	}
}

// A nil controller admits everything, so callers need no branch.
func TestNilControllerAdmits(t *testing.T) {
	var c *Controller
	release, err := c.Acquire(context.Background(), "d", time.Second)
	if err != nil {
		t.Fatalf("a nil controller refused: %v", err)
	}
	release()
	if got := c.Stats(); got.InFlight != 0 {
		t.Errorf("a nil controller reported %d in flight", got.InFlight)
	}
}

// The counters have to be right, because they are what an operator uses to decide whether the limit is the
// problem or the receiver is.
func TestStatsCount(t *testing.T) {
	c := New(Limits{Total: 4, PerDestination: 2})

	r1, _ := c.Acquire(context.Background(), "a", time.Second)
	r2, _ := c.Acquire(context.Background(), "b", time.Second)

	if got := c.Stats(); got.InFlight != 2 {
		t.Errorf("in flight = %d, want 2", got.InFlight)
	}
	if got := c.Stats(); got.Admitted != 2 {
		t.Errorf("admitted = %d, want 2", got.Admitted)
	}

	r1()
	r2()
	if got := c.Stats(); got.InFlight != 0 {
		t.Errorf("in flight after release = %d, want 0", got.InFlight)
	}

	// A refusal is counted, so saturation is visible rather than inferred.
	full := New(Limits{PerDestination: 1})
	held, _ := full.Acquire(context.Background(), "d", time.Second)
	_, _ = full.Acquire(context.Background(), "d", 20*time.Millisecond)
	if got := full.Stats(); got.Rejected != 1 {
		t.Errorf("rejected = %d, want 1", got.Rejected)
	}
	held()
}

// Under real concurrency the limit must never be exceeded, which a counter check under -race will catch.
func TestLimitIsNeverExceededUnderConcurrency(t *testing.T) {
	const limit = 8
	c := New(Limits{Total: limit, PerDestination: limit})

	var live, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, err := c.Acquire(context.Background(), "d", 5*time.Second)
			if err != nil {
				return
			}
			n := live.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			live.Add(-1)
			release()
		}(i)
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Errorf("peak concurrency was %d, above the limit of %d", got, limit)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
