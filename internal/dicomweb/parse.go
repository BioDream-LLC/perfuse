// Package dicomweb speaks DICOMweb (DICOM PS3.18): QIDO-RS to search, WADO-RS and WADO-URI to retrieve, STOW-RS to
// store, over HTTP.
//
// It is how viewers, AI services and cloud archives reach imaging now: OHIF, Google Cloud Healthcare, Azure Health Data
// Services, AWS HealthImaging, Orthanc's and dcm4chee's DICOMweb interfaces. An integration engine that only speaks the
// network protocol from 1993 (DIMSE: C-STORE, C-FIND) can feed a PACS but not a browser.
//
// The package has four parts: a data set reader that walks the whole object, sequences included, and knows where the
// pixel data is without loading it; the DICOM JSON model (PS3.18 F.2); multipart/related, which is how DICOMweb moves
// several objects in one request; and an archive with an index, which is what QIDO and WADO answer from.
package dicomweb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Transfer syntaxes this reader distinguishes. Every other syntax (all the compressed ones) has an explicit VR little
// endian data set; only its pixel data differs, and that is never decoded here.
const (
	implicitVRLittleEndian = "1.2.840.10008.1.2"
	explicitVRLittleEndian = "1.2.840.10008.1.2.1"
	explicitVRBigEndian    = "1.2.840.10008.1.2.2"
	deflatedExplicitVRLE   = "1.2.840.10008.1.2.1.99"
)

// Attr is one attribute of a data set.
type Attr struct {
	Tag uint32
	VR  string

	// Value is the raw value bytes, for every VR except SQ and bulk data.
	Value []byte

	// Items are a sequence's items, each a data set of its own.
	Items [][]Attr

	// Bulk is set for a value left in the file: pixel data, and any binary value too large to hold. Offset and Length
	// locate it in the Part 10 bytes; Fragments are the encapsulated fragments, when it is encapsulated.
	Bulk      bool
	Offset    int
	Length    int
	Fragments []Fragment
}

// Fragment is one fragment of encapsulated pixel data, by position in the file.
type Fragment struct{ Offset, Length int }

// Object is a parsed Part 10 object.
type Object struct {
	TransferSyntax string
	// Meta is group 0002; Attrs the data set.
	Meta  []Attr
	Attrs []Attr
	// Raw is the whole file, which bulk data refers into.
	Raw []byte
}

// maxInline is the largest binary value kept in memory; larger ones are referred to by position.
const maxInline = 64 << 10

var (
	errNotPart10 = errors.New("dicomweb: not a DICOM Part 10 object (no DICM prefix and no file meta group)")
	errTruncated = errors.New("dicomweb: the object ends in the middle of an attribute")
)

// Parse reads a Part 10 object: preamble, meta group, then the data set in its transfer syntax.
//
// Sequences are walked, with defined and undefined lengths, to any depth. A depth limit and the input length bound the
// work, so a crafted object cannot make this loop or recurse without end.
func Parse(raw []byte) (*Object, error) {
	start := 0
	if len(raw) >= 132 && string(raw[128:132]) == "DICM" {
		start = 132
	} else if len(raw) >= 4 && string(raw[0:4]) == "DICM" {
		start = 4
	}
	if len(raw) < start+12 || binary.LittleEndian.Uint16(raw[start:]) != 0x0002 {
		return nil, errNotPart10
	}

	// The meta group is always explicit VR little endian.
	r := &reader{raw: raw, explicit: true, order: binary.LittleEndian}
	meta, next, err := r.attrs(start, len(raw), 0, func(tag uint32) bool { return tag>>16 != 0x0002 })
	if err != nil {
		return nil, err
	}
	obj := &Object{Meta: meta, Raw: raw}
	for _, a := range meta {
		if a.Tag == 0x00020010 {
			obj.TransferSyntax = trimUID(a.Value)
		}
	}
	if obj.TransferSyntax == "" {
		return nil, errors.New("dicomweb: the file meta group names no transfer syntax")
	}

	switch obj.TransferSyntax {
	case implicitVRLittleEndian:
		r.explicit = false
	case explicitVRBigEndian:
		r.order = binary.BigEndian
	case deflatedExplicitVRLE:
		return nil, fmt.Errorf("dicomweb: %s is deflate-compressed as a whole, which this does not read", obj.TransferSyntax)
	}
	obj.Attrs, _, err = r.attrs(next, len(raw), 0, nil)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

type reader struct {
	raw      []byte
	explicit bool
	order    binary.ByteOrder
}

const maxDepth = 16

// attrs reads attributes from off up to end, or until stop says a tag belongs to whoever called.
func (r *reader) attrs(off, end, depth int, stop func(uint32) bool) ([]Attr, int, error) {
	if depth > maxDepth {
		return nil, 0, errors.New("dicomweb: sequences nested more deeply than any real object")
	}
	var out []Attr
	for off < end {
		if end-off < 8 {
			// Trailing padding to an even length is common and harmless.
			break
		}
		group := r.order.Uint16(r.raw[off:])
		elem := r.order.Uint16(r.raw[off+2:])
		tag := uint32(group)<<16 | uint32(elem)
		if tag == 0xFFFEE00D { // item delimitation: the caller's item ends here
			return out, off + 8, nil
		}
		if stop != nil && stop(tag) {
			return out, off, nil
		}
		a, next, err := r.attr(off, end, depth)
		if err != nil {
			return out, 0, err
		}
		out = append(out, a)
		off = next
	}
	return out, off, nil
}

// attr reads one attribute at off.
func (r *reader) attr(off, end, depth int) (Attr, int, error) {
	tag := uint32(r.order.Uint16(r.raw[off:]))<<16 | uint32(r.order.Uint16(r.raw[off+2:]))
	a := Attr{Tag: tag}
	var length uint32
	p := off + 4
	if r.explicit {
		if end-p < 4 {
			return a, 0, errTruncated
		}
		a.VR = string(r.raw[p : p+2])
		if longLength(a.VR) {
			if end-p < 8 {
				return a, 0, errTruncated
			}
			length = r.order.Uint32(r.raw[p+4:])
			p += 8
		} else {
			length = uint32(r.order.Uint16(r.raw[p+2:]))
			p += 4
		}
	} else {
		if end-p < 4 {
			return a, 0, errTruncated
		}
		length = r.order.Uint32(r.raw[p:])
		p += 4
		a.VR = vrOf(tag)
	}

	undefined := length == 0xFFFFFFFF
	if a.VR == "SQ" || (undefined && a.VR == "UN") {
		// An undefined-length UN is a sequence in implicit VR (PS3.5 6.2.2), and is read as one.
		a.VR = "SQ"
		items, next, err := r.sequence(p, end, length, depth)
		a.Items = items
		return a, next, err
	}

	if undefined {
		if tag != 0x7FE00010 {
			return a, 0, fmt.Errorf("dicomweb: %s has an undefined length, which only sequences and pixel data may", tagString(tag))
		}
		frags, next, err := r.fragments(p, end)
		a.Bulk, a.Offset, a.Fragments = true, p, frags
		a.Length = next - p
		return a, next, err
	}

	stop := p + int(length)
	if stop > end || stop < p {
		return a, 0, errTruncated
	}
	if tag == 0x7FE00010 || (isBinary(a.VR) && length > maxInline) {
		a.Bulk, a.Offset, a.Length = true, p, int(length)
		return a, stop, nil
	}
	a.Value = r.raw[p:stop]
	return a, stop, nil
}

// sequence reads a sequence's items.
func (r *reader) sequence(off, end int, length uint32, depth int) ([][]Attr, int, error) {
	limit := end
	if length != 0xFFFFFFFF {
		limit = off + int(length)
		if limit > end || limit < off {
			return nil, 0, errTruncated
		}
	}
	var items [][]Attr
	for off < limit {
		if limit-off < 8 {
			return items, 0, errTruncated
		}
		tag := uint32(r.order.Uint16(r.raw[off:]))<<16 | uint32(r.order.Uint16(r.raw[off+2:]))
		itemLen := r.order.Uint32(r.raw[off+4:])
		off += 8
		switch tag {
		case 0xFFFEE0DD: // sequence delimitation
			return items, off, nil
		case 0xFFFEE000:
		default:
			return items, 0, fmt.Errorf("dicomweb: expected an item in a sequence and found %s", tagString(tag))
		}
		itemEnd := limit
		if itemLen != 0xFFFFFFFF {
			itemEnd = off + int(itemLen)
			if itemEnd > limit || itemEnd < off {
				return items, 0, errTruncated
			}
		}
		attrs, next, err := r.attrs(off, itemEnd, depth+1, nil)
		if err != nil {
			return items, 0, err
		}
		if itemLen != 0xFFFFFFFF {
			next = itemEnd
		}
		items = append(items, attrs)
		off = next
	}
	return items, off, nil
}

// fragments walks encapsulated pixel data: an offset table item, then one item per fragment, then a delimiter.
func (r *reader) fragments(off, end int) ([]Fragment, int, error) {
	var out []Fragment
	first := true
	for off+8 <= end {
		tag := uint32(r.order.Uint16(r.raw[off:]))<<16 | uint32(r.order.Uint16(r.raw[off+2:]))
		n := int(r.order.Uint32(r.raw[off+4:]))
		off += 8
		if tag == 0xFFFEE0DD {
			return out, off, nil
		}
		if tag != 0xFFFEE000 || off+n > end || n < 0 {
			return out, 0, errors.New("dicomweb: encapsulated pixel data is malformed")
		}
		if !first {
			out = append(out, Fragment{Offset: off, Length: n})
		}
		first = false
		off += n
	}
	return out, 0, errTruncated
}

func longLength(vr string) bool {
	switch vr {
	case "OB", "OD", "OF", "OL", "OV", "OW", "SQ", "SV", "UC", "UN", "UR", "UT", "UV":
		return true
	}
	return false
}

func isBinary(vr string) bool {
	switch vr {
	case "OB", "OD", "OF", "OL", "OV", "OW", "UN":
		return true
	}
	return false
}

func trimUID(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == 0 || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}

func tagString(tag uint32) string { return fmt.Sprintf("(%04X,%04X)", tag>>16, tag&0xFFFF) }

// Find returns the first attribute with a tag, at the top level.
func Find(attrs []Attr, tag uint32) (Attr, bool) {
	for _, a := range attrs {
		if a.Tag == tag {
			return a, true
		}
	}
	return Attr{}, false
}

// Text returns a string attribute's value, trimmed of its padding.
func (o *Object) Text(tag uint32) string {
	a, ok := Find(o.Attrs, tag)
	if !ok {
		return ""
	}
	return trimString(a.Value)
}
