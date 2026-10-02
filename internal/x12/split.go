package x12

import (
	"bytes"
	"fmt"
	"strconv"
)

// Splitting an interchange into one interchange per transaction set.
//
// This is the operation X12 integrators reach for most. A single 837 file from a
// practice management system routinely carries hundreds of claims across many
// transaction sets, and the systems downstream want them one at a time - one claim, one
// message, one acknowledgement, one row. The same is true in reverse for an 835: a
// payer sends one file and the posting system wants each remittance separately.
//
// # Why the envelope segments are copied as raw bytes
//
// Each output has to be a complete, valid interchange: ISA, GS, the transaction set,
// GE, IEA. The obvious implementation rebuilds ISA and GS from their parsed elements,
// and that is wrong. ISA is fixed width, so re-encoding it risks changing the padding,
// and an ISA that is no longer 106 bytes is one whose delimiters cannot be read from
// their byte offsets. The original bytes are copied verbatim instead, which makes that
// class of mistake unavailable.
//
// # Why each set keeps its own functional group
//
// GS08 carries the implementation guide - 005010X222A1 for a professional claim,
// 005010X221A1 for a remittance - and an interchange may hold more than one group.
// Wrapping every set in the first group's GS would relabel an 835 as an 837, and the
// receiver would reject it or, worse, try to read it as one.

// Split returns one complete interchange per transaction set.
//
// A file with a single transaction set is returned unchanged and byte-identical, not
// rebuilt. Most files are single-set, and rewriting one would mean a channel configured
// to split was quietly altering files it had nothing to do.
func (m *Message) Split() ([]*Message, error) {
	groups := m.groupSpans()
	if len(groups) == 0 {
		return nil, fmt.Errorf("x12: nothing to split: the interchange has no functional group")
	}

	total := 0
	for _, g := range groups {
		total += len(g.sets)
	}
	switch total {
	case 0:
		// Returning an empty slice here would make the message disappear. A caller
		// that asked for transaction sets and received none needs to hear about it,
		// because the alternative is a file that was accepted and then vanished.
		return nil, fmt.Errorf("x12: nothing to split: the interchange has no transaction set")
	case 1:
		return []*Message{m}, nil
	}

	isa, ok := m.Segment("ISA", 1)
	if !ok {
		return nil, fmt.Errorf("x12: nothing to split: the interchange has no ISA segment")
	}
	iea, hasIEA := m.Segment("IEA", 1)
	if !hasIEA {
		// Splitting an incomplete file would turn one detectably broken interchange
		// into several that each look complete. Refusing keeps the fault visible.
		return nil, fmt.Errorf("x12: refusing to split an interchange with no IEA trailer, because the result would be several files that each look intact")
	}

	term := m.delim.Segment
	out := make([]*Message, 0, total)

	for _, g := range groups {
		for _, set := range g.sets {
			var buf bytes.Buffer

			writeSeg(&buf, isa.Raw(), term)
			writeSeg(&buf, g.gs.Raw(), term)
			for _, seg := range set {
				writeSeg(&buf, seg.Raw(), term)
			}
			// One transaction set in this group, and one group in this interchange.
			writeSeg(&buf, m.rebuildTrailer("GE", 1, g.ge), term)
			writeSeg(&buf, m.rebuildTrailer("IEA", 1, iea), term)

			// Parsed rather than returned as bytes, so a fault in this function
			// surfaces here instead of at a trading partner.
			part, err := Parse(buf.Bytes())
			if err != nil {
				return nil, fmt.Errorf("x12: split produced an interchange that does not parse: %w", err)
			}
			if v := part.Validate(); !v.OK() {
				return nil, fmt.Errorf("x12: split produced an interchange that fails its own envelope check: %w", v.Err())
			}
			out = append(out, part)
		}
	}
	return out, nil
}

// groupSpan is one functional group and the transaction sets inside it.
type groupSpan struct {
	gs   Segment
	ge   Segment
	sets [][]Segment
}

// groupSpans walks the interchange once, attributing each transaction set to its group.
//
// A single pass rather than repeated lookups, because the same segment identifiers
// recur throughout and position is the only thing that says which group a set is in.
func (m *Message) groupSpans() []groupSpan {
	var groups []groupSpan
	var current *groupSpan
	var set []Segment
	inSet := false

	for _, seg := range m.segs {
		switch seg.ID {
		case "GS":
			groups = append(groups, groupSpan{gs: seg})
			current = &groups[len(groups)-1]
			inSet = false
			set = nil

		case "GE":
			if current != nil {
				current.ge = seg
			}
			current = nil
			inSet = false
			set = nil

		case "ST":
			inSet = true
			set = []Segment{seg}

		case "SE":
			if inSet && current != nil {
				set = append(set, seg)
				current.sets = append(current.sets, set)
			}
			inSet = false
			set = nil

		default:
			if inSet {
				set = append(set, seg)
			}
		}
	}
	return groups
}

// rebuildTrailer rewrites a GE or IEA with a corrected count in element 1.
//
// Element 2 - the control number - is preserved from the original rather than
// regenerated. It is the audit trail back to the file the trading partner sent, and
// reconciliation conversations are conducted in those numbers. Duplicate detection at
// the far end keys on the transaction set control number as well, and that differs per
// set, so preserving the interchange number does not cause false duplicates.
func (m *Message) rebuildTrailer(id string, count int, original Segment) []byte {
	var buf bytes.Buffer
	buf.WriteString(id)
	buf.WriteByte(m.delim.Element)
	buf.WriteString(strconv.Itoa(count))

	// Everything from element 2 onward is carried across untouched.
	for i := 2; i <= original.ElementCount(); i++ {
		buf.WriteByte(m.delim.Element)
		buf.WriteString(original.Element(i).String())
	}
	return buf.Bytes()
}

func writeSeg(buf *bytes.Buffer, raw []byte, term byte) {
	buf.Write(raw)
	buf.WriteByte(term)
}

// SetCount reports how many transaction sets Split would produce.
//
// Useful before splitting, so a channel can log or meter the fan-out without building
// every message first. A file of four hundred claims is a different operational event
// from a file of one, and knowing which before the work starts is worth a method.
func (m *Message) SetCount() int {
	n := 0
	for _, g := range m.groupSpans() {
		n += len(g.sets)
	}
	return n
}
