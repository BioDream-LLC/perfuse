package queue

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"
)

// Sender delivers one message. It is the same contract the engine uses, so a
// destination's real sender drains the queue rather than a reimplementation of it
// — otherwise the retry path could succeed where the live path fails, or worse,
// the other way round.
type Sender interface {
	Send(ctx context.Context, raw []byte) error
	Describe() string
}

// Policy bounds how hard a queued message is retried.
type Policy struct {
	// MaxAttempts is how many times the queue tries before giving up. Zero means
	// keep trying, which is a legitimate choice for a feed that must not lose
	// anything, but it has to be chosen rather than inherited.
	MaxAttempts int

	// Backoff is the first delay, doubling up to MaxBackoff.
	Backoff    time.Duration
	MaxBackoff time.Duration

	// Timeout bounds one attempt.
	Timeout time.Duration
}

// Defaults for anything left unset. The backoff ceiling is a minute rather than
// something larger because a receiver that comes back after an outage should be
// found quickly; waiting an hour to notice would turn a two-minute blip into an
// hour-long backlog.
const (
	DefaultBackoff    = 5 * time.Second
	DefaultMaxBackoff = time.Minute
	DefaultTimeout    = 30 * time.Second
)

func (p Policy) normalise() Policy {
	if p.Backoff <= 0 {
		p.Backoff = DefaultBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = DefaultMaxBackoff
	}
	if p.MaxBackoff < p.Backoff {
		p.MaxBackoff = p.Backoff
	}
	if p.Timeout <= 0 {
		p.Timeout = DefaultTimeout
	}
	return p
}

// delay returns how long to wait before attempt n, counting from 1.
func (p Policy) delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Doubling, computed in float to avoid overflowing a Duration on a queue that
	// has been retrying for days.
	d := float64(p.Backoff) * math.Pow(2, float64(attempt-1))
	if d > float64(p.MaxBackoff) || math.IsInf(d, 0) {
		return p.MaxBackoff
	}
	return time.Duration(d)
}

// Observer is told what the worker did, so metrics and alerts can be driven
// without the queue importing either.
type Observer interface {
	QueueDelivered(channel, destination string, attempts int, waited time.Duration)
	QueueRetried(channel, destination string, attempts int, cause string)
	QueueGaveUp(channel, destination string, attempts int, cause string)
}

// Worker drains one destination's queue, in order, one message at a time.
//
// One worker per destination is the whole point. Running several would drain
// faster and reorder the messages, and an A03 arriving before its A01 is a
// receiving system being told about a discharge for a patient it has never heard
// of.
type Worker struct {
	Store       *Store
	Channel     string
	Destination string
	Sender      Sender
	Policy      Policy
	Log         *slog.Logger
	Observer    Observer

	// Idle is how long to wait before looking again when there was nothing to do.
	// Short enough that a recovered destination is noticed quickly, long enough
	// that an empty queue is not a busy loop.
	Idle time.Duration

	wake chan struct{}
	once sync.Once
}

// DefaultIdle is the poll interval when the queue is empty.
const DefaultIdle = 2 * time.Second

func (w *Worker) init() {
	w.once.Do(func() {
		w.wake = make(chan struct{}, 1)
		if w.Idle <= 0 {
			w.Idle = DefaultIdle
		}
		if w.Log == nil {
			w.Log = slog.Default()
		}
		w.Policy = w.Policy.normalise()
	})
}

// Wake asks the worker to look now rather than at the next poll. Called when
// something is enqueued, so the first retry after a failure is prompt.
func (w *Worker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
		// Already pending. One wake-up is as good as three.
	}
}

// Run drains until the context is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.init()

	log := w.Log.With("channel", w.Channel, "destination", w.Destination)
	log.Info("queue worker started", "sender", w.Sender.Describe())
	defer log.Info("queue worker stopped")

	timer := time.NewTimer(w.Idle)
	defer timer.Stop()

	for {
		worked, wait := w.step(ctx, log)

		if ctx.Err() != nil {
			return
		}
		if worked {
			// Something moved. Look again immediately rather than sleeping, so a
			// backlog drains at the speed of the receiver.
			continue
		}

		if wait <= 0 {
			wait = w.Idle
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-w.wake:
		}
	}
}

// step attempts at most one delivery. It reports whether it did something, and
// how long to wait if it did not.
func (w *Worker) step(ctx context.Context, log *slog.Logger) (bool, time.Duration) {
	now := time.Now().UTC()

	item, wait, err := w.Store.Next(ctx, w.Channel, w.Destination, now)
	if err != nil {
		log.Error("reading the queue failed", "error", err)
		return false, w.Idle
	}
	if item == nil {
		// Either the queue is empty, or its head is still backing off. In the
		// second case wait exactly that long: sleeping for the whole poll interval
		// would add latency to a recovery, and skipping ahead to a message that is
		// due would deliver it out of order.
		if wait > 0 {
			return false, wait
		}
		return false, w.Idle
	}

	attempt := item.Attempts + 1
	sendCtx, cancel := context.WithTimeout(ctx, w.Policy.Timeout)
	sendErr := w.Sender.Send(sendCtx, item.Raw)
	cancel()

	if sendErr == nil {
		waited := time.Since(item.EnqueuedAt)
		if err := w.Store.Succeeded(ctx, item.ID, attempt); err != nil {
			// The message arrived. Failing to record that would replay it, and a
			// duplicate admission is worse than a stale queue row.
			log.Error("delivered from the queue but could not mark it delivered",
				"id", item.ID, "error", err)
		}
		log.Info("delivered from the queue",
			"id", item.ID, "control_id", item.ControlID,
			"attempt", attempt, "waited", waited.Round(time.Millisecond))
		if w.Observer != nil {
			w.Observer.QueueDelivered(w.Channel, w.Destination, attempt, waited)
		}
		return true, 0
	}

	if ctx.Err() != nil {
		// Shutting down. Leave the item pending rather than counting an attempt
		// that never really happened.
		return false, 0
	}

	cause := sendErr.Error()

	if w.Policy.MaxAttempts > 0 && attempt >= w.Policy.MaxAttempts {
		if err := w.Store.GaveUp(ctx, item.ID, attempt, cause); err != nil {
			log.Error("could not mark a queue item failed", "id", item.ID, "error", err)
		}
		log.Error("giving up on a queued message",
			"id", item.ID, "control_id", item.ControlID,
			"attempts", attempt, "error", cause)
		if w.Observer != nil {
			w.Observer.QueueGaveUp(w.Channel, w.Destination, attempt, cause)
		}
		// Give up and move on: the next message is now at the head. Returning true
		// keeps the queue moving rather than stopping on the first bad message.
		return true, 0
	}

	next := now.Add(w.Policy.delay(attempt))
	if err := w.Store.Retry(ctx, item.ID, attempt, next, cause); err != nil {
		log.Error("could not schedule a retry", "id", item.ID, "error", err)
		return false, w.Idle
	}
	log.Warn("a queued delivery failed, will retry",
		"id", item.ID, "control_id", item.ControlID,
		"attempt", attempt, "next", next.Format(time.RFC3339), "error", cause)
	if w.Observer != nil {
		w.Observer.QueueRetried(w.Channel, w.Destination, attempt, cause)
	}

	// Do not return true here. The head of the queue is still the head, and
	// looping immediately would spin against a destination that is down.
	return false, time.Until(next)
}
