package eprescribe

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Tree is what a filter sees: the prescription as a generic XML tree.
//
// # Why a tree and not the typed Message
//
// Parse produces a typed Message - Header, Body, Prescription - which is the right shape for the code that builds a response or
// checks a controlled substance, because those need named fields with real types.
//
// It is the wrong shape for a filter. A struct can only be addressed where a field exists, so a filter could ask about the
// elements this package happens to model and nothing else. SCRIPT is large, sites use parts of it this package has no struct
// for, and a partner adding an optional element should not require a code change before a filter can mention it.
//
// So a filter reads a tree, addressed by hl7v3.Path. That is not a new mechanism: v3 and CDA already use it, the grammar is the
// same //Element and //Element@attribute, and the entire path implementation is shared rather than copied. The cost is parsing
// the document twice on a channel that has a filter, which is one XML parse against a network delivery.
type Tree struct {
	Root *xtree.Node
}

// ParseTree reads a SCRIPT message as a tree.
//
// The root element is checked here as well as in Parse, so that a filter on a channel receiving the wrong document type fails
// with the same message rather than silently matching nothing.
func ParseTree(data []byte) (Tree, error) {
	root, err := xtree.Parse(data)
	if err != nil {
		return Tree{}, fmt.Errorf("this is not a SCRIPT message: %w", err)
	}
	if root == nil {
		return Tree{}, fmt.Errorf("this is not a SCRIPT message: the document is empty")
	}
	if root.Name != "Message" {
		return Tree{}, fmt.Errorf("the root element is <%s> and a SCRIPT message is <Message>", root.Name)
	}

	return Tree{Root: root}, nil
}

// Resolver prepares SCRIPT paths for the shared filter grammar.
//
// # Why it borrows v3's semantics rather than the default ones
//
// The three Semantics that v3 sets are set here for the same reasons, because they are properties of XML rather than of v3:
//
//   - AbsentMatchesNothing. In HL7 v2 a comparison against "" deliberately tests for a missing field, because a v2 field is
//     always positionally present. In XML an element that is absent and an element that is present and empty are different
//     facts - a prescription with no <Note> and one with an empty <Note> are not the same document - so == "" is not allowed
//     to conflate them.
//   - RequireQuotedValues. v2 accepts MSH-9.2 == A08 unquoted and has a test saying so. XML element content routinely
//     contains characters the lexer would read as structure, so requiring quotes here removes a class of confusing parse
//     error rather than adding ceremony.
//   - OrderAsText. SCRIPT's ordered values are dates and timestamps in the same YYYYMMDD form v3 uses, where text order is
//     time order. Comparing them numerically breaks the moment one carries a timezone offset, because 20260830-0500 is not a
//     number.
//
// This is deliberately hl7v3.Resolver's answer reused, not rediscovered. If the two ever need to differ, that is a finding
// worth a comment explaining which property of SCRIPT makes it so.
type Resolver struct{}

// Semantics reports the XML dialect rules.
func (Resolver) Semantics() expr.Semantics {
	return expr.Semantics{
		OrderAsText:          true,
		AbsentMatchesNothing: true,
		RequireQuotedValues:  true,
	}
}

// Prepare compiles one path, once, at parse time.
func (Resolver) Prepare(path string) (expr.Prepared[Tree], error) {
	p, err := hl7v3.ParsePath(path)
	if err != nil {
		return expr.Prepared[Tree]{}, err
	}

	return expr.Prepared[Tree]{
		Candidates: func(t Tree) []expr.Value {
			// PathValues rather than resolving and reading each node, because the two cases differ: an explicit
			// //Element@attribute path must read that attribute literally, while a bare //Element falls back through
			// text, value and code. Reading nodes directly would answer the attribute case with element text.
			//
			// This is the bug v3 had for months, and the reason both helpers are exported rather than copied: a value
			// like <WrittenDate value="20260830"/> lives in an attribute, and an evaluator reading only element content
			// answers false for every comparison against it while exists still reports it present.
			vals := hl7v3.PathValues(p, t.Root)
			out := make([]expr.Value, 0, len(vals))
			for _, v := range vals {
				out = append(out, expr.Value{Text: v, Empty: strings.TrimSpace(v) == ""})
			}

			return out
		},

		Presence: func(t Tree) (int, bool) {
			vals := hl7v3.PathValues(p, t.Root)
			allEmpty := true
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					allEmpty = false

					break
				}
			}

			return len(vals), allEmpty
		},

		Single: func(t Tree) string {
			vals := hl7v3.PathValues(p, t.Root)
			if len(vals) == 0 {
				return ""
			}

			return vals[0]
		},
	}, nil
}

// ParseFilter compiles a filter expression against a SCRIPT prescription.
func ParseFilter(src string) (expr.Expr[Tree], error) {
	return expr.ParseFor(src, Resolver{})
}
