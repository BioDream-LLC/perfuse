// Package hl7 parses HL7 version 2.x messages.
//
// The parser indexes rather than decodes. Parsing records byte offsets for
// every segment and field into two slices and touches nothing else; components,
// subcomponents and repetitions are split only when something asks for them,
// and escape sequences are resolved only when a value is turned into a string.
//
// This matters because the common case in an interface engine is a message that
// gets filtered on one field and forwarded whole. Decoding a 40-segment ORU into
// objects to read MSH-9 is most of the cost of handling it.
package hl7

import (
	"bytes"
	"errors"
)

// Default encoding characters, per the HL7 v2 standard.
const (
	DefaultField        = '|'
	DefaultComponent    = '^'
	DefaultRepeat       = '~'
	DefaultEscape       = '\\'
	DefaultSubcomponent = '&'
)

// MLLP framing bytes. Messages read off a socket usually still carry them.
const (
	mllpStart = 0x0B
	mllpEnd   = 0x1C
)

var (
	// ErrNotHL7 means the input does not begin with a parseable MSH segment.
	ErrNotHL7 = errors.New("hl7: input does not start with an MSH segment")
	// ErrShortHeader means MSH exists but is too short to carry encoding
	// characters.
	ErrShortHeader = errors.New("hl7: MSH segment is too short to contain encoding characters")
)

// Separators are the encoding characters for one message, taken from MSH-1 and
// MSH-2 rather than assumed.
type Separators struct {
	Field        byte
	Component    byte
	Repeat       byte
	Escape       byte
	Subcomponent byte
}

// DefaultSeparators returns the standard encoding characters.
func DefaultSeparators() Separators {
	return Separators{
		Field:        DefaultField,
		Component:    DefaultComponent,
		Repeat:       DefaultRepeat,
		Escape:       DefaultEscape,
		Subcomponent: DefaultSubcomponent,
	}
}

// EncodingCharacters renders the separators as they appear in MSH-2.
func (s Separators) EncodingCharacters() string {
	return string([]byte{s.Component, s.Repeat, s.Escape, s.Subcomponent})
}

// span is a half-open byte range within a message.
type span struct{ start, end int }

func (s span) len() int { return s.end - s.start }

// segIndex locates one segment and its fields.
type segIndex struct {
	name span
	body span
	// first and last bound this segment's entries in Message.fields.
	first, last int
	// msh is true for the header segment, whose field numbering is offset by
	// one because MSH-1 is the field separator itself.
	msh bool
}

// Message is a parsed HL7 v2 message. It holds the original bytes unmodified;
// every accessor returns a view into them.
type Message struct {
	raw    []byte
	sep    Separators
	segs   []segIndex
	fields []span
}

// Raw returns the original message bytes, without MLLP framing. The slice is
// not copied, so treat it as read-only.
func (m *Message) Raw() []byte { return m.raw }

// Separators returns the encoding characters this message declared.
func (m *Message) Separators() Separators { return m.sep }

// Parse indexes an HL7 v2 message.
//
// MLLP framing bytes are stripped if present. Segments may be separated by CR,
// LF or CRLF: the standard says CR, and real senders do not always agree.
//
// The input is copied, so the returned Message stays valid however the caller
// reuses its buffer. That costs about 3% and one allocation per message. Pass
// WithZeroCopy to skip the copy when the input outlives the Message or is never
// written again - and read that option's documentation first, because getting it
// wrong produces silently wrong field values rather than an error.
func Parse(raw []byte, opts ...Option) (*Message, error) {
	o := newOptions(opts)

	raw = trimFraming(raw)
	if !o.zeroCopy {
		// Copied after trimming rather than before, so framing bytes are not paid for, and only once the input
		// looks like it might be a message.
		if len(raw) >= 4 && bytes.HasPrefix(raw, []byte("MSH")) {
			raw = append([]byte(nil), raw...)
		}
	}
	if len(raw) < 4 || !bytes.HasPrefix(raw, []byte("MSH")) {
		return nil, ErrNotHL7
	}

	m := &Message{raw: raw}
	m.sep.Field = raw[3]

	// MSH-2 holds the remaining encoding characters. Read what is there and
	// fall back to the standard for anything missing, which is common in
	// hand-built test messages.
	m.sep.Component = DefaultComponent
	m.sep.Repeat = DefaultRepeat
	m.sep.Escape = DefaultEscape
	m.sep.Subcomponent = DefaultSubcomponent

	end := indexAny(raw, 4, m.sep.Field, '\r', '\n')
	if end < 0 {
		end = len(raw)
	}
	enc := raw[4:end]
	if len(enc) == 0 {
		return nil, ErrShortHeader
	}
	for i, b := range enc {
		switch i {
		case 0:
			m.sep.Component = b
		case 1:
			m.sep.Repeat = b
		case 2:
			m.sep.Escape = b
		case 3:
			m.sep.Subcomponent = b
		}
	}

	m.index()
	return m, nil
}

// ParseString is Parse for a string input.
//
// A string is already immutable, so this never needs to copy and passes WithZeroCopy regardless of what the caller
// asked for. Converting the string to a slice has already made the only copy required.
func ParseString(s string, opts ...Option) (*Message, error) {
	return Parse([]byte(s), append(opts, WithZeroCopy())...)
}

// trimFraming removes MLLP start and end blocks and any trailing terminator.
func trimFraming(raw []byte) []byte {
	if len(raw) > 0 && raw[0] == mllpStart {
		raw = raw[1:]
	}
	if i := bytes.IndexByte(raw, mllpEnd); i >= 0 {
		raw = raw[:i]
	}
	return bytes.TrimRight(raw, "\r\n")
}

// index walks the message once, recording segment and field offsets.
//
// Both slices are sized from a counting pass first. Counting delimiters is
// cheap and vectorised, and it means the index is allocated exactly once
// instead of doubling its way up on a long ORU.
func (m *Message) index() {
	segCount := 1 + bytes.Count(m.raw, []byte{'\r'}) + bytes.Count(m.raw, []byte{'\n'})
	fieldCount := bytes.Count(m.raw, []byte{m.sep.Field})

	m.segs = make([]segIndex, 0, segCount)
	m.fields = make([]span, 0, fieldCount)

	pos := 0
	for pos <= len(m.raw) {
		lineEnd := indexAny(m.raw, pos, '\r', '\n')
		if lineEnd < 0 {
			lineEnd = len(m.raw)
		}
		if lineEnd > pos {
			m.indexSegment(span{pos, lineEnd})
		}
		if lineEnd >= len(m.raw) {
			break
		}
		// Skip the terminator, tolerating CRLF.
		pos = lineEnd + 1
		if pos < len(m.raw) && (m.raw[pos] == '\n' || m.raw[pos] == '\r') && m.raw[pos] != m.raw[lineEnd] {
			pos++
		}
	}
}

func (m *Message) indexSegment(line span) {
	nameEnd := line.start + 3
	if nameEnd > line.end {
		nameEnd = line.end
	}
	seg := segIndex{
		name:  span{line.start, nameEnd},
		body:  line,
		first: len(m.fields),
		msh:   m.equalName(span{line.start, nameEnd}, "MSH"),
	}

	// Fields start after the segment name. For MSH the separator immediately
	// following the name is itself MSH-1, so the first delimited element that
	// follows is MSH-2.
	pos := nameEnd
	for pos < line.end {
		if m.raw[pos] != m.sep.Field {
			// Malformed: no separator after the segment name. Treat the rest of
			// the line as a single field rather than discarding it.
			m.fields = append(m.fields, span{pos, line.end})
			break
		}
		pos++
		next := bytes.IndexByte(m.raw[pos:line.end], m.sep.Field)
		if next < 0 {
			m.fields = append(m.fields, span{pos, line.end})
			break
		}
		m.fields = append(m.fields, span{pos, pos + next})
		pos += next
	}

	seg.last = len(m.fields)
	m.segs = append(m.segs, seg)
}

func (m *Message) equalName(s span, name string) bool {
	return s.len() == len(name) && string(m.raw[s.start:s.end]) == name
}

// SegmentCount returns the number of segments.
func (m *Message) SegmentCount() int { return len(m.segs) }

// SegmentNames returns every segment name in order, including repeats.
func (m *Message) SegmentNames() []string {
	out := make([]string, 0, len(m.segs))
	for _, s := range m.segs {
		out = append(out, string(m.raw[s.name.start:s.name.end]))
	}
	return out
}

// Segment returns the nth occurrence of a segment, counting from 1.
func (m *Message) Segment(name string, occurrence int) (Segment, bool) {
	if occurrence < 1 {
		return Segment{}, false
	}
	seen := 0
	for i := range m.segs {
		if !m.equalName(m.segs[i].name, name) {
			continue
		}
		seen++
		if seen == occurrence {
			return Segment{m: m, idx: i}, true
		}
	}
	return Segment{}, false
}

// Segments returns every occurrence of a segment, in order.
func (m *Message) Segments(name string) []Segment {
	var out []Segment
	for i := range m.segs {
		if m.equalName(m.segs[i].name, name) {
			out = append(out, Segment{m: m, idx: i})
		}
	}
	return out
}

// SegmentAt returns the segment at an absolute position, counting from 0.
func (m *Message) SegmentAt(i int) (Segment, bool) {
	if i < 0 || i >= len(m.segs) {
		return Segment{}, false
	}
	return Segment{m: m, idx: i}, true
}

// Segment is one segment of a message.
type Segment struct {
	m   *Message
	idx int
}

// Valid reports whether this segment refers to anything.
func (s Segment) Valid() bool { return s.m != nil && s.idx < len(s.m.segs) }

// Name returns the three-character segment name.
func (s Segment) Name() string {
	if !s.Valid() {
		return ""
	}
	n := s.m.segs[s.idx].name
	return string(s.m.raw[n.start:n.end])
}

// Raw returns the segment's bytes, excluding the terminator.
func (s Segment) Raw() []byte {
	if !s.Valid() {
		return nil
	}
	b := s.m.segs[s.idx].body
	return s.m.raw[b.start:b.end]
}

// FieldCount returns the highest field number present in this segment.
func (s Segment) FieldCount() int {
	if !s.Valid() {
		return 0
	}
	seg := s.m.segs[s.idx]
	n := seg.last - seg.first
	if seg.msh {
		// MSH-1 is the separator itself, which is not in the field index.
		return n + 1
	}
	return n
}

// Field returns a field by its HL7 number, counting from 1.
//
// MSH is special: MSH-1 is the field separator and MSH-2 is the encoding
// characters, so the numbering is shifted by one relative to every other
// segment. Callers do not have to know that.
func (s Segment) Field(n int) Value {
	if !s.Valid() || n < 1 {
		return Value{}
	}
	seg := s.m.segs[s.idx]

	if seg.msh {
		if n == 1 {
			// Synthesised: the separator lives between the name and MSH-2.
			return Value{m: s.m, sp: span{seg.name.end, seg.name.end + 1}}
		}
		n--
	}

	i := seg.first + n - 1
	if i < seg.first || i >= seg.last {
		return Value{}
	}
	return Value{m: s.m, sp: s.m.fields[i]}
}

// Value is a view of some part of a message: a field, a repetition, a component
// or a subcomponent. Narrowing it does no work beyond finding a separator.
type Value struct {
	m  *Message
	sp span
}

// Exists reports whether the value is present in the message. A present but
// empty field exists; a field beyond the end of the segment does not.
func (v Value) Exists() bool { return v.m != nil }

// IsEmpty reports whether the value has no content. HL7's explicit null, two
// double quotes, counts as empty.
func (v Value) IsEmpty() bool {
	if !v.Exists() || v.sp.len() == 0 {
		return true
	}
	return v.sp.len() == 2 && v.m.raw[v.sp.start] == '"' && v.m.raw[v.sp.start+1] == '"'
}

// Bytes returns the raw bytes of the value, escape sequences intact. The slice
// points into the message and must not be modified.
func (v Value) Bytes() []byte {
	if !v.Exists() {
		return nil
	}
	return v.m.raw[v.sp.start:v.sp.end]
}

// Raw returns the value exactly as it appears in the message.
func (v Value) Raw() string { return string(v.Bytes()) }

// String returns the value with HL7 escape sequences resolved. This is the only
// accessor that allocates for content, and it is where a caller pays for the
// parts of the message it actually reads.
func (v Value) String() string {
	if !v.Exists() {
		return ""
	}
	if v.IsEmpty() && v.sp.len() == 2 {
		return ""
	}
	return Unescape(v.Bytes(), v.m.sep)
}

// Repeat returns the nth repetition of a field, counting from 1. A field with
// no repeat separator has exactly one repetition, which is itself.
func (v Value) Repeat(n int) Value {
	if !v.Exists() {
		return Value{}
	}
	return v.split(v.m.sep.Repeat, n)
}

// RepeatCount returns how many repetitions the value has.
func (v Value) RepeatCount() int {
	if !v.Exists() {
		return 0
	}
	return v.count(v.m.sep.Repeat)
}

// Repeats returns every repetition.
func (v Value) Repeats() []Value {
	if !v.Exists() {
		return nil
	}
	return v.all(v.m.sep.Repeat)
}

// Component returns the nth component, counting from 1.
//
// If the value has repetitions, this addresses the first, which matches how
// HL7 paths are conventionally read.
func (v Value) Component(n int) Value {
	if !v.Exists() {
		return Value{}
	}
	return v.Repeat(1).split(v.m.sep.Component, n)
}

// ComponentCount returns how many components the value has.
func (v Value) ComponentCount() int {
	if !v.Exists() {
		return 0
	}
	return v.Repeat(1).count(v.m.sep.Component)
}

// Subcomponent returns the nth subcomponent, counting from 1.
func (v Value) Subcomponent(n int) Value {
	if !v.Exists() {
		return Value{}
	}
	return v.split(v.m.sep.Subcomponent, n)
}

// SubcomponentCount returns how many subcomponents the value has.
func (v Value) SubcomponentCount() int {
	if !v.Exists() {
		return 0
	}
	return v.count(v.m.sep.Subcomponent)
}

// split narrows the value to the nth part delimited by sep, counting from 1.
func (v Value) split(sep byte, n int) Value {
	if !v.Exists() || n < 1 {
		return Value{}
	}
	start := v.sp.start
	idx := 1
	for {
		next := bytes.IndexByte(v.m.raw[start:v.sp.end], sep)
		if next < 0 {
			if idx == n {
				return Value{m: v.m, sp: span{start, v.sp.end}}
			}
			return Value{}
		}
		if idx == n {
			return Value{m: v.m, sp: span{start, start + next}}
		}
		start += next + 1
		idx++
	}
}

func (v Value) count(sep byte) int {
	if !v.Exists() {
		return 0
	}
	return 1 + bytes.Count(v.m.raw[v.sp.start:v.sp.end], []byte{sep})
}

func (v Value) all(sep byte) []Value {
	if !v.Exists() {
		return nil
	}
	out := make([]Value, 0, v.count(sep))
	start := v.sp.start
	for {
		next := bytes.IndexByte(v.m.raw[start:v.sp.end], sep)
		if next < 0 {
			out = append(out, Value{m: v.m, sp: span{start, v.sp.end}})
			return out
		}
		out = append(out, Value{m: v.m, sp: span{start, start + next}})
		start += next + 1
	}
}

// Type returns the message type, trigger event and structure from MSH-9, for
// example ("ADT", "A01", "ADT_A01"). Missing parts come back empty.
func (m *Message) Type() (msgType, event, structure string) {
	seg, ok := m.Segment("MSH", 1)
	if !ok {
		return "", "", ""
	}
	f := seg.Field(9)
	return f.Component(1).String(), f.Component(2).String(), f.Component(3).String()
}

// ControlID returns MSH-10, the message control ID.
func (m *Message) ControlID() string {
	seg, ok := m.Segment("MSH", 1)
	if !ok {
		return ""
	}
	return seg.Field(10).String()
}

// Version returns the HL7 version from MSH-12, or the empty string if it is absent.
//
// Only the version ID component is returned, so "2.5.1" rather than the whole field, because MSH-12 may carry
// internationalisation and profile components that nobody routing on version wants to strip themselves.
//
// Worth knowing: senders lie about this. A message declaring 2.3 routinely contains fields that only exist in later
// versions, so this is useful for reporting and for choosing a dictionary, and a poor thing to make a strict
// decision on.
func (m *Message) Version() string {
	seg, ok := m.Segment("MSH", 1)
	if !ok {
		return ""
	}
	return seg.Field(12).Component(1).String()
}

// String renders the message back out. Because the parser never modifies the
// input, this is the original text with a canonical CR terminator.
func (m *Message) String() string {
	return string(m.raw)
}

// indexAny returns the offset of the first of the given bytes at or after from,
// or -1.
func indexAny(b []byte, from int, targets ...byte) int {
	for i := from; i < len(b); i++ {
		for _, t := range targets {
			if b[i] == t {
				return i
			}
		}
	}
	return -1
}
