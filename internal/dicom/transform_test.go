package dicom

import (
	"strings"
	"testing"
)

// identified builds a data set carrying the identity a de-identification step has to remove.
func identified() *DataSet {
	text := func(tag Tag, v string) Element {
		if len(v)%2 == 1 {
			v += " "
		}

		return Element{Tag: tag, VR: inferVR(tag), Value: []byte(v)}
	}

	// Implicit VR little endian, which is what Encode defaults to. Encode writes no meta group, so Parse has no way to learn
	// a different syntax and would read explicit-VR bytes as implicit. The first version of this fixture claimed explicit and
	// the round-trip test failed on the *untransformed* object, which is how I found the fixture was wrong rather than the
	// transform - worth checking that way round before believing a new test.
	return &DataSet{
		TransferSyntax: ImplicitVRLittleEndian,
		Elements: []Element{
			text(tagPatientName, "OKONKWO^ADAEZE"),
			text(tagPatientID, "MRN123456"),
			text(tagPatientBirthDate, "19551014"),
			text(tagPatientSex, "F"),
			text(tagPatientAddress, "12 CHURCH LANE"),
			text(tagReferringPhysician, "NAKAMURA^KENJI"),
			text(tagInstitutionName, "ST ELSEWHERE"),
			text(tagStudyDate, "20260830"),
			text(tagAccessionNumber, "ACC001"),
			// A private tag, to prove the two steps are independent.
			{Tag: Tag{0x0029, 0x1010}, VR: "LO", Value: []byte("VENDORDATA")},
		},
	}
}

func TestDeidentifyEmptiesAndRemovesTheRightAttributes(t *testing.T) {
	out, changes, err := Apply(identified(), []Step{{Deidentify: &DeidentifyStep{PatientID: "ANON-001"}}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("no changes were reported")
	}

	// Emptied-with-a-pseudonym: present, and not the original.
	if got := out.Text(tagPatientID); got != "ANON-001" {
		t.Errorf("PatientID is %q, want the pseudonym", got)
	}
	if got := out.Text(tagPatientName); got != "ANON-001" {
		t.Errorf("PatientName is %q, want it to agree with the identifier", got)
	}

	// Emptied: present but carrying nothing. Present matters because these are Type 2 and some viewers refuse an object
	// missing them.
	if _, ok := out.Get(tagPatientBirthDate); !ok {
		t.Error("PatientBirthDate was removed rather than emptied, which can make a viewer refuse the object")
	}
	if got := out.Text(tagPatientBirthDate); got != "" {
		t.Errorf("PatientBirthDate still carries %q", got)
	}

	// Removed entirely.
	for _, tag := range []Tag{tagPatientAddress, tagInstitutionName} {
		if _, ok := out.Get(tag); ok {
			t.Errorf("%s is still present", tag)
		}
	}

	// Kept, being clinically necessary.
	if got := out.Text(tagPatientSex); got != "F" {
		t.Errorf("PatientSex is %q, want it kept - it is needed to interpret the study", got)
	}

	// The profile's own marker, so a receiver can tell this went through de-identification.
	if got := out.Text(tagPatientIdentityRemoved); got != "YES" {
		t.Errorf("PatientIdentityRemoved is %q, want YES", got)
	}
	if out.Text(tagDeidentifyMethod) == "" {
		t.Error("DeidentificationMethod is empty, so a receiver cannot tell what was done")
	}
}

// TestDeidentifyLeavesTheInputAlone is why Apply copies.
//
// A failed later step must not leave a half-de-identified object, and a retry has to start from what arrived.
func TestDeidentifyLeavesTheInputAlone(t *testing.T) {
	in := identified()
	if _, _, err := Apply(in, []Step{{Deidentify: &DeidentifyStep{PatientID: "ANON-001"}}}); err != nil {
		t.Fatal(err)
	}

	if got := in.Text(tagPatientName); got != "OKONKWO^ADAEZE" {
		t.Errorf("the input was modified: PatientName is now %q", got)
	}
}

func TestKeepDatesRetainsThem(t *testing.T) {
	out, _, err := Apply(identified(), []Step{{Deidentify: &DeidentifyStep{PatientID: "A", KeepDates: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Text(tagStudyDate); got != "20260830" {
		t.Errorf("StudyDate is %q with keep_dates set", got)
	}

	out, _, err = Apply(identified(), []Step{{Deidentify: &DeidentifyStep{PatientID: "A"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Text(tagStudyDate); got != "" {
		t.Errorf("StudyDate is %q without keep_dates", got)
	}
}

// TestDeidentifyDoesNotTouchPrivateTags is the honest limit, asserted so nobody assumes otherwise.
//
// Vendors store identity in private tags. De-identification alone therefore does not produce a publishable object, and a site
// needs strip_private as well. This test exists to make that a documented behaviour rather than a surprise.
func TestDeidentifyDoesNotTouchPrivateTags(t *testing.T) {
	out, _, err := Apply(identified(), []Step{{Deidentify: &DeidentifyStep{PatientID: "A"}}})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := out.Get(Tag{0x0029, 0x1010}); !ok {
		t.Error("the private tag was removed by deidentify, which is not what it claims to do - if this is now intended, the doc comment and the queue's honest-limits note both have to change")
	}
}

func TestStripPrivateRemovesOddGroupsAndKeepsWhatIsNamed(t *testing.T) {
	in := identified()
	in.Elements = append(in.Elements, Element{Tag: Tag{0x0029, 0x1020}, VR: "LO", Value: []byte("DOSEINFO")})

	out, changes, err := Apply(in, []Step{{StripPrivate: &StripPrivateStep{Keep: []string{"0029,1020"}}}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("got %d change(s), want 1", len(changes))
	}

	if _, ok := out.Get(Tag{0x0029, 0x1010}); ok {
		t.Error("an unnamed private tag survived")
	}
	if _, ok := out.Get(Tag{0x0029, 0x1020}); !ok {
		t.Error("a private tag named in keep was removed, which could destroy the only copy of a dose record")
	}

	// Public tags are untouched.
	if got := out.Text(tagPatientName); got != "OKONKWO^ADAEZE" {
		t.Errorf("strip_private changed a public tag: PatientName is %q", got)
	}
}

// TestKeepingAnEvenGroupIsRefused stops a misunderstanding becoming a false sense of safety.
func TestKeepingAnEvenGroupIsRefused(t *testing.T) {
	_, _, err := Apply(identified(), []Step{{StripPrivate: &StripPrivateStep{Keep: []string{"0010,0010"}}}})
	if err == nil {
		t.Fatal("an even group was accepted in a private-tag keep list, leaving somebody believing that tag was protected by a rule that never applied to it")
	}
	if !strings.Contains(err.Error(), "odd") {
		t.Errorf("the error should explain that private groups are odd-numbered, got: %v", err)
	}
}

func TestSetAETitleWritesBothDirections(t *testing.T) {
	out, changes, err := Apply(identified(), []Step{{SetAETitle: &AETitleStep{Calling: "PERFUSE", Called: "ARCHIVE"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("got %d change(s), want 2", len(changes))
	}
	if got := out.Text(tagCallingAE); got != "PERFUSE" {
		t.Errorf("calling AE is %q", got)
	}
	if got := out.Text(tagCalledAE); got != "ARCHIVE" {
		t.Errorf("called AE is %q", got)
	}
}

// TestAnOverlongAETitleIsRefusedRatherThanTruncated is the failure worth preventing.
//
// A truncated AE title reaches a different node than the one written down, and the symptom is images arriving somewhere
// unexpected rather than a configuration error.
func TestAnOverlongAETitleIsRefusedRatherThanTruncated(t *testing.T) {
	s := Step{SetAETitle: &AETitleStep{Called: "THIS_TITLE_IS_FAR_TOO_LONG_TO_SEND"}}
	err := s.Validate()
	if err == nil {
		t.Fatal("an over-long AE title was accepted, so it would be truncated and reach a different node")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Errorf("the error should say what the limit is, got: %v", err)
	}
}

func TestABackslashInAnAETitleIsRefused(t *testing.T) {
	s := Step{SetAETitle: &AETitleStep{Calling: `A\B`}}
	if err := s.Validate(); err == nil {
		t.Fatal("a backslash was accepted, and it separates values on the wire so the title would split in two")
	}
}

func TestSetInstitutionRewritesWhatIsNamed(t *testing.T) {
	out, _, err := Apply(identified(), []Step{{SetInstitution: &InstitutionStep{Name: "RESEARCH SITE"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Text(tagInstitutionName); got != "RESEARCH SITE" {
		t.Errorf("InstitutionName is %q", got)
	}
	// Address was not named, so it is left alone rather than emptied.
	if got := out.Text(tagInstitutionAddress); got != "" {
		t.Errorf("InstitutionAddress is %q and was not named in the step", got)
	}
}

// TestANoOpSetReportsNoChange keeps an audit trail honest.
//
// Setting an attribute to the value it already has is not a change, and counting it would make a change count that overstates -
// which is the same failure the X12 neutralised-write test guards.
func TestANoOpSetReportsNoChange(t *testing.T) {
	_, changes, err := Apply(identified(), []Step{{SetInstitution: &InstitutionStep{Name: "ST ELSEWHERE"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("setting an attribute to its existing value reported %d change(s)", len(changes))
	}
}

// TestDeidentifyingAnObjectWithoutIdentityDoesNotInventChanges is the same honesty from the other side.
func TestDeidentifyingAnObjectWithoutIdentityDoesNotInventChanges(t *testing.T) {
	bare := &DataSet{TransferSyntax: ImplicitVRLittleEndian, Elements: []Element{
		{Tag: tagPatientSex, VR: "CS", Value: []byte("F ")},
	}}

	_, changes, err := Apply(bare, []Step{{Deidentify: &DeidentifyStep{}}})
	if err != nil {
		t.Fatal(err)
	}

	// Only the two markers should be added: nothing was removed, because there was nothing there.
	for _, c := range changes {
		if c.Action == "removed" {
			t.Errorf("claimed to remove %s from an object that never had it", c.Tag)
		}
	}
}

// TestAChangeDoesNotCarryTheValueIntoALog is a privacy property of the audit trail itself.
//
// A change to PatientName rendered with its value would put the patient's name into a log file the whole operations team can
// read - the identity the step exists to remove.
func TestAChangeDoesNotCarryTheValueIntoALog(t *testing.T) {
	_, changes, err := Apply(identified(), []Step{{Deidentify: &DeidentifyStep{PatientID: "ANON-001"}}})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range changes {
		rendered := c.String()
		for _, secret := range []string{"OKONKWO", "MRN123456", "19551014", "12 CHURCH LANE"} {
			if strings.Contains(rendered, secret) {
				t.Errorf("a change rendered %q, putting identity into the log: %s", secret, rendered)
			}
		}
	}
}

func TestAStepNamingTwoActionsIsRefused(t *testing.T) {
	s := Step{
		Deidentify:   &DeidentifyStep{},
		StripPrivate: &StripPrivateStep{},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("a step with two actions was accepted, so the order would be this struct's field order - arbitrary and invisible in the yaml")
	}
}

func TestAnEmptyStepIsRefused(t *testing.T) {
	if err := (Step{}).Validate(); err == nil {
		t.Fatal("a step naming no action was accepted")
	}
}

func TestParseTagAcceptsBothForms(t *testing.T) {
	for _, raw := range []string{"0010,0010", "(0010,0010)", " 0010 , 0010 "} {
		tag, err := ParseTag(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)

			continue
		}
		if tag != (Tag{0x0010, 0x0010}) {
			t.Errorf("%q parsed to %s", raw, tag)
		}
	}
}

func TestParseTagRefusesNonsense(t *testing.T) {
	for _, raw := range []string{"0010", "ZZZZ,0010", "10,10", ""} {
		if _, err := ParseTag(raw); err == nil {
			t.Errorf("%q was accepted as a tag", raw)
		}
	}
}

// TestTheTransformedObjectStillEncodes is the check that the result is a DICOM object rather than a struct that looks like one.
func TestTheTransformedObjectStillEncodes(t *testing.T) {
	out, _, err := Apply(identified(), []Step{
		{Deidentify: &DeidentifyStep{PatientID: "ANON-001"}},
		{StripPrivate: &StripPrivateStep{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := Encode(out.Elements, out.TransferSyntax)
	if err != nil {
		t.Fatalf("the transformed object does not encode: %v", err)
	}

	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("the encoded object does not parse: %v", err)
	}
	if got := back.Text(tagPatientID); got != "ANON-001" {
		t.Errorf("after a round trip PatientID is %q", got)
	}
	if got := back.Text(tagPatientName); got != "ANON-001" {
		t.Errorf("after a round trip PatientName is %q", got)
	}
}
