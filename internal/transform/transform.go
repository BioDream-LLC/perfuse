// Package transform applies declarative changes to a message.
//
// This is the layer meant to carry the ordinary work, with scripting kept as a
// marked escape hatch. The distinction is not stylistic. A step declared in YAML
// can be validated before it runs, shown in a diff, reviewed by somebody who does
// not read JavaScript, and reported on; a script can only be executed and hoped
// about. Most of what Mirth transformers actually do is boring - copy a field,
// pad an identifier, map a code through a table, clear something that should not
// leave the building - and all of that belongs here.
//
// Every step names its target explicitly and every step is reversible in the
// sense that it says what it did, because the most expensive failure in an
// interface is a transformation nobody knew was happening.
package transform

import (
	"fmt"
	"github.com/biodream-llc/perfuse/internal/codeset"
	"regexp"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Step is one declarative change.
//
// Exactly one action field may be set. The alternative - a single "action" string
// plus a bag of arguments - moves the error from configuration load to message
// handling, and an interface that only discovers a broken step when a patient is
// admitted is worse than one that refuses to start.
type Step struct {
	// Description is free text explaining why this step exists. It is optional
	// but strongly encouraged, and the interface shows it.
	Description string `yaml:"description,omitempty"`

	// When is an optional condition. The step is skipped unless it holds.
	When string `yaml:"when,omitempty"`

	// Set writes a literal value.
	Set *SetStep `yaml:"set,omitempty"`
	// Copy moves a value from one path to another.
	Copy *CopyStep `yaml:"copy,omitempty"`
	// Clear empties a field.
	Clear *ClearStep `yaml:"clear,omitempty"`
	// Remove deletes a segment or element outright.
	Remove *RemoveStep `yaml:"remove,omitempty"`
	// Map translates a value through a lookup table.
	Map *MapStep `yaml:"map,omitempty"`
	// Replace performs a regular-expression substitution.
	Replace *ReplaceStep `yaml:"replace,omitempty"`
	// Pad left- or right-pads a value to a fixed width.
	Pad *PadStep `yaml:"pad,omitempty"`
	// Date reformats a timestamp.
	Date *DateStep `yaml:"date,omitempty"`
	// Trim removes surrounding whitespace.
	Trim *TrimStep `yaml:"trim,omitempty"`
	// Case changes a value to upper or lower case.
	Case *CaseStep `yaml:"case,omitempty"`

	compiledWhen *condition
	compiledFrom *regexp.Regexp
}

// SetStep writes a value to a path, creating it if absent.
type SetStep struct {
	Path  string `yaml:"path"`
	Value string `yaml:"value"`
}

// CopyStep copies one path to another.
type CopyStep struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	// Default is used when the source is empty. Without it, copying an absent
	// value clears the target, which is occasionally wanted and usually not.
	Default string `yaml:"default,omitempty"`
}

// ClearStep empties a field but leaves it present.
type ClearStep struct {
	Path string `yaml:"path"`
}

// RemoveStep deletes an element entirely.
type RemoveStep struct {
	Path string `yaml:"path"`
}

// MapStep translates a value through a table.
type MapStep struct {
	Path  string            `yaml:"path"`
	Table map[string]string `yaml:"table"`

	// Use names a shared table instead of writing one inline.
	//
	// The reason to have both: an inline table is right for a mapping only this channel will ever need, and a
	// shared one is right for the sex codes that forty channels translate identically. Forcing everything into
	// a shared file would make a one-off mapping a two-file change; forcing everything inline is what makes a
	// site maintain forty copies of the same table.
	//
	// A shared table also carries provenance - who decided, when, and why each row exists - which an inline
	// map cannot, because a YAML map has nowhere to put a comment that survives being rewritten by the form.
	Use string `yaml:"use,omitempty"`

	// resolved is the shared table, filled in at compile time when Use names one.
	resolved *codeset.Table
	// Default applies when the value is not in the table. When it is empty and
	// Strict is false the original value is kept, which is the safe choice: an
	// unmapped local code preserved is recoverable, an unmapped code silently
	// blanked is not.
	Default string `yaml:"default,omitempty"`
	// Strict makes an unmapped value an error rather than a pass-through. Worth
	// turning on for a field the receiver validates against a closed set.
	Strict bool `yaml:"strict,omitempty"`
}

// ReplaceStep performs a regular-expression substitution.
type ReplaceStep struct {
	Path string `yaml:"path"`
	// Pattern is a Go regular expression, which is RE2: no backtracking, so a
	// pattern cannot take exponential time on a hostile input.
	Pattern string `yaml:"pattern"`
	With    string `yaml:"with"`
	// All replaces every occurrence rather than the first.
	All bool `yaml:"all,omitempty"`
}

// PadStep pads a value to a width.
type PadStep struct {
	Path  string `yaml:"path"`
	Width int    `yaml:"width"`
	With  string `yaml:"with,omitempty"`
	// Right pads on the right instead of the left.
	Right bool `yaml:"right,omitempty"`
	// Truncate shortens a value that is already longer than Width. Off by
	// default, because truncating an identifier produces one that matches the
	// wrong patient.
	Truncate bool `yaml:"truncate,omitempty"`
}

// DateStep reformats a timestamp between two layouts.
type DateStep struct {
	Path string `yaml:"path"`
	// From and To are Java SimpleDateFormat patterns, the same notation the
	// scripts use, so that one channel does not need two date vocabularies.
	From string `yaml:"from"`
	To   string `yaml:"to"`
	// OnError chooses what happens when the value does not match From:
	// "keep" leaves it alone, "clear" empties it, "fail" stops the message.
	// The default is "fail", because a timestamp that silently did not convert
	// is accepted downstream and then misread.
	OnError string `yaml:"on_error,omitempty"`
}

// TrimStep removes surrounding whitespace.
type TrimStep struct {
	Path string `yaml:"path"`
}

// CaseStep changes letter case.
type CaseStep struct {
	Path string `yaml:"path"`
	// To is "upper" or "lower".
	To string `yaml:"to"`
}

// Change records what a step did, so the interface can show a diff and the
// message store can keep an account of how a message was altered.
type Change struct {
	Step string `json:"step"`
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
	// Note explains anything surprising, such as a value left unmapped.
	Note string `json:"note,omitempty"`

	// Deliberate marks a step that ran, left the value unchanged, and meant to.
	//
	// Needed because "the value did not change" otherwise means two opposite things. A date conversion with
	// on_error: keep did its job and is reporting the result; a copy from a field the message does not carry did
	// nothing and is the commonest silent bug there is. Without this flag the second was being counted as a change,
	// which told somebody their step worked.
	//
	// A flag rather than an inspection of Note, because a copy always sets a note to record where the value came
	// from, so the presence of a note says nothing about whether anything happened.
	Deliberate bool `json:"deliberate,omitempty"`
}

// Pipeline is a validated sequence of steps.
type Pipeline struct {
	steps []Step
}

// Compile validates steps and prepares them for use.
//
// Everything that can fail is made to fail here rather than per message: a bad
// regular expression, an unknown date pattern, a step with two actions or none.
func Compile(steps []Step) (*Pipeline, error) {
	return CompileWith(steps, nil)
}

// CompileWith compiles steps that may reference shared mapping tables.
//
// A separate entry point rather than changing Compile's signature, because the overwhelming majority of callers
// have no shared tables and should not have to pass nil. A step naming a table that the set does not contain is
// an error here rather than at run time: a mapping that silently does nothing produces messages that look
// plausible and are wrong, which is the worst failure mode available to a transformation.
func CompileWith(steps []Step, tables *codeset.Set) (*Pipeline, error) {
	out := make([]Step, len(steps))

	for i := range steps {
		step := steps[i]
		label := fmt.Sprintf("step %d", i+1)
		if step.Description != "" {
			label = fmt.Sprintf("step %d (%s)", i+1, step.Description)
		}

		if step.Map != nil && strings.TrimSpace(step.Map.Use) != "" {
			if len(step.Map.Table) > 0 {
				return nil, fmt.Errorf("%s: a map step has both an inline table and a reference to the "+
					"shared table %q; one of them would be ignored", label, step.Map.Use)
			}

			table, ok := tables.Table(step.Map.Use)
			if !ok {
				available := ""
				if names := tables.Names(); len(names) > 0 {
					available = "; the tables loaded are " + strings.Join(names, ", ")
				}
				return nil, fmt.Errorf("%s: no shared mapping table called %q%s",
					label, step.Map.Use, available)
			}
			step.Map.resolved = table
		}

		set := step.actions()
		if len(set) == 0 {
			return nil, fmt.Errorf("%s: no action; expected one of set, copy, clear, remove, map, replace, pad, date, trim, case", label)
		}
		if len(set) > 1 {
			return nil, fmt.Errorf("%s: %d actions (%s); a step does exactly one thing so the order is never in doubt",
				label, len(set), strings.Join(set, ", "))
		}

		if step.When != "" {
			cond, err := parseCondition(step.When)
			if err != nil {
				return nil, fmt.Errorf("%s: when: %w", label, err)
			}
			step.compiledWhen = cond
		}

		if err := step.validate(label); err != nil {
			return nil, err
		}
		out[i] = step
	}

	return &Pipeline{steps: out}, nil
}

func (s *Step) actions() []string {
	var names []string
	if s.Set != nil {
		names = append(names, "set")
	}
	if s.Copy != nil {
		names = append(names, "copy")
	}
	if s.Clear != nil {
		names = append(names, "clear")
	}
	if s.Remove != nil {
		names = append(names, "remove")
	}
	if s.Map != nil {
		names = append(names, "map")
	}
	if s.Replace != nil {
		names = append(names, "replace")
	}
	if s.Pad != nil {
		names = append(names, "pad")
	}
	if s.Date != nil {
		names = append(names, "date")
	}
	if s.Trim != nil {
		names = append(names, "trim")
	}
	if s.Case != nil {
		names = append(names, "case")
	}
	return names
}

func (s *Step) validate(label string) error {
	switch {
	case s.Set != nil:
		return requirePath(label, "set.path", s.Set.Path)

	case s.Copy != nil:
		if err := requirePath(label, "copy.from", s.Copy.From); err != nil {
			return err
		}
		return requirePath(label, "copy.to", s.Copy.To)

	case s.Clear != nil:
		return requirePath(label, "clear.path", s.Clear.Path)

	case s.Remove != nil:
		return requirePath(label, "remove.path", s.Remove.Path)

	case s.Map != nil:
		if err := requirePath(label, "map.path", s.Map.Path); err != nil {
			return err
		}
		// One or the other, and at least one. A step with neither would do nothing while looking configured,
		// which is the failure this whole codebase refuses.
		if len(s.Map.Table) == 0 && strings.TrimSpace(s.Map.Use) == "" {
			return fmt.Errorf("%s: map has neither an inline table nor a reference to a shared one, so the "+
				"step would do nothing", label)
		}
		if s.Map.Strict && s.Map.Default != "" {
			return fmt.Errorf("%s: map is strict and also has a default; strict means an unmapped value is an error, so the default could never apply", label)
		}
		return nil

	case s.Replace != nil:
		if err := requirePath(label, "replace.path", s.Replace.Path); err != nil {
			return err
		}
		if s.Replace.Pattern == "" {
			return fmt.Errorf("%s: replace.pattern is empty", label)
		}
		re, err := regexp.Compile(s.Replace.Pattern)
		if err != nil {
			return fmt.Errorf("%s: replace.pattern is not a valid expression: %w", label, err)
		}
		s.compiledFrom = re
		return nil

	case s.Pad != nil:
		if err := requirePath(label, "pad.path", s.Pad.Path); err != nil {
			return err
		}
		if s.Pad.Width <= 0 {
			return fmt.Errorf("%s: pad.width must be positive", label)
		}
		if s.Pad.Width > 512 {
			return fmt.Errorf("%s: pad.width of %d is implausible for an HL7 field", label, s.Pad.Width)
		}
		if s.Pad.With != "" && len(s.Pad.With) != 1 {
			return fmt.Errorf("%s: pad.with must be a single character, got %q", label, s.Pad.With)
		}
		return nil

	case s.Date != nil:
		if err := requirePath(label, "date.path", s.Date.Path); err != nil {
			return err
		}
		if s.Date.From == "" || s.Date.To == "" {
			return fmt.Errorf("%s: date needs both from and to patterns", label)
		}
		if _, err := javaLayout(s.Date.From); err != nil {
			return fmt.Errorf("%s: date.from: %w", label, err)
		}
		if _, err := javaLayout(s.Date.To); err != nil {
			return fmt.Errorf("%s: date.to: %w", label, err)
		}
		switch s.Date.OnError {
		case "", "fail", "keep", "clear":
		default:
			return fmt.Errorf("%s: date.on_error is %q; expected fail, keep or clear", label, s.Date.OnError)
		}
		return nil

	case s.Trim != nil:
		return requirePath(label, "trim.path", s.Trim.Path)

	case s.Case != nil:
		if err := requirePath(label, "case.path", s.Case.Path); err != nil {
			return err
		}
		switch strings.ToLower(s.Case.To) {
		case "upper", "lower":
			return nil
		default:
			return fmt.Errorf("%s: case.to is %q; expected upper or lower", label, s.Case.To)
		}
	}
	return nil
}

func requirePath(label, field, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s: %s is required", label, field)
	}
	if _, err := ParsePath(path); err != nil {
		return fmt.Errorf("%s: %s: %w", label, field, err)
	}
	return nil
}

// Len reports how many steps the pipeline has.
func (p *Pipeline) Len() int {
	if p == nil {
		return 0
	}
	return len(p.steps)
}

// Steps exposes the compiled steps for display.
func (p *Pipeline) Steps() []Step {
	if p == nil {
		return nil
	}
	return p.steps
}

// Skip is a step that ran and changed nothing, with the reason.
//
// The most useful thing a trace can carry, and the thing no existing output reported. The commonest fault in a working
// interface is a step addressing a field the sender does not populate: nothing errors, the message goes on unchanged, and the
// only evidence is that the count of changes is lower than somebody expected - which requires them to have an expectation.
type Skip struct {
	// Step is the step's description, or its kind when it has none.
	Step string `json:"step"`

	// Path is what it addressed.
	Path string `json:"path,omitempty"`

	// Why says what happened.
	Why string `json:"why"`
}

// Apply runs every step against a message tree, returning what changed.
func (p *Pipeline) Apply(root *xtree.Node) ([]Change, error) {
	changes, _, err := p.ApplyTraced(root)

	return changes, err
}

// ApplyTraced runs every step and also reports the ones that changed nothing.
//
// A separate entry point rather than a wider return on Apply, because almost every caller does not want the skips and the
// ones that do are asking a different question. Apply delegates here, so there is one implementation and the traced and
// untraced paths cannot diverge - which would be the worst possible outcome, since the trace would then describe a run that
// did not happen.
// ApplyStep runs a single step from an already-compiled pipeline.
//
// Exists so a trace can apply steps one at a time and keep the message between them. Taking the step from the compiled
// pipeline rather than recompiling it matters for two reasons: recompiling needs the mapping tables passed in again, and
// a separately compiled copy could differ from what runs. A trace that disagrees with production is worse than none.
//
// The returned Skip explains a step that ran and did nothing, which is the outcome the caller most needs.
func (p *Pipeline) ApplyStep(i int, root *xtree.Node) (changes []Change, skip *Skip, err error) {
	if p == nil || i < 0 || i >= len(p.steps) {
		return nil, nil, fmt.Errorf("there is no step %d in this pipeline", i+1)
	}

	step := &p.steps[i]

	label := step.Description
	if label == "" {
		label = step.kind()
	}

	if step.compiledWhen != nil {
		holds, err := step.compiledWhen.eval(root)
		if err != nil {
			return nil, nil, fmt.Errorf("when: %w", err)
		}
		if !holds {
			return nil, &Skip{
				Step: label,
				Path: step.path(),
				Why:  "its condition was false for this message: " + step.When,
			}, nil
		}
	}

	change, err := step.apply(root)
	if err != nil {
		return nil, nil, err
	}
	if change == nil {
		return nil, &Skip{
			Step: label,
			Path: step.path(),
			Why:  "it addressed a field this message does not carry, so nothing was changed",
		}, nil
	}
	if step.Description != "" {
		change.Step = step.Description
	}

	// A change that changed nothing is a skip, not a change.
	//
	// A copy from a field the message does not carry produces a change from "" to "", and counting it made the trace
	// report "3 change(s) were made" when two fields changed. Found by a test asserting the per-step and stage-level
	// views agree - they did not, and the stage-level total was the wrong one.
	//
	// It matters beyond a count. Somebody reading that a step made a change concludes the step works, and the
	// commonest reason a step silently does nothing is exactly this: the field it reads from is absent.
	//
	// Unless the step marked it deliberate. A date conversion with on_error: keep leaves the value alone on purpose
	// and reports why, which is a real result rather than a step that found nothing.
	if change.From == change.To && !change.Deliberate {
		return nil, &Skip{
			Step: label,
			Path: change.Path,
			Why: "it ran and changed nothing: " + change.Path + " was already " +
				describeValue(change.From) + ", which usually means the field it reads from is not in this message",
		}, nil
	}

	return []Change{*change}, nil, nil
}

func (p *Pipeline) ApplyTraced(root *xtree.Node) ([]Change, []Skip, error) {
	if p == nil || len(p.steps) == 0 {
		return nil, nil, nil
	}

	var (
		changes []Change
		skips   []Skip
	)

	// Delegates to ApplyStep rather than repeating it.
	//
	// The two used to be separate implementations of the same thing, and they disagreed: a no-op change counted as a
	// change here and as nothing there. Whichever is right, having two answers is worse, and a trace that
	// contradicts the stage it summarises leaves somebody deciding which half to believe.
	for i := range p.steps {
		stepChanges, skip, err := p.ApplyStep(i, root)
		if err != nil {
			return changes, skips, fmt.Errorf("step %d: %w", i+1, err)
		}
		if skip != nil {
			skips = append(skips, *skip)
			continue
		}
		changes = append(changes, stepChanges...)
	}

	return changes, skips, nil
}

func describeValue(s string) string {
	if s == "" {
		return "empty"
	}
	return fmt.Sprintf("%q", s)
}

// kind names a step's action, for a trace where the author wrote no description.
func (s *Step) kind() string {
	switch {
	case s.Set != nil:
		return "set"
	case s.Copy != nil:
		return "copy"
	case s.Clear != nil:
		return "clear"
	case s.Remove != nil:
		return "remove"
	case s.Map != nil:
		return "map"
	case s.Replace != nil:
		return "replace"
	case s.Pad != nil:
		return "pad"
	case s.Date != nil:
		return "date"
	case s.Trim != nil:
		return "trim"
	case s.Case != nil:
		return "case"
	}

	return "step"
}

// path names what a step addresses, for a trace.
//
// A copy reports its destination, because that is the field somebody is looking for when they ask why it is empty.
// Path is the field this step addresses, or empty when it addresses none.
//
// Exported because a trace has to name it. The path is the single most useful thing about a step that did nothing:
// almost always the field is absent from the message, and the path is what somebody compares against a real one.
func (s Step) Path() string { return s.path() }

func (s *Step) path() string {
	switch {
	case s.Set != nil:
		return s.Set.Path
	case s.Copy != nil:
		return s.Copy.To
	case s.Clear != nil:
		return s.Clear.Path
	case s.Remove != nil:
		return s.Remove.Path
	case s.Map != nil:
		return s.Map.Path
	case s.Replace != nil:
		return s.Replace.Path
	case s.Pad != nil:
		return s.Pad.Path
	case s.Date != nil:
		return s.Date.Path
	case s.Trim != nil:
		return s.Trim.Path
	case s.Case != nil:
		return s.Case.Path
	}

	return ""
}

func (s *Step) apply(root *xtree.Node) (*Change, error) {
	switch {
	case s.Set != nil:
		return writeValue(root, s.Set.Path, "set", func(string) (string, string, error) {
			return s.Set.Value, "", nil
		})

	case s.Copy != nil:
		source, _ := ReadPath(root, s.Copy.From)
		if source == "" && s.Copy.Default != "" {
			source = s.Copy.Default
		}
		return writeValue(root, s.Copy.To, "copy", func(string) (string, string, error) {
			return source, "from " + s.Copy.From, nil
		})

	case s.Clear != nil:
		return writeValue(root, s.Clear.Path, "clear", func(string) (string, string, error) {
			return "", "", nil
		})

	case s.Remove != nil:
		path, err := ParsePath(s.Remove.Path)
		if err != nil {
			return nil, err
		}
		node := path.resolve(root, false)
		if node == nil {
			return nil, nil
		}
		before := node.Value()
		parent := node.Parent()
		if parent == nil {
			return nil, fmt.Errorf("cannot remove the root element")
		}
		parent.Remove(node)
		return &Change{Step: "remove", Path: s.Remove.Path, From: before, To: "(removed)"}, nil

	case s.Map != nil:
		return writeValue(root, s.Map.Path, "map", func(current string) (string, string, error) {
			// A shared table carries its own default and strictness, and the reason each row exists. The note
			// is what makes the change explainable rather than merely visible: "M became 1" is a fact, and
			// "because the lab's interface predates the HL7 table" is why.
			if s.Map.resolved != nil {
				mapped, entry, ok := s.Map.resolved.Lookup(current)
				switch {
				case ok:
					note := ""
					if entry.Why != "" {
						note = entry.Why
					}
					return mapped, note, nil
				case s.Map.resolved.Strict:
					return "", "", fmt.Errorf("value %q is not in the %s table, which is strict: %s",
						current, s.Map.resolved.Name, s.Map.resolved.Describes)
				case s.Map.resolved.Default != "":
					return mapped, "not in the " + s.Map.resolved.Name + " table, used its default", nil
				default:
					return mapped, "not in the " + s.Map.resolved.Name + " table, left as it was", nil
				}
			}

			if mapped, ok := s.Map.Table[current]; ok {
				return mapped, "", nil
			}
			if s.Map.Strict {
				return "", "", fmt.Errorf("value %q is not in the table and the step is strict", current)
			}
			if s.Map.Default != "" {
				return s.Map.Default, "not in the table, used the default", nil
			}
			// Keeping the original is the safe outcome. An unmapped local code
			// preserved can be corrected later; one silently blanked cannot.
			return current, "not in the table, left as it was", nil
		})

	case s.Replace != nil:
		return writeValue(root, s.Replace.Path, "replace", func(current string) (string, string, error) {
			if s.Replace.All {
				return s.compiledFrom.ReplaceAllString(current, s.Replace.With), "", nil
			}
			done := false
			return s.compiledFrom.ReplaceAllStringFunc(current, func(m string) string {
				if done {
					return m
				}
				done = true
				return s.compiledFrom.ReplaceAllString(m, s.Replace.With)
			}), "", nil
		})

	case s.Pad != nil:
		fill := s.Pad.With
		if fill == "" {
			fill = "0"
		}
		return writeValue(root, s.Pad.Path, "pad", func(current string) (string, string, error) {
			if current == "" {
				// Padding an empty field would invent an identifier made of
				// zeroes, which downstream cannot distinguish from a real one.
				return "", "", nil
			}
			if len(current) >= s.Pad.Width {
				if s.Pad.Truncate {
					return current[:s.Pad.Width], "truncated", nil
				}
				return current, "", nil
			}
			padding := strings.Repeat(fill, s.Pad.Width-len(current))
			if s.Pad.Right {
				return current + padding, "", nil
			}
			return padding + current, "", nil
		})

	case s.Date != nil:
		// keptDeliberately records that on_error: keep left the value alone on purpose, so a no-op change here is a
		// real reported outcome rather than a step that found nothing.
		var keptDeliberately bool
		change, err := writeValue(root, s.Date.Path, "date", func(current string) (string, string, error) {
			if current == "" {
				return "", "", nil
			}
			converted, err := convertDate(current, s.Date.From, s.Date.To)
			if err == nil {
				return converted, "", nil
			}
			switch s.Date.OnError {
			case "keep":
				keptDeliberately = true
				return current, "did not match " + s.Date.From + ", left as it was", nil
			case "clear":
				return "", "did not match " + s.Date.From + ", cleared", nil
			default:
				return "", "", err
			}
		})
		if change != nil {
			change.Deliberate = keptDeliberately
		}
		return change, err

	case s.Trim != nil:
		return writeValue(root, s.Trim.Path, "trim", func(current string) (string, string, error) {
			return strings.TrimSpace(current), "", nil
		})

	case s.Case != nil:
		upper := strings.EqualFold(s.Case.To, "upper")
		return writeValue(root, s.Case.Path, "case", func(current string) (string, string, error) {
			if upper {
				return strings.ToUpper(current), "", nil
			}
			return strings.ToLower(current), "", nil
		})
	}
	return nil, nil
}

// writeValue reads a path, computes a new value, and writes it back, reporting
// the change only when something actually differed.
func writeValue(root *xtree.Node, rawPath, name string, compute func(current string) (string, string, error)) (*Change, error) {
	path, err := ParsePath(rawPath)
	if err != nil {
		return nil, err
	}

	// A path that addresses every repetition changes every repetition, matching
	// the filter language, where a path naming no repetition means all of them.
	targets := path.resolveAll(root)
	if len(targets) == 0 {
		created := path.resolve(root, true)
		if created == nil {
			return nil, fmt.Errorf("%s: cannot create %s", name, rawPath)
		}
		targets = []*xtree.Node{created}
	}

	var change *Change
	for _, node := range targets {
		before := node.Value()
		after, note, err := compute(before)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", name, rawPath, err)
		}
		if after == before && note == "" {
			continue
		}
		node.SetText(after)
		if change == nil {
			change = &Change{Step: name, Path: rawPath, From: before, To: after, Note: note}
		}
	}
	return change, nil
}

// convertDate reformats a timestamp using Java patterns.
func convertDate(text, from, to string) (string, error) {
	inLayout, err := javaLayout(from)
	if err != nil {
		return "", err
	}
	outLayout, err := javaLayout(to)
	if err != nil {
		return "", err
	}

	trimmed := strings.TrimSpace(text)
	if len(trimmed) > len(inLayout) {
		trimmed = trimmed[:len(inLayout)]
	}

	when, err := time.ParseInLocation(inLayout, trimmed, time.Local)
	if err != nil {
		return "", fmt.Errorf("%q does not match %q", text, from)
	}
	return when.Format(outLayout), nil
}

// javaLayout converts the subset of SimpleDateFormat that appears in HL7 work.
// It is kept here rather than shared with the script package so that the
// declarative layer has no dependency on the scripting layer; the two are
// deliberately independent, and the overlap is a dozen lines.
func javaLayout(pattern string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(pattern); {
		c := pattern[i]
		run := 1
		for i+run < len(pattern) && pattern[i+run] == c {
			run++
		}
		token := pattern[i : i+run]

		switch c {
		case 'y':
			if run <= 2 {
				b.WriteString("06")
			} else {
				b.WriteString("2006")
			}
		case 'M':
			switch {
			case run == 1:
				b.WriteString("1")
			case run == 2:
				b.WriteString("01")
			case run == 3:
				b.WriteString("Jan")
			default:
				b.WriteString("January")
			}
		case 'd':
			if run == 1 {
				b.WriteString("2")
			} else {
				b.WriteString("02")
			}
		case 'H':
			b.WriteString("15")
		case 'h':
			if run == 1 {
				b.WriteString("3")
			} else {
				b.WriteString("03")
			}
		case 'm':
			if run == 1 {
				b.WriteString("4")
			} else {
				b.WriteString("04")
			}
		case 's':
			if run == 1 {
				b.WriteString("5")
			} else {
				b.WriteString("05")
			}
		case 'S':
			b.WriteString("." + strings.Repeat("0", run))
		case 'a':
			b.WriteString("PM")
		case 'E':
			if run <= 3 {
				b.WriteString("Mon")
			} else {
				b.WriteString("Monday")
			}
		case 'z':
			b.WriteString("MST")
		case 'Z':
			b.WriteString("-0700")
		case 'X':
			b.WriteString("-07:00")
		default:
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				return "", fmt.Errorf("pattern letter %q is not supported; use year, month, day, hour, minute and second fields", token)
			}
			b.WriteString(token)
		}
		i += run
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("date pattern is empty")
	}
	return b.String(), nil
}

// ReadPath returns the value at a path, and whether it was present.
func ReadPath(root *xtree.Node, rawPath string) (string, bool) {
	path, err := ParsePath(rawPath)
	if err != nil {
		return "", false
	}
	node := path.resolve(root, false)
	if node == nil {
		return "", false
	}
	return node.Value(), true
}
