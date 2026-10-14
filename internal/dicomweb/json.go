package dicomweb

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSON is one DICOM JSON data set (PS3.18 F.2): keys are eight hexadecimal digits.
type JSON map[string]any

// BulkURI makes the BulkDataURI for a bulk value, given its tag. Nil writes no URI and leaves the value out.
type BulkURI func(tag uint32) string

// ToJSON converts attributes to DICOM JSON.
//
// keep, when set, chooses which top-level tags are written (QIDO's default attribute sets).
func ToJSON(attrs []Attr, order binary.ByteOrder, bulk BulkURI, keep func(uint32) bool) JSON {
	out := JSON{}
	for _, a := range attrs {
		if keep != nil && !keep(a.Tag) {
			continue
		}
		if a.Tag>>16 == 0x0002 || a.Tag&0xFFFF == 0 {
			// Meta information and group lengths are encoding detail, not content, and DICOM JSON leaves them out.
			continue
		}
		key := fmt.Sprintf("%08X", a.Tag)
		out[key] = attrJSON(a, order, bulk)
	}
	return out
}

func attrJSON(a Attr, order binary.ByteOrder, bulk BulkURI) map[string]any {
	v := map[string]any{"vr": a.VR}
	switch {
	case a.VR == "SQ":
		if len(a.Items) > 0 {
			items := make([]any, 0, len(a.Items))
			for _, it := range a.Items {
				// Bulk data inside a sequence is rare (an icon image) and has no addressable URI of its own here, so
				// it is left out rather than given one that would not resolve.
				items = append(items, ToJSON(it, order, nil, nil))
			}
			v["Value"] = items
		}
	case a.Bulk:
		if bulk != nil {
			v["BulkDataURI"] = bulk(a.Tag)
		}
	case isBinary(a.VR):
		if len(a.Value) > 0 {
			v["InlineBinary"] = base64.StdEncoding.EncodeToString(a.Value)
		}
	default:
		if vals := values(a, order); len(vals) > 0 {
			v["Value"] = vals
		}
	}
	return v
}

// values decodes a value into the JSON types PS3.18 F.2.3 assigns each VR.
func values(a Attr, order binary.ByteOrder) []any {
	b := a.Value
	if len(b) == 0 {
		return nil
	}
	fixed := func(size int, read func([]byte) any) []any {
		var out []any
		for i := 0; i+size <= len(b); i += size {
			out = append(out, read(b[i:i+size]))
		}
		return out
	}
	switch a.VR {
	case "US":
		return fixed(2, func(p []byte) any { return order.Uint16(p) })
	case "SS":
		return fixed(2, func(p []byte) any { return int16(order.Uint16(p)) })
	case "UL":
		return fixed(4, func(p []byte) any { return order.Uint32(p) })
	case "SL":
		return fixed(4, func(p []byte) any { return int32(order.Uint32(p)) })
	case "UV":
		return fixed(8, func(p []byte) any { return order.Uint64(p) })
	case "SV":
		return fixed(8, func(p []byte) any { return int64(order.Uint64(p)) })
	case "FL":
		return fixed(4, func(p []byte) any { return finite(float64(math.Float32frombits(order.Uint32(p)))) })
	case "FD":
		return fixed(8, func(p []byte) any { return finite(math.Float64frombits(order.Uint64(p))) })
	case "AT":
		return fixed(4, func(p []byte) any {
			return fmt.Sprintf("%04X%04X", order.Uint16(p), order.Uint16(p[2:]))
		})
	}

	text := decodeText(b)
	var parts []string
	switch a.VR {
	case "LT", "ST", "UT", "UR":
		// Single-valued: a backslash in these is a character, not a separator.
		parts = []string{strings.TrimRight(text, " \x00")}
	default:
		parts = strings.Split(strings.TrimRight(text, " \x00"), `\`)
	}

	out := make([]any, 0, len(parts))
	for _, p := range parts {
		switch a.VR {
		case "PN":
			out = append(out, personName(p))
		case "IS":
			p = strings.TrimSpace(p)
			if p == "" {
				out = append(out, nil)
			} else if n, err := strconv.ParseInt(p, 10, 64); err == nil {
				out = append(out, n)
			} else {
				out = append(out, p)
			}
		case "DS":
			p = strings.TrimSpace(p)
			if p == "" {
				out = append(out, nil)
			} else if f, err := strconv.ParseFloat(p, 64); err == nil {
				out = append(out, finite(f))
			} else {
				out = append(out, p)
			}
		default:
			p = strings.TrimRight(p, " \x00")
			if a.VR != "LT" && a.VR != "ST" && a.VR != "UT" {
				p = strings.TrimLeft(p, " ")
			}
			if p == "" {
				out = append(out, nil)
			} else {
				out = append(out, p)
			}
		}
	}
	return out
}

func personName(p string) any {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	groups := strings.SplitN(p, "=", 3)
	out := map[string]any{}
	for i, key := range []string{"Alphabetic", "Ideographic", "Phonetic"} {
		if i < len(groups) && groups[i] != "" {
			out[key] = groups[i]
		}
	}
	return out
}

// finite keeps a JSON-unrepresentable float out of the output: NaN and infinity have no JSON form and would make the
// whole response unreadable.
func finite(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}

// decodeText turns a value into UTF-8.
//
// UTF-8 (ISO_IR 192) and ASCII pass through; anything else is read as ISO 8859-1 (ISO_IR 100, the commonest by far
// after ASCII). A wrong guess here shows an accented name with the wrong accent; reading Latin-1 bytes as UTF-8 would
// replace it with a question mark, or produce JSON a strict client rejects.
func decodeText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func trimString(b []byte) string { return strings.TrimSpace(strings.TrimRight(decodeText(b), "\x00")) }
