package peers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustValidate(t *testing.T, c *Config) {
	t.Helper()
	if err := c.Validate(); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}
}

func TestPlainHTTPToARemoteHostIsRefused(t *testing.T) {
	// The token travels on every single poll. Over plain HTTP to another machine it crosses the hospital network in
	// clear text, repeatedly, forever. Same rule already applied to fhir.url.
	c := Config{Peers: []Peer{{Name: "two", URL: "http://perfuse-02.hospital.internal:8443", Token: "t"}}}

	err := c.Validate()
	if err == nil {
		t.Fatal("plain http to a remote host was accepted")
	}
	if !strings.Contains(err.Error(), "clear text") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}
}

func TestPlainHTTPToLocalhostIsAllowed(t *testing.T) {
	// There is no network to cross, and refusing it would make local testing impossible for no security gain.
	c := Config{Peers: []Peer{{Name: "local", URL: "http://127.0.0.1:8443", Token: "t"}}}
	mustValidate(t, &c)
}

func TestAMissingTokenIsRefusedRatherThanShowingAsAnOutage(t *testing.T) {
	// Without this, a peer with no token polls, gets rejected, and appears unreachable - sending somebody to check
	// a network that is perfectly fine.
	c := Config{Peers: []Peer{{Name: "two", URL: "https://p2:8443"}}}

	err := c.Validate()
	if err == nil {
		t.Fatal("a peer with no token was accepted")
	}
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("the error does not distinguish unreachable from unauthenticated: %v", err)
	}
}

func TestDuplicateNamesAndURLsAreRefused(t *testing.T) {
	dupName := Config{Peers: []Peer{
		{Name: "two", URL: "https://a:8443", Token: "t"},
		{Name: "TWO", URL: "https://b:8443", Token: "t"},
	}}
	if err := dupName.Validate(); err == nil {
		t.Error("two peers with the same name were accepted; one would be invisible")
	}

	dupURL := Config{Peers: []Peer{
		{Name: "a", URL: "https://same:8443", Token: "t"},
		{Name: "b", URL: "https://same:8443", Token: "t"},
	}}
	err := dupURL.Validate()
	if err == nil {
		t.Fatal("two peers with the same url were accepted; the fleet would be counted twice")
	}
	if !strings.Contains(err.Error(), "larger than it is") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}
}

func TestATimeoutLongerThanTheIntervalIsRefused(t *testing.T) {
	c := Config{
		PollEvery: 5 * time.Second,
		Timeout:   30 * time.Second,
		Peers:     []Peer{{Name: "a", URL: "https://a:8443", Token: "t"}},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("a timeout longer than the poll interval was accepted")
	}
	if !strings.Contains(err.Error(), "stop updating") {
		t.Errorf("the error does not explain why it matters: %v", err)
	}
}

func TestPeersAreSortedSoTheViewIsStable(t *testing.T) {
	c := Config{Peers: []Peer{
		{Name: "zebra", URL: "https://z:8443", Token: "t"},
		{Name: "alpha", URL: "https://a:8443", Token: "t"},
	}}
	mustValidate(t, &c)

	if c.Peers[0].Name != "alpha" {
		t.Errorf("peers were not sorted: first is %q", c.Peers[0].Name)
	}
}

// fakePeer serves a SelfReport, or whatever failure the test asks for.
func fakePeer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func healthyHandler(report SelfReport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != SelfPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	}
}

func TestPollingAHealthyPeer(t *testing.T) {
	srv := fakePeer(t, healthyHandler(SelfReport{
		Label:   "perfuse-02",
		Version: "1.2.3",
		Now:     time.Now(),
		Health:  Health{ChannelsTotal: 10, ChannelsRunning: 9, ChannelsStopped: 1, QueueDepth: 4},
	}))

	c := NewClient(2 * time.Second)
	got := c.Poll(context.Background(), Peer{Name: "two", URL: srv.URL, Token: "good-token"}, Status{})

	if got.Reachability != Reachable {
		t.Fatalf("reachability = %q (%s)", got.Reachability, got.Error)
	}
	if got.Version != "1.2.3" {
		t.Errorf("version = %q", got.Version)
	}
	if got.Health == nil || got.Health.ChannelsRunning != 9 {
		t.Errorf("health did not come through: %+v", got.Health)
	}
	if got.LastReachable == nil {
		t.Error("a successful poll did not record when it succeeded")
	}
	if !strings.Contains(got.Summary(), "9 of 10") {
		t.Errorf("summary = %q", got.Summary())
	}
}

func TestABadTokenReadsAsUnauthorisedNotUnreachable(t *testing.T) {
	// The distinction that stops somebody investigating a healthy network. A wrong token and a dead server need
	// completely different actions.
	srv := fakePeer(t, healthyHandler(SelfReport{}))

	c := NewClient(2 * time.Second)
	got := c.Poll(context.Background(), Peer{Name: "two", URL: srv.URL, Token: "wrong"}, Status{})

	if got.Reachability != Unauthorised {
		t.Fatalf("reachability = %q, want unauthorised", got.Reachability)
	}
	if !strings.Contains(got.Summary(), "credentials problem") {
		t.Errorf("summary does not distinguish the cause: %q", got.Summary())
	}
}

func TestAnOlderPeerReadsAsIncompatible(t *testing.T) {
	// Expected mid-upgrade. Reporting a rolling deployment as an outage is noise, and noise is how a fleet view
	// gets ignored.
	srv := fakePeer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	c := NewClient(2 * time.Second)
	got := c.Poll(context.Background(), Peer{Name: "old", URL: srv.URL, Token: "good-token"}, Status{})

	if got.Reachability != Incompatible {
		t.Fatalf("reachability = %q, want incompatible", got.Reachability)
	}
	if !strings.Contains(got.Error, "older build") {
		t.Errorf("the error does not name the likely cause: %q", got.Error)
	}
}

func TestADeadPeerKeepsWhenItLastAnswered(t *testing.T) {
	// "When did it last work" is the first question about a server that is down, and it is unanswerable if a failed
	// poll overwrites the history.
	earlier := time.Now().Add(-90 * time.Second)
	previous := Status{LastReachable: &earlier}

	c := NewClient(200 * time.Millisecond)
	// Port 1 on loopback: nothing listens there, so this refuses fast rather than timing out.
	got := c.Poll(context.Background(), Peer{Name: "dead", URL: "http://127.0.0.1:1", Token: "t"}, previous)

	if got.Reachability != Unreachable {
		t.Fatalf("reachability = %q, want unreachable", got.Reachability)
	}
	if got.LastReachable == nil || !got.LastReachable.Equal(earlier) {
		t.Error("a failed poll discarded when the peer last answered")
	}
	if !strings.Contains(got.Summary(), "last answered") {
		t.Errorf("summary = %q", got.Summary())
	}
}

func TestANewerPeerAddingAFieldIsStillReadable(t *testing.T) {
	// A newer build adding a field must not read as a broken peer, or every upgrade would look like a fleet-wide
	// fault for as long as the rollout takes.
	srv := fakePeer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"label":"p2","version":"9.9.9","somethingNew":true,` +
			`"health":{"channelsTotal":3,"channelsRunning":3}}`))
	})

	c := NewClient(2 * time.Second)
	got := c.Poll(context.Background(), Peer{Name: "newer", URL: srv.URL, Token: "t"}, Status{})

	if got.Reachability != Reachable {
		t.Fatalf("a peer with an extra field read as %q: %s", got.Reachability, got.Error)
	}
	if got.Health == nil || got.Health.ChannelsRunning != 3 {
		t.Errorf("health did not survive the unknown field: %+v", got.Health)
	}
}

func TestClockSkewIsReportedNotCorrected(t *testing.T) {
	// Silently normalising would hide a real misconfiguration, and an aggregated series across skewed clocks is
	// wrong in a way nobody notices.
	srv := fakePeer(t, healthyHandler(SelfReport{
		Now:    time.Now().Add(45 * time.Second),
		Health: Health{ChannelsTotal: 1, ChannelsRunning: 1},
	}))

	c := NewClient(2 * time.Second)
	got := c.Poll(context.Background(), Peer{Name: "skewed", URL: srv.URL, Token: "good-token"}, Status{})

	if got.SkewSeconds < 40 || got.SkewSeconds > 50 {
		t.Errorf("skew = %.1fs, want about 45", got.SkewSeconds)
	}
}

func TestUnknownIsNotUnreachable(t *testing.T) {
	// At startup nothing is known. Claiming a problem before looking is as wrong as claiming health, and both
	// mislead in a way that costs somebody a phone call.
	cfg := Config{Peers: []Peer{{Name: "two", URL: "https://p2:8443", Token: "t"}}}
	mustValidate(t, &cfg)

	f := New(cfg)
	statuses := f.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1 - a peer must appear before it is polled", len(statuses))
	}
	if statuses[0].Reachability != Unknown {
		t.Errorf("an unpolled peer reads as %q, want unknown", statuses[0].Reachability)
	}
	if statuses[0].NeedsAttention() {
		t.Error("an unpolled peer demands attention; that is an alarm for having just started up")
	}
	if !strings.Contains(statuses[0].Summary(), "not polled") {
		t.Errorf("summary = %q", statuses[0].Summary())
	}
}

func TestTheFleetPollsAndCaches(t *testing.T) {
	srv := fakePeer(t, healthyHandler(SelfReport{
		Version: "1.0.0",
		Now:     time.Now(),
		Health:  Health{ChannelsTotal: 5, ChannelsRunning: 5},
	}))

	cfg := Config{
		PollEvery: 2 * time.Second,
		Timeout:   time.Second,
		Peers:     []Peer{{Name: "two", URL: srv.URL, Token: "good-token"}},
	}
	mustValidate(t, &cfg)

	f := New(cfg)
	f.Start()
	defer f.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := f.Statuses(); len(s) == 1 && s[0].Reachability == Reachable {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the fleet never recorded the peer as reachable: %+v", f.Statuses())
}

func TestAttentionSortsToTheTop(t *testing.T) {
	statuses := []Status{
		{Name: "healthy", Reachability: Reachable, Health: &Health{ChannelsTotal: 2, ChannelsRunning: 2}},
		{Name: "broken", Reachability: Unreachable},
	}

	// Same comparison the Fleet uses, exercised directly.
	f := &Fleet{statuses: map[string]Status{
		"healthy": statuses[0],
		"broken":  statuses[1],
	}}
	got := f.Statuses()

	if got[0].Name != "broken" {
		t.Errorf("first is %q, want the unreachable peer first", got[0].Name)
	}
}

func TestDrainingDoesNotDemandAttention(t *testing.T) {
	// A planned restart is not a problem, and putting it beside a real outage trains people to skim past the top of
	// the list - which is where the real outages are.
	s := Status{Reachability: Reachable, Health: &Health{Draining: true, ChannelsTotal: 3, ChannelsRunning: 1}}
	if s.NeedsAttention() {
		t.Error("a draining peer demands attention")
	}
	if !strings.Contains(s.Summary(), "draining") {
		t.Errorf("summary = %q", s.Summary())
	}
}

func TestTheRollupSaysHowMuchItActuallyKnows(t *testing.T) {
	// A total is unreadable without this. "12 channels running" means something different when two of five servers
	// did not answer, and the number alone cannot say so.
	self := SelfReport{Health: Health{ChannelsTotal: 4, ChannelsRunning: 4}}
	statuses := []Status{
		{Name: "b", Reachability: Reachable, Health: &Health{ChannelsTotal: 6, ChannelsRunning: 5}},
		{Name: "c", Reachability: Unreachable},
		{Name: "d", Reachability: Unauthorised},
	}

	r := Rollup(self, statuses)

	if r.Total != 4 {
		t.Errorf("total = %d, want 4 including this instance", r.Total)
	}
	if r.Reachable != 2 {
		t.Errorf("reachable = %d, want 2", r.Reachable)
	}
	if r.Unreachable != 1 {
		t.Errorf("unreachable = %d, want 1", r.Unreachable)
	}
	if r.Undetermined != 1 {
		t.Errorf("undetermined = %d, want 1 - unauthorised is not the same as down", r.Undetermined)
	}
	if r.Reachable+r.Unreachable+r.Undetermined != r.Total {
		t.Error("the states do not account for every instance, so the headline would be dishonest")
	}
	if r.KnownFrom != 2 {
		t.Errorf("knownFrom = %d, want 2", r.KnownFrom)
	}
	if r.Complete() {
		t.Error("the rollup claims to be complete when two instances were not read")
	}
	if r.ChannelsRunning != 9 {
		t.Errorf("channelsRunning = %d, want 9", r.ChannelsRunning)
	}
}

func TestAStatusGoesStale(t *testing.T) {
	now := time.Now()
	fresh := Status{CheckedAt: now.Add(-5 * time.Second)}
	old := Status{CheckedAt: now.Add(-10 * time.Minute)}
	never := Status{}

	if fresh.Stale(now, time.Minute) {
		t.Error("a five-second-old reading is stale")
	}
	if !old.Stale(now, time.Minute) {
		t.Error("a ten-minute-old reading is not stale")
	}
	if !never.Stale(now, time.Minute) {
		t.Error("a reading that never happened is not stale")
	}
}

// A fleet built from a zero configuration must be usable, not merely constructible.
//
// It was neither for a while, and the failure was hidden: Start returned immediately when there were no peers, so
// the zero poll interval never reached NewTicker. The moment peers could be added at run time - which meant the
// loop had to run even when empty - it panicked at startup on every server that had no peers file, which is most
// of them.
func TestAFleetWithNoPeersRunsWithoutPanicking(t *testing.T) {
	f := New(Config{})

	if f.cfg.PollEvery <= 0 {
		t.Fatalf("poll interval is %s; a ticker cannot be built from that", f.cfg.PollEvery)
	}
	if f.cfg.Timeout <= 0 {
		t.Fatalf("timeout is %s", f.cfg.Timeout)
	}

	// The real check: this used to panic in a goroutine, which takes the whole process with it.
	f.Start()
	f.Stop()
}

// A peer added after the fleet started must be polled, which is the entire point of making the list mutable.
func TestPeersCanBeAddedAndRemovedWhileRunning(t *testing.T) {
	f := New(Config{})
	f.Start()
	defer f.Stop()

	f.SetPeers([]Peer{{Name: "site-b", URL: "https://b.example"}})
	if got := len(f.Peers()); got != 1 {
		t.Fatalf("after adding, the fleet has %d peer(s), want 1", got)
	}

	f.SetPeers([]Peer{})
	if got := len(f.Peers()); got != 0 {
		t.Errorf("after removing, the fleet has %d peer(s), want 0", got)
	}

	// A removed peer must not linger in the statuses. Leaving it there shows a deliberately removed instance as
	// one that has stopped responding, and somebody goes looking for a server that is fine.
	for _, st := range f.Statuses() {
		if st.Name == "site-b" {
			t.Error("a removed peer is still reported in the fleet's statuses")
		}
	}
}
