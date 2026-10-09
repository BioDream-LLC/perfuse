package fhir

import (
	"strconv"
	"strings"
)

// The rest of the US Core profiles the HL7 v2 converter can claim, transcribed from the US Core 9.0.0 snapshots - the version
// ONC's 2026 Standards Version Advancement Process approves for USCDI v6. Each checks the elements the profile makes
// required (min 1) and the error-level invariants that can be decided without a terminology server. Required bindings that need
// one are approximated by the code system - a DocumentReference type must be LOINC - and the official validator is the
// arbiter: docs/verification.md records the run against it.

const usCoreSource = "US Core 9.0.0, StructureDefinition-"

func usCoreRules(name, id, appliesTo string, check func(r Resource, res *ValidationResult)) ProfileRules {
	return ProfileRules{URL: "http://hl7.org/fhir/us/core/StructureDefinition/" + id, Name: name, Source: usCoreSource + id,
		AppliesTo: appliesTo, Check: check}
}

func need(res *ValidationResult, ok bool, path, rule, what string) {
	if !ok {
		res.add(Error, path, rule, "%s", what)
	}
}

func hasCoding(cc []CodeableConcept, system, code string) bool {
	for _, c := range cc {
		for _, x := range c.Coding {
			if x.System == system && (code == "" || x.Code == code) {
				return true
			}
		}
	}
	return false
}

func conceptPresent(c *CodeableConcept) bool {
	return c != nil && (len(c.Coding) > 0 || strings.TrimSpace(c.Text) != "")
}

func identifiersComplete(res *ValidationResult, ids []Identifier, path, rule string) {
	for i, id := range ids {
		need(res, strings.TrimSpace(id.System) != "", indexed(path, i)+".system", rule, "US Core requires a system on this identifier")
		need(res, strings.TrimSpace(id.Value) != "", indexed(path, i)+".value", rule, "US Core requires a value on this identifier")
	}
}

// npiValid is us-core-16 and us-core-17: ten digits with a valid Luhn check digit over the 80840 prefix.
func npiValid(v string) bool {
	if len(v) != 10 {
		return false
	}
	sum := 24 // the 80840 prefix's contribution
	for i := 0; i < 10; i++ {
		d, err := strconv.Atoi(v[i : i+1])
		if err != nil {
			return false
		}
		if i%2 == 0 && i < 9 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

const systemNPI = "http://hl7.org/fhir/sid/us-npi"

var usCoreMoreRules = []ProfileRules{
	usCoreRules("US Core Encounter", "us-core-encounter", "Encounter", func(r Resource, res *ValidationResult) {
		e := r.(*Encounter)
		identifiersComplete(res, e.Identifier, "Encounter.identifier", "us-core-encounter")
		need(res, len(e.Type) > 0, "Encounter.type", "us-core-encounter",
			"US Core requires an encounter type, and this Encounter has none")
		need(res, e.Subject != nil, "Encounter.subject", "us-core-encounter", "US Core requires the patient")
		need(res, len(e.Class) > 0, "Encounter.class", "us-core-encounter", "an Encounter requires a class")
	}),
	usCoreRules("US Core Laboratory Result Observation", "us-core-observation-lab", "Observation", func(r Resource, res *ValidationResult) {
		o := r.(*Observation)
		need(res, hasCoding(o.Category, "http://terminology.hl7.org/CodeSystem/observation-category", "laboratory"),
			"Observation.category", "us-core-observation-lab", "US Core requires the laboratory category")
		need(res, conceptPresent(o.Code), "Observation.code", "us-core-observation-lab", "US Core requires a code")
		need(res, o.Subject != nil, "Observation.subject", "us-core-observation-lab", "US Core requires the patient")
		for i, rr := range o.ReferenceRange {
			for _, q := range []*Quantity{rr.Low, rr.High} {
				need(res, q == nil || q.System == "" || q.System == SystemUCUM, indexed("Observation.referenceRange", i), "us-core-22",
					"US Core invariant us-core-22: a reference range quantity's system must be UCUM")
			}
		}
	}),
	usCoreRules("US Core DiagnosticReport for Laboratory Results Reporting", "us-core-diagnosticreport-lab", "DiagnosticReport",
		func(r Resource, res *ValidationResult) {
			d := r.(*DiagnosticReport)
			need(res, hasCoding(d.Category, "http://terminology.hl7.org/CodeSystem/v2-0074", "LAB"), "DiagnosticReport.category",
				"us-core-diagnosticreport-lab", "US Core requires the LAB category")
			need(res, conceptPresent(d.Code), "DiagnosticReport.code", "us-core-diagnosticreport-lab", "US Core requires a code")
			need(res, d.Subject != nil, "DiagnosticReport.subject", "us-core-diagnosticreport-lab", "US Core requires the patient")
			reported := map[string]bool{"partial": true, "preliminary": true, "final": true, "amended": true, "corrected": true, "appended": true}
			if reported[d.Status] {
				need(res, d.EffectiveDateTime != "" || d.EffectivePeriod != nil, "DiagnosticReport.effective", "us-core-8",
					"US Core invariant us-core-8: a reported result needs its clinically relevant time")
				need(res, d.Issued != "", "DiagnosticReport.issued", "us-core-9",
					"US Core invariant us-core-9: a reported result needs the time it was issued")
			}
		}),
	usCoreRules("US Core Practitioner", "us-core-practitioner", "Practitioner", func(r Resource, res *ValidationResult) {
		p := r.(*Practitioner)
		need(res, len(p.Identifier) > 0, "Practitioner.identifier", "us-core-practitioner", "US Core requires an identifier, such as the NPI")
		identifiersComplete(res, p.Identifier, "Practitioner.identifier", "us-core-practitioner")
		for i, id := range p.Identifier {
			if id.System == systemNPI {
				need(res, npiValid(id.Value), indexed("Practitioner.identifier", i), "us-core-17",
					"US Core invariants us-core-16 and us-core-17: an NPI is ten digits with a valid check digit")
			}
		}
		need(res, len(p.Name) > 0, "Practitioner.name", "us-core-practitioner", "US Core requires a name")
		for i, n := range p.Name {
			need(res, strings.TrimSpace(n.Family) != "", indexed("Practitioner.name", i)+".family", "us-core-practitioner",
				"US Core requires a family name")
		}
	}),
	usCoreRules("US Core Organization", "us-core-organization", "Organization", func(r Resource, res *ValidationResult) {
		o := r.(*Organization)
		need(res, o.Active != nil, "Organization.active", "us-core-organization", "US Core requires active")
		need(res, strings.TrimSpace(o.Name) != "", "Organization.name", "us-core-organization", "US Core requires a name")
		for i, id := range o.Identifier {
			if id.System == systemNPI {
				need(res, npiValid(id.Value), indexed("Organization.identifier", i), "us-core-17",
					"US Core invariants us-core-16 and us-core-17: an NPI is ten digits with a valid check digit")
			}
		}
	}),
	usCoreRules("US Core Location", "us-core-location", "Location", func(r Resource, res *ValidationResult) {
		l := r.(*Location)
		need(res, strings.TrimSpace(l.Name) != "", "Location.name", "us-core-location", "US Core requires a name")
		for i, t := range l.Type {
			need(res, len(t.Coding) > 0, indexed("Location.type", i), "us-core-location", "US Core requires a coded type when there is one")
			for j, c := range t.Coding {
				need(res, c.System != "" && c.Code != "", indexed(indexed("Location.type", i)+".coding", j), "us-core-location",
					"US Core requires a system and code on a type coding")
			}
		}
		need(res, l.PartOf == nil || strings.HasPrefix(l.PartOf.Reference, "Location/"), "Location.partOf", "location-partof",
			"partOf names another Location; an Organization belongs in managingOrganization")
	}),
	usCoreRules("US Core Specimen", "us-core-specimen", "Specimen", func(r Resource, res *ValidationResult) {
		s := r.(*Specimen)
		need(res, conceptPresent(s.Type), "Specimen.type", "us-core-specimen", "US Core requires the specimen type")
	}),
	usCoreRules("US Core Immunization", "us-core-immunization", "Immunization", func(r Resource, res *ValidationResult) {
		im := r.(*Immunization)
		need(res, im.Status != "", "Immunization.status", "us-core-immunization", "an Immunization requires a status")
		need(res, conceptPresent(im.VaccineCode), "Immunization.vaccineCode", "us-core-immunization", "US Core requires the vaccine")
		need(res, im.Patient != nil, "Immunization.patient", "us-core-immunization", "US Core requires the patient")
		need(res, im.OccurrenceDateTime != "" || im.OccurrenceString != "", "Immunization.occurrence[x]", "us-core-immunization",
			"US Core requires when it was given")
	}),
	usCoreRules("US Core DocumentReference", "us-core-documentreference", "DocumentReference", func(r Resource, res *ValidationResult) {
		d := r.(*DocumentReference)
		// A required binding to US Core's document types: LOINC document codes. A code from another system cannot be in it.
		need(res, d.Type != nil && hasCoding([]CodeableConcept{*d.Type}, SystemLOINC, ""), "DocumentReference.type", "us-core-documentreference",
			"US Core requires a LOINC document type, and a v2 table 0270 code alone is not one")
		need(res, len(d.Category) > 0, "DocumentReference.category", "us-core-documentreference", "US Core requires a category")
		need(res, d.Subject != nil, "DocumentReference.subject", "us-core-documentreference", "US Core requires the patient")
		need(res, len(d.Content) > 0, "DocumentReference.content", "us-core-documentreference", "a DocumentReference requires content")
		for i, c := range d.Content {
			// url.exists() or data.exists(): an element holding only an extension (a data absent reason) exists.
			a := c.Attachment
			need(res, a != nil && (a.URL != "" || a.Data != "" || a.URLElement != nil || a.DataElement != nil), indexed("DocumentReference.content", i),
				"us-core-6", "US Core invariant: an attachment carries its data or a URL to it")
		}
	}),
	usCoreRules("US Core Condition Encounter Diagnosis", "us-core-condition-encounter-diagnosis", "Condition",
		func(r Resource, res *ValidationResult) {
			c := r.(*Condition)
			need(res, hasCoding(c.Category, "http://terminology.hl7.org/CodeSystem/condition-category", "encounter-diagnosis"),
				"Condition.category", "us-core-condition-encounter-diagnosis", "US Core requires the encounter-diagnosis category")
			need(res, conceptPresent(c.Code), "Condition.code", "us-core-condition-encounter-diagnosis", "US Core requires a code")
			need(res, c.Subject != nil, "Condition.subject", "us-core-condition-encounter-diagnosis", "US Core requires the patient")
		}),
	usCoreRules("US Core AllergyIntolerance", "us-core-allergyintolerance", "AllergyIntolerance", func(r Resource, res *ValidationResult) {
		a := r.(*AllergyIntolerance)
		need(res, conceptPresent(a.Code), "AllergyIntolerance.code", "us-core-allergyintolerance", "US Core requires the substance")
		need(res, a.Patient != nil, "AllergyIntolerance.patient", "us-core-allergyintolerance", "US Core requires the patient")
		for i, rx := range a.Reaction {
			need(res, len(rx.Manifestation) > 0, indexed("AllergyIntolerance.reaction", i)+".manifestation", "us-core-allergyintolerance",
				"a reaction requires a manifestation")
		}
	}),
}

func init() {
	for _, r := range usCoreMoreRules {
		profileRules[r.URL] = r
	}
}
