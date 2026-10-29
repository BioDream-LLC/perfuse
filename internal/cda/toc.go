package cda

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/fhir"
)

// A transfer summary for the place the patient goes next: a C-CDA from the hospital, turned into the document PACIO's
// Transitions of Care guide (hl7.fhir.us.pacio-toc 1.0.0) describes, so a nursing home, a rehabilitation unit or a
// home health agency receives one FHIR document instead of an XML file it has to read by eye.
//
// The guide's Composition has fifteen sections and every one is required, each with its own text. A hospital's
// document seldom has all fifteen, and that is information too: a section the source did not carry is sent empty,
// with the reason "unavailable" and a sentence saying so, never filled in. A section the source carried as text alone
// keeps its text and no entries; the conversion does not read prose into coded data. Entries come only from the coded
// entries the source carried, built by the same rules as the ordinary conversion (negation carried through, nothing
// translated between code systems).
//
// The output is FHIR R4, the release US Core and the guide are written for, whatever release the caller asked for.

// TOCOptions configures a transfer summary.
type TOCOptions struct {
	// IdentifierSystems maps an OID root to a URI, as for ToFHIR.
	IdentifierSystems map[string]string

	// Language is the document's language when the source does not say (BCP 47). Default en-US.
	Language string
}

// tocSection is one of the guide's required sections.
type tocSection struct {
	slice  string // the guide's slice name
	system string
	code   string
	title  string
	kinds  []string // the C-CDA section kinds that feed it, in order of preference
}

const (
	tocProfile     = "http://hl7.org/fhir/us/pacio-toc/StructureDefinition/TOC-Bundle"
	tocComposition = "http://hl7.org/fhir/us/pacio-toc/StructureDefinition/TOC-Composition"
	tocTempCS      = "http://hl7.org/fhir/us/pacio-toc/CodeSystem/toc-temp-cs"
	usCore         = "http://hl7.org/fhir/us/core/StructureDefinition/"
)

// tocSections are the guide's sections in its own order.
var tocSections = []tocSection{
	{"advance_directives", "http://loinc.org", "42348-3", "Advance directives", []string{"Advance directives"}},
	{"allergies", "http://loinc.org", "48765-2", "Allergies and intolerances", []string{"Allergies"}},
	{"behavioral_health", tocTempCS, "behavioral_health_summary", "Behavioral health", []string{"Mental status"}},
	{"functional_status", "http://loinc.org", "47420-5", "Functional status", []string{"Functional status"}},
	{"immunizations", "http://loinc.org", "11369-6", "Immunizations", []string{"Immunisations"}},
	{"discharge_instructions", "http://loinc.org", "69730-0", "Discharge instructions", []string{"Hospital discharge instructions"}},
	{"medical_devices", "http://loinc.org", "46264-8", "Medical devices", []string{"Medical equipment"}},
	{"medications", "http://loinc.org", "10160-0", "Medications", []string{"Discharge medications", "Medications"}},
	{"plan_of_care", "http://loinc.org", "18776-5", "Plan of care", []string{"Plan of treatment"}},
	{"problems", "http://loinc.org", "11450-4", "Problems", []string{"Problems", "Hospital discharge diagnosis"}},
	{"procedures", "http://loinc.org", "47519-4", "Procedures", []string{"Procedures"}},
	{"reason_for_referral", "http://loinc.org", "42349-1", "Reason for referral", []string{"Reason for referral", "Chief complaint and reason for visit", "Chief complaint", "Reason for visit"}},
	{"clinical_results", "http://loinc.org", "30954-2", "Results", []string{"Results"}},
	{"social_history", "http://loinc.org", "29762-2", "Social history", []string{"Social history"}},
	{"vital_signs", "http://loinc.org", "8716-3", "Vital signs", []string{"Vital signs"}},
}

// ToTOC converts the document to a PACIO Transitions of Care document Bundle (FHIR R4). A document without an
// author person or a custodian is refused: the guide requires both, and inventing either would put a name on a
// clinical record that nobody gave.
func (d *Document) ToTOC(opts TOCOptions) (*FHIRResult, error) {
	if d == nil {
		return nil, fmt.Errorf("cda: no document")
	}
	if strings.TrimSpace(d.Custodian) == "" {
		return nil, fmt.Errorf("cda: the document names no custodian organisation, and a transfer summary must say " +
			"which organisation keeps the record")
	}
	var author *Author
	for i := range d.Authors {
		if d.Authors[i].Family != "" || d.Authors[i].Given != "" {
			author = &d.Authors[i]
			break
		}
	}
	if author == nil {
		return nil, fmt.Errorf("cda: the document names no person as its author, and a transfer summary must be " +
			"authored by a practitioner (a device or an organisation alone is not enough)")
	}

	result := &FHIRResult{Counts: map[string]int{}}
	b := &tocBuilder{result: result}
	fopts := FHIROptions{Version: "R4", IdentifierSystems: opts.IdentifierSystems}

	patientID := d.patientID()
	patient, notes := d.patientResource(fopts, patientID)
	result.Notes = append(result.Notes, notes...)
	patient["meta"] = profile("us-core-patient")
	patientRef := b.add(patient)
	ref := map[string]any{"reference": patientRef}

	org := map[string]any{
		"resourceType": "Organization",
		"id":           deterministicID("organization", d.Custodian),
		"active":       true,
		"name":         d.Custodian,
	}
	orgRef := b.add(org)

	practitioner := map[string]any{
		"resourceType": "Practitioner",
		"id":           deterministicID("practitioner", author.ID, author.Family, author.Given),
	}
	name := map[string]any{}
	if author.Family != "" {
		name["family"] = author.Family
	}
	if author.Given != "" {
		name["given"] = []string{author.Given}
	}
	practitioner["name"] = []map[string]any{name}
	if root, ext, _ := strings.Cut(author.ID, "^"); root == "2.16.840.1.113883.4.6" && !validNPI(ext) {
		result.Notes = append(result.Notes, Note{Severity: "warning", Path: "Practitioner.identifier",
			Message: fmt.Sprintf("the author's NPI %q fails its check digit, so it was sent under the NPI's OID rather "+
				"than as a US NPI: a receiver would refuse it as an NPI", ext), Rule: "npi-check-digit"})
	}
	if ident := authorIdentifier(author.ID, opts.IdentifierSystems); ident != nil {
		practitioner["identifier"] = []map[string]any{ident}
	} else {
		result.Notes = append(result.Notes, Note{Severity: "warning", Path: "Practitioner.identifier",
			Message: "the author carries no identifier (an NPI, say), so the receiver cannot tell this practitioner " +
				"from another of the same name", Rule: "author-identifier-absent"})
	}
	authorRef := b.add(practitioner)

	var encounterRef string
	if e := d.Encounter; e != nil {
		enc := map[string]any{
			"resourceType": "Encounter",
			"id":           deterministicID(patientID, "encounter", e.ID, e.Start),
			"status":       "completed",
			"class": []map[string]any{codeableConcept("http://terminology.hl7.org/CodeSystem/v3-ActCode",
				"IMP", "inpatient encounter")},
			"type":    []map[string]any{{"text": firstNonEmpty(e.Code, "Hospital stay")}},
			"subject": ref,
		}
		if e.ID != "" {
			enc["identifier"] = []map[string]any{{"system": "urn:perfuse:cda-encounter", "value": e.ID}}
		}
		period := map[string]any{}
		if s, _ := fhirDateTime(e.Start, "Encounter.period.start"); s != "" {
			period["start"] = s
		}
		if s, _ := fhirDateTime(e.End, "Encounter.period.end"); s != "" {
			period["end"] = s
		}
		if len(period) > 0 {
			enc["actualPeriod"] = period
		}
		if e.Start == "" && e.End == "" {
			// The class is a guess without a date to anchor it; say what was assumed.
			result.Notes = append(result.Notes, Note{Severity: "info", Path: "Encounter",
				Message: "the stay carries no dates", Rule: "encounter-undated"})
		}
		result.Notes = append(result.Notes, Note{Severity: "info", Path: "Encounter.class",
			Message: "the hospital stay the summary closes was recorded as an inpatient encounter (IMP), since a " +
				"transfer summary is written at discharge from one", Rule: "encounter-class-inpatient"})
		encounterRef = b.add(enc)
	}

	language := bcp47(d.LanguageCode)
	if language == "" {
		language = firstNonEmpty(opts.Language, "en-US")
		if d.LanguageCode != "" {
			result.Notes = append(result.Notes, Note{Severity: "info", Path: "Composition.language",
				Message: fmt.Sprintf("the document's language %q is not a BCP 47 tag; %s was used", d.LanguageCode, language),
				Rule:    "language-not-bcp47"})
		}
	}

	var sections []map[string]any
	for _, want := range tocSections {
		section, sectionNotes := b.section(d, want, ref, authorRef, patientID)
		result.Notes = append(result.Notes, sectionNotes...)
		sections = append(sections, section)
	}

	date, n := fhirDateTime(d.EffectiveTime, "Composition.date")
	if n != nil {
		result.Notes = append(result.Notes, *n)
	}
	if date == "" {
		return nil, fmt.Errorf("cda: the document carries no usable effectiveTime, and a transfer summary must be dated")
	}

	compID := deterministicID(patientID, "toc", d.ID, d.SetID, d.Version)
	identifier := map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + fhir.DeterministicUUID("toc", compID)}
	if root, ext, ok := strings.Cut(d.ID, "^"); ok && ext != "" {
		switch {
		case looksLikeOID(root):
			identifier = map[string]any{"system": "urn:oid:" + root, "value": ext}
		case looksLikeUUID(root):
			identifier = map[string]any{"system": "urn:uuid:" + strings.ToLower(root), "value": ext}
		}
	} else if d.ID != "" && !ok {
		switch {
		case looksLikeOID(d.ID):
			identifier = map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:oid:" + d.ID}
		case looksLikeUUID(d.ID):
			identifier = map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + strings.ToLower(d.ID)}
		}
	}
	composition := map[string]any{
		"resourceType": "Composition",
		"id":           compID,
		"meta":         map[string]any{"profile": []string{tocComposition}},
		"language":     language,
		"identifier":   identifier,
		"status":       "final",
		"type":         codeableConcept("http://loinc.org", "18761-7", "Transfer summary note"),
		"category":     []map[string]any{codeableConcept("http://loinc.org", "18761-7", "Transfer Summary Note")},
		"subject":      ref,
		"date":         date,
		"author":       []map[string]any{{"reference": authorRef}},
		"title":        firstNonEmpty(d.Title, "Transfer summary"),
		"custodian":    map[string]any{"reference": orgRef},
		"section":      sections,
	}
	if encounterRef != "" {
		composition["encounter"] = map[string]any{"reference": encounterRef}
	}
	composition["text"] = narrativeIn(fmt.Sprintf("Transfer summary for %s, from %s.",
		firstNonEmpty(d.Patient.Name(), "the patient"), d.Custodian), language)

	compEntry, err := b.entry(composition)
	if err != nil {
		return nil, err
	}
	result.Counts["Composition"]++

	entries := append([]map[string]any{compEntry}, b.entries...)
	if b.err != nil {
		return nil, b.err
	}

	timestamp := date
	if !strings.Contains(timestamp, "T") {
		timestamp += "T00:00:00Z"
		result.Notes = append(result.Notes, Note{Severity: "info", Path: "Bundle.timestamp",
			Message: "the document is dated without a time, so the Bundle's timestamp is midnight UTC on that day",
			Rule:    "timestamp-midnight"})
	}
	result.Bundle = map[string]any{
		"resourceType": "Bundle",
		"id":           compID,
		"meta":         map[string]any{"profile": []string{tocProfile}},
		"identifier":   identifier,
		"type":         "document",
		"timestamp":    timestamp,
		"entry":        entries,
	}
	return result, nil
}

// tocBuilder collects the Bundle's entries, each given an urn:uuid fullUrl and written as R4.
type tocBuilder struct {
	result  *FHIRResult
	entries []map[string]any
	seen    map[string]bool
	err     error
}

func (b *tocBuilder) add(resource map[string]any) string {
	rt, _ := resource["resourceType"].(string)
	id, _ := resource["id"].(string)
	url := "urn:uuid:" + fhir.DeterministicUUID(rt, id)
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	if b.seen[url] {
		return url
	}
	b.seen[url] = true
	entry, err := b.entry(resource)
	if err != nil {
		if b.err == nil {
			b.err = err
		}
		return url
	}
	b.entries = append(b.entries, entry)
	b.result.Counts[rt]++
	return url
}

// entry writes one resource as R4 through the fhir package, so the shapes are R4's (medicationCodeableConcept,
// performedDateTime) and not the R5 form the builders produce.
func (b *tocBuilder) entry(resource map[string]any) (map[string]any, error) {
	r4, err := asR4(resource)
	if err != nil {
		return nil, err
	}
	rt, _ := r4["resourceType"].(string)
	id, _ := r4["id"].(string)
	return map[string]any{"fullUrl": "urn:uuid:" + fhir.DeterministicUUID(rt, id), "resource": r4}, nil
}

// asR4 rewrites an R5-shaped resource into R4 form.
func asR4(resource map[string]any) (map[string]any, error) {
	if resource["resourceType"] == "MedicationStatement" && resource["status"] == "recorded" {
		// R5's "recorded" says only that the statement was made; R4 has no such code and refuses it. A medication
		// listed in a document's medications section is one the patient is taking, which R4 calls active.
		resource["status"] = "active"
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		return nil, err
	}
	r, err := fhir.UnmarshalResource(raw)
	if err != nil {
		return nil, fmt.Errorf("cda: %s: %w", resource["resourceType"], err)
	}
	out, err := fhir.Marshal(r, fhir.R4)
	if err != nil {
		return nil, err
	}
	var tree map[string]any
	if err := json.Unmarshal(out, &tree); err != nil {
		return nil, err
	}
	return tree, nil
}

// section builds one of the guide's sections from the C-CDA section that feeds it.
func (b *tocBuilder) section(d *Document, want tocSection, patient map[string]any, authorRef, patientID string) (map[string]any, []Note) {
	var notes []Note
	out := map[string]any{
		"title": want.title,
		"code":  codeableConcept(want.system, want.code, want.title),
	}
	if want.system == tocTempCS {
		out["code"] = map[string]any{"coding": []map[string]any{{"system": want.system, "code": want.code}}, "text": want.title}
	}

	var source *Section
	for _, kind := range want.kinds {
		if s := d.SectionByKind(kind); s != nil {
			source = s
			break
		}
	}
	if source == nil {
		out["text"] = narrative(fmt.Sprintf("The sending document had no %s section.", strings.ToLower(want.title)))
		out["emptyReason"] = codeableConcept("http://terminology.hl7.org/CodeSystem/list-empty-reason", "unavailable", "Unavailable")
		return out, nil
	}

	var refs []map[string]any
	addRef := func(resource map[string]any) {
		refs = append(refs, map[string]any{"reference": b.add(resource)})
	}

	switch want.slice {
	case "allergies":
		for i, entry := range source.Entries {
			r, n, ok := allergyResource(entry, patient, "R4", patientID, i)
			notes = append(notes, n...)
			if ok {
				r["meta"] = profile("us-core-allergyintolerance")
				addRef(r)
			}
		}
	case "problems":
		category := "problem-list-item"
		if source.Kind == "Hospital discharge diagnosis" {
			category = "encounter-diagnosis"
		}
		for i, entry := range source.Entries {
			r, n, ok := conditionResource(entry, patient, patientID, i)
			notes = append(notes, n...)
			if !ok {
				continue
			}
			r["category"] = []map[string]any{codeableConcept("http://terminology.hl7.org/CodeSystem/condition-category",
				category, map[string]string{"problem-list-item": "Problem List Item", "encounter-diagnosis": "Encounter Diagnosis"}[category])}
			if category == "problem-list-item" {
				r["meta"] = profile("us-core-condition-problems-health-concerns")
			} else {
				r["meta"] = profile("us-core-condition-encounter-diagnosis")
			}
			addRef(r)
		}
	case "medications":
		list, n := b.medicationList(source, patient, authorRef, patientID, d.EffectiveTime)
		notes = append(notes, n...)
		if list != nil {
			addRef(list)
		}
	case "immunizations":
		for i, entry := range source.Entries {
			if entry.Kind != "SubstanceAdministration" {
				continue
			}
			r, n, ok := immunizationResource(entry, patient, patientID, i)
			notes = append(notes, n...)
			if ok {
				addRef(r)
			}
		}
	case "procedures":
		for i, entry := range source.Entries {
			if entry.Kind != "Procedure" {
				continue
			}
			r, n, ok := procedureResource(entry, patient, patientID, i)
			notes = append(notes, n...)
			if ok {
				if _, dated := r["occurrenceDateTime"]; !dated {
					notes = append(notes, Note{Severity: "warning", Path: "Procedure.performed",
						Message: fmt.Sprintf("%q carries no date, so the receiver cannot tell when it was done",
							nameOr(entry.CodeName, entry.Code)), Rule: "procedure-undated"})
				}
				r["meta"] = profile("us-core-procedure")
				addRef(r)
			}
		}
	case "vital_signs":
		obs, n := vitalSigns(source.Entries, patient, patientID, true)
		notes = append(notes, n...)
		for _, r := range obs {
			addRef(r)
		}
	case "clinical_results":
		category := "laboratory"
		for i, entry := range flatten(source.Entries) {
			if entry.Kind != "Observation" {
				continue
			}
			r, n, ok := observationResource(entry, patient, category, patientID, i)
			notes = append(notes, n...)
			if !ok {
				continue
			}
			r["meta"] = profile("us-core-observation-clinical-result")
			addRef(r)
		}
	}

	if len(refs) > 0 {
		out["entry"] = refs
	} else if len(source.Entries) > 0 {
		notes = append(notes, Note{Severity: "info", Path: "Composition.section:" + want.slice,
			Message: fmt.Sprintf("the %s section's coded entries were kept in its text only: this conversion does "+
				"not yet turn them into FHIR resources", strings.ToLower(want.title)), Rule: "section-narrative-only"})
	}

	text := strings.TrimSpace(source.NarrativeText)
	switch {
	case text != "":
		out["text"] = narrative(text)
	case len(refs) > 0:
		out["text"] = narrative(fmt.Sprintf("%d entries, from the sending document's %s section.", len(refs), source.Title))
	default:
		out["text"] = narrative(fmt.Sprintf("The sending document's %s section was empty.", firstNonEmpty(source.Title, want.title)))
	}
	if len(refs) == 0 {
		reason := "unavailable"
		if source.Empty || source.NilFlavor != "" {
			reason = "nilknown"
		}
		if text != "" && len(source.Entries) == 0 {
			// Narrative only: the content is in the text, and there is nothing to say is missing.
			return out, notes
		}
		out["emptyReason"] = codeableConcept("http://terminology.hl7.org/CodeSystem/list-empty-reason", reason,
			map[string]string{"unavailable": "Unavailable", "nilknown": "Nil Known"}[reason])
	}
	return out, notes
}

// medicationList wraps the medications in a PACIO medication list (smp-medication-list), the form the guide's
// medications section takes: a discharge section is the discharged list, any other the active one.
func (b *tocBuilder) medicationList(source *Section, patient map[string]any, authorRef, patientID, when string) (map[string]any, []Note) {
	var notes []Note
	var items []map[string]any
	for i, entry := range source.Entries {
		entry = medicationEntry(entry)
		if entry.Kind != "SubstanceAdministration" {
			continue
		}
		r, n, ok := medicationResource(entry, patient, patientID, source.Kind, i)
		notes = append(notes, n...)
		if !ok {
			continue
		}
		r["meta"] = map[string]any{"profile": []string{"http://hl7.org/fhir/us/smp/StructureDefinition/smp-medicationstatement"}}
		items = append(items, map[string]any{"item": map[string]any{"reference": b.add(r)}})
	}
	if len(items) == 0 {
		return nil, notes
	}
	code, display := "104202-7", "Active medication list"
	if source.Kind == "Discharge medications" {
		code, display = "104204-3", "Discharged medication list"
	}
	list := map[string]any{
		"resourceType": "List",
		"id":           deterministicID(patientID, "medication-list", source.Kind),
		"meta":         map[string]any{"profile": []string{"http://hl7.org/fhir/us/smp/StructureDefinition/smp-medication-list"}},
		"status":       "current",
		"mode":         "snapshot",
		"title":        firstNonEmpty(source.Title, display),
		"code":         codeableConcept("http://loinc.org", code, display),
		"subject":      patient,
		"source":       map[string]any{"reference": authorRef},
		"entry":        items,
	}
	if d, _ := fhirDateTime(when, "List.date"); d != "" {
		list["date"] = d
	}
	return list, notes
}

// immunizationResource builds a US Core Immunization from an immunization activity.
func immunizationResource(entry Entry, patient map[string]any, patientID string, index int) (map[string]any, []Note, bool) {
	var notes []Note
	code, name, system := entry.Code, entry.CodeName, entry.CodeSystem
	if code == "" {
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
	when, n := fhirDateTime(firstNonEmpty(entry.EffectiveTime, entry.Low), "Immunization.occurrenceDateTime")
	if n != nil {
		notes = append(notes, *n)
	}
	if when == "" {
		// occurrence is required. A vaccine with no date is still worth knowing about, so it is recorded as
		// "date unknown" in the string form FHIR allows, rather than dropped or given an invented date.
		notes = append(notes, Note{Severity: "warning", Path: "Immunization.occurrence",
			Message: fmt.Sprintf("%q carries no date; it was sent with the occurrence \"unknown\"", nameOr(name, code)),
			Rule:    "immunization-undated"})
	}
	id := deterministicID(patientID, "immunization", code, fmt.Sprint(index))
	coded, cn := concept(code, system, name, "Immunization.vaccineCode")
	if cn != nil {
		notes = append(notes, *cn)
	}
	resource := map[string]any{
		"resourceType":  "Immunization",
		"id":            id,
		"meta":          profile("us-core-immunization"),
		"identifier":    []map[string]any{{"system": "urn:perfuse:cda", "value": id}},
		"status":        "completed",
		"vaccineCode":   coded,
		"patient":       patient,
		"primarySource": false,
	}
	if when != "" {
		resource["occurrenceDateTime"] = when
	} else {
		resource["occurrenceString"] = "unknown"
	}
	if entry.NegationInd {
		resource["status"] = "not-done"
		notes = append(notes, Note{Severity: "warning", Path: "Immunization.status",
			Message: fmt.Sprintf("the document records that %q was NOT given; it was sent with status 'not-done' "+
				"rather than dropped", nameOr(name, code)), Rule: "negated-immunization"})
	}
	return resource, notes, true
}

func profile(name string) map[string]any {
	return map[string]any{"profile": []string{usCore + name}}
}

// narrative is a section's or resource's text, escaped as XHTML.
func narrative(text string) map[string]any {
	return narrativeIn(text, "")
}

// narrativeIn is narrative with the document's language on the div, as FHIR asks of a resource that states one.
func narrativeIn(text, lang string) map[string]any {
	var b strings.Builder
	if lang != "" {
		fmt.Fprintf(&b, `<div xmlns="http://www.w3.org/1999/xhtml" lang="%s" xml:lang="%s">`, xhtmlEscape(lang), xhtmlEscape(lang))
	} else {
		b.WriteString(`<div xmlns="http://www.w3.org/1999/xhtml">`)
	}
	for _, para := range strings.Split(text, "\n") {
		if para = strings.TrimSpace(para); para == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(xhtmlEscape(para))
		b.WriteString("</p>")
	}
	b.WriteString("</div>")
	return map[string]any{"status": "generated", "div": b.String()}
}

func xhtmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func authorIdentifier(id string, systems map[string]string) map[string]any {
	if id == "" {
		return nil
	}
	root, ext, ok := strings.Cut(id, "^")
	if !ok {
		return nil
	}
	if root == "2.16.840.1.113883.4.6" && validNPI(ext) {
		return map[string]any{"system": "http://hl7.org/fhir/sid/us-npi", "value": ext}
	}
	if uri, ok := systems[root]; ok {
		return map[string]any{"system": uri, "value": ext}
	}
	return map[string]any{"system": "urn:oid:" + root, "value": ext}
}

func looksLikeOID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return strings.Contains(s, ".")
}

// vitalCodes are the codes FHIR's vital-signs profiles fix ("magic" codes), keyed by the more specific LOINC codes
// a C-CDA often sends instead. FHIR R4 says such an observation also carries the magic code; it is added beside the
// original, never in its place.
var vitalCodes = map[string][2]string{
	"3141-9":  {"29463-7", "Body weight"},
	"29463-7": {"29463-7", "Body weight"},
	"8302-2":  {"8302-2", "Body height"},
	"8306-3":  {"8302-2", "Body height"},
	"8308-9":  {"8302-2", "Body height"},
	"39156-5": {"39156-5", "Body mass index (BMI) [Ratio]"},
	"8310-5":  {"8310-5", "Body temperature"},
	"8331-1":  {"8310-5", "Body temperature"},
	"8332-9":  {"8310-5", "Body temperature"},
	"8333-7":  {"8310-5", "Body temperature"},
	"8334-5":  {"8310-5", "Body temperature"},
	"8867-4":  {"8867-4", "Heart rate"},
	"9279-1":  {"9279-1", "Respiratory rate"},
	"2708-6":  {"2708-6", "Oxygen saturation in Arterial blood"},
	"59408-5": {"2708-6", "Oxygen saturation in Arterial blood"},
	"2710-2":  {"2708-6", "Oxygen saturation in Arterial blood"},
	"9843-4":  {"9843-4", "Head Occipital-frontal circumference"},
	"8287-5":  {"9843-4", "Head Occipital-frontal circumference"},
}

// vitalSigns builds US Core vital signs from a vital signs section. A systolic and a diastolic pressure in one
// organiser become one blood pressure panel (85354-9) with two components, the form FHIR requires; C-CDA sends them
// as two observations.
func vitalSigns(entries []Entry, patient map[string]any, patientID string, usCore bool) ([]map[string]any, []Note) {
	var out []map[string]any
	var notes []Note
	added := false
	index := 0
	emit := func(entry Entry) {
		r, n, ok := observationResource(entry, patient, "vital-signs", patientID, index)
		index++
		notes = append(notes, n...)
		if !ok {
			return
		}
		if usCore {
			r["meta"] = profile("us-core-vital-signs")
		}
		if magic, ok := vitalCodes[entry.Code]; ok && magic[0] != entry.Code {
			if code, _ := r["code"].(map[string]any); code != nil {
				codings, _ := code["coding"].([]map[string]any)
				code["coding"] = append([]map[string]any{{"system": "http://loinc.org", "code": magic[0], "display": magic[1]}}, codings...)
				added = true
			}
		}
		out = append(out, r)
	}
	groups := entries
	for _, organizer := range groups {
		members := organizer.Children
		if organizer.Kind == "Observation" {
			members = []Entry{organizer}
		}
		var systolic, diastolic *Entry
		for i := range members {
			switch members[i].Code {
			case "8480-6":
				systolic = &members[i]
			case "8462-4":
				diastolic = &members[i]
			}
		}
		for i := range members {
			m := members[i]
			if m.Kind != "Observation" {
				continue
			}
			if systolic != nil && diastolic != nil && (m.Code == "8480-6" || m.Code == "8462-4") {
				continue
			}
			emit(m)
		}
		if systolic != nil && diastolic != nil {
			id := deterministicID(patientID, "observation", "bp", systolic.EffectiveTime, fmt.Sprint(index))
			index++
			bp := map[string]any{
				"resourceType": "Observation",
				"id":           id,
				"identifier":   []map[string]any{{"system": "urn:perfuse:cda", "value": id}},
				"status":       observationStatus(systolic.StatusCode),
				"category": []map[string]any{codeableConcept(
					"http://terminology.hl7.org/CodeSystem/observation-category", "vital-signs", "Vital Signs")},
				"code":    codeableConcept("http://loinc.org", "85354-9", "Blood pressure panel with all children optional"),
				"subject": patient,
			}
			when, n := fhirDateTime(firstNonEmpty(systolic.EffectiveTime, systolic.Low, organizer.EffectiveTime, organizer.Low), "Observation.effectiveDateTime")
			if n != nil {
				notes = append(notes, *n)
			}
			if when != "" {
				bp["effectiveDateTime"] = when
			}
			var components []map[string]any
			for _, part := range []*Entry{systolic, diastolic} {
				c := map[string]any{"code": codeableConcept("http://loinc.org", part.Code, firstNonEmpty(part.CodeName,
					map[string]string{"8480-6": "Systolic blood pressure", "8462-4": "Diastolic blood pressure"}[part.Code]))}
				if part.Value != "" {
					q := map[string]any{"value": numberOrString(part.Value), "unit": part.Unit}
					if isUCUM(part.Unit) {
						q["system"], q["code"] = "http://unitsofmeasure.org", part.Unit
					}
					c["valueQuantity"] = q
				} else {
					c["dataAbsentReason"] = codeableConcept("http://terminology.hl7.org/CodeSystem/data-absent-reason", "unknown", "Unknown")
				}
				components = append(components, c)
			}
			bp["component"] = components
			if usCore {
				bp["meta"] = profile("us-core-blood-pressure")
			}
			out = append(out, bp)
			notes = append(notes, Note{Severity: "info", Path: "Observation",
				Message: "a systolic and a diastolic pressure taken together were sent as one blood pressure panel " +
					"(85354-9), the form FHIR requires", Rule: "blood-pressure-panel"})
		}
	}
	if added {
		notes = append(notes, Note{Severity: "info", Path: "Observation.code",
			Message: "vital signs sent with a more specific LOINC code (body weight measured, say) also carry the code " +
				"FHIR's vital-signs profiles fix, beside the original, as FHIR R4 requires", Rule: "vital-sign-code-added"})
	}
	return out, notes
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// bcp47 turns a C-CDA languageCode into a BCP 47 tag, or "" when it is not one. C-CDA requires BCP 47 (en-US), but
// documents in the wild send ISO 639-2 ("eng"), which FHIR refuses.
func bcp47(code string) string {
	code = strings.TrimSpace(code)
	switch strings.ToLower(code) {
	case "eng":
		return "en"
	case "spa":
		return "es"
	}
	lang, region, hasRegion := strings.Cut(code, "-")
	if len(lang) != 2 || !isLetters(lang) {
		return ""
	}
	if !hasRegion {
		return strings.ToLower(lang)
	}
	if len(region) != 2 || !isLetters(region) {
		return ""
	}
	return strings.ToLower(lang) + "-" + strings.ToUpper(region)
}

func isLetters(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return s != ""
}

// validNPI checks an NPI's Luhn check digit, computed with the 80840 prefix as CMS specifies.
func validNPI(npi string) bool {
	if len(npi) != 10 {
		return false
	}
	digits := "80840" + npi
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		n := int(c - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}
