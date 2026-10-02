// X12's binding to the shared transformation steps.
//
// The step vocabulary and the engine live in internal/steps, generic over the message format. This is everything X12 has
// to say for itself: how a path is compiled, how a condition is compiled, and the two steps it refuses.
//
// It used to be four hundred lines of engine. Keeping that copy would have meant maintaining it alongside a second one for
// NCPDP and a third for delimited, and the behaviour worth having is subtle enough that the copies would not have kept it.
package x12

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/steps"
)

// Re-exported so a configuration file and the builder keep naming x12.Step, and so the yaml keys are unchanged.
type (
	// Step is one declarative change to an interchange.
	Step = steps.Step
	// SetStep writes a literal value.
	SetStep = steps.SetStep
	// CopyStep moves a value from one path to another.
	CopyStep = steps.CopyStep
	// ClearStep empties an element.
	ClearStep = steps.ClearStep
	// MapStep translates a value through a named table.
	MapStep = steps.MapStep
	// ReplaceStep performs a regular-expression substitution.
	ReplaceStep = steps.ReplaceStep
	// TrimStep removes surrounding whitespace.
	TrimStep = steps.TrimStep
	// CaseStep changes the case of a value.
	CaseStep = steps.CaseStep
	// Change records one alteration.
	Change = steps.Change
	// Steps is a compiled sequence over an interchange.
	Steps = steps.Steps[*Message]
)

// Accessor binds the shared steps to X12.
type Accessor struct{}

// Path compiles one X12 path.
//
// A whole segment is refused here rather than at the write, so a channel that could never work does not start. The steps
// change elements; naming a segment has no meaning for any of them.
func (Accessor) Path(raw string) (steps.Path[*Message], error) {
	p, err := ParsePath(raw)
	if err != nil {
		return steps.Path[*Message]{}, err
	}
	if p.Element == 0 {
		return steps.Path[*Message]{}, fmt.Errorf("%q addresses a whole segment; the steps change elements, so name "+
			"one such as %s-1", raw, p.Segment)
	}

	return steps.Path[*Message]{
		Canonical: p.String(),
		Read:      func(m *Message) (string, bool) { return Read(m, p) },
		Write:     func(m *Message, v string) (*Message, error) { return Set(m, p, v) },
	}, nil
}

// Condition compiles a when expression over an interchange.
func (Accessor) Condition(src string) (expr.Expr[*Message], error) { return ParseFilter(src) }

// CheckStep refuses the two steps X12 cannot honour.
func (Accessor) CheckStep(s Step) error {
	// A trim on an ISA element can never have an effect. ISA is the only X12 segment whose element lengths are semantic,
	// so a write to it is padded back to the standard width - the padding a trim just removed goes straight back on.
	// Refusing at load beats a step that runs on every message forever and does nothing.
	if s.Trim != nil && isISAPath(s.Trim.Path) {
		return fmt.Errorf("trim.path is %q, and ISA elements are fixed width: whatever a trim removes is padded back "+
			"immediately, so this step could never have an effect", s.Trim.Path)
	}
	return nil
}

// isISAPath reports whether a path addresses the interchange header.
func isISAPath(raw string) bool {
	p, err := ParsePath(raw)
	if err != nil {
		// Left to the path compiler to report properly, rather than guessed at here.
		return false
	}
	return strings.EqualFold(p.Segment, "ISA")
}

// CompileSteps prepares a sequence for running over an interchange.
func CompileSteps(in []Step, tables *codeset.Set) (*Steps, error) {
	return steps.Compile(in, tables, Accessor{})
}
