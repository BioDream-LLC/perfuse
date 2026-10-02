package x12

import (
	"fmt"
	"strconv"
	"strings"
)

// Path addressing for X12.
//
// Two notations are accepted, because two different people write these.
//
// The canonical form matches the HL7 paths already in this codebase:
//
//	CLM            the whole segment, as written
//	CLM-1          element 1
//	CLM-5.1        element 5, component 1
//	NM1(2)-3       the second NM1 segment, element 3
//	REF-2(3)       element 2, third repetition
//
// The native form is what every X12 implementation guide uses:
//
//	CLM01          element 1
//	NM103          element 3
//	SV101          element 1 of SV1
//
// Accepting the native form matters more than it looks. The people who configure
// claims interfaces read 837 implementation guides all day, and those guides write
// CLM01, never CLM-1. Refusing their notation means every path gets translated by hand
// on the way in, and hand translation of numbers is where transposition errors come
// from. A claim routed on CLM02 instead of CLM01 is a real outage.
//
// # How the two are told apart without a dictionary
//
// X12 segment identifiers are two or three characters and element numbers are always
// written as exactly two digits. So a bare identifier is read as native form only when
// the last two characters are digits and what remains is a plausible segment id of two
// or three characters.
//
// That resolves the cases that look dangerous. "L11" is a real segment: stripping two
// digits leaves "L", which is too short to be a segment, so it reads as the whole L11
// segment. "L1101" leaves "L11", which is valid, so it reads as L11 element 1. "NM103"
// leaves "NM1" and reads as element 3. Each of those is what the person meant.
//
// Ambiguity is refused rather than guessed at, because a path that silently addresses
// the wrong element produces plausible output with the wrong values in it, and that
// survives review.

// Path is a parsed reference to part of an interchange.
type Path struct {
	// Segment is the segment identifier, for example "CLM".
	Segment string
	// SegmentOccurs is which occurrence, counting from 1.
	//
	// X12 repeats segments constantly - NM1 appears in every loop of an 837 - so this
	// is not the rarity it is in HL7. A path without an occurrence means the first.
	SegmentOccurs int
	// Element is which element, counting from 1. Zero means the whole segment.
	Element int
	// Repeat is which repetition of the element, counting from 1.
	//
	// Zero means the whole element including every repetition, which is not the same
	// as 1. Asking for REF-2 should show you that it repeats rather than quietly
	// handing back the first value and hiding the rest.
	Repeat int
	// Component is which component of a composite element, counting from 1. Zero
	// means the whole element.
	//
	// There is no subcomponent. X12 composites are one level deep, unlike HL7 fields.
	Component int
}

// String renders the path in canonical form.
func (p Path) String() string {
	var sb strings.Builder
	sb.WriteString(p.Segment)
	if p.SegmentOccurs > 1 {
		fmt.Fprintf(&sb, "(%d)", p.SegmentOccurs)
	}
	if p.Element == 0 {
		return sb.String()
	}
	fmt.Fprintf(&sb, "-%d", p.Element)
	// Rendered whenever it is set, including 1, because a repetition of 1 means the
	// first repetition alone while no repetition means the whole element. Dropping
	// "(1)" would change what the path resolves to.
	if p.Repeat > 0 {
		fmt.Fprintf(&sb, "(%d)", p.Repeat)
	}
	if p.Component > 0 {
		fmt.Fprintf(&sb, ".%d", p.Component)
	}
	return sb.String()
}

// ParsePath reads a path expression in either notation.
func ParsePath(s string) (Path, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return Path{}, fmt.Errorf("x12: the path is empty")
	}

	p := Path{SegmentOccurs: 1}

	// The segment identifier runs to the first punctuation.
	i := 0
	for i < len(s) && isSegmentByte(s[i]) {
		i++
	}
	head := s[:i]
	rest := s[i:]

	if head == "" {
		return Path{}, fmt.Errorf("x12: %q does not start with a segment identifier", orig)
	}

	// Native form applies only to a bare identifier. Once the path carries an explicit
	// element or occurrence, the identifier is the identifier and nothing is inferred.
	if rest == "" {
		seg, elem, native := splitNative(head)
		if native {
			p.Segment, p.Element = seg, elem
			return p, nil
		}
		if err := validateSegmentID(head, orig); err != nil {
			return Path{}, err
		}
		p.Segment = head
		return p, nil
	}

	if err := validateSegmentID(head, orig); err != nil {
		return Path{}, err
	}
	p.Segment = head

	// An optional segment occurrence.
	if n, r, ok, err := takeIndex(rest); err != nil {
		return Path{}, fmt.Errorf("x12: %q: segment occurrence: %w", orig, err)
	} else if ok {
		if n < 1 {
			return Path{}, fmt.Errorf("x12: %q: a segment occurrence starts at 1", orig)
		}
		p.SegmentOccurs, rest = n, r
	}

	if rest == "" {
		return p, nil
	}

	if rest[0] != '-' {
		return Path{}, fmt.Errorf("x12: %q: expected %q before the element number, found %q", orig, "-", string(rest[0]))
	}
	rest = rest[1:]

	n, rest, err := takePathNumber(rest)
	if err != nil {
		return Path{}, fmt.Errorf("x12: %q: element number: %w", orig, err)
	}
	if n < 1 {
		return Path{}, fmt.Errorf("x12: %q: element numbers start at 1", orig)
	}
	p.Element = n

	// An optional element repetition.
	if n, r, ok, err := takeIndex(rest); err != nil {
		return Path{}, fmt.Errorf("x12: %q: element repetition: %w", orig, err)
	} else if ok {
		if n < 1 {
			return Path{}, fmt.Errorf("x12: %q: a repetition starts at 1", orig)
		}
		p.Repeat, rest = n, r
	}

	if rest == "" {
		return p, nil
	}

	if rest[0] != '.' {
		return Path{}, fmt.Errorf("x12: %q: expected %q before the component number, found %q", orig, ".", string(rest[0]))
	}
	rest = rest[1:]

	n, rest, err = takePathNumber(rest)
	if err != nil {
		return Path{}, fmt.Errorf("x12: %q: component number: %w", orig, err)
	}
	if n < 1 {
		return Path{}, fmt.Errorf("x12: %q: component numbers start at 1", orig)
	}
	p.Component = n

	if rest != "" {
		// Most often somebody has written an HL7 subcomponent. X12 composites are one
		// level deep, and saying so is more use than "unexpected input".
		if rest[0] == '.' {
			return Path{}, fmt.Errorf("x12: %q: X12 composite elements have no subcomponents, unlike HL7 fields", orig)
		}
		return Path{}, fmt.Errorf("x12: %q: unexpected %q at the end of the path", orig, rest)
	}
	return p, nil
}

// splitNative reads implementation-guide notation such as CLM01.
//
// Reports false when the identifier is not in that form, leaving it to be treated as a
// whole-segment reference.
func splitNative(head string) (segment string, element int, ok bool) {
	if len(head) < 4 {
		// Needs at least a two-character segment id and two digits.
		return "", 0, false
	}
	digits := head[len(head)-2:]
	if !isDigit(digits[0]) || !isDigit(digits[1]) {
		return "", 0, false
	}
	seg := head[:len(head)-2]
	if len(seg) < 2 || len(seg) > 3 {
		return "", 0, false
	}
	if !isSegmentID(seg) {
		return "", 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return "", 0, false
	}
	return seg, n, true
}

// validateSegmentID refuses an identifier that cannot be an X12 segment.
//
// Checked at parse time so a typo in a channel file is caught when the file loads
// rather than at three in the morning when a path silently resolves to nothing.
func validateSegmentID(id, orig string) error {
	if len(id) < 2 || len(id) > 3 {
		return fmt.Errorf("x12: %q: %q is not a segment identifier; X12 identifiers are two or three characters", orig, id)
	}
	if !isSegmentID(id) {
		return fmt.Errorf("x12: %q: %q is not a segment identifier; they start with a letter and contain only letters and digits", orig, id)
	}
	return nil
}

func isSegmentID(id string) bool {
	if id == "" {
		return false
	}
	if !isLetter(id[0]) {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !isLetter(id[i]) && !isDigit(id[i]) {
			return false
		}
	}
	return true
}

func isSegmentByte(c byte) bool { return isLetter(c) || isDigit(c) }
func isLetter(c byte) bool      { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool       { return c >= '0' && c <= '9' }

// takePathNumber reads a leading decimal number.
func takePathNumber(s string) (int, string, error) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == 0 {
		if s == "" {
			return 0, "", fmt.Errorf("a number is missing")
		}
		return 0, "", fmt.Errorf("expected a number, found %q", s)
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, "", fmt.Errorf("%q is not a usable number", s[:i])
	}
	return n, s[i:], nil
}

// takeIndex reads a parenthesised index such as "(2)".
func takeIndex(s string) (n int, rest string, ok bool, err error) {
	if s == "" || s[0] != '(' {
		return 0, s, false, nil
	}
	end := strings.IndexByte(s, ')')
	if end < 0 {
		return 0, s, false, fmt.Errorf("a %q is missing", ")")
	}
	body := s[1:end]
	if body == "" {
		return 0, s, false, fmt.Errorf("the index is empty")
	}
	v, convErr := strconv.Atoi(body)
	if convErr != nil {
		return 0, s, false, fmt.Errorf("%q is not a number", body)
	}
	return v, s[end+1:], true, nil
}

// Get resolves a path expression against the interchange.
func (m *Message) Get(path string) (string, error) {
	p, err := ParsePath(path)
	if err != nil {
		return "", err
	}
	return m.ValueAt(p), nil
}

// ValueAt resolves a parsed path.
//
// A path that addresses something absent returns an empty string rather than an error.
// Absence is ordinary in X12 - trailing elements are routinely omitted and whole
// segments are situational - so treating it as a fault would make every caller write
// the same check, and the check would be wrong: an empty element and a missing one mean
// the same thing to a receiver.
func (m *Message) ValueAt(p Path) string {
	occurs := p.SegmentOccurs
	if occurs < 1 {
		occurs = 1
	}

	seg, ok := m.Segment(p.Segment, occurs)
	if !ok {
		return ""
	}
	if p.Element == 0 {
		return seg.String()
	}

	el := seg.Element(p.Element)

	// A repetition is selected before a component, because a repeated composite
	// element repeats whole composites.
	if p.Repeat > 0 {
		reps := el.Repetitions()
		if p.Repeat > len(reps) {
			return ""
		}
		el = Element{raw: []byte(reps[p.Repeat-1]), delim: el.delim}
	}

	if p.Component > 0 {
		return el.Component(p.Component)
	}
	return el.String()
}

// Exists reports whether a path resolves to a present, non-empty value.
//
// Separate from ValueAt because a filter needs to distinguish "this element is empty"
// from "this segment is not here at all", and a string cannot carry both.
func (m *Message) Exists(p Path) bool {
	occurs := p.SegmentOccurs
	if occurs < 1 {
		occurs = 1
	}
	seg, ok := m.Segment(p.Segment, occurs)
	if !ok {
		return false
	}
	if p.Element == 0 {
		return true
	}
	return !seg.Element(p.Element).IsEmpty()
}
