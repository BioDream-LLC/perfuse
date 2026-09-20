package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/script"
)

// jsReader runs a JavaScript script on a timer and feeds returned messages into
// the channel. This is the equivalent of Mirth Connect's JavaScript Reader.
type jsReader struct {
	ch  *Channel
	cfg *config.JavaScriptSource
	log *slog.Logger

	engine   *script.Engine
	compiled *script.Script

	mu    sync.Mutex
	stats JSReaderStats

	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup
}

// JSReaderStats reports what the reader has done.
type JSReaderStats struct {
	Polls        int64     `json:"polls"`
	Messages     int64     `json:"messages"`
	Errors       int64     `json:"errors"`
	LastPoll     time.Time `json:"lastPoll,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	LastDuration string    `json:"lastDuration,omitempty"`
}

func (c *Channel) startJavaScriptSource() error {
	cfg := c.cfg.Source.JavaScript
	if cfg == nil {
		return fmt.Errorf("channel %q has a javascript source but no javascript block", c.cfg.Name)
	}
	if cfg.Script == "" {
		return fmt.Errorf("channel %q: javascript source has an empty script", c.cfg.Name)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	engine := script.New(script.Options{
		Timeout: timeout,
	})

	compiled, err := engine.Compile("reader", cfg.Script, script.Reader)
	if err != nil {
		return fmt.Errorf("channel %q: compiling reader script: %w", c.cfg.Name, err)
	}

	interval := cfg.PollInterval
	if interval == 0 {
		interval = 5 * time.Second
	}

	r := &jsReader{
		ch:       c,
		cfg:      cfg,
		log:      c.log.With("source", "javascript"),
		engine:   engine,
		compiled: compiled,
		stop:     make(chan struct{}),
	}

	r.wg.Add(1)
	go r.loop(interval)

	c.jsReader = r
	return nil
}

func (r *jsReader) loop(interval time.Duration) {
	defer r.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.poll()
		}
	}
}

func (r *jsReader) poll() {
	r.mu.Lock()
	r.stats.Polls++
	r.stats.LastPoll = time.Now()
	r.mu.Unlock()

	result, err := r.engine.Run(r.compiled, &script.Context{
		Log: func(level, message string) {
			r.ch.logScript("reader", level, message)
		},
	})

	r.mu.Lock()
	r.stats.LastDuration = result.Duration.String()
	r.mu.Unlock()

	if err != nil {
		r.mu.Lock()
		r.stats.Errors++
		r.stats.LastError = err.Error()
		r.mu.Unlock()
		r.log.Error("reader script failed", "channel", r.ch.cfg.Name, "error", err)
		r.ch.incMetric(metrics.ScriptErrors)
		return
	}

	if len(result.Messages) == 0 {
		return
	}

	for _, msg := range result.Messages {
		ctx := context.Background()
		if _, err := r.ch.handle(ctx, []byte(msg)); err != nil {
			r.mu.Lock()
			r.stats.Errors++
			r.stats.LastError = err.Error()
			r.mu.Unlock()
			r.log.Error("handle failed", "channel", r.ch.cfg.Name, "error", err)
		} else {
			r.mu.Lock()
			r.stats.Messages++
			r.mu.Unlock()
		}
	}
}

func (r *jsReader) Close() {
	r.closed.Do(func() {
		close(r.stop)
	})
	r.wg.Wait()
}

func (c *Channel) stopJavaScriptSource() {
	if c.jsReader != nil {
		c.jsReader.Close()
		c.jsReader = nil
	}
}
