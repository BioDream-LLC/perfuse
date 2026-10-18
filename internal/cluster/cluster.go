// Package cluster lets several Perfuse servers share one PostgreSQL store and decide, between them, which runs what.
//
// # The problem it solves
//
// One server is one point of failure: when it stops, every interface stops. Two servers on one database solve that only if
// they never both run the same channel, because two MLLP listeners on one feed take turns with the sender, two queue
// workers deliver the same message twice, and two DICOM pollers each report the same study.
//
// # How
//
// A lease is a row naming who holds something, with a counter its holder moves on at every renewal, every few seconds. No
// clock is compared with another: a holder that cannot renew (crashed, cut off from the database) stops what the lease
// covers once its own clock says the lease is nearly done, and another server takes it only after its own clock has
// watched the counter stand still for the whole lease. Each measures an interval on its own clock, so the servers' clocks,
// and the database's, need not agree; they need only run at about the same rate. A clock that jumps (a virtual machine
// resynchronising, an administrator setting the time) changes nothing: the first version used the database's clock for
// expiry, and a test found a database whose clock jumped thirteen seconds hand every lease to a new server while the old
// ones still ran them.
//
// Between the holder stopping and the next one starting there is a short gap with nobody running, which costs a sender a
// few seconds of refused connections, and never a moment with two.
//
// Two modes:
//
//   - active-passive: one lease, "leader". Its holder runs every channel and every job that must run once (subscription
//     notifications, alerts). The others wait, and the first to see it lapse takes over.
//   - active-active: the leader still runs the jobs, and every channel has a lease of its own, spread evenly over the live
//     servers. A server that leaves gives its channels to the rest; one that joins takes its share as they come free.
//
// Each lease carries an epoch, counted up whenever its holder changes, which names a holding in the history.
package cluster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mode is how the servers divide the work.
type Mode string

const (
	// ActivePassive runs everything on the leader.
	ActivePassive Mode = "active-passive"
	// ActiveActive spreads the channels over every live server.
	ActiveActive Mode = "active-active"
)

// LeaderLease is the lease whose holder runs what must run once.
const LeaderLease = "leader"

// ChannelLease is the lease name of one channel.
func ChannelLease(tenant, channel string) string {
	if tenant == "" {
		tenant = "main"
	}
	return "channel:" + tenant + "/" + channel
}

// ParseChannelLease reads a channel lease name back.
func ParseChannelLease(lease string) (tenant, channel string, ok bool) {
	rest, ok := strings.CutPrefix(lease, "channel:")
	if !ok {
		return "", "", false
	}
	tenant, channel, ok = strings.Cut(rest, "/")
	return tenant, channel, ok
}

// Config describes this server in the cluster.
type Config struct {
	Mode Mode
	// NodeID is stable for a server: it is kept in its data directory (NodeID reads or makes it).
	NodeID string
	// Name is what people see. Default: the node id.
	Name string
	// Address is the URL of this server's console and API, as the others and a load balancer reach it.
	Address string
	// Version is the build, shown beside each node: a rolling upgrade has two for a while.
	Version string
	// TTL is how long a lease lasts without renewal: how long a crashed holder blocks its work. Default 15s.
	TTL time.Duration
	// Renew is how often leases are renewed and taken. Default TTL/5.
	Renew time.Duration
}

func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = 15 * time.Second
	}
	if c.Renew <= 0 {
		c.Renew = c.TTL / 5
	}
	if c.Name == "" {
		c.Name = c.NodeID
	}
	if c.Mode == "" {
		c.Mode = ActivePassive
	}
	return c
}

// margin is how long before a lease's database expiry its holder treats it as gone. It covers the time a renewal spent
// in flight and any difference in clock rates: the holder must stop before anybody else can start.
func (c Config) margin() time.Duration { return max(time.Second, c.TTL/3) }

// NodeID reads this server's id from dir, or makes one and writes it there.
func NodeID(dir string) (string, error) {
	path := filepath.Join(dir, "cluster-node-id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := "node-" + hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("cluster: keeping the node id in %s: %w", path, err)
	}
	return id, nil
}

// Hooks are what the coordinator calls when this server gains or loses a lease. They run on the coordinator's goroutine,
// one at a time; Lose must return only once the work has stopped.
type Hooks struct {
	Gain func(lease string)
	Lose func(lease string)
	// Channels lists the channel leases there should be (active-active), from the channels configured.
	Channels func() []string
}

// Coordinator holds and renews this server's leases.
type Coordinator struct {
	db    *sql.DB
	cfg   Config
	log   *slog.Logger
	hooks Hooks

	mu       sync.Mutex
	held     map[string]holding
	cooldown time.Time // no leadership before this, after stepping down
	lastErr  error
	started  time.Time
	// vanish ends Run without giving leases up or stopping work, as a killed process ends: for tests.
	vanish bool

	// seen is when this server first saw each lease at its current counter, by its own monotonic clock: a lease whose
	// counter has stood still for a whole lease has been given up by a holder that crashed or was cut off.
	seen map[string]observation

	// leaseMu serialises the hooks of one lease, so its Gain and Lose never interleave, while the watchdog can stop one
	// lease's work even as the renewal loop is busy starting another's.
	leaseMu sync.Map // lease -> *sync.Mutex
}

func (c *Coordinator) lock(lease string) func() {
	m, _ := c.leaseMu.LoadOrStore(lease, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type observation struct {
	holder string
	seq    int64
	since  time.Time
}

type holding struct {
	epoch    int64
	deadline time.Time // local: after this the work stops, whatever the database says
}

// ErrNeedsPostgres is a cluster asked for on a SQLite store, which only one server can open.
var ErrNeedsPostgres = errors.New("cluster: servers can share only a PostgreSQL store; give -db a postgres:// URL")

// New prepares the cluster tables and a coordinator for this server.
func New(ctx context.Context, db *sql.DB, cfg Config, hooks Hooks, log *slog.Logger) (*Coordinator, error) {
	cfg = cfg.withDefaults()
	if cfg.NodeID == "" {
		return nil, errors.New("cluster: a node id is required")
	}
	if cfg.Mode != ActivePassive && cfg.Mode != ActiveActive {
		return nil, fmt.Errorf("cluster: mode %q is neither %s nor %s", cfg.Mode, ActivePassive, ActiveActive)
	}
	if cfg.Renew*3 > cfg.TTL {
		return nil, fmt.Errorf("cluster: leases renewed every %s cannot last %s; renew at least three times per lease", cfg.Renew, cfg.TTL)
	}
	if log == nil {
		log = slog.Default()
	}
	if err := prepare(ctx, db); err != nil {
		return nil, fmt.Errorf("cluster: preparing its tables: %w", err)
	}
	return &Coordinator{db: db, cfg: cfg, log: log, hooks: hooks, held: map[string]holding{}, seen: map[string]observation{},
		started: time.Now()}, nil
}

// prepare creates the tables, on one connection under an advisory lock: servers starting together take turns.
func prepare(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7212002)`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7212002)`) }()
	for _, q := range schema {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS cluster_nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, address TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL DEFAULT '', mode TEXT NOT NULL DEFAULT '', started_at TIMESTAMPTZ NOT NULL,
		seen_at TIMESTAMPTZ NOT NULL, leaving INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS cluster_leases (name TEXT PRIMARY KEY, holder TEXT NOT NULL, epoch BIGINT NOT NULL,
		expires_at TIMESTAMPTZ NOT NULL, acquired_at TIMESTAMPTZ NOT NULL, wanted_by TEXT NOT NULL DEFAULT '',
		seq BIGINT NOT NULL DEFAULT 0)`,
	`ALTER TABLE cluster_leases ADD COLUMN IF NOT EXISTS seq BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE cluster_nodes ADD COLUMN IF NOT EXISTS beat BIGINT NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS cluster_events (id BIGSERIAL PRIMARY KEY, at TIMESTAMPTZ NOT NULL, node TEXT NOT NULL,
		kind TEXT NOT NULL, lease TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '')`,
	`CREATE INDEX IF NOT EXISTS cluster_events_at ON cluster_events (at DESC)`,
	`CREATE TABLE IF NOT EXISTS cluster_desired (tenant TEXT NOT NULL, channel TEXT NOT NULL, running INTEGER NOT NULL,
		by_user TEXT NOT NULL DEFAULT '', at TIMESTAMPTZ NOT NULL, PRIMARY KEY (tenant, channel))`,
}

// Config is this server's configuration, defaults filled in.
func (c *Coordinator) Config() Config { return c.cfg }

// Run renews and takes leases until ctx ends, then gives up what it holds so another server takes over at once.
func (c *Coordinator) Run(ctx context.Context) {
	tick := time.NewTicker(c.cfg.Renew)
	defer tick.Stop()
	// The watchdog stops work when a lease runs out locally. It has a goroutine of its own: the renewal loop can be held
	// up (a slow statement, a channel taking its time to start), and a watchdog waiting behind it would let a lease lapse
	// in the store, and another server start, while this one still ran the work.
	wctx, stopWatch := context.WithCancel(context.Background())
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watch := time.NewTicker(min(200*time.Millisecond, c.cfg.Renew/4))
		defer watch.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-watch.C:
				c.expire(time.Now())
			}
		}
	}()
	defer func() { stopWatch(); <-watchDone }()
	c.round(ctx)
	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			vanish := c.vanish
			c.mu.Unlock()
			if !vanish {
				c.leave()
			}
			return
		case <-tick.C:
			c.round(ctx)
		}
	}
}

// expire drops every lease whose local deadline has passed, stopping its work.
func (c *Coordinator) expire(now time.Time) {
	c.mu.Lock()
	var gone []string
	for name, h := range c.held {
		if now.After(h.deadline) {
			gone = append(gone, name)
			delete(c.held, name)
		}
	}
	c.mu.Unlock()
	sort.Strings(gone)
	for _, name := range gone {
		c.log.Error("cluster: a lease ran out before it could be renewed; its work is stopped here", "lease", name)
		unlock := c.lock(name)
		c.lose(name, "expired")
		unlock()
	}
}

// lose stops a lease's work. The caller holds the lease's lock.
func (c *Coordinator) lose(name, why string) {
	if c.hooks.Lose != nil {
		c.hooks.Lose(name)
	}
	c.event("lost", name, why)
}

func (c *Coordinator) gain(name string, epoch int64) {
	c.event("gained", name, fmt.Sprintf("epoch %d", epoch))
	if c.hooks.Gain != nil {
		c.hooks.Gain(name)
	}
}

// event records what happened, without making the caller wait for the store.
func (c *Coordinator) event(kind, lease, detail string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), c.cfg.TTL)
		defer cancel()
		_, _ = c.db.ExecContext(ctx, `INSERT INTO cluster_events (at, node, kind, lease, detail) VALUES (now(), ?, ?, ?, ?)`,
			c.cfg.NodeID, kind, lease, detail)
	}()
}

// round is one renewal: heartbeat, the leader lease, and in active-active the channel leases.
func (c *Coordinator) round(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Renew)
	defer cancel()
	sent := time.Now()
	err := c.heartbeat(ctx)
	if err == nil {
		err = c.leader(ctx, sent)
	}
	if err == nil && c.cfg.Mode == ActiveActive {
		err = c.channels(ctx, sent)
	}
	c.mu.Lock()
	changed := (err == nil) != (c.lastErr == nil)
	c.lastErr = err
	c.mu.Unlock()
	if err != nil && changed {
		c.log.Error("cluster: the shared store cannot be reached; this server keeps its work only until its leases run out", "error", err)
	} else if err == nil && changed {
		c.log.Info("cluster: the shared store answers again")
	}
}

func (c *Coordinator) heartbeat(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO cluster_nodes (id, name, address, version, mode, started_at, seen_at, leaving, beat)
		VALUES (?, ?, ?, ?, ?, ?, now(), 0, 1)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, address = excluded.address, version = excluded.version,
			mode = excluded.mode, started_at = excluded.started_at, seen_at = now(), leaving = 0,
			beat = cluster_nodes.beat + 1`,
		c.cfg.NodeID, c.cfg.Name, c.cfg.Address, c.cfg.Version, string(c.cfg.Mode), c.started)
	return err
}

// take acquires or renews one lease, and reports whether this server holds it and with which epoch. A renewal moves the
// counter on. Another server's lease is taken only when it was given up (no holder), or when this server has watched its
// counter stand still for a whole lease; the counter is compared in the same statement, so of two servers trying at once
// one wins.
func (c *Coordinator) take(ctx context.Context, name string) (bool, int64, error) {
	var holder string
	var seq, epoch int64
	err := c.db.QueryRowContext(ctx, `SELECT holder, seq, epoch FROM cluster_leases WHERE name = ?`, name).Scan(&holder, &seq, &epoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := c.db.ExecContext(ctx, `INSERT INTO cluster_leases (name, holder, epoch, expires_at, acquired_at, wanted_by, seq)
			VALUES (?, ?, 1, now() + make_interval(secs => ?), now(), '', 1) ON CONFLICT (name) DO NOTHING`,
			name, c.cfg.NodeID, c.cfg.TTL.Seconds())
		if err != nil {
			return false, 0, err
		}
		n, _ := res.RowsAffected()
		return n == 1, 1, nil
	case err != nil:
		return false, 0, err
	}
	if holder == c.cfg.NodeID {
		err := c.db.QueryRowContext(ctx, `UPDATE cluster_leases SET seq = seq + 1, expires_at = now() + make_interval(secs => ?)
			WHERE name = ? AND holder = ? RETURNING epoch`, c.cfg.TTL.Seconds(), name, c.cfg.NodeID).Scan(&epoch)
		if errors.Is(err, sql.ErrNoRows) {
			return false, 0, nil
		}
		return err == nil, epoch, err
	}
	if holder != "" && !c.stale(name, holder, seq) {
		return false, 0, nil
	}
	err = c.db.QueryRowContext(ctx, `UPDATE cluster_leases SET holder = ?, epoch = epoch + 1, seq = seq + 1,
			expires_at = now() + make_interval(secs => ?), acquired_at = now(),
			wanted_by = CASE WHEN wanted_by = ? THEN wanted_by ELSE '' END
		WHERE name = ? AND holder = ? AND seq = ? RETURNING epoch`,
		c.cfg.NodeID, c.cfg.TTL.Seconds(), c.cfg.NodeID, name, holder, seq).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	return err == nil, epoch, err
}

// stale reports whether another server's lease has stood still for a whole lease, as this server has watched it.
func (c *Coordinator) stale(name, holder string, seq int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.seen[name]
	if !ok || o.holder != holder || o.seq != seq {
		c.seen[name] = observation{holder: holder, seq: seq, since: time.Now()}
		return false
	}
	return time.Since(o.since) >= c.cfg.TTL
}

// free reports whether this server could take a lease now: nobody holds it, or its counter has stood still for a lease.
func (c *Coordinator) free(name, holder string, seq int64) bool {
	if holder == "" {
		return true
	}
	return c.stale(name, holder, seq)
}

// keep records a renewed or newly taken lease, and calls Gain for a new one. A lease whose local deadline has already
// passed (the round took that long) is not started: by the time its work began, another server could be entitled to it.
func (c *Coordinator) keep(name string, epoch int64, sent time.Time) {
	deadline := sent.Add(c.cfg.TTL - c.cfg.margin())
	unlock := c.lock(name)
	defer unlock()
	if !time.Now().Before(deadline) {
		return
	}
	c.mu.Lock()
	h, had := c.held[name]
	c.held[name] = holding{epoch: epoch, deadline: deadline}
	c.mu.Unlock()
	if !had || h.epoch != epoch {
		c.gain(name, epoch)
		// Starting may have taken a while; if the lease ran out meanwhile, the watchdog finds it and stops it again.
	}
}

// drop gives a lease up: its work is stopped first, then the row is expired so the next server need not wait.
func (c *Coordinator) drop(ctx context.Context, name, why string) {
	unlock := c.lock(name)
	c.mu.Lock()
	_, had := c.held[name]
	delete(c.held, name)
	c.mu.Unlock()
	if had {
		c.lose(name, why)
	}
	unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.Renew)
	defer cancel()
	_, _ = c.db.ExecContext(ctx, `UPDATE cluster_leases SET holder = '', seq = seq + 1, expires_at = now()
		WHERE name = ? AND holder = ?`, name, c.cfg.NodeID)
}

func (c *Coordinator) leader(ctx context.Context, sent time.Time) error {
	c.mu.Lock()
	cooling := time.Now().Before(c.cooldown)
	_, holding := c.held[LeaderLease]
	c.mu.Unlock()
	if cooling && !holding {
		return nil
	}
	ok, epoch, err := c.take(ctx, LeaderLease)
	if err != nil {
		return err
	}
	if ok {
		c.keep(LeaderLease, epoch, sent)
	} else if holding {
		// The database says somebody else holds it: this server was cut off long enough for the lease to lapse.
		c.drop(ctx, LeaderLease, "held by another server")
	}
	return nil
}

// channels spreads the channel leases: each server holds about its share, keeps what it has, takes what is free while it
// is under its share, and hands over a channel another server has asked for.
func (c *Coordinator) channels(ctx context.Context, sent time.Time) error {
	if c.hooks.Channels == nil {
		return nil
	}
	want := map[string]bool{}
	for _, l := range c.hooks.Channels() {
		want[l] = true
	}
	liveIDs, err := c.liveNodes(ctx)
	if err != nil {
		return err
	}
	live := max(len(liveIDs), 1)
	share := int(math.Ceil(float64(len(want)) / float64(live)))

	type row struct {
		holder, wanted string
		seq            int64
	}
	rows, err := c.db.QueryContext(ctx, `SELECT name, holder, wanted_by, seq FROM cluster_leases WHERE name LIKE 'channel:%'`)
	if err != nil {
		return err
	}
	leases := map[string]row{}
	for rows.Next() {
		var name string
		var r row
		if err := rows.Scan(&name, &r.holder, &r.wanted, &r.seq); err != nil {
			rows.Close()
			return err
		}
		leases[name] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	mine := 0
	for name := range c.held {
		if strings.HasPrefix(name, "channel:") {
			mine++
		}
	}
	c.mu.Unlock()

	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	// Held ones first: renew them, or hand over the ones asked for and the ones no longer configured.
	for name := range c.heldChannels() {
		r := leases[name]
		switch {
		case !want[name]:
			c.drop(ctx, name, "no longer configured")
			mine--
		case r.wanted != "" && r.wanted != c.cfg.NodeID:
			c.drop(ctx, name, "handed to "+r.wanted)
			mine--
		default:
			ok, epoch, err := c.take(ctx, name)
			if err != nil {
				return err
			}
			if ok {
				c.keep(name, epoch, sent)
			} else {
				c.drop(ctx, name, "held by another server")
				mine--
			}
		}
	}
	held := c.heldChannels()
	for _, name := range names {
		if held[name] {
			continue
		}
		r, exists := leases[name]
		askedForMe := exists && r.wanted == c.cfg.NodeID
		askedForOther := exists && r.wanted != "" && r.wanted != c.cfg.NodeID && liveIDs[r.wanted]
		free := !exists || c.free(name, r.holder, r.seq)
		if !askedForMe && (askedForOther || !free || mine >= share) {
			continue
		}
		ok, epoch, err := c.take(ctx, name)
		if err != nil {
			return err
		}
		if ok {
			c.keep(name, epoch, sent)
			mine++
		}
	}
	// Over its share with others under theirs: give one back per round, so a server that joins gets work without a storm.
	// A channel a person moved here stays: the move is a preference that lasts until it is moved again or this server goes.
	if mine > share {
		held := c.heldChannels()
		spare := make([]string, 0, len(held))
		for name := range held {
			if leases[name].wanted != c.cfg.NodeID {
				spare = append(spare, name)
			}
		}
		sort.Strings(spare)
		if len(spare) > 0 {
			c.drop(ctx, spare[len(spare)-1], "rebalancing")
		}
	}
	return nil
}

// liveNodes reads the servers and says which are live: this one, and any whose heartbeat counter this server has seen move
// within a lease, by its own clock. Like the leases, no clock is compared with another.
func (c *Coordinator) liveNodes(ctx context.Context) (map[string]bool, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, beat, leaving FROM cluster_nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := map[string]bool{}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for rows.Next() {
		var id string
		var beat, leaving int64
		if err := rows.Scan(&id, &beat, &leaving); err != nil {
			return nil, err
		}
		key := "node:" + id
		o, seen := c.seen[key]
		if !seen || o.seq != beat {
			o = observation{holder: id, seq: beat, since: now}
			c.seen[key] = o
		}
		if leaving == 0 && (id == c.cfg.NodeID || now.Sub(o.since) < c.cfg.TTL) {
			live[id] = true
		}
	}
	return live, rows.Err()
}

func (c *Coordinator) heldChannels() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]bool{}
	for name := range c.held {
		if strings.HasPrefix(name, "channel:") {
			out[name] = true
		}
	}
	return out
}

// leave gives every lease up, for a server shutting down: the next one starts at once rather than after the lease lapses.
func (c *Coordinator) leave() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.mu.Lock()
	names := make([]string, 0, len(c.held))
	for n := range c.held {
		names = append(names, n)
	}
	c.mu.Unlock()
	sort.Strings(names)
	for _, n := range names {
		c.drop(ctx, n, "shutting down")
	}
	_, _ = c.db.ExecContext(ctx, `UPDATE cluster_nodes SET leaving = 1 WHERE id = ?`, c.cfg.NodeID)
}

// Holds reports whether this server holds a lease now, by its own clock.
func (c *Coordinator) Holds(lease string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.held[lease]
	return ok && time.Now().Before(h.deadline)
}

// IsLeader reports whether this server is the leader now.
func (c *Coordinator) IsLeader() bool { return c.Holds(LeaderLease) }

// Runs reports whether this server is the one to run a channel now.
func (c *Coordinator) Runs(tenant, channel string) bool {
	if c == nil {
		return true
	}
	if c.cfg.Mode == ActivePassive {
		return c.IsLeader()
	}
	return c.Holds(ChannelLease(tenant, channel))
}

// Healthy reports whether the last renewal reached the store.
func (c *Coordinator) Healthy() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// StepDown gives up leadership and keeps away from it for a while, so another server takes over: for maintenance on this
// one without stopping it.
func (c *Coordinator) StepDown(ctx context.Context, for_ time.Duration, by string) error {
	c.mu.Lock()
	c.cooldown = time.Now().Add(max(for_, 2*c.cfg.TTL))
	c.mu.Unlock()
	c.drop(ctx, LeaderLease, "stepped down by "+by)
	return nil
}

// Move asks for a channel to run on another server. Its holder stops it and lets it go at its next renewal, and the
// server asked for takes it.
func (c *Coordinator) Move(ctx context.Context, lease, toNode, by string) error {
	if c.cfg.Mode != ActiveActive {
		return errors.New("cluster: channels move between servers only in active-active mode; in active-passive the leader runs them all")
	}
	live, err := c.liveNodes(ctx)
	if err != nil {
		return err
	}
	if !live[toNode] {
		return fmt.Errorf("cluster: %s is not a live server of this cluster", toNode)
	}
	name := lease
	res, err := c.db.ExecContext(ctx, `UPDATE cluster_leases SET wanted_by = ? WHERE name = ?`, toNode, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := c.db.ExecContext(ctx, `INSERT INTO cluster_leases (name, holder, epoch, expires_at, acquired_at, wanted_by, seq)
			VALUES (?, '', 0, now(), now(), ?, 0) ON CONFLICT (name) DO UPDATE SET wanted_by = excluded.wanted_by`,
			name, toNode); err != nil {
			return err
		}
	}
	c.event("move", name, "to "+toNode+" by "+by)
	return nil
}

// Holder names the server holding a lease now (its id and name), or "" when none does: as this server sees it, a lease
// whose counter has not moved for a whole lease has no holder.
func (c *Coordinator) Holder(ctx context.Context, lease string) (id, name string) {
	var seq int64
	if err := c.db.QueryRowContext(ctx, `SELECT l.holder, COALESCE(n.name, l.holder), l.seq FROM cluster_leases l
		LEFT JOIN cluster_nodes n ON n.id = l.holder WHERE l.name = ?`, lease).Scan(&id, &name, &seq); err != nil {
		return "", ""
	}
	if id == "" || (id != c.cfg.NodeID && c.stale(lease, id, seq)) {
		return "", ""
	}
	return id, name
}

// SetDesired records whether a channel should run, wherever it runs: a person's start or stop on any server.
func (c *Coordinator) SetDesired(ctx context.Context, tenant, channel string, running bool, by string) error {
	if tenant == "" {
		tenant = "main"
	}
	r := 0
	if running {
		r = 1
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO cluster_desired (tenant, channel, running, by_user, at) VALUES (?, ?, ?, ?, now())
		ON CONFLICT (tenant, channel) DO UPDATE SET running = excluded.running, by_user = excluded.by_user, at = excluded.at`,
		tenant, channel, r, by)
	return err
}

// Desired reads every recorded start or stop: tenant/channel to whether it should run. A channel with no record runs if
// its file enables it.
func (c *Coordinator) Desired(ctx context.Context) (map[string]bool, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT tenant, channel, running FROM cluster_desired`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t, ch string
		var r int64
		if err := rows.Scan(&t, &ch, &r); err != nil {
			return nil, err
		}
		out[t+"/"+ch] = r != 0
	}
	return out, rows.Err()
}

// Node is one server of the cluster.
type Node struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Address  string    `json:"address"`
	Version  string    `json:"version"`
	Mode     string    `json:"mode"`
	Started  time.Time `json:"startedAt"`
	Seen     time.Time `json:"seenAt"`
	Live     bool      `json:"live"`
	Leaving  bool      `json:"leaving"`
	Leader   bool      `json:"leader"`
	Channels []string  `json:"channels,omitempty"`
	This     bool      `json:"this"`
}

// Lease is one lease as the store has it.
type Lease struct {
	Name     string    `json:"name"`
	Holder   string    `json:"holder"`
	Epoch    int64     `json:"epoch"`
	Expires  time.Time `json:"expiresAt"`
	Acquired time.Time `json:"acquiredAt"`
	WantedBy string    `json:"wantedBy,omitempty"`
	Lapsed   bool      `json:"lapsed"`
}

// Event is a lease gained, lost, moved or given up.
type Event struct {
	At     time.Time `json:"at"`
	Node   string    `json:"node"`
	Kind   string    `json:"kind"`
	Lease  string    `json:"lease,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Status is the cluster as a console shows it.
type Status struct {
	Mode   Mode    `json:"mode"`
	This   string  `json:"this"`
	Leader string  `json:"leader,omitempty"`
	TTL    float64 `json:"leaseSeconds"`
	Nodes  []Node  `json:"nodes"`
	Leases []Lease `json:"leases"`
	Events []Event `json:"events"`
	Error  string  `json:"error,omitempty"`
	// Members lists, per channel lease, the channels it covers when more than one: channels that route to one another
	// run on one server and move together.
	Members map[string][]string `json:"members,omitempty"`
}

// Status reads the whole cluster.
func (c *Coordinator) Status(ctx context.Context) (Status, error) {
	st := Status{Mode: c.cfg.Mode, This: c.cfg.NodeID, TTL: c.cfg.TTL.Seconds()}
	if err := c.Healthy(); err != nil {
		st.Error = err.Error()
	}
	rows, err := c.db.QueryContext(ctx, `SELECT name, holder, epoch, expires_at, acquired_at, wanted_by, seq
		FROM cluster_leases ORDER BY name`)
	if err != nil {
		return st, err
	}
	byHolder := map[string][]string{}
	for rows.Next() {
		var l Lease
		var seq int64
		if err := rows.Scan(&l.Name, &l.Holder, &l.Epoch, &l.Expires, &l.Acquired, &l.WantedBy, &seq); err != nil {
			rows.Close()
			return st, err
		}
		l.Lapsed = l.Holder == "" || (l.Holder != c.cfg.NodeID && c.stale(l.Name, l.Holder, seq))
		if l.Name == LeaderLease && !l.Lapsed {
			st.Leader = l.Holder
		}
		if !l.Lapsed && strings.HasPrefix(l.Name, "channel:") {
			byHolder[l.Holder] = append(byHolder[l.Holder], strings.TrimPrefix(l.Name, "channel:"))
		}
		st.Leases = append(st.Leases, l)
	}
	rows.Close()
	liveIDs, err := c.liveNodes(ctx)
	if err != nil {
		return st, err
	}
	rows, err = c.db.QueryContext(ctx, `SELECT id, name, address, version, mode, started_at, seen_at, leaving
		FROM cluster_nodes ORDER BY name`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var n Node
		var leaving int64
		if err := rows.Scan(&n.ID, &n.Name, &n.Address, &n.Version, &n.Mode, &n.Started, &n.Seen, &leaving); err != nil {
			rows.Close()
			return st, err
		}
		n.Leaving, n.Live = leaving != 0, liveIDs[n.ID]
		n.Leader = n.ID == st.Leader
		n.This = n.ID == c.cfg.NodeID
		n.Channels = byHolder[n.ID]
		sort.Strings(n.Channels)
		// A server gone for a day is history, not a member.
		if !n.Live && time.Since(n.Seen) > 24*time.Hour {
			continue
		}
		st.Nodes = append(st.Nodes, n)
	}
	rows.Close()
	rows, err = c.db.QueryContext(ctx, `SELECT at, node, kind, lease, detail FROM cluster_events ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.At, &e.Node, &e.Kind, &e.Lease, &e.Detail); err != nil {
			return st, err
		}
		st.Events = append(st.Events, e)
	}
	return st, rows.Err()
}

// Prune forgets events older than keep.
func (c *Coordinator) Prune(ctx context.Context, keep time.Duration) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM cluster_events WHERE at < now() - make_interval(secs => ?)`, keep.Seconds())
	return err
}
