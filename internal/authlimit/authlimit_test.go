package authlimit

import (
	"testing"
	"time"
)

// clock is a controllable time source, so these tests never sleep.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

func newTestLimiter() (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 8, 21, 22, 0, 0, 0, time.UTC)}
	return NewWith(5, 5*time.Minute, 30*time.Minute, c.now), c
}

// TestFailuresBelowTheThresholdAreNotDelayed covers the ordinary mistyped password.
//
// A person who gets it wrong twice and right on the third try must not be delayed at all, or the limiter becomes the
// thing people complain about rather than the thing that protects them.
func TestFailuresBelowTheThresholdAreNotDelayed(t *testing.T) {
	l, _ := newTestLimiter()

	for i := 0; i < 4; i++ {
		if delay := l.Failed("192.0.2.1"); delay != 0 {
			t.Fatalf("failure %d was delayed by %s; nothing under the threshold should be", i+1, delay)
		}
		if _, blocked := l.Blocked("192.0.2.1"); blocked {
			t.Fatalf("blocked after only %d failures", i+1)
		}
	}
}

// TestTheThresholdBlocks covers the point of the package.
func TestTheThresholdBlocks(t *testing.T) {
	l, _ := newTestLimiter()

	for i := 0; i < 5; i++ {
		l.Failed("192.0.2.2")
	}

	wait, blocked := l.Blocked("192.0.2.2")
	if !blocked {
		t.Fatal("five failures did not block; twenty guesses a second was the measured rate without this")
	}
	if wait <= 0 {
		t.Errorf("blocked with a wait of %s", wait)
	}
}

// TestTheDelayGrows covers escalation.
func TestTheDelayGrows(t *testing.T) {
	l, c := newTestLimiter()

	var delays []time.Duration
	for i := 0; i < 9; i++ {
		d := l.Failed("192.0.2.3")
		if d > 0 {
			delays = append(delays, d)
		}
		// Past the current block, so the next failure is recorded rather than refused.
		c.advance(d + time.Second)
	}

	if len(delays) < 3 {
		t.Fatalf("only %d delays were issued", len(delays))
	}
	for i := 1; i < len(delays); i++ {
		if delays[i] <= delays[i-1] && delays[i-1] < 5*time.Minute {
			t.Errorf("delay %d (%s) did not grow beyond %s", i, delays[i], delays[i-1])
		}
	}
}

// TestTheDelayIsCapped covers the maximum.
func TestTheDelayIsCapped(t *testing.T) {
	l, c := newTestLimiter()

	for i := 0; i < 40; i++ {
		d := l.Failed("192.0.2.4")
		if d > 5*time.Minute {
			t.Fatalf("delay %s exceeds the five minute cap", d)
		}
		c.advance(d + time.Second)
	}
}

// TestManyFailuresDoNotOverflowTheDelay is the one that would be a silent disaster.
//
// The delay doubles with each failure past the threshold, so an unbounded shift eventually overflows and the throttle
// switches itself off precisely when it is working hardest - with nothing reporting it.
//
// Two defences cover this: the shift count is capped, and a delay that comes out non-positive falls back to the maximum.
// Removing either one alone leaves the test passing, which is worth knowing rather than assuming - it means this test
// asserts the property rather than one implementation of it. With both removed it fails at the thirty-ninth failure with
// a delay of minus three hundred and fifty thousand hours.
func TestManyFailuresDoNotOverflowTheDelay(t *testing.T) {
	l, c := newTestLimiter()

	for i := 0; i < 200; i++ {
		d := l.Failed("192.0.2.5")
		// Below the threshold a zero delay is correct; past it a zero delay means the shift has overflowed.
		if i+1 >= 5 && d <= 0 {
			t.Fatalf("failure %d produced a delay of %s; the throttle has switched itself off", i+1, d)
		}
		c.advance(time.Second)
	}

	if _, blocked := l.Blocked("192.0.2.5"); !blocked {
		t.Error("still not blocked after two hundred failures")
	}
}

// TestSuccessClearsTheHistory covers the legitimate client.
func TestSuccessClearsTheHistory(t *testing.T) {
	l, _ := newTestLimiter()

	for i := 0; i < 7; i++ {
		l.Failed("192.0.2.6")
	}
	if _, blocked := l.Blocked("192.0.2.6"); !blocked {
		t.Fatal("not blocked before the successful attempt")
	}

	l.Succeeded("192.0.2.6")

	if _, blocked := l.Blocked("192.0.2.6"); blocked {
		t.Error("still blocked after a successful sign-in")
	}
	if n := l.Failures("192.0.2.6"); n != 0 {
		t.Errorf("%d failures remembered after success, want 0", n)
	}
}

// TestOneAddressDoesNotBlockAnother covers the blast radius.
//
// Throttling by anything shared - a username, or a global counter - would let one guesser lock out everybody else. This
// is why the key is a source address.
func TestOneAddressDoesNotBlockAnother(t *testing.T) {
	l, _ := newTestLimiter()

	for i := 0; i < 10; i++ {
		l.Failed("198.51.100.1")
	}

	if _, blocked := l.Blocked("198.51.100.1"); !blocked {
		t.Fatal("the guessing address was not blocked")
	}
	if _, blocked := l.Blocked("198.51.100.2"); blocked {
		t.Error("a different address was blocked by somebody else's failures")
	}
}

// TestOldRecordsAreForgotten covers unbounded growth.
//
// A scanner walking a /16 would otherwise leave sixty-five thousand records in memory for the life of the process, which
// turns a defence against guessing into a way to exhaust memory.
func TestOldRecordsAreForgotten(t *testing.T) {
	l, c := newTestLimiter()

	for i := 0; i < 50; i++ {
		l.Failed("203.0.113." + itoa(i))
	}
	if l.Tracked() != 50 {
		t.Fatalf("tracking %d addresses, want 50", l.Tracked())
	}

	// Past the memory window, then one call that triggers the sweep.
	c.advance(31 * time.Minute)
	_, _ = l.Blocked("203.0.113.0")

	if l.Tracked() != 0 {
		t.Errorf("still tracking %d addresses after the memory window; the map grows without bound", l.Tracked())
	}
}

// TestABlockExpires covers recovery.
//
// A permanent lockout would mean one burst of guessing against an address denies that address forever, and behind a NAT
// that address is an entire hospital.
func TestABlockExpires(t *testing.T) {
	l, c := newTestLimiter()

	for i := 0; i < 5; i++ {
		l.Failed("192.0.2.9")
	}
	wait, blocked := l.Blocked("192.0.2.9")
	if !blocked {
		t.Fatal("not blocked")
	}

	c.advance(wait + time.Second)

	if _, stillBlocked := l.Blocked("192.0.2.9"); stillBlocked {
		t.Error("still blocked after the delay elapsed")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
