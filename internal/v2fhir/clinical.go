package v2fhir

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/fhir"
)

// Diagnoses (DG1) and allergies (AL1), which USCDI calls Problems and Allergies and Intolerances. Mapped as the HL7 v2-to-FHIR
// implementation guide's segment maps do (DG1[Condition], AL1[AllergyIntolerance]).

const (
	systemConditionCategory = "http://terminology.hl7.org/CodeSystem/condition-category"
	systemConditionVer      = "http://terminology.hl7.org/CodeSystem/condition-ver-status"
	systemAllergyClinical   = "http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical"
)

// diagnosisRole is HL7 table 0052 (DG1-6) to FHIR's diagnosis-role, as the v2-to-FHIR table map has it: only admitting maps.
// Working and final have no diagnosis-role equivalent there, so they are left without a use rather than guessed at (a final
// diagnosis on a discharge is not necessarily the discharge diagnosis a receiver means by DD).
var diagnosisRole = map[string][2]string{"A": {"AD", "Admission diagnosis"}}

// buildDiagnoses turns each DG1 into an encounter diagnosis: a Condition, and an entry in the visit's Encounter.diagnosis
// naming it, with DG1-6's use (the v2-to-FHIR DG1 to Encounter map).
func (c *converter) buildDiagnoses(patient, encounter *fhir.Reference, enc *fhir.Encounter) {
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
		if enc != nil {
			d := fhir.EncounterDiagnosis{Condition: []fhir.CodeableReference{{Reference: fhir.Ref("Condition", cond.ID)}}}
			if role, ok := diagnosisRole[strings.ToUpper(c.get(p+"-6"))]; ok {
				d.Use = []fhir.CodeableConcept{*fhir.NewCodeableConcept("http://terminology.hl7.org/CodeSystem/diagnosis-role", role[0], role[1])}
			}
			enc.Diagnosis = append(enc.Diagnosis, d)
		}
	}
}

// procedureFunction is the role PR1-8 and PR1-11 give a performer, SNOMED CT codes the v2-to-FHIR PR1 map assigns.
var procedureFunction = map[string][2]string{
	"8":  {"88189002", "Anesthesiologist"},
	"11": {"304292004", "Surgeon"},
}

// opsCategory is HL7 Deutschland's ConceptMap OPS-SNOMED-Category (http://fhir.de/ConceptMap/OPS-SNOMED-Category): an OPS
// code's chapter, its first character, to a SNOMED CT procedure category. Chapter 1 is written as ISiK's ProzedurenKategorieSCT
// value set has it (165197003, Diagnostic assessment), which the ISiK profile binds; Basis DE 1.2.0's copy of the map said
// 103693007. OPS has no chapters 2, 4 or 7.
var opsCategory = map[string][2]string{
	"1": {"165197003", "Diagnostic assessment"},
	"3": {"363679005", "Imaging"},
	"5": {"387713003", "Surgical procedure"},
	"6": {"18629005", "Administration of medicine"},
	"8": {"277132007", "Therapeutic procedure"},
	"9": {"394841004", "Other category"},
}

// procedureFunctionalType is HL7 table 0230 (PR1-6).
var procedureFunctionalType = map[string]string{"A": "Anesthesia", "P": "Procedure for treatment (therapeutic, including operations)",
	"I": "Invasive procedure not classified elsewhere (I.V., catheter, etc.)", "D": "Diagnostic procedure"}

// buildProcedures turns each PR1 into a Procedure (the v2-to-FHIR PR1 to Procedure map).
//
// Its status is unknown: the map leaves it to the message's context, and a PR1 on an ADT says what was done or planned without
// saying which, so the map's own fallback is used rather than assuming the procedure was completed.
func (c *converter) buildProcedures(patient, encounter *fhir.Reference) {
	for i := range c.msg.Segments("PR1") {
		p := fmt.Sprintf("PR1(%d)", i+1)
		code := c.codedValue(p+"-3", p+"-3")
		if code == nil {
			if text := c.get(p + "-4"); text != "" {
				code = &fhir.CodeableConcept{Text: text}
			}
		}
		if code == nil {
			c.note("warning", p+"-3", "Procedure.code", "a procedure with no code or description was not converted")
			continue
		}
		proc := &fhir.Procedure{
			Status:            "unknown",
			Code:              code,
			Subject:           patient,
			Encounter:         encounter,
			PerformedDateTime: c.v2DateTime(c.get(p+"-5"), p+"-5"),
		}
		if t := strings.ToUpper(c.get(p + "-6")); t != "" {
			proc.Category = fhir.NewCodeableConcept("http://terminology.hl7.org/CodeSystem/v2-0230", t, procedureFunctionalType[t])
		}
		// An OPS code's chapter is its kind of procedure: HL7 Deutschland's ConceptMap OPS-SNOMED-Category maps the first
		// character to a SNOMED CT category, which ISiK requires on every OPS-coded procedure (proc-ISiK-1).
		for _, cd := range code.Coding {
			if cd.System == "http://fhir.de/CodeSystem/bfarm/ops" && cd.Code != "" {
				if cat, ok := opsCategory[cd.Code[:1]]; ok {
					// One category in R4, so the SNOMED CT coding joins table 0230's when PR1-6 gave one.
					sct := fhir.Coding{System: "http://snomed.info/sct", Code: cat[0], Display: cat[1]}
					if proc.Category == nil {
						proc.Category = fhir.NewCodeableConcept(sct.System, sct.Code, sct.Display)
					} else {
						proc.Category.Coding = append(proc.Category.Coding, sct)
					}
					c.note("info", p+"-3", "Procedure.category", "OPS chapter %s is the SNOMED CT category %s (%s), by HL7 Deutschland's OPS-SNOMED-Category map",
						cd.Code[:1], cat[0], cat[1])
				}
				break
			}
		}
		for _, field := range []string{"8", "11", "12"} {
			for r := 1; r <= c.repeatCount(p+"-"+field); r++ {
				path := fmt.Sprintf("%s-%s(%d)", p, field, r)
				prac := c.buildPractitioner(path)
				if prac == nil {
					continue
				}
				c.addEntry(prac, "Practitioner", "")
				perf := fhir.ProcedurePerformer{Actor: fhir.Ref("Practitioner", prac.ID)}
				if fn, ok := procedureFunction[field]; ok {
					perf.Function = fhir.NewCodeableConcept("http://snomed.info/sct", fn[0], fn[1])
				}
				proc.Performer = append(proc.Performer, perf)
			}
		}
		if reason := c.codedValue(p+"-15", p+"-15"); reason != nil {
			proc.ReasonCode = []fhir.CodeableConcept{*reason}
		}
		key := c.get(p+"-19.1") + "|" + c.get(p+"-3.1") + "|" + c.get(p+"-5") + "|" + c.get(p+"-1")
		if id := c.get(p + "-19.1"); id != "" {
			proc.Identifier = []fhir.Identifier{{System: c.opts.DefaultIdentifierSystem, Value: id}}
		}
		if encounter != nil {
			key = encounter.Reference + "|" + key
		}
		proc.SetResourceID(c.deterministicID("Procedure", key))
		c.addEntry(proc, "Procedure", "")
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
