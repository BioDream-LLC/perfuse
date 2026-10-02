package dicom

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// Builders that produce real DICOM byte layouts. Written by hand rather than using a fixture file, because the whole
// risk in this parser is byte offsets and a hand-built data set is the only way to be sure a test exercises the layout
// it claims to.

func explicitElement(tag Tag, vr, value string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, tag.Group)
	_ = binary.Write(&b, binary.LittleEndian, tag.Element)
	b.WriteString(vr)

	padded := value
	if len(padded)%2 == 1 {
		// DICOM values are padded to an even length. A space for most types, a null for UIDs.
		if vr == "UI" {
			padded += "\x00"
		} else {
			padded += " "
		}
	}

	switch vr {
	case "OB", "OW", "OF", "SQ", "UT", "UN":
		b.Write([]byte{0, 0})
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(padded)))
	default:
		_ = binary.Write(&b, binary.LittleEndian, uint16(len(padded)))
	}

	b.WriteString(padded)
	return b.Bytes()
}

func implicitElement(tag Tag, value string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, tag.Group)
	_ = binary.Write(&b, binary.LittleEndian, tag.Element)

	padded := value
	if len(padded)%2 == 1 {
		padded += " "
	}
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(padded)))
	b.WriteString(padded)
	return b.Bytes()
}

// metaGroup builds a file meta information group declaring a transfer syntax.
func metaGroup(transferSyntax string) []byte {
	inner := bytes.Join([][]byte{
		explicitElement(TagMediaStorageSOPClassUID, "UI", "1.2.840.10008.5.1.4.1.1.2"),
		explicitElement(TagMediaStorageSOPInstanceUID, "UI", "1.2.3.4.5"),
		explicitElement(TagTransferSyntaxUID, "UI", transferSyntax),
	}, nil)

	var lengthElement bytes.Buffer
	_ = binary.Write(&lengthElement, binary.LittleEndian, uint16(0x0002))
	_ = binary.Write(&lengthElement, binary.LittleEndian, uint16(0x0000))
	lengthElement.WriteString("UL")
	_ = binary.Write(&lengthElement, binary.LittleEndian, uint16(4))
	_ = binary.Write(&lengthElement, binary.LittleEndian, uint32(len(inner)))

	return append(lengthElement.Bytes(), inner...)
}

func withPreamble(body []byte) []byte {
	out := make([]byte, preambleLength)
	out = append(out, []byte(magic)...)
	return append(out, body...)
}

func TestAnExplicitVRFileParses(t *testing.T) {
	data := withPreamble(append(metaGroup(ExplicitVRLittleEndian), bytes.Join([][]byte{
		explicitElement(TagSOPInstanceUID, "UI", "1.2.3.4.5"),
		explicitElement(TagStudyDate, "DA", "20260821"),
		explicitElement(TagModality, "CS", "CT"),
		explicitElement(TagPatientName, "PN", "FROST^IVY^MARIE"),
		explicitElement(TagPatientID, "SH", "MRN0001"),
		explicitElement(TagAccessionNumber, "SH", "ACC123"),
		explicitElement(TagStudyInstanceUID, "UI", "1.2.840.113619.2.1"),
	}, nil)...))

	ds, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if !ds.HadPreamble {
		t.Error("the preamble was not recognised")
	}
	if ds.TransferSyntax != ExplicitVRLittleEndian {
		t.Errorf("transfer syntax = %q", ds.TransferSyntax)
	}

	for _, tc := range []struct {
		tag  Tag
		want string
	}{
		{TagPatientName, "FROST^IVY^MARIE"},
		{TagPatientID, "MRN0001"},
		{TagAccessionNumber, "ACC123"},
		{TagModality, "CT"},
		{TagStudyDate, "20260821"},
		{TagStudyInstanceUID, "1.2.840.113619.2.1"},
	} {
		if got := ds.Text(tc.tag); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestTheMetaGroupIsAlwaysExplicitEvenWhenTheDataSetIsNot(t *testing.T) {
	// The detail that makes a naive parser read a whole file wrongly. The meta group is explicit VR little endian
	// regardless of what it declares for the rest of the data set, so a parser that used the declared syntax to read
	// the group it came from would be reading it circularly - and would then misread every element after it.
	body := append(metaGroup(ImplicitVRLittleEndian), bytes.Join([][]byte{
		implicitElement(TagPatientName, "FROST^IVY"),
		implicitElement(TagPatientID, "MRN0002"),
		implicitElement(TagModality, "MR"),
	}, nil)...)

	ds, err := Parse(withPreamble(body))
	if err != nil {
		t.Fatal(err)
	}

	if ds.TransferSyntax != ImplicitVRLittleEndian {
		t.Fatalf("transfer syntax = %q, want implicit", ds.TransferSyntax)
	}
	if got := ds.Text(TagPatientName); got != "FROST^IVY" {
		t.Errorf("patient name = %q; the implicit data set after an explicit meta group was misread", got)
	}
	if got := ds.Text(TagModality); got != "MR" {
		t.Errorf("modality = %q", got)
	}
}

func TestImplicitVRInfersTheRepresentation(t *testing.T) {
	// A caller must not have to know which encoding a file used, because the same channel receives both.
	ds, err := Parse(withPreamble(append(metaGroup(ImplicitVRLittleEndian),
		implicitElement(TagPatientName, "FROST^IVY")...)))
	if err != nil {
		t.Fatal(err)
	}

	e, ok := ds.Get(TagPatientName)
	if !ok {
		t.Fatal("patient name absent")
	}
	if e.VR != "PN" {
		t.Errorf("inferred VR = %q, want PN", e.VR)
	}
}

func TestAnUnknownImplicitTagReportsUNRatherThanGuessing(t *testing.T) {
	// A wrong VR does not fail, it produces a wrong value. "Unknown" is a fact a caller can act on; a guess is not.
	private := Tag{0x0009, 0x0010}
	ds, err := Parse(withPreamble(append(metaGroup(ImplicitVRLittleEndian),
		implicitElement(private, "vendor thing")...)))
	if err != nil {
		t.Fatal(err)
	}

	e, ok := ds.Get(private)
	if !ok {
		t.Fatal("the private element was dropped")
	}
	if e.VR != "UN" {
		t.Errorf("VR = %q, want UN for a tag not in the dictionary", e.VR)
	}
	if !e.Tag.IsPrivate() {
		t.Error("an odd group number was not reported as private")
	}
}

func TestTheSixLongLengthVRsAreReadCorrectly(t *testing.T) {
	// There are exactly six VRs with a four-byte length preceded by two reserved bytes. Reading one of them as a
	// two-byte length takes the length from the wrong place and produces a plausible wrong value rather than an
	// error, which is the worst kind of parsing bug.
	for _, vr := range []string{"OB", "OW", "OF", "SQ", "UT", "UN"} {
		tag := Tag{0x0011, 0x0001}
		body := append(metaGroup(ExplicitVRLittleEndian), explicitElement(tag, vr, "twelve bytes")...)

		ds, err := Parse(withPreamble(body))
		if err != nil {
			t.Errorf("%s: %v", vr, err)
			continue
		}
		e, ok := ds.Get(tag)
		if !ok {
			t.Errorf("%s: element absent", vr)
			continue
		}
		if e.String() != "twelve bytes" {
			t.Errorf("%s: value = %q, want %q", vr, e.String(), "twelve bytes")
		}
	}
}

func TestPaddingIsTrimmedForBothKindsOfPad(t *testing.T) {
	// UIDs are padded with a null and most other types with a space. A caller comparing a UID against a constant
	// would fail on an invisible trailing byte, and the failure would look like a mismatched study.
	body := append(metaGroup(ExplicitVRLittleEndian), bytes.Join([][]byte{
		explicitElement(TagSOPInstanceUID, "UI", "1.2.3"),   // odd length, null padded
		explicitElement(TagStudyDescription, "LO", "CHEST"), // odd length, space padded
	}, nil)...)

	ds, err := Parse(withPreamble(body))
	if err != nil {
		t.Fatal(err)
	}

	if got := ds.Text(TagSOPInstanceUID); got != "1.2.3" {
		t.Errorf("UID = %q, want the null padding removed", got)
	}
	if got := ds.Text(TagStudyDescription); got != "CHEST" {
		t.Errorf("description = %q, want the space padding removed", got)
	}
}

func TestMultipleValuesAreSplit(t *testing.T) {
	// Reading only the first value of a legitimately multi-valued element is a silent truncation.
	tag := Tag{0x0010, 0x1000}
	body := append(metaGroup(ExplicitVRLittleEndian),
		explicitElement(tag, "LO", `MRN1\MRN2\MRN3`)...)

	ds, err := Parse(withPreamble(body))
	if err != nil {
		t.Fatal(err)
	}

	e, _ := ds.Get(tag)
	values := e.Values()
	if len(values) != 3 {
		t.Fatalf("split into %d values, want 3: %q", len(values), values)
	}
	if values[2] != "MRN3" {
		t.Errorf("last value = %q", values[2])
	}
}

func TestPixelDataIsSkippedRatherThanRead(t *testing.T) {
	// The reason this is fast on a real study. A chest CT is hundreds of megabytes of pixels behind two kilobytes of
	// the information anybody routes on, and reading the pixels to reach the metadata would make the common case as
	// slow as the worst one.
	pixels := strings.Repeat("\x00", 4<<20)
	body := append(metaGroup(ExplicitVRLittleEndian), bytes.Join([][]byte{
		explicitElement(TagPatientID, "SH", "MRN0003"),
		explicitElement(TagPixelData, "OW", pixels),
	}, nil)...)

	ds, err := Parse(withPreamble(body))
	if err != nil {
		t.Fatal(err)
	}

	if got := ds.Text(TagPatientID); got != "MRN0003" {
		t.Errorf("patient id = %q", got)
	}

	e, ok := ds.Get(TagPixelData)
	if !ok {
		t.Fatal("pixel data was not reported at all; a caller cannot tell an image is present")
	}
	if !e.Skipped {
		t.Error("pixel data was read into memory")
	}
	if len(e.Value) != 0 {
		t.Errorf("pixel data holds %d bytes despite being skipped", len(e.Value))
	}
	if e.Length != uint32(len(pixels)) {
		t.Errorf("declared length = %d, want %d - the size must still be reported", e.Length, len(pixels))
	}
}

func TestANetworkDataSetWithNoPreambleParses(t *testing.T) {
	// Data sets sent over the wire have no preamble, because the preamble exists so a file can be recognised on disk.
	// Requiring one would reject everything arriving over the network, which is the main way they arrive.
	body := bytes.Join([][]byte{
		implicitElement(TagPatientName, "FROST^IVY"),
		implicitElement(TagPatientID, "MRN0004"),
	}, nil)

	ds, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if ds.HadPreamble {
		t.Error("a preamble was reported where there was none")
	}
	if got := ds.Text(TagPatientID); got != "MRN0004" {
		t.Errorf("patient id = %q", got)
	}
}

func TestSomethingThatIsNotDICOMIsRefused(t *testing.T) {
	// Without this check any file at all parses into a list of nonsense elements, and the caller gets garbage rather
	// than an error - which is how a JPEG ends up in a study.
	for _, input := range [][]byte{
		[]byte("this is just some text, definitely not DICOM at all"),
		{0, 0, 0, 0, 0, 0, 0, 0},
	} {
		if _, err := Parse(input); !errors.Is(err, ErrNotDICOM) {
			t.Errorf("Parse(%q) error = %v, want ErrNotDICOM", input[:min(20, len(input))], err)
		}
	}
}

func TestATruncatedDataSetSaysSoRatherThanReturningPartialData(t *testing.T) {
	// A different cause from the wrong kind of file, and a different fix: this is the right kind cut short, which
	// usually means a transfer failed part way and points at the network rather than the sender's software.
	body := append(metaGroup(ExplicitVRLittleEndian),
		explicitElement(TagPatientName, "PN", "FROST^IVY")...)
	full := withPreamble(body)

	// Cut inside the last element's value.
	truncated := full[:len(full)-4]

	_, err := Parse(truncated)
	if !errors.Is(err, ErrTruncated) {
		t.Errorf("error = %v, want ErrTruncated", err)
	}
}

func TestDeflatedTransferSyntaxIsRefusedWithAReason(t *testing.T) {
	// The data set is compressed as a whole, so reading it means decompressing first - a different shape of operation
	// from everything else here. Silently producing nonsense would be worse than saying no.
	ds, err := Parse(withPreamble(metaGroup(DeflatedExplicitVRLE)))
	if !errors.Is(err, ErrUnsupportedTransferSyntax) {
		t.Fatalf("error = %v (ds %v), want ErrUnsupportedTransferSyntax", err, ds)
	}
	if !strings.Contains(err.Error(), "deflate") {
		t.Errorf("the error does not say why: %v", err)
	}
}

func TestACompressedPixelSyntaxStillYieldsMetadata(t *testing.T) {
	// JPEG and JPEG 2000 compress the pixel data, not the data set, so the metadata is ordinary explicit VR little
	// endian. Refusing these would shut out most of real radiology traffic for no reason.
	jpegLossless := "1.2.840.10008.1.2.4.70"
	body := append(metaGroup(jpegLossless), bytes.Join([][]byte{
		explicitElement(TagPatientID, "SH", "MRN0005"),
		explicitElement(TagModality, "CS", "CR"),
	}, nil)...)

	ds, err := Parse(withPreamble(body))
	if err != nil {
		t.Fatalf("a JPEG-compressed study was refused: %v", err)
	}
	if got := ds.Text(TagPatientID); got != "MRN0005" {
		t.Errorf("patient id = %q", got)
	}
}

func TestTheParserCopiesRatherThanAliasingTheInput(t *testing.T) {
	// The same hazard the HL7 parser documents and the same decision. The caller's buffer is frequently a network read
	// buffer that will be reused, and aliasing it means one patient's study reporting another patient's identifiers.
	body := append(metaGroup(ExplicitVRLittleEndian),
		explicitElement(TagPatientID, "SH", "MRN0006")...)
	buf := withPreamble(body)

	ds, err := Parse(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := ds.Text(TagPatientID); got != "MRN0006" {
		t.Fatalf("patient id = %q before reuse", got)
	}

	for i := range buf {
		buf[i] = 'X'
	}

	if got := ds.Text(TagPatientID); got != "MRN0006" {
		t.Errorf("after the caller reused its buffer the patient id read %q", got)
	}
}

func TestElementOrderIsPreserved(t *testing.T) {
	// Order is evidence. A data set gets diffed against another during an investigation, and reordering would make
	// every comparison noisy.
	body := append(metaGroup(ExplicitVRLittleEndian), bytes.Join([][]byte{
		explicitElement(TagModality, "CS", "CT"),
		explicitElement(TagPatientName, "PN", "FROST^IVY"),
		explicitElement(TagPatientID, "SH", "MRN0007"),
	}, nil)...)

	ds, err := Parse(withPreamble(body))
	if err != nil {
		t.Fatal(err)
	}

	var seen []Tag
	for _, e := range ds.Elements {
		if e.Tag.Group == 0x0008 || e.Tag.Group == 0x0010 {
			seen = append(seen, e.Tag)
		}
	}

	want := []Tag{TagModality, TagPatientName, TagPatientID}
	if len(seen) != len(want) {
		t.Fatalf("saw %d data elements, want %d", len(seen), len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("element %d is %s, want %s", i, seen[i], want[i])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
