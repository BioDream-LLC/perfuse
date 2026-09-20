package fhir

import (
	"strings"
	"testing"
)

// conformantPatient is the minimum US Core Patient: one identifier with a system and a value, and one name with a family.
func conformantPatient() *Patient {
	p := &Patient{
		Identifier: []Identifier{{System: "http://sitea.example.org/mrn", Value: "MRN001"}},
		Name:       []HumanName{{Family: "Evrard", Given: []string{"Camille"}}},
	}
	p.SetResourceID("p1")
	p.SetResourceMeta(&Meta{Profile: []string{usCorePatientRules.URL}})

	return p
}

// A claim that holds must pass cleanly.
func TestAConformantPatientPassesItsProfileClaim(t *testing.T) {
	res := Validate(conformantPatient(), ResourceShapeVersion)

	errs, _, _ := res.Counts()
	if errs != 0 {
		t.Fatalf("a conformant US Core Patient reported %d error(s): %s", errs, messages(res))
	}
	if res.Profile == "" {
		t.Error("the result does not record which profile was checked, so a caller cannot say what it verified")
	}
}

// US Core Patient does NOT require gender, and this test exists because I believed it did.
//
// From memory I had gender as mandatory. The published profile has it 0..1 - the four mandatory elements are identifier and name plus
// the nested identifier.system and identifier.value. Encoding the wrong cardinality would have rejected conformant resources, and a
// validator that reports false errors is worse than no validator: the feed stops, and the message blames the sender for something
// they did correctly.
func TestUSCorePatientDoesNotRequireGender(t *testing.T) {
	p := conformantPatient()
	p.Gender = ""

	res := Validate(p, ResourceShapeVersion)
	if errs, _, _ := res.Counts(); errs != 0 {
		t.Fatalf("a Patient with no gender was rejected, but US Core has gender at 0..1: %s", messages(res))
	}

	// Nor birthDate, which is Must Support rather than mandatory.
	p.BirthDate = ""
	if res := Validate(p, ResourceShapeVersion); !res.Valid() {
		t.Errorf("a Patient with no birth date was rejected, but birthDate is must-support, not mandatory: %s", messages(res))
	}
}

// The cases US Core does require.
func TestAFalseUSCoreClaimIsRejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *Patient)
		expect string
	}{
		{
			name:   "no identifier at all",
			mutate: func(p *Patient) { p.Identifier = nil },
			expect: "identifier",
		},
		{
			name:   "an identifier with no system has no namespace",
			mutate: func(p *Patient) { p.Identifier = []Identifier{{Value: "MRN001"}} },
			expect: "system",
		},
		{
			name:   "an identifier with no value",
			mutate: func(p *Patient) { p.Identifier = []Identifier{{System: "http://sitea.example.org/mrn"}} },
			expect: "value",
		},
		{
			name:   "no name at all",
			mutate: func(p *Patient) { p.Name = nil },
			expect: "name",
		},
		{
			name:   "a name with neither family nor given",
			mutate: func(p *Patient) { p.Name = []HumanName{{Use: "official"}} },
			expect: "us-core-6",
		},
		{
			name:   "a name whose given entries are blank strings",
			mutate: func(p *Patient) { p.Name = []HumanName{{Given: []string{"", "  "}}} },
			expect: "us-core-6",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := conformantPatient()
			tc.mutate(p)

			res := Validate(p, ResourceShapeVersion)
			if errs, _, _ := res.Counts(); errs == 0 {
				t.Fatalf("this Patient claims US Core conformance and does not hold, but validation passed. "+
					"A claim nobody checks is worse than no claim: %s", messages(res))
			}
			if !strings.Contains(messages(res), tc.expect) {
				t.Errorf("the finding does not mention %q, so it does not say what to fix: %s", tc.expect, messages(res))
			}
		})
	}
}

// An unidentified patient is legitimate, and us-core-6 names the way to say so.
func TestANameMayBeAbsentForAStatedReason(t *testing.T) {
	p := conformantPatient()
	p.Name = []HumanName{{
		Extension: []Extension{{URL: "http://hl7.org/fhir/StructureDefinition/data-absent-reason"}},
	}}

	res := Validate(p, ResourceShapeVersion)
	if errs, _, _ := res.Counts(); errs != 0 {
		t.Errorf("a name carrying the data absent reason extension was rejected, but us-core-6 permits it exactly so that "+
			"an unidentified patient can be represented: %s", messages(res))
	}
}

// A claim this build cannot check must be reported as unchecked, never passed quietly.
//
// This is the whole point. If an unknown profile passes in silence, a resource can assert conformance to anything at all and the
// server will agree with it.
func TestAnUnknownProfileClaimIsReportedAsUnverified(t *testing.T) {
	p := conformantPatient()
	p.SetResourceMeta(&Meta{Profile: []string{"http://example.org/StructureDefinition/something-we-never-heard-of"}})

	res := Validate(p, ResourceShapeVersion)

	_, warns, _ := res.Counts()
	if warns == 0 {
		t.Fatalf("a profile this build cannot check passed with nothing said, so any resource can claim anything: %s",
			messages(res))
	}
	if !strings.Contains(messages(res), "cannot check") {
		t.Errorf("the warning does not say the claim was not checked: %s", messages(res))
	}

	// It must not be reported as an error either. The resource may well conform; we simply do not know, and refusing a write over a
	// profile we do not stock would make us useless to anyone using their own profiles.
	if errs, _, _ := res.Counts(); errs != 0 {
		t.Errorf("an unverifiable claim was treated as invalid, which would refuse resources that are probably fine: %s",
			messages(res))
	}
}

// A profile claimed on the wrong resource type is a plain mistake.
func TestAProfileClaimedOnTheWrongTypeIsAnError(t *testing.T) {
	o := &Observation{Status: "final"}
	o.SetResourceID("o1")
	o.SetResourceMeta(&Meta{Profile: []string{usCorePatientRules.URL}})

	res := Validate(o, ResourceShapeVersion)
	if !strings.Contains(messages(res), "constrains Patient") {
		t.Errorf("an Observation claiming the Patient profile was not called out: %s", messages(res))
	}
}

// ConformsToProfile must not answer "yes" when it means "I do not know".
func TestConformsToProfileSaysNoWhenItCannotCheck(t *testing.T) {
	ok, res := ConformsToProfile(conformantPatient(), "http://example.org/StructureDefinition/unknown")
	if ok {
		t.Fatal("ConformsToProfile returned true for a profile it cannot check. " +
			"\"I could not check\" and \"it passes\" must not be the same answer, or the claim it gates is meaningless")
	}
	if len(res.Findings) == 0 {
		t.Error("no explanation was given for the refusal")
	}
}

// KnownProfiles must be sorted, since it is user-visible and Go maps range randomly.
func TestKnownProfilesIsSorted(t *testing.T) {
	got := KnownProfiles()
	if len(got) == 0 {
		t.Fatal("no profiles are checkable, so the claim path has nothing behind it")
	}

	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("KnownProfiles is not sorted: %q came before %q", got[i-1], got[i])

			break
		}
	}
}

// messages flattens findings for a failure message.
func messages(res *ValidationResult) string {
	parts := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		parts = append(parts, string(f.Severity)+" "+f.Path+": "+f.Message)
	}

	return strings.Join(parts, " | ")
}
