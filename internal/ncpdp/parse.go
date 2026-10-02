package ncpdp

import (
	"fmt"
	"strings"
)

// Parse reads a transmission.
//
// The header is fixed width, so it is measured rather than scanned. Everything after it is start-marked:
// a 0x1E begins a segment, a 0x1C begins a field, a 0x1D begins the next transaction.
func Parse(data []byte) (Message, error) {
	if len(data) == 0 {
		return Message{}, fmt.Errorf("empty transmission")
	}

	// Batch framing, if present, is stripped before anything else. A batch transaction is wrapped in
	// STX/ETX and carries a G1 identifier and a ten-character reference number ahead of the header, so a
	// reader that does not strip them measures the header from the wrong place and every field is wrong.
	body, batch, err := stripBatchFraming(data)
	if err != nil {
		return Message{}, err
	}

	if len(body) < HeaderLength {
		return Message{}, fmt.Errorf(
			"transmission is %d bytes and the transaction header alone is %d; a short header is not padded because "+
				"the fields are fixed width with no separators, so a missing byte shifts every field after it and a "+
				"billing request reads as a different transaction entirely",
			len(body), HeaderLength)
	}

	// A separator inside the fixed header means the header was short.
	//
	// Length alone cannot catch this. A fifty-five byte header followed by segments is still more than
	// fifty-six bytes, so the header slice simply reaches into the first segment and every field lands one
	// place left - and each one is still the right width, so nothing looks wrong. The BIN absorbs part of
	// the version, the transaction code becomes part of the processor control number, and a B1 billing
	// request parses as some other transaction with plausible values throughout.
	//
	// What makes it detectable is that a correct header never contains a separator byte: the fields are
	// fixed width precisely so that none is needed. So finding one inside the first fifty-six bytes means
	// the boundary is wrong, and where it was found says how far out it is.
	if i := indexAnySeparator(body[:HeaderLength]); i >= 0 {
		return Message{}, fmt.Errorf(
			"the transaction header contains a separator byte at position %d, and a header is %d fixed-width bytes "+
				"with no separators in it; the header is short by %d, which shifts every field after the gap while "+
				"leaving each one the right length",
			i, HeaderLength, HeaderLength-i)
	}

	h := parseHeader(body[:HeaderLength])
	rest := body[HeaderLength:]

	msg := Message{Header: h}
	if batch {
		// Recorded on the header so a caller can tell a batch transaction from a telecom one. The layouts
		// differ and a response has to match what was sent.
		msg.Header.VersionRelease = h.VersionRelease
	}

	// Transactions are separated by the group separator. Split rather than start-marked: the first
	// transaction is not introduced by one, it simply follows the header.
	chunks := splitOn(rest, GroupStart)
	for _, chunk := range chunks {
		if len(trimSeparators(chunk)) == 0 {
			// An empty chunk is a group separator with nothing after it, which happens when a sender ends
			// the transmission with one. Skipped rather than reported as an empty transaction, because an
			// empty transaction would fail validation for a reason the sender did not cause.
			continue
		}
		t, err := parseTransaction(chunk)
		if err != nil {
			return Message{}, err
		}
		msg.Transactions = append(msg.Transactions, t)
	}

	if len(msg.Transactions) == 0 {
		return Message{}, fmt.Errorf("transmission has a header for a %s transaction but no segments after it",
			describeCode(h.TransactionCode))
	}
	return msg, nil
}

func describeCode(code string) string {
	if n := TransactionName(code); n != "" {
		return n
	}
	return fmt.Sprintf("%q", code)
}

// stripBatchFraming removes STX/ETX wrapping and the batch-only fields ahead of the header.
//
// The batch flavour prepends a G1 identifier and a ten-character transaction reference number. Both are
// removed here so the rest of the parser measures the fixed header from one place.
func stripBatchFraming(data []byte) (body []byte, wasBatch bool, err error) {
	// Leading and trailing framing first. Trailing ETX may be followed by nothing or by line endings.
	s := data
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	if len(s) == 0 || s[0] != STX {
		return s, false, nil
	}
	s = s[1:]
	if len(s) > 0 && s[len(s)-1] == ETX {
		s = s[:len(s)-1]
	}

	// G1 plus a ten-character reference number. Checked rather than assumed: a batch header or trailer
	// also starts with STX and carries a different identifier, and reading one as a transaction would
	// produce a claim out of a file's control record.
	if len(s) < 2 {
		return nil, false, fmt.Errorf("batch transaction is truncated: nothing after the start marker")
	}
	id := string(s[:2])
	if id != "G1" {
		return nil, false, fmt.Errorf(
			"batch record identifier is %q, and a transaction is G1; G0 is the batch header and G9 the trailer, "+
				"which are file control records rather than claims", id)
	}
	s = s[2:]
	if len(s) < 10 {
		return nil, false, fmt.Errorf("batch transaction is truncated: it has no room for the ten-character reference number")
	}
	return s[10:], true, nil
}

func parseHeader(b []byte) Header {
	// Positions rather than a loop over widths, so each one is readable against a payer sheet.
	at := func(start, length int) string {
		return strings.TrimRight(string(b[start:start+length]), " ")
	}
	return Header{
		BIN:                           at(0, 6),
		VersionRelease:                at(6, 2),
		TransactionCode:               at(8, 2),
		ProcessorControlNumber:        at(10, 10),
		TransactionCount:              at(20, 1),
		ServiceProviderIDQualifier:    at(21, 2),
		ServiceProviderID:             at(23, 15),
		DateOfService:                 at(38, 8),
		SoftwareVendorCertificationID: at(46, 10),
	}
}

func parseTransaction(chunk []byte) (Transaction, error) {
	var t Transaction

	// Segments are start-marked, so the text before the first marker is not a segment. It is normally
	// empty; anything else is data outside any segment and is reported rather than attached to the
	// segment that follows, which is where it would otherwise silently end up.
	parts := splitOn(chunk, SegmentStart)
	for i, p := range parts {
		if i == 0 {
			if leftover := trimSeparators(p); len(leftover) > 0 {
				return Transaction{}, fmt.Errorf(
					"%d bytes appear before the first segment marker (%q); data outside a segment belongs to no field",
					len(leftover), clip(string(leftover)))
			}
			continue
		}
		if len(trimSeparators(p)) == 0 {
			continue
		}
		s, err := parseSegment(p)
		if err != nil {
			return Transaction{}, err
		}
		t.Segments = append(t.Segments, s)
	}

	if len(t.Segments) == 0 {
		return Transaction{}, fmt.Errorf("transaction contains no segments")
	}
	return t, nil
}

func parseSegment(b []byte) (Segment, error) {
	// The segment identifier comes first, before any field marker.
	fieldParts := splitOn(b, FieldStart)
	if len(fieldParts) == 0 {
		return Segment{}, fmt.Errorf("segment is empty")
	}

	id := strings.TrimSpace(string(fieldParts[0]))
	if id == "" {
		return Segment{}, fmt.Errorf("segment has no identifier after its start marker")
	}
	if len(id) != 2 {
		return Segment{}, fmt.Errorf(
			"segment identifier %q is %d characters and every identifier is two; a wrong length here means the "+
				"segment boundary was misread and the fields belong to a different segment", id, len(id))
	}

	s := Segment{ID: id}

	// Every remaining part is a field, because 0x1C starts one. The first part is the identifier and is
	// not a field, which is the off-by-one that treating these as separators would introduce.
	for _, fp := range fieldParts[1:] {
		raw := string(fp)
		if strings.TrimSpace(raw) == "" {
			// A field marker with nothing after it. Kept out rather than stored as an empty field: senders
			// pad, and an empty value is not the same as a field that was sent blank on purpose. Anything
			// relying on the difference would be relying on a sender's padding habits.
			continue
		}
		if len(raw) < 2 {
			return Segment{}, fmt.Errorf("segment %s has a field %q shorter than its two-character identifier", s.describe(), raw)
		}
		s.Fields = append(s.Fields, Field{
			ID: raw[:2],
			// Right-trimmed only. A leading space can be significant in a name or an address, and trimming
			// both would quietly edit patient data.
			Value: strings.TrimRight(raw[2:], " "),
		})
	}
	return s, nil
}

func (s Segment) describe() string {
	if n := SegmentName(s.ID); n != "" {
		return fmt.Sprintf("%s (%s)", s.ID, n)
	}
	return s.ID
}

// splitOn divides on a single byte, keeping empty pieces so callers can tell where markers were.
func splitOn(b []byte, sep byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == sep {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	out = append(out, b[start:])
	return out
}

// indexAnySeparator returns the position of the first separator byte, or -1.
func indexAnySeparator(b []byte) int {
	for i, c := range b {
		switch c {
		case SegmentStart, FieldStart, GroupStart, STX, ETX:
			return i
		}
	}
	return -1
}

// trimSeparators removes whitespace and any stray control bytes, for emptiness checks only.
func trimSeparators(b []byte) []byte {
	return []byte(strings.Trim(string(b), " \t\r\n\x00\x1c\x1d\x1e\x02\x03"))
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
