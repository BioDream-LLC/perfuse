package api

import (
	"sync"
	"time"
)

// maxContractHistory bounds what is kept per channel.
//
// A window rather than everything, because this lives in memory and a long-running instance checking every fifteen
// minutes would otherwise accumulate for months. Ninety-six entries is a day at that interval, which is the span
// somebody actually looks at when asking "when did this start".
const maxContractHistory = 96

// contractVerdict is one check's outcome, kept for trend.
//
// Deliberately narrow: an outcome, a count and a time. No expectation detail and no values, because history
// multiplies whatever it stores and a profile must never report values in the first place.
type contractVerdict struct {
	At         time.Time `json:"at"`
	Judged     bool      `json:"judged"`
	Violations int       `json:"violations"`
}

// Holding reports whether this verdict was a pass. Not judged is not holding: "we have no evidence" must never read
// as "all well", which is the failure mode of every monitoring system that reports green when its input stops.
func (v contractVerdict) Holding() bool { return v.Judged && v.Violations == 0 }

// contractHistory records verdicts per channel and works out when the current state began.
type contractHistory struct {
	mu      sync.RWMutex
	entries map[string][]contractVerdict
	// startedAt is when this instance began observing, so the interface can scope its claims. Without it, a page
	// saying "failing for 20 minutes" after a 20-minute-old restart would be asserting something it cannot know.
	startedAt time.Time
}

func newContractHistory() *contractHistory {
	return &contractHistory{
		entries:   map[string][]contractVerdict{},
		startedAt: time.Now(),
	}
}

// Record adds a verdict, dropping the oldest once the window is full.
//
// Consecutive identical outcomes are all kept rather than collapsed, because the count of checks is what makes a
// rate meaningful - "failed 3 of 40 checks" and "failed 3 of 3" are very different situations and collapsing would
// erase the difference.
func (h *contractHistory) Record(channel string, v contractVerdict) {
	h.mu.Lock()
	defer h.mu.Unlock()

	list := append(h.entries[channel], v)
	if len(list) > maxContractHistory {
		list = list[len(list)-maxContractHistory:]
	}
	h.entries[channel] = list
}

// Forget drops a channel's history, for when the channel itself is removed.
func (h *contractHistory) Forget(channel string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.entries, channel)
}

// Trend describes a channel's recent contract behaviour.
type Trend struct {
	// Verdicts is the window, oldest first, for a sparkline.
	Verdicts []contractVerdict `json:"verdicts"`

	// Checks is how many are in the window, and Failing how many of those did not hold. A rate is unreadable
	// without both.
	Checks  int `json:"checks"`
	Failing int `json:"failing"`

	// ChangedAt is when the current outcome first appeared, or nil when the whole window agrees. This is the answer
	// to "when did this start", which is the first question after "what changed".
	ChangedAt *time.Time `json:"changedAt,omitempty"`

	// ObservedSince is when this instance started watching. The interface needs it to avoid claiming a duration it
	// cannot support: if ChangedAt equals the first entry, the change may well have happened before we were
	// looking.
	ObservedSince time.Time `json:"observedSince"`

	// FromStartOfWindow is true when the current outcome runs all the way back to the oldest entry, meaning the
	// real change happened earlier than anything recorded - so a duration derived from ChangedAt is a floor, not a
	// measurement.
	FromStartOfWindow bool `json:"fromStartOfWindow"`

	// Flapping reports the outcome changing repeatedly rather than once. It matters because a feed alternating
	// between holding and failing is a different problem from one that broke and stayed broken, and the same
	// number of failures describes both.
	Flapping bool `json:"flapping"`
}

// Trend returns the history for one channel.
func (h *contractHistory) Trend(channel string) Trend {
	h.mu.RLock()
	list := make([]contractVerdict, len(h.entries[channel]))
	copy(list, h.entries[channel])
	started := h.startedAt
	h.mu.RUnlock()

	out := Trend{
		Verdicts:      list,
		Checks:        len(list),
		ObservedSince: started,
	}
	if len(list) == 0 {
		return out
	}

	for _, v := range list {
		if !v.Holding() {
			out.Failing++
		}
	}

	// Walk backwards to find where the current outcome began.
	changeIndex := -1
	transitions := 0
	for i := len(list) - 2; i >= 0; i-- {
		if list[i].Holding() != list[i+1].Holding() {
			transitions++
			if changeIndex < 0 {
				changeIndex = i + 1
			}
		}
	}

	if changeIndex >= 0 {
		at := list[changeIndex].At
		out.ChangedAt = &at
	} else {
		// The whole window agrees, so whatever happened, happened before it. Reported as the oldest entry with a
		// flag saying it is a floor rather than a measurement.
		at := list[0].At
		out.ChangedAt = &at
		out.FromStartOfWindow = true
	}

	// Two or more transitions inside one window is flapping rather than a single change. One transition is a
	// change; three is a different story that a single "failing since" sentence would misrepresent.
	out.Flapping = transitions >= 2

	return out
}
