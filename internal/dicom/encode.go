package dicom

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Encode writes elements as a data set in the given transfer syntax.
//
// Needed because query and retrieve send data sets rather than only receiving them: a C-FIND query is a data set of the
// keys being matched, and the empty ones are the fields being asked for. Storage never needed this - an object arrives
// already encoded and is relayed byte for byte.
//
// Elements are sorted by tag. That is not tidiness: the standard requires ascending tag order within a data set, and
// receivers do rely on it, particularly ones that stream rather than buffer. A caller-built slice is unordered enough that
// leaving it to the caller would produce a query that works against a forgiving archive and fails against a strict one.
func Encode(elements []Element, transferSyntax string) ([]byte, error) {
	sorted := make([]Element, len(elements))
	copy(sorted, elements)
	sortElements(sorted)

	if transferSyntax == "" {
		transferSyntax = ImplicitVRLittleEndian
	}

	// The reader's own function, deliberately. Encoding against one table and decoding against another is how a system
	// ends up able to read only what it wrote.
	explicit, byteOrder, err := encodingFor(transferSyntax)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for _, e := range sorted {
		if err := encodeElement(&out, e, explicit, byteOrder); err != nil {
			return nil, err
		}
	}

	return out.Bytes(), nil
}

func encodeElement(w *bytes.Buffer, e Element, explicit bool, byteOrder binary.ByteOrder) error {
	vr := e.VR
	if vr == "" {
		vr = inferVR(e.Tag)
	}

	value := padValue(e.Value, vr)

	tag := make([]byte, 4)
	byteOrder.PutUint16(tag[0:2], e.Tag.Group)
	byteOrder.PutUint16(tag[2:4], e.Tag.Element)
	w.Write(tag)

	if !explicit {
		// Implicit VR carries no VR at all: the receiver infers it from the tag, exactly as our parser does. So an
		// element whose VR we guessed wrong is still encoded correctly here, because the guess is not transmitted.
		length := make([]byte, 4)
		byteOrder.PutUint32(length, uint32(len(value)))
		w.Write(length)
		w.Write(value)
		return nil
	}

	w.WriteString(vr)

	if longFormVR(vr) {
		w.Write([]byte{0x00, 0x00})
		length := make([]byte, 4)
		byteOrder.PutUint32(length, uint32(len(value)))
		w.Write(length)
	} else {
		if len(value) > 0xFFFF {
			return fmt.Errorf("dicom: element %s has %d bytes, which will not fit the two-byte length that VR %s uses",
				e.Tag, len(value), vr)
		}
		length := make([]byte, 2)
		byteOrder.PutUint16(length, uint16(len(value)))
		w.Write(length)
	}

	w.Write(value)
	return nil
}

// longFormVR reports the six value representations that carry a four-byte length after two reserved bytes.
func longFormVR(vr string) bool {
	switch vr {
	case "OB", "OW", "OF", "SQ", "UT", "UN":
		return true
	}
	return false
}

// padValue makes a value an even number of bytes.
//
// Every element in a data set has an even length; that is a hard requirement, not a convention. UIDs pad with a null and
// everything else with a space. Getting that backwards leaves an invisible trailing byte on a UID, and a receiver
// comparing it against a constant sees a study it does not recognise.
func padValue(value []byte, vr string) []byte {
	if len(value)%2 == 0 {
		return value
	}

	pad := byte(' ')
	if vr == "UI" {
		pad = 0x00
	}

	out := make([]byte, 0, len(value)+1)
	out = append(out, value...)
	return append(out, pad)
}

// sortElements orders elements by tag ascending.
//
// A plain insertion sort. The lists here are query keys - a dozen elements at most - and this keeps the ordering rule
// visible next to the reason for it.
func sortElements(elements []Element) {
	for i := 1; i < len(elements); i++ {
		for j := i; j > 0 && tagLess(elements[j].Tag, elements[j-1].Tag); j-- {
			elements[j], elements[j-1] = elements[j-1], elements[j]
		}
	}
}

func tagLess(a, b Tag) bool {
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	return a.Element < b.Element
}
