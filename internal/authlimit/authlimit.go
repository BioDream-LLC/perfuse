// Package authlimit throttles failed authentication attempts by source address.
//
// It exists because two listeners needed the same thing and one of them had nothing. The FHIR server got a limiter when
// it got authentication; the console login had none at all, and a measurement showed twenty password guesses a second
// sustained against the administrator account with no delay and no lockout. At that rate a weak password is gone
// overnight and the only trace is a log full of identical warnings.
//
// Deliberately not a general-purpose rate limiter. It throttles failures, not requests: a busy legitimate client is
// never affected, and the only thing that accumulates is being wrong.
package authlimit

import (
	"sync"
	"time"
)

// Defaults. Five failures before any delay, doubling to a five minute maximum.
//
// Five rather than three, because a person genuinely mistyping a password twice and then getting it right on the third
// attempt is ordinary, and locking them out would make the limiter the thing people complain about instead of the thing
// that protects them.
const (
	DefaultThreshold = 5
	DefaultMaxDelay  = 5 * time.Minute
	DefaultMemory    = 30 * time.Minute
)

// Limiter tracks failures per key.
//
// The key is a source address. It is never a username: throttling by username lets anybody lock out a named account by
// guessing at it deliberately, which turns a protection into a way to deny an administrator access at the moment they
// need it most.
type Limiter struct {
	threshold int
	maxDelay  time.Duration
	memory    time.Duration

	// now is overridable so tests do not have to sleep.
	now func() time.Time

	mu sync.Mutex
	by map[string]*record
}

type record struct {
	failures int
	until    time.Time
	seen     time.Time
}

// New builds a limiter with the default thresholds.
func New() *Limiter {
	return &Limiter{
		threshold: DefaultThreshold,
		maxDelay:  DefaultMaxDelay,
		memory:    DefaultMemory,
		now:       time.Now,
		by:        map[string]*record{},
	}
}

// NewWith builds a limiter with explicit thresholds, for tests.
func NewWith(threshold int, maxDelay, memory time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		threshold: threshold,
		maxDelay:  maxDelay,
		memory:    memory,
		now:       now,
		by:        map[string]*record{},
	}
}

// Blocked reports whether this key must wait, and for how long.
func (l *Limiter) Blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.forget()

	rec, ok := l.by[key]
	if !ok {
		return 0, false
	}

	if wait := rec.until.Sub(l.now()); wait > 0 {
		return wait, true
	}
	return 0, false
}

// Failed records a failed attempt and returns how long the key is now blocked for.
func (l *Limiter) Failed(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.by[key]
	if !ok {
		rec = &record{}
		l.by[key] = rec
	}

	rec.failures++
	rec.seen = l.now()

	if rec.failures < l.threshold {
		return 0
	}

	// Doubling from one second. The shift is bounded before it is taken, because at 63 failures it would overflow to
	// zero and silently switch the throttle off exactly when it was working hardest.
	steps := rec.failures - l.threshold
	if steps > 20 {
		steps = 20
	}
	delay := time.Duration(1<<uint(steps)) * time.Second
	if delay > l.maxDelay || delay <= 0 {
		delay = l.maxDelay
	}

	rec.until = l.now().Add(delay)
	return delay
}

// Succeeded clears a key's history.
//
// Cleared rather than decremented. Somebody who has just proved they hold the right credential is not who this is for,
// and leaving a count behind would eventually throttle a busy legitimate client that mistyped once an hour ago.
//
// The tradeoff is real and worth naming: a guesser who happens to find a valid credential gets their slate wiped. That
// matters much less than it sounds, because by then they are in.
func (l *Limiter) Succeeded(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.by, key)
}

// Failures reports the current count, for a log line or a test.
func (l *Limiter) Failures(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	if rec, ok := l.by[key]; ok {
		return rec.failures
	}
	return 0
}

// Tracked reports how many keys are held, so a test can prove the map does not grow without bound.
func (l *Limiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.by)
}

// forget drops records nobody has touched recently.
//
// Without this, a scanner walking a /16 would leave sixty-five thousand entries in memory for the life of the process -
// which turns a protection against guessing into a way to exhaust memory.
func (l *Limiter) forget() {
	now := l.now()
	cutoff := now.Add(-l.memory)

	for key, rec := range l.by {
		if rec.seen.Before(cutoff) && rec.until.Before(now) {
			delete(l.by, key)
		}
	}
}
