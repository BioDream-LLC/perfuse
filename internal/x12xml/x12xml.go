// Package x12xml converts an X12 interchange to and from the node tree the
// transformation, filter and script layers already work on.
//
// This is the same trick hl7xml plays, and for the same reason. Everything above the
// parser - declarative transformation steps, the filter language, JavaScript,
// the channel test runner - operates on an xtree.Node rather than on a format. Render
// X12 into that tree and all of it works on claims without a line of new code in any of
// those packages. Write a second addressing layer instead and every one of them grows an
// X12 branch that has to be kept in step with the HL7 one for ever.
//
// # Element naming
//
// Elements are named in implementation-guide form: ISA13, CLM01, NM103.
//
// Two independent reasons point the same way. It is what every 837 and 835
// implementation guide writes, so a person reading the guide and a person reading the
// tree see the same token. And it is what Mirth's own X12 serialiser produces, so a
// Mirth script that walks msg['CLM']['CLM01'] keeps working - which is the whole
// migration promise.
//
// The element number is always two digits, matching the guides, so CLM01 rather than
// CLM1. Composite components add a further dotted number, CLM05.1, because X12
// composites are one level deep and there is nothing to collide with.
package x12xml

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/x12"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Root is the element name given to a serialised interchange.
//
// Matches Mirth's X12 serialiser so a migrated script that reaches for the root by name
// finds it.
const Root = "X12Interchange"

// FromMessage renders a parsed interchange as a node tree.
func FromMessage(msg *x12.Message) (*xtree.Node, error) {
	if msg == nil {
		return nil, fmt.Errorf("x12xml: no message")
	}

	root := xtree.New(Root)
	delim := msg.Delimiters()

	// The delimiters are recorded on the root, because two of them cannot be recovered
	// from the tree any other way.
	//
	// HL7 gets this for free: MSH-1 and MSH-2 hold the delimiters as content, so a
	// serialised message still describes its own encoding. X12 only does it halfway.
	// ISA16 holds the component separator and ISA11 the repetition separator, but the
	// element separator and segment terminator are purely structural - they exist
	// between elements and between segments and appear nowhere as a value. Parsing
	// consumes them and nothing is left.
	//
	// Without this, an interchange from a partner using | and + would come back out
	// with * and ~, which is a different dialect and a rejected file.
	setDelimiterAttrs(root, delim)

	for i := 0; i < msg.SegmentCount(); i++ {
		seg, ok := msg.SegmentAt(i)
		if !ok {
			continue
		}
		if seg.ID == "" {
			continue
		}

		node := xtree.New(seg.ID)

		for e := 1; e <= seg.ElementCount(); e++ {
			el := seg.Element(e)
			name := ElementName(seg.ID, e)

			// The ISA segment is fixed width, so its elements carry padding that is
			// part of the format rather than part of the value. It is preserved
			// exactly, because an ISA rebuilt with the padding stripped is no longer
			// 106 bytes and the next parser along cannot read its delimiters.
			//
			// Every other segment is variable width and its values are ordinary.
			if seg.ID == "ISA" {
				node.Append(xtree.Leaf(name, el.String()))
				continue
			}

			// Repetitions become repeated elements with the same name, which is what
			// makes indexing work the same way it does for HL7 fields.
			reps := el.Repetitions()
			for _, rep := range reps {
				elNode := xtree.New(name)
				addComponents(elNode, name, rep, delim)
				node.Append(elNode)
			}
		}

		root.Append(node)
	}

	root.SetParent(nil)
	return root, nil
}

// FromRaw parses and renders in one step.
func FromRaw(raw []byte) (*xtree.Node, error) {
	msg, err := x12.Parse(raw)
	if err != nil {
		return nil, err
	}
	return FromMessage(msg)
}

// ElementName renders the guide-form name of an element, for example "CLM01".
//
// Two digits below ten because that is how the guides write it. A single digit would
// read as a different element to anybody checking against an implementation guide, and
// the whole point of this naming is that the two agree.
func ElementName(segment string, element int) string {
	if element < 10 {
		return fmt.Sprintf("%s0%d", segment, element)
	}
	return fmt.Sprintf("%s%d", segment, element)
}

// addComponents fills in an element node, splitting a composite.
func addComponents(elNode *xtree.Node, elName string, raw string, delim x12.Delimiters) {
	if delim.Component == 0 || !strings.ContainsRune(raw, rune(delim.Component)) {
		elNode.SetText(raw)
		return
	}

	for i, comp := range strings.Split(raw, string(delim.Component)) {
		elNode.Append(xtree.Leaf(fmt.Sprintf("%s.%d", elName, i+1), comp))
	}
}

// ToX12 renders a node tree back to an interchange.
//
// The delimiters are read from the tree's own ISA segment rather than taken as an
// argument, because a tree that came from a real interchange already carries the ones
// its trading partner uses. Re-encoding a partner's file with different delimiters is
// how a round trip through a transformation quietly changes the file's dialect.
func ToX12(root *xtree.Node) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("x12xml: no tree")
	}
	if root.Name != Root && root.Name != "" && len(root.Children) > 0 {
		// Being strict here catches a transformation that replaced the root by
		// accident, which otherwise produces a file with one segment in it.
		return nil, fmt.Errorf("x12xml: the tree root is %q, expected %q", root.Name, Root)
	}

	delim, err := delimitersFrom(root)
	if err != nil {
		return nil, err
	}

	var out strings.Builder
	for _, seg := range root.Children {
		if !looksLikeSegmentID(seg.Name) {
			// Anything that is not a segment is skipped rather than guessed at. A
			// transformation can leave scratch nodes behind, and writing them into a
			// claims file would corrupt it.
			continue
		}
		if err := writeSegment(&out, seg, delim); err != nil {
			return nil, err
		}
	}
	return []byte(out.String()), nil
}

// Attribute names under which the delimiters travel on the root node.
const (
	attrElement   = "elementSeparator"
	attrComponent = "componentSeparator"
	attrSegment   = "segmentTerminator"
	attrRepeat    = "repetitionSeparator"
)

func setDelimiterAttrs(root *xtree.Node, d x12.Delimiters) {
	root.SetAttr(attrElement, string(d.Element))
	root.SetAttr(attrComponent, string(d.Component))
	root.SetAttr(attrSegment, string(d.Segment))
	if d.Repeat != 0 {
		root.SetAttr(attrRepeat, string(d.Repeat))
	}
}

// delimitersFrom recovers the delimiters for re-encoding.
//
// Attributes first, because they are the only place the element separator and segment
// terminator survive. ISA content second, so a tree assembled by hand with a proper ISA
// still encodes in the right dialect. The near-universal defaults last, for a
// transformation building an interchange from nothing.
func delimitersFrom(root *xtree.Node) (x12.Delimiters, error) {
	d := x12.Delimiters{Element: '*', Component: ':', Segment: '~'}

	if v := root.AttrValue(attrElement); v != "" {
		d.Element = v[0]
	}
	if v := root.AttrValue(attrComponent); v != "" {
		d.Component = v[0]
	}
	if v := root.AttrValue(attrSegment); v != "" {
		d.Segment = v[0]
	}
	if v := root.AttrValue(attrRepeat); v != "" {
		d.Repeat = v[0]
	}

	isa := root.First("ISA")
	if isa == nil {
		return d, nil
	}

	// ISA16 is the component separator, and ISA11 is the repetition separator from
	// version 00501. Only consulted where an attribute did not already say.
	if root.AttrValue(attrComponent) == "" {
		if v := valueOf(isa, "ISA16"); v != "" {
			d.Component = v[0]
		}
	}
	if root.AttrValue(attrRepeat) == "" {
		version := strings.TrimSpace(valueOf(isa, "ISA12"))
		if version >= "00501" {
			if v := valueOf(isa, "ISA11"); v != "" {
				d.Repeat = v[0]
			}
		}
	}
	return d, nil
}

func valueOf(seg *xtree.Node, name string) string {
	n := seg.First(name)
	if n == nil {
		return ""
	}
	return n.Value()
}

// writeSegment encodes one segment and its terminator.
func writeSegment(out *strings.Builder, seg *xtree.Node, delim x12.Delimiters) error {
	out.WriteString(seg.Name)

	// Elements are written by number rather than in tree order, so a transformation
	// that appended an element out of order still produces a valid segment. X12 is
	// positional: an element written in the wrong slot is not a formatting problem, it
	// is a different value.
	byNumber := map[int][]*xtree.Node{}
	highest := 0
	for _, child := range seg.Children {
		n, ok := elementNumber(seg.Name, child.Name)
		if !ok {
			continue
		}
		byNumber[n] = append(byNumber[n], child)
		if n > highest {
			highest = n
		}
	}

	for i := 1; i <= highest; i++ {
		out.WriteByte(delim.Element)

		nodes := byNumber[i]
		if len(nodes) == 0 {
			continue
		}

		sep := ""
		if len(nodes) > 1 {
			if delim.Repeat == 0 {
				// Refused rather than joined on some substitute character. An
				// interchange before version 00501 has no way to express a repeated
				// element, and inventing one produces a file the receiver reads as a
				// single value with punctuation in it.
				return fmt.Errorf("x12xml: %s%02d has %d repetitions but the interchange has no repetition separator (ISA12 is before 00501)", seg.Name, i, len(nodes))
			}
			sep = string(delim.Repeat)
		}

		for j, node := range nodes {
			if j > 0 {
				out.WriteString(sep)
			}
			out.WriteString(encodeElement(node, delim))
		}
	}

	out.WriteByte(delim.Segment)
	return nil
}

// encodeElement writes one element, joining components if it has them.
func encodeElement(node *xtree.Node, delim x12.Delimiters) string {
	if node.Simple() {
		return node.Text
	}

	// Components are positional too, so they are written by number. A trailing empty
	// component is written rather than truncated: a partner who sends "11:" gets "11:"
	// back, and truncating to "11" would mean an unrelated transformation changed the
	// bytes of a claims file. The two are equivalent to a receiver, but only one of
	// them round-trips.
	byNumber := map[int]string{}
	highest := 0
	for _, comp := range node.Children {
		n, ok := trailingNumber(node.Name, comp.Name)
		if !ok {
			continue
		}
		byNumber[n] = comp.Value()
		if n > highest {
			highest = n
		}
	}

	var parts []string
	for i := 1; i <= highest; i++ {
		parts = append(parts, byNumber[i])
	}
	return strings.Join(parts, string(delim.Component))
}

// elementNumber reads the element number out of a node name such as "CLM01".
func elementNumber(segment, element string) (int, bool) {
	if !strings.HasPrefix(element, segment) {
		return 0, false
	}
	rest := element[len(segment):]
	if rest == "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// trailingNumber reads the component number out of a name such as "CLM05.1".
func trailingNumber(parent, child string) (int, bool) {
	if !strings.HasPrefix(child, parent+".") {
		return 0, false
	}
	rest := child[len(parent)+1:]
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// looksLikeSegmentID reports whether a node name could be an X12 segment.
func looksLikeSegmentID(name string) bool {
	if len(name) < 2 || len(name) > 3 {
		return false
	}
	if !isLetter(name[0]) {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isLetter(name[i]) && !isDigit(name[i]) {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
