package engine

import (
	"context"
	"sync"
)

// Hooks used by the channel test runner.
//
// They are here rather than in a _test.go file because the runner lives in another
// package: `perfuse test` is a shipped command, not a Go test, so it cannot reach
// test-only helpers.
//
// Each one is a thin accessor over the real path. The runner deliberately does not
// get a way to bypass the filter, the declarative steps or the script, because a
// test that skipped any of those would pass while the channel failed — which is
// worse than no test, since it actively misleads.

// lastOutcome records the outcome of the most recent message, for the test runner.
//
// Kept on the channel rather than returned from handle because handle's signature
// is the transport's contract: it answers with acknowledgement bytes, which is what
// a sender gets. Changing that shape to serve testing would put test concerns into
// the production path.
type lastOutcome struct {
	mu      sync.Mutex
	outcome Outcome
}

func (l *lastOutcome) set(o Outcome) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outcome = o
}

func (l *lastOutcome) get() Outcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.outcome
}

// HandleForTest processes one message and returns the acknowledgement, without a
// listener.
//
// This is the same function the MLLP server calls. A test therefore exercises the
// filter, the declarative transformations and the script in the real order, with
// only the transport replaced.
func (c *Channel) HandleForTest(ctx context.Context, raw []byte) ([]byte, error) {
	return c.handle(ctx, raw)
}

// LastOutcomeForTest reports what happened to the most recent message.
func (c *Channel) LastOutcomeForTest() Outcome { return c.lastOutcome.get() }

// Close releases the senders without needing a listener to have been started.
//
// Stop is the normal path and it shuts down the server first. A test never starts
// one, so calling Stop would return an error about a server that was never there.
func (c *Channel) Close() error {
	c.closeSenders()
	return nil
}
