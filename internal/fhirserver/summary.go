package fhirserver

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
)

// Patient/$summary: an International Patient Summary.
//
// The IPS (HL7 FHIR IG hl7.fhir.uv.ips) is the short record a clinician who has never seen the patient needs first:
// problems, allergies, medicines, immunizations, results. It is what a SMART Health Link shares, what the European
// Health Data Space calls the patient summary (exchanged across the EU from 2029), and the operation is defined by the
// IPS guide as Patient/$summary.
//
// It is assembled from what this server holds, never invented. A section with nothing in it says why, with the coded
// empty reason the IPS asks for, so "no allergies recorded here" can never be read as "no known allergies". That
// distinction is the whole safety argument for the three required sections: an empty allergy list that means "nobody
// asked" is how a patient gets a drug they told someone else about.

const (
	loinc            = "http://loinc.org"
	ipsProfile       = "http://hl7.org/fhir/uv/ips/StructureDefinition/Bundle-uv-ips"
	ipsComposition   = "http://hl7.org/fhir/uv/ips/StructureDefinition/Composition-uv-ips"
	emptyReasonCodes = "http://terminology.hl7.org/CodeSystem/list-empty-reason"
	summaryAuthorID  = "perfuse-ips-generator"
	maxSummaryRefs   = 200
)

// ipsSection is one section of the summary and how resources are sorted into it.
type ipsSection struct {
	// display is LOINC's own display for the code, which validators check; title is what a person reads.
	code, display, title string
	required             bool
	// takes reports whether a resource, as a JSON object, belongs here.
	takes func(resourceType string, r map[string]any) bool
}

// ipsSections are the sections built, in the IPS guide's order.
//
// Problems, allergies and medicines are required by the guide and always present. The others appear when there is
// something to put in them, as the guide recommends.
var ipsSections = []ipsSection{
	{"11450-4", "Problem list - Reported", "Problems", true, func(t string, r map[string]any) bool {
		return t == "Condition" && !conditionResolved(r)
	}},
	{"48765-2", "Allergies and adverse reactions Document", "Allergies and intolerances", true, func(t string, r map[string]any) bool {
		return t == "AllergyIntolerance"
	}},
	{"10160-0", "History of Medication use Narrative", "Medication summary", true, func(t string, r map[string]any) bool {
		switch t {
		case "MedicationStatement":
			return true
		case "MedicationRequest":
			// A prescription that was stopped or completed is history, not what the patient takes.
			s := str(r["status"])
			return s == "active" || s == "on-hold" || s == ""
		}
		return false
	}},
	{"11369-6", "History of Immunization note", "History of immunizations", false, func(t string, r map[string]any) bool {
		return t == "Immunization"
	}},
	{"30954-2", "Relevant diagnostic tests/laboratory data note", "Results", false, func(t string, r map[string]any) bool {
		if t == "DiagnosticReport" {
			return true
		}
		return t == "Observation" && (hasCategory(r, "laboratory") || hasCategory(r, "imaging"))
	}},
	{"8716-3", "Vital signs note", "Vital signs", false, func(t string, r map[string]any) bool {
		return t == "Observation" && hasCategory(r, "vital-signs")
	}},
	{"47519-4", "History of Procedures Document", "History of procedures", false, func(t string, r map[string]any) bool {
		return t == "Procedure"
	}},
	{"46264-8", "History of medical device use", "Medical devices", false, func(t string, r map[string]any) bool {
		return t == "DeviceUseStatement" || t == "DeviceUsage"
	}},
	{"11348-0", "History of Past illness note", "History of past illness", false, func(t string, r map[string]any) bool {
		return t == "Condition" && conditionResolved(r)
	}},
	{"29762-2", "Social history note", "Social history", false, func(t string, r map[string]any) bool {
		return t == "Observation" && hasCategory(r, "social-history")
	}},
	{"18776-5", "Plan of care note", "Plan of care", false, func(t string, r map[string]any) bool {
		return t == "CarePlan"
	}},
}

// referencedTypes are the types a summary pulls in when its entries point at them, so the document reads on its own.
var referencedTypes = map[string]bool{
	"Practitioner": true, "PractitionerRole": true, "Organization": true, "Medication": true, "Location": true,
	"Specimen": true, "Device": true,
}

// Summary builds an IPS document Bundle for one patient.
func (s *Store) Summary(ctx context.Context, patientID, baseURL string, caller *Caller, now time.Time) (*fhir.Bundle, error) {
	record, err := s.Everything(ctx, &EverythingRequest{PatientID: patientID}, caller)
	if err != nil {
		return nil, err
	}

	base := strings.TrimRight(baseURL, "/")
	var patient fhir.Resource
	type item struct {
		res fhir.Resource
		obj map[string]any
	}
	var items []item
	for _, r := range record.Resources {
		if r.ResourceTypeName() == "Patient" {
			patient = r
			continue
		}
		obj, err := asObject(r)
		if err != nil {
			continue
		}
		if enteredInError(obj) {
			// A record marked as a mistake is not part of anybody's summary.
			continue
		}
		items = append(items, item{r, obj})
	}
	if patient == nil {
		return nil, ErrNotFound
	}

	date := now.UTC().Format(time.RFC3339)
	comp := &fhir.Composition{
		Status:  "final",
		Type:    &fhir.CodeableConcept{Coding: []fhir.Coding{{System: loinc, Code: "60591-5", Display: "Patient summary Document"}}},
		Subject: &fhir.Reference{Reference: "Patient/" + patientID, Display: patientName(patient)},
		Date:    date,
		Author:  []fhir.Reference{{Reference: "Device/" + summaryAuthorID, Display: "Perfuse"}},
		Title:   "International Patient Summary",
	}
	comp.SetResourceID(fhir.DeterministicUUID("ips-composition", patientID, date))
	comp.SetResourceMeta(&fhir.Meta{Profile: []string{ipsComposition}})

	included := map[string]fhir.Resource{}
	var order []string
	include := func(r fhir.Resource) {
		key := r.ResourceTypeName() + "/" + r.ResourceID()
		if _, ok := included[key]; ok {
			return
		}
		included[key] = r
		order = append(order, key)
	}

	var compText []string
	for _, sec := range ipsSections {
		var entries []fhir.Reference
		var rows []string
		for _, it := range items {
			if !sec.takes(it.res.ResourceTypeName(), it.obj) {
				continue
			}
			key := it.res.ResourceTypeName() + "/" + it.res.ResourceID()
			entries = append(entries, fhir.Reference{Reference: key})
			rows = append(rows, "<li>"+html.EscapeString(describeEntry(it.res.ResourceTypeName(), it.obj))+"</li>")
			include(it.res)
		}
		if len(entries) == 0 && !sec.required {
			continue
		}
		section := fhir.CompositionSection{
			Title: sec.title,
			Code:  &fhir.CodeableConcept{Coding: []fhir.Coding{{System: loinc, Code: sec.code, Display: sec.display}}},
			Entry: entries,
		}
		if len(entries) == 0 {
			// Not "none": this server holds nothing, which is a statement about the server, not about the patient.
			section.EmptyReason = &fhir.CodeableConcept{Coding: []fhir.Coding{{
				System: emptyReasonCodes, Code: "unavailable", Display: "Unavailable"}}}
			section.Text = &fhir.Narrative{Status: "generated", Div: `<div xmlns="http://www.w3.org/1999/xhtml">` +
				"No information about " + html.EscapeString(strings.ToLower(sec.title)) +
				" is held by this server. This does not mean there is none.</div>"}
		} else {
			sort.Strings(rows)
			section.Text = &fhir.Narrative{Status: "generated",
				Div: `<div xmlns="http://www.w3.org/1999/xhtml"><ul>` + strings.Join(rows, "") + "</ul></div>"}
		}
		compText = append(compText, fmt.Sprintf("%s: %d", sec.title, len(entries)))
		comp.Section = append(comp.Section, section)
	}
	comp.Text = &fhir.Narrative{Status: "generated", Div: `<div xmlns="http://www.w3.org/1999/xhtml">` +
		"International Patient Summary for " + html.EscapeString(patientName(patient)) + ", generated " +
		html.EscapeString(date) + " by Perfuse from the records this server holds. " +
		html.EscapeString(strings.Join(compText, "; ")) + ".</div>"}

	// What the entries point at, so a reader of the document does not have to come back to this server for the name of
	// the prescriber or the medicine. Bounded, and only types that describe rather than add clinical facts.
	fetched := 0
	for _, key := range append([]string(nil), order...) {
		obj, _ := asObject(included[key])
		for _, ref := range references(obj) {
			if fetched >= maxSummaryRefs {
				break
			}
			typ, id, ok := strings.Cut(ref, "/")
			if !ok || !referencedTypes[typ] || included[ref] != nil {
				continue
			}
			r, err := s.Get(ctx, typ, id)
			if err != nil || !permitsResource(caller, r) {
				continue
			}
			fetched++
			include(r)
		}
	}

	author := &fhir.Device{
		Status:     "active",
		DeviceName: []fhir.DeviceName{{Name: "Perfuse", Type: "manufacturer-name"}},
	}
	author.SetResourceID(summaryAuthorID)

	b := &fhir.Bundle{
		Identifier: &fhir.Identifier{System: "urn:ietf:rfc:3986", Value: "urn:uuid:" + comp.ResourceID()},
		Type:       fhir.BundleDocument,
		Timestamp:  date,
	}
	b.SetResourceMeta(&fhir.Meta{Profile: []string{ipsProfile}})
	full := func(r fhir.Resource) string { return base + "/" + r.ResourceTypeName() + "/" + r.ResourceID() }
	b.Entry = append(b.Entry,
		fhir.BundleEntry{FullURL: full(comp), Resource: comp},
		fhir.BundleEntry{FullURL: full(patient), Resource: patient},
		fhir.BundleEntry{FullURL: full(author), Resource: author},
	)
	for _, key := range order {
		b.Entry = append(b.Entry, fhir.BundleEntry{FullURL: full(included[key]), Resource: included[key]})
	}
	return b, nil
}

func asObject(r fhir.Resource) (map[string]any, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	return obj, json.Unmarshal(raw, &obj)
}

// codeOf returns the first code in a CodeableConcept-shaped value (or R5 CodeableReference's concept).
func codeOf(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if c, ok := m["concept"]; ok {
		return codeOf(c)
	}
	if codings, ok := m["coding"].([]any); ok {
		for _, c := range codings {
			if cm, ok := c.(map[string]any); ok && str(cm["code"]) != "" {
				return str(cm["code"])
			}
		}
	}
	return ""
}

// textOf is how a coded value reads to a person: its text, else the first display, else the first code.
func textOf(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if c, ok := m["concept"]; ok {
		return textOf(c)
	}
	if t := str(m["text"]); t != "" {
		return t
	}
	if codings, ok := m["coding"].([]any); ok {
		for _, c := range codings {
			if cm, ok := c.(map[string]any); ok && str(cm["display"]) != "" {
				return str(cm["display"])
			}
		}
		for _, c := range codings {
			if cm, ok := c.(map[string]any); ok && str(cm["code"]) != "" {
				return str(cm["code"])
			}
		}
	}
	if d := str(m["display"]); d != "" {
		return d
	}
	return ""
}

func hasCategory(r map[string]any, want string) bool {
	cats, _ := r["category"].([]any)
	for _, c := range cats {
		if codeOf(c) == want {
			return true
		}
	}
	return false
}

func conditionResolved(r map[string]any) bool {
	switch codeOf(r["clinicalStatus"]) {
	case "resolved", "inactive", "remission":
		return true
	}
	return false
}

func enteredInError(r map[string]any) bool {
	return str(r["status"]) == "entered-in-error" || codeOf(r["verificationStatus"]) == "entered-in-error"
}

// describeEntry is one line of narrative for a resource: what it is and when.
func describeEntry(resourceType string, r map[string]any) string {
	var what string
	for _, key := range []string{"code", "vaccineCode", "medicationCodeableConcept", "medication", "type"} {
		if what = textOf(r[key]); what != "" {
			break
		}
	}
	if what == "" {
		if ref, ok := r["medicationReference"].(map[string]any); ok {
			what = str(ref["display"])
		}
	}
	if what == "" {
		what = "(not coded) " + resourceType
	}

	var when string
	for _, key := range []string{"effectiveDateTime", "occurrenceDateTime", "onsetDateTime", "performedDateTime",
		"authoredOn", "recordedDate", "issued", "dateAsserted"} {
		if when = str(r[key]); when != "" {
			break
		}
	}
	if when == "" {
		if p, ok := r["effectivePeriod"].(map[string]any); ok {
			when = str(p["start"])
		}
	}

	line := what
	if v, ok := r["valueQuantity"].(map[string]any); ok {
		line += fmt.Sprintf(": %v %s", v["value"], str(v["unit"]))
	} else if t := textOf(r["valueCodeableConcept"]); t != "" {
		line += ": " + t
	} else if t := str(r["valueString"]); t != "" {
		line += ": " + t
	}
	if crit := str(r["criticality"]); crit != "" {
		line += " (criticality " + crit + ")"
	}
	if when != "" {
		line += ", " + when
	}
	return line
}

// references lists the "Type/id" references anywhere in a resource.
func references(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if k == "reference" {
					if s := str(x); s != "" && !strings.Contains(s, "://") && !strings.HasPrefix(s, "#") {
						out = append(out, s)
					}
					continue
				}
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	sort.Strings(out)
	return out
}

func patientName(r fhir.Resource) string {
	p, ok := r.(*fhir.Patient)
	if !ok || len(p.Name) == 0 {
		return "the patient"
	}
	n := p.Name[0]
	if n.Text != "" {
		return n.Text
	}
	parts := append([]string{}, n.Given...)
	if n.Family != "" {
		parts = append(parts, n.Family)
	}
	if len(parts) == 0 {
		return "the patient"
	}
	return strings.Join(parts, " ")
}
