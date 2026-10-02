package hl7v3

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Transformations for HL7 v3.
//
// The step names are the same as the v2 ones wherever the meaning is the same - set, copy, clear, remove, map, replace,
// trim, case - so somebody who has written one channel can write the other. That was the whole reason not to invent a
// second vocabulary.
//
// What is different is what v3 requires and v2 has no way to express:
//
//   - A value nearly always belongs in an attribute, so a step that writes element text is almost always wrong. The write
//     side decides this, and the rule is in write.go.
//   - Removing a value has three distinct meanings. Emptying the element, stating why it is empty, and saying the element
//     does not apply are different clinical statements, so clear, nullflavor and remove are three steps rather than one
//     with a mode.
//   - Element order is significant, so where a created element goes matters.
//
// And what is deliberately absent: pad and date. Padding to a fixed width is a fixed-format concern with no meaning in
// XML, and a v3 timestamp is already constrained by its datatype - reformatting one is how a document stops validating.
// Both are refused at load with that reasoning rather than accepted and ignored.

// Step is one declarative change to a v3 document.
//
// Exactly one action per step, checked at load. A step carrying two actions has no defined order between them, and
// guessing one would make the same channel behave differently between releases.
type Step struct {
	// Description is free text explaining why this step exists. Optional, shown in the interface, and worth
	// writing: the next person to read this channel is trying to work out whether the step is still needed.
	Description string `yaml:"description,omitempty"`

	// When is an optional condition in the v3 filter language. The step is skipped unless it holds.
	When string `yaml:"when,omitempty"`

	// Set writes a value, creating the element when the path is anchored.
	Set *V3SetStep `yaml:"set,omitempty"`

	// Copy takes the value at one path and writes it to another.
	Copy *V3CopyStep `yaml:"copy,omitempty"`

	// Clear empties an element without saying why. The weakest of the three removals.
	Clear *V3ClearStep `yaml:"clear,omitempty"`

	// NullFlavor states that no value is available, and why. The construct v2 cannot express.
	NullFlavor *V3NullFlavorStep `yaml:"nullflavor,omitempty"`

	// Remove deletes the element outright, meaning it does not apply.
	Remove *V3RemoveStep `yaml:"remove,omitempty"`

	// Map translates a value through a lookup table.
	Map *V3MapStep `yaml:"map,omitempty"`

	// Replace performs a regular-expression substitution on a value.
	Replace *V3ReplaceStep `yaml:"replace,omitempty"`

	// Trim removes surrounding whitespace.
	Trim *V3TrimStep `yaml:"trim,omitempty"`

	// Case changes a value to upper or lower case.
	Case *V3CaseStep `yaml:"case,omitempty"`

	compiledWhen  *Filter
	compiledFrom  *regexp.Regexp
	compiledPath  map[string]Path
	compiledTable *codeset.Table
}

// V3SetStep writes a literal value.
type V3SetStep struct {
	Path  string `yaml:"path"`
	Value string `yaml:"value"`
}

// V3CopyStep copies a value between paths.
type V3CopyStep struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// V3ClearStep empties an element.
type V3ClearStep struct {
	Path string `yaml:"path"`
}

// V3NullFlavorStep records a stated reason no value is available.
type V3NullFlavorStep struct {
	Path string `yaml:"path"`

	// Reason is a NullFlavor code. Validated at load against the writable set, because a typo here does not fail:
	// it writes a code no receiver recognises, which most treat as "no information" - so a step meaning "we asked
	// and they did not know" quietly means "nothing is known", and the difference is whether anybody asks again.
	Reason string `yaml:"reason"`
}

// V3RemoveStep deletes an element.
type V3RemoveStep struct {
	Path string `yaml:"path"`
}

// V3MapStep translates a value through a lookup table.
type V3MapStep struct {
	Path  string `yaml:"path"`
	Table string `yaml:"table"`

	// OnMissing decides what happens when the value is not in the table: "keep", "clear", or "fail".
	//
	// No default that silently keeps the value. A code that failed to translate and travelled on unchanged is the
	// failure mode of every mapping table ever written - the receiving system gets a code from the sender's
	// vocabulary and either rejects the message or, worse, recognises it as something else.
	OnMissing string `yaml:"on_missing,omitempty"`
}

// V3ReplaceStep performs a regular-expression substitution.
type V3ReplaceStep struct {
	Path string `yaml:"path"`
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// V3TrimStep removes surrounding whitespace.
type V3TrimStep struct {
	Path string `yaml:"path"`
}

// V3CaseStep changes a value's case.
type V3CaseStep struct {
	Path string `yaml:"path"`

	// To is "upper" or "lower".
	To string `yaml:"to"`
}

// Steps is a compiled sequence of transformations.
type Steps struct {
	steps []Step
}

// CompileSteps validates a sequence and prepares it to run.
//
// Everything that can be checked without a message is checked here: paths parse, regular expressions compile, null
// flavours are recognised, tables are named, exactly one action per step. A channel with a bad step must refuse to start
// rather than fail on the first message - the operator who deployed it is watching at that moment and will not be watching
// at three the following morning.
//
// This is also where the mistake found in the v3 filter is avoided: matches compiled its pattern at load and =~ did not,
// so one form failed to start and the other failed on every message. Every pattern here compiles at load.
func CompileSteps(steps []Step, tables *codeset.Set) (*Steps, error) {
	out := make([]Step, 0, len(steps))

	for i, st := range steps {
		where := fmt.Sprintf("step %d", i+1)
		if st.Description != "" {
			where = fmt.Sprintf("step %d (%s)", i+1, st.Description)
		}

		if err := st.compile(where, tables); err != nil {
			return nil, err
		}
		out = append(out, st)
	}

	return &Steps{steps: out}, nil
}

// Len reports how many steps there are, for a caller deciding whether to bother.
func (s *Steps) Len() int {
	if s == nil {
		return 0
	}

	return len(s.steps)
}

// compile validates one step and caches what it can.
func (st *Step) compile(where string, tables *codeset.Set) error {
	actions := st.actions()
	switch len(actions) {
	case 0:
		return fmt.Errorf("%s does nothing; give it one of set, copy, clear, nullflavor, remove, map, "+
			"replace, trim or case", where)
	case 1:
		// Exactly right.
	default:
		return fmt.Errorf("%s has more than one action (%s), and there is no defined order between them; "+
			"split it into separate steps", where, strings.Join(actions, " and "))
	}

	st.compiledPath = map[string]Path{}
	for _, raw := range st.paths() {
		if strings.TrimSpace(raw) == "" {
			return fmt.Errorf("%s has an empty path", where)
		}
		p, err := ParsePath(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		st.compiledPath[raw] = p
	}

	if st.When != "" {
		f, err := ParseFilter(st.When)
		if err != nil {
			return fmt.Errorf("%s: the when condition is not valid: %w", where, err)
		}
		st.compiledWhen = f
	}

	if st.Replace != nil {
		re, err := regexp.Compile(st.Replace.From)
		if err != nil {
			return fmt.Errorf("%s: the replace pattern %q is not a valid regular expression: %w",
				where, st.Replace.From, err)
		}
		st.compiledFrom = re
	}

	if st.NullFlavor != nil && !KnownNullFlavor(st.NullFlavor.Reason) {
		return fmt.Errorf("%s: %q is not a null flavour this can write; the ones that carry meaning are %s",
			where, st.NullFlavor.Reason, knownNullFlavorList())
	}

	if st.Map != nil {
		if strings.TrimSpace(st.Map.Table) == "" {
			return fmt.Errorf("%s: a map step needs a table", where)
		}
		switch st.Map.OnMissing {
		case "", "keep", "clear", "fail":
			// Recognised. Empty means keep, matching the v2 steps.
		default:
			return fmt.Errorf("%s: on_missing is %q; it must be keep, clear or fail",
				where, st.Map.OnMissing)
		}

		// The table is resolved now rather than looked up per message, matching the v2 pipeline. A step naming a
		// table that does not exist is a channel that must not start: looking it up at run time would mean
		// every message failing, or worse, every message passing through untranslated.
		if tables == nil {
			return fmt.Errorf("%s: this step uses table %q, but no lookup tables are loaded",
				where, st.Map.Table)
		}
		table, ok := tables.Table(st.Map.Table)
		if !ok {
			available := ""
			if names := tables.Names(); len(names) > 0 {
				available = "; the tables loaded are " + strings.Join(names, ", ")
			}
			return fmt.Errorf("%s: there is no lookup table called %q%s", where, st.Map.Table, available)
		}
		st.compiledTable = table
	}

	if st.Case != nil {
		switch st.Case.To {
		case "upper", "lower":
			// Recognised.
		default:
			return fmt.Errorf("%s: case to is %q; it must be upper or lower", where, st.Case.To)
		}
	}

	return nil
}

// actions names the actions this step carries, so more than one can be reported.
func (st *Step) actions() []string {
	var out []string
	if st.Set != nil {
		out = append(out, "set")
	}
	if st.Copy != nil {
		out = append(out, "copy")
	}
	if st.Clear != nil {
		out = append(out, "clear")
	}
	if st.NullFlavor != nil {
		out = append(out, "nullflavor")
	}
	if st.Remove != nil {
		out = append(out, "remove")
	}
	if st.Map != nil {
		out = append(out, "map")
	}
	if st.Replace != nil {
		out = append(out, "replace")
	}
	if st.Trim != nil {
		out = append(out, "trim")
	}
	if st.Case != nil {
		out = append(out, "case")
	}

	return out
}

// paths names every path this step uses, so all of them are parsed at load.
func (st *Step) paths() []string {
	var out []string
	switch {
	case st.Set != nil:
		out = append(out, st.Set.Path)
	case st.Copy != nil:
		out = append(out, st.Copy.From, st.Copy.To)
	case st.Clear != nil:
		out = append(out, st.Clear.Path)
	case st.NullFlavor != nil:
		out = append(out, st.NullFlavor.Path)
	case st.Remove != nil:
		out = append(out, st.Remove.Path)
	case st.Map != nil:
		out = append(out, st.Map.Path)
	case st.Replace != nil:
		out = append(out, st.Replace.Path)
	case st.Trim != nil:
		out = append(out, st.Trim.Path)
	case st.Case != nil:
		out = append(out, st.Case.Path)
	}

	return out
}

// Apply runs every step against a document, in order.
//
// Stops at the first failure and says which step, because a half-transformed clinical message delivered as though it were
// complete is worse than one that did not go. The caller decides what to do with the failure; the engine records it and
// does not deliver.
func (s *Steps) Apply(root *xtree.Node) error {
	if s == nil || len(s.steps) == 0 {
		return nil
	}
	if root == nil {
		return fmt.Errorf("there is no document to transform")
	}

	for i, st := range s.steps {
		where := fmt.Sprintf("step %d", i+1)
		if st.Description != "" {
			where = fmt.Sprintf("step %d (%s)", i+1, st.Description)
		}

		if st.compiledWhen != nil {
			ok, err := st.compiledWhen.Match(root)
			if err != nil {
				return fmt.Errorf("%s: the when condition could not be evaluated: %w", where, err)
			}
			if !ok {
				continue
			}
		}

		if err := st.apply(root); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}

	return nil
}

// apply runs one step.
func (st *Step) apply(root *xtree.Node) error {
	switch {
	case st.Set != nil:
		return st.compiledPath[st.Set.Path].SetValue(root, st.Set.Value)

	case st.Copy != nil:
		from := st.compiledPath[st.Copy.From]
		value, ok := from.ReadValue(root)
		if !ok {
			// Copying from something absent is refused rather than treated as copying an empty string.
			// Writing empty would overwrite a good value at the destination with nothing, and the
			// author's intent was to move a value that exists.
			return fmt.Errorf("%q has no value to copy", st.Copy.From)
		}
		return st.compiledPath[st.Copy.To].SetValue(root, value)

	case st.Clear != nil:
		return st.compiledPath[st.Clear.Path].ClearValue(root)

	case st.NullFlavor != nil:
		return st.compiledPath[st.NullFlavor.Path].SetNullFlavor(root, st.NullFlavor.Reason)

	case st.Remove != nil:
		return st.compiledPath[st.Remove.Path].RemoveElement(root)

	case st.Map != nil:
		return st.applyMap(root)

	case st.Replace != nil:
		return st.rewrite(root, st.compiledPath[st.Replace.Path], func(in string) string {
			return st.compiledFrom.ReplaceAllString(in, st.Replace.To)
		})

	case st.Trim != nil:
		return st.rewrite(root, st.compiledPath[st.Trim.Path], strings.TrimSpace)

	case st.Case != nil:
		return st.rewrite(root, st.compiledPath[st.Case.Path], func(in string) string {
			if st.Case.To == "upper" {
				return strings.ToUpper(in)
			}
			return strings.ToLower(in)
		})
	}

	return fmt.Errorf("this step has no action, which should have been caught at load")
}

// applyMap translates a value through a table.
func (st *Step) applyMap(root *xtree.Node) error {
	path := st.compiledPath[st.Map.Path]

	value, ok := path.ReadValue(root)
	if !ok {
		// Nothing to translate. Not an error: a step mapping a gender code should not fail on a message that
		// carries no gender, and the null flavour is the correct way for a sender to say so.
		return nil
	}

	mapped, _, found := st.compiledTable.Lookup(value)
	if found {
		return path.SetValue(root, mapped)
	}

	switch st.Map.OnMissing {
	case "clear":
		return path.ClearValue(root)
	case "fail":
		return fmt.Errorf("%q is not in table %q, and this step is set to fail when that happens",
			value, st.Map.Table)
	default:
		// Keep. The value travels on untranslated, which is why on_missing is worth setting deliberately.
		return nil
	}
}

// rewrite reads a value, transforms it, and writes it back.
//
// Absent means nothing happens, and that is deliberate: trimming a field a message does not carry should not create it.
// The alternative - creating an element to hold an empty string - would turn "this sender does not send a middle name"
// into "this patient has no middle name", and those are different claims.
func (st *Step) rewrite(root *xtree.Node, path Path, fn func(string) string) error {
	value, ok := path.ReadValue(root)
	if !ok {
		return nil
	}

	updated := fn(value)
	if updated == value {
		return nil
	}

	return path.SetValue(root, updated)
}
