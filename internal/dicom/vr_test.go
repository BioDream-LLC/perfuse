package dicom

import (
	"testing"
)

// TestEveryTagTheStepsWriteHasAKnownVR is the guardrail for a real hazard rather than a tidiness check.
//
// inferVR returns "UN" for a tag it does not know. UN is encoded in long form, and an implicit VR data set has no VR on the
// wire at all - so inference is the only source of truth for what a value means. An attribute written with the wrong VR
// produces a file that encodes without complaint and is then read as something else, or not read at all.
//
// The de-identification and AE title steps add attributes that may not have been in the object. Every one of those tags has to
// be in the dictionary, and the failure if it is not would appear as a de-identified study sitting unreadable in an archive
// rather than as an error at the time.
//
// # What this test does and does not prove
//
// It proves the dictionary covers the tags the steps write. It does not prove those VRs are the right ones - that would need
// comparing against a file written by an independent implementation, which is how TagPatientID was found to be LO rather than
// SH, and the comment there records that my own reading of the standard had it wrong.
//
// So this is the cheap half of the check, and the expensive half is still owed. Stated rather than implied, because a passing
// test named after VR correctness would otherwise suggest the question is settled.
func TestEveryTagTheStepsWriteHasAKnownVR(t *testing.T) {
	// Every tag the transformation steps can write or add, with the VR each is expected to carry.
	written := map[Tag]string{
		tagPatientName:            "PN",
		tagPatientID:              "LO",
		tagPatientBirthDate:       "DA",
		tagAccessionNumber:        "SH",
		tagStudyID:                "SH",
		tagReferringPhysician:     "PN",
		tagStudyDate:              "DA",
		tagSeriesDate:             "DA",
		tagAcquisitionDate:        "DA",
		tagContentDate:            "DA",
		tagStudyTime:              "TM",
		tagSeriesTime:             "TM",
		tagPatientIdentityRemoved: "CS",
		tagDeidentifyMethod:       "LO",
		tagInstitutionName:        "LO",
		tagInstitutionAddress:     "ST",
		tagDepartmentName:         "LO",
		tagCallingAE:              "AE",
		tagCalledAE:               "AE",
	}

	for tag, want := range written {
		got := inferVR(tag)
		if got == "UN" {
			t.Errorf("%s is written by a transformation step and is not in the VR dictionary, so it would be encoded as UN: in an implicit VR data set there is no VR on the wire, and the object would be read as something other than %s or not read at all", tag, want)

			continue
		}
		if got != want {
			t.Errorf("%s infers VR %q, want %q", tag, got, want)
		}
	}
}

// TestEveryTagTheProfileRemovesHasAKnownVR covers the removal lists too.
//
// Removing an element does not need its VR. This checks them anyway, because the lists are also the documentation of what the
// profile touches, and a tag in them that the dictionary does not know is a sign the two were written from different sources -
// which is how the emptied and removed lists would drift from what inferVR believes those attributes are.
func TestEveryTagTheProfileRemovesHasAKnownVR(t *testing.T) {
	all := append(append([]Tag{}, emptied...), removed...)
	all = append(all, dateTags...)

	for _, tag := range all {
		if inferVR(tag) == "UN" {
			t.Errorf("%s is in a de-identification list but not in the VR dictionary", tag)
		}
	}
}

// TestTheEmptiedAndRemovedListsDoNotOverlap catches a contradiction that would be invisible.
//
// A tag in both lists is emptied and then removed, so the emptying is pointless and the object loses a Type 2 attribute it was
// supposed to keep. Nothing about the code would look wrong: both loops run, both succeed, and the result quietly violates the
// profile the step claims to implement.
func TestTheEmptiedAndRemovedListsDoNotOverlap(t *testing.T) {
	inEmptied := make(map[Tag]bool, len(emptied))
	for _, tag := range emptied {
		inEmptied[tag] = true
	}

	for _, tag := range removed {
		if inEmptied[tag] {
			t.Errorf("%s is in both the emptied and removed lists, so it is emptied and then deleted - the object loses a Type 2 attribute that was meant to be kept, and both loops appear to succeed", tag)
		}
	}
}

// TestPatientSexIsDeliberatelyNotRemoved records a decision so that changing it is a decision.
//
// Sex is clinically necessary to interpret a study and weakly identifying alone. Its absence from the removal list looks like an
// oversight to anybody reading the lists, so this asserts it is not one.
func TestPatientSexIsDeliberatelyNotRemoved(t *testing.T) {
	for _, tag := range append(append([]Tag{}, emptied...), removed...) {
		if tag == tagPatientSex {
			t.Error("PatientSex is now removed or emptied by the profile. That may be right for a stricter use, but it makes studies harder to interpret, so it needs to be a stated decision rather than an edit to a list")
		}
	}
}

// TestNoTagIsListedTwice catches a duplicate that would double-count changes.
//
// A tag appearing twice in the same list would be reported as two changes for one modification, which makes an audit trail
// overstate - the same failure the no-op-write tests guard from the other direction.
func TestNoTagIsListedTwice(t *testing.T) {
	for name, list := range map[string][]Tag{"emptied": emptied, "removed": removed, "dateTags": dateTags} {
		seen := make(map[Tag]bool, len(list))
		for _, tag := range list {
			if seen[tag] {
				t.Errorf("%s appears twice in %s, so one modification would be reported as two changes", tag, name)
			}
			seen[tag] = true
		}
	}
}
