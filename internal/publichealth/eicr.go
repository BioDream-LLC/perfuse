package publichealth

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The eICR here is HL7 FHIR eCR 2.1.2 (hl7.fhir.us.ecr), the release the eCR Now and AIMS pipelines accept. It is built from
// what a v2 message converted to FHIR already holds - the Patient, Encounter, Conditions and lab Observations - rather than
// from a form, because a case report is triggered by what a clinician recorded, and that arrives as messages.

const (
	ecrBase = "http://hl7.org/fhir/us/ecr/StructureDefinition/"
	// rctcOID is the Reportable Conditions Trigger Codes value set, which the eICR's trigger code flag names when the code was
	// matched against the RCTC itself.
	rctcOID          = "2.16.840.1.114222.4.11.7508"
	builtinRCTCLabel = "perfuse-builtin-sample"
	triggerFlagURL   = ecrBase + "eicr-trigger-code-flag-extension"
	darURL           = "http://hl7.org/fhir/StructureDefinition/data-absent-reason"
)

// TriggerSet is the codes whose presence makes an encounter reportable, each with the value set it came from.
type TriggerSet struct {
	// Source says where the codes came from, for the report: a file name, or the built-in sample.
	Source string
	codes  map[string]triggerCode // "system|code"
	// prefixes are ICD-10 categories (A15) that match every code beneath them (A15.0), as the built-in list is written.
	prefixes map[string]triggerCode
}

type triggerCode struct {
	// Display is the code's display as the value set gives it, empty for the built-in list; never the sender's text, which the
	// HL7 validator rightly rejects as a display for a standard code.
	Display, ValueSet, Version, Condition string
}

var systemURLs = map[string]string{
	"SNOMED": "http://snomed.info/sct",
	"ICD-10": "http://hl7.org/fhir/sid/icd-10-cm",
	"LOINC":  "http://loinc.org",
}

// BuiltinTriggers is the short list of common reportable conditions that ships with Perfuse. It is a sample, not the RCTC:
// jurisdictions report from the RCTC, which is published through the eRSD and needs a UMLS licence, so it cannot be bundled.
// Load the real one with LoadTriggers for anything sent to public health.
func BuiltinTriggers() *TriggerSet {
	ts := &TriggerSet{Source: "Perfuse's built-in sample of reportable conditions (not the RCTC)", codes: map[string]triggerCode{}, prefixes: map[string]triggerCode{}}
	for _, e := range ReportableConditions {
		tc := triggerCode{ValueSet: rctcOID, Version: builtinRCTCLabel, Condition: e.Condition}
		sys := systemURLs[e.System]
		if e.System == "ICD-10" {
			ts.prefixes[sys+"|"+e.Code] = tc
		}
		ts.codes[sys+"|"+e.Code] = tc
	}
	return ts
}

// LoadTriggers reads trigger codes from a FHIR ValueSet, or a Bundle of them such as the eRSD specification bundle, using each
// value set's expansion where it has one and its enumerated concepts otherwise.
func LoadTriggers(path string) (*TriggerSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: not JSON: %w", path, err)
	}
	ts := &TriggerSet{Source: path, codes: map[string]triggerCode{}, prefixes: map[string]triggerCode{}}
	var add func(vs map[string]any)
	add = func(vs map[string]any) {
		oid := valueSetOID(vs)
		version, _ := vs["version"].(string)
		title := str(vs, "title")
		if title == "" {
			title = str(vs, "name")
		}
		put := func(system, code, display string) {
			if system == "" || code == "" {
				return
			}
			if _, seen := ts.codes[system+"|"+code]; !seen {
				ts.codes[system+"|"+code] = triggerCode{Display: display, ValueSet: oid, Version: version, Condition: title}
			}
		}
		var walk func(items []any)
		walk = func(items []any) {
			for _, it := range items {
				c, _ := it.(map[string]any)
				put(str(c, "system"), str(c, "code"), str(c, "display"))
				walk(arr(c, "contains"))
			}
		}
		if exp, ok := vs["expansion"].(map[string]any); ok {
			walk(arr(exp, "contains"))
		}
		if comp, ok := vs["compose"].(map[string]any); ok {
			for _, inc := range arr(comp, "include") {
				m, _ := inc.(map[string]any)
				for _, c := range arr(m, "concept") {
					cm, _ := c.(map[string]any)
					put(str(m, "system"), str(cm, "code"), str(cm, "display"))
				}
			}
		}
	}
	switch doc["resourceType"] {
	case "ValueSet":
		add(doc)
	case "Bundle":
		for _, e := range arr(doc, "entry") {
			if r, ok := e.(map[string]any)["resource"].(map[string]any); ok && r["resourceType"] == "ValueSet" {
				add(r)
			}
		}
	default:
		return nil, fmt.Errorf("%s: expected a ValueSet or a Bundle of ValueSets, got %v", path, doc["resourceType"])
	}
	if len(ts.codes) == 0 {
		return nil, fmt.Errorf("%s: no codes found; a value set needs an expansion or enumerated concepts", path)
	}
	return ts, nil
}

// Len is the number of codes loaded.
func (t *TriggerSet) Len() int { return len(t.codes) }

func (t *TriggerSet) match(system, code string) (triggerCode, bool) {
	if tc, ok := t.codes[system+"|"+code]; ok {
		return tc, true
	}
	if i := strings.IndexByte(code, '.'); i > 0 {
		if tc, ok := t.prefixes[system+"|"+code[:i]]; ok {
			return tc, true
		}
	}
	return triggerCode{}, false
}

func valueSetOID(vs map[string]any) string {
	for _, id := range arr(vs, "identifier") {
		if v := str(id.(map[string]any), "value"); strings.HasPrefix(v, "urn:oid:") {
			return strings.TrimPrefix(v, "urn:oid:")
		}
	}
	url := str(vs, "url")
	if i := strings.LastIndexByte(url, '/'); i >= 0 && strings.Count(url[i+1:], ".") > 3 {
		return url[i+1:]
	}
	return rctcOID
}

// Trigger is one code in the source that made the encounter reportable.
type Trigger struct {
	Resource string `json:"resource"`
	System   string `json:"system"`
	Code     string `json:"code"`
	Display  string `json:"display,omitempty"`
	// CodeDisplay is the value set's display for the code, the only one put on the trigger flag.
	CodeDisplay string `json:"-"`
	Condition   string `json:"condition,omitempty"`
	ValueSet    string `json:"valueSet"`
	Version     string `json:"valueSetVersion"`
}

// EICR is a built case report and what went into it.
type EICR struct {
	Bundle   map[string]any `json:"bundle,omitempty"`
	Triggers []Trigger      `json:"triggers"`
	// Notes are what the report could not take from the source and marked as absent, which a public health agency will see.
	Notes []string `json:"notes"`
}

// EICROptions are the parts of a report that do not come from the source message.
type EICROptions struct {
	Now time.Time
	// Facility is the reporting facility as the agency knows it. eCR requires its address and phone, which a v2 message does not
	// carry, so they come from configuration.
	Facility Facility
}

// Facility is the organisation that sends case reports: its name, NPI, and how to reach it.
type Facility struct {
	Name       string `json:"name" yaml:"name"`
	NPI        string `json:"npi" yaml:"npi"`
	Phone      string `json:"phone" yaml:"phone"`
	Line       string `json:"line" yaml:"line"`
	City       string `json:"city" yaml:"city"`
	State      string `json:"state" yaml:"state"`
	PostalCode string `json:"postalCode" yaml:"postalCode"`
}

// LoadFacility reads a Facility from a JSON file.
func LoadFacility(path string) (Facility, error) {
	var f Facility
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Missing lists what eCR requires of the facility that is not set.
func (f Facility) Missing() []string {
	var out []string
	for _, x := range []struct{ v, name string }{{f.Phone, "phone"}, {f.City, "city"}, {f.State, "state"}} {
		if strings.TrimSpace(x.v) == "" {
			out = append(out, x.name)
		}
	}
	return out
}

func (f Facility) address() map[string]any {
	a := map[string]any{"use": "work"}
	if f.Line != "" {
		a["line"] = []any{f.Line}
	}
	for k, v := range map[string]string{"city": f.City, "state": f.State, "postalCode": f.PostalCode} {
		if v != "" {
			a[k] = v
		}
	}
	a["country"] = "US"
	return a
}

func (f Facility) telecom() []any {
	if f.Phone == "" {
		return nil
	}
	return []any{map[string]any{"system": "phone", "value": f.Phone, "use": "work"}}
}

// FindTriggers lists the trigger codes in a FHIR bundle's Conditions, Observations (code and coded value) and ServiceRequests.
func FindTriggers(source []byte, triggers *TriggerSet) ([]Trigger, error) {
	res, err := resources(source)
	if err != nil {
		return nil, err
	}
	var out []Trigger
	for _, r := range res {
		out = append(out, triggersIn(r, triggers)...)
	}
	return out, nil
}

func triggersIn(r map[string]any, triggers *TriggerSet) []Trigger {
	var concepts []any
	switch r["resourceType"] {
	case "Condition", "ServiceRequest":
		concepts = []any{r["code"]}
	case "Observation":
		concepts = []any{r["code"], r["valueCodeableConcept"]}
	default:
		return nil
	}
	var out []Trigger
	for _, c := range concepts {
		cc, _ := c.(map[string]any)
		for _, cd := range arr(cc, "coding") {
			m, _ := cd.(map[string]any)
			if tc, ok := triggers.match(str(m, "system"), str(m, "code")); ok {
				d := str(m, "display")
				if d == "" {
					d = str(cc, "text")
				}
				out = append(out, Trigger{Resource: r["resourceType"].(string) + "/" + str(r, "id"), System: str(m, "system"), Code: str(m, "code"),
					Display: d, CodeDisplay: tc.Display, Condition: tc.Condition, ValueSet: tc.ValueSet, Version: tc.Version})
			}
		}
	}
	return out
}

// BuildEICR makes an eICR document bundle from a FHIR bundle converted from a clinical message. It refuses a source with no
// trigger code: an eICR is only ever sent because something reportable was recorded.
func BuildEICR(source []byte, triggers *TriggerSet, opts EICROptions) (*EICR, error) {
	if triggers == nil {
		triggers = BuiltinTriggers()
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	res, err := resources(source)
	if err != nil {
		return nil, err
	}
	out := &EICR{Triggers: []Trigger{}, Notes: []string{}}
	byType := map[string][]map[string]any{}
	for _, r := range res {
		byType[str(r, "resourceType")] = append(byType[str(r, "resourceType")], r)
	}
	for _, r := range res {
		out.Triggers = append(out.Triggers, triggersIn(r, triggers)...)
	}
	if len(out.Triggers) == 0 {
		return out, fmt.Errorf("nothing reportable: no code in the message is in %s", triggers.Source)
	}
	if len(byType["Patient"]) == 0 {
		return out, fmt.Errorf("the message names no patient, and an eICR is about one")
	}
	if len(byType["Encounter"]) == 0 {
		return out, fmt.Errorf("the message has no encounter (PV1), and an eICR reports one")
	}
	note := func(format string, a ...any) { out.Notes = append(out.Notes, fmt.Sprintf(format, a...)) }
	if triggers.codes != nil && strings.HasPrefix(triggers.Source, "Perfuse's built-in") {
		note("triggered from %s; load the RCTC (-rctc) before reporting to a public health agency", triggers.Source)
	}

	// Every resource gets a real urn:uuid, and every reference is rewritten to it, so the document resolves within itself.
	fullURL := map[string]string{}
	for _, r := range res {
		key := str(r, "resourceType") + "/" + str(r, "id")
		fullURL[key] = "urn:uuid:" + fhir.DeterministicUUID("eicr", key)
	}
	for _, r := range res {
		rewriteRefs(r, fullURL)
		delete(r, "meta")
	}
	ref := func(r map[string]any) map[string]any {
		return map[string]any{"reference": fullURL[str(r, "resourceType")+"/"+str(r, "id")]}
	}

	patient := byType["Patient"][0]
	encounter := byType["Encounter"][0]
	fixPatient(patient, note)
	fixEncounter(encounter, fullURL["Encounter/"+str(encounter, "id")], note)
	patient["meta"] = profile(ecrBase + "us-ph-patient")
	encounter["meta"] = profile(ecrBase + "eicr-encounter")
	if miss := opts.Facility.Missing(); len(miss) > 0 {
		note("the reporting facility's %s is not configured, and eCR requires it: the report is incomplete", strings.Join(miss, ", "))
	}

	// The facility: the encounter's service provider, else the first organisation, else one made from the configuration.
	var org map[string]any
	if sp, ok := encounter["serviceProvider"].(map[string]any); ok {
		for _, o := range byType["Organization"] {
			if fullURL["Organization/"+str(o, "id")] == str(sp, "reference") {
				org = o
			}
		}
	}
	if org == nil && len(byType["Organization"]) > 0 {
		org = byType["Organization"][0]
	}
	if org == nil {
		if opts.Facility.Name == "" {
			return out, fmt.Errorf("the message names no facility (MSH-4 or PV1-3) and none is configured, and an eICR needs a custodian")
		}
		org = map[string]any{"resourceType": "Organization", "id": "facility", "active": true}
		fullURL["Organization/facility"] = "urn:uuid:" + fhir.DeterministicUUID("eicr", "Organization/facility", opts.Facility.Name)
		res = append(res, org)
		byType["Organization"] = append(byType["Organization"], org)
	}
	if opts.Facility.Name != "" {
		org["name"] = opts.Facility.Name
	}
	if opts.Facility.NPI != "" {
		org["identifier"] = []any{map[string]any{"system": "http://hl7.org/fhir/sid/us-npi", "value": opts.Facility.NPI}}
	}
	if t := opts.Facility.telecom(); t != nil {
		org["telecom"] = t
	}
	org["address"] = []any{opts.Facility.address()}
	org["meta"] = profile(ecrBase + "us-ph-organization")
	orgRef := map[string]any{"reference": fullURL["Organization/"+str(org, "id")]}
	if _, ok := encounter["serviceProvider"]; !ok {
		encounter["serviceProvider"] = orgRef
	}

	// Locations are at the facility's address; v2 names a ward or room (PV1-3) but never where the building is.
	for _, l := range byType["Location"] {
		if len(arr(l, "identifier")) == 0 && str(l, "name") != "" {
			l["identifier"] = []any{map[string]any{"value": str(l, "name")}}
		}
		if len(arr(l, "type")) == 0 {
			code, text := locationType(encounter)
			t := map[string]any{"text": text}
			if code != "" {
				t["coding"] = []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/v3-RoleCode", "code": code, "display": text}}
			}
			l["type"] = []any{t}
		}
		l["address"] = opts.Facility.address()
		if _, ok := l["managingOrganization"]; !ok {
			l["managingOrganization"] = orgRef
		}
		l["meta"] = profile(ecrBase + "us-ph-location")
	}
	if len(arr(encounter, "location")) == 0 {
		note("no location (PV1-3) in the message, and eCR requires one: the report is incomplete")
	}

	// eCR names clinicians through PractitionerRole, the clinician at this facility, so each Practitioner gets one, reached at
	// the facility's number, and the encounter's participants point at it.
	roleOf := map[string]string{}
	for _, pr := range byType["Practitioner"] {
		key := "Practitioner/" + str(pr, "id")
		roleKey := "PractitionerRole/" + str(pr, "id") + "-role"
		role := map[string]any{
			"resourceType": "PractitionerRole", "id": str(pr, "id") + "-role",
			"meta":         profile(ecrBase + "us-ph-practitionerrole"),
			"practitioner": map[string]any{"reference": fullURL[key]},
			"organization": orgRef,
		}
		if ids := arr(pr, "identifier"); len(ids) > 0 {
			role["identifier"] = ids[:1]
		} else {
			note("clinician %s has no identifier (XCN.1), and eCR requires one: the report is incomplete", key)
		}
		if t := opts.Facility.telecom(); t != nil {
			role["telecom"] = t
		}
		fullURL[roleKey] = "urn:uuid:" + fhir.DeterministicUUID("eicr", roleKey)
		roleOf[fullURL[key]] = fullURL[roleKey]
		res = append(res, role)
		byType["PractitionerRole"] = append(byType["PractitionerRole"], role)
	}
	for _, p := range arr(encounter, "participant") {
		pm, _ := p.(map[string]any)
		if ind, ok := pm["individual"].(map[string]any); ok {
			if r, ok := roleOf[str(ind, "reference")]; ok {
				ind["reference"] = r
				delete(ind, "type")
			}
		}
	}
	for _, c := range byType["Condition"] {
		c["meta"] = profile(ecrBase + "us-ph-condition")
	}
	for _, o := range byType["Observation"] {
		if isLab(o) {
			o["meta"] = profile(ecrBase + "us-ph-lab-result-observation")
		}
	}

	// The trigger code flag goes on the Encounter's diagnosis for a Condition, and on the section entry for anything else.
	flagged := map[string][]Trigger{}
	for _, t := range out.Triggers {
		flagged[t.Resource] = append(flagged[t.Resource], t)
	}
	var diagnoses []any
	seen := map[string]bool{}
	for _, d := range arr(encounter, "diagnosis") {
		dm, _ := d.(map[string]any)
		if c, ok := dm["condition"].(map[string]any); ok {
			seen[str(c, "reference")] = true
		}
		diagnoses = append(diagnoses, d)
	}
	for _, c := range byType["Condition"] {
		u := fullURL["Condition/"+str(c, "id")]
		var dm map[string]any
		for _, d := range diagnoses {
			if cc, _ := d.(map[string]any)["condition"].(map[string]any); str(cc, "reference") == u {
				dm = d.(map[string]any)
			}
		}
		if dm == nil {
			if seen[u] {
				continue
			}
			dm = map[string]any{"condition": map[string]any{"reference": u}}
			diagnoses = append(diagnoses, dm)
		}
		if ts := flagged["Condition/"+str(c, "id")]; len(ts) > 0 {
			dm["extension"] = flags(ts)
		}
	}
	if len(diagnoses) > 0 {
		encounter["diagnosis"] = diagnoses
	}

	// The organisation that holds the record is the custodian; the attending clinician, or failing that the organisation, is
	// the author.
	custodian := orgRef
	author := orgRef
	if rs := byType["PractitionerRole"]; len(rs) > 0 {
		author = ref(rs[0])
	}

	entries := func(rs []map[string]any) []any {
		var out []any
		for _, r := range rs {
			e := ref(r)
			if ts := flagged[str(r, "resourceType")+"/"+str(r, "id")]; len(ts) > 0 && r["resourceType"] != "Condition" {
				e["extension"] = flags(ts)
			}
			out = append(out, e)
		}
		return out
	}
	var labs []map[string]any
	for _, o := range byType["Observation"] {
		labs = append(labs, o)
	}
	labs = append(labs, byType["DiagnosticReport"]...)

	reason := reasonText(encounter)
	sections := []any{
		section("Reason for Visit", "29299-5", "Reason for visit Narrative", reason, nil, "the reason for the visit (PV2-3)", note),
		section("Chief Complaint", "10154-3", "Chief complaint Narrative - Reported", "", nil, "the chief complaint", note),
		section("History of Present Illness", "10164-2", "History of Present illness Narrative", "", nil, "the history of present illness", note),
		section("Problems", "11450-4", "Problem list - Reported", describe(byType["Condition"]), entries(byType["Condition"]), "", note),
		section("Medications Administered", "29549-3", "Medication administered Narrative", describe(byType["MedicationAdministration"]),
			entries(byType["MedicationAdministration"]), "", note),
		section("Results", "30954-2", "Relevant diagnostic tests/laboratory data note", describe(labs), entries(labs), "", note),
		section("Social History", "29762-2", "Social history note", "", nil, "social history", note),
	}
	if sr := byType["ServiceRequest"]; len(sr) > 0 {
		sections = append(sections, section("Plan of Treatment", "18776-5", "Plan of care note", describe(sr), entries(sr), "", note))
	}
	if im := byType["Immunization"]; len(im) > 0 {
		sections = append(sections, section("Immunizations", "11369-6", "History of Immunization Narrative", describe(im), entries(im), "", note))
	}

	docID := fhir.DeterministicUUID("eicr-document", fullURL["Encounter/"+str(encounter, "id")], opts.Now.UTC().Format(time.RFC3339Nano))
	stamp := opts.Now.Format(time.RFC3339)
	composition := map[string]any{
		"resourceType": "Composition",
		"id":           docID,
		"meta":         profile(ecrBase + "eicr-composition"),
		"text":         narrative("Initial public health case report: " + triggerSummary(out.Triggers)),
		"extension": []any{map[string]any{
			"url": "http://hl7.org/fhir/StructureDefinition/composition-clinicaldocument-versionNumber", "valueString": "1",
		}},
		"identifier": map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + docID},
		"status":     "final",
		"type": map[string]any{"coding": []any{map[string]any{
			"system": "http://loinc.org", "code": "55751-2", "display": "Public Health Case Report",
		}}},
		"subject":   ref(patient),
		"encounter": ref(encounter),
		"date":      stamp,
		"author":    []any{author},
		"title":     "Initial Public Health Case Report",
		"custodian": custodian,
		"section":   sections,
	}

	compURL := "urn:uuid:" + docID
	bundleEntries := []any{map[string]any{"fullUrl": compURL, "resource": composition}}
	keys := make([]string, 0, len(res))
	byKey := map[string]map[string]any{}
	for _, r := range res {
		k := str(r, "resourceType") + "/" + str(r, "id")
		keys = append(keys, k)
		byKey[k] = r
	}
	sort.SliceStable(keys, func(i, j int) bool { return order(keys[i]) < order(keys[j]) })
	for _, k := range keys {
		bundleEntries = append(bundleEntries, map[string]any{"fullUrl": fullURL[k], "resource": byKey[k]})
	}
	out.Bundle = map[string]any{
		"resourceType": "Bundle",
		"meta":         profile(ecrBase + "eicr-document-bundle"),
		"identifier":   map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + fhir.DeterministicUUID("eicr-bundle", docID)},
		"type":         "document",
		"timestamp":    stamp,
		"entry":        bundleEntries,
	}
	return out, nil
}

func isLab(o map[string]any) bool {
	for _, c := range arr(o, "category") {
		for _, cd := range arr(c.(map[string]any), "coding") {
			if str(cd.(map[string]any), "code") == "laboratory" {
				return true
			}
		}
	}
	return false
}

// locationType names the kind of place from the encounter class (v3 RoleCode), since v2's PV1-3 names the place but not
// what it is.
func locationType(e map[string]any) (string, string) {
	c, _ := e["class"].(map[string]any)
	switch str(c, "code") {
	case "EMER":
		return "ER", "Emergency room"
	case "IMP", "ACUTE", "NONAC":
		return "HOSP", "Hospital"
	case "AMB":
		return "OF", "Outpatient facility"
	}
	return "", "Healthcare facility"
}

// order puts the subject and encounter straight after the Composition, which is how a reader expects a document to open.
func order(key string) int {
	for i, t := range []string{"Patient/", "Encounter/", "Condition/", "Observation/", "DiagnosticReport/", "ServiceRequest/"} {
		if strings.HasPrefix(key, t) {
			return i
		}
	}
	return 99
}

func resources(source []byte) ([]map[string]any, error) {
	var b map[string]any
	if err := json.Unmarshal(source, &b); err != nil {
		return nil, fmt.Errorf("the source is not a FHIR bundle: %w", err)
	}
	if b["resourceType"] != "Bundle" {
		return nil, fmt.Errorf("the source is a %v, not a Bundle", b["resourceType"])
	}
	var out []map[string]any
	for _, e := range arr(b, "entry") {
		if r, ok := e.(map[string]any)["resource"].(map[string]any); ok && str(r, "id") != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

func rewriteRefs(v any, fullURL map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		if r, ok := t["reference"].(string); ok {
			if u, ok := fullURL[r]; ok {
				t["reference"] = u
			}
		}
		for _, c := range t {
			rewriteRefs(c, fullURL)
		}
	case []any:
		for _, c := range t {
			rewriteRefs(c, fullURL)
		}
	}
}

// fixPatient fills what us-ph-patient requires and a v2 PID may not carry, marking each with a data-absent-reason rather than
// inventing it.
func fixPatient(p map[string]any, note func(string, ...any)) {
	exts := arr(p, "extension")
	has := func(suffix string) bool {
		for _, e := range exts {
			if strings.HasSuffix(str(e.(map[string]any), "url"), suffix) {
				return true
			}
		}
		return false
	}
	for _, x := range []struct{ suffix, label string }{{"us-core-race", "race (PID-10)"}, {"us-core-ethnicity", "ethnicity (PID-22)"}} {
		if !has(x.suffix) {
			// Only the text, which US Core requires and allows alone; eCR fixes the absent reason to "masked", which would claim
			// the facility withheld something it never had.
			exts = append(exts, map[string]any{
				"url":       "http://hl7.org/fhir/us/core/StructureDefinition/" + x.suffix,
				"extension": []any{map[string]any{"url": "text", "valueString": "Unknown"}},
			})
			note("%s not in the message: sent as unknown", x.label)
		}
	}
	p["extension"] = exts
	for _, f := range []struct{ field, label string }{
		{"identifier", "patient identifier (PID-3)"}, {"name", "patient name (PID-5)"}, {"telecom", "patient phone (PID-13)"},
		{"address", "patient address (PID-11)"}, {"communication", "preferred language (PID-15)"},
	} {
		if len(arr(p, f.field)) > 0 {
			continue
		}
		if f.field == "communication" {
			// BCP 47's "und" is the code for a language that was not determined.
			p[f.field] = []any{map[string]any{"language": map[string]any{
				"coding": []any{map[string]any{"system": "urn:ietf:bcp:47", "code": "und", "display": "Undetermined"}}, "text": "Not recorded",
			}}}
			note("%s not in the message: sent as undetermined (und)", f.label)
			continue
		}
		// eCR allows only "masked" as a reason here, so a missing value is left missing, and the report will not validate.
		note("%s not in the message, and eCR requires it: the report is incomplete", f.label)
	}
	if _, ok := p["birthDate"]; !ok {
		note("birth date (PID-7) not in the message, and eCR requires it: the report is incomplete")
	}
	if _, ok := p["gender"]; !ok {
		p["gender"] = "unknown"
		note("administrative sex (PID-8) not in the message: sent as unknown")
	}
	_, b := p["deceasedBoolean"]
	_, d := p["deceasedDateTime"]
	if !b && !d {
		// A message about a current encounter that does not say the patient died (PID-29, PID-30) is about a living patient.
		p["deceasedBoolean"] = false
		note("PID-30 empty: the patient is reported as living")
	}
}

func fixEncounter(e map[string]any, selfURL string, note func(string, ...any)) {
	if ids := arr(e, "identifier"); len(ids) > 1 {
		e["identifier"] = ids[:1]
	} else if len(ids) == 0 {
		// eCR requires a system and value, so with no visit number the encounter is identified by the URI it has in this
		// report. That is honest, and stable for the same message, but it will not match the facility's own record.
		e["identifier"] = []any{map[string]any{"system": "urn:ietf:rfc:3986", "value": selfURL}}
		note("visit number (PV1-19) not in the message: the encounter is identified by its id in this report, which the facility's records will not match")
	}
	if len(arr(e, "type")) == 0 {
		// eCR requires a type; v2 has none beyond the class (PV1-2), so the class is what it says, in words.
		text := "Encounter"
		if c, ok := e["class"].(map[string]any); ok && str(c, "display") != "" {
			text = str(c, "display") + " encounter"
		}
		e["type"] = []any{map[string]any{"text": text}}
	}
	p, _ := e["period"].(map[string]any)
	if p == nil {
		p = map[string]any{}
		e["period"] = p
	}
	if _, ok := p["start"]; !ok {
		p["_start"] = map[string]any{"extension": []any{map[string]any{"url": darURL, "valueCode": "unknown"}}}
		note("encounter start (PV1-44) not in the message: sent as unknown")
	}
}

func reasonText(e map[string]any) string {
	var parts []string
	for _, r := range arr(e, "reasonCode") {
		parts = append(parts, conceptText(r.(map[string]any)))
	}
	return strings.Join(parts, "; ")
}

func conceptText(cc map[string]any) string {
	if t := str(cc, "text"); t != "" {
		return t
	}
	for _, c := range arr(cc, "coding") {
		m := c.(map[string]any)
		if d := str(m, "display"); d != "" {
			return d
		}
		return str(m, "code")
	}
	return ""
}

// describe is a section's narrative: one line per resource, in words.
func describe(rs []map[string]any) string {
	var lines []string
	for _, r := range rs {
		line := ""
		if cc, ok := r["code"].(map[string]any); ok {
			line = conceptText(cc)
		} else if cc, ok := r["vaccineCode"].(map[string]any); ok {
			line = conceptText(cc)
		} else if cc, ok := r["medicationCodeableConcept"].(map[string]any); ok {
			line = conceptText(cc)
		}
		if q, ok := r["valueQuantity"].(map[string]any); ok {
			line += fmt.Sprintf(": %v %s", q["value"], str(q, "unit"))
		} else if cc, ok := r["valueCodeableConcept"].(map[string]any); ok {
			line += ": " + conceptText(cc)
		} else if s := str(r, "valueString"); s != "" {
			line += ": " + s
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func section(title, code, display, text string, entries []any, missing string, note func(string, ...any)) map[string]any {
	s := map[string]any{
		"title": title,
		"code":  map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": code, "display": display}}},
	}
	if text == "" {
		text = "No information."
		if missing != "" {
			text = "Not recorded in the source message."
			note("%s is not carried by the message: the section says so", missing)
		}
		s["emptyReason"] = map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/list-empty-reason", "code": "unavailable", "display": "Unavailable",
		}}}
	}
	s["text"] = narrative(text)
	if len(entries) > 0 {
		s["entry"] = entries
		delete(s, "emptyReason")
	}
	return s
}

func narrative(text string) map[string]any {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = html.EscapeString(l)
	}
	return map[string]any{"status": "generated", "div": `<div xmlns="http://www.w3.org/1999/xhtml"><p>` + strings.Join(lines, "<br/>") + "</p></div>"}
}

func flags(ts []Trigger) []any {
	var out []any
	for _, t := range ts {
		coding := map[string]any{"system": t.System, "code": t.Code}
		if t.CodeDisplay != "" {
			coding["display"] = t.CodeDisplay
		}
		out = append(out, map[string]any{"url": triggerFlagURL, "extension": []any{
			map[string]any{"url": "triggerCodeValueSet", "valueOid": "urn:oid:" + t.ValueSet},
			map[string]any{"url": "triggerCodeValueSetVersion", "valueString": t.Version},
			map[string]any{"url": "triggerCode", "valueCoding": coding},
		}})
	}
	return out
}

func triggerSummary(ts []Trigger) string {
	var names []string
	seen := map[string]bool{}
	for _, t := range ts {
		n := t.Condition
		if n == "" {
			n = t.Display
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

func profile(url string) map[string]any { return map[string]any{"profile": []any{url}} }

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func arr(m map[string]any, k string) []any {
	if m == nil {
		return nil
	}
	a, _ := m[k].([]any)
	return a
}

// ReportingOptions say where a case report goes and where it comes from, for the eCR message header.
type ReportingOptions struct {
	// Destination is the public health endpoint's base URL (AIMS, or the agency's own).
	Destination string
	// Source is this sender's endpoint, where the agency's Reportability Response is sent back.
	Source string
	// Event is why the report is sent now: a named event from eCR's trigger definitions, such as encounter-change.
	Event string
	Now   time.Time
}

// ReportingBundle wraps an eICR in the eCR message an agency's $process-message accepts: a MessageHeader whose focus is the
// document, with the sending organisation it names.
func ReportingBundle(e *EICR, opts ReportingOptions) (map[string]any, error) {
	if e == nil || e.Bundle == nil {
		return nil, fmt.Errorf("no case report to send")
	}
	if opts.Destination == "" || opts.Source == "" {
		return nil, fmt.Errorf("an eCR message needs the destination endpoint and this sender's endpoint for the response")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Event == "" {
		opts.Event = "encounter-change"
	}
	docID := str(e.Bundle["identifier"].(map[string]any), "value")
	docURL := "urn:uuid:" + fhir.DeterministicUUID("eicr-document-entry", docID)
	// The sender is the document's custodian, carried again so the header resolves within the message.
	var sender map[string]any
	var senderURL string
	comp := arr(e.Bundle, "entry")[0].(map[string]any)["resource"].(map[string]any)
	custodian := str(comp["custodian"].(map[string]any), "reference")
	for _, en := range arr(e.Bundle, "entry") {
		em := en.(map[string]any)
		if str(em, "fullUrl") == custodian {
			sender, senderURL = em["resource"].(map[string]any), custodian
		}
	}
	if sender == nil {
		return nil, fmt.Errorf("the case report's custodian is not in the document")
	}
	headerID := fhir.DeterministicUUID("eicr-header", docID)
	contact := map[string]any{}
	if t := arr(sender, "telecom"); len(t) > 0 {
		contact = t[0].(map[string]any)
	}
	source := map[string]any{"name": str(sender, "name"), "software": "Perfuse", "endpoint": opts.Source}
	if len(contact) > 0 {
		source["contact"] = contact
	}
	header := map[string]any{
		"resourceType": "MessageHeader",
		"id":           headerID,
		"meta":         profile(ecrBase + "us-ph-messageheader"),
		"extension": []any{
			map[string]any{"url": ecrBase + "us-ph-message-processing-category-extension", "valueCode": "consequence"},
		},
		"eventCoding": map[string]any{
			"system": "http://hl7.org/fhir/us/ecr/CodeSystem/us-ph-message-types-codesystem", "code": "eicr-case-report-message",
		},
		"destination": []any{map[string]any{"endpoint": opts.Destination}},
		"sender":      map[string]any{"reference": senderURL},
		"source":      source,
		"reason": map[string]any{"coding": []any{map[string]any{
			"system": "http://hl7.org/fhir/us/ecr/CodeSystem/us-ph-triggerdefinition-namedevents", "code": opts.Event,
		}}},
		"focus": []any{map[string]any{"reference": docURL}},
	}
	return map[string]any{
		"resourceType": "Bundle",
		"meta":         profile(ecrBase + "us-ph-reporting-bundle"),
		"identifier":   map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + fhir.DeterministicUUID("eicr-message", docID)},
		"type":         "message",
		"timestamp":    opts.Now.Format(time.RFC3339),
		"entry": []any{
			map[string]any{"fullUrl": "urn:uuid:" + headerID, "resource": header},
			map[string]any{"fullUrl": docURL, "resource": e.Bundle},
			map[string]any{"fullUrl": senderURL, "resource": sender},
		},
	}, nil
}

// EventFor names the eCR trigger event for a v2 trigger event: an admission, transfer or discharge changes the encounter, a
// result changes a lab result, and anything else is a change to a diagnosis.
func EventFor(v2Event string, triggers []Trigger) string {
	for _, t := range triggers {
		if strings.HasPrefix(t.Resource, "Observation/") {
			return "labresult-change"
		}
	}
	switch strings.ToUpper(v2Event) {
	case "A01", "A02", "A03", "A04", "A06", "A07", "A08", "A11", "A12", "A13":
		return "encounter-change"
	}
	return "diagnosis-change"
}
