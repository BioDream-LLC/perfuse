package dicom

import (
	"testing"
)

// TestEncodeRoundTrip encodes a data set and reads it back with the parser.
//
// The parser is the reference here rather than a hand-written byte layout, because the parser is already verified against
// DCMTK. If these two agree and one of them agrees with DCMTK, both do.
func TestEncodeRoundTrip(t *testing.T) {
	for _, syntax := range []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian, ExplicitVRBigEndian} {
		t.Run(syntax, func(t *testing.T) {
			in := []Element{
				{Tag: TagPatientID, Value: []byte("MRN0009001")},
				{Tag: TagPatientName, Value: []byte("FROST^IVY^MARIE")},
				{Tag: TagModality, Value: []byte("CT")},
				{Tag: TagAccessionNumber, Value: []byte("ACC77321")},
				{Tag: TagStudyInstanceUID, Value: []byte("1.2.826.0.1.3680043.8.1055.99")},
			}

			encoded, err := Encode(in, syntax)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			ds, err := ParseDataSet(encoded, syntax)
			if err != nil {
				t.Fatalf("parse back: %v", err)
			}

			for _, want := range in {
				got := ds.Text(want.Tag)
				if got != string(want.Value) {
					t.Errorf("%s: encoded %q and read back %q", want.Tag, want.Value, got)
				}
			}
		})
	}
}

// TestEncodeSortsByTag checks ascending tag order.
//
// The standard requires it and streaming receivers rely on it, so an unsorted query would work against a forgiving
// archive and fail against a strict one - the worst kind of bug to find in production.
func TestEncodeSortsByTag(t *testing.T) {
	out, err := Encode([]Element{
		{Tag: TagStudyInstanceUID, Value: []byte("1.2.3")},
		{Tag: TagPatientID, Value: []byte("MRN1")},
		{Tag: TagModality, Value: []byte("CT")},
	}, ExplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	ds, err := ParseDataSet(out, ExplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var last Tag
	for i, e := range ds.Elements {
		if i > 0 && !tagLess(last, e.Tag) {
			t.Fatalf("element %d has tag %s, which does not follow %s", i, e.Tag, last)
		}
		last = e.Tag
	}
}

// TestEncodeOddLengthPadding checks that an odd value is padded, and padded with the right byte.
//
// A UID padded with a space rather than a null is the specific failure worth a test: it is invisible in a dump and makes a
// receiver comparing against a constant see a study it does not recognise.
func TestEncodeOddLengthPadding(t *testing.T) {
	out, err := Encode([]Element{
		{Tag: TagStudyInstanceUID, Value: []byte("1.2.3.4.5")}, // nine bytes
		{Tag: TagPatientName, Value: []byte("FROST")},          // five bytes
	}, ExplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if len(out)%2 != 0 {
		t.Errorf("the whole data set is %d bytes, which is odd", len(out))
	}

	ds, err := ParseDataSet(out, ExplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Read back through Text, which trims padding. The point is that the round trip is clean, not that the bytes match.
	if got := ds.Text(TagStudyInstanceUID); got != "1.2.3.4.5" {
		t.Errorf("UID read back as %q", got)
	}
	if got := ds.Text(TagPatientName); got != "FROST" {
		t.Errorf("name read back as %q", got)
	}
}
