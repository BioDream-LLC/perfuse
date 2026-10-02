package cda

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Conversion to FHIR.
//
// The same rule that governs the HL7 v2 mapper governs this one: a value that
// cannot be mapped is preserved as text and never guessed at. It matters more here,
// because a CDA carries codes from half a dozen systems and the temptation to
// translate between them is constant. A SNOMED problem code turned into an
// approximate ICD-10 code is worse than an untranslated one, because the receiver
// believes it.
//
// Two additional decisions specific to documents:
//
// Negation is carried through explicitly. A CDA entry can assert the absence of
// something, and FHIR expresses that differently for each resource type. Dropping
// the negation would turn "no penicillin allergy" into "penicillin allergy", which
// is the most dangerous single error available in this conversion, so an entry
// whose negation cannot be represented is refused rather than emitted positively.
//
// The document itself becomes a DocumentReference holding the original bytes. A
// conversion that produced only the extracted resources would throw away the
// legal record, which is the one artefact a hospital is required to retain.

// FHIRResult is the outcome of converting a document.
type FHIRResult struct {
	// Bundle is a transaction bundle of conditional upserts, so re-sending the
	// same document updates rather than duplicating.
	Bundle map[string]any `json:"bundle"`

	// Counts is how many of each resource type were produced.
	Counts map[string]int `json:"counts"`

	// Notes explain every judgement the conversion made.
	Notes []Note `json:"notes"`
}

// FHIROptions configures the conversion.
type FHIROptions struct {
	// Version is "R4", "R4B" or "R5".
	Version string

	// IdentifierSystems maps an OID root to a URI, so that an MRN becomes
	// namespaced. Without one the identifier is emitted with an OID-derived
	// system rather than being dropped, because losing the identifier makes the
	// patient unmatchable.
	IdentifierSystems map[string]string

	// Original is the document bytes, attached to the DocumentReference. When
	// absent the DocumentReference records only metadata and says so.
	Original []byte
}

// ToFHIR converts a document.
func (d *Document) ToFHIR(opts FHIROptions) (*FHIRResult, error) {
	if d == nil {
		return nil, fmt.Errorf("cda: no document")
	}
	version := strings.ToUpper(strings.TrimSpace(opts.Version))
	if version == "" {
		version = "R5"
	}
	switch version {
	case "R4", "R4B", "R5":
	case "R6":
		return nil, fmt.Errorf("cda: FHIR R6 is still in ballot and is not published; use R5, or R4 for a system that requires it")
	default:
		return nil, fmt.Errorf("cda: unknown FHIR version %q; expected R4, R4B or R5", version)
	}

	result := &FHIRResult{Counts: map[string]int{}}
	var entries []map[string]any

	patientID := d.patientID()
	patient, patientNotes := d.patientResource(opts, patientID)
	result.Notes = append(result.Notes, patientNotes...)
	entries = append(entries, upsert("Patient", patientID, patient, patientMatch(d, opts)))
	result.Counts["Patient"]++

	patientRef := map[string]any{"reference": "Patient/" + patientID}

	// Allergies.
	if section := d.SectionByKind("Allergies"); section != nil {
		// Top-level entries only. An allergy is an act wrapping an observation
		// wrapping a reaction, and flattening it would emit three
		// AllergyIntolerance resources for one allergy.
		for i, entry := range section.Entries {
			resource, notes, ok := allergyResource(entry, patientRef, version, patientID, i)
			result.Notes = append(result.Notes, notes...)
			if !ok {
				continue
			}
			id := deterministicID(patientID, "allergy", entry.Code, entry.ValueCode, fmt.Sprint(i))
			entries = append(entries, upsert("AllergyIntolerance", id, resource, "identifier=urn:perfuse:cda|"+id))
			result.Counts["AllergyIntolerance"]++
		}
	}

	// Problems.
	if section := d.SectionByKind("Problems"); section != nil {
		// Top-level only, for the same reason as allergies: the act and the
		// observation inside it are one problem, not two.
		for i, entry := range section.Entries {
			resource, notes, ok := conditionResource(entry, patientRef, patientID, i)
			result.Notes = append(result.Notes, notes...)
			if !ok {
				continue
			}
			id := deterministicID(patientID, "condition", entry.ValueCode, entry.Code, fmt.Sprint(i))
			entries = append(entries, upsert("Condition", id, resource, "identifier=urn:perfuse:cda|"+id))
			result.Counts["Condition"]++
		}
	}

	// Medications.
	for _, kind := range []string{"Medications", "Discharge medications"} {
		section := d.SectionByKind(kind)
		if section == nil {
			continue
		}
		for i, entry := range section.Entries {
			if entry.Kind != "SubstanceAdministration" {
				continue
			}
			resource, notes, ok := medicationResource(entry, patientRef, patientID, kind, i)
			result.Notes = append(result.Notes, notes...)
			if !ok {
				continue
			}
			id := deterministicID(patientID, "medication", kind, entry.Code, fmt.Sprint(i))
			entries = append(entries, upsert("MedicationStatement", id, resource, "identifier=urn:perfuse:cda|"+id))
			result.Counts["MedicationStatement"]++
		}
	}

	// Results and vital signs both become Observations.
	for _, kind := range []string{"Results", "Vital signs"} {
		section := d.SectionByKind(kind)
		if section == nil {
			continue
		}
		category := "laboratory"
		if kind == "Vital signs" {
			category = "vital-signs"
		}
		// Results are the one place flattening is right: an organiser is a panel
		// and the observations inside it are the individual results, each of which
		// becomes its own resource.
		for i, entry := range flatten(section.Entries) {
			if entry.Kind != "Observation" {
				continue
			}
			resource, notes, ok := observationResource(entry, patientRef, category, patientID, i)
			result.Notes = append(result.Notes, notes...)
			if !ok {
				continue
			}
			id := deterministicID(patientID, "observation", kind, entry.Code, entry.EffectiveTime, fmt.Sprint(i))
			entries = append(entries, upsert("Observation", id, resource, "identifier=urn:perfuse:cda|"+id))
			result.Counts["Observation"]++
		}
	}

	// Procedures.
	if section := d.SectionByKind("Procedures"); section != nil {
		for i, entry := range section.Entries {
			if entry.Kind != "Procedure" {
				continue
			}
			resource, notes, ok := procedureResource(entry, patientRef, patientID, i)
			result.Notes = append(result.Notes, notes...)
			if !ok {
				continue
			}
			id := deterministicID(patientID, "procedure", entry.Code, fmt.Sprint(i))
			entries = append(entries, upsert("Procedure", id, resource, "identifier=urn:perfuse:cda|"+id))
			result.Counts["Procedure"]++
		}
	}

	// The document itself. This goes last so that a reader of the bundle meets the
	// clinical content first, but it is the resource a hospital cares most about
	// keeping.
	docID := deterministicID(patientID, "document", d.ID, d.SetID)
	docRef, docNotes := d.documentReference(opts, patientRef, docID, version)
	result.Notes = append(result.Notes, docNotes...)
	entries = append(entries, upsert("DocumentReference", docID, docRef, "identifier=urn:perfuse:cda|"+docID))
	result.Counts["DocumentReference"]++

	result.Bundle = map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry":        entries,
	}
	return result, nil
}

func (d *Document) patientID() string {
	// A namespaced identifier gives a stable id across documents, which is what
	// makes re-sending a document an update rather than a duplicate.
	for _, id := range d.Patient.Identifiers {
		if id.Extension != "" {
			return deterministicID("patient", id.Root, id.Extension)
		}
	}
	// Without an identifier, fall back to the demographics. This is weaker and the
	// conversion says so in a note rather than pretending otherwise.
	return deterministicID("patient", d.Patient.Family, strings.Join(d.Patient.Given, " "), d.Patient.BirthTime)
}

func patientMatch(d *Document, opts FHIROptions) string {
	for _, id := range d.Patient.Identifiers {
		if id.Extension == "" {
			continue
		}
		system := identifierSystem(id.Root, opts)
		return "identifier=" + system + "|" + id.Extension
	}
	return ""
}

func identifierSystem(root string, opts FHIROptions) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return "urn:perfuse:unknown-authority"
	}
	if mapped, ok := opts.IdentifierSystems[root]; ok && mapped != "" {
		return mapped
	}
	// An OID has a standard URI form. Using it keeps the identifier usable and
	// unambiguous even when nobody configured a friendly system.
	return "urn:oid:" + root
}

func (d *Document) patientResource(opts FHIROptions, id string) (map[string]any, []Note) {
	var notes []Note

	patient := map[string]any{
		"resourceType": "Patient",
		"id":           id,
	}

	var identifiers []map[string]any
	for _, ident := range d.Patient.Identifiers {
		if ident.Extension == "" {
			continue
		}
		system := identifierSystem(ident.Root, opts)
		if strings.HasPrefix(system, "urn:oid:") {
			if _, configured := opts.IdentifierSystems[ident.Root]; !configured {
				notes = append(notes, Note{
					Severity: "warning",
					Path:     "Patient.identifier",
					Message: fmt.Sprintf("the identifier %q uses the assigning authority %s, which is not configured; "+
						"it was emitted as %s rather than dropped, but a receiving system will not recognise it",
						ident.Extension, ident.Root, system),
					Rule: "identifier-system-unmapped",
				})
			}
		}
		identifiers = append(identifiers, map[string]any{
			"system": system,
			"value":  ident.Extension,
		})
	}
	if len(identifiers) == 0 {
		notes = append(notes, Note{
			Severity: "error",
			Path:     "Patient.identifier",
			Message: "the document carries no patient identifier, so the resource id was derived from name and date of birth. " +
				"Two patients with the same name and birthday would collide",
			Rule: "identifier-present",
		})
	} else {
		patient["identifier"] = identifiers
	}

	if d.Patient.Family != "" || len(d.Patient.Given) > 0 {
		name := map[string]any{"use": "official"}
		if d.Patient.Family != "" {
			name["family"] = d.Patient.Family
		}
		if len(d.Patient.Given) > 0 {
			name["given"] = d.Patient.Given
		}
		if d.Patient.Prefix != "" {
			name["prefix"] = []string{d.Patient.Prefix}
		}
		if d.Patient.Suffix != "" {
			name["suffix"] = []string{d.Patient.Suffix}
		}
		patient["name"] = []map[string]any{name}
	}

	switch strings.ToUpper(d.Patient.Gender) {
	case "M":
		patient["gender"] = "male"
	case "F":
		patient["gender"] = "female"
	case "UN":
		patient["gender"] = "other"
	case "":
	default:
		// An unrecognised gender code is recorded as unknown with a note rather
		// than guessed, because guessing wrong on this field is both visible and
		// offensive.
		patient["gender"] = "unknown"
		notes = append(notes, Note{
			Severity: "warning",
			Path:     "Patient.gender",
			Message:  fmt.Sprintf("the gender code %q is not one of the HL7 administrative values; it was recorded as unknown", d.Patient.Gender),
			Rule:     "gender-code-unknown",
		})
	}

	date, dateNote := fhirDate(d.Patient.BirthTime, "Patient.birthDate")
	if dateNote != nil {
		notes = append(notes, *dateNote)
	}
	if date != "" {
		patient["birthDate"] = date
	}

	if addr := d.Patient.Address; addr != nil {
		out := map[string]any{}
		if len(addr.Lines) > 0 {
			out["line"] = addr.Lines
		}
		for key, value := range map[string]string{
			"city": addr.City, "state": addr.State, "postalCode": addr.Postal, "country": addr.Country,
		} {
			if value != "" {
				out[key] = value
			}
		}
		if use := addressUse(addr.Use); use != "" {
			out["use"] = use
		}
		if len(out) > 0 {
			patient["address"] = []map[string]any{out}
		}
	}

	if d.Patient.Phone != "" {
		patient["telecom"] = []map[string]any{{"system": "phone", "value": d.Patient.Phone}}
	}

	return patient, notes
}

func addressUse(use string) string {
	switch strings.ToUpper(strings.TrimSpace(use)) {
	case "H", "HP", "HV":
		return "home"
	case "WP":
		return "work"
	case "TMP":
		return "temp"
	case "OLD", "BAD":
		return "old"
	default:
		return ""
	}
}

func allergyResource(entry Entry, patient map[string]any, version, patientID string, index int) (map[string]any, []Note, bool) {
	var notes []Note

	// An allergy entry nests: the outer act is the concern, the inner observation
	// carries the substance. The substance is what matters.
	substance := entry
	if entry.Kind == "Act" && len(entry.Children) > 0 {
		substance = entry.Children[0]
	}

	code := substance.ValueCode
	name := substance.ValueName
	system := substance.CodeSystem
	if code == "" {
		code = substance.Code
		name = substance.CodeName
	}

	if code == "" && name == "" {
		if substance.NilFlavor == "" {
			return nil, nil, false
		}
		// An entry stating no known allergies is real information and FHIR has a
		// code for it. Dropping it would leave the receiver unable to tell
		// "asked, none" from "never asked".
		return map[string]any{
			"resourceType": "AllergyIntolerance",
			"id":           deterministicID(patientID, "allergy", "none", fmt.Sprint(index)),
			"identifier": []map[string]any{{
				"system": "urn:perfuse:cda",
				"value":  deterministicID(patientID, "allergy", "none", fmt.Sprint(index)),
			}},
			"clinicalStatus": codeableConcept(
				"http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical", "active", "Active"),
			"code":    codeableConcept("http://snomed.info/sct", "716186003", "No known allergy"),
			"patient": patient,
		}, notes, true
	}

	if substance.NegationInd {
		// FHIR has no general way to say "not allergic to X" on an
		// AllergyIntolerance. Emitting it positively would invert the meaning, so
		// it is refused and reported.
		notes = append(notes, Note{
			Severity: "error",
			Path:     "AllergyIntolerance",
			Message: fmt.Sprintf("the document explicitly records that the patient is NOT allergic to %q. "+
				"FHIR cannot express that on an AllergyIntolerance, and emitting it as an allergy would invert "+
				"the meaning, so it was not converted", nameOr(name, code)),
			Rule: "negated-allergy-not-representable",
		})
		return nil, notes, false
	}

	resource := map[string]any{
		"resourceType": "AllergyIntolerance",
		"patient":      patient,
	}

	id := deterministicID(patientID, "allergy", substance.Code, substance.ValueCode, fmt.Sprint(index))
	resource["id"] = id
	resource["identifier"] = []map[string]any{{"system": "urn:perfuse:cda", "value": id}}

	coded, conceptNote := concept(code, system, name, "AllergyIntolerance.code")
	resource["code"] = coded
	if conceptNote != nil {
		notes = append(notes, *conceptNote)
	}

	status := "active"
	if strings.EqualFold(substance.StatusCode, "completed") {
		// A completed observation of an allergy does not mean the allergy ended;
		// it means the observation is finished. Treating it as inactive would
		// hide a live allergy, so it stays active.
		status = "active"
	}
	resource["clinicalStatus"] = codeableConcept(
		"http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical", status, "Active")

	// A reaction, when the document records one.
	for _, child := range substance.Children {
		if child.ValueCode == "" && child.ValueName == "" {
			continue
		}
		manifestation, note := concept(child.ValueCode, child.CodeSystem, child.ValueName, "AllergyIntolerance.reaction.manifestation")
		if note != nil {
			notes = append(notes, *note)
		}
		// R5 changed manifestation from CodeableConcept to CodeableReference.
		var wrapped any = manifestation
		if version == "R5" {
			wrapped = map[string]any{"concept": manifestation}
		}
		resource["reaction"] = []map[string]any{{"manifestation": []any{wrapped}}}
		break
	}

	return resource, notes, true
}

func conditionResource(entry Entry, patient map[string]any, patientID string, index int) (map[string]any, []Note, bool) {
	var notes []Note

	problem := entry
	if entry.Kind == "Act" && len(entry.Children) > 0 {
		problem = entry.Children[0]
	}

	code, name, system := problem.ValueCode, problem.ValueName, problem.CodeSystem
	if code == "" {
		code, name = problem.Code, problem.CodeName
	}
	if code == "" && name == "" {
		return nil, nil, false
	}

	if problem.NegationInd {
		notes = append(notes, Note{
			Severity: "warning",
			Path:     "Condition",
			Message: fmt.Sprintf("the document records that the patient does NOT have %q. This was converted to a "+
				"Condition with clinicalStatus 'refuted' rather than being dropped, so the assertion is not lost",
				nameOr(name, code)),
			Rule: "negated-condition-refuted",
		})
	}

	id := deterministicID(patientID, "condition", problem.ValueCode, problem.Code, fmt.Sprint(index))
	resource := map[string]any{
		"resourceType": "Condition",
		"id":           id,
		"identifier":   []map[string]any{{"system": "urn:perfuse:cda", "value": id}},
		"subject":      patient,
	}

	coded, note := concept(code, system, name, "Condition.code")
	resource["code"] = coded
	if note != nil {
		notes = append(notes, *note)
	}

	status := "active"
	if problem.NegationInd {
		status = "refuted"
	} else if strings.EqualFold(problem.StatusCode, "completed") && problem.High != "" {
		status = "resolved"
	}
	resource["clinicalStatus"] = codeableConcept(
		"http://terminology.hl7.org/CodeSystem/condition-clinical", status, capitalise(status))

	onset, n := fhirDateTime(firstNonEmpty(problem.Low, problem.EffectiveTime), "Condition.onsetDateTime")
	if n != nil {
		notes = append(notes, *n)
	}
	if onset != "" {
		resource["onsetDateTime"] = onset
	}

	return resource, notes, true
}

func medicationResource(entry Entry, patient map[string]any, patientID, section string, index int) (map[string]any, []Note, bool) {
	var notes []Note

	// The drug is in a nested manufacturedMaterial, reached through the consumable.
	code, name, system := entry.Code, entry.CodeName, entry.CodeSystem
	if code == "" {
		// Look one level down for the material code.
		for _, child := range entry.Children {
			if child.Code != "" {
				code, name, system = child.Code, child.CodeName, child.CodeSystem
				break
			}
		}
	}
	if code == "" && name == "" {
		return nil, nil, false
	}

	if entry.NegationInd {
		notes = append(notes, Note{
			Severity: "warning",
			Path:     "MedicationStatement",
			Message: fmt.Sprintf("the document records that %q was NOT administered. This was converted with status "+
				"'not-taken' rather than being dropped", nameOr(name, code)),
			Rule: "negated-medication",
		})
	}

	id := deterministicID(patientID, "medication", section, entry.Code, fmt.Sprint(index))
	resource := map[string]any{
		"resourceType": "MedicationStatement",
		"id":           id,
		"identifier":   []map[string]any{{"system": "urn:perfuse:cda", "value": id}},
		"subject":      patient,
	}

	coded, note := concept(code, system, name, "MedicationStatement.medication")
	if note != nil {
		notes = append(notes, *note)
	}
	// R5 changed medication from a choice to a CodeableReference. The value is the
	// same shape either way here, so it is written in the R5 form and downgraded
	// by the FHIR serialiser when R4 is asked for.
	resource["medication"] = map[string]any{"concept": coded}

	status := "recorded"
	if entry.NegationInd {
		status = "not-taken"
	}
	resource["status"] = status

	when, n := fhirDateTime(firstNonEmpty(entry.Low, entry.EffectiveTime), "MedicationStatement.effective")
	if n != nil {
		notes = append(notes, *n)
	}
	if when != "" {
		resource["effectiveDateTime"] = when
	}

	return resource, notes, true
}

func observationResource(entry Entry, patient map[string]any, category, patientID string, index int) (map[string]any, []Note, bool) {
	var notes []Note

	if entry.Code == "" && entry.CodeName == "" {
		return nil, nil, false
	}

	id := deterministicID(patientID, "observation", category, entry.Code, entry.EffectiveTime, fmt.Sprint(index))
	resource := map[string]any{
		"resourceType": "Observation",
		"id":           id,
		"identifier":   []map[string]any{{"system": "urn:perfuse:cda", "value": id}},
		"subject":      patient,
		"status":       observationStatus(entry.StatusCode),
		"category": []map[string]any{codeableConcept(
			"http://terminology.hl7.org/CodeSystem/observation-category", category, category)},
	}

	coded, note := concept(entry.Code, entry.CodeSystem, entry.CodeName, "Observation.code")
	resource["code"] = coded
	if note != nil {
		notes = append(notes, *note)
	}

	switch {
	case entry.ValueCode != "":
		valueConcept, n := concept(entry.ValueCode, entry.CodeSystem, entry.ValueName, "Observation.valueCodeableConcept")
		resource["valueCodeableConcept"] = valueConcept
		if n != nil {
			notes = append(notes, *n)
		}

	case entry.Value != "" && entry.Unit != "":
		quantity := map[string]any{"value": numberOrString(entry.Value)}
		// A unit is only given a UCUM system when it is certainly UCUM. Asserting
		// UCUM on a unit that is not can turn a normal result into an alarming
		// one on a system that converts.
		if isUCUM(entry.Unit) {
			quantity["unit"] = entry.Unit
			quantity["system"] = "http://unitsofmeasure.org"
			quantity["code"] = entry.Unit
		} else {
			quantity["unit"] = entry.Unit
			notes = append(notes, Note{
				Severity: "warning",
				Path:     "Observation.valueQuantity",
				Message: fmt.Sprintf("the unit %q was kept as text without a UCUM code, because it is not one this "+
					"conversion recognises. A receiving system will display it but cannot convert it", entry.Unit),
				Rule: "unit-not-ucum",
			})
		}
		resource["valueQuantity"] = quantity

	case entry.Value != "":
		resource["valueString"] = entry.Value

	case entry.NilFlavor != "":
		resource["dataAbsentReason"] = codeableConcept(
			"http://terminology.hl7.org/CodeSystem/data-absent-reason",
			dataAbsentReason(entry.NilFlavor), nilFlavorMeaning(entry.NilFlavor))

	default:
		if len(entry.Children) == 0 {
			return nil, notes, false
		}
	}

	when, n := fhirDateTime(firstNonEmpty(entry.EffectiveTime, entry.Low), "Observation.effectiveDateTime")
	if n != nil {
		notes = append(notes, *n)
	}
	if when != "" {
		resource["effectiveDateTime"] = when
	}

	return resource, notes, true
}

func observationStatus(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "completed":
		return "final"
	case "active":
		return "preliminary"
	case "aborted":
		return "cancelled"
	case "":
		return "final"
	default:
		return "final"
	}
}

func dataAbsentReason(flavor string) string {
	switch strings.ToUpper(strings.TrimSpace(flavor)) {
	case "UNK":
		return "unknown"
	case "ASKU":
		return "asked-unknown"
	case "NASK":
		return "not-asked"
	case "NAV":
		return "temp-unknown"
	case "NA":
		return "not-applicable"
	case "MSK":
		return "masked"
	default:
		return "unknown"
	}
}

func procedureResource(entry Entry, patient map[string]any, patientID string, index int) (map[string]any, []Note, bool) {
	var notes []Note
	if entry.Code == "" && entry.CodeName == "" {
		return nil, nil, false
	}

	id := deterministicID(patientID, "procedure", entry.Code, fmt.Sprint(index))
	resource := map[string]any{
		"resourceType": "Procedure",
		"id":           id,
		"identifier":   []map[string]any{{"system": "urn:perfuse:cda", "value": id}},
		"subject":      patient,
		"status":       procedureStatus(entry.StatusCode, entry.NegationInd),
	}

	coded, note := concept(entry.Code, entry.CodeSystem, entry.CodeName, "Procedure.code")
	resource["code"] = coded
	if note != nil {
		notes = append(notes, *note)
	}

	if entry.NegationInd {
		notes = append(notes, Note{
			Severity: "warning",
			Path:     "Procedure.status",
			Message: fmt.Sprintf("the document records that %q was NOT performed; the status was set to 'not-done' "+
				"rather than the procedure being dropped", nameOr(entry.CodeName, entry.Code)),
			Rule: "negated-procedure",
		})
	}

	when, n := fhirDateTime(firstNonEmpty(entry.EffectiveTime, entry.Low), "Procedure.occurrenceDateTime")
	if n != nil {
		notes = append(notes, *n)
	}
	if when != "" {
		resource["occurrenceDateTime"] = when
	}

	return resource, notes, true
}

func procedureStatus(code string, negated bool) string {
	if negated {
		return "not-done"
	}
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "completed":
		return "completed"
	case "active":
		return "in-progress"
	case "aborted":
		return "stopped"
	case "cancelled":
		return "not-done"
	default:
		return "completed"
	}
}

func (d *Document) documentReference(opts FHIROptions, patient map[string]any, id, version string) (map[string]any, []Note) {
	var notes []Note

	resource := map[string]any{
		"resourceType": "DocumentReference",
		"id":           id,
		"identifier":   []map[string]any{{"system": "urn:perfuse:cda", "value": id}},
		"status":       "current",
		"subject":      patient,
	}

	if d.TypeCode != "" {
		resource["type"] = codeableConcept("http://loinc.org", d.TypeCode, firstNonEmpty(d.TypeName, d.DocumentType))
	}
	if d.Title != "" {
		// R5 has description; R4 has description too, so this is safe in both.
		resource["description"] = d.Title
	}
	when, n := fhirDateTime(d.EffectiveTime, "DocumentReference.date")
	if n != nil {
		notes = append(notes, *n)
	}
	if when != "" {
		resource["date"] = when
	}

	// A superseding document must say what it replaces, or the receiver files two
	// contradictory versions with nothing to distinguish them.
	if d.Transport != nil && d.Transport.Replaces() {
		resource["relatesTo"] = []map[string]any{{
			"code": "replaces",
			"target": map[string]any{
				"identifier": map[string]any{
					"system": "urn:perfuse:cda-source",
					"value":  d.Transport.ParentID,
				},
			},
		}}
		notes = append(notes, Note{
			Severity: "info",
			Path:     "DocumentReference.relatesTo",
			Message: fmt.Sprintf("this document replaces %q. The relationship was carried over, because a receiver "+
				"that files both would hold two contradictory versions with nothing to say which is current",
				d.Transport.ParentID),
			Rule: "replacement-recorded",
		})
	}

	attachment := map[string]any{"contentType": "application/hl7-cda+xml"}
	if len(opts.Original) > 0 {
		attachment["data"] = base64Of(opts.Original)
		attachment["size"] = len(opts.Original)
	} else {
		notes = append(notes, Note{
			Severity: "warning",
			Path:     "DocumentReference.content.attachment",
			Message: "the original document bytes were not supplied, so the DocumentReference records only metadata. " +
				"The signed document is the legal record and is normally the thing a hospital must retain",
			Rule: "original-not-attached",
		})
	}
	if d.Title != "" {
		attachment["title"] = d.Title
	}
	resource["content"] = []map[string]any{{"attachment": attachment}}

	return resource, notes
}

// concept builds a CodeableConcept, keeping an unmapped system as text.
func concept(code, system, display, path string) (map[string]any, *Note) {
	if code == "" {
		if display == "" {
			return map[string]any{}, nil
		}
		// No code at all: the text is all there is, and that is what gets sent.
		// Inventing a code here is exactly the failure this package refuses.
		return map[string]any{"text": display}, &Note{
			Severity: "warning",
			Path:     path,
			Message: fmt.Sprintf("%q had no code, so it was carried as text only. A receiving system can display it "+
				"but cannot act on it", display),
			Rule: "code-absent",
		}
	}

	uri, known := systemURI(system)
	out := map[string]any{
		"coding": []map[string]any{{"system": uri, "code": code, "display": display}},
	}
	if display != "" {
		out["text"] = display
	}
	if known {
		return out, nil
	}
	return out, &Note{
		Severity: "warning",
		Path:     path,
		Message: fmt.Sprintf("the code system %q is not one this conversion recognises. The code was kept with an "+
			"OID-derived system rather than being translated, because a guessed translation is worse than an "+
			"untranslated code", system),
		Rule: "code-system-unmapped",
	}
}

// systemURI maps a code system name to its FHIR URI.
func systemURI(system string) (string, bool) {
	// The name arrives already translated from an OID by oidName, which appends a
	// marker when it did not recognise it.
	clean := strings.TrimSuffix(system, " (not recognised)")

	switch clean {
	case "LOINC":
		return "http://loinc.org", true
	case "SNOMED CT":
		return "http://snomed.info/sct", true
	case "RxNorm":
		return "http://www.nlm.nih.gov/research/umls/rxnorm", true
	case "ICD-10-CM":
		return "http://hl7.org/fhir/sid/icd-10-cm", true
	case "ICD-9-CM diagnosis":
		return "http://hl7.org/fhir/sid/icd-9-cm", true
	case "CPT-4":
		return "http://www.ama-assn.org/go/cpt", true
	case "NDC":
		return "http://hl7.org/fhir/sid/ndc", true
	case "UCUM":
		return "http://unitsofmeasure.org", true
	case "CVX vaccine codes":
		return "http://hl7.org/fhir/sid/cvx", true
	case "HL7 administrative gender":
		return "http://terminology.hl7.org/CodeSystem/v3-AdministrativeGender", true
	case "HL7 act code":
		return "http://terminology.hl7.org/CodeSystem/v3-ActCode", true
	case "HL7 observation interpretation":
		return "http://terminology.hl7.org/CodeSystem/v3-ObservationInterpretation", true
	case "":
		return "urn:perfuse:unknown-system", false
	}

	if strings.HasPrefix(clean, "2.16.") || strings.HasPrefix(clean, "1.3.") {
		return "urn:oid:" + clean, false
	}
	return "urn:perfuse:unknown-system", false
}

func codeableConcept(system, code, display string) map[string]any {
	return map[string]any{
		"coding": []map[string]any{{"system": system, "code": code, "display": display}},
		"text":   display,
	}
}

// ucumUnits are the units this conversion will assert a UCUM code for.
//
// The list is short on purpose. A unit asserted as UCUM that is not gets converted
// by the receiver, and a converted number that was never in the unit claimed can
// turn a normal result into an alarming one.
var ucumUnits = map[string]bool{
	"mg": true, "g": true, "kg": true, "mcg": true, "ug": true, "ng": true,
	"L": true, "dL": true, "mL": true, "uL": true,
	"mg/dL": true, "g/dL": true, "g/L": true, "mg/L": true, "ug/L": true,
	"mmol/L": true, "umol/L": true, "mEq/L": true, "meq/L": true,
	"U/L": true, "IU/L": true, "10*3/uL": true, "10*6/uL": true, "10*9/L": true,
	"%": true, "mm[Hg]": true, "cm": true, "m": true, "mm": true,
	"/min": true, "1/min": true, "Cel": true, "[degF]": true, "s": true, "min": true,
	"h": true, "d": true, "wk": true, "mo": true, "a": true,
}

func isUCUM(unit string) bool { return ucumUnits[strings.TrimSpace(unit)] }

// fhirDate converts a CDA timestamp to a FHIR date.
func fhirDate(value, path string) (string, *Note) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) >= 8 {
		digits := value[:8]
		if _, err := time.Parse("20060102", digits); err == nil {
			return digits[:4] + "-" + digits[4:6] + "-" + digits[6:8], nil
		}
	}
	if len(value) == 6 {
		if _, err := time.Parse("200601", value); err == nil {
			return value[:4] + "-" + value[4:6], nil
		}
	}
	if len(value) == 4 {
		if _, err := time.Parse("2006", value); err == nil {
			return value, nil
		}
	}
	return "", &Note{
		Severity: "warning",
		Path:     path,
		Message:  fmt.Sprintf("the date %q is not a recognisable CDA timestamp and was omitted rather than guessed at", value),
		Rule:     "date-unparseable",
	}
}

// fhirDateTime converts a CDA timestamp to a FHIR dateTime.
//
// A CDA timestamp may carry an offset, and may not. FHIR requires one whenever the
// value has a time. Supplying a local offset silently is how a discharge time ends
// up several hours out, so when one has to be assumed the conversion says so.
func fhirDateTime(value, path string) (string, *Note) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	// Date only.
	if len(value) == 8 {
		if date, note := fhirDate(value, path); date != "" {
			return date, note
		}
	}
	if len(value) < 8 {
		return fhirDate(value, path)
	}

	// With an explicit offset.
	for _, layout := range []string{"20060102150405-0700", "200601021504-0700", "2006010215-0700"} {
		if when, err := time.Parse(layout, value); err == nil {
			return when.Format(time.RFC3339), nil
		}
	}

	// Without one.
	for _, layout := range []string{"20060102150405", "200601021504", "2006010215"} {
		if when, err := time.Parse(layout, value); err == nil {
			return when.Format("2006-01-02T15:04:05") + "+00:00", &Note{
				Severity: "warning",
				Path:     path,
				Message: fmt.Sprintf("the timestamp %q has no time zone offset. FHIR requires one, so UTC was assumed; "+
					"if the sending system meant local time this value is wrong by the local offset", value),
				Rule: "timestamp-offset-assumed",
			}
		}
	}

	return "", &Note{
		Severity: "warning",
		Path:     path,
		Message:  fmt.Sprintf("the timestamp %q could not be read and was omitted rather than guessed at", value),
		Rule:     "timestamp-unparseable",
	}
}

func upsert(resourceType, id string, resource map[string]any, match string) map[string]any {
	request := map[string]any{"method": "PUT", "url": resourceType + "/" + id}
	if match != "" {
		// A conditional update means a document sent twice updates rather than
		// duplicating, which is the difference between a feed that retries safely
		// and one that accumulates copies somebody later merges by hand.
		request = map[string]any{"method": "PUT", "url": resourceType + "?" + match}
	}
	return map[string]any{
		"fullUrl":  "urn:uuid:" + id,
		"resource": resource,
		"request":  request,
	}
}

func deterministicID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:32]
}

func base64Of(data []byte) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	for i := 0; i < len(data); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], data[i:])
		b.WriteByte(chars[chunk[0]>>2])
		b.WriteByte(chars[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			b.WriteByte(chars[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			b.WriteByte('=')
		}
		if n > 2 {
			b.WriteByte(chars[chunk[2]&0x3f])
		} else {
			b.WriteByte('=')
		}
	}
	return b.String()
}

func numberOrString(s string) any {
	var f float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f); err == nil {
		return f
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func nameOr(name, code string) string {
	if name != "" {
		return name
	}
	return code
}
