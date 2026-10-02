// Package hl7xml converts HL7 v2 messages to and from the XML shape Mirth
// Connect uses, and back to ER7 encoding.
//
// The shape is not a design choice. Mirth's scripts are written against the
// output of its own ER7-to-XML serialiser, so they contain expressions like
//
//	msg['PID']['PID.5']['PID.5.1'].toString()
//
// and the promise that an existing script runs unchanged is only kept if the
// tree it walks has exactly the same element names, nesting and repetition
// behaviour. Every decision below is therefore "what does Mirth do", not "what
// would be tidier".
//
// The two that matter most:
//
// Every field is wrapped in numbered component elements even when the field has
// only one component, so PID-5 with the value "Doe" still produces
// <PID.5><PID.5.1>Doe</PID.5.1></PID.5>. Scripts index straight to .1 constantly
// and a tree that put the text on the field element would break nearly all of
// them.
//
// MSH keeps standard HL7 field numbers, where MSH-1 is the field separator and
// MSH-9 is the message type. The parser in internal/hl7 indexes MSH with an
// offset because the separator occupies a position no delimiter splits, and that
// offset stops here: a script saying MSH.9 means the message type.
package hl7xml

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Root is the element name Mirth gives a serialised HL7 message.
const Root = "HL7Message"

// Options controls the conversion.
type Options struct {
	// Separators is used when converting a tree back to ER7. When zero the
	// defaults are used.
	Separators hl7.Separators

	// StripEmptyTrailing drops trailing empty fields from each segment on the
	// way back to ER7. Mirth does this, and it matters because some receiving
	// systems reject a segment padded out to 40 empty fields.
	StripEmptyTrailing bool
}

// DefaultOptions returns the conversion Mirth performs.
func DefaultOptions() Options {
	return Options{Separators: hl7.DefaultSeparators(), StripEmptyTrailing: true}
}

// FromMessage converts a parsed HL7 message into Mirth's XML tree.
func FromMessage(msg *hl7.Message) (*xtree.Node, error) {
	if msg == nil {
		return nil, fmt.Errorf("hl7xml: no message")
	}
	sep := msg.Separators()

	root := xtree.New(Root)

	for i := 0; i < msg.SegmentCount(); i++ {
		seg, ok := msg.SegmentAt(i)
		if !ok {
			continue
		}
		name := seg.Name()
		if name == "" {
			continue
		}
		node := xtree.New(name)

		isMSH := name == "MSH"
		count := seg.FieldCount()

		for field := 1; field <= count; field++ {
			// MSH-1 and MSH-2 are the delimiters themselves. They are not
			// component-split, because the separator characters would be
			// interpreted as separators, and Mirth writes them as plain text.
			if isMSH && field <= 2 {
				node.Append(xtree.Leaf(fmt.Sprintf("MSH.%d", field), delimiterText(field, sep)))
				continue
			}

			value := seg.Field(field)
			fieldName := fmt.Sprintf("%s.%d", name, field)

			// Repetitions become repeated elements with the same name, which is
			// what makes E4X indexing work.
			reps := value.Repeats()
			if len(reps) == 0 {
				reps = []hl7.Value{value}
			}
			for _, rep := range reps {
				fieldNode := xtree.New(fieldName)
				addComponents(fieldNode, fieldName, rep, sep)
				node.Append(fieldNode)
			}
		}

		root.Append(node)
	}

	root.SetParent(nil)
	return root, nil
}

func delimiterText(field int, sep hl7.Separators) string {
	if field == 1 {
		return string(sep.Field)
	}
	return sep.EncodingCharacters()
}

// addComponents fills in the numbered component elements of one field
// repetition, descending into subcomponents only when the data has them.
func addComponents(fieldNode *xtree.Node, fieldName string, rep hl7.Value, sep hl7.Separators) {
	count := rep.ComponentCount()
	if count < 1 {
		count = 1
	}

	for i := 1; i <= count; i++ {
		comp := rep.Component(i)
		compName := fmt.Sprintf("%s.%d", fieldName, i)
		compNode := xtree.New(compName)

		if subs := comp.SubcomponentCount(); subs > 1 {
			for j := 1; j <= subs; j++ {
				compNode.Append(xtree.Leaf(fmt.Sprintf("%s.%d", compName, j), comp.Subcomponent(j).String()))
			}
		} else {
			compNode.Text = comp.String()
		}

		fieldNode.Append(compNode)
	}
}

// FromRaw parses and converts in one step.
func FromRaw(raw []byte) (*xtree.Node, error) {
	msg, err := hl7.Parse(raw)
	if err != nil {
		return nil, err
	}
	return FromMessage(msg)
}

// ToER7 converts a tree back to wire-format HL7.
//
// It reads the delimiters out of MSH-1 and MSH-2 in the tree rather than from
// the options when they are present, because a script is allowed to change them
// and a message encoded with delimiters that disagree with its own MSH is
// unparseable by the receiver.
func ToER7(root *xtree.Node, opts Options) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("hl7xml: no tree")
	}
	if opts.Separators.Field == 0 {
		opts.Separators = hl7.DefaultSeparators()
	}
	sep := opts.Separators

	segments := root.Children
	if root.Name != Root && root.Name != "" && len(root.Children) > 0 {
		// Tolerate a tree whose root a script replaced.
		if looksLikeSegment(root.Name) {
			segments = []*xtree.Node{root}
		}
	}

	if msh := findSegment(segments, "MSH"); msh != nil {
		if v := msh.First("MSH.1"); v != nil && v.Value() != "" {
			sep.Field = v.Value()[0]
		}
		if v := msh.First("MSH.2"); v != nil {
			if enc := v.Value(); len(enc) >= 4 {
				sep.Component, sep.Repeat = enc[0], enc[1]
				sep.Escape, sep.Subcomponent = enc[2], enc[3]
			}
		}
	}

	var out strings.Builder
	for _, seg := range segments {
		if !looksLikeSegment(seg.Name) {
			continue
		}
		if err := writeSegment(&out, seg, sep, opts); err != nil {
			return nil, err
		}
		out.WriteByte('\r')
	}

	if out.Len() == 0 {
		return nil, fmt.Errorf("hl7xml: tree contains no segments")
	}
	return []byte(out.String()), nil
}

func looksLikeSegment(name string) bool {
	if len(name) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		c := name[i]
		upper := c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		if !upper && !(i > 0 && digit) {
			return false
		}
	}
	return true
}

func findSegment(segments []*xtree.Node, name string) *xtree.Node {
	for _, s := range segments {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func writeSegment(out *strings.Builder, seg *xtree.Node, sep hl7.Separators, opts Options) error {
	name := seg.Name
	out.WriteString(name)

	// Group children by field number. A script may have appended fields out of
	// order, or created a gap, and both have to come out in numeric order with
	// the gap preserved as an empty field.
	byField := map[int][]*xtree.Node{}
	highest := 0
	for _, child := range seg.Children {
		num, ok := fieldNumber(name, child.Name)
		if !ok {
			continue
		}
		byField[num] = append(byField[num], child)
		if num > highest {
			highest = num
		}
	}

	isMSH := name == "MSH"
	fields := make([]string, highest+1)

	for num, reps := range byField {
		if isMSH && num == 1 {
			continue // The field separator is written by the joining below.
		}
		if isMSH && num == 2 {
			fields[num] = string([]byte{sep.Component, sep.Repeat, sep.Escape, sep.Subcomponent})
			continue
		}
		parts := make([]string, 0, len(reps))
		for _, rep := range reps {
			parts = append(parts, encodeField(rep, sep))
		}
		fields[num] = strings.Join(parts, string(sep.Repeat))
	}

	last := highest
	if opts.StripEmptyTrailing {
		for last > 0 && fields[last] == "" {
			last--
		}
	}

	// MSH is special: MSH-1 *is* the separator that joins MSH-2 onward, so the
	// segment name is followed directly by the separator and then MSH-2.
	start := 1
	if isMSH {
		start = 2
	}
	for num := start; num <= last; num++ {
		out.WriteByte(sep.Field)
		out.WriteString(fields[num])
	}
	return nil
}

// fieldNumber extracts N from an element named SEG.N.
func fieldNumber(segment, element string) (int, bool) {
	prefix := segment + "."
	if !strings.HasPrefix(element, prefix) {
		return 0, false
	}
	rest := element[len(prefix):]
	if strings.ContainsRune(rest, '.') {
		return 0, false // A component element cannot be a direct field child.
	}
	num, err := strconv.Atoi(rest)
	if err != nil || num < 1 {
		return 0, false
	}
	return num, true
}

// encodeField renders one field repetition, escaping as it goes.
func encodeField(field *xtree.Node, sep hl7.Separators) string {
	// A field a script assigned a plain string to has text and no components.
	if len(field.Children) == 0 {
		return hl7.Escape(field.Text, sep)
	}

	byComp := map[int]*xtree.Node{}
	highest := 0
	for _, child := range field.Children {
		num, ok := trailingNumber(field.Name, child.Name)
		if !ok {
			continue
		}
		byComp[num] = child
		if num > highest {
			highest = num
		}
	}
	if highest == 0 {
		return hl7.Escape(field.Value(), sep)
	}

	comps := make([]string, highest)
	for num, node := range byComp {
		comps[num-1] = encodeComponent(node, sep)
	}
	for len(comps) > 1 && comps[len(comps)-1] == "" {
		comps = comps[:len(comps)-1]
	}
	return strings.Join(comps, string(sep.Component))
}

func encodeComponent(comp *xtree.Node, sep hl7.Separators) string {
	if len(comp.Children) == 0 {
		return hl7.Escape(comp.Text, sep)
	}

	bySub := map[int]*xtree.Node{}
	highest := 0
	for _, child := range comp.Children {
		num, ok := trailingNumber(comp.Name, child.Name)
		if !ok {
			continue
		}
		bySub[num] = child
		if num > highest {
			highest = num
		}
	}
	if highest == 0 {
		return hl7.Escape(comp.Value(), sep)
	}

	subs := make([]string, highest)
	for num, node := range bySub {
		subs[num-1] = hl7.Escape(node.Value(), sep)
	}
	for len(subs) > 1 && subs[len(subs)-1] == "" {
		subs = subs[:len(subs)-1]
	}
	return strings.Join(subs, string(sep.Subcomponent))
}

// trailingNumber extracts the last number from a child element name given its
// parent's name, so PID.5.1 under PID.5 yields 1.
func trailingNumber(parent, child string) (int, bool) {
	prefix := parent + "."
	if !strings.HasPrefix(child, prefix) {
		return 0, false
	}
	num, err := strconv.Atoi(child[len(prefix):])
	if err != nil || num < 1 {
		return 0, false
	}
	return num, true
}
