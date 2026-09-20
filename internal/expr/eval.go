package expr

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Evaluation never fails on missing data. A path that is not in the message
// yields an empty value, so a comparison against it is simply false rather than
// an error. Filters run on every message that arrives, including malformed ones,
// and a filter that errors instead of deciding leaves a message stuck.

// candidates expands a path into every value it addresses.
//
// anyCandidate reports whether any addressed value satisfies match. When the path addresses nothing, match is given the
// empty string, so a comparison against "" can test for a missing field without a separate keyword.
func anyCandidate[M any](m M, cands func(M) []Value, absentFails bool, match func(string) bool) bool {
	vs := cands(m)
	if len(vs) == 0 {
		// A format may refuse to treat an absent path as an empty value. See Semantics.AbsentMatchesNothing.
		if absentFails {
			return false
		}
		return match("")
	}
	for _, v := range vs {
		if match(v.Text) {
			return true
		}
	}
	return false
}

// ---------- logical nodes ----------

type andExpr[M any] struct{ left, right Expr[M] }

func (e *andExpr[M]) Eval(m M) (bool, error) {
	l, err := e.left.Eval(m)
	if err != nil {
		return false, err
	}
	// Short circuit, so the right side is not evaluated when it cannot matter.
	if !l {
		return false, nil
	}
	return e.right.Eval(m)
}

func (e *andExpr[M]) String() string  { return e.left.String() + " and " + e.right.String() }
func (e *andExpr[M]) Paths() []string { return mergePaths(e.left, e.right) }

type orExpr[M any] struct{ left, right Expr[M] }

func (e *orExpr[M]) Eval(m M) (bool, error) {
	l, err := e.left.Eval(m)
	if err != nil {
		return false, err
	}
	if l {
		return true, nil
	}
	return e.right.Eval(m)
}

func (e *orExpr[M]) String() string  { return e.left.String() + " or " + e.right.String() }
func (e *orExpr[M]) Paths() []string { return mergePaths(e.left, e.right) }

type notExpr[M any] struct{ inner Expr[M] }

func (e *notExpr[M]) Eval(m M) (bool, error) {
	v, err := e.inner.Eval(m)
	if err != nil {
		return false, err
	}
	return !v, nil
}

func (e *notExpr[M]) String() string  { return "not " + e.inner.String() }
func (e *notExpr[M]) Paths() []string { return e.inner.Paths() }

type groupExpr[M any] struct{ inner Expr[M] }

func (e *groupExpr[M]) Eval(m M) (bool, error) { return e.inner.Eval(m) }
func (e *groupExpr[M]) String() string         { return "(" + e.inner.String() + ")" }
func (e *groupExpr[M]) Paths() []string        { return e.inner.Paths() }

// ---------- leaf nodes ----------

type existsExpr[M any] struct {
	presence func(M) (int, bool)
	raw      string
}

// Eval reports whether the path is present in the message, whether or not it has
// content. Present but empty and absent are different things: in an A08 update,
// an empty field means no change and an absent one means the sender never sent
// that field at all.
func (e *existsExpr[M]) Eval(m M) (bool, error) {
	n, _ := e.presence(m)
	return n > 0, nil
}

func (e *existsExpr[M]) String() string  { return e.raw + " exists" }
func (e *existsExpr[M]) Paths() []string { return []string{e.raw} }

type emptyExpr[M any] struct {
	presence func(M) (int, bool)
	raw      string
}

// Eval reports whether the path has no content. An absent path is empty, and so
// is HL7's explicit null.
func (e *emptyExpr[M]) Eval(m M) (bool, error) {
	_, allEmpty := e.presence(m)
	return allEmpty, nil
}

func (e *emptyExpr[M]) String() string  { return e.raw + " empty" }
func (e *emptyExpr[M]) Paths() []string { return []string{e.raw} }

type matchExpr[M any] struct {
	cands       func(M) []Value
	raw         string
	re          *regexp.Regexp
	absentFails bool
}

func (e *matchExpr[M]) Eval(m M) (bool, error) {
	return anyCandidate(m, e.cands, e.absentFails, e.re.MatchString), nil
}

func (e *matchExpr[M]) String() string  { return fmt.Sprintf("%s matches %q", e.raw, e.re.String()) }
func (e *matchExpr[M]) Paths() []string { return []string{e.raw} }

type inExpr[M any] struct {
	cands       func(M) []Value
	raw         string
	values      []string
	absentFails bool
}

// Eval reports whether any repetition of the field equals one of the listed
// values.
//
// Repetitions matter here. A patient identifier list carries several
// identifiers, and a filter asking whether the assigning authority is in a set
// means any of them, not just the first.
func (e *inExpr[M]) Eval(m M) (bool, error) {
	return anyCandidate(m, e.cands, e.absentFails, func(got string) bool {
		for _, want := range e.values {
			if got == want {
				return true
			}
		}
		return false
	}), nil
}

func (e *inExpr[M]) String() string {
	quoted := make([]string, 0, len(e.values))
	for _, v := range e.values {
		quoted = append(quoted, strconv.Quote(v))
	}
	return fmt.Sprintf("%s in [%s]", e.raw, strings.Join(quoted, ", "))
}

func (e *inExpr[M]) Paths() []string { return []string{e.raw} }

type compareExpr[M any] struct {
	cands  func(M) []Value
	single func(M) string
	raw    string
	op     string
	value  string

	// sem carries the format's comparison choices.
	sem Semantics
}

// Eval compares the path against a literal.
//
// Equality and inequality consider every repetition, so a message whose PID-3
// carries two identifiers matches if either does. Ordering comparisons are
// numeric and only apply to the addressed value; comparing text with > is
// meaningless in HL7, where a value that is not a number means the sender sent
// something unexpected, and the honest answer is false rather than an error.
func (e *compareExpr[M]) Eval(m M) (bool, error) {
	equals := func() bool {
		return anyCandidate(m, e.cands, e.sem.AbsentMatchesNothing, func(s string) bool { return s == e.value })
	}

	switch e.op {
	case "==":
		return equals(), nil

	case "!=":
		// Not equal means no addressed value equals it. Otherwise a field
		// carrying two identifiers would satisfy == and != at the same time.
		//
		// True for an absent path under either rule: whatever a field that was never sent holds, it is not this value.
		// That is why != is the documented exception to AbsentMatchesNothing.
		if e.sem.AbsentMatchesNothing && len(e.cands(m)) == 0 {
			return true, nil
		}
		return !equals(), nil

	case ">", "<", ">=", "<=":
		// Ordering applies to a single value, so it uses the addressed value
		// directly rather than every repetition.
		if e.sem.AbsentMatchesNothing && len(e.cands(m)) == 0 {
			// Nothing is greater or less than anything, or a channel would route on a field its sender never sent.
			return false, nil
		}
		return e.compareOrder(e.single(m))

	default:
		return false, fmt.Errorf("expr: unknown operator %q", e.op)
	}
}

// compareOrder applies >, <, >= or <= as the format asked.
func (e *compareExpr[M]) compareOrder(got string) (bool, error) {
	if e.sem.OrderAsText {
		return e.compareText(got), nil
	}
	return e.compareNumeric(got)
}

// compareText compares as strings, for formats whose ordered values are timestamps.
func (e *compareExpr[M]) compareText(got string) bool {
	switch e.op {
	case ">":
		return got > e.value
	case "<":
		return got < e.value
	case ">=":
		return got >= e.value
	default:
		return got <= e.value
	}
}

func (e *compareExpr[M]) compareNumeric(got string) (bool, error) {
	want, err := strconv.ParseFloat(e.value, 64)
	if err != nil {
		return false, fmt.Errorf("expr: %s %s %q: the right side is not a number",
			e.raw, e.op, e.value)
	}
	have, err := strconv.ParseFloat(strings.TrimSpace(got), 64)
	if err != nil {
		// The message did not carry a number where one was expected. Reporting
		// false keeps the filter deciding instead of stalling the message.
		return false, nil
	}

	switch e.op {
	case ">":
		return have > want, nil
	case "<":
		return have < want, nil
	case ">=":
		return have >= want, nil
	case "<=":
		return have <= want, nil
	}
	return false, fmt.Errorf("expr: unknown operator %q", e.op)
}

func (e *compareExpr[M]) String() string {
	return fmt.Sprintf("%s %s %s", e.raw, e.op, strconv.Quote(e.value))
}

func (e *compareExpr[M]) Paths() []string { return []string{e.raw} }

func mergePaths[M any](exprs ...Expr[M]) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range exprs {
		for _, p := range e.Paths() {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// opExpr is a comparison a format contributed rather than one the shared grammar defines.
type opExpr[M any] struct {
	test    func(M) (bool, error)
	raw     string
	keyword string
	value   string
}

func (e *opExpr[M]) Eval(m M) (bool, error) { return e.test(m) }

func (e *opExpr[M]) String() string {
	return fmt.Sprintf("%s %s %s", e.raw, e.keyword, strconv.Quote(e.value))
}

func (e *opExpr[M]) Paths() []string { return []string{e.raw} }
