package cda

import (
	"strings"
	"testing"
)

// A document whose Medications section holds a real prescription and no narrative.
//
// This is the shape that matters, and it is common: one part of an EHR writes the coded entries and another is
// supposed to write the prose, and when the second is skipped nothing complains. The document is conformant, the
// transfer succeeds, both ends record it as a success, and the section displays as empty.
const invisibleMedication = `<?xml version="1.0"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <recordTarget><patientRole><id root="2.16.840.1.113883.19.5" extension="P1"/>
    <patient><name><given>Ada</given><family>Lovelace</family></name>
    <administrativeGenderCode code="F"/><birthTime value="19151210"/></patient>
  </patientRole></recordTarget>
  <custodian><assignedCustodian><representedCustodianOrganization>
    <name>Example Hospital</name>
  </representedCustodianOrganization></assignedCustodian></custodian>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.1.1"/>
      <code code="10160-0" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Medications</title>
      <text/>
      <entry><substanceAdministration classCode="SBADM" moodCode="EVN">
        <statusCode code="active"/>
        <consumable><manufacturedProduct><manufacturedMaterial>
          <code code="860975" codeSystem="2.16.840.1.113883.6.88" displayName="metformin 500 MG"/>
        </manufacturedMaterial></manufacturedProduct></consumable>
      </substanceAdministration></entry>
      <entry><substanceAdministration classCode="SBADM" moodCode="EVN">
        <statusCode code="active"/>
        <consumable><manufacturedProduct><manufacturedMaterial>
          <code code="855332" codeSystem="2.16.840.1.113883.6.88" displayName="warfarin sodium 5 MG"/>
        </manufacturedMaterial></manufacturedProduct></consumable>
      </substanceAdministration></entry>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`

// The failure is that the document is valid and the content is invisible, and nothing anywhere says so.
func TestAnInvisibleMedicationListIsFoundAndRebuilt(t *testing.T) {
	d, err := Parse([]byte(invisibleMedication))
	if err != nil {
		t.Fatal(err)
	}

	// Establishes the premise rather than assuming it. If parsing ever starts synthesising narrative, this test
	// would otherwise keep passing while testing nothing.
	if got := strings.TrimSpace(d.Sections[0].NarrativeText); got != "" {
		t.Fatalf("the fixture is supposed to have no narrative, it has %q", got)
	}
	if len(d.Sections[0].Entries) != 2 {
		t.Fatalf("the fixture is supposed to have 2 entries, it has %d", len(d.Sections[0].Entries))
	}

	report, err := Repair(d, RepairOptions{Indent: true})
	if err != nil {
		t.Fatal(err)
	}

	// The count that matters: two prescriptions no reader would have seen.
	if report.InvisibleEntries != 2 {
		t.Errorf("reported %d invisible entries, want 2", report.InvisibleEntries)
	}
	if report.Fixed == 0 {
		t.Fatalf("nothing was repaired; report was %+v", report.Repairs)
	}

	if report.Document == "" {
		t.Fatal("no repaired document was produced")
	}

	// Both drugs must now be readable, and it has to be the narrative that carries them. Finding the name
	// somewhere in the file proves nothing: it was already in the coded entry, which is exactly the part viewers
	// ignore.
	back, err := Parse([]byte(report.Document))
	if err != nil {
		t.Fatalf("the repaired document does not parse: %v", err)
	}

	var narrative string
	for _, s := range back.Sections {
		narrative += " " + s.NarrativeText + " " + s.NarrativeHTML
	}
	narrative = strings.ToLower(narrative)

	for _, drug := range []string{"metformin", "warfarin"} {
		if !strings.Contains(narrative, drug) {
			t.Errorf("%s is still invisible: it is not in the narrative of the repaired document", drug)
		}
	}
}

// The consequence has to be stated in clinical terms, because that is what makes anybody act.
func TestTheReportSaysWhatWouldHaveHappenedNotJustWhatIsWrong(t *testing.T) {
	d, err := Parse([]byte(invisibleMedication))
	if err != nil {
		t.Fatal(err)
	}

	report, err := Repair(d, RepairOptions{})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, rep := range report.Repairs {
		if rep.Kind != RepairNarrativeGenerated {
			continue
		}
		found = true
		if strings.TrimSpace(rep.Consequence) == "" {
			t.Error("a repair reports a problem with no consequence, which is the half nobody acts on")
		}
		if strings.TrimSpace(rep.Action) == "" {
			t.Error("a repair does not say what was done about it")
		}
	}
	if !found {
		t.Fatal("the blank narrative was not reported at all")
	}
}

// A document that already displays properly must be left alone, or the tool is noise.
func TestADocumentThatDisplaysProperlyIsNotRepaired(t *testing.T) {
	d, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}

	report, err := Repair(d, RepairOptions{CustodianName: "Example Hospital"})
	if err != nil {
		t.Fatal(err)
	}

	for _, rep := range report.Repairs {
		if rep.Kind == RepairNarrativeGenerated {
			t.Errorf("a section with narrative was rebuilt anyway: %s", rep.Problem)
		}
	}
}

// Narrative a clinician may have written must never be overwritten, even when it is incomplete.
//
// This is the line between a repair tool and a liability. The narrative is the attested content of a clinical
// document; replacing a human assertion with a derived one, silently, leaves no reader able to tell which is
// which. So partial narrative is reported and left alone.
func TestIncompleteNarrativeIsReportedAndNotOverwritten(t *testing.T) {
	partial := strings.Replace(invisibleMedication,
		"<text/>",
		"<text>Patient takes metformin 500 MG daily.</text>", 1)

	d, err := Parse([]byte(partial))
	if err != nil {
		t.Fatal(err)
	}

	report, err := Repair(d, RepairOptions{CustodianName: "Example Hospital"})
	if err != nil {
		t.Fatal(err)
	}

	var incomplete *RepairAction
	for i := range report.Repairs {
		if report.Repairs[i].Kind == RepairNarrativeIncomplete {
			incomplete = &report.Repairs[i]
		}
	}
	if incomplete == nil {
		t.Fatalf("a narrative describing one of two medications was not reported; got %+v", report.Repairs)
	}
	if incomplete.Fixed {
		t.Error("attested narrative was overwritten, which replaces a clinician's assertion with a derived one")
	}

	// The missing drug has to be named. "This narrative is incomplete" sends somebody to read the whole section
	// and compare it against the entries by hand.
	if !strings.Contains(strings.ToLower(incomplete.Problem+incomplete.Recovered), "warfarin") {
		t.Errorf("the report does not name what is missing: %+v", incomplete)
	}

	// And the one that was described must not be reported as missing.
	if strings.Contains(strings.ToLower(incomplete.Recovered), "metformin") {
		t.Error("a medication the narrative does describe was reported as missing")
	}
}

// A missing custodian is reported, never invented.
func TestAMissingCustodianIsReportedRatherThanFilledIn(t *testing.T) {
	without := strings.Replace(invisibleMedication,
		`<custodian><assignedCustodian><representedCustodianOrganization>
    <name>Example Hospital</name>
  </representedCustodianOrganization></assignedCustodian></custodian>`, "", 1)

	d, err := Parse([]byte(without))
	if err != nil {
		t.Fatal(err)
	}

	report, err := Repair(d, RepairOptions{})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, rep := range report.Repairs {
		if strings.Contains(strings.ToLower(rep.Problem), "custodian") {
			found = true
			if rep.Fixed {
				t.Error("a custodian was invented, which is an assertion about an organisation")
			}
		}
	}
	if !found {
		t.Errorf("a document with no custodian did not report one; got %+v", report.Repairs)
	}

	// No document either, because Generate refuses without a custodian - and the findings still have to arrive.
	if report.Document != "" {
		t.Error("a document was produced despite having no custodian to name")
	}
	if len(report.Repairs) == 0 {
		t.Error("the findings were lost along with the document")
	}
}

// Repairing must not change the document the caller passed in.
//
// A caller showing the report beside the original would otherwise be showing it beside the repair, which destroys
// the comparison that justifies accepting the change.
func TestRepairingDoesNotAlterTheOriginal(t *testing.T) {
	d, err := Parse([]byte(invisibleMedication))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Repair(d, RepairOptions{CustodianName: "Example Hospital"}); err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(d.Sections[0].NarrativeText); got != "" {
		t.Errorf("the original document was modified: its narrative is now %q", got)
	}
}
