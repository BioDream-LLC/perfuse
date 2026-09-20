package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/transform"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// This file is the stage between filtering and delivery, where a message may be
// changed.
//
// Two things run here, in a fixed order: the declarative transformations, then
// the script. The order is not configurable, and the reason is that the two
// layers are not equal. A declarative step is validated when the file loads, is
// visible in a diff and can be reviewed by somebody who does not read
// JavaScript. A script can only be run. Putting the checkable layer first means a
// script sees a message that has already been normalised, and anything it then
// does stands out as the exception rather than being mixed in with the ordinary
// work.
//
// The whole stage is skipped when a channel has neither, which is the common
// case, and skipping it avoids the cost of building a tree at all.

// stageResult is what the transformation stage produced.
type stageResult struct {
	// Raw is the message to deliver. It is the original bytes when nothing
	// changed, so a channel that only filters does not pay for a re-encode.
	Raw []byte

	// Message is the reparsed message when the bytes changed, so that
	// destinations and acknowledgement see what is actually being sent rather
	// than what arrived.
	Message *hl7.Message

	// Changes are the declarative edits that took effect.
	Changes []transform.Change

	// ScriptPaths are the paths a script wrote, for the formats addressed by path rather than as a tree.
	//
	// Separate from Changes because a declarative change carries before and after values and a script write does not - the
	// script had the values and chose not to tell us. Reporting them in one list would mean inventing empty before values,
	// which reads as "it was blank" rather than "nobody recorded it".
	ScriptPaths []string

	// Skips are the steps that ran and changed nothing, with why.
	//
	// Carried so the trace can show them. The commonest fault in an interface that looks like it works is a step
	// addressing a field the sender does not populate: nothing errors, the message goes on unchanged, and the only
	// evidence is that fewer changes happened than somebody expected - which requires them to have had an
	// expectation precise enough to notice.
	Skips []transform.Skip

	// Logs are the lines the script wrote.
	Logs []script.LogLine

	// Accepted is false when a filter script rejected the message.
	Accepted bool

	// RejectedBy names what rejected it, for the record.
	RejectedBy string
}

// hasTransformStage reports whether anything would run.
func hasTransformStage(cfg *config.Channel) bool {
	return cfg.Pipeline().Len() > 0 || cfg.FilterScript() != nil || cfg.TransformerScript() != nil
}

// runTransformStage applies the transformations and scripts to one message.
//
// An error here is a channel fault rather than the sender's fault, so the caller
// answers with an application error and leaves the data at its origin. Silently
// forwarding a message whose transformation failed would be worse: the receiver
// would store something the configuration says should never have been sent.
func (c *Channel) runTransformStage(m *hl7.Message, raw []byte) (stageResult, error) {
	out := stageResult{Raw: raw, Message: m, Accepted: true}

	cfg := c.cfg
	if !hasTransformStage(cfg) {
		return out, nil
	}

	// Mirth's scripts operate on the message as XML, and so do the declarative
	// steps, so one tree serves both.
	root, err := hl7xml.FromMessage(m)
	if err != nil {
		return out, fmt.Errorf("could not build a message tree: %w", err)
	}

	if pipeline := cfg.Pipeline(); pipeline.Len() > 0 {
		changes, skips, err := pipeline.ApplyTraced(root)
		if err != nil {
			return out, fmt.Errorf("transformation: %w", err)
		}
		out.Changes = changes
		out.Skips = skips
	}

	engine := cfg.ScriptEngine()

	if filter := cfg.FilterScript(); filter != nil && engine != nil {
		ctx := c.scriptContext(root, string(raw))
		started := time.Now()
		res, err := engine.Run(filter, ctx)
		// Timed and recorded whether it succeeded or not. A script that has become
		// slow enough to matter is invisible in a success count, and a script that
		// is timing out needs to show up somewhere other than the log.
		c.recordScript("filter", time.Since(started), err, errors.Is(err, script.ErrTimeout))
		out.Logs = append(out.Logs, res.Logs...)
		if err != nil {
			return out, fmt.Errorf("filter script: %w", err)
		}
		if !res.Accept {
			out.Accepted = false
			out.RejectedBy = "filter script"
			return out, nil
		}
	}

	if transformer := cfg.TransformerScript(); transformer != nil && engine != nil {
		// Encoded from the tree rather than reusing the incoming bytes, because the declarative steps have already run and
		// the incoming bytes predate them. A module handed the earlier text would return output that silently undid every
		// step, and the channel would report success.
		current, encErr := hl7xml.ToER7(root, hl7xml.DefaultOptions())
		if encErr != nil {
			return out, fmt.Errorf("could not encode the message for the transformer: %w", encErr)
		}

		ctx := c.scriptContext(root, string(current))
		started := time.Now()
		res, err := engine.Run(transformer, ctx)
		c.recordScript("transformer", time.Since(started), err, errors.Is(err, script.ErrTimeout))
		out.Logs = append(out.Logs, res.Logs...)
		if err != nil {
			return out, fmt.Errorf("transformer script: %w", err)
		}

		// A script that returned replacement text rather than editing the tree.
		//
		// JavaScript and Lua transformers work by mutation - they assign into msg - so for years the result's text was
		// ignored here and that was correct. A WebAssembly module cannot mutate the tree: it is handed the message on stdin
		// and writes its answer to stdout, which is what lets it be written in any language. So its output was discarded,
		// and because a module writing nothing means the message is unchanged, an inert transformer was indistinguishable
		// from one that had decided to leave the message alone. The channel reported success on every message.
		//
		// Re-parsed rather than passed through as bytes, so that a module which produces something that is not HL7 fails
		// here, where it is attributable, rather than at the destination where it looks like the receiver's problem.
		if res.Replaced {
			replaced, perr := hl7.Parse([]byte(res.Text))
			if perr != nil {
				return out, fmt.Errorf("the transformer produced something that is not valid HL7: %w", perr)
			}

			next, nerr := hl7xml.FromMessage(replaced)
			if nerr != nil {
				return out, fmt.Errorf("could not build a tree from the transformer's output: %w", nerr)
			}
			root = next
		}
	}

	// Re-encode only when something could have changed the tree. A filter script
	// on its own leaves the message alone, and re-encoding it would rewrite
	// delimiters and trailing separators for no reason - a difference the
	// receiving system might notice even though the data is identical.
	if len(out.Changes) == 0 && cfg.TransformerScript() == nil {
		return out, nil
	}

	encoded, err := hl7xml.ToER7(root, hl7xml.DefaultOptions())
	if err != nil {
		return out, fmt.Errorf("could not re-encode the transformed message: %w", err)
	}

	// The transformed bytes must still parse. A transformation that produces
	// something unparseable has to fail here, where it is attributable, rather
	// than at the destination where it looks like the receiver's problem.
	reparsed, err := hl7.Parse(encoded)
	if err != nil {
		return out, fmt.Errorf("the transformed message is no longer valid HL7: %w", err)
	}

	out.Raw = encoded
	out.Message = reparsed
	return out, nil
}

// scriptContext builds the per-message script context.
//
// A fresh channel map per message is deliberate: it is the scope a Mirth script
// expects to be private to one message, and sharing it would let one patient's
// values appear while handling another's.
func (c *Channel) scriptContext(root *xtree.Node, raw string) *script.Context {
	return &script.Context{
		Message: root,

		// The message as text, which is what a WebAssembly module is given on stdin.
		//
		// It was left empty for a long time, and the consequence was silent: a module received nothing, decided on nothing,
		// and a filter dropped every message while a transformer appeared to run and changed nothing. Nothing errored,
		// because a module that writes no output means the message is unchanged - so a wiring gap read as a script that had
		// decided to do nothing.
		//
		// Passed in rather than derived here, because deriving it would need to know the format: a v2 tree encodes to ER7
		// and a v3 tree does not.
		Raw:          raw,
		ChannelName:  c.cfg.Name,
		ChannelMap:   script.NewSharedMap(),
		ConnectorMap: script.NewSharedMap(),
		ResponseMap:  script.NewSharedMap(),
		SourceMap:    script.NewSharedMap(),
		Log: func(level, message string) {
			// Script output goes to the channel log at the level the script
			// asked for, tagged so it is obvious where it came from.
			switch level {
			case "error", "fatal":
				c.log.Error("script", "message", message)
			case "warn":
				c.log.Warn("script", "message", message)
			case "debug", "trace":
				c.log.Debug("script", "message", message)
			default:
				c.log.Info("script", "message", message)
			}
		},
	}
}

// runTreeScriptStage runs a channel's filter and transformer scripts against an XML tree.
//
// Named for the binding rather than the format, because nothing in here is v3-specific: it takes an xtree.Node, runs the
// scripts against it, and re-serialises. v3 documents and SCRIPT prescriptions are both XML, so both arrive as the same type
// and get the same treatment. It was called runTreeScriptStage while v3 was the only caller, which read as though the stage
// knew something about v3 - and that impression is why SCRIPT went without scripted filters for as long as it did.
//
// Simpler than the v2 equivalent, and for a reason worth stating: the script layer already works on an XML tree, because Mirth's
// scripts do. For v2 that means converting a pipe-delimited message into a tree and back again, and the conversion is where the
// delimiters and trailing separators get rewritten. A v3 document is already a tree, so there is nothing to convert - the script
// sees the document itself.
//
// That also means the script API needs no v3-specific additions. A script reading msg['patient']['id'] is doing the same thing
// on both, which is the whole reason not to invent a second scripting model.
func (c *Channel) runTreeScriptStage(root *xtree.Node, raw []byte) (stageResult, error) {
	out := stageResult{Raw: raw, Accepted: true}

	cfg := c.cfg
	engine := cfg.ScriptEngine()
	if engine == nil {
		return out, nil
	}

	if filter := cfg.FilterScript(); filter != nil {
		ctx := c.scriptContext(root, string(raw))
		started := time.Now()
		res, err := engine.Run(filter, ctx)
		c.recordScript("filter", time.Since(started), err, errors.Is(err, script.ErrTimeout))
		out.Logs = append(out.Logs, res.Logs...)
		if err != nil {
			return out, fmt.Errorf("filter script: %w", err)
		}
		if !res.Accept {
			out.Accepted = false
			out.RejectedBy = "filter script"

			return out, nil
		}
	}

	transformer := cfg.TransformerScript()
	if transformer != nil {
		ctx := c.scriptContext(root, string(raw))
		started := time.Now()
		res, err := engine.Run(transformer, ctx)
		c.recordScript("transformer", time.Since(started), err, errors.Is(err, script.ErrTimeout))
		out.Logs = append(out.Logs, res.Logs...)
		if err != nil {
			return out, fmt.Errorf("transformer script: %w", err)
		}

		// A script that returned replacement text rather than editing the tree.
		//
		// JavaScript and Lua transformers work by mutation - they assign into msg - so for years the result's text was
		// ignored here and that was correct. A WebAssembly module cannot mutate the tree: it is handed the message on stdin
		// and writes its answer to stdout, which is what lets it be written in any language. So its output was discarded,
		// and because a module writing nothing means the message is unchanged, an inert transformer was indistinguishable
		// from one that had decided to leave the message alone. The channel reported success on every message.
		//
		// Re-parsed as XML rather than passed through, so that a module producing something malformed fails here, where it
		// is attributable, rather than at the destination where it looks like the receiver's problem.
		//
		// WellFormed and then Parse, not Parse alone: Parse sets Strict to false, which is right for reading a document
		// somebody else sent and wrong as a check on our own output - it accepts text no conforming parser would.
		//
		// This stage serves HL7 v3 and SCRIPT, both XML. An earlier version of this block parsed the replacement as HL7 v2
		// because the edit that added it to the v2 stage matched here as well, and nothing caught it: a v3 transformer
		// written in JavaScript or Lua mutates the tree and never sets Replaced, so the branch was unreachable except from
		// a WebAssembly module on a v3 or SCRIPT channel, which nothing tested. It does now.
		if res.Replaced {
			if wf := xtree.WellFormed([]byte(res.Text)); wf != nil {
				return out, fmt.Errorf("the transformer produced XML that is not well formed: %w", wf)
			}

			next, perr := xtree.Parse([]byte(res.Text))
			if perr != nil {
				return out, fmt.Errorf("could not read the transformer's output as XML: %w", perr)
			}
			root = next
		}
	}

	// Re-serialised only when a transformer ran.
	//
	// A filter script on its own leaves the document alone, and re-serialising it would rewrite whitespace and
	// self-closing tags for no reason. That is the same reasoning as the v2 path and it matters more here, because a
	// receiver comparing documents byte for byte is a thing that happens with signed XML.
	if transformer == nil {
		return out, nil
	}

	encoded := root.Marshal(2)

	// The result must still parse. A script can set an element name with a space in it, which serialises into
	// something no parser will read - and failing here makes it attributable, rather than at the destination where it
	// looks like the receiver's problem.
	// WellFormed rather than Parse, because Parse sets Strict to false and will read back text that no conforming parser
	// accepts. A guard built on it passed a document containing an element named "not a valid name" - the exact case this
	// comment describes - and the receiver would have been the one to reject it.
	if err := xtree.WellFormed(encoded); err != nil {
		return out, fmt.Errorf("the transformed document is no longer valid XML: %w", err)
	}

	out.Raw = encoded

	return out, nil
}
