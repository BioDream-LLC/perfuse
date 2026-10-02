package engine

import (
	"context"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/admit"
	"log/slog"
	"sync"

	"github.com/biodream-llc/perfuse/internal/config"
)

// Engine runs a set of channels.
type Engine struct {
	log      *slog.Logger
	channels []*Channel

	// admit bounds deliveries in flight across every channel.
	//
	// One controller for the process, because the descriptor budget it protects belongs to the process. A
	// controller per channel would let ten channels each believe they had the whole allowance.
	admit *admit.Controller

	mu      sync.Mutex
	started []*Channel
}

// New prepares an engine from loaded configuration.
//
// Every channel is constructed before any is started, so a configuration error
// in the last file does not leave the first three listening.
func New(cfgs []*config.Channel, factory SenderFactory, log *slog.Logger) (*Engine, error) {
	if log == nil {
		log = slog.Default()
	}

	// What this process can afford, worked out from the descriptor limit it actually has and raised first
	// where that is possible. Logged rather than silent: a limit that engages without ever having explained
	// itself is indistinguishable from a bug, and nobody chose this one.
	controller, budget, report := admit.Shared()
	if report.Err != nil {
		log.Warn("could not raise the file descriptor limit, planning inside the current one",
			"err", report.Err, "limit", report.Soft)
	}
	if budget.TooTight {
		log.Warn("file descriptor limit is too low for comfort", "detail", budget.Explanation)
	} else {
		log.Info("delivery concurrency planned", "detail", budget.Explanation,
			"in_flight_limit", budget.Total, "per_destination_limit", budget.PerDestination)
	}

	e := &Engine{log: log, admit: controller}

	for _, cfg := range cfgs {
		if !cfg.IsEnabled() {
			log.Info("channel disabled, not starting", "channel", cfg.Name, "file", cfg.Path())
			continue
		}
		ch, err := NewChannel(cfg, factory, log)
		if err != nil {
			e.closeAll()
			return nil, err
		}
		ch.SetAdmit(e.admit)
		e.channels = append(e.channels, ch)
	}

	if len(e.channels) == 0 {
		return nil, fmt.Errorf("no enabled channels to run")
	}
	return e, nil
}

// Admit is the delivery limiter this engine shares with its channels.
//
// Exported so a server that starts and stops channels outside the engine - which is what the interface
// does - can give them the same one, rather than each newly started channel getting its own allowance.
func (e *Engine) Admit() *admit.Controller { return e.admit }

// Channels returns the prepared channels.
func (e *Engine) Channels() []*Channel { return e.channels }

// Start starts every channel. If one fails, those already started are stopped,
// so the process does not end up half up with nobody having said so.
func (e *Engine) Start() error {
	for _, ch := range e.channels {
		if err := ch.Start(); err != nil {
			e.log.Error("channel failed to start, stopping the ones already running",
				"channel", ch.Name(), "err", err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // stop immediately; nothing is in flight yet
			_ = e.Stop(ctx)
			return err
		}
		e.mu.Lock()
		e.started = append(e.started, ch)
		e.mu.Unlock()

		e.log.Info("channel started",
			"channel", ch.Name(),
			"listen", ch.cfg.Source.Listen,
			"destinations", len(ch.dests),
			"ack", string(ch.cfg.Source.AckWhen()))
	}
	return nil
}

// Stop shuts every channel down, waiting for messages in flight until ctx is
// done.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	running := make([]*Channel, len(e.started))
	copy(running, e.started)
	e.started = nil
	e.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, len(running))
	for i, ch := range running {
		wg.Add(1)
		go func(i int, ch *Channel) {
			defer wg.Done()
			errs[i] = ch.Stop(ctx)
		}(i, ch)
	}
	wg.Wait()

	var firstErr error
	for _, err := range errs {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Stats returns a snapshot per channel.
func (e *Engine) Stats() map[string]Stats {
	out := make(map[string]Stats, len(e.channels))
	for _, ch := range e.channels {
		out[ch.Name()] = ch.Stats()
	}
	return out
}

func (e *Engine) closeAll() {
	for _, ch := range e.channels {
		ch.closeSenders()
	}
}
