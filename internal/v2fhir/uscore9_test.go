package v2fhir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The messages in testdata/uscdi are the ones the official HL7 validator checked against US Core 9.0.0 (docs/verification.md).
// This holds what that run established, without Java: which resources carry a US Core claim, and the conversions the validator
// found wrong staying fixed.
func usCoreFixture(t *testing.T, name string) *Result {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "uscdi", name))
	if err != nil {
		t.Fatal(err)
	}
	return convert(t, string(raw), Options{ClaimUSCore: true, DefaultIdentifierSystem: "http://springfield.example/fhir/id",
		AssigningAuthoritySystems: map[string]string{"SPRINGFIELD": "http://springfield.example/fhir/mrn"}})
}

func claims(r fhir.Resource) string {
	if m := r.ResourceMeta(); m != nil && len(m.Profile) > 0 {
		return m.Profile[0]
	}
	return ""
}

func TestEveryConformingResourceClaimsItsUSCoreProfile(t *testing.T) {
	want := map[string]map[string]string{
		"adt-a01.hl7": {"Patient": "us-core-patient", "Encounter": "us-core-encounter", "Practitioner": "us-core-practitioner",
			"Organization": "us-core-organization", "Location": "us-core-location",
			"Condition": "us-core-condition-encounter-diagnosis", "AllergyIntolerance": "us-core-allergyintolerance"},
		"oru-r01.hl7": {"Observation": "us-core-observation-lab", "DiagnosticReport": "us-core-diagnosticreport-lab", "Specimen": "us-core-specimen"},
		"vxu-v04.hl7": {"Immunization": "us-core-immunization"},
		"mdm-t02.hl7": {"DocumentReference": "us-core-documentreference"},
	}
	for file, types := range want {
		res := usCoreFixture(t, file)
		got := map[string]bool{}
		for _, e := range res.Bundle.Entry {
			typ := e.Resource.ResourceTypeName()
			if p, ok := types[typ]; ok {
				if !strings.HasSuffix(claims(e.Resource), "/"+p) {
					t.Errorf("%s: the %s claims %q, not %s", file, typ, claims(e.Resource), p)
				}
				got[typ] = true
			}
		}
		for typ := range types {
			if !got[typ] {
				t.Errorf("%s: no %s was produced", file, typ)
			}
		}
	}
}

// What the validator rejected on the first run, each held.
func TestTheUSCoreValidatorFindingsStayFixed(t *testing.T) {
	adt := usCoreFixture(t, "adt-a01.hl7")
	loc := find[*fhir.Location](t, adt)
	if loc.PartOf != nil || loc.ManagingOrganization == nil || !strings.HasPrefix(loc.ManagingOrganization.Reference, "Organization/") {
		t.Errorf("the facility is not the location's managing organization: partOf %v", loc.PartOf)
	}
	if org := find[*fhir.Organization](t, adt); org.Active == nil || !*org.Active {
		t.Error("the facility organization has no active, which US Core requires")
	}
	cond := find[*fhir.Condition](t, adt)
	if cond.OnsetDateTime != "2026-04-10" || cond.Code.Coding[0].System != "http://hl7.org/fhir/sid/icd-10-cm" || cond.Encounter == nil {
		t.Errorf("DG1 became %+v", cond)
	}
	allergy := find[*fhir.AllergyIntolerance](t, adt)
	if allergy.Criticality != "high" || allergy.Category[0] != "medication" || allergy.Reaction[0].Manifestation[0].Text != "Hives" ||
		allergy.ClinicalStatus == nil {
		t.Errorf("AL1 became %+v", allergy)
	}

	oru := usCoreFixture(t, "oru-r01.hl7")
	if s := find[*fhir.Specimen](t, oru); s.Type == nil || s.Type.Coding[0].Code != "119297000" {
		t.Errorf("the SPM specimen type was not used: %+v", s.Type)
	}
	// The sender's text is the concept's text, not a display LOINC would contradict.
	dr := find[*fhir.DiagnosticReport](t, oru)
	if dr.Code.Coding[0].Display != "" || dr.Code.Text != "Comprehensive metabolic panel" {
		t.Errorf("code %+v", dr.Code)
	}

	mdm := usCoreFixture(t, "mdm-t02.hl7")
	doc := find[*fhir.DocumentReference](t, mdm)
	if doc.Type.Coding[0].System != fhir.SystemLOINC || doc.Type.Coding[0].Code != "18842-5" || len(doc.Type.Coding) != 1 {
		t.Errorf("a LOINC TXA-2 was not kept as LOINC: %+v", doc.Type)
	}
	// The MDM's visit has no type, so its Encounter is not claimed - and says why.
	if e := find[*fhir.Encounter](t, mdm); claims(e) != "" {
		t.Error("an Encounter with no type claims US Core")
	}
}

func TestNPICheckDigit(t *testing.T) {
	for v, ok := range map[string]bool{"1234567893": true, "1234567890": false, "123456789": false, "12345678a3": false} {
		p := &fhir.Practitioner{Identifier: []fhir.Identifier{{System: "http://hl7.org/fhir/sid/us-npi", Value: v}},
			Name: []fhir.HumanName{{Family: "X"}}}
		got, _ := fhir.ConformsToProfile(p, "http://hl7.org/fhir/us/core/StructureDefinition/us-core-practitioner")
		if got != ok {
			t.Errorf("NPI %s: conforms %v", v, got)
		}
	}
}

// USCDI patient demographics the converter used to drop: race and ethnicity (CDC codes only), preferred language as BCP 47, and a
// phone number sent in the v2.5 split form.
func TestUSCDIDemographicsAreCarried(t *testing.T) {
	p := find[*fhir.Patient](t, usCoreFixture(t, "adt-a01.hl7"))
	var race, eth *fhir.Extension
	for i := range p.Extension {
		switch {
		case strings.HasSuffix(p.Extension[i].URL, "us-core-race"):
			race = &p.Extension[i]
		case strings.HasSuffix(p.Extension[i].URL, "us-core-ethnicity"):
			eth = &p.Extension[i]
		}
	}
	if race == nil || race.Extension[0].URL != "ombCategory" || race.Extension[0].ValueCoding.Code != "2106-3" ||
		*race.Extension[len(race.Extension)-1].ValueString != "White" {
		t.Errorf("race %+v", race)
	}
	if eth == nil || eth.Extension[0].ValueCoding.Code != "2186-5" {
		t.Errorf("ethnicity %+v", eth)
	}
	if len(p.Communication) != 1 || p.Communication[0].Language.Coding[0].Code != "en" ||
		p.Communication[0].Language.Coding[0].System != "urn:ietf:bcp:47" {
		t.Errorf("language %+v", p.Communication)
	}
	phones := map[string]bool{}
	for _, cp := range p.Telecom {
		phones[cp.System+" "+cp.Use+" "+cp.Value] = true
	}
	if !phones["phone home +1 217 555 0100"] || !phones["phone work +1 217 555 0199"] || !phones["email home alex@example.org"] {
		t.Errorf("telecom %v", phones)
	}
}
