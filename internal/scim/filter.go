package scim

import (
	"fmt"
	"strings"
)

// Filters. An identity provider uses these constantly, and one shape above all others:
//
//	filter=userName eq "rturner@example.org"
//
// That is the lookup a provider performs before creating an account, to find out whether it already exists. If it comes back
// empty when the account does exist, the provider creates a second one - and then has two, disables the one it just made,
// and leaves the original enabled. A broken filter is therefore a deprovisioning failure wearing a provisioning costume.
//
// # Deliberately a small subset
//
// RFC 7644 defines a full expression language: nested groups, complex attribute filters with square brackets, and, or, not.
// Implementing all of it correctly is a real piece of work, and implementing it *almost* correctly is worse than not
// implementing it - a filter that is misparsed rather than refused returns the wrong users, and returning the wrong users to
// a provisioning system means it acts on the wrong accounts.
//
// So this handles the operators providers actually send and **refuses everything else with invalidFilter**, which is a
// response the specification defines and providers handle. Refusing is honest; guessing is not.
//
// What is supported: eq, ne, co, sw, ew, pr, and a single "and" or "or" joining two comparisons. Nothing nested.

// Filter is a parsed filter expression.
type Filter struct {
	// Comparisons are the individual comparisons.
	Comparisons []Comparison

	// Conjunction is "and" or "or" when there are two comparisons, empty when there is one.
	Conjunction string
}

// Comparison is one attribute comparison.
type Comparison struct {
	// Attribute is the attribute name, lower-cased for comparison.
	//
	// Lower-cased because SCIM attribute names are case-insensitive and providers are inconsistent: "userName",
	// "username" and "UserName" all arrive.
	Attribute string

	// Operator is the comparison: eq, ne, co, sw, ew, pr.
	Operator string

	// Value is the compared value, with its quotes removed. Empty for pr, which takes no value.
	Value string
}

// Supported operators.
const (
	OpEqual      = "eq"
	OpNotEqual   = "ne"
	OpContains   = "co"
	OpStartsWith = "sw"
	OpEndsWith   = "ew"
	OpPresent    = "pr"
)

// ErrUnsupportedFilter is returned for a filter this does not handle.
//
// A distinct error rather than a generic one, because it maps to SCIM's invalidFilter keyword and a 400 - which a provider
// understands, logs usefully, and does not retry for ever.
type ErrUnsupportedFilter struct {
	// Filter is the expression that was refused.
	Filter string

	// Why explains it, for the provisioning log a person will eventually read.
	Why string
}

// Error makes this an error.
func (e ErrUnsupportedFilter) Error() string {
	return fmt.Sprintf("this filter is not supported (%s): %s", e.Why, e.Filter)
}

// ParseFilter reads a SCIM filter expression.
//
// Returns ErrUnsupportedFilter for anything outside the subset, which the caller turns into a 400 with invalidFilter. That is
// deliberate and is the whole design of this file: a filter that is misparsed returns the wrong users, and returning the
// wrong users to a provisioning system means it acts on the wrong accounts.
func ParseFilter(expression string) (Filter, error) {
	raw := strings.TrimSpace(expression)
	if raw == "" {
		return Filter{}, nil
	}

	// Nesting is refused rather than attempted. A parenthesised group changes precedence, and getting precedence wrong
	// silently returns a different set of users than was asked for.
	if strings.ContainsAny(raw, "()[]") {
		return Filter{}, ErrUnsupportedFilter{
			Filter: raw,
			Why:    "grouped or complex-attribute filters are not supported, and guessing at precedence would return the wrong accounts",
		}
	}
	if strings.HasPrefix(strings.ToLower(raw), "not ") {
		return Filter{}, ErrUnsupportedFilter{Filter: raw, Why: "negation is not supported"}
	}

	// Split on a single conjunction. Split on the keyword surrounded by spaces so that an attribute or value containing
	// the letters "and" is not mistaken for one.
	for _, conjunction := range []string{" and ", " or "} {
		lower := strings.ToLower(raw)
		if i := indexOutsideQuotes(lower, conjunction); i >= 0 {
			left := raw[:i]
			right := raw[i+len(conjunction):]

			// A second conjunction means nesting, whose precedence this does not attempt.
			if indexOutsideQuotes(strings.ToLower(right), " and ") >= 0 ||
				indexOutsideQuotes(strings.ToLower(right), " or ") >= 0 {
				return Filter{}, ErrUnsupportedFilter{
					Filter: raw,
					Why:    "more than two conditions are not supported, because their precedence would have to be guessed",
				}
			}

			first, err := parseComparison(left)
			if err != nil {
				return Filter{}, err
			}
			second, err := parseComparison(right)
			if err != nil {
				return Filter{}, err
			}

			return Filter{
				Comparisons: []Comparison{first, second},
				Conjunction: strings.TrimSpace(conjunction),
			}, nil
		}
	}

	one, err := parseComparison(raw)
	if err != nil {
		return Filter{}, err
	}

	return Filter{Comparisons: []Comparison{one}}, nil
}

// parseComparison reads a single comparison.
func parseComparison(raw string) (Comparison, error) {
	raw = strings.TrimSpace(raw)

	fields := splitOutsideQuotes(raw)
	if len(fields) == 2 {
		// The presence operator takes no value: `userName pr`.
		if strings.EqualFold(fields[1], OpPresent) {
			return Comparison{Attribute: strings.ToLower(fields[0]), Operator: OpPresent}, nil
		}

		return Comparison{}, ErrUnsupportedFilter{Filter: raw, Why: "an operator with no value"}
	}
	if len(fields) < 3 {
		return Comparison{}, ErrUnsupportedFilter{Filter: raw, Why: "not a comparison"}
	}

	attribute := strings.ToLower(fields[0])
	operator := strings.ToLower(fields[1])
	value := strings.Join(fields[2:], " ")

	switch operator {
	case OpEqual, OpNotEqual, OpContains, OpStartsWith, OpEndsWith:
	case "gt", "ge", "lt", "le":
		// Ordering comparisons are refused rather than approximated. They are defined over dates and numbers, and
		// comparing those as strings gives wrong answers that look right - "10" sorts before "9".
		return Comparison{}, ErrUnsupportedFilter{
			Filter: raw,
			Why:    "ordering comparisons are not supported; comparing dates or numbers as text gives wrong answers that look plausible",
		}
	default:
		return Comparison{}, ErrUnsupportedFilter{Filter: raw, Why: "unrecognised operator " + operator}
	}

	unquoted, err := unquote(value)
	if err != nil {
		return Comparison{}, ErrUnsupportedFilter{Filter: raw, Why: err.Error()}
	}

	return Comparison{Attribute: attribute, Operator: operator, Value: unquoted}, nil
}

// unquote removes surrounding double quotes and unescapes what SCIM permits inside them.
func unquote(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		// An unquoted value is accepted, because providers send booleans and numbers bare: `active eq true`.
		if strings.ContainsAny(value, `"`) {
			return "", fmt.Errorf("a value with an unbalanced quotation mark")
		}

		return value, nil
	}

	inner := value[1 : len(value)-1]

	// SCIM permits backslash escapes inside a quoted value. Unescaped here rather than left alone, because a username
	// containing a quotation mark would otherwise never match the account it names.
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			i++
			switch inner[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(inner[i])
			}

			continue
		}
		b.WriteByte(inner[i])
	}

	return b.String(), nil
}

// indexOutsideQuotes finds a substring, ignoring occurrences inside a quoted value.
//
// Needed because a username can contain the word "and" - "brandon@example.org" contains it - and splitting on that would
// turn one comparison into two nonsensical ones. The failure would be a lookup that returns nothing, and a lookup that
// returns nothing means a duplicate account.
func indexOutsideQuotes(haystack, needle string) int {
	inQuotes := false
	for i := 0; i < len(haystack); i++ {
		switch {
		case haystack[i] == '\\' && inQuotes:
			i++
		case haystack[i] == '"':
			inQuotes = !inQuotes
		case !inQuotes && strings.HasPrefix(haystack[i:], needle):
			return i
		}
	}

	return -1
}

// splitOutsideQuotes splits on whitespace, keeping a quoted value together.
func splitOutsideQuotes(s string) []string {
	var (
		fields  []string
		current strings.Builder
		inQuote bool
	)

	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && inQuote:
			current.WriteByte(c)
			if i+1 < len(s) {
				i++
				current.WriteByte(s[i])
			}
		case c == '"':
			inQuote = !inQuote
			current.WriteByte(c)
		case (c == ' ' || c == '\t') && !inQuote:
			flush()
		default:
			current.WriteByte(c)
		}
	}
	flush()

	return fields
}

// UserNameEquals returns the username a filter is looking for, when that is all it asks.
//
// The overwhelmingly common case: a provider checking whether an account exists before creating one. Recognised specially so
// that the store can be asked a single indexed question rather than every account being listed and compared - which matters
// once a tenant has thousands of them, because a provisioning run touches every one.
func (f Filter) UserNameEquals() (string, bool) {
	if len(f.Comparisons) != 1 {
		return "", false
	}
	c := f.Comparisons[0]
	if c.Attribute != "username" || c.Operator != OpEqual {
		return "", false
	}

	return c.Value, true
}

// ExternalIDEquals returns the external identifier a filter is looking for, when that is all it asks.
//
// The other lookup providers perform, and the more reliable of the two: the external identifier is stable across a rename,
// so matching on it correctly is what stops a marriage producing two accounts.
func (f Filter) ExternalIDEquals() (string, bool) {
	if len(f.Comparisons) != 1 {
		return "", false
	}
	c := f.Comparisons[0]
	if c.Attribute != "externalid" || c.Operator != OpEqual {
		return "", false
	}

	return c.Value, true
}

// Matches reports whether a candidate satisfies the filter.
//
// Attribute values are supplied by the caller as a map with lower-case keys, so this package does not need to know how
// Perfuse stores an account.
//
// String comparison is case-insensitive for eq. SCIM says usernames are case-insensitive, and a provider that sends
// "RTurner" for an account stored as "rturner" would otherwise find nothing and create a duplicate.
func (f Filter) Matches(attributes map[string]string) bool {
	if len(f.Comparisons) == 0 {
		return true
	}

	results := make([]bool, 0, len(f.Comparisons))
	for _, c := range f.Comparisons {
		value, present := attributes[c.Attribute]

		switch c.Operator {
		case OpPresent:
			results = append(results, present && value != "")
		case OpEqual:
			results = append(results, present && strings.EqualFold(value, c.Value))
		case OpNotEqual:
			results = append(results, !present || !strings.EqualFold(value, c.Value))
		case OpContains:
			results = append(results, present && strings.Contains(strings.ToLower(value), strings.ToLower(c.Value)))
		case OpStartsWith:
			results = append(results, present && strings.HasPrefix(strings.ToLower(value), strings.ToLower(c.Value)))
		case OpEndsWith:
			results = append(results, present && strings.HasSuffix(strings.ToLower(value), strings.ToLower(c.Value)))
		default:
			results = append(results, false)
		}
	}

	if len(results) == 1 {
		return results[0]
	}

	if strings.EqualFold(f.Conjunction, "or") {
		return results[0] || results[1]
	}

	return results[0] && results[1]
}
