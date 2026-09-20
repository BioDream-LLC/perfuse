package transform

import (
	"fmt"
	"sort"
	"strings"
)

// Describing a step in a sentence.
//
// This exists so a channel can be read by somebody who did not write it, which is the whole
// argument for having a declarative layer at all. A step that can only be understood by
// reading YAML is not much better than a step that can only be understood by reading
// JavaScript.
//
// It is also what the graphical builder shows back to the user as confirmation. Somebody who
// has just filled in a form does not necessarily know they built what they meant to, and a
// sentence to check against their intention is the cheapest way to find out.
//
// The author's own description always wins. Somebody who took the trouble to explain why a
// step exists knows something the fields cannot express - "strip the leading zeroes Epic
// adds" says more than "replace ^0+ with nothing in PID-3.1".

// Describe renders the step as a sentence.
func (s Step) Describe() string {
	body := s.describeAction()

	if s.Description != "" {
		// Both, not just the author's text: the description says why, the action says
		// what, and a reviewer checking one against the other is the point.
		body = s.Description + " (" + body + ")"
	}

	if s.When != "" {
		body += " when " + s.When
	}
	return body
}

// describeAction renders whichever action the step carries.
func (s Step) describeAction() string {
	switch {
	case s.Set != nil:
		if s.Set.Value == "" {
			// Distinguished from clear on purpose: setting an empty value writes an
			// empty field, which is not the same as removing one, and a channel that
			// confuses the two is hard to debug.
			return fmt.Sprintf("set %s to an empty value", s.Set.Path)
		}
		return fmt.Sprintf("set %s to %q", s.Set.Path, s.Set.Value)

	case s.Copy != nil:
		out := fmt.Sprintf("copy %s to %s", s.Copy.From, s.Copy.To)
		if s.Copy.Default != "" {
			out += fmt.Sprintf(", or %q when it is empty", s.Copy.Default)
		}
		return out

	case s.Clear != nil:
		return fmt.Sprintf("clear the value of %s", s.Clear.Path)

	case s.Remove != nil:
		return fmt.Sprintf("remove %s entirely", s.Remove.Path)

	case s.Map != nil:
		return s.describeMap()

	case s.Replace != nil:
		scope := "the first match of"
		if s.Replace.All {
			scope = "every match of"
		}
		return fmt.Sprintf("in %s, replace %s %q with %q",
			s.Replace.Path, scope, s.Replace.Pattern, s.Replace.With)

	case s.Pad != nil:
		side := "the left"
		if s.Pad.Right {
			side = "the right"
		}
		with := s.Pad.With
		if with == "" {
			with = " "
		}
		out := fmt.Sprintf("pad %s on %s with %q to %d characters",
			s.Pad.Path, side, with, s.Pad.Width)
		if s.Pad.Truncate {
			out += ", cutting anything longer"
		} else {
			out += ", leaving anything longer alone"
		}
		return out

	case s.Date != nil:
		out := fmt.Sprintf("reformat the date in %s from %s to %s",
			s.Date.Path, s.Date.From, s.Date.To)
		if s.Date.OnError != "" {
			out += fmt.Sprintf(", and on a value that does not parse: %s", s.Date.OnError)
		}
		return out

	case s.Trim != nil:
		return fmt.Sprintf("trim whitespace from %s", s.Trim.Path)

	case s.Case != nil:
		return fmt.Sprintf("change %s to %s case", s.Case.Path, s.Case.To)
	}

	// Unreachable through a validated pipeline, which refuses a step with no action.
	// Reported rather than returned as an empty string, because a blank line in a list
	// of steps looks like a rendering fault and sends somebody looking in the wrong place.
	return "does nothing (no action is set on this step)"
}

// describeMap renders a lookup table without printing all of it.
func (s Step) describeMap() string {
	out := fmt.Sprintf("look up %s in a table of %d value", s.Map.Path, len(s.Map.Table))
	if len(s.Map.Table) != 1 {
		out += "s"
	}

	// A few examples, in a stable order. A map ranges randomly in Go, so without sorting
	// the same channel would describe itself differently on each request and anybody
	// diffing two descriptions would see phantom changes.
	if len(s.Map.Table) > 0 {
		keys := make([]string, 0, len(s.Map.Table))
		for k := range s.Map.Table {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		const show = 3
		parts := make([]string, 0, show)
		for i, k := range keys {
			if i == show {
				break
			}
			parts = append(parts, fmt.Sprintf("%s to %s", k, s.Map.Table[k]))
		}
		out += " (" + strings.Join(parts, ", ")
		if len(keys) > show {
			out += fmt.Sprintf(", and %d more", len(keys)-show)
		}
		out += ")"
	}

	switch {
	case s.Map.Strict:
		out += "; a value that is not in the table is an error"
	case s.Map.Default != "":
		out += fmt.Sprintf("; anything else becomes %q", s.Map.Default)
	default:
		out += "; anything else is left as it is"
	}
	return out
}
