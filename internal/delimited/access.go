package delimited

import (
	"fmt"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/steps"
)

// Resolver prepares delimited paths for the shared filter grammar.
//
// The zero Semantics apply, which is the v2/X12 behaviour: a comparison against "" tests for a missing or blank field, and an
// unquoted bare word is accepted on the right of an operator. Both are deliberate. A delimited file's commonest real question
// is whether a column is empty, and requiring quotes would break the resemblance to the X12 and HL7 filters somebody has
// already learned.
type Resolver struct{}

// Prepare compiles one path into readers, once, at parse time.
func (Resolver) Prepare(path string) (expr.Prepared[Message], error) {
	p, err := ParsePath(path)
	if err != nil {
		return expr.Prepared[Message]{}, err
	}

	return expr.Prepared[Message]{
		Candidates: func(m Message) []expr.Value {
			values := Read(m, p)
			out := make([]expr.Value, 0, len(values))
			for _, v := range values {
				// Empty tracks blankness rather than absence. A field present and blank has no content, and Read has
				// already dropped the records where the column does not exist at all, so a caller can tell "the supplier
				// sent nothing in this column" from "the supplier stopped sending this column" by count.
				out = append(out, expr.Value{Text: v, Empty: v == ""})
			}

			return out
		},

		// Presence answers exists and empty without materialising any text, which is what keeps those operators from
		// allocating a slice per row on a four-hundred-row document.
		Presence: func(m Message) (int, bool) {
			count := 0
			allEmpty := true
			for _, record := range m {
				var v string
				var ok bool
				if p.Position > 0 {
					v, ok = record.At(p.Position)
				} else {
					v, ok = record.Get(p.Column)
				}
				if !ok {
					continue
				}
				count++
				if v != "" {
					allEmpty = false
				}
			}

			return count, allEmpty
		},

		Single: func(m Message) string {
			v, _ := ReadOne(m, p)

			return v
		},
	}, nil
}

// ParseFilter compiles a filter expression against a delimited document.
func ParseFilter(src string) (expr.Expr[Message], error) {
	return expr.ParseFor(src, Resolver{})
}

// Accessor binds delimited paths to the shared transformation engine.
type Accessor struct{}

// Path prepares one path for reading and writing.
func (Accessor) Path(raw string) (steps.Path[Message], error) {
	p, err := ParsePath(raw)
	if err != nil {
		return steps.Path[Message]{}, err
	}

	return steps.Path[Message]{
		Canonical: p.Canonical(),
		Read: func(m Message) (string, bool) {
			return ReadOne(m, p)
		},
		Write: func(m Message, value string) (Message, error) {
			return Set(m, p, value)
		},
	}, nil
}

// Condition compiles a step's when expression.
func (Accessor) Condition(src string) (expr.Expr[Message], error) {
	return ParseFilter(src)
}

// CheckStep vetoes a step this format cannot honour.
//
// Only one veto, and it is about the header rather than about any particular step. A named column that is not in the header
// cannot be created, because the header belongs to the document and a message may be one row of it - so writing a new named
// column to one row would give the rows different widths, which is no longer a delimited file. Set already refuses that at
// runtime with the column list in the message, which is a better error than anything available at load time, where the file
// has not been seen.
//
// So this exists to be honest about there being nothing to check here, rather than to check nothing by accident. A veto that
// could be written at load time - a path that does not parse - is already handled by Path.
func (Accessor) CheckStep(s steps.Step) error {
	for key, raw := range s.TargetPaths() {
		if _, err := ParsePath(raw); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}

	return nil
}

// Steps is a compiled sequence of delimited transformation steps.
type Steps = steps.Steps[Message]

// Step is one declarative transformation. Re-exported so that configuration and tests do not import two packages to describe
// one thing, which is what X12 and NCPDP already do.
type Step = steps.Step

// CompileSteps prepares the steps at load time.
//
// Compiled at load rather than per message, for the reason the other formats already are: a map step binds its table now, so a
// channel naming a table that does not exist refuses to start instead of passing every row through untranslated.
func CompileSteps(in []Step, tables *codeset.Set) (*Steps, error) {
	compiled, err := steps.Compile(in, tables, Accessor{})
	if err != nil {
		return nil, err
	}

	return compiled, nil
}
