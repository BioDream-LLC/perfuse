// Declarative transformation steps, for any message format.
//
// # Why this is shared
//
// The step vocabulary - set, copy, clear, map, replace, trim, case, each with a description and an optional condition -
// has nothing to do with any particular format. Only three things differ between formats: how a path is written, how a
// value is read, and how it is written back.
//
// This is the same conclusion the filter grammar reached in internal/expr, and for the same reason. X12 had a step engine
// and NCPDP needed one; copying it would have produced a second four-hundred-line engine to keep in step by hand, and then
// a third for delimited. The behaviour worth having is subtle enough that a copy would not keep it - the reason the result
// is re-read after every write, for instance, took a real defect to discover.
//
// # What a format supplies
//
// An Accessor: a path compiler, a condition compiler, and an optional veto on steps it cannot honour. Nothing else.
package steps

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/expr"
)

// Path is a path compiled against one message format.
//
// Closures rather than a string handed back to the accessor on every access, so a path is parsed once at load. The filter
// grammar measured this and it was worth roughly a third of the cost of an evaluation.
type Path[M any] struct {
	// Canonical is the path in canonical form, for change records and error messages.
	Canonical string

	// Read returns the value at this path and whether it was present.
	//
	// The bool distinguishes absent from empty, which every format here treats as different things: a blank field was
	// sent deliberately and an absent one was never sent.
	Read func(M) (string, bool)

	// Write returns a message with the value written. It must not modify its receiver.
	Write func(M, string) (M, error)
}

// Accessor is how one message format addresses, reads and writes values.
type Accessor[M any] interface {
	// Path compiles one path, or reports why it cannot.
	Path(raw string) (Path[M], error)

	// Condition compiles a when expression over this format.
	Condition(src string) (expr.Expr[M], error)

	// CheckStep refuses a step this format cannot honour, and returns nil for everything it can.
	//
	// For the refusals that are real but specific: a trim on an X12 ISA element can never have an effect, because ISA is
	// fixed width and the padding is put straight back. Refusing at load beats a step that runs forever and does nothing.
	CheckStep(s Step) error
}

// Step is one declarative change.
//
// Exactly one action per step, checked at load. Two actions in one step have no defined order, and a configuration whose
// meaning depends on which the reader assumes is worse than one that refuses to load.
type Step struct {
	// Description is free text explaining why this step exists. Optional, shown in the interface, and worth writing: the
	// next person to read this channel is trying to work out whether the step is still needed.
	Description string `yaml:"description,omitempty"`

	// When is an optional condition. The step is skipped unless it holds.
	//
	// The same grammar as any filter in this server - and, or, not, ==, !=, =~, exists, empty, in - because the grammar
	// is generic over the format. The only thing that changes between formats is how the path is written.
	When string `yaml:"when,omitempty"`

	// Set writes a value at a path.
	Set *SetStep `yaml:"set,omitempty"`

	// Copy takes the value at one path and writes it to another.
	Copy *CopyStep `yaml:"copy,omitempty"`

	// Clear empties a field, leaving it present.
	//
	// Not the same as removing it, and these formats make that distinction meaningful: a blank field was sent
	// deliberately while an absent one was never sent. There is no remove step for exactly that reason - the honest
	// options are to blank the field or to leave it alone.
	Clear *ClearStep `yaml:"clear,omitempty"`

	// Map translates a value through a lookup table.
	Map *MapStep `yaml:"map,omitempty"`

	// Replace performs a regular-expression substitution.
	Replace *ReplaceStep `yaml:"replace,omitempty"`

	// Trim removes surrounding whitespace.
	//
	// Worth having as its own step rather than a replace: these formats pad fixed-width fields with spaces, so this is
	// one of the most common changes an interface needs, and a named step is readable where a pattern is not.
	Trim *TrimStep `yaml:"trim,omitempty"`

	// Case changes a value to upper or lower case.
	Case *CaseStep `yaml:"case,omitempty"`
}

// SetStep writes a literal value.
type SetStep struct {
	Path  string `yaml:"path"`
	Value string `yaml:"value"`
}

// CopyStep moves a value from one path to another.
type CopyStep struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// ClearStep empties a field.
type ClearStep struct {
	Path string `yaml:"path"`
}

// MapStep translates a value through a named table.
type MapStep struct {
	Path string `yaml:"path"`

	// Table names a code set loaded with the channel.
	Table string `yaml:"table"`

	// Default is written when the value is not in the table. Optional.
	Default string `yaml:"default,omitempty"`

	// OnMissing decides what happens when the value is absent from the table and no default is given.
	//
	// Defaults to "keep", which leaves the value alone. The alternative is "fail", which stops the message. There is no
	// option that silently blanks it, because a claim with an empty payer identifier is rejected downstream in a way
	// that points at the wrong system.
	OnMissing string `yaml:"on_missing,omitempty"`
}

// ReplaceStep performs a regular-expression substitution.
type ReplaceStep struct {
	Path    string `yaml:"path"`
	Pattern string `yaml:"pattern"`
	With    string `yaml:"with"`
}

// TrimStep removes surrounding whitespace.
type TrimStep struct {
	Path string `yaml:"path"`
}

// CaseStep changes the case of a value.
type CaseStep struct {
	Path string `yaml:"path"`

	// To is "upper" or "lower".
	To string `yaml:"to"`
}

// TargetPaths returns the paths this step writes to, keyed by the configuration key that named each one.
//
// For an Accessor's CheckStep, which usually needs to veto a step by what it writes rather than by which action it is: a
// format with a derived or fixed-width field cares that something writes to it, not whether the step was a set or a trim.
//
// copy.from is deliberately absent. Reading a field is always allowed; it is writing that a format restricts.
func (s Step) TargetPaths() map[string]string {
	out := map[string]string{}
	add := func(key, path string) {
		if strings.TrimSpace(path) != "" {
			out[key] = path
		}
	}

	if s.Set != nil {
		add("set.path", s.Set.Path)
	}
	if s.Clear != nil {
		add("clear.path", s.Clear.Path)
	}
	if s.Copy != nil {
		add("copy.to", s.Copy.To)
	}
	if s.Map != nil {
		add("map.path", s.Map.Path)
	}
	if s.Replace != nil {
		add("replace.path", s.Replace.Path)
	}
	if s.Trim != nil {
		add("trim.path", s.Trim.Path)
	}
	if s.Case != nil {
		add("case.path", s.Case.Path)
	}
	return out
}

// Change records one alteration, for the trace and the report.
type Change struct {
	// Path is where it happened, in canonical form.
	Path string
	// Description is the step's own explanation, when it gave one.
	Description string
	// From and To are the values before and after.
	From string
	To   string
}

// Steps is a compiled sequence, ready to run.
type Steps[M any] struct {
	compiled []compiledStep[M]
}

// Len reports how many steps there are.
func (s *Steps[M]) Len() int {
	if s == nil {
		return 0
	}
	return len(s.compiled)
}

// compiledStep is one step with everything that can be prepared in advance already prepared.
type compiledStep[M any] struct {
	step Step

	path Path[M]
	from Path[M]
	to   Path[M]

	pattern *regexp.Regexp
	table   *codeset.Table

	// when is compiled at load, so a condition that does not parse refuses the channel rather than failing per message.
	when expr.Expr[M]
}

// Compile prepares a sequence for running.
//
// Everything checkable without a message is checked here: paths parse, patterns compile, tables are named, case
// directions are recognised, exactly one action per step. A channel with a bad step must refuse to start rather than fail
// on the first message, because the operator who deployed it is watching at that moment and will not be watching at three
// the following morning.
func Compile[M any](in []Step, tables *codeset.Set, a Accessor[M]) (*Steps[M], error) {
	out := &Steps[M]{}

	for i, st := range in {
		where := fmt.Sprintf("step %d", i+1)
		if st.Description != "" {
			where = fmt.Sprintf("step %d (%s)", i+1, st.Description)
		}

		if n := actionCount(st); n != 1 {
			if n == 0 {
				return nil, fmt.Errorf("%s: no action; give it one of set, copy, clear, map, replace, trim or case", where)
			}
			return nil, fmt.Errorf("%s: %d actions, and a step does exactly one; the order is undefined when a step "+
				"declares two, so split them into separate steps in the order you want", where, n)
		}

		if err := a.CheckStep(st); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}

		c := compiledStep[M]{step: st}

		if st.When != "" {
			cond, err := a.Condition(st.When)
			if err != nil {
				return nil, fmt.Errorf("%s: when: %w", where, err)
			}
			c.when = cond
		}

		var err error
		switch {
		case st.Set != nil:
			c.path, err = compilePath(where, "set.path", st.Set.Path, a)

		case st.Copy != nil:
			c.from, err = compilePath(where, "copy.from", st.Copy.From, a)
			if err == nil {
				c.to, err = compilePath(where, "copy.to", st.Copy.To, a)
			}
			if err == nil && c.from.Canonical == c.to.Canonical {
				// Compared canonically rather than as written, so CLM01 and CLM-1 are recognised as the same path. A copy
				// that cannot change anything is almost certainly a typo in one of the two paths, and a step that runs
				// forever doing nothing is the thing this project refuses to ship.
				err = fmt.Errorf("%s: copy.from and copy.to are both %s, so this step could never change anything; "+
					"one of the two paths is likely a typo", where, c.from.Canonical)
			}
			if err == nil && c.from.Canonical == c.to.Canonical {
				// Compared canonically rather than as written, so CLM01 and CLM-1 are recognised as the same path. A copy
				// that cannot change anything is almost certainly a typo in one of the two paths, and a step that runs
				// forever doing nothing is the thing this project refuses to ship.
				err = fmt.Errorf("%s: copy.from and copy.to are both %s, so this step could never change anything; "+
					"one of the two paths is likely a typo", where, c.from.Canonical)
			}

		case st.Clear != nil:
			c.path, err = compilePath(where, "clear.path", st.Clear.Path, a)

		case st.Map != nil:
			c.path, err = compilePath(where, "map.path", st.Map.Path, a)
			if err == nil {
				err = compileMap(where, st, tables, &c)
			}

		case st.Replace != nil:
			c.path, err = compilePath(where, "replace.path", st.Replace.Path, a)
			if err == nil {
				if st.Replace.Pattern == "" {
					err = fmt.Errorf("%s: replace.pattern is empty", where)
				} else if c.pattern, err = regexp.Compile(st.Replace.Pattern); err != nil {
					err = fmt.Errorf("%s: replace.pattern: %w", where, err)
				}
			}

		case st.Trim != nil:
			c.path, err = compilePath(where, "trim.path", st.Trim.Path, a)

		case st.Case != nil:
			c.path, err = compilePath(where, "case.path", st.Case.Path, a)
			if err == nil {
				switch strings.ToLower(st.Case.To) {
				case "upper", "lower":
				default:
					err = fmt.Errorf("%s: case.to is %q; it is upper or lower", where, st.Case.To)
				}
			}
		}
		if err != nil {
			return nil, err
		}

		out.compiled = append(out.compiled, c)
	}

	return out, nil
}

// compileMap resolves the table and checks on_missing.
func compileMap[M any](where string, st Step, tables *codeset.Set, c *compiledStep[M]) error {
	if st.Map.Table == "" {
		return fmt.Errorf("%s: map.table is empty", where)
	}
	switch strings.ToLower(st.Map.OnMissing) {
	case "", "keep", "fail":
	default:
		return fmt.Errorf("%s: map.on_missing is %q; it is keep or fail", where, st.Map.OnMissing)
	}
	if tables == nil {
		return fmt.Errorf("%s: map.table is %q but no code sets are loaded on this channel", where, st.Map.Table)
	}
	t, ok := tables.Table(st.Map.Table)
	if !ok {
		return fmt.Errorf("%s: map.table is %q, which is not a table loaded on this channel", where, st.Map.Table)
	}
	c.table = t
	return nil
}

// actionCount counts how many actions a step declares.
func actionCount(s Step) int {
	n := 0
	for _, set := range []bool{
		s.Set != nil, s.Copy != nil, s.Clear != nil, s.Map != nil,
		s.Replace != nil, s.Trim != nil, s.Case != nil,
	} {
		if set {
			n++
		}
	}
	return n
}

// compilePath parses a path and blames the right key when it does not.
func compilePath[M any](where, key, raw string, a Accessor[M]) (Path[M], error) {
	if strings.TrimSpace(raw) == "" {
		return Path[M]{}, fmt.Errorf("%s: %s is empty", where, key)
	}
	p, err := a.Path(raw)
	if err != nil {
		return Path[M]{}, fmt.Errorf("%s: %s: %w", where, key, err)
	}
	return p, nil
}

// Apply runs the sequence, returning the result and what changed.
//
// The message given is not modified. Every step works on the output of the last, so a later step sees earlier changes -
// which is what a reader of the file expects, and the alternative would make a sequence of steps behave differently from
// the same steps in separate channels.
func (s *Steps[M]) Apply(m M) (M, []Change, error) {
	if s == nil || len(s.compiled) == 0 {
		return m, nil, nil
	}

	current := m
	var changes []Change

	for i, c := range s.compiled {
		where := fmt.Sprintf("step %d", i+1)
		if c.step.Description != "" {
			where = fmt.Sprintf("step %d (%s)", i+1, c.step.Description)
		}

		if c.when != nil {
			holds, err := c.when.Eval(current)
			if err != nil {
				var zero M
				return zero, nil, fmt.Errorf("%s: when: %w", where, err)
			}
			if !holds {
				continue
			}
		}

		next, change, err := c.apply(current)
		if err != nil {
			var zero M
			return zero, nil, fmt.Errorf("%s: %w", where, err)
		}
		if change != nil {
			changes = append(changes, *change)
		}
		current = next
	}

	return current, changes, nil
}

// apply runs one step.
func (c compiledStep[M]) apply(m M) (M, *Change, error) {
	switch {
	case c.step.Set != nil:
		return c.write(m, c.path, c.step.Set.Value)

	case c.step.Copy != nil:
		value, ok := c.from.Read(m)
		if !ok {
			// Nothing to copy is not a failure. A source field a partner did not send is ordinary, and stopping the
			// message would turn an optional field into a required one.
			return m, nil, nil
		}
		return c.write(m, c.to, value)

	case c.step.Clear != nil:
		return c.write(m, c.path, "")

	case c.step.Map != nil:
		value, ok := c.path.Read(m)
		if !ok {
			return m, nil, nil
		}
		mapped, _, found := c.table.Lookup(value)
		if !found {
			if c.step.Map.Default != "" {
				mapped = c.step.Map.Default
			} else if strings.EqualFold(c.step.Map.OnMissing, "fail") {
				return m, nil, fmt.Errorf("%s is %q, which is not in table %q, and on_missing is fail",
					c.path.Canonical, value, c.step.Map.Table)
			} else {
				return m, nil, nil
			}
		}
		return c.write(m, c.path, mapped)

	case c.step.Replace != nil:
		value, ok := c.path.Read(m)
		if !ok {
			return m, nil, nil
		}
		return c.write(m, c.path, c.pattern.ReplaceAllString(value, c.step.Replace.With))

	case c.step.Trim != nil:
		value, ok := c.path.Read(m)
		if !ok {
			return m, nil, nil
		}
		return c.write(m, c.path, strings.TrimSpace(value))

	case c.step.Case != nil:
		value, ok := c.path.Read(m)
		if !ok {
			return m, nil, nil
		}
		if strings.EqualFold(c.step.Case.To, "upper") {
			return c.write(m, c.path, strings.ToUpper(value))
		}
		return c.write(m, c.path, strings.ToLower(value))
	}

	return m, nil, nil
}

// write applies a value and records the change, or records nothing when nothing actually changed.
//
// # Why the result is re-read instead of trusting the value that was asked for
//
// A step that writes the value already present has changed nothing, and reporting it as a change is not harmless: a trace
// showing three changes where two happened tells somebody their step worked when it did nothing. That exact defect has
// been fixed once already in the HL7 path and is recorded in docs/queue.md.
//
// Comparing the requested value against the previous one is not enough, because a write can be neutralised. Setting an
// X12 ISA element to a shorter string re-pads it to its fixed width, so the request differs from what was there and the
// result does not. Found by a trim step on ISA06, which correctly left the interchange untouched and incorrectly reported
// a change.
//
// So the change is decided by reading the path back afterwards. That reports what happened rather than what was intended,
// which is the only version a trace can be trusted for. It generalises to every format: any format with fixed-width or
// otherwise normalised fields can neutralise a write, and none of them have to think about it here.
func (c compiledStep[M]) write(m M, p Path[M], value string) (M, *Change, error) {
	before, existed := p.Read(m)
	if existed && before == value {
		return m, nil, nil
	}

	out, err := p.Write(m, value)
	if err != nil {
		return m, nil, err
	}

	after, _ := p.Read(out)
	if after == before {
		return out, nil, nil
	}

	return out, &Change{
		Path:        p.Canonical,
		Description: c.step.Description,
		From:        before,
		To:          after,
	}, nil
}
