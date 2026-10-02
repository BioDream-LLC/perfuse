package peers

import (
	"fmt"
	"time"
)

// Reachability is what the aggregator knows about a peer's availability.
//
// This is the distinction the whole feature turns on. A fleet page that shows a dead server in the same colour as a
// quiet one is worse than no fleet page, because it converts "we do not know" into "all well" - which is the failure
// mode of every monitoring system that reports green when its input has stopped. It is the same distinction made
// elsewhere here between 410 and 404, and between filtered and failed.
type Reachability string

const (
	// Unknown means this peer has not been polled yet. Distinct from unreachable: at startup, nothing is known, and
	// claiming a problem before looking is as wrong as claiming health.
	Unknown Reachability = "unknown"

	// Reachable means the last poll succeeded.
	Reachable Reachability = "reachable"

	// Unreachable means the last poll failed at the network or TLS level.
	Unreachable Reachability = "unreachable"

	// Unauthorised means the peer answered and rejected the credentials.
	//
	// Deliberately not folded into Unreachable. A wrong token and a dead server need completely different actions,
	// and reporting one as the other sends somebody to check a network that is fine.
	Unauthorised Reachability = "unauthorised"

	// Incompatible means the peer answered but not in a way this version understands.
	//
	// Its own state because the likely cause is a version mismatch mid-upgrade, which is expected and temporary,
	// and alarming somebody about it is noise.
	Incompatible Reachability = "incompatible"
)

// Status is one peer's last known state. It deliberately holds no messages and no message content: the aggregator
// caches health, and patient data never leaves the instance that received it. That makes a fleet view safe by
// construction rather than by policy - there is no central store to protect and no new place for PHI to sit.
type Status struct {
	Name string `json:"name"`
	URL  string `json:"url"`

	Reachability Reachability `json:"reachability"`

	// Error explains a failure in the terms the operator needs. Empty when reachable.
	Error string `json:"error,omitempty"`

	// CheckedAt is when this instance last polled. LastReachable is when it last succeeded, which is the question
	// people actually ask about a server that is down.
	CheckedAt     time.Time  `json:"checkedAt"`
	LastReachable *time.Time `json:"lastReachable,omitempty"`

	// LatencyMS is how long the poll took. A peer that answers slowly is a different problem from one that does
	// not answer, and it is usually the earlier warning.
	LatencyMS int64 `json:"latencyMs"`

	// Version is the peer's build. During a rolling upgrade this is the only question anybody has.
	Version string `json:"version,omitempty"`

	// SkewSeconds is the peer's clock minus this instance's clock, as observed. Reported rather than corrected,
	// because silently normalising would hide a real misconfiguration, and an aggregated time series built across
	// servers with skewed clocks is quietly wrong in a way nobody notices.
	SkewSeconds float64 `json:"skewSeconds"`

	// Health is the peer's own summary. Nil when the peer could not be read.
	Health *Health `json:"health,omitempty"`

	// AllowControl mirrors the configuration, so the interface can decide whether to offer control at all rather
	// than showing buttons that will be refused.
	AllowControl bool `json:"allowControl"`
}

// Health is the subset of a peer's state worth aggregating. Counts and rates only.
type Health struct {
	ChannelsTotal   int `json:"channelsTotal"`
	ChannelsRunning int `json:"channelsRunning"`
	ChannelsStopped int `json:"channelsStopped"`
	ChannelsErrored int `json:"channelsErrored"`

	QueueDepth int `json:"queueDepth"`
	// QueueOldestSeconds is the age of the oldest queued message. Depth alone does not distinguish a busy queue
	// from a stuck one, which is the distinction that matters.
	QueueOldestSeconds float64 `json:"queueOldestSeconds"`

	AlertsFiring int `json:"alertsFiring"`

	// Draining reports that the peer is shutting down. Without it, a server mid-restart looks like a server with a
	// problem, and somebody investigates a planned event.
	Draining bool `json:"draining"`
}

// Stale reports whether a status is older than the given age, which the interface uses to grey out numbers rather
// than presenting a stale reading as current.
func (s Status) Stale(now time.Time, max time.Duration) bool {
	if s.CheckedAt.IsZero() {
		return true
	}
	return now.Sub(s.CheckedAt) > max
}

// Summary is a one-line description for an operator.
func (s Status) Summary() string {
	switch s.Reachability {
	case Unknown:
		return "not polled yet"
	case Unauthorised:
		return "answered, but rejected the token; this is a credentials problem rather than an outage"
	case Incompatible:
		return "answered in a way this version does not understand, which usually means a version mismatch"
	case Unreachable:
		if s.LastReachable != nil {
			return fmt.Sprintf("unreachable; last answered %s ago", roundDuration(time.Since(*s.LastReachable)))
		}
		return "unreachable, and has never answered since this instance started"
	}

	if s.Health == nil {
		return "reachable"
	}

	h := s.Health
	if h.Draining {
		return fmt.Sprintf("draining; %d of %d channel(s) still running", h.ChannelsRunning, h.ChannelsTotal)
	}
	if h.ChannelsErrored > 0 {
		return fmt.Sprintf("%d channel(s) in error, %d of %d running",
			h.ChannelsErrored, h.ChannelsRunning, h.ChannelsTotal)
	}
	return fmt.Sprintf("%d of %d channel(s) running", h.ChannelsRunning, h.ChannelsTotal)
}

// NeedsAttention reports whether a peer should be sorted to the top of a fleet view.
//
// Draining is excluded on purpose: a planned restart is not a problem, and putting it at the top next to a real
// outage trains people to skim past the top of the list.
func (s Status) NeedsAttention() bool {
	switch s.Reachability {
	case Unreachable, Unauthorised, Incompatible:
		return true
	}
	if s.Health == nil {
		return false
	}
	if s.Health.Draining {
		return false
	}
	return s.Health.ChannelsErrored > 0 || s.Health.AlertsFiring > 0
}

func roundDuration(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}
