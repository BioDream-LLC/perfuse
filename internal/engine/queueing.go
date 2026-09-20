package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/queue"
)

// Queueing is the channel's side of the durable queue.
//
// The ordering rule lives here and it is the part that matters. Once anything is
// waiting for a destination, everything for that destination goes behind it, even
// if the receiver has come back and the direct path would now succeed. HL7 is a
// stream of events about the same patients, so delivering a later A03 while an
// earlier A01 sits in a queue tells the receiving system about a discharge for a
// patient it never admitted.
//
// That costs throughput while a queue is draining. It is not configurable,
// because the faster alternative is silently wrong and the person who would turn
// it on to clear a backlog is exactly the person who cannot afford the
// consequence.

// Queues wires a channel to a durable queue. Nil means queueing is off.
type Queues struct {
	store *queue.Store
	log   *slog.Logger

	mu      sync.Mutex
	workers map[string]*queue.Worker
	cancels map[string]context.CancelFunc
	wg      sync.WaitGroup
}

// NewQueues builds the coordinator.
func NewQueues(store *queue.Store, log *slog.Logger) *Queues {
	if log == nil {
		log = slog.Default()
	}
	return &Queues{
		store:   store,
		log:     log,
		workers: map[string]*queue.Worker{},
		cancels: map[string]context.CancelFunc{},
	}
}

// SetQueues attaches a queue coordinator to the channel.
func (c *Channel) SetQueues(q *Queues) { c.queues = q }

func queueKey(channel, destination string) string { return channel + "\x00" + destination }

// start ensures a worker is draining this destination.
//
// Workers are started on demand rather than for every queued destination at
// boot, with one exception: Resume starts them for anything already waiting, so a
// restart does not leave a backlog sitting there with nobody looking at it.
func (q *Queues) start(channel string, d *destination, obs queue.Observer) *queue.Worker {
	if q == nil {
		return nil
	}
	key := queueKey(channel, d.cfg.Name)

	q.mu.Lock()
	defer q.mu.Unlock()

	if w, ok := q.workers[key]; ok {
		return w
	}

	cfg := d.cfg.Queue
	w := &queue.Worker{
		Store:       q.store,
		Channel:     channel,
		Destination: d.cfg.Name,
		Sender:      d.sender,
		Observer:    obs,
		Log:         q.log,
		Policy: queue.Policy{
			MaxAttempts: cfg.MaxAttempts,
			Backoff:     cfg.Backoff,
			MaxBackoff:  cfg.MaxBackoff,
			Timeout:     d.cfg.Timeout,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	q.workers[key] = w
	q.cancels[key] = cancel

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		w.Run(ctx)
	}()
	return w
}

// Stop halts every worker and waits for them.
//
// A worker mid-delivery is allowed to finish that attempt, because abandoning it
// would leave a message we may already have sent marked as pending, and the retry
// would duplicate it.
func (q *Queues) Stop() {
	if q == nil {
		return
	}
	q.mu.Lock()
	for _, cancel := range q.cancels {
		cancel()
	}
	q.cancels = map[string]context.CancelFunc{}
	q.workers = map[string]*queue.Worker{}
	q.mu.Unlock()

	q.wg.Wait()
}

// WakeAll asks every worker to look now.
//
// Called after an operator presses retry: somebody who has just fixed a receiver
// should see the queue move, not wait out a poll interval wondering whether the
// button worked.
func (q *Queues) WakeAll() {
	if q == nil {
		return
	}
	q.mu.Lock()
	workers := make([]*queue.Worker, 0, len(q.workers))
	for _, w := range q.workers {
		workers = append(workers, w)
	}
	q.mu.Unlock()

	for _, w := range workers {
		w.Wake()
	}
}

// Store exposes the queue for the API.
func (q *Queues) Store() *queue.Store {
	if q == nil {
		return nil
	}
	return q.store
}

// Resume starts workers for every destination that already has messages waiting.
//
// This is the difference between a queue that persists and one that recovers.
// The rows survive a restart either way; without this nothing looks at them until
// the next message happens to arrive for that destination, which for an overnight
// outage means the backlog sits untouched until morning.
func (c *Channel) resumeQueues(ctx context.Context) {
	if c.queues == nil {
		return
	}
	rows, err := c.queues.store.PendingDestinations(ctx)
	if err != nil {
		c.log.Error("could not read the queue on startup", "error", err)
		return
	}
	for _, row := range rows {
		if row.Channel != c.cfg.Name {
			continue
		}
		d := c.destinationByName(row.Destination)
		if d == nil {
			// The destination was renamed or removed while messages were waiting.
			// Say so loudly: those messages now have nowhere to go, and silently
			// leaving them would look like a queue that never drains.
			c.log.Error("queued messages have no matching destination",
				"destination", row.Destination, "waiting", row.Pending,
				"oldest", row.Oldest.Format(time.RFC3339),
				"detail", "the destination was renamed or removed; the messages are "+
					"still in the queue and can be drained or replayed once it is restored")
			continue
		}
		if !d.cfg.Queue.IsEnabled() {
			c.log.Warn("queued messages exist for a destination whose queue is now off",
				"destination", row.Destination, "waiting", row.Pending)
			continue
		}
		c.log.Info("resuming a queue after restart",
			"destination", row.Destination, "waiting", row.Pending,
			"oldest", row.Oldest.Format(time.RFC3339),
			"attempts_so_far", row.MaxAttempts)
		c.queues.start(c.cfg.Name, d, c.queueObserver())
	}
}

func (c *Channel) destinationByName(name string) *destination {
	for _, d := range c.dests {
		if d.cfg.Name == name {
			return d
		}
	}
	return nil
}

// queueBlocked reports whether this destination must queue rather than deliver.
//
// True when anything is already waiting. See the note at the top of this file:
// overtaking a queued message reorders the feed.
func (c *Channel) queueBlocked(ctx context.Context, d *destination) bool {
	if c.queues == nil || !d.cfg.Queue.IsEnabled() {
		return false
	}
	blocked, err := c.queues.store.Blocked(ctx, c.cfg.Name, d.cfg.Name)
	if err != nil {
		// Unable to tell. Treat it as blocked, because delivering directly might
		// overtake something, and a message arriving late is recoverable while one
		// arriving out of order may not be.
		c.log.Error("could not check the queue; queueing to preserve order",
			"destination", d.cfg.Name, "error", err)
		return true
	}
	return blocked
}

// enqueue puts a message on a destination's queue and wakes its worker.
//
// The write is committed before this returns, so the caller may acknowledge the
// sender. That ordering is the whole promise.
func (c *Channel) enqueue(ctx context.Context, d *destination, m *hl7.Message, raw []byte, reason string) error {
	if c.queues == nil {
		return fmt.Errorf("no queue is configured")
	}

	cfg := d.cfg.Queue
	if cfg.MaxDepth > 0 {
		depth, err := c.queueDepth(ctx, d.cfg.Name)
		if err == nil && depth >= cfg.MaxDepth {
			// Refusing is the honest answer: we are no longer promising to deliver
			// it, so saying so beats accepting it and filling the disk.
			return fmt.Errorf(
				"the queue for %s holds %d messages, at its limit of %d; "+
					"the receiver has been unreachable long enough that continuing to accept "+
					"would fill the disk",
				d.cfg.Name, depth, cfg.MaxDepth)
		}
	}

	controlID, messageType := "", ""
	if m != nil {
		controlID = m.ControlID()
		typ, event, _ := m.Type()
		messageType = typ
		if event != "" {
			// Keep the trigger event: a queue page listing eight ADTs is far less
			// useful than one showing which are admissions and which discharges.
			messageType = typ + "^" + event
		}
	}

	// The payload is copied. The buffer belongs to the connection and will be
	// reused for the next message on it, so storing the slice would queue whatever
	// arrives next instead.
	stored := make([]byte, len(raw))
	copy(stored, raw)

	id, err := c.queues.store.Enqueue(ctx, queue.Item{
		Channel:     c.cfg.Name,
		Destination: d.cfg.Name,
		ControlID:   controlID,
		MessageType: messageType,
		Raw:         stored,
		Reason:      reason,
	})
	if err != nil {
		return err
	}

	c.log.Info("queued for later delivery",
		"destination", d.cfg.Name, "id", id,
		"control_id", controlID, "reason", reason)

	if w := c.queues.start(c.cfg.Name, d, c.queueObserver()); w != nil {
		w.Wake()
	}
	c.recordQueued(d.cfg.Name)
	return nil
}

func (c *Channel) queueDepth(ctx context.Context, destination string) (int, error) {
	rows, err := c.queues.store.Depth(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if r.Channel == c.cfg.Name && r.Destination == destination {
			return r.Pending, nil
		}
	}
	return 0, nil
}

// queueObserver feeds queue activity into the same metrics the dashboard reads,
// so a backlog is visible there rather than only in the log.
func (c *Channel) queueObserver() queue.Observer { return channelQueueObserver{c} }

type channelQueueObserver struct{ c *Channel }

func (o channelQueueObserver) QueueDelivered(channel, destination string, attempts int, waited time.Duration) {
	o.c.recordDelivery(destination, attempts, waited, false)
	o.c.recordQueueDrained(destination, waited)
}

func (o channelQueueObserver) QueueRetried(channel, destination string, attempts int, cause string) {
	o.c.recordQueueRetry(destination)
}

func (o channelQueueObserver) QueueGaveUp(channel, destination string, attempts int, cause string) {
	o.c.recordQueueAbandoned(destination)
}

// queuePolicyFor is used by tests and by the API to show what a destination will
// actually do, rather than what the file says before defaults are applied.
func queuePolicyFor(d config.Destination) queue.Policy {
	cfg := d.Queue
	if cfg == nil {
		return queue.Policy{}
	}
	return queue.Policy{
		MaxAttempts: cfg.MaxAttempts,
		Backoff:     cfg.Backoff,
		MaxBackoff:  cfg.MaxBackoff,
		Timeout:     d.Timeout,
	}
}
