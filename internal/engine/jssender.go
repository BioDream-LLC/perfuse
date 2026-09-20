package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// JavaScriptSender runs a script instead of sending anywhere.
//
// Mirth's JavaScript Writer, and used far more than a feature list suggests: it is the escape hatch sites reach for when
// they need something no connector covers. A migration that cannot run those cannot move the channels containing them,
// which in a mature installation is a large fraction of them.
//
// Constructed by the channel rather than the sender factory, because it needs the channel's script engine - so the shared
// maps a transformer wrote are the same ones this reads, which is what makes a script written for Mirth behave the same
// way here.
type JavaScriptSender struct {
	cfg     *config.JavaScriptDestination
	name    string
	channel string
	script  *script.Script
	engine  *script.Engine
	log     *slog.Logger
}

// Send runs the script for one message.
func (s *JavaScriptSender) Send(ctx context.Context, msg []byte) error {
	if s.script == nil || s.engine == nil {
		return errors.New("this javascript destination has no compiled script, which means the channel was not validated")
	}

	// Parsed so the script sees msg as a tree, exactly as a transformer does. A parse failure is not fatal: a script
	// destination is sometimes the thing handling a message nothing else could read, and refusing to run it would remove
	// the only tool available for that.
	var tree *xtree.Node
	if parsed, err := hl7.Parse(msg); err == nil {
		if root, err := hl7xml.FromMessage(parsed); err == nil {
			tree = root
		}
	}

	timeout := s.cfg.Timeout
	if timeout <= 0 {
		timeout = config.DefaultJavaScriptTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_ = runCtx

	// Fresh maps per execution, matching what the channel does for its own scripts and for the same reason: these are
	// scoped to one message, and sharing them across deliveries would let one patient's values be read while handling
	// another's.
	result, err := s.engine.Run(s.script, &script.Context{
		Message:      tree,
		Raw:          string(msg),
		ChannelName:  s.channel,
		ChannelMap:   script.NewSharedMap(),
		ConnectorMap: script.NewSharedMap(),
		ResponseMap:  script.NewSharedMap(),
		SourceMap:    script.NewSharedMap(),
		Log:          func(level, message string) { s.logScript(level, message) },
	})
	if err != nil {
		// The script's own error, unwrapped as far as it goes. A JavaScript Writer that throws is reporting a delivery
		// failure, and the author's message is the useful part rather than a wrapper around it.
		return fmt.Errorf("the script failed: %w", err)
	}

	// A script that returned nothing counts as success by default, because Mirth's writers overwhelmingly do their work
	// and return nothing - requiring an explicit return would break every one of them on import.
	if !result.Replaced {
		if s.cfg.ReturnsSuccessOnUndefined() {
			return nil
		}
		return errors.New("the script returned nothing, and this destination is configured to require an explicit result")
	}

	text := strings.TrimSpace(result.Text)
	switch strings.ToLower(text) {
	case "", "true", "ok", "success", "sent":
		return nil
	case "false":
		return errors.New("the script returned false")
	}

	// Anything else is treated as a failure message rather than as data. A writer returning a string is almost always
	// reporting why it could not do its job, and treating that as success would hide it.
	return fmt.Errorf("the script reported: %s", text)
}

// Describe names the destination.
func (s *JavaScriptSender) Describe() string {
	lines := strings.Count(strings.TrimSpace(s.cfg.Script), "\n") + 1
	return fmt.Sprintf("handled by a script (%d line(s))", lines)
}

// Close releases nothing.
func (s *JavaScriptSender) Close() error { return nil }

func (s *JavaScriptSender) logScript(level, message string) {
	if s.log == nil {
		return
	}
	// Attributed to the destination rather than the channel, because a channel with three script destinations produces
	// three streams of logger lines and "which one said this" is otherwise unanswerable.
	s.log.Info("script", "destination", s.name, "level", level, "message", message)
	_ = time.Now
}
