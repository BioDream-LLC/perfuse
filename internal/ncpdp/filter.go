// Filtering NCPDP transmissions.
//
// The grammar is the one internal/expr implements for every format - and, or, not, parentheses, ==, !=, >, <, =~, matches,
// exists, empty, in. All this supplies is how an NCPDP path becomes values, so somebody who can filter an HL7 feed can
// filter a pharmacy claim and the only new thing to learn is that the path is written D1 or 07-D7.
package ncpdp

import (
	"strings"

	"github.com/biodream-llc/perfuse/internal/expr"
)

// Resolver addresses NCPDP transmissions for the filter grammar.
type Resolver struct{}

// Prepare compiles one NCPDP path.
func (Resolver) Prepare(raw string) (expr.Prepared[*Message], error) {
	p, err := ParsePath(raw)
	if err != nil {
		return expr.Prepared[*Message]{}, err
	}

	return expr.Prepared[*Message]{
		Candidates: func(m *Message) []expr.Value {
			vals := Read(*m, p)
			out := make([]expr.Value, 0, len(vals))
			for _, v := range vals {
				out = append(out, expr.Value{Text: v, Empty: strings.TrimSpace(v) == ""})
			}
			return out
		},

		Presence: func(m *Message) (int, bool) {
			vals := Read(*m, p)
			if len(vals) == 0 {
				return 0, true
			}
			allEmpty := true
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					allEmpty = false
					break
				}
			}
			return len(vals), allEmpty
		},

		Single: func(m *Message) string {
			vals := Read(*m, p)
			if len(vals) == 0 {
				return ""
			}
			return vals[0]
		},
	}, nil
}

// Semantics keeps ordering numeric and quotes optional, matching v2 and X12.
//
// Numeric is right here: the ordered fields in a claim are quantities and amounts - days supply, quantity dispensed,
// ingredient cost. NCPDP dates are CCYYMMDD, which compares correctly either way, so the timestamp argument that makes v3
// choose text does not apply.
func (Resolver) Semantics() expr.Semantics { return expr.Semantics{} }

// ParseFilter compiles a filter expression over a transmission.
func ParseFilter(src string) (expr.Expr[*Message], error) {
	return expr.ParseFor(src, Resolver{})
}
