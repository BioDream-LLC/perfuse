package cluster

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	neturl "net/url"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

func testURL(t *testing.T) string {
	t.Helper()
	url := sqlitedb.TestURL("cluster")
	if url == "" {
		t.Skip("set PERFUSE_TEST_POSTGRES to a disposable PostgreSQL database: a cluster needs one")
	}
	return url
}

func openURL(t *testing.T, url string) *sql.DB {
	t.Helper()
	d, err := sqlitedb.Open(url, sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.Write
}

// proxy stands between a server and PostgreSQL, so a test can cut the server off from the store while it runs on.
type proxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	cut   bool
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.cut {
				p.mu.Unlock()
				_ = c.Close()
				continue
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				p.mu.Unlock()
				_ = c.Close()
				continue
			}
			p.conns = append(p.conns, c, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); p.sever() })
	return p
}

// sever cuts every connection and refuses new ones.
func (p *proxy) sever() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cut = true
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

// via is url with its host replaced by the proxy's.
func (p *proxy) via(t *testing.T, raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = p.ln.Addr().String()
	return u.String()
}

// node is one server in a test: what it runs, as its hooks report.
type node struct {
	c       *Coordinator
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	running map[string]bool
	log     *[]string
	logMu   *sync.Mutex
}

func (n *node) runs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for k, v := range n.running {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

type world struct {
	t        *testing.T
	url      string
	db       *sql.DB
	mu       sync.Mutex
	owners   map[string]string // lease -> node currently running it
	overlaps []string
	channels []string
}

func newWorld(t *testing.T) *world {
	url := testURL(t)
	return &world{t: t, url: url, db: openURL(t, url), owners: map[string]string{}}
}

func (w *world) start(id string, mode Mode, ttl time.Duration) *node {
	w.t.Helper()
	return w.startOn(w.db, id, mode, ttl)
}

func (w *world) startOn(db *sql.DB, id string, mode Mode, ttl time.Duration) *node {
	w.t.Helper()
	n := &node{running: map[string]bool{}, done: make(chan struct{})}
	hooks := Hooks{
		Gain: func(lease string) {
			w.mu.Lock()
			if other, ok := w.owners[lease]; ok && other != id {
				var detail string
				rows, err := w.db.Query(`SELECT to_char(at, 'HH24:MI:SS.MS'), node, kind, detail FROM cluster_events WHERE lease = $1 ORDER BY id`, lease)
				if err == nil {
					for rows.Next() {
						var at, nd, k, d string
						_ = rows.Scan(&at, &nd, &k, &d)
						detail += fmt.Sprintf("\n   %s %s %s %s", at, nd, k, d)
					}
					rows.Close()
				}
				var row string
				_ = w.db.QueryRow(`SELECT holder || ' epoch ' || epoch || ' expires ' || to_char(expires_at, 'HH24:MI:SS.MS') || ' now ' || to_char(clock_timestamp(), 'HH24:MI:SS.MS') || ' wanted ' || wanted_by FROM cluster_leases WHERE name = $1`, lease).Scan(&row)
				w.overlaps = append(w.overlaps, fmt.Sprintf("%s gained %s while %s still ran it (at %s)\n  row: %s%s", id, lease, other,
					time.Now().UTC().Format("15:04:05.000"), row, detail))
			}
			w.owners[lease] = id
			w.mu.Unlock()
			n.mu.Lock()
			n.running[lease] = true
			n.mu.Unlock()
		},
		Lose: func(lease string) {
			w.t.Logf("%s lost %s at %s", id, lease, time.Now().Format("15:04:05.000"))
			w.mu.Lock()
			if w.owners[lease] == id {
				delete(w.owners, lease)
			}
			w.mu.Unlock()
			n.mu.Lock()
			n.running[lease] = false
			n.mu.Unlock()
		},
		Channels: func() []string { return w.channels },
	}
	c, err := New(context.Background(), db, Config{Mode: mode, NodeID: id, TTL: ttl, Renew: ttl / 5}, hooks,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.c, n.cancel = c, cancel
	go func() { c.Run(ctx); close(n.done) }()
	w.t.Cleanup(n.stop)
	return n
}

// stop shuts a node down cleanly: it gives its leases up.
func (n *node) stop() {
	n.cancel()
	<-n.done
}

// crash stops a node's loop without letting it give anything up or run its hooks, as a killed process does. What it
// was running is forgotten by the world as soon as it would really have stopped: at once, as a killed process stops.
func (w *world) crash(n *node, id string) {
	n.c.mu.Lock()
	n.c.vanish = true
	n.c.mu.Unlock()
	n.cancel()
	<-n.done
	w.mu.Lock()
	for lease, owner := range w.owners {
		if owner == id {
			delete(w.owners, lease)
		}
	}
	w.mu.Unlock()
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("after %s: %s", within, what)
}

func TestActivePassiveFailsOverAndNeverRunsTwice(t *testing.T) {
	w := newWorld(t)
	ttl := 2 * time.Second
	a := w.start("a", ActivePassive, ttl)
	eventually(t, 3*time.Second, "a becomes leader", a.c.IsLeader)
	b := w.start("b", ActivePassive, ttl)
	time.Sleep(ttl)
	if b.c.IsLeader() || !a.c.IsLeader() {
		t.Fatal("the second server took the lead from a healthy first")
	}

	// a crashes: b takes over once a's lease lapses, not before.
	crashed := time.Now()
	w.crash(a, "a")
	eventually(t, 3*ttl, "b takes over after a crash", b.c.IsLeader)
	if took := time.Since(crashed); took < ttl-ttl/5 {
		t.Errorf("b took over %s after a crash, before a's lease could have lapsed", took)
	}

	// a clean shutdown hands over at once.
	c := w.start("c", ActivePassive, ttl)
	time.Sleep(ttl / 2)
	stopped := time.Now()
	b.stop()
	eventually(t, 2*ttl, "c takes over after b shuts down", c.c.IsLeader)
	if took := time.Since(stopped); took > ttl {
		t.Errorf("a clean shutdown took %s to hand over; it should not wait for the lease", took)
	}

	// Stepping down hands the lead to another live server and keeps away from it.
	d := w.start("d", ActivePassive, ttl)
	time.Sleep(ttl / 2)
	if err := c.c.StepDown(context.Background(), 0, "test"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*ttl, "d leads after c steps down", d.c.IsLeader)
	if c.c.IsLeader() {
		t.Error("c leads again straight after stepping down")
	}
	if len(w.overlaps) > 0 {
		t.Fatalf("two servers ran the same work: %v", w.overlaps)
	}
	st, err := d.c.Status(context.Background())
	if err != nil || st.Leader != "d" || len(st.Events) == 0 {
		t.Fatalf("status %+v %v", st, err)
	}
}

// A server cut off from the store stops its work before the lease lapses, so the one that takes over never overlaps it.
func TestACutOffLeaderStopsBeforeAnotherStarts(t *testing.T) {
	w := newWorld(t)
	ttl := 2 * time.Second
	u, _ := neturl.Parse(w.url)
	px := newProxy(t, u.Host)
	a := w.startOn(openURL(t, px.via(t, w.url)), "a", ActivePassive, ttl)
	eventually(t, 3*time.Second, "a leads", a.c.IsLeader)
	b := w.start("b", ActivePassive, ttl)
	time.Sleep(ttl / 2)

	// Cut a off: its connections to the store drop, but its process and watchdog run on.
	t.Logf("cut at %s", time.Now().Format("15:04:05.000"))
	px.sever()
	eventually(t, 3*ttl, "b takes over", b.c.IsLeader)
	eventually(t, ttl, "a has stopped", func() bool { return len(a.runs()) == 0 })
	if len(w.overlaps) > 0 {
		t.Fatalf("overlap: %v", w.overlaps)
	}
}

func TestActiveActiveSpreadsChannelsAndMovesThem(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 6; i++ {
		w.channels = append(w.channels, ChannelLease("main", fmt.Sprintf("ch%d", i)))
	}
	ttl := 2 * time.Second
	a := w.start("a", ActiveActive, ttl)
	b := w.start("b", ActiveActive, ttl)
	c := w.start("c", ActiveActive, ttl)
	count := func(n *node) int {
		k := 0
		for _, l := range n.runs() {
			if l != LeaderLease {
				k++
			}
		}
		return k
	}
	eventually(t, 10*ttl, "six channels spread two each", func() bool { return count(a) == 2 && count(b) == 2 && count(c) == 2 })

	// Move one of a's channels to b.
	var moving string
	for _, l := range a.runs() {
		if l != LeaderLease {
			moving = l
			break
		}
	}
	if err := a.c.Move(context.Background(), moving, "b", "test"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 4*ttl, "the channel moves to b", func() bool { return b.c.Holds(moving) && !a.c.Holds(moving) })

	// c crashes: its two channels go to the others.
	w.crash(c, "c")
	eventually(t, 10*ttl, "c's channels are taken over", func() bool { return count(a)+count(b) == 6 })
	if len(w.overlaps) > 0 {
		t.Fatalf("overlap: %v", w.overlaps)
	}
	// Exactly one leader throughout.
	if a.c.IsLeader() == b.c.IsLeader() {
		t.Errorf("leaders: a %v, b %v", a.c.IsLeader(), b.c.IsLeader())
	}
}

func TestDesiredStateIsShared(t *testing.T) {
	w := newWorld(t)
	a := w.start("a", ActivePassive, 2*time.Second)
	b := w.start("b", ActivePassive, 2*time.Second)
	if err := b.c.SetDesired(context.Background(), "", "adt", false, "nurse"); err != nil {
		t.Fatal(err)
	}
	got, err := a.c.Desired(context.Background())
	if err != nil || got["main/adt"] != false {
		t.Fatalf("%v %v", got, err)
	}
	if _, ok := got["main/adt"]; !ok {
		t.Fatal("not recorded")
	}
	_ = b.c.SetDesired(context.Background(), "", "adt", true, "nurse")
	got, _ = a.c.Desired(context.Background())
	if !got["main/adt"] {
		t.Error("start not recorded")
	}
}

// A server that joins late gets work even when the others are already at their rounded-up share: four channels on three
// servers settle as 2, 1, 1. Found on EC2, where a server back from a crash sat idle beside two holding two each.
func TestALateServerGetsAShare(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 4; i++ {
		w.channels = append(w.channels, ChannelLease("main", fmt.Sprintf("ch%d", i)))
	}
	ttl := 2 * time.Second
	a := w.start("a", ActiveActive, ttl)
	b := w.start("b", ActiveActive, ttl)
	count := func(n *node) int {
		k := 0
		for _, l := range n.runs() {
			if l != LeaderLease {
				k++
			}
		}
		return k
	}
	eventually(t, 10*ttl, "four channels spread two each", func() bool { return count(a) == 2 && count(b) == 2 })
	c := w.start("c", ActiveActive, ttl)
	eventually(t, 15*ttl, "the late server takes a share", func() bool {
		return count(c) >= 1 && count(a)+count(b)+count(c) == 4 && max(count(a), count(b), count(c))-min(count(a), count(b), count(c)) <= 1
	})
	time.Sleep(3 * ttl)
	if count(a)+count(b)+count(c) != 4 || count(c) == 0 || max(count(a), count(b), count(c))-min(count(a), count(b), count(c)) > 1 {
		t.Errorf("not settled: a %d, b %d, c %d", count(a), count(b), count(c))
	}
	if len(w.overlaps) > 0 {
		t.Fatalf("overlap: %v", w.overlaps)
	}
}
