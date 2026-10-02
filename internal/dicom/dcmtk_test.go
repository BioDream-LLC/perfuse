package dicom

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures in testdata were produced by DCMTK 3.7.0, the OFFIS toolkit that most hospital equipment and most other
// DICOM software is built against.
//
// This matters more than it might look. Every other test in this package proves the parser agrees with my reading of
// the standard, which is worth something but shares an author with the parser. These prove it agrees with the reference
// implementation, which is a different and much stronger claim, because it catches the places where my reading is
// confidently wrong rather than merely uncertain.
//
// Both files contain synthetic data only. The patient does not exist.
const (
	explicitFixture = "dcmtk-explicit-le.dcm"
	implicitFixture = "dcmtk-implicit-le.dcm"
)

// wantFields is what DCMTK was asked to write, checked against what this parser reads back.
var wantFields = []struct {
	tag  Tag
	want string
}{
	{TagPatientName, "FROST^IVY^MARIE^^"},
	{TagPatientID, "MRN0009001"},
	{TagPatientBirthDate, "19910228"},
	{TagPatientSex, "F"},
	{TagAccessionNumber, "ACC77321"},
	{TagModality, "CT"},
	{TagStudyDate, "20260821"},
	{TagStudyTime, "143000"},
	{TagStudyDescription, "CT CHEST W CONTRAST"},
	{TagInstitutionName, "St Josephs Hospital"},
	{TagBodyPartExamined, "CHEST"},
	{TagStudyInstanceUID, "1.2.826.0.1.3680043.8.1055.2"},
	{TagSeriesInstanceUID, "1.2.826.0.1.3680043.8.1055.3"},
	{TagSOPInstanceUID, "1.2.826.0.1.3680043.8.1055.1"},
	{TagStudyID, "STU4471"},
	{TagSeriesNumber, "3"},
	{TagInstanceNumber, "17"},
}

func parseFixture(t *testing.T, name string) *DataSet {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := Parse(data)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return ds
}

func TestAnExplicitVRFileFromDCMTK(t *testing.T) {
	ds := parseFixture(t, explicitFixture)

	if ds.TransferSyntax != ExplicitVRLittleEndian {
		t.Errorf("transfer syntax = %q, want explicit VR little endian", ds.TransferSyntax)
	}
	if !ds.HadPreamble {
		t.Error("DCMTK writes a preamble and it was not recognised")
	}

	for _, tc := range wantFields {
		if got := ds.Text(tc.tag); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestAnImplicitVRFileFromDCMTK(t *testing.T) {
	// The harder path, and the one an earlier version of this parser got wrong: the meta group is explicit VR whatever
	// the data set is, so the boundary between them has to be right to the byte. It was not, and every implicit file
	// reported as truncated.
	ds := parseFixture(t, implicitFixture)

	if ds.TransferSyntax != ImplicitVRLittleEndian {
		t.Fatalf("transfer syntax = %q, want implicit VR little endian", ds.TransferSyntax)
	}

	for _, tc := range wantFields {
		if got := ds.Text(tc.tag); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestBothEncodingsAgreeWithEachOther(t *testing.T) {
	// The same study written two ways must read identically. If it does not, one of the two paths is wrong and the
	// per-field tests above cannot say which - whereas a disagreement points straight at the encoding.
	explicit := parseFixture(t, explicitFixture)
	implicit := parseFixture(t, implicitFixture)

	for _, tc := range wantFields {
		a, b := explicit.Text(tc.tag), implicit.Text(tc.tag)
		if a != b {
			t.Errorf("%s reads %q from the explicit file and %q from the implicit one", tc.tag, a, b)
		}
	}
}

func TestTheVRsComeFromTheFileWhenExplicit(t *testing.T) {
	// An explicit file states its value representations, so the parser must use them rather than its own inference -
	// and the two must agree for the tags it knows, or one of them is wrong.
	ds := parseFixture(t, explicitFixture)

	checked := 0
	for _, e := range ds.Elements {
		if e.Tag.Group == 0x0002 {
			continue
		}
		inferred := inferVR(e.Tag)
		if inferred == "UN" {
			continue
		}
		checked++
		if e.VR != inferred {
			// LO and SH are both short text and DCMTK may legitimately choose either for some tags, so this reports
			// rather than fails for that pair only.
			if (e.VR == "LO" && inferred == "SH") || (e.VR == "SH" && inferred == "LO") {
				t.Logf("%s: file says %s, inference says %s - both are short text", e.Tag, e.VR, inferred)
				continue
			}
			t.Errorf("%s: the file says VR %s but inference says %s", e.Tag, e.VR, inferred)
		}
	}

	if checked < 10 {
		t.Errorf("only %d elements were checked; the fixture may not contain what this test assumes", checked)
	}
}
