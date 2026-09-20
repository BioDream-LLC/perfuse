package hl7

import (
	"fmt"
	"strconv"
	"strings"
)

// Paths address one place in a message using the notation people already write
// in interface specifications and support tickets:
//
//	MSH-9.2          message trigger event
//	PID-5.1          patient family name
//	PID-5.1.1        first subcomponent of the family name
//	PID-3(2).1       second repetition of the patient identifier list
//	OBX(3)-5         observation value in the third OBX segment
//	OBX(3)-5(2).1    second repetition of that value
//
// A dot may be used in place of the dash after the segment name, since both are
// in common use.

// Path is a parsed reference to one location in a message.
type Path struct {
	Segment       string
	SegmentOccurs int // 1-based
	Field         int // 0 means the whole segment
	// FieldRepeat is 0 when the path did not name a repetition, which means the
	// whole field including every repetition. It is not the same as 1: asking
	// for PID-3 should show you that it repeats, not hide the second identifier.
	FieldRepeat  int
	Component    int // 0 means unspecified
	Subcomponent int // 0 means unspecified
}

// String renders the path in canonical form.
func (p Path) String() string {
	var sb strings.Builder
	sb.WriteString(p.Segment)
	if p.SegmentOccurs > 1 {
		fmt.Fprintf(&sb, "(%d)", p.SegmentOccurs)
	}
	if p.Field == 0 {
		return sb.String()
	}
	fmt.Fprintf(&sb, "-%d", p.Field)
	// Rendered whenever it is set, including 1. A repetition of 1 means the first
	// repetition alone, while no repetition means the whole field with every
	// repetition in it, so dropping "(1)" changes what the path resolves to. That
	// matters because these strings are shown in the interface and in shadow diffs,
	// and get pasted back into channel files.
	if p.FieldRepeat > 0 {
		fmt.Fprintf(&sb, "(%d)", p.FieldRepeat)
	}
	if p.Component > 0 {
		fmt.Fprintf(&sb, ".%d", p.Component)
	}
	if p.Subcomponent > 0 {
		fmt.Fprintf(&sb, ".%d", p.Subcomponent)
	}
	return sb.String()
}

// ParsePath reads a path expression.
func ParsePath(s string) (Path, error) {
	p := Path{SegmentOccurs: 1}
	rest := strings.TrimSpace(s)
	if rest == "" {
		return p, fmt.Errorf("hl7: empty path")
	}

	// Segment name, three characters by the standard, but accept any run of
	// letters and digits so that Z-segments and malformed names still address.
	i := 0
	for i < len(rest) && isNameByte(rest[i]) {
		i++
	}
	if i == 0 {
		return p, fmt.Errorf("hl7: path %q does not start with a segment name", s)
	}
	p.Segment = strings.ToUpper(rest[:i])
	rest = rest[i:]

	// Optional segment occurrence.
	if n, remainder, ok, err := takeIndex(rest); err != nil {
		return p, fmt.Errorf("hl7: path %q: segment occurrence: %w", s, err)
	} else if ok {
		p.SegmentOccurs = n
		rest = remainder
	}

	if rest == "" {
		return p, nil
	}

	// Separator between segment and field. Both - and . are accepted.
	if rest[0] != '-' && rest[0] != '.' {
		return p, fmt.Errorf("hl7: path %q: expected - or . after the segment name", s)
	}
	rest = rest[1:]

	// Field number.
	num, remainder, err := takeNumber(rest)
	if err != nil {
		return p, fmt.Errorf("hl7: path %q: field number: %w", s, err)
	}
	p.Field = num
	rest = remainder

	// Optional field repetition.
	if n, remainder, ok, err := takeIndex(rest); err != nil {
		return p, fmt.Errorf("hl7: path %q: field repetition: %w", s, err)
	} else if ok {
		p.FieldRepeat = n
		rest = remainder
	}

	// Component and subcomponent.
	for _, target := range []*int{&p.Component, &p.Subcomponent} {
		if rest == "" {
			return p, nil
		}
		if rest[0] != '.' && rest[0] != '-' {
			return p, fmt.Errorf("hl7: path %q: unexpected %q", s, rest)
		}
		rest = rest[1:]
		num, remainder, err := takeNumber(rest)
		if err != nil {
			return p, fmt.Errorf("hl7: path %q: %w", s, err)
		}
		*target = num
		rest = remainder
	}

	if rest != "" {
		return p, fmt.Errorf("hl7: path %q: trailing %q", s, rest)
	}
	return p, nil
}

func takeNumber(s string) (int, string, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, s, fmt.Errorf("expected a number, found %q", s)
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, s, err
	}
	if n < 1 {
		return 0, s, fmt.Errorf("index %d is not valid; HL7 numbering starts at 1", n)
	}
	return n, s[i:], nil
}

// takeIndex reads an optional (n) or [n] index.
func takeIndex(s string) (n int, rest string, ok bool, err error) {
	if s == "" {
		return 0, s, false, nil
	}
	var closing byte
	switch s[0] {
	case '(':
		closing = ')'
	case '[':
		closing = ']'
	default:
		return 0, s, false, nil
	}

	end := strings.IndexByte(s, closing)
	if end < 0 {
		return 0, s, false, fmt.Errorf("unclosed %q", s[0])
	}
	n, remainder, err := takeNumber(s[1:end])
	if err != nil {
		return 0, s, false, err
	}
	if remainder != "" {
		return 0, s, false, fmt.Errorf("unexpected %q inside the index", remainder)
	}
	return n, s[end+1:], true, nil
}

func isNameByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// Value resolves a path against the message, returning a Value that does not
// exist if any part of the path is absent.
func (m *Message) Value(path string) (Value, error) {
	p, err := ParsePath(path)
	if err != nil {
		return Value{}, err
	}
	return m.ValueAt(p), nil
}

// ValueAt resolves an already-parsed path.
func (m *Message) ValueAt(p Path) Value {
	seg, ok := m.Segment(p.Segment, p.SegmentOccurs)
	if !ok {
		return Value{}
	}
	if p.Field == 0 {
		return Value{m: m, sp: m.segs[seg.idx].body}
	}

	v := seg.Field(p.Field)
	if !v.Exists() {
		return Value{}
	}

	// MSH-1 and MSH-2 hold delimiters, so splitting them on those same
	// delimiters would be meaningless.
	if p.Segment == "MSH" && p.Field <= 2 {
		return v
	}

	// Narrow to a repetition only when one was named. Addressing a component
	// without naming a repetition reads the first, which is how interface
	// specifications are conventionally written, and Value.Component does that.
	if p.FieldRepeat > 0 {
		v = v.Repeat(p.FieldRepeat)
	}
	if p.Component > 0 {
		v = v.Component(p.Component)
	}
	if p.Subcomponent > 0 {
		v = v.Subcomponent(p.Subcomponent)
	}
	return v
}

// Get resolves a path and returns its unescaped text. A path that addresses
// nothing returns an empty string, which is what an interface engine wants:
// absent and empty are the same thing when you are deciding where to route.
func (m *Message) Get(path string) (string, error) {
	v, err := m.Value(path)
	if err != nil {
		return "", err
	}
	return v.String(), nil
}

// MustGet is Get for paths that are known good, such as literals in code.
func (m *Message) MustGet(path string) string {
	s, err := m.Get(path)
	if err != nil {
		panic(err)
	}
	return s
}
