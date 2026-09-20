package dicom

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// maxHeldValue bounds a value the parser will read into memory.
//
// Above this the element is reported with its tag and length but its bytes are skipped. The point of this package is
// metadata, and a chest CT is hundreds of megabytes of pixels behind two kilobytes of the information anybody routes
// on - reading the pixels to reach the metadata would make the common case as slow as the worst one.
const maxHeldValue = 1 << 20

// preamble is the 128 byte preamble followed by the DICM magic.
const (
	preambleLength = 128
	magic          = "DICM"
)

// DataSet is a parsed DICOM object.
type DataSet struct {
	// TransferSyntax is the UID the data set is encoded with, taken from the meta group rather than assumed.
	TransferSyntax string

	// Elements are in the order they appeared. A map as well would be faster to look up, but order is evidence: a
	// data set is diffed against another during an investigation, and reordering it would make every comparison
	// noisy.
	Elements []Element

	// HadPreamble reports whether the 128 byte preamble and DICM magic were present.
	//
	// Absent is normal rather than wrong: data sets sent over the network arrive without one, because the preamble
	// exists so that a file can be recognised on disk. Reporting it lets a caller tell a file from a stream.
	HadPreamble bool
}

// Get returns the first element with a tag.
func (d *DataSet) Get(tag Tag) (Element, bool) {
	for _, e := range d.Elements {
		if e.Tag == tag {
			return e, true
		}
	}
	return Element{}, false
}

// Text returns an element's value as text, or "" when absent.
//
// Absence and emptiness both yield "", deliberately. DICOM distinguishes them - a zero-length element means "known to
// be empty" while an absent one means "not sent" - but almost every caller wants the same thing for both, and the ones
// that do not can use Get.
func (d *DataSet) Text(tag Tag) string {
	e, ok := d.Get(tag)
	if !ok {
		return ""
	}
	return e.String()
}

// Parse reads a DICOM data set.
//
// Handles a file with a preamble and meta group, and a bare data set as arrives over the network. The transfer syntax
// is read from the meta group when present; otherwise implicit VR little endian is assumed, which is what the standard
// requires as the default and what a sender that negotiated nothing will send.
func Parse(data []byte) (*DataSet, error) {
	out := &DataSet{TransferSyntax: ImplicitVRLittleEndian}

	rest := data

	// A file begins with 128 bytes of preamble then DICM. Over the network there is neither, so both are optional and
	// their absence is not an error.
	if len(rest) >= preambleLength+len(magic) &&
		string(rest[preambleLength:preambleLength+len(magic)]) == magic {
		out.HadPreamble = true
		rest = rest[preambleLength+len(magic):]
	}

	// The meta group is always explicit VR little endian regardless of what the rest of the data set uses. This is
	// the detail that makes a naive parser read the whole file wrongly: it reads the meta group with the transfer
	// syntax it finds inside the meta group, which is circular.
	if len(rest) >= 8 && binary.LittleEndian.Uint16(rest) == 0x0002 {
		metaEnd, err := readMetaGroup(out, rest)
		if err != nil {
			return nil, err
		}
		rest = rest[metaEnd:]
	} else if !out.HadPreamble && !looksLikeDataSet(rest) {
		return nil, ErrNotDICOM
	}

	explicit, order, err := encodingFor(out.TransferSyntax)
	if err != nil {
		return nil, err
	}

	elements, err := readElements(rest, explicit, order)
	if err != nil {
		return nil, err
	}
	out.Elements = append(out.Elements, elements...)

	return out, nil
}

// looksLikeDataSet reports whether the bytes plausibly start with a data element.
//
// Needed because a bare network data set has no magic to check, so the only available evidence is whether the first
// element is sane. Without this check, any file at all would parse into a list of nonsense elements and the caller
// would get garbage rather than an error.
func looksLikeDataSet(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	group := binary.LittleEndian.Uint16(data)
	// Group 0 is command elements, group 2 is meta. A data set element should be in a plausible group, and groups
	// above 0x7FE0 are essentially unheard of outside pixel data and its siblings.
	if group == 0 || group > 0xFFFE {
		return false
	}
	// Explicit VR: bytes 4 and 5 are two upper-case letters.
	if isVR(data[4:6]) {
		return true
	}
	// Implicit VR: bytes 4 to 8 are a length, and a length larger than the input cannot be right.
	length := binary.LittleEndian.Uint32(data[4:8])
	return length == 0xFFFFFFFF || int(length) <= len(data)-8
}

func isVR(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	return b[0] >= 'A' && b[0] <= 'Z' && b[1] >= 'A' && b[1] <= 'Z'
}

// readMetaGroup reads the file meta information and returns how many bytes it occupied.
//
// Walks element by element tracking the offset, rather than parsing everything and measuring afterwards. An earlier
// version did the latter and got the boundary wrong, so every implicit VR data set after a meta group was read from a
// few bytes off and reported as truncated. One pass that both reads and counts cannot disagree with itself.
func readMetaGroup(out *DataSet, data []byte) (int, error) {
	offset := 0

	// The group length element declares how many bytes follow it. That is the authoritative end of the meta group -
	// scanning for the first non-group-2 element would break on a data set whose first real element happens to be in
	// group 2, and trusting the walk alone would break on padding.
	var declaredLength uint32
	var afterGroupLength int

	for offset+8 <= len(data) {
		if binary.LittleEndian.Uint16(data[offset:]) != 0x0002 {
			break
		}

		// Always explicit VR little endian here, whatever the transfer syntax inside says. This is the detail that
		// makes a naive parser read a whole file wrongly: it would use the syntax it found in the group to read the
		// group it found it in.
		element, size, ok := readOneElement(data[offset:], true, binary.LittleEndian)
		if !ok {
			return 0, ErrTruncated
		}

		out.Elements = append(out.Elements, element)
		offset += size

		if element.Tag == TagFileMetaInformationGroupLength {
			declaredLength = binary.LittleEndian.Uint32(pad4(element.Value))
			afterGroupLength = offset
		}
	}

	if syntax := out.Text(TagTransferSyntaxUID); syntax != "" {
		out.TransferSyntax = syntax
	}

	if declaredLength > 0 {
		end := afterGroupLength + int(declaredLength)
		if end <= len(data) {
			return end, nil
		}
		// A declared length past the end of the input is a truncated file rather than a reason to guess.
		return 0, ErrTruncated
	}

	return offset, nil
}

func pad4(b []byte) []byte {
	if len(b) >= 4 {
		return b[:4]
	}
	out := make([]byte, 4)
	copy(out, b)
	return out
}

// encodingFor reports how to read a data set with the given transfer syntax.
func encodingFor(syntax string) (explicit bool, order binary.ByteOrder, err error) {
	switch syntax {
	case ImplicitVRLittleEndian:
		return false, binary.LittleEndian, nil
	case ExplicitVRBigEndian:
		return true, binary.BigEndian, nil
	case DeflatedExplicitVRLE:
		// Refused rather than attempted. The data set is deflate-compressed as a whole, so reading it means
		// decompressing first - which is a different shape of operation than everything else here, and silently
		// producing nonsense would be worse than saying no.
		return false, nil, fmt.Errorf("%w: %s is deflate-compressed, which this does not decompress",
			ErrUnsupportedTransferSyntax, syntax)
	default:
		// Everything else - explicit VR little endian, and every compressed pixel encoding - has an explicit VR
		// little endian data set. The compression applies to pixel data, which this package skips anyway, so a JPEG
		// or JPEG 2000 study parses perfectly for metadata.
		return true, binary.LittleEndian, nil
	}
}

// readElements reads data elements until the input runs out.
func readElements(data []byte, explicit bool, order binary.ByteOrder) ([]Element, error) {
	var out []Element
	offset := 0

	for offset < len(data) {
		if len(data)-offset < 8 {
			// Trailing bytes too short to be an element. Tolerated rather than reported, because a data set padded
			// to an even length by a sender is common and refusing it would reject readable studies.
			break
		}

		element, size, ok := readOneElement(data[offset:], explicit, order)
		if !ok {
			return out, ErrTruncated
		}

		out = append(out, element)
		offset += size

		// Pixel data is the last thing in a data set in practice, and everything after it is more pixels. Stopping
		// here rather than walking encapsulated fragments is what keeps this fast on a large study.
		if element.Tag == TagPixelData {
			break
		}
	}

	return out, nil
}

// readOneElement reads a single element and reports how many bytes it occupied.
func readOneElement(data []byte, explicit bool, order binary.ByteOrder) (Element, int, bool) {
	if len(data) < 8 {
		return Element{}, 0, false
	}

	e := Element{
		Tag: Tag{
			Group:   order.Uint16(data[0:2]),
			Element: order.Uint16(data[2:4]),
		},
	}

	offset := 4

	if explicit {
		vr := string(data[4:6])
		e.VR = vr
		offset = 6

		// The VRs with a two-byte length are the exception, and there are exactly six of them. Every other VR uses a
		// four-byte length preceded by two reserved bytes. Getting this wrong reads the length from the wrong place
		// and produces a plausible-looking wrong value rather than an error.
		switch vr {
		case "OB", "OW", "OF", "SQ", "UT", "UN":
			if len(data) < 12 {
				return Element{}, 0, false
			}
			e.Length = order.Uint32(data[8:12])
			offset = 12
		default:
			e.Length = uint32(order.Uint16(data[6:8]))
			offset = 8
		}
	} else {
		e.Length = order.Uint32(data[4:8])
		offset = 8
		// Inferred from the tag so a caller never needs to know which encoding was used. Only the tags this package
		// cares about are inferred; anything else reports UN, which is honest rather than guessed.
		e.VR = inferVR(e.Tag)
	}

	// Undefined length means the value is delimited rather than counted, which happens for sequences and encapsulated
	// pixel data. Walking those properly is a recursive job; here the element is reported and reading stops, because
	// a partial walk that looked complete would be worse than an obvious stop.
	if e.Length == 0xFFFFFFFF {
		e.Skipped = true
		return e, offset, true
	}

	end := offset + int(e.Length)
	if end > len(data) || end < offset {
		return Element{}, 0, false
	}

	if e.Length > maxHeldValue {
		// Reported with its length, so a caller can see an image is present and how big it is without this having
		// pulled it into memory.
		e.Skipped = true
		return e, end, true
	}

	// Copied rather than referenced. The caller's buffer is frequently a network read buffer that will be reused, and
	// the failure from aliasing it is one patient's study reporting another patient's identifiers - the same hazard
	// the HL7 parser documents, and the same decision.
	e.Value = bytes.Clone(data[offset:end])

	return e, end, true
}

// inferVR guesses a value representation from a tag, for implicit VR data sets.
//
// Only the tags this package reads. Anything else returns UN rather than a guess, because a wrong VR does not fail, it
// produces a wrong value - and "unknown" is a fact a caller can act on while a wrong guess is not.
func inferVR(tag Tag) string {
	switch tag {
	case TagPatientName, TagReferringPhysician:
		return "PN"
	case TagAccessionNumber, TagStudyID:
		return "SH"
	case TagPatientID:
		// LO, not SH. Caught by comparing against a file DCMTK wrote, which states its representations explicitly -
		// my own reading of the standard had this wrong, and in an implicit VR file inference is the only source of
		// truth, so it would have been wrong silently.
		return "LO"
	case TagPatientBirthDate, TagStudyDate:
		return "DA"
	case TagStudyTime:
		return "TM"
	case TagPatientSex, TagModality, TagBodyPartExamined:
		return "CS"
	case TagSOPClassUID, TagSOPInstanceUID, TagStudyInstanceUID, TagSeriesInstanceUID,
		TagTransferSyntaxUID, TagMediaStorageSOPClassUID, TagMediaStorageSOPInstanceUID:
		return "UI"
	case TagStudyDescription, TagSeriesDescription, TagInstitutionName:
		return "LO"
	case TagSeriesNumber, TagInstanceNumber:
		return "IS"
	case TagSpecificCharacterSet:
		return "CS"
	case TagPixelData:
		return "OW"

	// The attributes the de-identification step writes. Here rather than special-cased in transform.go because inference is
	// the only source of truth in an implicit VR file, and a caller that knew better than this function would be a second
	// dictionary to keep in step.
	//
	// Without these they fall through to UN, which is encoded in long form. I first wrote that this caused a round-trip
	// failure I had seen; it did not. That failure was a wrong transfer syntax in my own test fixture, and the untransformed
	// object failed the same way - which is what identified the fixture rather than the code.
	//
	// The hazard is still real and is why they are here: an implicit VR data set carries no VR on the wire, so inference is
	// the only source of truth for what a value means, and an attribute written as UN is read as something other than what it
	// is. TestEveryTagTheStepsWriteHasAKnownVR guards the coverage. It does not guard that these VRs are correct - that needs
	// comparison against an independent implementation, which is how TagPatientID above was found to be LO and not SH.
	case tagPatientIdentityRemoved:
		return "CS"
	case tagDeidentifyMethod:
		return "LO"
	case tagPatientAddress, tagOtherPatientIDs, tagOtherPatientNames, tagOccupation, tagEthnicGroup:
		return "LO"
	case tagPatientComments, tagMedicalHistory:
		return "LT"
	case tagPatientAge:
		return "AS"
	case tagPatientPhone:
		return "SH"
	case tagPerformingPhysician, tagOperatorName, tagReadingPhysician:
		return "PN"
	case tagAdmittingDiagnoses:
		return "LO"
	case tagInstitutionAddress:
		return "ST"
	case tagDepartmentName, tagStationName:
		return "LO"
	case tagSeriesDate, tagAcquisitionDate, tagContentDate:
		return "DA"
	case tagSeriesTime:
		return "TM"
	case tagCallingAE, tagCalledAE:
		return "AE"
	}
	return "UN"
}
