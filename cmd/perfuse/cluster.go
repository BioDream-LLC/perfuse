package main

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/api"
	"github.com/biodream-llc/perfuse/internal/cluster"
	"github.com/biodream-llc/perfuse/internal/config"
)

// clusterRun puts this server's runtime under the cluster's leases: a channel runs here only while this server holds the
// lease covering it, and stops before the lease could pass to another server.
//
// In active-active mode, channels that route to one another share a lease, so they always run on one server: a channel
// destination hands the message to a running channel in the same process.
type clusterRun struct {
	coord *cluster.Coordinator
	rt    *api.Runtime
	repo  *api.ChannelRepo
	log   *slog.Logger

	// mu is held while channels are started or stopped, so a lease lost while a channel starts is stopped after it has
	// started rather than left running.
	mu sync.Mutex
	// gmu guards groups alone: the gate reads them from inside a start, while mu is held.
	gmu      sync.Mutex
	groups   map[string]string // channel -> lease (active-active)
	desired  map[string]bool   // tenant/channel -> whether it should run, where somebody said
	failures map[string]string // channel -> last start error, logged once
	jobs     []*leaderJob
	wake     chan struct{}
}

// leaderJob is work that must run once in the cluster (notifications, alerts): on the leader.
type leaderJob struct {
	name    string
	run     func(ctx context.Context)
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

func newClusterRun(rt *api.Runtime, repo *api.ChannelRepo, log *slog.Logger) *clusterRun {
	return &clusterRun{rt: rt, repo: repo, log: log, groups: map[string]string{}, desired: map[string]bool{},
		failures: map[string]string{}, wake: make(chan struct{}, 1)}
}

// leaderOnly registers work that runs while this server leads.
func (c *clusterRun) leaderOnly(name string, run func(ctx context.Context)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jobs = append(c.jobs, &leaderJob{name: name, run: run})
}

func (c *clusterRun) hooks() cluster.Hooks {
	return cluster.Hooks{Gain: c.gain, Lose: c.lose, Channels: c.leases}
}

// leases lists the channel leases there should be: one per group of channels that route to each other.
func (c *clusterRun) leases() []string {
	c.refreshGroups()
	c.gmu.Lock()
	defer c.gmu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, l := range c.groups {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// refreshGroups recomputes which channels travel together: a channel destination joins its channel to the target's group.
func (c *clusterRun) refreshGroups() {
	valid, _, err := c.repo.List()
	if err != nil {
		return
	}
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if p, ok := parent[x]; ok && p != x {
			r := find(p)
			parent[x] = r
			return r
		}
		parent[x] = x
		return x
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if rb < ra {
			ra, rb = rb, ra
		}
		parent[rb] = ra
	}
	for _, s := range valid {
		find(s.Name)
		cfg, err := config.LoadFile(filepath.Join(c.repo.Dir, s.File))
		if err != nil {
			continue
		}
		for _, d := range cfg.Destinations {
			if d.Type == config.DestinationChannel && d.Channel != nil && d.Channel.Name != "" {
				union(s.Name, d.Channel.Name)
			}
		}
	}
	groups := map[string]string{}
	for _, s := range valid {
		groups[s.Name] = cluster.ChannelLease("main", find(s.Name))
	}
	c.gmu.Lock()
	c.groups = groups
	c.gmu.Unlock()
}

func (c *clusterRun) leaseOf(name string) string {
	c.gmu.Lock()
	l, ok := c.groups[name]
	c.gmu.Unlock()
	if ok {
		return l
	}
	return cluster.ChannelLease("main", name)
}

// runs reports whether this server is the one to run a channel now.
func (c *clusterRun) runs(name string) bool {
	if c.coord.Config().Mode == cluster.ActivePassive {
		return c.coord.IsLeader()
	}
	return c.coord.Holds(c.leaseOf(name))
}

func (c *clusterRun) leaseFor(name string) string {
	if c.coord.Config().Mode == cluster.ActivePassive {
		return cluster.LeaderLease
	}
	return c.leaseOf(name)
}

// gate refuses to start a channel another server is to run.
func (c *clusterRun) gate(name string) error {
	if c.runs(name) {
		return nil
	}
	where := c.RunningOn(context.Background(), "", name)
	if where == "" {
		return fmt.Errorf("channel %q is not this server's to run in the cluster at the moment; the server that takes it starts it", name)
	}
	return fmt.Errorf("channel %q runs on %s in this cluster, not here", name, where)
}

// expected says whether a channel not running here counts as down: one this server is to run does, and on the leader so
// does one no server runs at all, so an alert says so once rather than from every server or from none.
func (c *clusterRun) expected(name string) bool {
	if c.runs(name) {
		return true
	}
	return c.coord.IsLeader() && c.RunningOn(context.Background(), "", name) == ""
}

func (c *clusterRun) gain(lease string) {
	if lease == cluster.LeaderLease {
		c.log.Info("cluster: this server is now the leader")
		c.startJobs()
	} else {
		c.log.Info("cluster: this server now runs", "lease", lease)
	}
	c.reconcile(context.Background())
}

// lose stops what a lease covers, and returns only once it has stopped.
func (c *clusterRun) lose(lease string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if lease == cluster.LeaderLease {
		c.log.Warn("cluster: this server is no longer the leader")
		c.stopJobsLocked()
		if c.coord.Config().Mode == cluster.ActivePassive {
			c.rt.StopAll(ctx)
			return
		}
		return
	}
	c.gmu.Lock()
	var names []string
	for name, l := range c.groups {
		if l == lease {
			names = append(names, name)
		}
	}
	c.gmu.Unlock()
	for _, name := range names {
		if c.rt.IsRunning(name) {
			if err := c.rt.Stop(ctx, name); err != nil {
				c.log.Error("cluster: a channel did not stop when its lease went", "channel", name, "error", err)
			}
		}
	}
	c.log.Info("cluster: this server no longer runs", "lease", lease)
}

func (c *clusterRun) startJobs() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, j := range c.jobs {
		if j.running {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		j.cancel, j.done, j.running = cancel, make(chan struct{}), true
		go func(j *leaderJob, done chan struct{}) {
			defer close(done)
			j.run(ctx)
		}(j, j.done)
	}
}

func (c *clusterRun) stopJobsLocked() {
	for _, j := range c.jobs {
		if !j.running {
			continue
		}
		j.cancel()
		<-j.done
		j.running = false
	}
}

// reconcile starts what this server is to run and somebody wants running, and stops everything else.
func (c *clusterRun) reconcile(ctx context.Context) {
	valid, _, err := c.repo.List()
	if err != nil {
		return
	}
	if d, err := c.coord.Desired(ctx); err == nil {
		c.mu.Lock()
		c.desired = d
		c.mu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	configured := map[string]bool{}
	for _, s := range valid {
		configured[s.Name] = true
		want, said := c.desired["main/"+s.Name]
		if !said {
			want = s.Enabled
		}
		want = want && c.runs(s.Name)
		running := c.rt.IsRunning(s.Name)
		switch {
		case want && !running:
			if err := c.rt.Start(s.Name, c.log); err != nil {
				if c.failures[s.Name] != err.Error() {
					c.log.Error("cluster: a channel this server is to run did not start", "channel", s.Name, "error", err)
				}
				c.failures[s.Name] = err.Error()
			} else {
				delete(c.failures, s.Name)
			}
		case !want && running:
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_ = c.rt.Stop(sctx, s.Name)
			cancel()
		}
	}
	// A channel running here whose file has gone.
	_, running := c.rt.ChannelStates()
	for name := range running {
		if !configured[name] {
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_ = c.rt.Stop(sctx, name)
			cancel()
		}
	}
}

// loop reconciles every second, and at once when woken.
func (c *clusterRun) loop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.wake:
		}
		c.reconcile(ctx)
	}
}

func (c *clusterRun) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// mayAlert says whether this server sends an alert about a channel: one it runs, or on the leader one no server runs and
// those about no channel at all.
func (c *clusterRun) mayAlert(channel string) bool {
	if channel == "" {
		return c.coord.IsLeader()
	}
	return c.expected(channel)
}

// ---------- api.ClusterControl ----------

func (c *clusterRun) Status(ctx context.Context) (cluster.Status, error) { return c.coord.Status(ctx) }

func (c *clusterRun) Runs(_ string, channel string) bool { return c.runs(channel) }

func (c *clusterRun) SetDesired(ctx context.Context, tenant, channel string, running bool, by string) error {
	err := c.coord.SetDesired(ctx, tenant, channel, running, by)
	c.poke()
	return err
}

func (c *clusterRun) StepDown(ctx context.Context, d time.Duration, by string) error {
	return c.coord.StepDown(ctx, d, by)
}

func (c *clusterRun) Move(ctx context.Context, _ string, channel, toNode, by string) error {
	return c.coord.Move(ctx, c.leaseFor(channel), toNode, by)
}

func (c *clusterRun) IsLeader() bool { return c.coord.IsLeader() }

func (c *clusterRun) RunningOn(ctx context.Context, _ string, channel string) string {
	_, name := c.coord.Holder(ctx, c.leaseFor(channel))
	return name
}

// sharedDataFiles are the files in the data directory every server of a cluster keeps the same. The log, the node id, a
// local database and TLS keys stay each server's own.
func sharedDataFile(rel string) bool {
	switch rel {
	case "perfuse-settings.yaml", "alerts.yaml", "perfuse-peers.yaml", "ldap.yaml", "saml.yaml", "oidc.yaml":
		return true
	}
	if strings.HasPrefix(rel, "perfuse-logo.") {
		return true
	}
	return strings.HasPrefix(rel, "direct-certs/") || strings.HasPrefix(rel, "views/")
}

// fileChanged applies a file another server changed: a channel restarts with its new file, a table's channels restart,
// settings and the logo are read again. What only a restart reads is said so.
func (c *clusterRun) fileChanged(area, rel string, deleted bool, settings interface{ Reload() ([]string, error) },
	logo interface{ Reload() error }, restartOnly func(key string) bool) {
	switch area {
	case "channels":
		base := path.Base(rel)
		switch {
		case strings.HasSuffix(base, ".codeset.yaml"):
			restarted, failed := c.rt.RestartUsingTables(context.Background(), base, c.log)
			c.log.Info("cluster: a mapping table changed on another server", "file", rel, "restarted", restarted, "failed", len(failed))
		case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
			name := strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
			c.mu.Lock()
			if c.rt.IsRunning(name) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				_ = c.rt.Stop(ctx, name)
				cancel()
				if !deleted {
					if err := c.rt.Start(name, c.log); err != nil {
						c.log.Error("cluster: a channel changed on another server did not start again", "channel", name, "error", err)
					}
				}
			}
			c.mu.Unlock()
			c.log.Info("cluster: a channel changed on another server", "channel", name, "deleted", deleted)
			c.poke()
		}
	case "data":
		switch {
		case rel == "perfuse-settings.yaml" && settings != nil:
			changed, err := settings.Reload()
			if err != nil {
				c.log.Error("cluster: the settings another server saved do not load; this server keeps its own", "error", err)
				return
			}
			var later []string
			for _, k := range changed {
				if restartOnly != nil && restartOnly(k) {
					later = append(later, k)
				}
			}
			c.log.Info("cluster: settings changed on another server", "changed", changed)
			if len(later) > 0 {
				c.log.Warn("cluster: some settings changed on another server take effect here at the next restart", "settings", later)
			}
		case strings.HasPrefix(rel, "perfuse-logo.") && logo != nil:
			_ = logo.Reload()
		default:
			c.log.Warn("cluster: a file changed on another server; this server reads it at its next restart", "file", rel)
		}
	}
}
