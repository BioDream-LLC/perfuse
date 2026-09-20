// Package dicom reads DICOM data sets.
//
// DICOM is where radiology lives, and a hospital integration engine that cannot speak it is shut out of imaging
// entirely. Mirth has a DICOM listener and sender, so this is a migration blocker for any site doing modality work.
//
// # Scope, stated plainly
//
// This package parses data sets: the file meta information, the elements, the transfer syntax, and the values that
// matter for routing and identification. It does not decode pixel data, and it never will as far as this package is
// concerned - an integration engine moves images, it does not display them, and a decoder is a large body of
// format-specific work whose only purpose here would be to make the binary bigger.
//
// Reading metadata without touching pixels is also what makes it fast on the traffic that matters: a chest CT is
// hundreds of megabytes of pixels behind two kilobytes of the information anybody routes on.
//
// # Why the value representations are handled explicitly
//
// A DICOM element carries a two-letter value representation saying how to read its value, and the rules differ per VR
// in ways that cannot be guessed: some have a two-byte length and some four, some are padded with a space and some
// with a null, and numeric strings can hold several values separated by backslashes. Getting one wrong does not
// produce an error, it produces a plausible-looking wrong value - a patient name read from the middle of a date, or a
// study identifier off by one byte.
package dicom

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Tag identifies an element by group and element number.
type Tag struct {
	Group   uint16
	Element uint16
}

// String renders a tag the way DICOM documentation does, so a value can be looked up.
func (t Tag) String() string {
	return fmt.Sprintf("(%04X,%04X)", t.Group, t.Element)
}

// IsPrivate reports whether this is a private tag, which is an odd group number.
//
// Worth distinguishing because private tags are vendor extensions whose meaning is not in the standard, so a
// de-identifier must remove them rather than trust them, and a router must not depend on them.
func (t Tag) IsPrivate() bool { return t.Group%2 == 1 }

// Tags used for routing and identification. Not a full dictionary: this is what an integration engine actually reads,
// and a complete dictionary is a large generated table that would earn its place only once something needs it.
var (
	TagFileMetaInformationGroupLength = Tag{0x0002, 0x0000}
	TagTransferSyntaxUID              = Tag{0x0002, 0x0010}
	TagMediaStorageSOPClassUID        = Tag{0x0002, 0x0002}
	TagMediaStorageSOPInstanceUID     = Tag{0x0002, 0x0003}

	TagSpecificCharacterSet = Tag{0x0008, 0x0005}
	TagSOPClassUID          = Tag{0x0008, 0x0016}
	TagSOPInstanceUID       = Tag{0x0008, 0x0018}
	TagStudyDate            = Tag{0x0008, 0x0020}
	TagStudyTime            = Tag{0x0008, 0x0030}
	TagAccessionNumber      = Tag{0x0008, 0x0050}
	TagModality             = Tag{0x0008, 0x0060}
	TagInstitutionName      = Tag{0x0008, 0x0080}
	TagReferringPhysician   = Tag{0x0008, 0x0090}
	TagStudyDescription     = Tag{0x0008, 0x1030}
	TagSeriesDescription    = Tag{0x0008, 0x103E}

	TagPatientName      = Tag{0x0010, 0x0010}
	TagPatientID        = Tag{0x0010, 0x0020}
	TagPatientBirthDate = Tag{0x0010, 0x0030}
	TagPatientSex       = Tag{0x0010, 0x0040}

	TagBodyPartExamined = Tag{0x0018, 0x0015}

	TagStudyInstanceUID  = Tag{0x0020, 0x000D}
	TagSeriesInstanceUID = Tag{0x0020, 0x000E}
	TagStudyID           = Tag{0x0020, 0x0010}
	TagSeriesNumber      = Tag{0x0020, 0x0011}
	TagInstanceNumber    = Tag{0x0020, 0x0013}

	TagPixelData = Tag{0x7FE0, 0x0010}
)

// Transfer syntax UIDs that determine how a data set is encoded.
const (
	ImplicitVRLittleEndian = "1.2.840.10008.1.2"
	ExplicitVRLittleEndian = "1.2.840.10008.1.2.1"
	ExplicitVRBigEndian    = "1.2.840.10008.1.2.2"
	DeflatedExplicitVRLE   = "1.2.840.10008.1.2.1.99"
)

// Errors this package returns.
var (
	// ErrNotDICOM means the input has no recognisable DICOM preamble or meta group.
	ErrNotDICOM = errors.New("dicom: this does not look like a DICOM data set")

	// ErrTruncated means the data set ended in the middle of an element.
	//
	// Distinguished from ErrNotDICOM because the causes are opposite: one is the wrong kind of file, the other is
	// the right kind cut short - usually a transfer that failed part way, which points at the network rather than
	// at the sender's software.
	ErrTruncated = errors.New("dicom: the data set ends part way through an element")

	// ErrUnsupportedTransferSyntax means the encoding is one this cannot read.
	ErrUnsupportedTransferSyntax = errors.New("dicom: unsupported transfer syntax")
)

// Element is one data element.
type Element struct {
	Tag Tag

	// VR is the two-letter value representation. Filled in even for implicit VR data sets, where it is inferred from
	// the tag, so a caller never has to know which encoding the file used.
	VR string

	// Length is the declared value length in bytes, before any padding is trimmed.
	Length uint32

	// Value is the raw bytes, excluding padding for string types.
	//
	// Not held for pixel data, which is skipped rather than read - see Skipped.
	Value []byte

	// Skipped reports that the value was not read because it was too large to be worth holding. The element's tag
	// and length are still reported, so a caller can see that an image is present and how big it is without the
	// parser having pulled hundreds of megabytes into memory.
	Skipped bool
}

// String returns the value as text with padding removed.
//
// Trailing nulls and spaces both go: DICOM pads string values to an even length, using a space for most types and a
// null for UIDs, and a caller comparing a UID against a constant would otherwise fail on an invisible byte.
func (e Element) String() string {
	return strings.TrimRight(string(e.Value), "\x00 ")
}

// Values splits a multi-valued element on the backslash separator.
//
// Several DICOM types legitimately hold a list - image orientation, a patient's other identifiers - and reading only
// the first is a silent truncation.
func (e Element) Values() []string {
	text := e.String()
	if text == "" {
		return nil
	}
	parts := strings.Split(text, `\`)
	for i := range parts {
		parts[i] = strings.TrimRight(parts[i], "\x00 ")
	}
	return parts
}

// Uint16 reads the value as an unsigned short, for US elements.
func (e Element) Uint16(order binary.ByteOrder) (uint16, bool) {
	if len(e.Value) < 2 {
		return 0, false
	}
	return order.Uint16(e.Value), true
}
