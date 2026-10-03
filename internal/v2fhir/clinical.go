package v2fhir

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Diagnoses (DG1) and allergies (AL1), which USCDI calls Problems and Allergies and Intolerances. Mapped as the HL7 v2-to-FHIR
// implementation guide's segment maps do (DG1[Condition], AL1[AllergyIntolerance]).

const (
	systemConditionCategory = "http://terminology.hl7.org/CodeSystem/condition-category"
	systemConditionVer      = "http://terminology.hl7.org/CodeSystem/condition-ver-status"
	systemAllergyClinical   = "http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical"
)

// buildDiagnoses turns each DG1 into an encounter diagnosis.
func (c *converter) buildDiagnoses(patient, encounter *fhir.Reference) {
	for i := range c.msg.Segments("DG1") {
		n := i + 1
		p := fmt.Sprintf("DG1(%d)", n)
		code := c.codedValue(p+"-3", p+"-3")
		if code == nil {
			if text := c.get(p + "-4"); text != "" {
				code = &fhir.CodeableConcept{Text: text}
			}
		}
		if code == nil {
			c.note("warning", p+"-3", "Condition.code", "a diagnosis with no code or description was not converted")
			continue
		}
		cond := &fhir.Condition{
			Category: []fhir.CodeableConcept{*fhir.NewCodeableConcept(systemConditionCategory, "encounter-diagnosis", "Encounter Diagnosis")},
			Code:     code,
			Subject:  patient,
			// DG1-5, the diagnosis date, is the onset in the v2-to-FHIR map - USCDI v6's Date of Onset. DG1-19, attestation, is
			// when it was recorded.
			OnsetDateTime: c.v2DateTime(c.get(p+"-5"), p+"-5"),
			RecordedDate:  c.v2DateTime(c.get(p+"-19"), p+"-19"),
		}
		if encounter != nil {
			cond.Encounter = encounter
		}
		key := c.get(p+"-20.1") + "|" + c.get(p+"-3.1") + "|" + c.get(p+"-1")
		if id := c.get(p + "-20.1"); id != "" {
			cond.Identifier = []fhir.Identifier{{System: c.opts.DefaultIdentifierSystem, Value: id}}
		}
		if encounter != nil {
			key = encounter.Reference + "|" + key
		}
		cond.SetResourceID(c.deterministicID("Condition", key))
		if strings.EqualFold(c.get(p+"-21"), "D") {
			cond.VerificationStatus = fhir.NewCodeableConcept(systemConditionVer, "entered-in-error", "Entered in Error")
		}
		if prac := c.buildPractitioner(p + "-16"); prac != nil {
			c.addEntry(prac, "Practitioner", "")
		}
		c.addEntry(cond, "Condition", "")
	}
}

// allergenCategory is table 0127 to FHIR's category; every 0127 code is an allergy rather than an intolerance.
var allergenCategory = map[string]string{
	"DA": "medication", "FA": "food", "EA": "environment", "AA": "environment", "PA": "environment", "LA": "environment",
}

// buildAllergies turns each AL1 into an AllergyIntolerance.
func (c *converter) buildAllergies(patient *fhir.Reference) {
	for i := range c.msg.Segments("AL1") {
		n := i + 1
		p := fmt.Sprintf("AL1(%d)", n)
		code := c.codedValue(p+"-3", p+"-3")
		if code == nil {
			c.note("warning", p+"-3", "AllergyIntolerance.code", "an allergy with no allergen was not converted")
			continue
		}
		a := &fhir.AllergyIntolerance{
			// Active, as the v2-to-FHIR map assigns: an allergy listed on a current message is a current allergy, and FHIR
			// (ait-1) requires a clinical status on one that is not entered in error.
			ClinicalStatus: fhir.NewCodeableConcept(systemAllergyClinical, "active", "Active"),
			Code:           code,
			Patient:        patient,
			OnsetDateTime:  c.v2DateTime(c.get(p+"-6"), p+"-6"),
		}
		typ := strings.ToUpper(c.get(p + "-2.1"))
		if cat, ok := allergenCategory[typ]; ok {
			a.Category = []string{cat}
		}
		if typ != "" && typ != "MC" {
			a.Type = "allergy"
		}
		severity := strings.ToUpper(c.get(p + "-4.1"))
		switch severity {
		case "SV":
			a.Criticality = "high"
		case "MO", "MI":
			a.Criticality = "low"
		case "U":
			a.Criticality = "unable-to-assess"
		}
		var manifestations []fhir.CodeableConcept
		for _, rep := range c.msg.Segments("AL1")[i].Field(5).Repeats() {
			if text := strings.TrimSpace(rep.String()); text != "" {
				manifestations = append(manifestations, fhir.CodeableConcept{Text: text})
			}
		}
		if len(manifestations) > 0 {
			rx := fhir.AllergyReaction{Manifestation: manifestations}
			rx.Severity = map[string]string{"SV": "severe", "MO": "moderate", "MI": "mild"}[severity]
			a.Reaction = []fhir.AllergyReaction{rx}
		}
		a.SetResourceID(c.deterministicID("AllergyIntolerance", patient.Reference+"|"+c.get(p+"-3.1")+"|"+c.get(p+"-3.2")))
		c.addEntry(a, "AllergyIntolerance", "")
	}
}
