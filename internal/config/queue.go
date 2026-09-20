package config

import (
	"errors"
	"fmt"
	"time"
)

// QueueConfig turns on durable queueing for a destination.
//
// Without it, a destination that is down for two minutes loses everything sent
// during those two minutes: retries happen in memory and when they run out the
// message is recorded as failed and gone. With it, the message is on disk and
// keeps its place until the receiver comes back.
//
// It is off by default, which is a deliberate choice rather than caution. A queue
// changes what an acknowledgement means and it changes ordering behaviour during
// an outage, and both of those are things an operator should opt into knowingly
// rather than discover.
type QueueConfig struct {
	// Enabled turns the queue on for this destination.
	Enabled bool `yaml:"enabled,omitempty"`

	// MaxAttempts is how many times the queue tries before marking a message
	// failed and moving on to the next one. Zero means keep trying indefinitely,
	// which is legitimate for a feed that must not lose anything but should be
	// chosen rather than inherited.
	MaxAttempts int `yaml:"max_attempts,omitempty"`

	// Backoff is the wait before the second attempt, doubling up to MaxBackoff.
	Backoff time.Duration `yaml:"backoff,omitempty"`

	// MaxBackoff caps the growing delay. The default is a minute rather than
	// something larger because a receiver that comes back after a brief outage
	// should be found quickly: an hour-long ceiling turns a two-minute blip into
	// an hour of backlog.
	MaxBackoff time.Duration `yaml:"max_backoff,omitempty"`

	// MaxDepth refuses new messages once this many are waiting, so a receiver
	// that has been down for a week cannot fill the disk. Zero means no limit.
	//
	// When the limit is reached the message is failed rather than queued, and the
	// sender is told so — which is the honest answer, because we are no longer
	// promising to deliver it.
	MaxDepth int `yaml:"max_depth,omitempty"`

	// RetainHours is how long delivered and abandoned items are kept before being
	// purged. They are kept at all so somebody watching a backlog drain can see it
	// happening, and so an abandoned message can be found afterwards.
	RetainHours int `yaml:"retain_hours,omitempty"`
}

// Queue defaults.
const (
	DefaultQueueBackoff     = 5 * time.Second
	DefaultQueueMaxBackoff  = time.Minute
	DefaultQueueRetainHours = 72
)

// IsEnabled reports whether the queue is on.
func (q *QueueConfig) IsEnabled() bool { return q != nil && q.Enabled }

// Retention returns how long finished items are kept.
func (q *QueueConfig) Retention() time.Duration {
	hours := DefaultQueueRetainHours
	if q != nil && q.RetainHours > 0 {
		hours = q.RetainHours
	}
	return time.Duration(hours) * time.Hour
}

func (q *QueueConfig) validate() []error {
	if q == nil {
		return nil
	}
	var errs []error

	if q.MaxAttempts < 0 {
		errs = append(errs, errors.New("queue.max_attempts must not be negative"))
	}
	if q.Backoff < 0 {
		errs = append(errs, errors.New("queue.backoff must not be negative"))
	}
	if q.MaxBackoff < 0 {
		errs = append(errs, errors.New("queue.max_backoff must not be negative"))
	}
	if q.Backoff > 0 && q.MaxBackoff > 0 && q.MaxBackoff < q.Backoff {
		errs = append(errs, fmt.Errorf(
			"queue.max_backoff (%s) is shorter than queue.backoff (%s), so the ceiling would cut the first wait short",
			q.MaxBackoff, q.Backoff))
	}
	if q.MaxDepth < 0 {
		errs = append(errs, errors.New("queue.max_depth must not be negative"))
	}
	if q.RetainHours < 0 {
		errs = append(errs, errors.New("queue.retain_hours must not be negative"))
	}

	// A ceiling long enough that a receiver coming back is not noticed for hours
	// defeats the purpose of queueing at all.
	if q.MaxBackoff > time.Hour {
		errs = append(errs, fmt.Errorf(
			"queue.max_backoff of %s means a recovered receiver may wait that long "+
				"before anything is tried again; keep it under an hour", q.MaxBackoff))
	}

	return errs
}
