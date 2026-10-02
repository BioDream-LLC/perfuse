package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Following one message through a channel, step by step.
//
// The question this answers is the one asked most often and served worst by every tool in this
// space: why did this message come out like that. Today the answer is to add a log line, deploy,
// and wait for it to happen again - so the feedback loop for a mapping mistake runs through a live
// interface, and the mistake is usually found by whoever is downstream of it.
//
// Nothing here can deliver. The trace is built on the same shadow construction as replay, so no
// sender object exists for the channel being traced. That is what makes it safe to point at a real
// message from production and press the button.

// TraceStage is one thing that happened to the message.
type TraceStage struct {
	// Stage names what ran: "parse", "filter", "transform", "output".
	Stage string `json:"stage"`
	// OK is false when this stage rejected or failed.
	OK bool `json:"ok"`
	// Detail explains the outcome in a sentence.
	Detail string `json:"detail"`

	// Reads are the fields this stage looked at, with their values. For a filter this is the
	// explanation: knowing the filter tested MSH-9.1 and found "ORU" answers the question
	// completely, without needing to reason about the expression.
	Reads []TraceValue `json:"reads,omitempty"`

	// Changes are what this stage altered.
	Changes []transform.Change `json:"changes,omitempty"`

	// Skipped are steps that ran and changed nothing, with why.
	//
	// The part of a trace people need most and the part no output had. A step addressing PID-19 on a feed that has
	// never sent PID-19 is not an error and produces no log line - it simply does nothing, for years.
	Skipped []transform.Skip `json:"skipped,omitempty"`

	// Steps breaks the transform stage into one entry per declarative step, each carrying the message as it stood
	// after that step ran.
	//
	// Only populated on the transform stage. Everything above says what the stage did as a whole, which answers
	// "what changed" but not "which of my fourteen steps changed it, and what did the message look like just
	// before". Those are the questions somebody has when a field is wrong and the pipeline is long.
	Steps []TraceStep `json:"steps,omitempty"`
}

// TraceStep is one declarative step, and the message immediately after it.
type TraceStep struct {
	// Number is the step's position in the pipeline, from 1, so it can be cited.
	Number int `json:"number"`

	// Label is the step's description, or what it does when the author wrote none.
	Label string `json:"label"`

	// Path is the field the step addresses.
	//
	// The most useful single thing about a step that did nothing: almost always the field is absent, and this is
	// what somebody compares against a real message.
	Path string `json:"path,omitempty"`

	// Outcome is "changed", "no-effect", "skipped" or "failed".
	Outcome string `json:"outcome"`

	// Detail explains the outcome in a sentence, including on success. "Ran" without saying what it did leaves the
	// reader to diff two blobs of pipe-delimited text.
	Detail string `json:"detail"`

	// Changes are what this step altered.
	Changes []transform.Change `json:"changes,omitempty"`

	// Message is the whole message as it stood after this step.
	//
	// Held per step rather than reconstructed from the changes, because the thing somebody wants to look at is the
	// message immediately before the step that broke it, and a list of deltas cannot show that without being
	// replayed to get there.
	Message string `json:"message"`
}

// TraceValue is one path and what was in it.
type TraceValue struct {
	Path  string `json:"path"`
	Value string `json:"value"`
	// Present distinguishes an empty field from an absent one, which is a distinction filters
	// turn on and people routinely conflate.
	Present bool `json:"present"`
}

// Trace is the whole journey of one message.
type Trace struct {
	Stages []TraceStage `json:"stages"`

	// Accepted says whether the message would be forwarded.
	Accepted bool `json:"accepted"`
	// Output is the message as it would leave, when it was accepted.
	Output string `json:"output,omitempty"`
	// Input is the message as it arrived, so the two can be compared side by side.
	Input string `json:"input"`

	// Destinations describes which destinations would receive it and which would not, and why.
	Destinations []TraceDestination `json:"destinations,omitempty"`

	// Caveat is set when the trace cannot describe the whole channel.
	Caveat string `json:"caveat,omitempty"`

	// Stale reports that the channel has been changed since the message arrived, so this trace describes what would
	// happen now rather than what happened then.
	//
	// Its own field as well as being in the caveat, so the interface can mark it rather than relying on somebody
	// reading a paragraph. A caveat in prose is a caveat somebody skims.
	Stale bool `json:"stale,omitempty"`
}

// TraceDestination is one destination's decision about this message.
type TraceDestination struct {
	Name string `json:"name"`
	// Would is true when this destination would receive the message.
	Would bool `json:"would"`
	// Why explains the decision.
	Why string `json:"why"`
	// Reads are the fields the destination filter looked at.
	Reads []TraceValue `json:"reads,omitempty"`
}

// TraceMessage follows one message through a channel without delivering anything.
func TraceMessage(ctx context.Context, cfg *config.Channel, raw []byte) (*Trace, error) {
	// Stages starts empty rather than nil. Every return path below happens to append at least one stage
	// first, so this is currently true by accident rather than by construction - and the interface maps
	// over it without a guard. Making it explicit costs nothing and does not depend on the next person
	// preserving an invariant nothing states.
	tr := &Trace{Input: string(raw), Stages: []TraceStage{}}

	if cfg.Type() != config.DataHL7 {
		// Refused rather than half-done. An X12 channel does not run any of the machinery
		// below, and a trace that silently described nothing would be worse than one that
		// explains why it cannot help.
		return nil, fmt.Errorf(
			"tracing is only available on HL7 channels: this channel handles %s, and its filter, "+
				"transformations and scripts are refused at load rather than run", cfg.Type())
	}

	msg, err := hl7.Parse(raw)
	if err != nil {
		tr.Stages = append(tr.Stages, TraceStage{
			Stage:  "parse",
			OK:     false,
			Detail: "the message could not be read as HL7: " + err.Error(),
		})
		return tr, nil
	}

	segs := make([]string, 0, msg.SegmentCount())
	for i := 0; i < msg.SegmentCount(); i++ {
		if s, ok := msg.SegmentAt(i); ok {
			segs = append(segs, s.Name())
		}
	}
	msgType, event, _ := msg.Type()

	tr.Stages = append(tr.Stages, TraceStage{
		Stage: "parse",
		OK:    true,
		Detail: fmt.Sprintf("read as %s^%s with %d segments: %s",
			msgType, event, len(segs), joinSegments(segs)),
	})

	// The filter.
	if f := cfg.FilterExpr(); f != nil {
		reads := readPaths(msg, f.Paths())

		match, err := f.Eval(msg)
		switch {
		case err != nil:
			tr.Stages = append(tr.Stages, TraceStage{
				Stage:  "filter",
				OK:     false,
				Detail: "the filter could not be evaluated: " + err.Error(),
				Reads:  reads,
			})
			return tr, nil
		case !match:
			tr.Stages = append(tr.Stages, TraceStage{
				Stage: "filter",
				OK:    false,
				Detail: "the filter excluded this message, so it is acknowledged as accepted and " +
					"not forwarded. The values it looked at are below.",
				Reads: reads,
			})
			return tr, nil
		default:
			tr.Stages = append(tr.Stages, TraceStage{
				Stage:  "filter",
				OK:     true,
				Detail: "the filter accepted this message",
				Reads:  reads,
			})
		}
	} else {
		tr.Stages = append(tr.Stages, TraceStage{
			Stage:  "filter",
			OK:     true,
			Detail: "no filter, so every message is accepted",
		})
	}

	// The transformations, through the real channel so the trace cannot disagree with what runs.
	// A discarding logger: a trace is a request-scoped diagnostic, and its internals in the
	// server log would be noise attached to somebody clicking a button.
	ch, err := newShadowChannel(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return nil, err
	}

	stage, err := ch.runTransformStage(msg, raw)
	if err != nil {
		tr.Stages = append(tr.Stages, TraceStage{
			Stage:  "transform",
			OK:     false,
			Detail: "a transformation failed, so nothing is delivered: " + err.Error(),
		})
		return tr, nil
	}
	if !stage.Accepted {
		tr.Stages = append(tr.Stages, TraceStage{
			Stage:  "transform",
			OK:     false,
			Detail: "the message was excluded during transformation by " + stage.RejectedBy,
		})
		return tr, nil
	}

	detail := "no transformations, so the message passes through unchanged"
	if n := len(stage.Changes); n > 0 {
		detail = fmt.Sprintf("%d change(s) were made", n)
	} else if cfg.Pipeline() != nil && cfg.Pipeline().Len() > 0 {
		// Steps that ran and changed nothing is a real and confusing outcome: a condition that
		// did not hold, or a value already correct. Said explicitly, because an empty change
		// list otherwise reads as though the steps did not run.
		detail = fmt.Sprintf("%d step(s) ran and changed nothing, either because a condition did "+
			"not hold or because the values were already what the steps would set",
			cfg.Pipeline().Len())
	}

	// Per-step detail, so a long pipeline can be walked rather than read as one before and one after.
	//
	// A failure here does not fail the trace. The stage-level result above is already correct and useful; losing the
	// step-by-step view is a smaller loss than losing the whole trace, and somebody diagnosing an incident should get
	// what can be produced rather than an error page.
	perStep, stepErr := traceSteps(cfg, raw)
	if stepErr != nil {
		perStep = nil
	}

	tr.Stages = append(tr.Stages, TraceStage{
		Stage:   "transform",
		OK:      true,
		Detail:  detail,
		Changes: stage.Changes,
		Skipped: stage.Skips,
		Steps:   perStep,
	})

	tr.Accepted = true
	tr.Output = string(stage.Raw)

	// Which destinations would take it.
	outMsg, err := hl7.Parse(stage.Raw)
	if err != nil {
		outMsg = msg
	}
	for _, d := range cfg.Destinations {
		td := TraceDestination{Name: d.Name}

		switch {
		case !d.IsEnabled():
			td.Why = "this destination is disabled"
		case d.Filter == "":
			td.Would = true
			td.Why = "no filter on this destination, so it takes every accepted message"
		default:
			e := d.FilterExpr()
			if e == nil {
				td.Why = "this destination has a filter that could not be evaluated"
				break
			}
			td.Reads = readPaths(outMsg, e.Paths())
			ok, err := e.Eval(outMsg)
			switch {
			case err != nil:
				td.Why = "the destination filter could not be evaluated: " + err.Error()
			case ok:
				td.Would = true
				td.Why = "the destination filter matched"
			default:
				td.Why = "the destination filter excluded it"
			}
		}

		tr.Destinations = append(tr.Destinations, td)
	}

	if cfg.Scripts != nil {
		// Stated rather than silently omitted. A script can change anything, so a trace that
		// showed only the declarative steps would be describing part of the pipeline as though
		// it were all of it - and somebody would conclude a field was untouched when a script
		// had rewritten it.
		tr.Caveat = "This channel also runs JavaScript, which is not shown above. A script can " +
			"read and change any part of a message, so the output shown here is the result of " +
			"the declarative steps only."
	}

	return tr, nil
}

// TraceStoredMessage traces a message that has already been through the channel.
//
// The distinction from TraceMessage is the whole reason this exists, and it was not stated anywhere: a trace runs the channel
// as it is configured now. For a message that arrived five minutes ago that is the same thing. For one that arrived last week,
// on a channel somebody has edited since, it is not - the trace explains what would happen to that message today, not what did
// happen to it then.
//
// Nothing said so. Somebody investigating why a message was delivered, on a channel whose filter was tightened yesterday,
// would be shown a trace saying it would be filtered - and would reasonably conclude the delivery record was wrong.
//
// So this takes the time the message arrived and says plainly whether the channel has changed since.
func TraceStoredMessage(ctx context.Context, cfg *config.Channel, raw []byte, arrived time.Time, changedAt time.Time) (*Trace, error) {
	tr, err := TraceMessage(ctx, cfg, raw)
	if err != nil {
		return nil, err
	}

	// Prepended rather than appended, and prepended to whatever caveat is already there, because this one changes
	// how the whole trace should be read. A reader who takes the steps at face value and finds the note afterwards
	// has already formed a conclusion.
	if !changedAt.IsZero() && changedAt.After(arrived) {
		note := fmt.Sprintf("This channel was changed on %s, after this message arrived on %s. "+
			"What follows is what this channel would do with the message today, not a record of what it "+
			"did at the time.",
			changedAt.Format("2 January 2006 at 15:04"), arrived.Format("2 January 2006 at 15:04"))

		if tr.Caveat != "" {
			tr.Caveat = note + " " + tr.Caveat
		} else {
			tr.Caveat = note
		}

		tr.Stale = true
	}

	return tr, nil
}

// readPaths resolves each path and records what was there.
//
// Values are shown, and that is deliberate: this is a debugging tool operating on a message
// somebody already has permission to read, and a trace that hid the values would answer no
// question at all. It is gated on the same role as reading a stored message.
func readPaths(msg *hl7.Message, paths []string) []TraceValue {
	out := make([]TraceValue, 0, len(paths))

	for _, raw := range paths {
		p, err := hl7.ParsePath(raw)
		if err != nil {
			continue
		}
		v := msg.ValueAt(p)
		out = append(out, TraceValue{
			Path: raw,
			// An empty field and an absent one are different things, and filters turn on the
			// difference while people routinely conflate them.
			Value:   v.String(),
			Present: v.Exists(),
		})
	}
	return out
}

func joinSegments(in []string) string {
	// Bounded: a message with two hundred OBX segments would otherwise produce a line nobody
	// reads, and the first dozen already say what shape it is.
	const max = 12
	if len(in) <= max {
		return joinComma(in)
	}
	return joinComma(in[:max]) + fmt.Sprintf(" and %d more", len(in)-max)
}

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
