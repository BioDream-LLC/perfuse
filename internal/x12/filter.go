// Filtering and conditions for X12.
//
// # Why this is fifteen lines rather than seven hundred
//
// The grammar - and, or, not, parentheses, ==, !=, >, <, =~, matches, exists, empty, in - lives in internal/expr and is
// generic over the message format. All a format has to supply is how to turn a path into values.
//
// That is the whole of this file. Somebody who can filter an HL7 feed can filter an X12 one, and the only thing to learn
// is that the path on the left is written CLM01 or CLM-1 rather than PID-3 - which they already know, because it is the
// same notation the transformation steps use.
package x12

import "github.com/biodream-llc/perfuse/internal/expr"

// Resolver addresses X12 interchanges for the filter grammar.
type Resolver struct{}

// Prepare compiles one X12 path.
func (Resolver) Prepare(path string) (expr.Prepared[*Message], error) {
	p, err := ParsePath(path)
	if err != nil {
		return expr.Prepared[*Message]{}, err
	}

	// Candidates returns at most one value. X12 element repetitions exist, but a path that names no repetition addresses
	// the whole element including its separators - which is what Read does and what the transformation steps write - so
	// treating an unrepeated path as "any repetition" here would make a filter and a step disagree about the same path.
	// That disagreement is worse than the missing convenience: a step and a filter written against the same path must
	// mean the same thing.
	return expr.Prepared[*Message]{
		Candidates: func(m *Message) []expr.Value {
			text, ok := Read(m, p)
			if !ok {
				return nil
			}
			return []expr.Value{{Text: text, Empty: text == ""}}
		},
		Presence: func(m *Message) (int, bool) {
			text, ok := Read(m, p)
			if !ok {
				return 0, true
			}
			return 1, text == ""
		},
		Single: func(m *Message) string {
			text, _ := Read(m, p)
			return text
		},
	}, nil
}

// ParseFilter compiles a filter expression over an interchange.
func ParseFilter(src string) (expr.Expr[*Message], error) {
	return expr.ParseFor(src, Resolver{})
}
