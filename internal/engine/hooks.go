package engine

import (
	"context"
	"fmt"

	"github.com/biodream-llc/perfuse/internal/script"
)

// logScript sends script output to the channel log at the level the script asked for, tagged with
// which hook it came from so a line in a busy log can be traced back.
func (c *Channel) logScript(hook, level, message string) {
	switch level {
	case "error", "fatal":
		c.log.Error("script", "hook", hook, "message", message)
	case "warn":
		c.log.Warn("script", "hook", hook, "message", message)
	case "debug", "trace":
		c.log.Debug("script", "hook", hook, "message", message)
	default:
		c.log.Info("script", "hook", hook, "message", message)
	}
}

// The two script hooks that run outside the message pipeline.
//
// A preprocessor runs before parsing and a postprocessor runs after everything is finished. Both are
// standard Mirth, and a migration hits them immediately: the preprocessor is where sites repair
// messages that would otherwise be rejected, and the postprocessor is where they count and notify.

// maxPreprocessorGrowth bounds what a preprocessor may return.
//
// A script that builds a string in a loop can turn a 2 KB message into something that exhausts memory
// before the timeout notices, because the timeout interrupts the interpreter between operations and a
// single string concatenation is one operation. A limit relative to the input is the honest shape of
// the rule: legitimate repairs add characters, they do not multiply the message.
const maxPreprocessorGrowth = 16

// runPreprocessor runs the preprocessor and reports the text to parse.
//
// A nil return means the preprocessor left the message alone, which is the common case: most
// preprocessors edit nothing or fall off the end without returning. That is deliberately different
// from returning an empty string, which is a script saying the message is now empty and is refused.
func (c *Channel) runPreprocessor(ctx context.Context, s *script.Script, raw []byte) ([]byte, error) {
	engine := c.cfg.Scripts.Engine()
	if engine == nil {
		return nil, fmt.Errorf("the preprocessor was compiled but there is no engine to run it")
	}

	// Message is left nil on purpose. A preprocessor runs before parsing and may be looking at
	// something that does not parse at all, so there is no tree to offer and pretending otherwise
	// would mean parsing twice and discarding the first attempt.
	result, err := engine.Run(s, &script.Context{
		Raw: string(raw),
		Log: func(level, message string) { c.logScript("preprocessor", level, message) },
	})
	if err != nil {
		return nil, err
	}

	if !result.Replaced {
		return nil, nil
	}

	text := result.Text

	if text == "" {
		// Refused rather than passed on. An empty message cannot be parsed, so the alternative is an
		// unparseable outcome that blames the sender for something a script did.
		return nil, fmt.Errorf("the preprocessor returned an empty message")
	}
	if len(text) > len(raw)*maxPreprocessorGrowth+1024 {
		return nil, fmt.Errorf(
			"the preprocessor returned %d bytes from a %d byte message, which is more than a repair; "+
				"this is usually a loop building a string",
			len(text), len(raw))
	}

	return []byte(text), nil
}

// runPostprocessor runs the postprocessor after a message has been handled.
//
// Errors are logged and otherwise ignored, which is the one place in this engine where that is the
// right thing to do. The message has already been delivered and the sender has already been
// acknowledged; failing now could not undo either, and turning a delivered message into a failure
// because a notification script had a typo would make the outcome a lie.
func (c *Channel) runPostprocessor(ctx context.Context, rec *MessageRecord) {
	s := c.cfg.Scripts.PostprocessorScript()
	if s == nil {
		return
	}

	engine := c.cfg.Scripts.Engine()
	if engine == nil {
		return
	}

	if _, err := engine.Run(s, &script.Context{
		Raw: string(rec.Raw),
		Log: func(level, message string) { c.logScript("postprocessor", level, message) },
	}); err != nil {
		c.log.Error("the postprocessor failed",
			"err", err, "channel", c.cfg.Name, "outcome", string(rec.Outcome))
	}
}

// runLifecycle runs a deploy or undeploy script.
//
// No message is bound, because there is none. A script reaching for one gets null and a complaint it
// can act on, rather than an empty message that makes its lookups quietly return nothing.
func (c *Channel) runLifecycle(hook string, s *script.Script) error {
	if s == nil {
		return nil
	}

	engine := c.cfg.Scripts.Engine()
	if engine == nil {
		return fmt.Errorf("the %s script was compiled but there is no engine to run it", hook)
	}

	if _, err := engine.Run(s, &script.Context{
		// There is no message at deploy or undeploy time, and saying so is different from saying the message is empty. A
		// hook reaching for one should stop on that line rather than read an empty string, decide the message contains
		// nothing it cares about, and let the channel start.
		RawUnavailable: true,

		Log: func(level, message string) { c.logScript(hook, level, message) },
	}); err != nil {
		return fmt.Errorf("%s script: %w", hook, err)
	}
	return nil
}
