package cda

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/hl7"
)

// sampleCCD is a Continuity of Care Document with the structure real ones have:
// a US Realm header, a patient with a namespaced MRN, and sections whose narrative
// is a table.
//
// The allergies section contains a deliberate defect. The narrative lists
// penicillin and sulfa; the coded entries only carry penicillin. That is exactly
// the failure this package exists to catch, and it is the shape it takes in the
// wild: one part of an EHR generates the text and another generates the codes.
const sampleCCD = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <realmCode code="US"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <id root="2.16.840.1.113883.19.5.99999.1" extension="DOC-1"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1" displayName="Summarization of Episode Note"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <confidentialityCode code="N" codeSystem="2.16.840.1.113883.5.25"/>
  <languageCode code="en-US"/>
  <setId root="2.16.840.1.113883.19.5.99999.19" extension="SET-1"/>
  <versionNumber value="2"/>
  <recordTarget>
    <patientRole>
      <id root="2.16.840.1.113883.19.5.99999.2" extension="MRN900"/>
      <addr use="HP">
        <streetAddressLine>4 Elm Rd</streetAddressLine>
        <city>Vestavia</city><state>AL</state><postalCode>35216</postalCode><country>US</country>
      </addr>
      <telecom value="tel:+12055551234" use="HP"/>
      <patient>
        <name use="L"><given>Ivy</given><given>L</given><family>Frost</family></name>
        <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1" displayName="Female"/>
        <birthTime value="19910228"/>
      </patient>
    </patientRole>
  </recordTarget>
  <author>
    <time value="20260818115500-0500"/>
    <assignedAuthor>
      <id root="2.16.840.1.113883.4.6" extension="1234567893"/>
      <assignedPerson><name><given>Sam</given><family>Shaw</family></name></assignedPerson>
      <representedOrganization><name>St Example Hospital</name></representedOrganization>
    </assignedAuthor>
  </author>
  <custodian><assignedCustodian><representedCustodianOrganization>
    <name>St Example Hospital</name>
  </representedCustodianOrganization></assignedCustodian></custodian>
  <componentOf><encompassingEncounter>
    <id root="2.16.840.1.113883.19.5.99999.3" extension="VISIT-900"/>
    <code code="IMP" codeSystem="2.16.840.1.113883.5.4" displayName="Inpatient encounter"/>
    <effectiveTime><low value="20260815080000-0500"/><high value="20260818110000-0500"/></effectiveTime>
    <location><healthCareFacility><location><name>Ward 7</name></location></healthCareFacility></location>
  </encompassingEncounter></componentOf>
  <component><structuredBody>

    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1" displayName="Allergies"/>
      <title>Allergies and Adverse Reactions</title>
      <text>
        <table>
          <thead><tr><th>Substance</th><th>Reaction</th></tr></thead>
          <tbody>
            <tr><td ID="allergy1">Penicillin</td><td>Hives</td></tr>
            <tr><td ID="allergy2">Sulfa</td><td>Rash</td></tr>
          </tbody>
        </table>
      </text>
      <entry>
        <act classCode="ACT" moodCode="EVN">
          <templateId root="2.16.840.1.113883.10.20.22.4.30"/>
          <statusCode code="active"/>
          <entryRelationship typeCode="SUBJ">
            <observation classCode="OBS" moodCode="EVN">
              <templateId root="2.16.840.1.113883.10.20.22.4.7"/>
              <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
              <statusCode code="completed"/>
              <value xsi:type="CD" code="7980" codeSystem="2.16.840.1.113883.6.88" displayName="Penicillin"/>
              <text><reference value="#allergy1"/></text>
              <entryRelationship typeCode="MFST">
                <observation classCode="OBS" moodCode="EVN">
                  <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
                  <value xsi:type="CD" code="247472004" codeSystem="2.16.840.1.113883.6.96" displayName="Hives"/>
                </observation>
              </entryRelationship>
            </observation>
          </entryRelationship>
        </act>
      </entry>
    </section></component>

    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.5.1"/>
      <code code="11450-4" codeSystem="2.16.840.1.113883.6.1" displayName="Problem List"/>
      <title>Problems</title>
      <text><list><item ID="prob1">Type 2 diabetes mellitus</item></list></text>
      <entry>
        <act classCode="ACT" moodCode="EVN">
          <statusCode code="active"/>
          <entryRelationship typeCode="SUBJ">
            <observation classCode="OBS" moodCode="EVN">
              <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
              <statusCode code="completed"/>
              <effectiveTime><low value="20180101"/></effectiveTime>
              <value xsi:type="CD" code="44054006" codeSystem="2.16.840.1.113883.6.96" displayName="Type 2 diabetes mellitus"/>
              <text><reference value="#prob1"/></text>
            </observation>
          </entryRelationship>
        </act>
      </entry>
    </section></component>

    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.3.1"/>
      <code code="30954-2" codeSystem="2.16.840.1.113883.6.1" displayName="Results"/>
      <title>Results</title>
      <text><table><tbody>
        <tr><td>Hemoglobin</td><td>13.5 g/dL</td></tr>
        <tr><td>Leukocytes</td><td>14.2 10*3/uL</td></tr>
      </tbody></table></text>
      <entry>
        <organizer classCode="BATTERY" moodCode="EVN">
          <code code="58410-2" codeSystem="2.16.840.1.113883.6.1" displayName="CBC panel"/>
          <statusCode code="completed"/>
          <component><observation classCode="OBS" moodCode="EVN">
            <code code="718-7" codeSystem="2.16.840.1.113883.6.1" displayName="Hemoglobin"/>
            <statusCode code="completed"/>
            <effectiveTime value="20260818090000-0500"/>
            <value xsi:type="PQ" value="13.5" unit="g/dL"/>
          </observation></component>
          <component><observation classCode="OBS" moodCode="EVN">
            <code code="6690-2" codeSystem="2.16.840.1.113883.6.1" displayName="Leukocytes"/>
            <statusCode code="completed"/>
            <effectiveTime value="20260818090000-0500"/>
            <value xsi:type="PQ" value="14.2" unit="wonky/units"/>
          </observation></component>
        </organizer>
      </entry>
    </section></component>

    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.1.1"/>
      <code code="10160-0" codeSystem="2.16.840.1.113883.6.1" displayName="Medications"/>
      <title>Medications</title>
      <text><list><item ID="med1">Metformin 500 mg twice daily</item></list></text>
      <entry>
        <substanceAdministration classCode="SBADM" moodCode="EVN">
          <statusCode code="active"/>
          <effectiveTime><low value="20260101"/></effectiveTime>
          <consumable><manufacturedProduct><manufacturedMaterial>
            <code code="860975" codeSystem="2.16.840.1.113883.6.88" displayName="Metformin 500 MG"/>
          </manufacturedMaterial></manufacturedProduct></consumable>
        </substanceAdministration>
      </entry>
    </section></component>

  </structuredBody></component>
</ClinicalDocument>`

func TestParseDocument(t *testing.T) {
	doc, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}

	if doc.DocumentType != "Continuity of Care Document" {
		t.Errorf("document type = %q, want the CCD name from its template", doc.DocumentType)
	}
	if doc.Title != "Continuity of Care Document" {
		t.Errorf("title = %q", doc.Title)
	}
	if doc.Version != "2" {
		t.Errorf("version = %q, want 2", doc.Version)
	}
	if doc.Confidentiality != "normal" {
		t.Errorf("confidentiality = %q, want it decoded to normal", doc.Confidentiality)
	}
	if doc.TypeSystem != "LOINC" {
		t.Errorf("type system = %q, want LOINC rather than an OID", doc.TypeSystem)
	}

	if got := doc.Patient.Name(); got != "Ivy L Frost" {
		t.Errorf("patient = %q", got)
	}
	if doc.Patient.GenderName != "female" {
		t.Errorf("gender = %q, want it decoded", doc.Patient.GenderName)
	}
	if len(doc.Patient.Identifiers) != 1 || doc.Patient.Identifiers[0].Extension != "MRN900" {
		t.Errorf("identifiers = %+v", doc.Patient.Identifiers)
	}
	if doc.Patient.Address == nil || doc.Patient.Address.City != "Vestavia" {
		t.Errorf("address = %+v", doc.Patient.Address)
	}

	if doc.Encounter == nil {
		t.Fatal("the encompassing encounter was not read")
	}
	if doc.Encounter.Location != "Ward 7" {
		t.Errorf("location = %q", doc.Encounter.Location)
	}

	if len(doc.Authors) != 1 || doc.Authors[0].Family != "Shaw" {
		t.Errorf("authors = %+v", doc.Authors)
	}
	if doc.Custodian != "St Example Hospital" {
		t.Errorf("custodian = %q", doc.Custodian)
	}

	if len(doc.Sections) != 4 {
		t.Fatalf("got %d sections, want 4", len(doc.Sections))
	}

	// Sections must be recognised by name, since the point is that a reader does
	// not have to know what 2.16.840.1.113883.10.20.22.2.6.1 means.
	kinds := map[string]bool{}
	for _, s := range doc.Sections {
		kinds[s.Kind] = true
	}
	for _, want := range []string{"Allergies", "Problems", "Results", "Medications"} {
		if !kinds[want] {
			t.Errorf("section %q was not recognised; got %v", want, kinds)
		}
	}
}

func TestNarrativeRendering(t *testing.T) {
	doc, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}
	allergies := doc.SectionByKind("Allergies")
	if allergies == nil {
		t.Fatal("no allergies section")
	}

	// Table structure has to survive, or "Penicillin" and "Hives" run together and
	// nothing can be compared.
	if !strings.Contains(allergies.NarrativeText, "Penicillin") {
		t.Errorf("narrative lost its content:\n%s", allergies.NarrativeText)
	}
	if strings.Contains(allergies.NarrativeText, "PenicillinHives") {
		t.Errorf("table cells were run together:\n%s", allergies.NarrativeText)
	}
	if !strings.Contains(allergies.NarrativeHTML, "<table>") {
		t.Errorf("narrative HTML lost its table:\n%s", allergies.NarrativeHTML)
	}
	// The identifier a coded entry references must survive into the HTML.
	if !strings.Contains(allergies.NarrativeHTML, `id="cda-allergy1"`) {
		t.Errorf("the narrative identifier was dropped, so entry links cannot resolve:\n%s", allergies.NarrativeHTML)
	}
}

// TestNarrativeHTMLIsSafe checks that markup from outside the organisation cannot
// execute. A clinical viewer is a bad place for a scripting hole.
func TestNarrativeHTMLIsSafe(t *testing.T) {
	hostile := strings.Replace(sampleCCD,
		"<tr><td ID=\"allergy1\">Penicillin</td><td>Hives</td></tr>",
		`<tr><td ID="allergy1" onclick="steal()">Penicillin<script>alert(1)</script></td><td><img src=x onerror="bad()"/>Hives</td></tr>`,
		1)

	doc, err := Parse([]byte(hostile))
	if err != nil {
		t.Fatal(err)
	}
	html := doc.SectionByKind("Allergies").NarrativeHTML

	for _, forbidden := range []string{"<script", "onclick", "onerror", "<img"} {
		if strings.Contains(strings.ToLower(html), forbidden) {
			t.Errorf("hostile markup survived: %q in\n%s", forbidden, html)
		}
	}
	// And the legitimate content must still be there.
	if !strings.Contains(html, "Penicillin") {
		t.Errorf("sanitising removed the actual content:\n%s", html)
	}
}

// TestAgreementFindsTheMismatch is the headline test. The narrative lists two
// substances and the entries carry one, which is the defect nothing else checks.
func TestAgreementFindsTheMismatch(t *testing.T) {
	doc, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}

	report := CheckAgreement(doc)
	if report.Agrees() {
		t.Fatal("the sample has a deliberate allergy mismatch and it was not found")
	}

	var found bool
	for _, f := range report.Findings {
		if f.Kind == "term-missing-from-entries" && strings.Contains(f.Message, "Sulfa") {
			found = true
			if f.Severity != "warning" {
				t.Errorf("severity = %q", f.Severity)
			}
			if !strings.Contains(f.Message, "will not import") {
				t.Errorf("the message should say what the consequence is: %q", f.Message)
			}
		}
	}
	if !found {
		t.Errorf("the sulfa allergy present in the narrative and absent from the entries was not reported.\nFindings: %+v", report.Findings)
	}

	if report.Checked == 0 {
		t.Error("no sections were checked")
	}
}

// TestAgreementCatchesNegationMismatch covers the most dangerous disagreement: the
// text saying there are no allergies while a code records one.
func TestAgreementCatchesNegationMismatch(t *testing.T) {
	contradictory := strings.Replace(sampleCCD,
		`<table>
          <thead><tr><th>Substance</th><th>Reaction</th></tr></thead>
          <tbody>
            <tr><td ID="allergy1">Penicillin</td><td>Hives</td></tr>
            <tr><td ID="allergy2">Sulfa</td><td>Rash</td></tr>
          </tbody>
        </table>`,
		`<paragraph>No known allergies</paragraph>`, 1)

	doc, err := Parse([]byte(contradictory))
	if err != nil {
		t.Fatal(err)
	}
	report := CheckAgreement(doc)

	var found bool
	for _, f := range report.Findings {
		if f.Kind == "negation-mismatch" {
			found = true
			if f.Severity != "error" {
				t.Errorf("a direct contradiction should be an error, got %q", f.Severity)
			}
			if !strings.Contains(f.Message, "opposite") {
				t.Errorf("the message should explain the consequence: %q", f.Message)
			}
		}
	}
	if !found {
		t.Errorf("text saying no allergies alongside a coded penicillin allergy was not reported.\nFindings: %+v", report.Findings)
	}
}

// TestAgreementCatchesEntriesWithoutNarrative covers the other structural case.
func TestAgreementCatchesEntriesWithoutNarrative(t *testing.T) {
	noText := strings.Replace(sampleCCD, `<text><list><item ID="prob1">Type 2 diabetes mellitus</item></list></text>`, "", 1)

	doc, err := Parse([]byte(noText))
	if err != nil {
		t.Fatal(err)
	}
	report := CheckAgreement(doc)

	var found bool
	for _, f := range report.Findings {
		if f.Kind == "entries-without-narrative" {
			found = true
			if f.Severity != "error" {
				t.Errorf("severity = %q, want error", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("coded entries with no narrative were not reported.\nFindings: %+v", report.Findings)
	}
}

// TestAgreementIsQuietOnAConsistentDocument is the property that decides whether
// the check is usable. One that cries wolf gets switched off.
func TestAgreementIsQuietOnAConsistentDocument(t *testing.T) {
	consistent := strings.Replace(sampleCCD,
		`<tr><td ID="allergy2">Sulfa</td><td>Rash</td></tr>`, "", 1)

	doc, err := Parse([]byte(consistent))
	if err != nil {
		t.Fatal(err)
	}
	report := CheckAgreement(doc)

	for _, f := range report.Findings {
		if f.Severity == "error" {
			t.Errorf("a consistent document produced an error: %+v", f)
		}
	}
	if report.Warnings > 1 {
		t.Errorf("a consistent document produced %d warnings, which is too noisy to be useful: %+v",
			report.Warnings, report.Findings)
	}
}

func TestToFHIR(t *testing.T) {
	doc, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}

	result, err := doc.ToFHIR(FHIROptions{
		Version:  "R5",
		Original: []byte(sampleCCD),
		IdentifierSystems: map[string]string{
			"2.16.840.1.113883.19.5.99999.2": "http://stexample.org/mrn",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Patient", "AllergyIntolerance", "Condition", "Observation", "MedicationStatement", "DocumentReference"} {
		if result.Counts[want] == 0 {
			t.Errorf("no %s was produced; counts %v", want, result.Counts)
		}
	}

	encoded, err := json.Marshal(result.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	out := string(encoded)

	if !strings.Contains(out, `"type":"transaction"`) {
		t.Error("the bundle should be a transaction")
	}
	// The configured system must be used, so the MRN is unambiguous.
	if !strings.Contains(out, "http://stexample.org/mrn") {
		t.Error("the configured identifier system was not used")
	}
	// Conditional upserts, so a document sent twice updates rather than duplicating.
	if !strings.Contains(out, `"url":"Patient?identifier=`) {
		t.Errorf("the patient should be a conditional upsert:\n%s", firstN(out, 400))
	}
	// The original document must be retained; it is the legal record.
	if !strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(sampleCCD))[:40]) {
		t.Error("the original document bytes were not attached to the DocumentReference")
	}
}

// TestUnknownUnitIsNotClaimedAsUCUM pins the rule that a unit is only given a UCUM
// code when it is certainly UCUM. The sample has a deliberately invented unit.
func TestUnknownUnitIsNotClaimedAsUCUM(t *testing.T) {
	doc, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}
	result, err := doc.ToFHIR(FHIROptions{Version: "R5"})
	if err != nil {
		t.Fatal(err)
	}

	encoded, _ := json.Marshal(result.Bundle)
	out := string(encoded)

	// The good unit gets a UCUM system.
	if !strings.Contains(out, `"unit":"g/dL"`) {
		t.Error("a recognised unit should be emitted")
	}
	// The invented one must not be asserted as UCUM.
	if strings.Contains(out, `"code":"wonky/units"`) {
		t.Error("an unrecognised unit was claimed as UCUM, which a receiver would try to convert")
	}

	var noted bool
	for _, n := range result.Notes {
		if n.Rule == "unit-not-ucum" && strings.Contains(n.Message, "wonky/units") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("keeping a unit as text should be reported: %+v", result.Notes)
	}
}

// TestNegatedAllergyIsRefusedRatherThanInverted is the safety property of the
// conversion. Emitting a negated allergy positively would invert its meaning.
func TestNegatedAllergyIsRefusedRatherThanInverted(t *testing.T) {
	negated := strings.Replace(sampleCCD,
		`<observation classCode="OBS" moodCode="EVN">
              <templateId root="2.16.840.1.113883.10.20.22.4.7"/>`,
		`<observation classCode="OBS" moodCode="EVN" negationInd="true">
              <templateId root="2.16.840.1.113883.10.20.22.4.7"/>`, 1)

	doc, err := Parse([]byte(negated))
	if err != nil {
		t.Fatal(err)
	}
	allergies := doc.SectionByKind("Allergies")
	entries := flatten(allergies.Entries)

	var sawNegation bool
	for _, e := range entries {
		if e.NegationInd {
			sawNegation = true
		}
	}
	if !sawNegation {
		t.Fatal("negationInd was not read from the document")
	}

	result, err := doc.ToFHIR(FHIROptions{Version: "R5"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts["AllergyIntolerance"] != 0 {
		t.Error("a negated allergy must not be emitted as a positive AllergyIntolerance")
	}

	var explained bool
	for _, n := range result.Notes {
		if n.Rule == "negated-allergy-not-representable" {
			explained = true
			if n.Severity != "error" {
				t.Errorf("severity = %q, want error", n.Severity)
			}
			if !strings.Contains(n.Message, "invert") {
				t.Errorf("the note should explain why: %q", n.Message)
			}
		}
	}
	if !explained {
		t.Errorf("refusing to convert should be explained: %+v", result.Notes)
	}
}

func TestFHIRVersionRefusals(t *testing.T) {
	doc, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := doc.ToFHIR(FHIROptions{Version: "R6"}); err == nil {
		t.Error("R6 is unpublished and should be refused")
	} else if !strings.Contains(err.Error(), "ballot") {
		t.Errorf("the refusal should explain why: %v", err)
	}

	if _, err := doc.ToFHIR(FHIROptions{Version: "R3"}); err == nil {
		t.Error("an unknown version should be refused")
	}

	for _, v := range []string{"R4", "R4B", "R5"} {
		if _, err := doc.ToFHIR(FHIROptions{Version: v}); err != nil {
			t.Errorf("%s should be supported: %v", v, err)
		}
	}
}

// TestTimestampWithoutOffsetIsReported pins the decision not to assume a time zone
// silently. This is how a discharge time ends up hours out.
func TestTimestampWithoutOffsetIsReported(t *testing.T) {
	noOffset := strings.Replace(sampleCCD,
		`<effectiveTime value="20260818090000-0500"/>`,
		`<effectiveTime value="20260818090000"/>`, 1)

	doc, err := Parse([]byte(noOffset))
	if err != nil {
		t.Fatal(err)
	}
	result, err := doc.ToFHIR(FHIROptions{Version: "R5"})
	if err != nil {
		t.Fatal(err)
	}

	var noted bool
	for _, n := range result.Notes {
		if n.Rule == "timestamp-offset-assumed" {
			noted = true
			if !strings.Contains(n.Message, "wrong by the local offset") {
				t.Errorf("the note should say what the risk is: %q", n.Message)
			}
		}
	}
	if !noted {
		t.Errorf("a timestamp with no offset should be reported: %+v", result.Notes)
	}
}

func TestMissingRecordTargetIsAnError(t *testing.T) {
	// A document with no patient at all.
	start := strings.Index(sampleCCD, "<recordTarget>")
	end := strings.Index(sampleCCD, "</recordTarget>") + len("</recordTarget>")
	stripped := sampleCCD[:start] + sampleCCD[end:]

	doc, err := Parse([]byte(stripped))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, n := range doc.Notes {
		if n.Rule == "record-target-present" && n.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Errorf("a document with no patient should be an error: %+v", doc.Notes)
	}
}

// TestMisplacedRecordTargetIsFlagged covers the more dangerous variant. Searching
// the whole document for a patientRole would find one inside a family history or a
// related person, and file the document against the wrong human being.
func TestMisplacedRecordTargetIsFlagged(t *testing.T) {
	moved := strings.Replace(sampleCCD, "<recordTarget>", "<somethingElse>", 1)
	moved = strings.Replace(moved, "</recordTarget>", "</somethingElse>", 1)

	doc, err := Parse([]byte(moved))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, n := range doc.Notes {
		if n.Rule == "record-target-misplaced" {
			found = true
			if !strings.Contains(n.Message, "relative") {
				t.Errorf("the note should say what the risk is: %q", n.Message)
			}
		}
	}
	if !found {
		t.Errorf("a patient found outside recordTarget should be flagged: %+v", doc.Notes)
	}
	// It should still be used, because refusing the document outright would lose
	// clinical information over a structural mistake.
	if doc.Patient.Family != "Frost" {
		t.Error("the patient should still have been read")
	}
}

func TestUnrecognisedOIDIsMarked(t *testing.T) {
	if got := oidName("1.2.3.4.5.6.7.8.9"); !strings.Contains(got, "not recognised") {
		t.Errorf("an unknown OID should be marked, got %q", got)
	}
	if got := oidName("2.16.840.1.113883.6.1"); got != "LOINC" {
		t.Errorf("a known OID should be named, got %q", got)
	}
	if got := oidName(""); got != "" {
		t.Errorf("an empty OID should stay empty, got %q", got)
	}
}

func TestParseRejectsNonDocument(t *testing.T) {
	if _, err := Parse([]byte(`<html><body>not a document</body></html>`)); err == nil {
		t.Error("XML that is not a clinical document should be refused")
	} else if !strings.Contains(err.Error(), "not a clinical document") {
		t.Errorf("the error should be specific: %v", err)
	}

	if _, err := Parse([]byte(`not xml at all`)); err == nil {
		t.Error("non-XML should be refused")
	}
}

// txaSegment builds a TXA segment from a field-number map.
//
// It is built this way because counting pipes by hand is how field numbers end up
// wrong, and I got this segment wrong the first time: the parent document number
// landed in TXA-14 instead of TXA-13, which made a replacement look like a new
// document. That is precisely the mistake the annotated message viewer exists to
// prevent, so the fixture should not depend on my counting either.
func txaSegment(fields map[int]string) string {
	highest := 0
	for n := range fields {
		if n > highest {
			highest = n
		}
	}
	parts := make([]string, highest+1)
	parts[0] = "TXA"
	for n, v := range fields {
		parts[n] = v
	}
	return strings.Join(parts, "|")
}

// TestExtractFromMDM is the pipeline that matters: a document arriving inside an
// HL7 v2 message, which is how they actually travel.
func TestExtractFromMDM(t *testing.T) {
	// Unwrapped, because a carriage return inside an HL7 field would terminate the
	// segment. A sender that wants to wrap has to use escape sequences, which is
	// covered separately below.
	encoded := base64.StdEncoding.EncodeToString([]byte(sampleCCD))

	txa := txaSegment(map[int]string{
		1:  "1",
		2:  "DS",
		3:  "AP^application^HL7",
		4:  "20260818125500-0500",
		9:  "1234^Shaw^Sam",
		12: "DOC-1^SITEA",
		13: "PARENT-DOC-0^SITEA",
		17: "AU",
		19: "AV",
	})

	mdm := "MSH|^~\\&|EHR|SITEA|ARCHIVE|RFAC|20260818130000-0500||MDM^T02^MDM_T02|MD1|P|2.5.1\r" +
		"EVN|T02|20260818130000-0500\r" +
		"PID|1||MRN900^^^SITEA^MR||Frost^Ivy^L||19910228|F\r" +
		txa + "\r" +
		"OBX|1|ED|34133-9^Summary^LN||^application/hl7-cda+xml^^Base64^" + encoded + "||||||F\r"

	m, err := hl7.Parse([]byte(mdm))
	if err != nil {
		t.Fatal(err)
	}

	if !IsDocumentMessage(m) {
		t.Error("an MDM^T02 should be recognised as carrying a document")
	}

	doc, found, err := ParseEmbedded(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d attachments, want 1", len(found))
	}
	if doc == nil {
		t.Fatalf("the embedded document was not parsed; notes: %v", found[0].Notes)
	}

	if doc.Patient.Family != "Frost" {
		t.Errorf("the wrong document came out: patient %q", doc.Patient.Family)
	}

	// The TXA metadata has to come across, because it says things the document does
	// not, including that this one supersedes another.
	if doc.Transport == nil {
		t.Fatal("the TXA segment was not read")
	}
	if doc.Transport.UniqueID != "DOC-1" {
		t.Errorf("unique id = %q", doc.Transport.UniqueID)
	}
	if !doc.Transport.Replaces() {
		t.Error("a document naming a parent should be recognised as a replacement")
	}
	if doc.Transport.StatusMeaning() != "authenticated" {
		t.Errorf("status = %q, want it decoded", doc.Transport.StatusMeaning())
	}

	// And the replacement must survive into FHIR, or the receiver files both.
	result, err := doc.ToFHIR(FHIROptions{Version: "R5", Original: found[0].Data})
	if err != nil {
		t.Fatal(err)
	}
	encodedBundle, _ := json.Marshal(result.Bundle)
	if !strings.Contains(string(encodedBundle), `"code":"replaces"`) {
		t.Error("the replacement relationship was lost in conversion")
	}
}

// TestEscapedLineBreaksInPayload covers the way a sender legitimately wraps a
// base64 payload: with HL7 escape sequences, since a bare carriage return would
// end the segment.
func TestEscapedLineBreaksInPayload(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`<ClinicalDocument xmlns="urn:hl7-org:v3">
		<title>Wrapped</title>
		<recordTarget><patientRole><id root="1.2.3" extension="X1"/>
		<patient><name><family>Wrapped</family></name></patient>
		</patientRole></recordTarget></ClinicalDocument>`))

	var wrapped strings.Builder
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		wrapped.WriteString(encoded[i:end])
		// Two separate sequences. Each escape is opened and closed by its own
		// backslash, so consecutive ones do not share a delimiter.
		wrapped.WriteString(`\X0D\` + `\X0A\`)
	}

	mdm := "MSH|^~\\&|EHR|SITEA|ARCH|RFAC|20260818130000-0500||MDM^T02|MD4|P|2.5.1\r" +
		"PID|1||X1^^^SITEA^MR||Wrapped\r" +
		"OBX|1|ED|34133-9^Summary^LN||^application/hl7-cda+xml^^Base64^" + wrapped.String() + "||||||F\r"

	m, err := hl7.Parse([]byte(mdm))
	if err != nil {
		t.Fatal(err)
	}
	doc, found, err := ParseEmbedded(m)
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil {
		t.Fatalf("an escaped-wrap payload should decode; notes: %v", found[0].Notes)
	}
	if doc.Title != "Wrapped" {
		t.Errorf("title = %q", doc.Title)
	}
	// The HL7 parser resolves \X..\ sequences when it unescapes a field, so by the
	// time the payload is seen the line breaks are real and no note is needed. The
	// scanner in this package covers the cases the parser leaves alone, and is
	// tested directly below.
	if len(found[0].Data) == 0 {
		t.Error("the payload decoded to nothing")
	}
}

// TestMalformedEscapeIsReportedNotSilentlyCorrupted covers a sender that writes
// consecutive escapes sharing a backslash, which is wrong and does happen. The
// payload cannot be recovered, and the important thing is that it is reported
// rather than turning into garbage that fails somewhere less obvious.
func TestMalformedEscapeIsReportedNotSilentlyCorrupted(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`<ClinicalDocument xmlns="urn:hl7-org:v3"><title>T</title></ClinicalDocument>`))

	var wrapped strings.Builder
	for i := 0; i < len(encoded); i += 40 {
		end := i + 40
		if end > len(encoded) {
			end = len(encoded)
		}
		wrapped.WriteString(encoded[i:end])
		wrapped.WriteString(`\X0D\X0A\`) // malformed: the sequences share a backslash
	}

	mdm := "MSH|^~\\&|EHR|SITEA|ARCH|RFAC|20260818130000-0500||MDM^T02|MD5|P|2.5.1\r" +
		"PID|1||X1^^^SITEA^MR||Test\r" +
		"OBX|1|ED|34133-9^Summary^LN||^application/hl7-cda+xml^^Base64^" + wrapped.String() + "||||||F\r"

	m, err := hl7.Parse([]byte(mdm))
	if err != nil {
		t.Fatal(err)
	}
	doc, found, err := ParseEmbedded(m)
	if err != nil {
		t.Fatalf("a malformed payload is not a parse error: %v", err)
	}
	if doc != nil {
		t.Error("a corrupted payload should not produce a document")
	}
	if len(found) != 1 {
		t.Fatalf("the attachment should still be reported, got %d", len(found))
	}
	notes := strings.Join(found[0].Notes, " ")
	if !strings.Contains(notes, "does not decode") {
		t.Errorf("the failure should be stated plainly: %v", found[0].Notes)
	}
	// And the raw text must be kept, so somebody can look at it.
	if len(found[0].Data) == 0 {
		t.Error("the undecodable payload should still be available as text")
	}
}

// TestMultiByteEscapeSequence covers the other legal spelling, where one sequence
// carries several bytes. A fixed replacer never matches this form.
func TestMultiByteEscapeSequence(t *testing.T) {
	if got, n := resolveHexEscapes(`a\X0D0A\b`); got != "a\r\nb" || n != 1 {
		t.Errorf("got %q (%d sequences), want a CRLF from one sequence", got, n)
	}
	if got, n := resolveHexEscapes(`a\X0D\` + `\X0A\b`); got != "a\r\nb" || n != 2 {
		t.Errorf("got %q (%d sequences), want a CRLF from two sequences", got, n)
	}
	// Something that is not an escape sequence must pass through untouched rather
	// than losing bytes out of a clinical document.
	if got, n := resolveHexEscapes(`C:\Xray\scan.pdf`); got != `C:\Xray\scan.pdf` || n != 0 {
		t.Errorf("a path that looks like an escape was mangled: %q (%d)", got, n)
	}
	if got, _ := resolveHexEscapes("no escapes here"); got != "no escapes here" {
		t.Errorf("plain text was changed: %q", got)
	}
}

// TestMislabelledEncodingStillWorks covers the reality that sending systems get
// the encoding component wrong, and losing clinical information over a metadata
// mistake would be the wrong trade.
func TestMislabelledEncodingStillWorks(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`<ClinicalDocument xmlns="urn:hl7-org:v3">
		<title>Mislabelled</title>
		<recordTarget><patientRole><id root="1.2.3" extension="X1"/>
		<patient><name><family>Test</family></name></patient>
		</patientRole></recordTarget></ClinicalDocument>`))

	// Declared as plain ASCII but actually base64.
	mdm := "MSH|^~\\&|EHR|SITEA|ARCH|RFAC|20260818130000-0500||MDM^T02|MD2|P|2.5.1\r" +
		"PID|1||X1^^^SITEA^MR||Test\r" +
		"OBX|1|ED|34133-9^Summary^LN||^application/xml^^A^" + encoded + "||||||F\r"

	m, err := hl7.Parse([]byte(mdm))
	if err != nil {
		t.Fatal(err)
	}
	doc, found, err := ParseEmbedded(m)
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil {
		t.Fatalf("a mislabelled but readable document should still be read; notes: %v", found[0].Notes)
	}
	if doc.Title != "Mislabelled" {
		t.Errorf("title = %q", doc.Title)
	}

	var explained bool
	for _, n := range found[0].Notes {
		if strings.Contains(n, "declared as plain text but is base64") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("the mislabelling should be recorded: %v", found[0].Notes)
	}
}

// TestNonDocumentAttachmentIsReportedNotFailed covers mixed attachments: a PDF in
// an OBX must not look like a parsing error.
func TestNonDocumentAttachmentIsReportedNotFailed(t *testing.T) {
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 not a clinical document at all, just bytes"))

	mdm := "MSH|^~\\&|EHR|SITEA|ARCH|RFAC|20260818130000-0500||MDM^T02|MD3|P|2.5.1\r" +
		"PID|1||X1^^^SITEA^MR||Test\r" +
		"OBX|1|ED|34133-9^Scan^LN||^application/pdf^^Base64^" + pdf + "||||||F\r"

	m, err := hl7.Parse([]byte(mdm))
	if err != nil {
		t.Fatal(err)
	}
	doc, found, err := ParseEmbedded(m)
	if err != nil {
		t.Fatalf("a PDF attachment should not be an error: %v", err)
	}
	if doc != nil {
		t.Error("a PDF is not a clinical document and should not parse as one")
	}
	if len(found) != 1 {
		t.Fatalf("the attachment should still be reported, got %d", len(found))
	}
	if !strings.Contains(strings.Join(found[0].Notes, " "), "not XML") {
		t.Errorf("the notes should say what it was: %v", found[0].Notes)
	}
	if !strings.HasPrefix(string(found[0].Data), "%PDF") {
		t.Error("the attachment bytes should still be available")
	}
}

func TestMessageWithoutDocument(t *testing.T) {
	adt := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rPID|1||X1\r"
	m, err := hl7.Parse([]byte(adt))
	if err != nil {
		t.Fatal(err)
	}
	if IsDocumentMessage(m) {
		t.Error("an ADT with no encapsulated data is not a document message")
	}
	doc, found, err := ParseEmbedded(m)
	if err != nil {
		t.Errorf("a message with no document is not an error: %v", err)
	}
	if doc != nil || len(found) != 0 {
		t.Error("nothing should be found")
	}
}

func TestSummarise(t *testing.T) {
	doc, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}
	s := doc.Summarise()
	if s.Patient != "Ivy L Frost" || s.Sections != 4 || s.Entries == 0 {
		t.Errorf("summary = %+v", s)
	}
	if s.DocumentType == "" {
		t.Error("the summary should name the document type")
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
