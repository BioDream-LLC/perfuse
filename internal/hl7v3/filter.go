// Filtering HL7 v3 and CDA documents.
//
// # What this used to be
//
// Seven hundred lines: a lexer, a parser, nine node types and their evaluators. Its own doc comment explained why it was
// a copy rather than a generalisation - teaching internal/expr about v3 would have meant changing a signature everywhere
// it appeared - and that was true when it was written.
//
// internal/expr is generic over the message format now, so all that is needed is how a v3 path becomes values. The
// grammar the two accepted was already character-for-character identical except for one keyword, which arrives here as an
// operator the format contributes.
//
// Keeping the copy would have meant maintaining the same grammar twice while adding it to three more formats.
package hl7v3

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Resolver addresses v3 documents for the filter grammar.
type Resolver struct{}

// Prepare compiles one v3 path.
func (Resolver) Prepare(raw string) (expr.Prepared[*xtree.Node], error) {
	p, err := ParsePath(raw)
	if err != nil {
		return expr.Prepared[*xtree.Node]{}, err
	}

	return expr.Prepared[*xtree.Node]{
		// Every match, not just the first. A path naming no index addresses all of them, because a document carrying
		// several id elements should match a filter written against any of them - the same rule the v2 resolver applies
		// to field repetitions.
		Candidates: func(root *xtree.Node) []expr.Value {
			vals := pathValues(p, root)
			out := make([]expr.Value, 0, len(vals))
			for _, v := range vals {
				out = append(out, expr.Value{Text: v, Empty: strings.TrimSpace(v) == ""})
			}
			return out
		},

		Presence: func(root *xtree.Node) (int, bool) {
			// Exists is not len(values) > 0. An element present carrying nothing but a nullFlavor exists and has no
			// value, and a filter that treated those the same could not express "the sender said they asked and the
			// patient did not know".
			if !p.Exists(root) {
				return 0, true
			}

			vals := pathValues(p, root)
			if len(vals) == 0 {
				return 1, true
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

		Single: func(root *xtree.Node) string {
			vals := pathValues(p, root)
			if len(vals) == 0 {
				return ""
			}
			return vals[0]
		},
	}, nil
}

// Semantics states where v3 comparisons differ from v2 and X12.
//
// All three of these were deliberate choices in the evaluator this replaced, each with a test that says so. They are the
// entire behavioural difference between the two grammars; everything else was identical.
func (Resolver) Semantics() expr.Semantics {
	return expr.Semantics{
		// Ordered values in v3 are timestamps written YYYYMMDD, where text order and time order agree. Numeric would
		// break on a timezone offset.
		OrderAsText: true,

		// An absent element and an empty one are different clinical facts. A missing deceasedTime must not match == "".
		AbsentMatchesNothing: true,

		// Quotes required, so a misspelled keyword or a path written as a bare word is refused rather than compiled into
		// a comparison against a literal that never matches.
		RequireQuotedValues: true,
	}
}

// pathValues returns what a person would call the values a v3 path addresses.
//
// # Why this is not simply Path.Values
//
// In v3 a value usually lives in an attribute. <birthTime value="19551014"/> has no text at all, and
// <administrativeGenderCode code="F"/> has none either. Path.Values reads element text for a bare path, deliberately: it
// is what the path picker and the path checker use, and there "the element's text" has to mean exactly that or a displayed
// value would not match what a path resolves to.
//
// A filter is a different question. Somebody writing //birthTime == "19551014" is asking about the message, not about the
// notation, and the honest answer is that the document does carry that birth date.
//
// # A bug this fixed
//
// The v3 filter this replaced had exactly one of its operators reading values this way - empty - and its comment recorded
// the mistake as "the mistake v3 invites". The fix was never carried across to ==, !=, matches, in or the ordering
// operators, so //birthTime empty correctly reported a value present while //birthTime == "19551014" could not match it.
// Every comparison against a v3 attribute value silently answered false, which for a filter means every message excluded
// or every message let through, with nothing to say why. Proven against the old implementation before this was changed.
//
// A path that names an attribute explicitly is taken exactly as written, which is why //birthTime@value and //birthTime
// are both available and both honest.
func pathValues(p Path, root *xtree.Node) []string {
	if p.Attribute != "" {
		// Explicit, so it is taken literally.
		return p.Values(root)
	}

	var out []string
	for _, node := range p.Resolve(root) {
		if v, ok := elementValue(node); ok {
			out = append(out, v)
		}
	}
	return out
}

// elementValue returns what a person would call the value of a v3 element.
//
// Text if there is any, then the value attribute, then code. Those three cover essentially everything: <family>X</family>
// carries text, <birthTime value="..."/> and <administrativeGenderCode code="F"/> carry attributes, and an element with a
// nullFlavor carries none of them - which is the answer that makes "empty" mean what it says.
func elementValue(n *xtree.Node) (string, bool) {
	return ElementValue(n)
}

// ElementValue is elementValue, exported because it is a property of XML rather than of v3.
//
// SCRIPT needs exactly this rule and it would be the same three lines copied. Copying it is how the original bug spread: the
// knowledge that a value can live in an attribute existed in one operator and not the other eight, and every comparison
// against an attribute silently answered false. One exported implementation means a second XML format cannot inherit half of
// it.
//
// The order is text, then the value attribute, then code. Those cover essentially everything: <family>X</family> carries text,
// <birthTime value="..."/> and <administrativeGenderCode code="F"/> carry attributes, and an element with only a nullFlavor
// carries none - which is what makes "empty" mean what it says.
func ElementValue(n *xtree.Node) (string, bool) {
	if n == nil {
		return "", false
	}

	if text := strings.TrimSpace(n.Text); text != "" {
		return text, true
	}
	for _, name := range []string{"value", "code"} {
		if v, ok := n.Attr(name); ok && v != "" {
			return v, true
		}
	}

	return "", false
}

// PathValues returns every value a path addresses, reading attributes as well as element text.
//
// Exported for the same reason as ElementValue: it is the pairing of a path with the value rule, and a second XML format that
// resolved paths itself while reading values differently would reintroduce the split that caused the original defect.
func PathValues(p Path, root *xtree.Node) []string {
	return pathValues(p, root)
}

// Operators contributes nullflavor, which is v3's and nobody else's.
//
// An HL7 v3 element can say why it has no value rather than simply being absent: ASKU for asked but unknown, NI for no
// information, MSK for masked. That is a real clinical distinction - a birth date nobody asked for is not the same as one
// the patient declined to give - and no other format has anything like it.
//
// Both spellings, because the standard writes nullFlavor and half the world spells it the other way, and a filter that
// failed to parse over a u would be a bad first experience.
func (Resolver) Operators() []expr.Operator[*xtree.Node] {
	nullFlavor := func(path, want string) (func(*xtree.Node) (bool, error), error) {
		p, err := ParsePath(path)
		if err != nil {
			return nil, err
		}

		return func(root *xtree.Node) (bool, error) {
			got, ok := p.NullFlavor(root)
			if !ok {
				return false, nil
			}

			// Case-insensitive, because null flavour codes are defined in upper case and senders write them both ways. A
			// filter that missed "asku" would silently stop matching for one sender's messages.
			return strings.EqualFold(got, want), nil
		}, nil
	}

	return []expr.Operator[*xtree.Node]{
		{Keyword: "nullflavor", Compile: nullFlavor},
		{Keyword: "nullflavour", Compile: nullFlavor},
	}
}

// Filter is a compiled v3 filter.
//
// Still its own type rather than expr.Expr[*xtree.Node] directly, because Match passes on a nil receiver and the engine
// depends on that. A bare interface would be nil-checkable but every call site would have to do it, and that is where a
// nil check gets forgotten and every message gets dropped.
type Filter struct {
	expr expr.Expr[*xtree.Node]
	raw  string
}

// ParseFilter compiles a filter expression over a v3 document.
//
// A blank filter is no filter, not an error: it returns nil, and a nil Filter passes everything. A channel with the key
// present and empty means the same as a channel without the key, which is what somebody clearing the box in the editor
// intends.
func ParseFilter(src string) (*Filter, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}

	e, err := expr.ParseFor(src, Resolver{})
	if err != nil {
		return nil, err
	}
	return &Filter{expr: e, raw: strings.TrimSpace(src)}, nil
}

// String returns the filter as written.
func (f *Filter) String() string {
	if f == nil {
		return ""
	}
	return f.raw
}

// Match reports whether a message passes.
//
// A nil filter passes everything, so a channel with no filter needs no special case at the call site - which is where a
// nil check gets forgotten and every message gets dropped.
func (f *Filter) Match(root *xtree.Node) (bool, error) {
	if f == nil || f.expr == nil {
		return true, nil
	}
	if root == nil {
		return false, fmt.Errorf("there is no message to filter")
	}

	return f.expr.Eval(root)
}

// Paths returns every path the filter reads, deduplicated and in the order they appear.
//
// For the interface, so a filter can be shown against a sample message with its paths highlighted, and so a channel
// summary can say which fields it depends on without anybody parsing the expression again.
func (f *Filter) Paths() []string {
	if f == nil || f.expr == nil {
		return nil
	}
	return f.expr.Paths()
}
