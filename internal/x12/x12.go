// Package x12 parses ASC X12 EDI interchanges.
//
// X12 is what HL7 v2 is to clinical messaging: an ancient, delimited, segment-based
// format that carries most of the money in American healthcare. 837 claims, 835
// remittance advice, 834 enrolment, 270/271 eligibility. A clearinghouse speaks
// nothing else, and an integration engine that cannot read it is shut out of revenue
// cycle work entirely.
//
// # How it differs from HL7 v2, and why that matters here
//
// HL7 finds its delimiters by reading MSH-1 and MSH-2 - scan until you hit
// something. X12 does not work that way. The ISA segment is fixed width, exactly 106
// bytes, and the delimiters live at known byte offsets inside it. That is not a
// convenience, it is the only reliable way: the element separator is itself at a
// fixed position, so you cannot scan for it without already knowing what it is.
//
// Getting this wrong is the classic X12 bug. A parser that guesses "*" works on
// almost every file, because almost everybody uses "*", and then fails on the one
// trading partner who uses "|" - producing a file that parses into one enormous
// element rather than failing outright. Silent misparsing of a claims file is worse
// than a rejection, because a rejection gets fixed the same day.
//
// # The envelope is self-describing, and that is worth using
//
// X12 carries its own counts: SE01 states how many segments were in the transaction
// set, GE01 how many transaction sets were in the group, IEA01 how many groups were
// in the interchange. Those exist precisely so a truncated file can be detected, and
// a parser that ignores them throws away the format's best safety feature. Half an
// 837 will frequently parse; it will just be missing claims, and nobody notices until
// the money does not arrive.
package x12

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// Errors returned by Parse.
var (
	// ErrNotX12 means the input does not begin with an ISA segment.
	ErrNotX12 = errors.New("x12: input does not start with an ISA segment")
	// ErrShortISA means the ISA segment is truncated.
	//
	// Reported distinctly from ErrNotX12 because it usually means a file was cut off
	// in transfer rather than that somebody sent the wrong format, and those call for
	// different actions.
	ErrShortISA = errors.New("x12: the ISA segment is shorter than the fixed 106 bytes the standard requires")

	// ErrMalformedBinarySegment means a length-prefixed segment's declared byte count does not match what follows it.
	//
	// Refused rather than recovered from. A wrong length means the splitter cannot tell where the payload ends, so continuing would
	// read part of an attachment as segments and misalign everything after it - and every fragment of a PDF is a structurally valid
	// segment, so nothing further would complain.
	ErrMalformedBinarySegment = errors.New("x12: a binary segment's declared length does not match the data")

	// ErrUnsupportedBinarySegment means the interchange contains a binary segment whose layout this package has not verified.
	//
	// Refused for the same reason. Guessing which element holds the byte count would produce a plausible-looking wrong length rather
	// than an error, which is the worst outcome available here.
	ErrUnsupportedBinarySegment = errors.New("x12: the interchange contains a binary segment this package cannot safely split")
)

// isaLength is the fixed width of the ISA segment, including its terminator.
//
// Not a guess and not configurable. The standard fixes every ISA element's length,
// which is what makes the delimiter offsets below usable.
const isaLength = 106

// Delimiter offsets within ISA. Derived from the fixed element widths.
const (
	offElementSep   = 3   // immediately after "ISA"
	offRepeatSep    = 82  // ISA11, a repetition separator from version 00501
	offVersion      = 84  // ISA12, five characters
	offComponentSep = 104 // ISA16
	offSegmentTerm  = 105 // the character after ISA16
)

// Delimiters are the four characters that structure an interchange.
type Delimiters struct {
	// Element separates elements within a segment. Usually '*'.
	Element byte
	// Component separates components within a composite element. Usually ':'.
	Component byte
	// Segment terminates a segment. Usually '~'.
	Segment byte
	// Repeat separates repetitions of one element. Usually '^'.
	//
	// Zero when the interchange predates version 00501, where ISA11 held something
	// else entirely. Treating that older value as a repetition separator would split
	// elements on a character that is ordinary data.
	Repeat byte
}

// Message is a parsed interchange.
type Message struct {
	raw   []byte
	delim Delimiters
	// version is ISA12, kept because it decides whether Repeat means anything.
	version string
	segs    []Segment
}

// Segment is one segment of an interchange.
type Segment struct {
	// ID is the segment identifier, for example "ISA", "GS", "ST", "CLM".
	ID string
	// raw is the segment without its terminator.
	raw   []byte
	delim Delimiters
	// elements are the positions of each element, excluding the segment ID.
	elements [][]byte
}

// Parse reads an interchange.
func Parse(raw []byte) (*Message, error) {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	if len(raw) < 3 || !bytes.HasPrefix(raw, []byte("ISA")) {
		return nil, ErrNotX12
	}
	if len(raw) < isaLength {
		return nil, fmt.Errorf("%w: got %d bytes", ErrShortISA, len(raw))
	}

	m := &Message{raw: raw}
	m.delim.Element = raw[offElementSep]
	m.delim.Component = raw[offComponentSep]
	m.delim.Segment = raw[offSegmentTerm]
	m.version = string(raw[offVersion : offVersion+5])

	// ISA11 is a repetition separator only from 00501. Before that it carried the
	// Interchange Control Standards Identifier, normally "U", and treating that as a
	// separator would split elements on a letter that is ordinary data.
	if m.version >= "00501" {
		m.delim.Repeat = raw[offRepeatSep]
	}

	if err := m.validateDelimiters(); err != nil {
		return nil, err
	}

	// Binary-aware, because a segment carrying raw bytes cannot be found by scanning for a terminator that its payload may contain.
	// See binary.go: this was silently corrupting any interchange with an attachment in it.
	if err := m.splitBinaryAware(); err != nil {
		return nil, err
	}

	return m, nil
}

// ParseString is Parse for a string input.
func ParseString(s string) (*Message, error) { return Parse([]byte(s)) }

// validateDelimiters refuses a set that cannot work.
//
// Worth checking rather than trusting, because the failure is silent. A file whose
// element and segment separators are the same character parses into nonsense that
// looks structurally plausible, and the first sign of trouble is a rejected claim
// weeks later.
func (m *Message) validateDelimiters() error {
	d := m.delim

	named := []struct {
		name string
		b    byte
	}{
		{"element", d.Element},
		{"component", d.Component},
		{"segment terminator", d.Segment},
	}
	if d.Repeat != 0 {
		named = append(named, struct {
			name string
			b    byte
		}{"repetition", d.Repeat})
	}

	for _, n := range named {
		// Alphanumerics are refused outright. A delimiter that is a letter or digit
		// means the ISA is misaligned - almost always a file with the wrong line
		// endings, or one that has been through a text editor.
		if isAlphanumeric(n.b) {
			return fmt.Errorf("x12: the %s delimiter is %q, which is a letter or digit; the ISA segment is probably misaligned or the file has been reformatted", n.name, string(n.b))
		}
		if n.b == 0 {
			return fmt.Errorf("x12: the %s delimiter is a null byte", n.name)
		}
	}

	for i := 0; i < len(named); i++ {
		for j := i + 1; j < len(named); j++ {
			if named[i].b == named[j].b {
				return fmt.Errorf("x12: the %s and %s delimiters are both %q; the interchange cannot be split unambiguously",
					named[i].name, named[j].name, string(named[i].b))
			}
		}
	}
	return nil
}

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// split indexes the segments.
func (m *Message) split() {
	term := m.delim.Segment

	start := 0
	for i := 0; i <= len(m.raw); i++ {
		atEnd := i == len(m.raw)
		if !atEnd && m.raw[i] != term {
			continue
		}

		seg := m.raw[start:i]
		// Trailing whitespace after a terminator is common: many partners write one
		// segment per line for readability, which is legal because the terminator is
		// what ends a segment, not the newline.
		seg = bytes.Trim(seg, " \t\r\n")
		if len(seg) > 0 {
			m.segs = append(m.segs, newSegment(seg, m.delim))
		}
		start = i + 1
		if atEnd {
			break
		}
	}
}

func newSegment(raw []byte, d Delimiters) Segment {
	s := Segment{raw: raw, delim: d}

	parts := bytes.Split(raw, []byte{d.Element})
	if len(parts) == 0 {
		return s
	}

	s.ID = string(parts[0])
	s.elements = parts[1:]

	// A binary payload is not delimited data and must not be split as though it were.
	//
	// The splitter already keeps the segment whole (see binary.go); this is the second half of the same problem. Splitting a PDF on
	// the element separator turns one attachment into as many elements as it happens to contain asterisks, and the caller reading
	// the data element gets the bytes up to the first one. That is a silently truncated attachment, which is worse than a refused
	// one because the claim it supports will be denied for a reason nobody can see.
	//
	// The payload is the segment's last element by definition, so everything before it splits normally and the remainder is rejoined
	// exactly as it arrived - separators included.
	if shape, isBinary := binarySegments[s.ID]; isBinary && len(s.elements) >= shape.dataElement {
		before := s.elements[:shape.dataElement-1]
		payload := bytes.Join(s.elements[shape.dataElement-1:], []byte{d.Element})

		s.elements = append(append([][]byte{}, before...), payload)
	}

	return s
}

// Delimiters reports the characters this interchange uses.
func (m *Message) Delimiters() Delimiters { return m.delim }

// Version reports ISA12.
func (m *Message) Version() string { return m.version }

// Raw returns the original bytes.
func (m *Message) Raw() []byte { return m.raw }

// SegmentCount reports how many segments were found.
func (m *Message) SegmentCount() int { return len(m.segs) }

// SegmentAt returns a segment by position, zero-based.
func (m *Message) SegmentAt(i int) (Segment, bool) {
	if i < 0 || i >= len(m.segs) {
		return Segment{}, false
	}
	return m.segs[i], true
}

// Segment returns the nth occurrence of a segment id, counting from 1.
//
// One-based to match how every X12 implementation guide refers to positions, and how
// HL7 does it in this codebase. Two conventions in one program is a bug waiting to
// happen.
func (m *Message) Segment(id string, occurrence int) (Segment, bool) {
	if occurrence < 1 {
		return Segment{}, false
	}
	n := 0
	for _, s := range m.segs {
		if s.ID == id {
			n++
			if n == occurrence {
				return s, true
			}
		}
	}
	return Segment{}, false
}

// Segments returns every segment with the given id.
func (m *Message) Segments(id string) []Segment {
	var out []Segment
	for _, s := range m.segs {
		if s.ID == id {
			out = append(out, s)
		}
	}
	return out
}

// ElementCount reports how many elements the segment has, excluding its id.
func (s Segment) ElementCount() int { return len(s.elements) }

// Element returns an element by position, counting from 1.
//
// One-based because implementation guides number them that way: CLM01 is the first
// element after "CLM". Returning an empty value for a position that does not exist
// rather than an error, because a short segment is normal - trailing empty elements
// are routinely omitted, and every caller would otherwise need the same check.
func (s Segment) Element(n int) Element {
	if n < 1 || n > len(s.elements) {
		return Element{delim: s.delim}
	}
	return Element{raw: s.elements[n-1], delim: s.delim}
}

// Raw returns the segment without its terminator.
func (s Segment) Raw() []byte { return s.raw }

// String renders the segment as written.
func (s Segment) String() string { return string(s.raw) }

// Element is one element of a segment.
type Element struct {
	raw   []byte
	delim Delimiters
}

// String returns the element as written.
func (e Element) String() string { return string(e.raw) }

// Bytes returns the element's raw bytes.
//
// Exists for binary payloads, and the reason is not correctness: a Go string holds arbitrary bytes, so String would return a PDF
// intact. It is that a string cannot be handed to anything expecting bytes without a conversion that copies a whole attachment, and
// that a caller reading a []byte is not tempted to compare, trim or lowercase it the way a string invites.
//
// Shares the message's buffer rather than copying. A caller keeping the result must copy, which ParseAttachment does - otherwise one
// small document holds the entire interchange alive.
func (e Element) Bytes() []byte { return e.raw }

// IsEmpty reports whether the element has no content.
func (e Element) IsEmpty() bool { return len(e.raw) == 0 }

// ComponentCount reports how many components the element has.
func (e Element) ComponentCount() int {
	if len(e.raw) == 0 {
		return 0
	}
	return bytes.Count(e.raw, []byte{e.delim.Component}) + 1
}

// Component returns a component by position, counting from 1.
//
// A non-composite element has exactly one component, which is itself. That means
// Element(1).Component(1) is always safe and always right, and callers do not need to
// know whether a given element is composite in a given implementation guide.
func (e Element) Component(n int) string {
	if n < 1 || len(e.raw) == 0 {
		return ""
	}
	parts := bytes.Split(e.raw, []byte{e.delim.Component})
	if n > len(parts) {
		return ""
	}
	return string(parts[n-1])
}

// Repetitions splits an element on the repetition separator.
//
// Returns the whole element as a single repetition when the interchange has no
// repetition separator, which is every version before 00501. Splitting on a character
// that is ordinary data in those versions would silently corrupt values.
func (e Element) Repetitions() []string {
	if e.delim.Repeat == 0 || len(e.raw) == 0 {
		return []string{string(e.raw)}
	}
	parts := bytes.Split(e.raw, []byte{e.delim.Repeat})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	return out
}

// TransactionSets reports the ST01 transaction set identifiers present.
//
// The answer to "what is in this file" - 837, 835, 834 and so on. An interchange can
// legitimately carry more than one kind, which is why this returns a list.
func (m *Message) TransactionSets() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range m.Segments("ST") {
		id := s.Element(1).String()
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Describe summarises an interchange for a log line or an error message.
//
// Deliberately carries no element values beyond the envelope identifiers. The
// contents of an 837 are somebody's diagnoses and charges, and a log line is the
// easiest place for that to end up somewhere it should not be.
func (m *Message) Describe() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "X12 %s", m.version)
	if sets := m.TransactionSets(); len(sets) > 0 {
		fmt.Fprintf(&sb, " %s", strings.Join(sets, ","))
	}
	fmt.Fprintf(&sb, ", %d segment(s)", len(m.segs))
	if isa, ok := m.Segment("ISA", 1); ok {
		fmt.Fprintf(&sb, ", control %s", strings.TrimSpace(isa.Element(13).String()))
	}
	return sb.String()
}
