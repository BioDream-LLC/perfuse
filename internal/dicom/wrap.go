package dicom

import (
	"bytes"
	"encoding/binary"
)

// Wrap turns a data set received over an association into a self-describing file representation.
//
// An object on the wire has no preamble and no meta group: its transfer syntax comes from the association negotiation
// rather than from the bytes. That is fine while the association is open and useless afterwards, because nothing
// downstream knows how to read it.
//
// So the negotiated syntax is written into a meta group as the object enters the engine. After that every part of the
// system can read it without being told anything: the message store holds something self-describing, a file destination
// writes a valid .dcm, and the sender can read it back and strip the meta group again on the way out.
//
// The alternative - carrying the transfer syntax alongside the bytes through every layer - was tried first and failed
// twice in the same afternoon, once on receipt and once on send, each time reporting "the data set ends part way through
// an element" from a parser that had guessed.
func Wrap(dataSet []byte, transferSyntax, sopClass, sopInstance string) []byte {
	if transferSyntax == "" {
		// The standard's mandatory default, and what a sender that negotiated nothing will have used.
		transferSyntax = ImplicitVRLittleEndian
	}

	meta := buildMetaGroup(transferSyntax, sopClass, sopInstance)

	out := make([]byte, 0, preambleLength+len(magic)+len(meta)+len(dataSet))
	out = append(out, make([]byte, preambleLength)...)
	out = append(out, []byte(magic)...)
	out = append(out, meta...)
	out = append(out, dataSet...)

	return out
}

// buildMetaGroup writes a file meta information group.
//
// Always explicit VR little endian regardless of what it declares for the data set. That is not a choice - the standard
// requires it - and it is the detail that makes a naive parser read a whole file wrongly, since it would otherwise use the
// syntax it found here to read the group it found it in.
func buildMetaGroup(transferSyntax, sopClass, sopInstance string) []byte {
	var inner bytes.Buffer

	// File meta information version, which is the fixed two bytes 0x0001 and an OB.
	writeExplicitElement(&inner, Tag{0x0002, 0x0001}, "OB", []byte{0x00, 0x01})

	if sopClass != "" {
		writeExplicitElement(&inner, TagMediaStorageSOPClassUID, "UI", []byte(sopClass))
	}
	if sopInstance != "" {
		writeExplicitElement(&inner, TagMediaStorageSOPInstanceUID, "UI", []byte(sopInstance))
	}
	writeExplicitElement(&inner, TagTransferSyntaxUID, "UI", []byte(transferSyntax))
	writeExplicitElement(&inner, Tag{0x0002, 0x0012}, "UI", []byte(ImplementationUID))
	writeExplicitElement(&inner, Tag{0x0002, 0x0013}, "SH", []byte(ImplementationName))

	// The group length counts everything after itself. Computed from the encoded bytes rather than tracked, because a
	// wrong value is the classic failure here: some readers ignore it and work, others read exactly that many bytes and
	// truncate the group.
	var out bytes.Buffer
	length := make([]byte, 4)
	binary.LittleEndian.PutUint32(length, uint32(inner.Len()))
	writeExplicitElement(&out, TagFileMetaInformationGroupLength, "UL", length)
	out.Write(inner.Bytes())

	return out.Bytes()
}

func writeExplicitElement(w *bytes.Buffer, tag Tag, vr string, value []byte) {
	padded := value
	if len(padded)%2 == 1 {
		// UIDs pad with a null, everything else with a space. Getting this wrong leaves an invisible byte on a UID, and a
		// reader comparing it against a constant sees a mismatched study.
		pad := byte(' ')
		if vr == "UI" {
			pad = 0x00
		}
		padded = append(append([]byte{}, padded...), pad)
	}

	header := make([]byte, 4)
	binary.LittleEndian.PutUint16(header[0:2], tag.Group)
	binary.LittleEndian.PutUint16(header[2:4], tag.Element)
	w.Write(header)
	w.WriteString(vr)

	switch vr {
	case "OB", "OW", "OF", "SQ", "UT", "UN":
		// The six VRs with a four-byte length after two reserved bytes.
		w.Write([]byte{0x00, 0x00})
		length := make([]byte, 4)
		binary.LittleEndian.PutUint32(length, uint32(len(padded)))
		w.Write(length)
	default:
		length := make([]byte, 2)
		binary.LittleEndian.PutUint16(length, uint16(len(padded)))
		w.Write(length)
	}

	w.Write(padded)
}
