package hl7v3

import (
	"testing"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// attrDoc is the shape almost every real v3 or CDA document takes: the values are in attributes, not in element text.
const attrDoc = `<ClinicalDocument xmlns="urn:hl7-org:v3"><recordTarget><patientRole><patient>
<birthTime value="19551014"/><administrativeGenderCode code="F"/><family>Okonkwo</family>
</patient></patientRole></recordTarget></ClinicalDocument>`

// TestEveryOperatorReadsAValueInAnAttribute is a regression test for a bug the old evaluator had.
//
// In v3 a value lives in an attribute. <birthTime value="19551014"/> has no element text at all. The evaluator this
// replaced had exactly one operator that knew this - empty - and its comment recorded the mistake as "the mistake v3
// invites". The fix was never carried across to the others.
//
// So //birthTime empty correctly reported that a value was present, while //birthTime == "19551014" could not match it.
// Every comparison against a v3 attribute value silently answered false. For a filter that means every message excluded or
// every message let through, with nothing in the log to say why.
//
// Proven against the old implementation before it was changed: the first two cases here failed.
func TestEveryOperatorReadsAValueInAnAttribute(t *testing.T) {
	root, err := xtree.Parse([]byte(attrDoc))
	if err != nil {
		t.Fatal(err)
	}

	for _, src := range []string{
		`//birthTime == "19551014"`,
		`//administrativeGenderCode == "F"`,
		`//birthTime in ("19551014")`,
		`//birthTime matches "1955"`,
		`//birthTime < "19600101"`,
		`//birthTime >= "19551014"`,
		`//administrativeGenderCode != "M"`,
		`not //birthTime empty`,
		// And element text still works, so the fallback did not replace the ordinary reading.
		`//family == "Okonkwo"`,
		// An explicit attribute is taken exactly as written.
		`//birthTime@value == "19551014"`,
	} {
		f, err := ParseFilter(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		ok, err := f.Match(root)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if !ok {
			t.Errorf("%s: did not match, but the document carries that value", src)
		}
	}
}

// TestBothListStylesParse covers the one syntax difference between the two grammars that reached users.
//
// The v2 filter documented in ["A", "B"] and the v3 filter documented in ("A", "B"). Unifying them onto one parser must
// not stop parsing filters written against either.
func TestBothListStylesParse(t *testing.T) {
	root, err := xtree.Parse([]byte(attrDoc))
	if err != nil {
		t.Fatal(err)
	}

	for _, src := range []string{
		`//administrativeGenderCode in ("F", "M")`,
		`//administrativeGenderCode in ["F", "M"]`,
	} {
		f, err := ParseFilter(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if ok, _ := f.Match(root); !ok {
			t.Errorf("%s: did not match", src)
		}
	}

	// A mismatched pair is still refused, so accepting both styles did not make the grammar sloppy.
	for _, bad := range []string{
		`//administrativeGenderCode in ("F"]`,
		`//administrativeGenderCode in ["F")`,
	} {
		if _, err := ParseFilter(bad); err == nil {
			t.Errorf("ParseFilter(%q) was accepted", bad)
		}
	}
}
