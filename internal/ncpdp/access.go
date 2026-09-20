// Writing a field in an NCPDP transmission.
//
// # Why a write refuses an ambiguous path
//
// Read returns every value a path addresses, because a filter asking whether any DUR conflict is a therapeutic
// duplication should see all of them. A write cannot work that way. If D7 addresses the product codes of two claims in one
// transmission, setting D7 has no single meaning: writing both would change a drug nobody asked about, and writing the
// first would silently leave the second.
//
// So a write refuses a path that addresses more than one field, and the error names the qualified form that would be
// unambiguous. This is the same reasoning that gives the transformation steps no remove action: where the honest answer is
// that the configuration is ambiguous, saying so beats picking one.
package ncpdp

import (
	"fmt"
	"strings"
)

// Set returns a copy of the transmission with the value written at the path.
//
// The receiver is not modified. A transformation sequence runs each step on the output of the last, and a step that
// mutated its input would make the recorded original wrong - which is the copy anybody investigating a claim reads first.
func Set(m Message, p Path, value string) (Message, error) {
	if p.Segment == "" {
		if _, ok := headerValue(m.Header, p.Field); ok {
			return setHeader(m, p, value)
		}
	}

	// Locate the field first, so an ambiguous path is refused before anything is copied.
	hits := locate(m, p)
	switch len(hits) {
	case 0:
		// A field the transmission does not carry. Added to the segment the path names, because a step setting a field
		// that a partner omitted is the ordinary reason to write one at all.
		return addField(m, p, value)
	case 1:
		// The single unambiguous case.
	default:
		return Message{}, fmt.Errorf("%s addresses %d fields in this transmission, so writing it has no single "+
			"meaning; name the segment and occurrence, such as %s(1)-%s", p.String(), len(hits),
			segmentOf(m, hits[0]), strings.ToUpper(p.Field))
	}

	out := clone(m)
	h := hits[0]
	out.Transactions[h.tx].Segments[h.seg].Fields[h.field].Value = value
	return out, nil
}

// Clear blanks a field, leaving it present.
//
// Present and empty rather than removed, because the two are different on the wire and a payer reads them differently: a
// blank field was sent deliberately and an absent one was never sent.
func Clear(m Message, p Path) (Message, error) { return Set(m, p, "") }

// hit is one located field.
type hit struct{ tx, seg, field int }

// locate finds every field the path addresses.
func locate(m Message, p Path) []hit {
	var out []hit
	want := strings.ToUpper(p.Field)

	for ti, tx := range m.Transactions {
		occurrence := 0
		for si, seg := range tx.Segments {
			if p.Segment != "" {
				if seg.ID != p.Segment {
					continue
				}
				occurrence++
				if p.SegmentOccurs > 0 && occurrence != p.SegmentOccurs {
					continue
				}
			}
			for fi, f := range seg.Fields {
				if strings.ToUpper(f.ID) == want {
					out = append(out, hit{ti, si, fi})
				}
			}
		}
	}
	return out
}

// segmentOf names the segment a hit landed in, for the ambiguity error.
func segmentOf(m Message, h hit) string { return m.Transactions[h.tx].Segments[h.seg].ID }

// setHeader writes a header field.
//
// Length is checked here rather than left to Build. Build refuses an over-long field, which is right, but by then the
// message has passed through the rest of the pipeline and the error arrives a long way from the step that caused it.
// Refusing at the write names the step and the width.
func setHeader(m Message, p Path, value string) (Message, error) {
	want := strings.ToUpper(p.Field)
	for _, f := range headerFields {
		if f.id != want {
			continue
		}
		if len(value) > f.width {
			return Message{}, fmt.Errorf("%s is the %s, which is %d characters, and %q is %d; NCPDP header fields are "+
				"fixed width and truncating this one would produce a valid transmission carrying the wrong value",
				p.String(), f.name, f.width, value, len(value))
		}
		out := clone(m)
		f.set(&out.Header, value)
		return out, nil
	}
	return Message{}, fmt.Errorf("%s is not a header field", p.String())
}

// addField appends a field the transmission did not carry.
func addField(m Message, p Path, value string) (Message, error) {
	if p.Segment == "" {
		return Message{}, fmt.Errorf("%s is not present in this transmission, and without a segment there is nowhere "+
			"to put it; name the segment, such as 07-%s", p.String(), strings.ToUpper(p.Field))
	}

	out := clone(m)
	occurrence := 0
	for ti := range out.Transactions {
		for si := range out.Transactions[ti].Segments {
			if out.Transactions[ti].Segments[si].ID != p.Segment {
				continue
			}
			occurrence++
			if p.SegmentOccurs > 0 && occurrence != p.SegmentOccurs {
				continue
			}
			out.Transactions[ti].Segments[si].Fields = append(
				out.Transactions[ti].Segments[si].Fields,
				Field{ID: strings.ToUpper(p.Field), Value: value},
			)
			return out, nil
		}
	}

	return Message{}, fmt.Errorf("%s names segment %s, which this transmission does not carry, so there is nowhere to "+
		"write the field", p.String(), p.Segment)
}

// clone returns a deep copy.
//
// Deep because a Transaction holds a slice of Segment and a Segment a slice of Field, so a shallow copy would share the
// field values with the original and a write would reach back into the message the pipeline recorded.
func clone(m Message) Message {
	out := Message{Header: m.Header}
	out.Transactions = make([]Transaction, len(m.Transactions))
	for ti, tx := range m.Transactions {
		segs := make([]Segment, len(tx.Segments))
		for si, seg := range tx.Segments {
			fields := make([]Field, len(seg.Fields))
			copy(fields, seg.Fields)
			segs[si] = Segment{ID: seg.ID, Fields: fields}
		}
		out.Transactions[ti] = Transaction{Segments: segs}
	}
	return out
}

// ReadOne returns the single value a path addresses, and whether there was exactly one.
//
// For the transformation steps, which need a value to read before they can write one back. Reports false for an ambiguous
// path as well as an absent one, so a step never reads one of several fields and writes to another.
func ReadOne(m Message, p Path) (string, bool) {
	vals := Read(m, p)
	if len(vals) != 1 {
		return "", false
	}
	return vals[0], true
}
