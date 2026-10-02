// NCPDP's binding to the shared transformation steps.
//
// The step vocabulary and the engine live in internal/steps, generic over the message format. This is everything NCPDP has
// to say for itself.
package ncpdp

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/steps"
)

// Re-exported so a configuration file names ncpdp.Step and the yaml keys match every other format's.
type (
	// Step is one declarative change to a transmission.
	Step = steps.Step
	// SetStep writes a literal value.
	SetStep = steps.SetStep
	// CopyStep moves a value from one path to another.
	CopyStep = steps.CopyStep
	// ClearStep empties a field.
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
	// Steps is a compiled sequence over a transmission.
	Steps = steps.Steps[*Message]
)

// Accessor binds the shared steps to NCPDP.
type Accessor struct{}

// Path compiles one NCPDP path.
func (Accessor) Path(raw string) (steps.Path[*Message], error) {
	p, err := ParsePath(raw)
	if err != nil {
		return steps.Path[*Message]{}, err
	}

	return steps.Path[*Message]{
		Canonical: p.String(),

		// ReadOne rather than the first of many, so a step never reads one of several fields and writes to another. An
		// ambiguous path reports absent, which makes every step skip rather than guess, and Set refuses it outright with
		// an error naming the qualified form.
		Read: func(m *Message) (string, bool) { return ReadOne(*m, p) },

		Write: func(m *Message, v string) (*Message, error) {
			out, err := Set(*m, p, v)
			if err != nil {
				return nil, err
			}
			return &out, nil
		},
	}, nil
}

// Condition compiles a when expression over a transmission.
func (Accessor) Condition(src string) (expr.Expr[*Message], error) { return ParseFilter(src) }

// CheckStep refuses the steps NCPDP cannot honour.
func (Accessor) CheckStep(s Step) error {
	for key, path := range s.TargetPaths() {
		p, err := ParsePath(path)
		if err != nil {
			// Left to the path compiler, which blames the right key and explains the notation.
			continue
		}

		// The transaction count tells the switch how many claims follow. Build derives it from the transmission, so a
		// step that changed it would describe a transmission that does not exist - and the switch would either look for
		// a claim that is not there or ignore one that is.
		if p.Segment == "" && strings.EqualFold(p.Field, "A9") {
			return fmt.Errorf("%s is A9, the transaction count, which is derived from the transmission when it is "+
				"written; changing it would describe a transmission that does not exist", key)
		}

		// The version/release identifies the standard the transmission is written in. Rewriting it does not convert
		// anything: the fields keep D.0 semantics and the header keeps D.0 widths, so it only mislabels the message.
		if p.Segment == "" && strings.EqualFold(p.Field, "A2") {
			return fmt.Errorf("%s is A2, the version and release, which says what standard this transmission follows; "+
				"changing it relabels the message without converting it, so a switch would read D.0 fields as though "+
				"they were another version", key)
		}
	}
	return nil
}

// CompileSteps prepares a sequence for running over a transmission.
func CompileSteps(in []Step, tables *codeset.Set) (*Steps, error) {
	return steps.Compile(in, tables, Accessor{})
}
