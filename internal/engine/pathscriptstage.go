package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/steps"
)

// runPathScriptStage runs a channel's filter and transformer scripts against a message addressed by path.
//
// # Why this is generic and the tree stages are not
//
// runTreeScriptStage and its v2 counterpart each know their format's tree. This one knows nothing about the format: it takes a
// steps.Accessor, which is the same interface the declarative transformation steps already use, so X12, NCPDP and delimited get
// scripting from one implementation.
//
// That is possible because those three formats agree on the shape of the question. A path names a value, a value can be read and
// written, and the accessor knows the invariants - x12.Set re-pads a fixed-width ISA element, ncpdp.Set refuses an ambiguous
// path, delimited.Set refuses a write across rows. A generic stage that tried to do those itself would have to learn all of them
// again, or lose them.
//
// # What is deliberately not here
//
// No re-serialisation. Each format's handler already knows how to turn its own message back into bytes, and doing it here would
// mean this function knowing three encodings - which is the thing the accessor exists to avoid.
func runPathScriptStage[M any](c *Channel, msg M, accessor steps.Accessor[M]) (M, stageResult, error) {
	out := stageResult{Accepted: true}

	engine := c.cfg.ScriptEngine()
	if engine == nil {
		return msg, out, nil
	}

	current := msg

	if filter := c.cfg.FilterScript(); filter != nil {
		// scriptContext with no tree: these formats have no xtree to offer, and pretending otherwise would mean building
		// one nobody reads. A fresh map per message is the same rule the tree path applies - it is the scope a script
		// expects to be private to one message, and sharing it would let one patient's values appear while handling
		// another's.
		ctx := c.scriptContext(nil, "")
		ctx.RawUnavailable = true

		next, _, res, err := script.RunPathScript(engine, filter, ctx, current, accessor)
		c.recordScript("filter", res.Duration, err, errors.Is(err, script.ErrTimeout))
		out.Logs = append(out.Logs, res.Logs...)
		if err != nil {
			return msg, out, fmt.Errorf("filter script: %w", err)
		}

		// A filter's writes are kept rather than discarded, because discarding them silently would make a script that set a
		// field and then returned true behave differently from the same two lines in a transformer. Whether writing from a
		// filter is good practice is the author's business; whether it works has to be consistent.
		current = next

		if !res.Accept {
			out.Accepted = false
			out.RejectedBy = "filter script"

			return current, out, nil
		}
	}

	if transformer := c.cfg.TransformerScript(); transformer != nil {
		ctx := c.scriptContext(nil, "")
		ctx.RawUnavailable = true

		started := time.Now()
		next, changed, res, err := script.RunPathScript(engine, transformer, ctx, current, accessor)
		c.recordScript("transformer", time.Since(started), err, errors.Is(err, script.ErrTimeout))
		out.Logs = append(out.Logs, res.Logs...)
		if err != nil {
			return msg, out, fmt.Errorf("transformer script: %w", err)
		}

		current = next
		out.ScriptPaths = changed
	}

	return current, out, nil
}
