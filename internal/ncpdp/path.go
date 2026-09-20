// Addressing a field in an NCPDP transmission.
//
// # The notation
//
// A pharmacy integration person knows NCPDP fields by the standard's own identifiers: 401-D1 is the date of service,
// 302-C2 the cardholder identifier, 407-D7 the product code. The two-character part is what identifies the field, and it
// is what this notation uses.
//
//	D1            the date of service, wherever it appears
//	C2            the cardholder identifier
//	07-D7         field D7 in the claim segment
//	07(2)-D7      field D7 in the second claim segment
//	08(3)-E4      the third DUR conflict's reason code
//
// # Why a bare field identifier is allowed at all
//
// Because NCPDP identifiers are unique across the standard. D1 is the date of service and nothing else, in any segment, in
// any transaction type. So requiring a segment qualifier would be asking somebody to supply information the standard
// already determines, and getting it wrong would be the most likely outcome.
//
// The qualified form exists for the fields that genuinely appear more than once - a coordination of benefits transmission
// carries several other-payer amounts, and a compound carries an ingredient row per drug.
//
// # Why the two forms cannot be confused
//
// Segment identifiers are two digits: 01 is the patient segment, 07 the claim segment. Field identifiers begin with a
// letter: D1, C2, AK. So "07-D7" can only be a qualified path and "D7" can only be a bare field, with no ambiguity to
// resolve and no rule for somebody to remember.
package ncpdp

import (
	"fmt"
	"strconv"
	"strings"
)

// Path addresses a field in a transmission.
type Path struct {
	// Segment is the two-digit segment identifier, or empty to search every segment.
	Segment string

	// SegmentOccurs selects one occurrence of a repeating segment, counting from 1. Zero means every occurrence.
	SegmentOccurs int

	// Field is the two-character field identifier.
	Field string

	// raw is the path as written, for error messages and round-tripping.
	raw string
}

// String returns the path as it was written.
func (p Path) String() string { return p.raw }

// ParsePath reads a field path.
func ParsePath(raw string) (Path, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Path{}, fmt.Errorf("a path cannot be empty")
	}

	p := Path{raw: text}

	// The segment qualifier, if there is one.
	if dash := strings.Index(text, "-"); dash >= 0 {
		seg := text[:dash]
		field := text[dash+1:]

		if open := strings.Index(seg, "("); open >= 0 {
			if !strings.HasSuffix(seg, ")") {
				return Path{}, fmt.Errorf("%q is missing its closing parenthesis", text)
			}
			inner := seg[open+1 : len(seg)-1]
			n, err := strconv.Atoi(strings.TrimSpace(inner))
			if err != nil || n < 1 {
				return Path{}, fmt.Errorf("%q is not a segment occurrence; it counts from 1", inner)
			}
			p.SegmentOccurs = n
			seg = seg[:open]
		}

		if err := checkSegmentID(seg); err != nil {
			return Path{}, fmt.Errorf("%q: %w", text, err)
		}
		if err := checkFieldID(field); err != nil {
			return Path{}, fmt.Errorf("%q: %w", text, err)
		}
		p.Segment = seg
		p.Field = field
		return p, nil
	}

	if err := checkFieldID(text); err != nil {
		return Path{}, fmt.Errorf("%q: %w", text, err)
	}
	p.Field = text
	return p, nil
}

// checkSegmentID rejects anything that is not a two-digit segment identifier.
func checkSegmentID(s string) error {
	if len(s) != 2 || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return fmt.Errorf("%q is not a segment identifier; those are two digits, like 07 for the claim segment", s)
	}
	return nil
}

// checkFieldID rejects anything that is not a field identifier.
//
// Two characters beginning with a letter, which is what the standard assigns. Checked rather than assumed, because a
// mistyped field would otherwise become a path that addresses nothing and a filter that silently never matches.
func checkFieldID(s string) error {
	if len(s) != 2 {
		return fmt.Errorf("%q is not a field identifier; those are two characters, like D1 for the date of service", s)
	}
	upper := strings.ToUpper(s)
	if upper[0] < 'A' || upper[0] > 'Z' {
		return fmt.Errorf("%q is not a field identifier; those begin with a letter, like D1 or C2", s)
	}
	c := upper[1]
	if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
		return fmt.Errorf("%q is not a field identifier; the second character is a letter or a digit", s)
	}
	return nil
}

// headerValue returns a header field by its identifier, and whether the header carries that field at all.
//
// The header is fixed-width with no separators, so its fields are not in a Segment and cannot be found by searching one.
// Addressing them by the same identifiers as everything else means somebody filtering on the BIN or the transaction code -
// which is most filters - writes A1 or A3 and does not have to know the header is a different shape underneath.
//
// Resolved through the same table Build writes from, so a header field cannot be addressable and unbuildable.
func headerValue(h Header, id string) (string, bool) {
	want := strings.ToUpper(id)
	for _, f := range headerFields {
		if f.id == want {
			return f.get(h), true
		}
	}
	return "", false
}

// Read returns every value the path addresses, in wire order.
//
// Every value rather than the first, because the fields worth filtering on are often the ones that repeat: a DUR conflict
// code, an other-payer amount, a compound ingredient. A filter asking whether any of them is a given value should match if
// any of them is.
//
// The header is searched first when the path names no segment, because that is where the field lives if it is a header
// field, and a header field never appears in a segment as well.
func Read(m Message, p Path) []string {
	var out []string

	if p.Segment == "" {
		if v, ok := headerValue(m.Header, p.Field); ok {
			// Trimmed because the header is space padded to fixed widths, and a filter comparing against "B1" should not
			// have to know that the wire carries "B1      ".
			if t := strings.TrimSpace(v); t != "" {
				out = append(out, t)
			}
			// Returned here rather than falling through. A header field identifier is not reused in a segment, so
			// continuing would only find a coincidence.
			return out
		}
	}

	for _, tx := range m.Transactions {
		occurrence := 0
		for _, seg := range tx.Segments {
			if p.Segment != "" {
				if seg.ID != p.Segment {
					continue
				}
				occurrence++
				if p.SegmentOccurs > 0 && occurrence != p.SegmentOccurs {
					continue
				}
			}
			out = append(out, seg.All(p.Field)...)
		}
	}

	return out
}
