package ncpdp

import (
	"bytes"
	"fmt"
	"strings"
)

// headerFields is the fixed layout, in order. Kept as data so Build and parseHeader cannot disagree
// about a width, which is the sort of difference that produces a transmission a switch rejects with a
// message about a field nobody touched.
//
// The id is the standard's two-character field identifier, so a path written A1 or D1 resolves through this same table
// rather than a second copy of it. One table means a header field cannot be addressable and unbuildable, or the reverse.
var headerFields = []struct {
	id    string
	name  string
	width int
	get   func(Header) string
	set   func(*Header, string)
}{
	{"A1", "BIN number", 6, func(h Header) string { return h.BIN }, func(h *Header, v string) { h.BIN = v }},
	{"A2", "version/release", 2, func(h Header) string { return h.VersionRelease }, func(h *Header, v string) { h.VersionRelease = v }},
	{"A3", "transaction code", 2, func(h Header) string { return h.TransactionCode }, func(h *Header, v string) { h.TransactionCode = v }},
	{"A4", "processor control number", 10, func(h Header) string { return h.ProcessorControlNumber }, func(h *Header, v string) { h.ProcessorControlNumber = v }},
	{"A9", "transaction count", 1, func(h Header) string { return h.TransactionCount }, func(h *Header, v string) { h.TransactionCount = v }},
	{"B2", "service provider ID qualifier", 2, func(h Header) string { return h.ServiceProviderIDQualifier }, func(h *Header, v string) { h.ServiceProviderIDQualifier = v }},
	{"B1", "service provider ID", 15, func(h Header) string { return h.ServiceProviderID }, func(h *Header, v string) { h.ServiceProviderID = v }},
	{"D1", "date of service", 8, func(h Header) string { return h.DateOfService }, func(h *Header, v string) { h.DateOfService = v }},
	{"AK", "software vendor/certification ID", 10, func(h Header) string { return h.SoftwareVendorCertificationID }, func(h *Header, v string) { h.SoftwareVendorCertificationID = v }},
}

// Build writes a transmission.
//
// Over-long fields are refused rather than truncated. Truncating a fifteen-character pharmacy identifier
// to fit produces a well-formed transmission for a different pharmacy, and the switch has no way to know
// it was not meant.
func Build(m Message) ([]byte, error) {
	var buf bytes.Buffer

	total := 0
	for _, f := range headerFields {
		v := f.get(m.Header)
		if len(v) > f.width {
			return nil, fmt.Errorf(
				"header %s is %q, which is %d characters and the field is %d; it is refused rather than shortened, "+
					"because a shortened identifier is a valid value belonging to somebody else",
				f.name, v, len(v), f.width)
		}
		if strings.ContainsAny(v, "\x1c\x1d\x1e\x02\x03") {
			return nil, fmt.Errorf("header %s contains a separator byte, which would end the header early", f.name)
		}
		// Space padded on the right, which is what the standard specifies and what a fixed-width reader
		// on the other end expects.
		buf.WriteString(v)
		for i := len(v); i < f.width; i++ {
			buf.WriteByte(' ')
		}
		total += f.width
	}
	if total != HeaderLength {
		// A guard on the table above rather than on the input. If someone edits a width, this says so here
		// rather than at a payer six weeks later.
		return nil, fmt.Errorf("the header layout sums to %d bytes and the standard is %d", total, HeaderLength)
	}

	if len(m.Transactions) == 0 {
		return nil, fmt.Errorf("a transmission with no transactions carries a header asking for nothing")
	}
	if len(m.Transactions) > 4 {
		return nil, fmt.Errorf(
			"a transmission carries at most four transactions and this has %d; the header has one digit for the count, "+
				"so a fifth cannot be announced even if a switch would accept it", len(m.Transactions))
	}

	for i, t := range m.Transactions {
		if i > 0 {
			buf.WriteByte(GroupStart)
		}
		if len(t.Segments) == 0 {
			return nil, fmt.Errorf("transaction %d has no segments", i+1)
		}
		for _, s := range t.Segments {
			if err := writeSegment(&buf, s); err != nil {
				return nil, fmt.Errorf("transaction %d: %w", i+1, err)
			}
		}
	}
	return buf.Bytes(), nil
}

func writeSegment(buf *bytes.Buffer, s Segment) error {
	if len(s.ID) != 2 {
		return fmt.Errorf("segment identifier %q is %d characters and every identifier is two", s.ID, len(s.ID))
	}
	buf.WriteByte(SegmentStart)
	buf.WriteString(s.ID)

	for _, f := range s.Fields {
		if len(f.ID) != 2 {
			return fmt.Errorf("segment %s has a field identifier %q that is %d characters and every identifier is two",
				s.describe(), f.ID, len(f.ID))
		}
		if strings.ContainsAny(f.Value, "\x1c\x1d\x1e") {
			// A value containing a separator would split into fields that were never sent, and the segment
			// after it would gain fields belonging to this one. There is no escaping mechanism in the
			// standard, so there is nothing to do but refuse.
			return fmt.Errorf(
				"segment %s field %s contains a separator byte; the standard has no way to escape one, so the value "+
					"cannot be sent as it stands", s.describe(), f.ID)
		}
		buf.WriteByte(FieldStart)
		buf.WriteString(f.ID)
		buf.WriteString(f.Value)
	}
	return nil
}

// SetHeaderCount fills in the transaction count from the transactions present.
//
// Separate from Build rather than done inside it, because a count that disagrees with the segments is a
// real error worth reporting on the way in: a sender announcing three claims and supplying two has lost
// one, and quietly correcting the count throws away the evidence.
func (m *Message) SetHeaderCount() {
	m.Header.TransactionCount = fmt.Sprintf("%d", len(m.Transactions))
}

// CheckHeaderCount reports whether the announced count matches what is present.
func (m Message) CheckHeaderCount() error {
	if m.Header.TransactionCount == "" {
		return fmt.Errorf("the header does not say how many transactions it carries")
	}
	want := fmt.Sprintf("%d", len(m.Transactions))
	if m.Header.TransactionCount != want {
		return fmt.Errorf(
			"the header announces %s transaction(s) and %d are present; a claim has probably been lost rather than "+
				"the count being wrong", m.Header.TransactionCount, len(m.Transactions))
	}
	return nil
}
