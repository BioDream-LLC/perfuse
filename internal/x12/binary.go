package x12

import (
	"bytes"
	"fmt"
	"strconv"
)

// Segments that carry a declared number of raw bytes, which the segment splitter must not scan for delimiters.
//
// # Why this is necessary rather than a refinement
//
// Every other X12 segment ends at the segment terminator, so splitting an interchange is a search for one byte. A binary segment
// breaks that: its payload is arbitrary bytes, and arbitrary bytes include the terminator, the element separator and the component
// separator. A splitter that scans for the terminator therefore cuts an attachment into pieces and invents segments out of the
// fragments - a PDF containing a tilde produced segments named after whatever followed it, and every segment after the attachment was
// misaligned. Nothing reported an error, because each fragment is a structurally valid segment.
//
// The standard's answer is a byte count in the segment itself, which is what makes the payload readable at all: the splitter reads the
// count, takes exactly that many bytes, and resumes looking for delimiters afterwards.
//
// # Why a table rather than a special case for BIN
//
// Because there is more than one such segment and the count is not in the same place in each, and getting the position wrong does not
// produce an error - it produces a plausible-looking wrong length, which truncates an attachment or swallows the rest of the
// interchange. Naming the position next to the segment makes the two facts one fact.
//
// BIN is the one implemented. BIN01 is the length and BIN02 is the data, which has been stable across versions and is what the 275
// attachment transaction and the 841 specification use. BDS is deliberately absent: it also carries binary data, its layout puts the
// count elsewhere, and this codebase has a rule against writing down a field position nobody has verified. A BDS segment is reported
// rather than guessed at - see splitError.
var binarySegments = map[string]struct {
	// lengthElement is the one-based element holding the byte count.
	lengthElement int

	// dataElement is the one-based element holding the payload, which must be the last element of the segment.
	dataElement int
}{
	"BIN": {lengthElement: 1, dataElement: 2},
}

// unverifiedBinarySegments are segments known to carry raw bytes whose layout this package has not confirmed.
//
// Listed so they can be refused with an explanation rather than silently mis-split. A mis-split binary segment corrupts everything
// after it in the interchange, so failing to parse is strictly better than appearing to succeed.
var unverifiedBinarySegments = map[string]string{
	"BDS": "BDS carries raw binary data and this package has not verified which element holds its byte count. " +
		"Parsing it by scanning for delimiters would corrupt the payload and every segment after it. " +
		"Use BIN, or open an issue with a sample so the layout can be confirmed rather than guessed.",
}

// splitBinaryAware splits an interchange into segments, honouring the byte counts on binary segments.
//
// Returns an error only for a binary segment this package cannot safely split. Everything else is left to the validation pass, which
// reports structural problems with context rather than refusing the whole interchange - a partner sending one malformed segment should
// still be told which one.
func (m *Message) splitBinaryAware() error {
	term := m.delim.Segment
	elem := m.delim.Element

	pos := 0
	for pos < len(m.raw) {
		// Skip the whitespace many partners put between segments for readability, which is legal because the terminator ends a
		// segment rather than the newline.
		for pos < len(m.raw) && isX12Space(m.raw[pos]) {
			pos++
		}
		if pos >= len(m.raw) {
			break
		}

		id := peekSegmentID(m.raw[pos:], elem, term)

		if reason, unverified := unverifiedBinarySegments[id]; unverified {
			return fmt.Errorf("%w: %s", ErrUnsupportedBinarySegment, reason)
		}

		shape, isBinary := binarySegments[id]
		if !isBinary {
			end := bytes.IndexByte(m.raw[pos:], term)
			if end < 0 {
				// No terminator left. The remainder is one final segment, which is what a partner who omitted the last
				// terminator produces - and the validation pass reports it as such rather than this dropping it.
				m.appendSegment(m.raw[pos:])

				break
			}
			m.appendSegment(m.raw[pos : pos+end])
			pos += end + 1

			continue
		}

		consumed, err := m.appendBinarySegment(m.raw[pos:], shape.lengthElement, shape.dataElement)
		if err != nil {
			return err
		}
		pos += consumed
	}

	return nil
}

// appendBinarySegment reads one length-prefixed segment and returns how many bytes it consumed, including the terminator.
func (m *Message) appendBinarySegment(raw []byte, lengthElement, dataElement int) (int, error) {
	elem := m.delim.Element
	term := m.delim.Segment

	// Walk to the start of the payload by counting element separators. The payload is the last element, so everything before it is
	// ordinary delimited data.
	offset := 0
	seen := 0
	for offset < len(raw) && seen < dataElement {
		if raw[offset] == elem {
			seen++
		}
		if raw[offset] == term && seen < dataElement {
			// The segment ended before the payload element, which means the declared shape and this segment disagree. Reported
			// rather than guessed at, because the alternative is reading the next segment as an attachment.
			return 0, fmt.Errorf("%w: a binary segment ended after %d element(s), before the data element",
				ErrMalformedBinarySegment, seen)
		}
		offset++
	}
	if seen < dataElement {
		return 0, fmt.Errorf("%w: a binary segment has no data element", ErrMalformedBinarySegment)
	}

	header := raw[:offset]
	lengthText := elementAt(header, elem, lengthElement)

	declared, err := strconv.Atoi(string(bytes.TrimSpace(lengthText)))
	if err != nil {
		return 0, fmt.Errorf("%w: the declared length %q is not a number", ErrMalformedBinarySegment, lengthText)
	}
	if declared < 0 {
		return 0, fmt.Errorf("%w: the declared length %d is negative", ErrMalformedBinarySegment, declared)
	}
	if offset+declared > len(raw) {
		// Reported with both numbers, because the useful question is whether the sender lied about the length or the interchange
		// was truncated in transit, and the difference between declared and available is what answers it.
		return 0, fmt.Errorf("%w: a binary segment declares %d bytes and only %d remain",
			ErrMalformedBinarySegment, declared, len(raw)-offset)
	}

	end := offset + declared

	// The segment is stored with its payload intact, so a caller reading element two gets the bytes that were sent.
	m.appendSegment(raw[:end])

	// A terminator should follow immediately. Anything else means the declared length was wrong, and continuing would read part of
	// the payload as segments - which is the corruption this whole file exists to prevent.
	rest := end
	for rest < len(raw) && isX12Space(raw[rest]) {
		rest++
	}
	if rest >= len(raw) {
		return len(raw), nil
	}
	if raw[rest] != term {
		return 0, fmt.Errorf("%w: a binary segment declaring %d bytes is not followed by a segment terminator, "+
			"so the declared length is wrong", ErrMalformedBinarySegment, declared)
	}

	return rest + 1, nil
}

// appendSegment records one segment, ignoring an empty one.
func (m *Message) appendSegment(raw []byte) {
	trimmed := bytes.Trim(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return
	}

	m.segs = append(m.segs, newSegment(trimmed, m.delim))
}

// peekSegmentID reads a segment's identifier without consuming the segment.
//
// Needed because whether a segment may be scanned for delimiters depends on which segment it is, and the identifier is the first
// element - so it can always be read by ordinary means even when the rest cannot.
func peekSegmentID(raw []byte, elem, term byte) string {
	for i := 0; i < len(raw); i++ {
		if raw[i] == elem || raw[i] == term {
			return string(bytes.Trim(raw[:i], " \t\r\n"))
		}
	}

	return string(bytes.Trim(raw, " \t\r\n"))
}

// elementAt returns the nth one-based element of a segment header.
func elementAt(header []byte, elem byte, n int) []byte {
	parts := bytes.Split(header, []byte{elem})
	if n < 0 || n >= len(parts) {
		return nil
	}

	return parts[n]
}

func isX12Space(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}
