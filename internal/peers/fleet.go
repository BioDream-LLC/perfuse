package peers

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fleet polls the configured peers and caches what they said.
//
// Cache only. The aggregator holds no messages and no message content, so patient data never leaves the instance
// that received it. A fleet view is therefore safe by construction rather than by policy: there is no central store
// to protect, no new place for PHI to sit, and nothing extra to cover in an agreement.
type Fleet struct {
	cfg    Config
	client *Client

	mu       sync.RWMutex
	statuses map[string]Status

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// New builds a fleet poller. Validate the configuration first.
func New(cfg Config) *Fleet {
	// Defaults applied here, not only on the way in from a file.
	//
	// Validate fills them, and it only runs when a peers file is loaded - so a Fleet built directly from a zero
	// Config had a poll interval of zero. That was harmless while Start returned immediately with no peers, and
	// became a panic in NewTicker the moment it did not. A constructor that produces an object which panics when
	// used is the wrong place to require care from callers.
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = DefaultPollEvery
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}

	f := &Fleet{
		cfg:      cfg,
		client:   NewClient(cfg.Timeout),
		statuses: make(map[string]Status, len(cfg.Peers)),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}

	// Seeded as Unknown rather than left absent, so a peer that has never been polled is visibly "not asked yet"
	// instead of missing from the list. A fleet view whose membership changes as polling progresses is a fleet view
	// that cannot be trusted to say how many servers there are.
	for _, p := range cfg.Peers {
		f.statuses[p.Name] = Status{
			Name:         p.Name,
			URL:          p.URL,
			Reachability: Unknown,
			AllowControl: p.AllowControl,
		}
	}

	return f
}

// Start begins polling until Stop is called.
func (f *Fleet) Start() {
	// Started even with no peers.
	//
	// It used to return immediately, which was fine while the peer list could only come from a file read at
	// startup. Now that peers can be added from the interface, bailing out here would mean the first one added
	// was never polled and the fleet view showed it as permanently unreachable - a feature that appears broken
	// on first use.
	go func() {
		defer close(f.done)

		// Polled once immediately, because waiting a full interval before showing anything means the fleet page is
		// blank for fifteen seconds after a restart and looks broken.
		f.pollAll()

		ticker := time.NewTicker(f.cfg.PollEvery)
		defer ticker.Stop()

		for {
			select {
			case <-f.stop:
				return
			case <-ticker.C:
				f.pollAll()
			}
		}
	}()
}

// Peers returns the peers currently being polled.
func (f *Fleet) Peers() []Peer {
	f.mu.RLock()
	defer f.mu.RUnlock()

	out := make([]Peer, len(f.cfg.Peers))
	copy(out, f.cfg.Peers)
	return out
}

// SetPeers replaces the peer list while polling continues.
//
// Statuses for peers that have gone are dropped. Keeping them would leave a removed instance on the fleet page
// showing its last known state for ever, which reads as an instance that has stopped responding rather than one
// that was deliberately removed - and somebody would go looking for a server that is fine.
func (f *Fleet) SetPeers(list []Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cfg.Peers = make([]Peer, len(list))
	copy(f.cfg.Peers, list)

	wanted := make(map[string]bool, len(list))
	for _, p := range list {
		wanted[p.Name] = true
	}
	for name := range f.statuses {
		if !wanted[name] {
			delete(f.statuses, name)
		}
	}
}

// Stop ends polling and waits for the loop to finish.
func (f *Fleet) Stop() {
	f.once.Do(func() { close(f.stop) })
	<-f.done
}

// pollAll polls every peer concurrently.
//
// Concurrent because sequential polling makes the whole round as slow as the sum of its timeouts, and one
// unreachable peer would delay the readings for every healthy one - the opposite of what a fleet view is for.
func (f *Fleet) pollAll() {
	ctx, cancel := context.WithTimeout(context.Background(), f.cfg.PollEvery)
	defer cancel()

	// Read each round rather than captured once, so a peer added while this is running is polled on the next
	// pass without anything needing to be restarted.
	peers := f.Peers()

	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func(p Peer) {
			defer wg.Done()

			f.mu.RLock()
			previous := f.statuses[p.Name]
			f.mu.RUnlock()

			status := f.client.Poll(ctx, p, previous)

			f.mu.Lock()
			f.statuses[p.Name] = status
			f.mu.Unlock()
		}(p)
	}
	wg.Wait()
}

// Statuses returns the cached status of every peer.
//
// Sorted with anything needing attention first, then by name. Sorted rather than map order because these get read
// under pressure and compared between refreshes, and a list that reorders itself is unreadable.
func (f *Fleet) Statuses() []Status {
	f.mu.RLock()
	out := make([]Status, 0, len(f.statuses))
	for _, s := range f.statuses {
		out = append(out, s)
	}
	f.mu.RUnlock()

	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].NeedsAttention(), out[j].NeedsAttention()
		if ai != aj {
			return ai
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Peer returns one peer's configuration by name.
func (f *Fleet) Peer(name string) (Peer, bool) {
	for _, p := range f.cfg.Peers {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Peer{}, false
}

// Label is how this instance names itself in a fleet view.
func (f *Fleet) Label() string { return f.cfg.Label }

// PollEverySeconds is the polling interval, so the interface can say how fresh these numbers can possibly be rather
// than implying they are live.
func (f *Fleet) PollEverySeconds() float64 { return f.cfg.PollEvery.Seconds() }

// Controllable reports whether any peer permits remote control.
func (f *Fleet) Controllable() bool { return f.cfg.Controllable() }

// Roll is the aggregate over the whole fleet, including this instance.
type Roll struct {
	// Total counts every instance, this one included. Reachable plus the unreachable states will always equal it,
	// which is what makes the headline honest: an instance whose state is unknown is counted somewhere.
	Total       int `json:"total"`
	Reachable   int `json:"reachable"`
	Unreachable int `json:"unreachable"`
	// Undetermined covers unknown, unauthorised and incompatible - the peers whose health nobody knows. Kept apart
	// from Unreachable because "we cannot tell" is not "it is down", and folding them together is how a fleet page
	// starts lying.
	Undetermined int `json:"undetermined"`

	ChannelsTotal   int `json:"channelsTotal"`
	ChannelsRunning int `json:"channelsRunning"`
	ChannelsErrored int `json:"channelsErrored"`
	QueueDepth      int `json:"queueDepth"`
	AlertsFiring    int `json:"alertsFiring"`

	// KnownFrom counts the instances the channel and queue figures were actually read from. Without it a total is
	// unreadable: 12 channels running across a fleet means something different when two of five servers did not
	// answer, and the number alone cannot say so.
	KnownFrom int `json:"knownFrom"`

	// MaxSkewSeconds is the worst clock disagreement observed. Surfaced rather than corrected, because it makes
	// every aggregated time series suspect and that is worth saying out loud.
	MaxSkewSeconds float64 `json:"maxSkewSeconds"`
}

// Rollup aggregates the fleet, counting this instance from its own report.
func Rollup(self SelfReport, statuses []Status) Roll {
	r := Roll{
		Total:           1 + len(statuses),
		Reachable:       1,
		KnownFrom:       1,
		ChannelsTotal:   self.Health.ChannelsTotal,
		ChannelsRunning: self.Health.ChannelsRunning,
		ChannelsErrored: self.Health.ChannelsErrored,
		QueueDepth:      self.Health.QueueDepth,
		AlertsFiring:    self.Health.AlertsFiring,
	}

	for _, s := range statuses {
		switch s.Reachability {
		case Reachable:
			r.Reachable++
		case Unreachable:
			r.Unreachable++
		default:
			r.Undetermined++
		}

		if abs(s.SkewSeconds) > abs(r.MaxSkewSeconds) {
			r.MaxSkewSeconds = s.SkewSeconds
		}

		if s.Health == nil {
			continue
		}
		r.KnownFrom++
		r.ChannelsTotal += s.Health.ChannelsTotal
		r.ChannelsRunning += s.Health.ChannelsRunning
		r.ChannelsErrored += s.Health.ChannelsErrored
		r.QueueDepth += s.Health.QueueDepth
		r.AlertsFiring += s.Health.AlertsFiring
	}

	return r
}

// Complete reports whether every instance was read, which the interface uses to decide whether to caveat the
// totals rather than presenting a partial figure as a whole one.
func (r Roll) Complete() bool { return r.KnownFrom == r.Total }

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
