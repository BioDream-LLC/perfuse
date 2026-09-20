package api

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/contract"
	"github.com/biodream-llc/perfuse/internal/profile"
)

// Checking contracts against live traffic.
//
// The CLI already checks a contract against a directory of messages, and that is genuinely useful in a
// scheduled job. This makes it continuous, which is the difference between a tool somebody remembers to run and
// a property of the system.
//
// # Why it is periodic rather than per message
//
// A contract is about a proportion, so it needs a population. Checking on every message would mean rebuilding a
// profile of the last five hundred messages five hundred times per five hundred messages, and it would make a
// channel's throughput depend on how much history it has - the kind of performance characteristic that only
// appears in production, six months in, on the busiest feed.
//
// So it runs on a timer, over a fixed count of recent messages. Fifteen minutes and five hundred messages by
// default: often enough that a vendor change is caught the same morning, and cheap enough to be invisible.
//
// # Why the result is a count on the alert reading
//
// It would be simpler to raise alerts here directly. It would also be wrong: the alert path already has
// debounce, acknowledgement, notification and a UI, and an alert somebody cannot acknowledge is an alert they
// will silence by deleting the rule. So this produces a number and a sentence, and the evaluator does the rest.

// contractState is the last known result for one channel.
type contractState struct {
	// Violations is how many expectations did not hold. Zero when the contract held, and zero when it could
	// not be judged - those are distinguished by Judged, because a count alone would make "no evidence" look
	// like "all well".
	Violations int

	// Judged is false when there were too few messages.
	Judged bool

	// Detail is the worst violation in words, for the alert body.
	Detail string

	// Summary is the whole result in one line, for the interface.
	Summary string

	// CheckedAt is when this was produced, so the interface can say how stale it is. A contract result with
	// no timestamp invites somebody to trust a check that stopped running a week ago.
	CheckedAt time.Time
}

// contractChecker re-checks every channel's contract on a timer.
type contractChecker struct {
	rt  *Runtime
	log *slog.Logger

	mu     sync.RWMutex
	states map[string]contractState
	nextAt map[string]time.Time

	// history records every verdict so the interface can answer "when did this start", which is the first question
	// after "what changed" and the one a single current state cannot answer.
	history *contractHistory
}

func newContractChecker(rt *Runtime, log *slog.Logger) *contractChecker {
	return &contractChecker{
		rt:      rt,
		log:     log,
		states:  map[string]contractState{},
		nextAt:  map[string]time.Time{},
		history: newContractHistory(),
	}
}

// State returns the last result for a channel.
func (c *contractChecker) State(channel string) (contractState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.states[channel]
	return s, ok
}

// States returns every result, for the interface.
func (c *contractChecker) States() map[string]contractState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make(map[string]contractState, len(c.states))
	for k, v := range c.states {
		out[k] = v
	}
	return out
}

// Forget drops a channel's result.
//
// Called when a channel is deleted or its contract removed, so the interface stops showing a verdict about
// something that no longer exists - which would be worse than showing nothing, because it looks current.
// Trend returns a channel's recent verdicts.
func (c *contractChecker) Trend(channel string) Trend {
	return c.history.Trend(channel)
}

func (c *contractChecker) Forget(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, channel)
	delete(c.nextAt, channel)
	// History too, or a channel removed and later re-added would inherit the old one's trend and appear to have
	// been failing since before it existed.
	c.history.Forget(channel)
}

// CheckDue re-checks any channel whose interval has elapsed.
//
// Driven from the existing alert evaluation loop rather than its own goroutine. One timer is easier to reason
// about than two, and it means a contract result is never newer or older than the reading it is attached to.
func (c *contractChecker) CheckDue(ctx context.Context, channels []*config.Channel) {
	now := time.Now()

	for _, cfg := range channels {
		ref := cfg.Contract
		if ref == nil || ref.Contract() == nil {
			continue
		}

		c.mu.RLock()
		next, seen := c.nextAt[cfg.Name]
		c.mu.RUnlock()

		if seen && now.Before(next) {
			continue
		}

		interval := ref.Interval()

		state := c.checkOne(ctx, cfg, ref)

		c.mu.Lock()
		c.states[cfg.Name] = state
		c.nextAt[cfg.Name] = now.Add(interval)
		c.mu.Unlock()

		// Recorded outside the state lock, since history has its own, and after the state is visible so a reader
		// never sees a trend that is ahead of the current verdict.
		c.history.Record(cfg.Name, contractVerdict{
			At:         state.CheckedAt,
			Judged:     state.Judged,
			Violations: state.Violations,
		})
	}
}

// checkOne profiles recent traffic for one channel and checks its contract.
func (c *contractChecker) checkOne(
	ctx context.Context, cfg *config.Channel, ref *config.ContractRef,
) contractState {
	if c.rt == nil || c.rt.Messages == nil {
		return contractState{
			CheckedAt: time.Now(),
			Summary:   "not checked: this server is not storing messages, so there is nothing to profile",
		}
	}

	raw, err := c.rt.Messages.RecentPayloads(ctx, cfg.Name, ref.Window())
	if err != nil {
		// Logged and reported rather than raising an alert. A storage problem is not a contract violation, and
		// conflating them would send somebody looking at the wrong end - which is the specific failure this
		// whole feature exists to prevent.
		c.log.Warn("could not read recent messages to check a contract",
			"channel", cfg.Name, "err", err)
		return contractState{
			CheckedAt: time.Now(),
			Summary:   "not checked: recent messages could not be read",
		}
	}

	// Profiled according to the channel's data type. A v3 corpus read by the v2 profiler comes back with no fields,
	// which is indistinguishable from a feed that populates nothing - so the contract would report every expectation
	// as violated and the operator would go looking at the sender.
	report := profile.BuildFor(string(cfg.DataType), raw)
	result := contract.Check(ref.Contract(), report)

	state := contractState{
		Judged:    result.Judged,
		Summary:   result.Summary(),
		CheckedAt: time.Now(),
	}

	if result.Judged {
		state.Violations = len(result.Violations)
		if len(result.Violations) > 0 {
			// The worst one, which Check already sorted first. An alert is one line plus a sentence, and a
			// list of nine findings in a page it is delivered to is a list nobody reads. The full set is in
			// the interface.
			state.Detail = result.Violations[0].Says
			if len(result.Violations) > 1 {
				state.Detail += " (and others)"
			}
		}
	}

	return state
}

// EnableContracts turns on periodic contract checking.
//
// Called by the server only when at least one channel has a contract, so an installation that uses none pays
// nothing - no goroutine, no state, no reads of the message store.
func (rt *Runtime) EnableContracts(log *slog.Logger) {
	if rt.Contracts == nil {
		rt.Contracts = newContractChecker(rt, log)
	}
}

// ContractDrift reports each channel's contract result for the alert path.
//
// Returns the cached result rather than checking, because the alert loop runs every thirty seconds and
// profiling five hundred messages on that schedule would be load for no benefit.
func (rt *Runtime) ContractDrift() map[string]alerts.ContractDrift {
	if rt == nil || rt.Contracts == nil {
		return nil
	}

	out := map[string]alerts.ContractDrift{}
	for name, state := range rt.Contracts.States() {
		// An unjudged result contributes nothing rather than zero violations. Zero would be indistinguishable
		// from a contract that held, and "we have no evidence" must not read as "all well".
		if !state.Judged {
			continue
		}
		out[name] = alerts.ContractDrift{
			Violations: state.Violations,
			Detail:     state.Detail,
		}
	}
	return out
}

// CheckContractsDue re-checks any channel whose interval has elapsed.
//
// Driven from the alert loop rather than its own goroutine: one timer is easier to reason about than two, and it
// means a contract result is never newer or older than the reading it is attached to.
func (rt *Runtime) CheckContractsDue(ctx context.Context, channels []*config.Channel) {
	if rt == nil || rt.Contracts == nil {
		return
	}
	rt.Contracts.CheckDue(ctx, channels)
}
