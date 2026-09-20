// Package admit bounds how much work is in flight at once.
//
// The engine spawns a goroutine per destination per message. Goroutines are cheap; what they hold is not.
// Each outbound delivery holds a socket at the far end and a file descriptor here, and it holds them for
// the whole retry budget - with the defaults, five attempts at a thirty second timeout plus fifteen seconds
// of backoff, so nearly three minutes. A few hundred senders against one receiver that has gone quiet is
// therefore a few hundred descriptors held for minutes, and the process runs out.
//
// Running out is the failure worth preventing, because it does not surface where it happened. It surfaces
// as "too many open files" somewhere unrelated: the database cannot open its journal, the interface stops
// accepting, a log cannot be written. The engine looks broken in three places at once and the cause is a
// partner system that stopped answering.
//
// Two limits, and the second matters more than the first:
//
//	A total, so the process stays inside its descriptor budget.
//
//	A limit per destination, so one receiver cannot consume the total. Without it a single hung partner
//	takes down every other channel, which is worse than having no limit at all: the blast radius grows
//	from one interface to all of them.
package admit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBusy is returned when no slot came free in time.
//
// Deliberately an error the caller treats as a failed delivery, so it joins the path that already exists
// for a receiver that will not answer: the message goes to that destination's queue if it has one, and is
// reported as undelivered if it does not. A destination saturated with stalled deliveries *is* a receiver
// that is not answering, so routing it the same way is honest rather than convenient.
var ErrBusy = errors.New("no delivery slot came free")

// Controller admits deliveries within a total and a per-destination limit.
type Controller struct {
	// total is the process-wide budget. Nil means unlimited, which is what tests and single-channel
	// installations get when nothing needs bounding.
	total chan struct{}

	// perDestination is how many concurrent deliveries one destination may have.
	perDestination int

	mu    sync.Mutex
	slots map[string]chan struct{}

	// Counters, read by the metrics endpoint. A limit nobody can see is a limit somebody will eventually
	// blame for something else.
	inFlight  atomic.Int64
	waiting   atomic.Int64
	admitted  atomic.Int64
	rejected  atomic.Int64
	waitNanos atomic.Int64
}

// Limits describes what a controller will permit.
type Limits struct {
	// Total is the number of deliveries that may be in flight across every channel. Zero means unlimited.
	Total int

	// PerDestination is the number one destination may have in flight. Zero means unlimited.
	PerDestination int
}

// New builds a controller.
func New(l Limits) *Controller {
	c := &Controller{
		perDestination: l.PerDestination,
		slots:          make(map[string]chan struct{}),
	}
	if l.Total > 0 {
		c.total = make(chan struct{}, l.Total)
	}
	return c
}

// Acquire reserves a slot for one delivery to key, and returns the release.
//
// The order of the two reservations is the whole point and is not interchangeable. The per-destination slot
// is taken first, then the total.
//
// Taken the other way round, a delivery waiting for a hung destination would be holding a slice of the
// process-wide budget while it waited. Enough of them and the budget is gone, so every other channel is
// blocked by a receiver it does not send to - which is the failure this package exists to prevent, arrived
// at by a different route. Taking the per-destination slot first means waiters for a hung receiver queue up
// against that receiver's own small allowance and nothing else.
//
// A nil controller admits everything, so a caller that was built without limits needs no branch.
func (c *Controller) Acquire(ctx context.Context, key string, wait time.Duration) (func(), error) {
	if c == nil {
		return func() {}, nil
	}

	started := time.Now()
	c.waiting.Add(1)
	defer func() {
		c.waiting.Add(-1)
		c.waitNanos.Add(int64(time.Since(started)))
	}()

	// A bounded wait, so a saturated destination becomes a queued message rather than a goroutine that
	// waits for ever. Unbounded waiting is how a limit turns into a hang.
	waitCtx := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}

	release := func() {}

	if c.perDestination > 0 {
		slot := c.slotFor(key)
		select {
		case slot <- struct{}{}:
			release = func() { <-slot }
		case <-waitCtx.Done():
			c.rejected.Add(1)
			return nil, fmt.Errorf("%w for %s within %s (%d already in flight to it)",
				ErrBusy, key, wait, c.perDestination)
		}
	}

	if c.total != nil {
		select {
		case c.total <- struct{}{}:
		case <-waitCtx.Done():
			release()
			c.rejected.Add(1)
			return nil, fmt.Errorf("%w: the process-wide delivery limit of %d is reached",
				ErrBusy, cap(c.total))
		}
		inner := release
		release = func() {
			<-c.total
			inner()
		}
	}

	c.admitted.Add(1)
	c.inFlight.Add(1)

	var once sync.Once
	return func() {
		once.Do(func() {
			c.inFlight.Add(-1)
			release()
		})
	}, nil
}

// slotFor returns the semaphore for one destination, making it on first use.
//
// Kept rather than reclaimed when it empties. The number of destinations is bounded by configuration, so
// this map cannot grow without someone editing channel files, and reclaiming an empty semaphore is a race
// with the next acquirer for no benefit.
func (c *Controller) slotFor(key string) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.slots[key]; ok {
		return s
	}
	s := make(chan struct{}, c.perDestination)
	c.slots[key] = s
	return s
}

// Stats is what the controller will say about itself.
type Stats struct {
	InFlight       int64
	Waiting        int64
	Admitted       int64
	Rejected       int64
	WaitSeconds    float64
	Total          int
	PerDestination int
}

// Stats reports the counters.
func (c *Controller) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	total := 0
	if c.total != nil {
		total = cap(c.total)
	}
	return Stats{
		InFlight:       c.inFlight.Load(),
		Waiting:        c.waiting.Load(),
		Admitted:       c.admitted.Load(),
		Rejected:       c.rejected.Load(),
		WaitSeconds:    time.Duration(c.waitNanos.Load()).Seconds(),
		Total:          total,
		PerDestination: c.perDestination,
	}
}
