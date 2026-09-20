package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/msgstore"
)

// rhythmCache learns what each channel normally carries, and does it rarely.
//
// # Why a cache at all
//
// The alert loop runs every thirty seconds. Learning a rhythm scans four weeks of message history, which on a busy
// installation is millions of rows. Doing that twice a minute would make the monitoring the heaviest thing on the box.
//
// # Why an hour
//
// A rhythm is measured in hours, so it cannot meaningfully change faster than that. Re-learning more often would burn
// the work to produce the same answer. Re-learning less often would mean a channel created this morning stays
// unwatched until tomorrow.
//
// # Why the previous answer is kept when learning fails
//
// A failed query returns the last good rhythm rather than nothing. Returning nothing would silently disable the rule -
// which is the failure mode this whole feature exists to prevent, applied to itself. A rhythm an hour out of date is
// still a far better basis for judging silence than no rhythm at all.
type rhythmCache struct {
	store *msgstore.Store
	log   *slog.Logger

	// weeks of history to learn from. Four is enough that one odd week cannot dominate a median, and short enough
	// that a feed whose volume genuinely changed is re-learned within a month.
	weeks int

	// every is how long a learned rhythm is reused.
	every time.Duration

	mu        sync.Mutex
	learned   map[string]alerts.RhythmSource
	learnedAt time.Time
}

func newRhythmCache(store *msgstore.Store, log *slog.Logger) *rhythmCache {
	return &rhythmCache{store: store, log: log, weeks: 4, every: time.Hour}
}

// Rhythms returns the current rhythms, learning them if the cached set has expired.
func (c *rhythmCache) Rhythms() map[string]alerts.RhythmSource {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.learned != nil && time.Since(c.learnedAt) < c.every {
		return c.learned
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	byChannel, err := c.store.LearnRhythm(ctx, "", c.weeks)
	if err != nil {
		// Kept, not cleared. See the type comment: clearing would disable the rule that watches for silence, which is
		// exactly the kind of quiet failure it exists to catch.
		c.log.Warn("could not learn what channels normally carry; keeping the previous answer",
			"err", err, "age", time.Since(c.learnedAt).Round(time.Minute))
		return c.learned
	}

	out := make(map[string]alerts.RhythmSource, len(byChannel))
	for name, r := range byChannel {
		out[name] = r
	}
	c.learned = out
	c.learnedAt = time.Now()

	c.log.Debug("learned what each channel normally carries", "channels", len(out), "weeks", c.weeks)
	return out
}
