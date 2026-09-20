package alerts

import (
	"context"
	"log/slog"
	"time"

	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/queue"
)

// Watcher turns live metrics into readings and evaluates the rules on a timer.
//
// It is separate from Evaluator so the rules can be tested against numbers rather
// than against a running server, and so the same rules could later be evaluated
// against readings collected from somewhere else.
type Watcher struct {
	Evaluator *Evaluator
	Metrics   *metrics.Collector
	Queue     *queue.Store
	Log       *slog.Logger

	// Interval is how often rules are evaluated. Frequent enough that a critical
	// condition is noticed promptly, and not so frequent that evaluation itself
	// becomes load.
	Interval time.Duration

	// Window is how much history each reading covers. It has to be longer than the
	// shortest debounce, or a condition could never be observed for long enough to
	// fire.
	Window time.Duration

	// RunningChannels reports which enabled channels are actually listening.
	// Supplied as a function because the watcher must not import the API package.
	RunningChannels func() (configured []string, running map[string]bool)

	// Contracts reports how far each channel's feed has drifted from its contract.
	//
	// A function for the same reason as RunningChannels: checking a contract means profiling stored traffic,
	// and this package must not depend on the message store or the profiler. Nil when no channel has a
	// contract, which is the common case and costs nothing.
	//
	// Called on each evaluation, so the caller is expected to return a cached result rather than doing the
	// work here - the alert loop runs every thirty seconds and profiling five hundred messages on that
	// schedule would be pointless load.
	Contracts func() map[string]ContractDrift

	// Rhythms reports what each channel normally carries, keyed by channel name.
	//
	// A function for the same reason as Contracts, and with the same expectation: learning a rhythm scans four weeks
	// of message history, and doing that every thirty seconds would be absurd. The supplier caches.
	//
	// Nil when there is no message store, which is a legitimate deployment - the rule then never fires rather than
	// firing on an assumption.
	Rhythms func() map[string]RhythmSource

	// BeforeRead runs immediately before each reading is taken.
	//
	// Exists so work that has to be in step with a reading - re-checking a contract, for instance - happens on
	// the same schedule rather than on a timer of its own. Two timers producing a number and its explanation
	// independently is how a report comes to disagree with itself.
	BeforeRead func()
}

// ContractDrift is one channel's contract result, as the alert path needs it.
type ContractDrift struct {
	// Violations is how many expectations did not hold.
	Violations int

	// Detail is the worst one, in words, for the alert body.
	Detail string
}

// Defaults.
const (
	DefaultInterval = 30 * time.Second
	DefaultWindow   = 5 * time.Minute
)

// Run evaluates until the channel is closed.
func (w *Watcher) Run(stop <-chan struct{}) {
	if w.Evaluator == nil || w.Metrics == nil {
		return
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	if w.Interval <= 0 {
		w.Interval = DefaultInterval
	}
	if w.Window <= 0 {
		w.Window = DefaultWindow
	}

	w.Log.Info("watching for alerts",
		"interval", w.Interval.String(), "window", w.Window.String(),
		"rules", len(w.Evaluator.Rules()))

	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		reading := w.read()
		for _, a := range w.Evaluator.Evaluate(reading) {
			// Logged as well as notified, so a deployment with no webhook still has
			// a record, and so the log explains a gap in the message store.
			level := slog.LevelWarn
			if a.Severity == Critical {
				level = slog.LevelError
			}
			w.Log.Log(context.Background(), level, "alert",
				"kind", string(a.Kind), "channel", a.Channel,
				"destination", a.Destination, "summary", a.Summary,
				"value", a.Value, "threshold", a.Threshold,
				"since", a.FiringSince.Format(time.RFC3339))
		}
	}
}

// read builds a reading from the collector and the queue.
func (w *Watcher) read() Reading {
	if w.BeforeRead != nil {
		w.BeforeRead()
	}

	now := time.Now()
	r := Reading{
		At:       now,
		Channels: map[string]ChannelReading{},
		Queues:   map[string]QueueReading{},
	}

	// Counters are read as a delta over the window rather than as running totals.
	// An error rate computed from totals since boot converges on a constant and
	// stops responding to anything, which makes it useless for alerting.
	deltas := w.Metrics.Deltas(w.Window)

	if w.Rhythms != nil {
		r.Rhythms = w.Rhythms()
	}

	if w.Contracts != nil {
		for name, drift := range w.Contracts() {
			c := r.Channels[name]
			c.ContractViolations = float64(drift.Violations)
			c.ContractDetail = drift.Detail
			r.Channels[name] = c
		}
	}

	for name, value := range deltas[metrics.MessagesReceived] {
		c := r.Channels[name]
		c.Received = value
		r.Channels[name] = c
	}
	for name, value := range deltas[metrics.MessagesDelivered] {
		c := r.Channels[name]
		c.Delivered = value
		r.Channels[name] = c
	}
	for name, value := range deltas[metrics.MessagesFailed] {
		c := r.Channels[name]
		c.Failed = value
		r.Channels[name] = c
	}
	for name, value := range deltas[metrics.MessagesUnparseable] {
		c := r.Channels[name]
		c.Unparseable = value
		r.Channels[name] = c
	}
	for name, value := range deltas[metrics.ScriptErrors] {
		c := r.Channels[name]
		c.ScriptErrors = value
		r.Channels[name] = c
	}
	for name, value := range deltas[metrics.DatabaseQuarantined] {
		c := r.Channels[name]
		c.Quarantined = value
		r.Channels[name] = c
	}
	for name, value := range w.Metrics.Percentile(metrics.MessageDuration, 0.99, w.Window) {
		c := r.Channels[name]
		c.P99Seconds = value
		r.Channels[name] = c
	}

	// The queue is read from the table rather than from the gauges. The gauges are
	// sampled on their own timer, and an alert about a backlog should not be
	// looking at a number up to ten seconds stale when the truth is one query away.
	if w.Queue != nil {
		rows, err := w.Queue.Depth(context.Background())
		if err != nil {
			w.Log.Error("could not read the queue for alerting", "error", err)
		}
		for _, row := range rows {
			r.Queues[row.Channel+"/"+row.Destination] = QueueReading{
				Channel:       row.Channel,
				Destination:   row.Destination,
				Pending:       row.Pending,
				Failed:        row.Failed,
				OldestSeconds: row.OldestSeconds,
				MaxAttempts:   row.MaxAttempts,
			}
		}
	}

	if w.RunningChannels != nil {
		configured, running := w.RunningChannels()
		r.ChannelsConfigured = len(configured)
		for _, name := range configured {
			if running[name] {
				r.ChannelsRunning++
				continue
			}
			r.NotRunning = append(r.NotRunning, name)
		}
	}

	return r
}
