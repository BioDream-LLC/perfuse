// Reading and writing values at a path.
//
// # Why this is separate from Path
//
// path.go decides what a path expression means. This file resolves one against an actual interchange. Kept apart
// because the parsing rules are worth reading without the byte handling, and because the parser is used by the
// configuration validator, which has no message to resolve against.
//
// # Why a write re-parses instead of editing bytes in place
//
// Segment.elements are subslices of Segment.raw, and Message.segs indexes into Message.raw. Editing a value in place
// changes the length of everything after it, so every one of those slices would need adjusting, and any that was missed
// would read correct until the one message where it did not.
//
// So a write rebuilds the segment, rebuilds the interchange, and parses it again. That costs a parse per changed
// message and buys the guarantee that a Message returned from here is exactly what parsing its own bytes produces.
// There is no state where the indexes and the bytes disagree, because that state is unrepresentable.
package x12

import (
	"bytes"
	"fmt"
	"strings"
)

// Read resolves a path against an interchange.
//
// The second return distinguishes "not present" from "present and empty". X12 carries meaning in that difference: an
// absent element was never sent, an empty one was sent deliberately as blank, and a claims processor treats them
// differently.
func Read(m *Message, p Path) (string, bool) {
	if m == nil || p.Segment == "" {
		return "", false
	}

	occurrence := p.SegmentOccurs
	if occurrence < 1 {
		occurrence = 1
	}

	matches := m.Segments(p.Segment)
	if occurrence > len(matches) {
		return "", false
	}
	seg := matches[occurrence-1]

	// Element zero means the whole segment as written, which is what a path of "CLM" asks for.
	if p.Element == 0 {
		return string(seg.raw), true
	}
	if p.Element > seg.ElementCount() {
		return "", false
	}
	el := seg.Element(p.Element)

	// A repetition is resolved before a component, because the composite lives inside one repetition. Doing it the
	// other way round would split on the component separator first and then look for repetitions inside a component,
	// which is not where they are.
	if p.Repeat > 0 {
		reps := el.Repetitions()
		if p.Repeat > len(reps) {
			return "", false
		}
		// Repetitions come back as strings, so from here the component is split by hand rather than through Element.
		// Wrapping the repetition back into an Element would need the unexported fields and buys nothing.
		rep := reps[p.Repeat-1]
		if p.Component == 0 {
			return rep, true
		}
		parts := strings.Split(rep, string(m.Delimiters().Component))
		if p.Component > len(parts) {
			return "", false
		}
		return parts[p.Component-1], true
	}

	if p.Component == 0 {
		return el.String(), true
	}
	if p.Component > el.ComponentCount() {
		return "", false
	}
	return el.Component(p.Component), true
}

// ReadString resolves a path expression in either notation.
func ReadString(m *Message, path string) (string, bool) {
	p, err := ParsePath(path)
	if err != nil {
		return "", false
	}
	return Read(m, p)
}

// isaWidths are the fixed widths of the ISA elements, counting from 1.
//
// # Why this has to exist
//
// ISA is the only segment in X12 whose element lengths are semantic. Every other segment is delimited, so an element
// can be any length. ISA is fixed width by the standard, and a great deal of software finds ISA16 - the component
// separator - by counting bytes from the start rather than by splitting on delimiters.
//
// So shortening an element does not produce a short element. It shifts every byte after it, and the next reader takes
// its component separator from the middle of the receiver identifier. Found by a trim step on ISA06: the interchange
// came back reporting its component delimiter as "C", which is a letter out of RECEIVER.
//
// Padding is therefore not a convenience here, it is what the standard requires: ISA06 is fifteen characters, space
// padded on the right.
var isaWidths = map[int]int{
	1: 2, 2: 10, 3: 2, 4: 10, 5: 2, 6: 15, 7: 2, 8: 15,
	9: 6, 10: 4, 11: 1, 12: 5, 13: 9, 14: 1, 15: 1, 16: 1,
}

// fitISA pads a value to its fixed ISA width, or refuses when it is too long to fit.
//
// Truncating is not offered. Silently shortening a submitter identifier produces an interchange a trading partner
// accepts and attributes to somebody else, which is worse than a channel that will not send it.
func fitISA(element int, value string) (string, error) {
	width, fixed := isaWidths[element]
	if !fixed {
		return value, nil
	}
	if len(value) > width {
		return "", fmt.Errorf("x12: ISA%02d is fixed at %d characters and %q is %d; the ISA segment is positional, so "+
			"a longer value would shift every byte after it and the next reader would take its component separator "+
			"from the middle of another element", element, width, value, len(value))
	}
	// Right padded with spaces, which is what the standard specifies and what every ISA on the wire looks like.
	return value + strings.Repeat(" ", width-len(value)), nil
}

// Set writes a value at a path and returns the resulting interchange.
//
// The receiver is not modified. A step that fails must leave the message as it was, or a pipeline that stops halfway
// through delivers something no configuration describes.
//
// # What is refused, and why
//
// A path with no element addresses a whole segment. Writing one is refused: replacing a segment's text wholesale can
// change how many elements it has, which for most segments changes what the values mean positionally, and the caller
// almost always meant a specific element. Refused rather than attempted, because the failure would be a valid-looking
// interchange carrying values in the wrong positions.
func Set(m *Message, p Path, value string) (*Message, error) {
	if m == nil {
		return nil, fmt.Errorf("x12: there is no message to change")
	}
	if p.Segment == "" {
		return nil, fmt.Errorf("x12: the path names no segment")
	}
	if p.Element == 0 {
		return nil, fmt.Errorf("x12: %s addresses a whole segment, and writing one is refused because it would "+
			"change how many elements the segment has and shift every value after it. Name an element, such as %s-1",
			p, p.Segment)
	}

	d := m.Delimiters()
	if err := checkNoDelimiters(value, d, p.Component > 0 || p.Repeat > 0); err != nil {
		return nil, err
	}

	occurrence := p.SegmentOccurs
	if occurrence < 1 {
		occurrence = 1
	}

	// Which segment in the whole interchange, so the rebuild edits the right one. Segments(id) loses that position.
	index := -1
	seen := 0
	for i := 0; i < m.SegmentCount(); i++ {
		at, ok := m.SegmentAt(i)
		if !ok {
			break
		}
		if at.ID == p.Segment {
			seen++
			if seen == occurrence {
				index = i
				break
			}
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("x12: %s does not appear in this interchange (found %d of them, needed %d)",
			p.Segment, seen, occurrence)
	}

	seg, ok := m.SegmentAt(index)
	if !ok {
		return nil, fmt.Errorf("x12: segment %d is out of range", index)
	}

	// Elements are positional, so writing element 9 of a five-element segment means the four between have to exist as
	// empty. Padding is correct rather than convenient: an 837 that omits trailing elements is normal, and refusing to
	// write past the last one present would make half the useful paths unusable.
	elements := make([]string, seg.ElementCount())
	for i := range elements {
		elements[i] = seg.Element(i + 1).String()
	}
	for len(elements) < p.Element {
		elements = append(elements, "")
	}

	updated, err := writeInto(elements[p.Element-1], value, p, d)
	if err != nil {
		return nil, err
	}

	// ISA is fixed width, so the value is padded to its element's width before it goes in. Done after writeInto rather
	// than before, so that a component or repetition inside an ISA element - which does not occur in practice but is
	// expressible - is assembled first and the whole element padded once.
	if seg.ID == "ISA" {
		if updated, err = fitISA(p.Element, updated); err != nil {
			return nil, err
		}
	}

	elements[p.Element-1] = updated

	return rebuild(m, index, seg.ID, elements)
}

// SetString writes a value at a path expression.
func SetString(m *Message, path, value string) (*Message, error) {
	p, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
	return Set(m, p, value)
}

// Clear empties the value at a path without removing the element.
//
// Distinct from removing it. An empty element was sent as blank; an absent one was never sent. X12 trading partners
// read those differently, so a configuration that wants one must not silently get the other.
func Clear(m *Message, p Path) (*Message, error) { return Set(m, p, "") }

// writeInto places a value inside an existing element, honouring the repetition and component parts of the path.
func writeInto(existing, value string, p Path, d Delimiters) (string, error) {
	// Neither a repetition nor a component: the value is the whole element.
	if p.Repeat == 0 && p.Component == 0 {
		return value, nil
	}

	if p.Repeat > 0 {
		if d.Repeat == 0 {
			return "", fmt.Errorf("x12: %s names a repetition, but this interchange has no repetition separator - "+
				"ISA11 holds something else in versions before 00501, so repetitions cannot be addressed here", p)
		}
		reps := strings.Split(existing, string(d.Repeat))
		for len(reps) < p.Repeat {
			reps = append(reps, "")
		}
		inner, err := writeComponent(reps[p.Repeat-1], value, p.Component, d)
		if err != nil {
			return "", err
		}
		reps[p.Repeat-1] = inner
		return strings.Join(reps, string(d.Repeat)), nil
	}

	return writeComponent(existing, value, p.Component, d)
}

// writeComponent places a value at a component position within one element or repetition.
func writeComponent(existing, value string, component int, d Delimiters) (string, error) {
	if component == 0 {
		return value, nil
	}
	if d.Component == 0 {
		return "", fmt.Errorf("x12: this interchange has no component separator, so a component cannot be addressed")
	}

	parts := strings.Split(existing, string(d.Component))
	for len(parts) < component {
		parts = append(parts, "")
	}
	parts[component-1] = value
	return strings.Join(parts, string(d.Component)), nil
}

// checkNoDelimiters refuses a value carrying this interchange's own delimiters.
//
// A value containing the element separator does not produce a wrong value - it produces extra elements, silently
// shifting everything after it into the wrong position. The result parses, looks ordinary, and means something else
// entirely. That is the failure this codebase refuses on principle, so it is caught at the write rather than left for a
// claims processor to find.
func checkNoDelimiters(value string, d Delimiters, insideComposite bool) error {
	named := []struct {
		b    byte
		what string
	}{
		{d.Element, "element separator"},
		{d.Segment, "segment terminator"},
	}
	// The component and repetition separators are only a hazard for a value being written as a whole element or
	// component - a caller addressing a component legitimately cannot include one, and a caller writing a whole
	// element may be assembling a composite on purpose.
	if insideComposite {
		named = append(named,
			struct {
				b    byte
				what string
			}{d.Component, "component separator"},
			struct {
				b    byte
				what string
			}{d.Repeat, "repetition separator"})
	}

	for _, n := range named {
		if n.b == 0 {
			continue
		}
		if strings.IndexByte(value, n.b) >= 0 {
			return fmt.Errorf("x12: the value contains %q, which this interchange uses as its %s. Writing it would "+
				"split the value and shift every element after it into the wrong position", string(n.b), n.what)
		}
	}
	return nil
}

// rebuild writes one segment's new elements back into the interchange and re-parses it.
func rebuild(m *Message, index int, id string, elements []string) (*Message, error) {
	d := m.Delimiters()

	var seg bytes.Buffer
	seg.WriteString(id)
	for _, e := range elements {
		seg.WriteByte(d.Element)
		seg.WriteString(e)
	}

	var out bytes.Buffer
	for i := 0; i < m.SegmentCount(); i++ {
		if i == index {
			out.Write(seg.Bytes())
		} else {
			at, ok := m.SegmentAt(i)
			if !ok {
				break
			}
			out.Write(at.raw)
		}
		out.WriteByte(d.Segment)

		// The original may separate segments with a newline as well as the terminator, which plenty of senders do for
		// readability. Preserved so a transformed interchange still looks like the one that arrived - a diff between
		// them should show the value that changed and nothing else.
		if trailing := newlineAfter(m, i); trailing != "" {
			out.WriteString(trailing)
		}
	}

	return Parse(out.Bytes())
}

// newlineAfter reports the whitespace that followed a segment terminator in the original bytes.
func newlineAfter(m *Message, index int) string {
	raw := m.Raw()
	found, ok := m.SegmentAt(index)
	if !ok {
		return ""
	}
	seg := found.raw
	if len(seg) == 0 {
		return ""
	}

	// Where this segment's text ends in the original. Searching rather than storing an offset, because Segment does not
	// keep one and adding a field to it would change a type the whole package shares for the benefit of this one case.
	at := bytes.Index(raw, seg)
	if at < 0 {
		return ""
	}
	after := at + len(seg)
	if after >= len(raw) || raw[after] != m.Delimiters().Segment {
		return ""
	}
	after++

	end := after
	for end < len(raw) && (raw[end] == '\r' || raw[end] == '\n') {
		end++
	}
	return string(raw[after:end])
}
