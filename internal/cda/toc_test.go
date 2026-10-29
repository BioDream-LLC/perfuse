package cda

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readHL7(t *testing.T, name string) *Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hl7", name))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func tocParts(t *testing.T, result *FHIRResult) (composition map[string]any, byURL map[string]map[string]any) {
	t.Helper()
	var bundle map[string]any
	if err := json.Unmarshal([]byte(mustJSON(result.Bundle)), &bundle); err != nil {
		t.Fatal(err)
	}
	entries := listOf(bundle["entry"])
	byURL = map[string]map[string]any{}
	for _, e := range entries {
		byURL[e["fullUrl"].(string)] = e["resource"].(map[string]any)
	}
	if len(entries) == 0 || entries[0]["resource"].(map[string]any)["resourceType"] != "Composition" {
		t.Fatalf("the document Bundle does not start with its Composition")
	}
	return entries[0]["resource"].(map[string]any), byURL
}

func sectionByCode(t *testing.T, composition map[string]any, code string) map[string]any {
	t.Helper()
	for _, s := range listOf(composition["section"]) {
		coding := listOf(s["code"].(map[string]any)["coding"])
		if coding[0]["code"] == code {
			return s
		}
	}
	t.Fatalf("no section %s", code)
	return nil
}

// HL7's own transfer summary becomes a PACIO Transitions of Care document: every one of the guide's fifteen sections,
// each with text, every reference resolving inside the Bundle, and the coded entries the source carried.
func TestTransferSummaryBecomesAPACIODocument(t *testing.T) {
	doc := readHL7(t, "transfer-summary.xml")
	if doc.DocumentType != "Transfer Summary" {
		t.Errorf("document type %q, want Transfer Summary (the template table once read 22.1.13 as a Procedure Note)", doc.DocumentType)
	}
	result, err := doc.ToTOC(TOCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b := result.Bundle
	if b["type"] != "document" || b["identifier"] == nil || b["timestamp"] == nil {
		t.Fatalf("not a document Bundle with identifier and timestamp: %v %v %v", b["type"], b["identifier"], b["timestamp"])
	}
	composition, byURL := tocParts(t, result)
	sections := listOf(composition["section"])
	if len(sections) != 15 {
		t.Fatalf("%d sections, the guide requires 15", len(sections))
	}
	for _, s := range sections {
		if s["text"] == nil {
			t.Errorf("section %v has no text", s["title"])
		}
		if s["entry"] == nil && s["emptyReason"] == nil {
			if div, _ := s["text"].(map[string]any)["div"].(string); strings.Contains(div, "had no") {
				t.Errorf("section %v says the source had none but gives no emptyReason", s["title"])
			}
		}
		for _, ref := range listOf(s["entry"]) {
			if byURL[ref["reference"].(string)] == nil {
				t.Errorf("section %v points at %v, which is not in the Bundle", s["title"], ref["reference"])
			}
		}
	}
	for _, key := range []string{"subject", "custodian"} {
		if byURL[composition[key].(map[string]any)["reference"].(string)] == nil {
			t.Errorf("Composition.%s does not resolve", key)
		}
	}
	if composition["language"] != "en" {
		t.Errorf("language %v: the document's ISO 639-2 'eng' should become BCP 47 'en'", composition["language"])
	}

	// The allergy is to the substance (participant), not the allergy type (value), and its reaction is SNOMED CT.
	allergies := sectionByCode(t, composition, "48765-2")
	first := byURL[listOf(allergies["entry"])[0]["reference"].(string)]
	coding := firstCoding(first["code"])
	if coding["code"] == "419199007" || coding["code"] == "419511003" {
		t.Errorf("allergy coded as the allergy type %v, not the substance", coding["code"])
	}
	if raw, _ := json.Marshal(first); strings.Contains(string(raw), "v3-ActCode") {
		t.Errorf("a SNOMED CT value was given the observation code's system (HL7 act code): %s", raw)
	}

	// Medications travel as a PACIO medication list of R4 MedicationStatements.
	meds := sectionByCode(t, composition, "10160-0")
	list := byURL[listOf(meds["entry"])[0]["reference"].(string)]
	if list["resourceType"] != "List" || len(listOf(list["entry"])) == 0 {
		t.Fatalf("medications section entry is %v, want a List with items", list["resourceType"])
	}
	stmt := byURL[listOf(list["entry"])[0]["item"].(map[string]any)["reference"].(string)]
	if stmt["medicationCodeableConcept"] == nil || stmt["medication"] != nil || stmt["status"] != "active" {
		t.Errorf("MedicationStatement is not R4-shaped: %v", stmt)
	}

	// Vital signs: blood pressure as one panel with two components.
	vitals := sectionByCode(t, composition, "8716-3")
	panels := 0
	for _, ref := range listOf(vitals["entry"]) {
		obs := byURL[ref["reference"].(string)]
		code := firstCoding(obs["code"])["code"]
		if code == "8480-6" || code == "8462-4" {
			t.Errorf("a systolic or diastolic pressure travels alone (code %v)", code)
		}
		if code == "85354-9" {
			panels++
			if n := len(listOf(obs["component"])); n != 2 {
				t.Errorf("blood pressure panel has %d components", n)
			}
		}
	}
	if panels == 0 {
		t.Errorf("no blood pressure panel")
	}

	// Procedures in R4 form.
	procs := sectionByCode(t, composition, "47519-4")
	for _, ref := range listOf(procs["entry"]) {
		p := byURL[ref["reference"].(string)]
		if p["occurrenceDateTime"] != nil {
			t.Errorf("Procedure carries R5's occurrenceDateTime: %v", p)
		}
	}

	// Immunizations as US Core Immunization.
	imms := sectionByCode(t, composition, "11369-6")
	if len(listOf(imms["entry"])) == 0 {
		t.Errorf("no immunizations, though the source has five")
	}
}

// A section the source did not have is sent empty with a reason, never filled in; one it carried as text alone keeps
// its text.
func TestMissingSectionsAreSaidToBeMissing(t *testing.T) {
	doc := readHL7(t, "ccd.xml")
	result, err := doc.ToTOC(TOCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	composition, _ := tocParts(t, result)
	ad := sectionByCode(t, composition, "42348-3")
	if ad["entry"] != nil || ad["emptyReason"] == nil {
		t.Errorf("advance directives (absent from a CCD): %v", ad)
	}
	if !strings.Contains(ad["text"].(map[string]any)["div"].(string), "had no") {
		t.Errorf("the text does not say the section was missing: %v", ad["text"])
	}
}

// The discharge summary's medications are wrapped in discharge-medication acts; they convert, as the discharged list.
func TestDischargeMedicationsAreTheDischargedList(t *testing.T) {
	doc := readHL7(t, "discharge-summary.xml")
	result, err := doc.ToTOC(TOCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	composition, byURL := tocParts(t, result)
	meds := listOf(sectionByCode(t, composition, "10160-0")["entry"])
	if len(meds) == 0 {
		t.Fatalf("no medications: the discharge medication acts were not unwrapped")
	}
	list := byURL[meds[0]["reference"].(string)]
	if code := firstCoding(list["code"])["code"]; code != "104204-3" {
		t.Errorf("list code %v, want 104204-3 (discharged medication list)", code)
	}
	if composition["encounter"] == nil {
		t.Errorf("the stay the summary closes is not referenced")
	}
}

// Without a custodian or an author person a transfer summary is refused, not given an invented one.
func TestNoAuthorOrCustodianIsRefused(t *testing.T) {
	doc := readHL7(t, "transfer-summary.xml")
	noCustodian := *doc
	noCustodian.Custodian = ""
	if _, err := noCustodian.ToTOC(TOCOptions{}); err == nil || !strings.Contains(err.Error(), "custodian") {
		t.Errorf("no custodian: %v", err)
	}
	noAuthor := *doc
	noAuthor.Authors = []Author{{Organisation: "Device only"}}
	if _, err := noAuthor.ToTOC(TOCOptions{}); err == nil || !strings.Contains(err.Error(), "author") {
		t.Errorf("no author person: %v", err)
	}
}

// "No known allergies" (a negated allergy to any substance) is sent as FHIR's code for it, and a negated problem as
// refuted in verificationStatus, not clinicalStatus.
func TestNegationsKeepTheirMeaning(t *testing.T) {
	r, _, ok := allergyResource(Entry{Kind: "Observation", NegationInd: true, ParticipantCode: "105590001"}, nil, "R4", "p", 0)
	if !ok || firstCoding(r["code"])["code"] != "716186003" {
		t.Errorf("no known allergies: %v %v", ok, r)
	}
	if _, _, ok := allergyResource(Entry{Kind: "Observation", NegationInd: true, ParticipantCode: "7980", ParticipantName: "penicillin"}, nil, "R4", "p", 0); ok {
		t.Errorf("a negated allergy to one drug was emitted, which would read as an allergy to it")
	}
	c, _, _ := conditionResource(Entry{Kind: "Observation", NegationInd: true, ValueCode: "195967001", ValueSystem: "SNOMED CT"}, nil, "p", 0)
	if c["verificationStatus"] == nil || strings.Contains(mustJSON(c["clinicalStatus"]), "refuted") {
		t.Errorf("refuted problem: clinical %v verification %v", c["clinicalStatus"], c["verificationStatus"])
	}
}

func TestNPICheckDigit(t *testing.T) {
	for npi, want := range map[string]bool{"1234567893": true, "1245319599": true, "5555555555": false, "123": false} {
		if got := validNPI(npi); got != want {
			t.Errorf("validNPI(%s) = %v", npi, got)
		}
	}
}

// The ordinary conversion's R4 output is R4: no R5 members.
func TestR4ConversionHasNoR5Members(t *testing.T) {
	doc := readHL7(t, "transfer-summary.xml")
	result, err := doc.ToFHIR(FHIROptions{Version: "R4"})
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(result.Bundle)
	for _, r5 := range []string{`"occurrenceDateTime"`, `"medication":{"concept"`, `"status":"recorded"`} {
		if strings.Contains(raw, r5) {
			t.Errorf("R4 bundle carries R5's %s", r5)
		}
	}
}

// PERFUSE_TEST_FHIR_VALIDATOR=path/to/validator_cli.jar validates the three HL7 examples' transfer summaries with the
// official FHIR validator against PACIO TOC 1.0.0. The one error it may report is the guide's own: a vital sign
// matches both its simple-observation and vital-signs entry slices.
func TestTransferSummariesWithTheFHIRValidator(t *testing.T) {
	jar := os.Getenv("PERFUSE_TEST_FHIR_VALIDATOR")
	if jar == "" {
		t.Skip("PERFUSE_TEST_FHIR_VALIDATOR not set")
	}
	dir := t.TempDir()
	var files []string
	for _, name := range []string{"transfer-summary.xml", "discharge-summary.xml", "ccd.xml"} {
		result, err := readHL7(t, name).ToTOC(TOCOptions{})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, strings.TrimSuffix(name, ".xml")+".json")
		if err := os.WriteFile(path, []byte(mustJSON(result.Bundle)), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, path)
	}
	args := append([]string{"-jar", jar}, files...)
	args = append(args, "-version", "4.0.1", "-ig", "hl7.fhir.us.pacio-toc#1.0.0", "-profile",
		"http://hl7.org/fhir/us/pacio-toc/StructureDefinition/TOC-Bundle", "-tx", "n/a", "-output-style", "compact")
	out, _ := exec.Command("java", args...).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, ": Error") {
			continue
		}
		if strings.Contains(line, "matches more than one slice - simple-observation, vital-signs") {
			continue
		}
		t.Error(line)
	}
}

func listOf(v any) []map[string]any {
	switch l := v.(type) {
	case []map[string]any:
		return l
	case []any:
		out := make([]map[string]any, 0, len(l))
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func firstCoding(concept any) map[string]any {
	m, _ := concept.(map[string]any)
	codings := listOf(m["coding"])
	if len(codings) == 0 {
		return map[string]any{}
	}
	return codings[0]
}
