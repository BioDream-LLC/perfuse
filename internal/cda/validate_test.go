package cda

import "testing"

// TestValidate_WellFormed checks that a properly constructed document produces
// zero errors. A validator that cries wolf is worse than none.
func TestValidate_WellFormed(t *testing.T) {
	d := wellFormedCCD()
	r := Validate(d)
	if r.Errors != 0 {
		t.Errorf("expected zero errors for a well-formed document, got %d:", r.Errors)
		for _, f := range r.Findings {
			if f.Severity == "error" {
				t.Errorf("  [%s] %s", f.Rule, f.Message)
			}
		}
	}
	if !r.Conformant() {
		t.Error("expected Conformant() == true for a well-formed document")
	}
	if r.Profile == "" {
		t.Error("expected Profile to be set")
	}
}

// --- Header checks ---

func TestValidate_HeaderUSRealmTemplate(t *testing.T) {
	d := wellFormedCCD()
	d.TemplateIDs = []string{"2.16.840.1.113883.10.20.22.1.2"} // missing US Realm Header
	assertFinding(t, Validate(d), "header-us-realm-template", "error")
}

func TestValidate_HeaderIDPresent(t *testing.T) {
	d := wellFormedCCD()
	d.ID = ""
	assertFinding(t, Validate(d), "header-id-present", "error")
}

func TestValidate_HeaderCodePresent(t *testing.T) {
	d := wellFormedCCD()
	d.TypeCode = ""
	assertFinding(t, Validate(d), "header-code-present", "error")
}

func TestValidate_HeaderTitlePresent(t *testing.T) {
	d := wellFormedCCD()
	d.Title = ""
	assertFinding(t, Validate(d), "header-title-present", "error")
}

func TestValidate_HeaderEffectiveTimePresent(t *testing.T) {
	d := wellFormedCCD()
	d.EffectiveTime = ""
	assertFinding(t, Validate(d), "header-effective-time-present", "error")
}

func TestValidate_HeaderPatientIDRoot(t *testing.T) {
	d := wellFormedCCD()
	d.Patient.Identifiers = []Identifier{{Extension: "MRN1"}} // no root
	assertFinding(t, Validate(d), "header-patient-id-root", "error")
}

func TestValidate_HeaderPatientName(t *testing.T) {
	d := wellFormedCCD()
	d.Patient.Family = ""
	assertFinding(t, Validate(d), "header-patient-name", "error")
}

func TestValidate_HeaderPatientBirthTime(t *testing.T) {
	d := wellFormedCCD()
	d.Patient.BirthTime = ""
	assertFinding(t, Validate(d), "header-patient-birth-time", "error")
}

func TestValidate_HeaderPatientGender(t *testing.T) {
	d := wellFormedCCD()
	d.Patient.Gender = ""
	assertFinding(t, Validate(d), "header-patient-gender", "error")
}

func TestValidate_HeaderAuthorTime(t *testing.T) {
	d := wellFormedCCD()
	d.Authors = []Author{{Family: "Smith"}} // no time
	assertFinding(t, Validate(d), "header-author-time", "error")
}

func TestValidate_HeaderCustodianPresent(t *testing.T) {
	d := wellFormedCCD()
	d.Custodian = ""
	assertFinding(t, Validate(d), "header-custodian-present", "error")
}

// --- Document type checks ---

func TestValidate_CCDRequiredSectionAllergies(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = removeSectionByKind(d.Sections, "Allergies")
	assertFinding(t, Validate(d), "ccd-required-section-allergies", "error")
}

func TestValidate_CCDRequiredSectionMedications(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = removeSectionByKind(d.Sections, "Medications")
	assertFinding(t, Validate(d), "ccd-required-section-medications", "error")
}

func TestValidate_CCDRequiredSectionProblems(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = removeSectionByKind(d.Sections, "Problems")
	assertFinding(t, Validate(d), "ccd-required-section-problems", "error")
}

func TestValidate_CCDRequiredSectionResults(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = removeSectionByKind(d.Sections, "Results")
	assertFinding(t, Validate(d), "ccd-required-section-results", "error")
}

func TestValidate_CCDDocumentCode(t *testing.T) {
	d := wellFormedCCD()
	d.TypeCode = "18842-5" // wrong code for a CCD
	assertFinding(t, Validate(d), "ccd-document-code", "error")
}

// --- Section checks ---

func TestValidate_SectionNotEmpty(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:       "11348-0",
		CodeSystem: "LOINC",
		Kind:       "Past medical history",
		// No narrative, no entries, no nilFlavor.
	})
	assertFinding(t, Validate(d), "section-not-empty", "error")
}

func TestValidate_SectionTemplateHasCode(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		TemplateIDs:   []string{"2.16.840.1.113883.10.20.22.2.17"},
		NarrativeText: "Some text",
		// No code.
	})
	assertFinding(t, Validate(d), "section-template-has-code", "error")
}

func TestValidate_SectionNarrativeWithEntries(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:       "29762-2",
		CodeSystem: "LOINC",
		Kind:       "Social history",
		Entries:    []Entry{{Code: "72166-2", CodeSystem: "LOINC", StatusCode: "completed", EffectiveTime: "20260101"}},
		// No narrative.
	})
	assertFinding(t, Validate(d), "section-narrative-with-entries", "warning")
}

func TestValidate_SectionEntriesWithNarrative(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:          "29762-2",
		CodeSystem:    "LOINC",
		Kind:          "Social history",
		NarrativeText: "Former smoker",
		// No entries.
	})
	assertFinding(t, Validate(d), "section-entries-with-narrative", "warning")
}

// --- Entry checks ---

func TestValidate_EntryCodeOrNilFlavor(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:          "29762-2",
		CodeSystem:    "LOINC",
		Kind:          "Social history",
		NarrativeText: "Something here",
		Entries:       []Entry{{Kind: "Observation", StatusCode: "completed", EffectiveTime: "20260101"}},
	})
	assertFinding(t, Validate(d), "entry-code-or-nilflavor", "error")
}

func TestValidate_EntryValueNeedsCode(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:          "29762-2",
		CodeSystem:    "LOINC",
		Kind:          "Social history",
		NarrativeText: "Something here",
		Entries:       []Entry{{Kind: "Observation", Value: "120", StatusCode: "completed", EffectiveTime: "20260101"}},
	})
	assertFinding(t, Validate(d), "entry-value-needs-code", "error")
}

func TestValidate_EntryStatusCode(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:          "29762-2",
		CodeSystem:    "LOINC",
		Kind:          "Social history",
		NarrativeText: "Something here",
		Entries:       []Entry{{Kind: "Observation", Code: "72166-2", CodeSystem: "LOINC", EffectiveTime: "20260101"}},
	})
	assertFinding(t, Validate(d), "entry-status-code", "warning")
}

func TestValidate_EntryEffectiveTime(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:          "29762-2",
		CodeSystem:    "LOINC",
		Kind:          "Social history",
		NarrativeText: "Something here",
		Entries:       []Entry{{Kind: "Observation", Code: "72166-2", CodeSystem: "LOINC", StatusCode: "completed"}},
	})
	assertFinding(t, Validate(d), "entry-effective-time", "warning")
}

func TestValidate_EntryNegationInd(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:          "29762-2",
		CodeSystem:    "LOINC",
		Kind:          "Social history",
		NarrativeText: "Something here",
		Entries: []Entry{{
			Kind:          "Observation",
			Code:          "72166-2",
			CodeSystem:    "LOINC",
			StatusCode:    "completed",
			EffectiveTime: "20260101",
			NegationInd:   true,
		}},
	})
	assertFinding(t, Validate(d), "entry-negation-ind", "info")
}

// --- Entry checks on children ---

func TestValidate_EntryChildCodeOrNilFlavor(t *testing.T) {
	d := wellFormedCCD()
	d.Sections = append(d.Sections, Section{
		Code:          "29762-2",
		CodeSystem:    "LOINC",
		Kind:          "Social history",
		NarrativeText: "Something here",
		Entries: []Entry{{
			Kind:          "Organizer",
			Code:          "LP29693-6",
			CodeSystem:    "LOINC",
			StatusCode:    "completed",
			EffectiveTime: "20260101",
			Children:      []Entry{{Kind: "Observation", StatusCode: "completed", EffectiveTime: "20260101"}},
		}},
	})
	assertFinding(t, Validate(d), "entry-code-or-nilflavor", "error")
}

// --- Conformant reports no errors for a document with only warnings ---

func TestValidate_ConformantWithWarnings(t *testing.T) {
	d := wellFormedCCD()
	// Add a section with entries but no narrative - produces a warning, not an error.
	d.Sections = append(d.Sections, Section{
		Code:       "29762-2",
		CodeSystem: "LOINC",
		Kind:       "Social history",
		Entries:    []Entry{{Code: "72166-2", CodeSystem: "LOINC", StatusCode: "completed", EffectiveTime: "20260101"}},
	})
	r := Validate(d)
	if r.Warnings == 0 {
		t.Fatal("expected at least one warning")
	}
	if !r.Conformant() {
		t.Error("expected Conformant() == true when only warnings are present")
	}
}

// --- helpers ---

// wellFormedCCD returns a Document that passes all checks. Each test breaks
// exactly one rule from this baseline.
func wellFormedCCD() *Document {
	return &Document{
		Title:         "Continuity of Care Document",
		TypeCode:      "34133-9",
		TypeSystem:    "LOINC",
		DocumentType:  "Continuity of Care Document",
		ID:            "2.16.840.1.113883.19.5.99999.1^DOC-1",
		EffectiveTime: "20260818120000-0500",
		TemplateIDs: []string{
			"2.16.840.1.113883.10.20.22.1.1",
			"2.16.840.1.113883.10.20.22.1.2",
		},
		Patient: Patient{
			Identifiers: []Identifier{{Root: "2.16.840.1.113883.19.5.99999.2", Extension: "MRN900"}},
			Family:      "Frost",
			Given:       []string{"Ivy"},
			Gender:      "F",
			BirthTime:   "19910228",
		},
		Authors:   []Author{{Time: "20260818115500-0500", Family: "Shaw", Given: "Sam"}},
		Custodian: "St Example Hospital",
		Sections: []Section{
			{Code: "48765-2", CodeSystem: "LOINC", Kind: "Allergies", NarrativeText: "Penicillin allergy",
				Entries: []Entry{{Code: "7980", CodeSystem: "RxNorm", StatusCode: "active", EffectiveTime: "20200101"}}},
			{Code: "10160-0", CodeSystem: "LOINC", Kind: "Medications", NarrativeText: "Lisinopril 10mg",
				Entries: []Entry{{Code: "314076", CodeSystem: "RxNorm", StatusCode: "active", EffectiveTime: "20230601"}}},
			{Code: "11450-4", CodeSystem: "LOINC", Kind: "Problems", NarrativeText: "Essential hypertension",
				Entries: []Entry{{Code: "59621000", CodeSystem: "SNOMED CT", StatusCode: "active", EffectiveTime: "20220315"}}},
			{Code: "30954-2", CodeSystem: "LOINC", Kind: "Results", NarrativeText: "HbA1c 6.1%",
				Entries: []Entry{{Code: "4548-4", CodeSystem: "LOINC", Value: "6.1", StatusCode: "completed", EffectiveTime: "20260801"}}},
		},
	}
}

func removeSectionByKind(sections []Section, kind string) []Section {
	var out []Section
	for _, s := range sections {
		if s.Kind != kind {
			out = append(out, s)
		}
	}
	return out
}

func assertFinding(t *testing.T, r ValidationReport, rule, severity string) {
	t.Helper()
	for _, f := range r.Findings {
		if f.Rule == rule {
			if f.Severity != severity {
				t.Errorf("rule %q: expected severity %q, got %q", rule, severity, f.Severity)
			}
			return
		}
	}
	t.Errorf("rule %q not found in findings; got %d findings:", rule, len(r.Findings))
	for _, f := range r.Findings {
		t.Errorf("  [%s] %s: %s", f.Severity, f.Rule, f.Message)
	}
}

// === AUDIT TESTS ===

// Verify the well-formed document produces zero findings at all - not just zero
// errors. A rule that fires on everything is as useless as one that never fires.
func TestValidate_WellFormed_NoFindingsAtAll(t *testing.T) {
	d := wellFormedCCD()
	r := Validate(d)
	if len(r.Findings) != 0 {
		t.Errorf("expected zero findings for a well-formed document, got %d:", len(r.Findings))
		for _, f := range r.Findings {
			t.Errorf("  [%s] %s: %s", f.Severity, f.Rule, f.Message)
		}
	}
}

// Verify severity counting agrees with the Findings slice. The counts are
// computed after appending all findings; if someone adds a finding in one place
// and increments a counter in another, these drift.
func TestValidate_SeverityCountingConsistency(t *testing.T) {
	d := wellFormedCCD()
	// Add a section that triggers warning (entries without narrative).
	d.Sections = append(d.Sections, Section{
		Code:       "29762-2",
		CodeSystem: "LOINC",
		Kind:       "Social history",
		Entries:    []Entry{{Code: "72166-2", CodeSystem: "LOINC", StatusCode: "completed", EffectiveTime: "20260101"}},
	})
	// Add an entry with negationInd (triggers info).
	d.Sections = append(d.Sections, Section{
		Code:          "48765-2",
		CodeSystem:    "LOINC",
		Kind:          "Extra",
		NarrativeText: "some text",
		Entries: []Entry{{
			Code: "A", CodeSystem: "B", StatusCode: "active", EffectiveTime: "20260101",
			NegationInd: true,
		}},
	})

	r := Validate(d)

	var errors, warnings, infos int
	for _, f := range r.Findings {
		switch f.Severity {
		case "error":
			errors++
		case "warning":
			warnings++
		case "info":
			infos++
		}
	}

	if r.Errors != errors {
		t.Errorf("Errors count %d disagrees with findings slice count %d", r.Errors, errors)
	}
	if r.Warnings != warnings {
		t.Errorf("Warnings count %d disagrees with findings slice count %d", r.Warnings, warnings)
	}
	if r.Infos != infos {
		t.Errorf("Infos count %d disagrees with findings slice count %d", r.Infos, infos)
	}
}

// Verify Conformant() is exactly Errors==0.
func TestValidate_ConformantIsExactlyErrorsZero(t *testing.T) {
	d := wellFormedCCD()
	d.ID = ""
	r := Validate(d)
	if r.Errors == 0 {
		t.Fatal("expected errors")
	}
	if r.Conformant() {
		t.Error("Conformant() should be false when Errors > 0")
	}

	d2 := wellFormedCCD()
	r2 := Validate(d2)
	if r2.Errors != 0 {
		t.Fatal("expected no errors on well-formed doc")
	}
	if !r2.Conformant() {
		t.Error("Conformant() should be true when Errors == 0")
	}
}
