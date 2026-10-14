package dicomweb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrFramesUnavailable means frames were asked for that this object cannot give separately.
var ErrFramesUnavailable = errors.New("dicomweb: frames unavailable")

// FrameMediaType is the media type of one frame in the object's transfer syntax, for a WADO-RS frames response.
func FrameMediaType(transferSyntax string) string {
	switch transferSyntax {
	case "1.2.840.10008.1.2.4.50", "1.2.840.10008.1.2.4.51", "1.2.840.10008.1.2.4.57", "1.2.840.10008.1.2.4.70":
		return "image/jpeg"
	case "1.2.840.10008.1.2.4.80", "1.2.840.10008.1.2.4.81":
		return "image/jls"
	case "1.2.840.10008.1.2.4.90", "1.2.840.10008.1.2.4.91":
		return "image/jp2"
	case "1.2.840.10008.1.2.4.201", "1.2.840.10008.1.2.4.202", "1.2.840.10008.1.2.4.203":
		return "image/jphc"
	}
	return "application/octet-stream"
}

// Frames returns the requested frames (counting from 1) as stored, never transcoded.
//
// Native pixel data is cut by frame size. Encapsulated pixel data is one fragment per frame when the counts agree, all
// fragments for a single-frame object, or grouped by the basic offset table; otherwise frames cannot be told apart and
// the request is refused rather than answered with the wrong pixels.
func (o *Object) Frames(want []int) ([][]byte, error) {
	px, ok := Find(o.Attrs, 0x7FE00010)
	if !ok || !px.Bulk {
		return nil, fmt.Errorf("%w: this object has no pixel data", ErrFramesUnavailable)
	}
	number := 1
	if n, err := strconv.Atoi(o.Text(0x00280008)); err == nil && n > 0 {
		number = n
	}
	for _, f := range want {
		if f < 1 || f > number {
			return nil, fmt.Errorf("%w: frame %d does not exist; this object has %d", ErrFramesUnavailable, f, number)
		}
	}

	var frames [][]byte
	if px.Fragments == nil {
		rows, cols := o.uint16(0x00280010), o.uint16(0x00280011)
		bits, samples := o.uint16(0x00280100), o.uint16(0x00280002)
		if samples == 0 {
			samples = 1
		}
		if rows == 0 || cols == 0 || bits == 0 || bits%8 != 0 {
			// One-bit images pack frames on bit boundaries, and a frame need not start on a byte.
			return nil, fmt.Errorf("%w: frames of %d-bit pixel data are not separated here", ErrFramesUnavailable, bits)
		}
		size := int(rows) * int(cols) * int(samples) * int(bits/8)
		if size*number > px.Length {
			return nil, fmt.Errorf("%w: the pixel data is shorter than %d frames of %d bytes", ErrFramesUnavailable, number, size)
		}
		for _, f := range want {
			start := px.Offset + (f-1)*size
			frames = append(frames, o.Raw[start:start+size])
		}
		return frames, nil
	}

	groups, err := o.fragmentGroups(px, number)
	if err != nil {
		return nil, err
	}
	for _, f := range want {
		frames = append(frames, groups[f-1])
	}
	return frames, nil
}

func (o *Object) fragmentGroups(px Attr, number int) ([][]byte, error) {
	join := func(frs []Fragment) []byte {
		var b []byte
		for _, fr := range frs {
			b = append(b, o.Raw[fr.Offset:fr.Offset+fr.Length]...)
		}
		return b
	}
	switch {
	case len(px.Fragments) == number:
		out := make([][]byte, number)
		for i, fr := range px.Fragments {
			out[i] = o.Raw[fr.Offset : fr.Offset+fr.Length]
		}
		return out, nil
	case number == 1:
		return [][]byte{join(px.Fragments)}, nil
	}

	// The basic offset table: the first item's value, offsets of each frame's first fragment, measured from the first
	// byte of the first fragment's item tag.
	bot := o.Raw[px.Offset+8 : px.Fragments[0].Offset-8]
	if len(bot) != 4*number {
		return nil, fmt.Errorf("%w: %d frames in %d fragments with no basic offset table to say which is which",
			ErrFramesUnavailable, number, len(px.Fragments))
	}
	base := px.Fragments[0].Offset - 8
	starts := make([]int, number)
	for i := range starts {
		starts[i] = base + int(binary.LittleEndian.Uint32(bot[4*i:]))
	}
	out := make([][]byte, number)
	for i := range starts {
		var frs []Fragment
		for _, fr := range px.Fragments {
			item := fr.Offset - 8
			if item >= starts[i] && (i == number-1 || item < starts[i+1]) {
				frs = append(frs, fr)
			}
		}
		if len(frs) == 0 {
			return nil, fmt.Errorf("%w: the basic offset table points at no fragment for frame %d", ErrFramesUnavailable, i+1)
		}
		out[i] = join(frs)
	}
	return out, nil
}

func (o *Object) uint16(tag uint32) uint16 {
	a, ok := Find(o.Attrs, tag)
	if !ok || len(a.Value) < 2 {
		return 0
	}
	if o.TransferSyntax == explicitVRBigEndian {
		return binary.BigEndian.Uint16(a.Value)
	}
	return binary.LittleEndian.Uint16(a.Value)
}

// Order is the byte order of the data set.
func (o *Object) Order() binary.ByteOrder {
	if o.TransferSyntax == explicitVRBigEndian {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// ParseFrameList reads "1,3,5" from a frames URL.
func ParseFrameList(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("%q is not a list of frame numbers counting from 1", s)
		}
		out = append(out, n)
	}
	if len(out) == 0 || len(out) > 4096 {
		return nil, fmt.Errorf("%q names no frames, or too many", s)
	}
	return out, nil
}
