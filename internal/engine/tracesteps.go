package engine

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// traceSteps runs a channel's declarative steps one at a time, keeping the message after each.
//
// # Why one at a time
//
// The stage above already reports every change and every skip for the pipeline as a whole. That answers what changed. It
// does not answer which of fourteen steps changed it, and it cannot show the message as it stood immediately before the
// step that broke something - which is the thing somebody actually wants to look at.
//
// Each step is compiled on its own and applied to the running tree, so the snapshot after step three is the real
// intermediate message rather than a reconstruction.
//
// # Why this is possible here
//
// Declarative steps. Because a transformation is a list of described operations rather than arbitrary JavaScript, a step
// can be applied in isolation and the result rendered. There is nothing to instrument and no interpreter to pause. The
// engine this project replaces cannot do this at all: its transformers are scripts, and stepping through one means a
// debugger that practitioners report has to be coaxed into starting.
//
// # What it does not cover
//
// Scripts. A JavaScript transformer runs after these steps and is not entered, which is reported as a caveat rather than
// omitted - a trace that quietly ignored it would have somebody conclude a field survived processing when it was the
// script that changed it, and they would then look for the bug in the wrong place.
func traceSteps(cfg *config.Channel, raw []byte) ([]TraceStep, error) {
	pipeline := cfg.Pipeline()
	if pipeline == nil || pipeline.Len() == 0 {
		return nil, nil
	}
	steps := pipeline.Steps()

	root, err := hl7xml.FromRaw(raw)
	if err != nil {
		return nil, fmt.Errorf("reading the message into a tree: %w", err)
	}

	snapshot := func() string {
		out, err := hl7xml.ToER7(root, hl7xml.DefaultOptions())
		if err != nil {
			return "(the message could not be rendered: " + err.Error() + ")"
		}
		return string(out)
	}

	out := make([]TraceStep, 0, len(steps))

	for i, step := range steps {
		entry := TraceStep{
			Number: i + 1,
			Label:  stepLabel(step, i),
			Path:   step.Path(),
		}

		changes, skip, err := pipeline.ApplyStep(i, root)
		entry.Message = snapshot()

		entry.Changes = changes

		switch {
		case err != nil:
			entry.Outcome = "failed"
			entry.Detail = err.Error()
			out = append(out, entry)
			return out, nil

		case len(changes) > 0:
			entry.Outcome = "changed"
			entry.Detail = describeStepChanges(changes)

		case skip != nil:
			// A false condition and an absent field look identical from outside and mean completely different things.
			// One is the step working exactly as written; the other is almost always the bug.
			if strings.Contains(skip.Why, "condition") {
				entry.Outcome = "skipped"
			} else {
				entry.Outcome = "no-effect"
			}
			entry.Detail = skip.Why

		default:
			entry.Outcome = "no-effect"
			entry.Detail = "ran and changed nothing, and did not say why"
		}

		out = append(out, entry)
	}

	return out, nil
}

func describeStepChanges(changes []transform.Change) string {
	if len(changes) == 1 {
		c := changes[0]
		switch {
		case c.From == "":
			return fmt.Sprintf("set %s to %q, which was empty before", c.Path, c.To)
		case c.To == "":
			return fmt.Sprintf("cleared %s, which held %q", c.Path, c.From)
		default:
			return fmt.Sprintf("changed %s from %q to %q", c.Path, c.From, c.To)
		}
	}
	return fmt.Sprintf("changed %d fields", len(changes))
}

// stepLabel names a step for a reader.
//
// Falls through to the step's own description of what it does rather than to "step 3", because unnamed steps are the
// common case and a trace of "step 1, step 2, step 3" is no better than counting them by hand.
func stepLabel(s transform.Step, i int) string {
	if s.Description != "" {
		return s.Description
	}
	if d := s.Describe(); d != "" {
		return d
	}
	return fmt.Sprintf("step %d", i+1)
}
