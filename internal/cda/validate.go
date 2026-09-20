package cda

import "strings"

// Validate checks a parsed document against the C-CDA R2.1 implementation guide.
//
// It covers the conformance statements that actually cause rejection or
// misinterpretation in production: header completeness, required sections for the
// claimed document type, and entry-level invariants that receivers depend on. It
// does not attempt to replicate the full Schematron, which checks hundreds of
// things nobody acts on, because a report with two hundred findings teaches nobody
// anything.
//
// Each finding carries a Rule name so a consumer can suppress or group by check,
// and a Severity that says whether the document would be rejected (error), would
// lose information (warning), or is worth knowing about (info).
func Validate(d *Document) ValidationReport {
	profile := "C-CDA R2.1"
	if d.DocumentType != "" {
		profile += " " + d.DocumentType
	}

	r := ValidationReport{Profile: profile}

	validateHeader(&r, d)
	validateDocumentType(&r, d)
	validateSections(&r, d)
	validateEntries(&r, d)

	for _, f := range r.Findings {
		switch f.Severity {
		case "error":
			r.Errors++
		case "warning":
			r.Warnings++
		case "info":
			r.Infos++
		}
	}
	return r
}

// ValidationReport is the result of checking a document against an implementation
// guide.
type ValidationReport struct {
	// Profile is which IG was checked, e.g. "C-CDA R2.1 Continuity of Care Document".
	Profile string `json:"profile"`

	// Findings reuse the existing Note type so they integrate with the rest of
	// the package without conversion.
	Findings []Note `json:"findings"`

	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// Conformant reports whether the document passes all error-level checks.
func (r ValidationReport) Conformant() bool { return r.Errors == 0 }

// validateHeader checks the US Realm Header constraints that cause rejection.
func validateHeader(r *ValidationReport, d *Document) {
	if !hasTemplate(d.TemplateIDs, "2.16.840.1.113883.10.20.22.1.1") {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/templateId",
			Message:  "the document does not claim the US Realm Header template (2.16.840.1.113883.10.20.22.1.1)",
			Rule:     "header-us-realm-template",
		})
	}

	if d.ID == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/id",
			Message:  "the document has no id",
			Rule:     "header-id-present",
		})
	}

	if d.TypeCode == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/code",
			Message:  "the document has no code",
			Rule:     "header-code-present",
		})
	}

	if d.Title == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/title",
			Message:  "the document has no title",
			Rule:     "header-title-present",
		})
	}

	if d.EffectiveTime == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/effectiveTime",
			Message:  "the document has no effectiveTime",
			Rule:     "header-effective-time-present",
		})
	}

	// recordTarget patient identifiers.
	hasRootedID := false
	for _, id := range d.Patient.Identifiers {
		if id.Root != "" {
			hasRootedID = true
			break
		}
	}
	if !hasRootedID {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/recordTarget/patientRole/id",
			Message:  "the patient has no identifier with a root; the document cannot be matched to a patient",
			Rule:     "header-patient-id-root",
		})
	}

	if d.Patient.Family == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/recordTarget/patientRole/patient/name",
			Message:  "the patient has no family name",
			Rule:     "header-patient-name",
		})
	}

	if d.Patient.BirthTime == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/recordTarget/patientRole/patient/birthTime",
			Message:  "the patient has no birthTime",
			Rule:     "header-patient-birth-time",
		})
	}

	if d.Patient.Gender == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/recordTarget/patientRole/patient/administrativeGenderCode",
			Message:  "the patient has no administrativeGenderCode",
			Rule:     "header-patient-gender",
		})
	}

	// Author with a time.
	hasAuthorWithTime := false
	for _, a := range d.Authors {
		if a.Time != "" {
			hasAuthorWithTime = true
			break
		}
	}
	if !hasAuthorWithTime {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/author",
			Message:  "the document has no author with a time",
			Rule:     "header-author-time",
		})
	}

	if d.Custodian == "" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/custodian",
			Message:  "the document has no custodian",
			Rule:     "header-custodian-present",
		})
	}
}

// validateDocumentType checks type-specific constraints.
func validateDocumentType(r *ValidationReport, d *Document) {
	if !hasTemplate(d.TemplateIDs, "2.16.840.1.113883.10.20.22.1.2") {
		return
	}

	// CCD requires specific sections.
	required := map[string]string{
		"Allergies":   "Allergies",
		"Medications": "Medications",
		"Problems":    "Problems",
		"Results":     "Results",
	}
	present := map[string]bool{}
	for _, s := range d.Sections {
		if s.Kind != "" {
			present[s.Kind] = true
		}
	}
	for kind, name := range required {
		if !present[kind] {
			r.Findings = append(r.Findings, Note{
				Severity: "error",
				Path:     "ClinicalDocument/component/structuredBody",
				Message:  "CCD requires a " + name + " section but none is present",
				Rule:     "ccd-required-section-" + strings.ToLower(kind),
			})
		}
	}

	// CCD must be coded LOINC 34133-9.
	if d.TypeCode != "34133-9" {
		r.Findings = append(r.Findings, Note{
			Severity: "error",
			Path:     "ClinicalDocument/code",
			Message:  "a CCD must have code 34133-9 (LOINC) but has " + d.TypeCode,
			Rule:     "ccd-document-code",
		})
	}
}

// validateSections checks section-level conformance.
func validateSections(r *ValidationReport, d *Document) {
	for _, s := range d.Sections {
		label := sectionLabel(s)
		hasNarrative := strings.TrimSpace(s.NarrativeText) != ""
		hasEntries := len(s.Entries) > 0

		// A section asserting nothing at all.
		if !hasNarrative && !hasEntries && s.NilFlavor == "" {
			r.Findings = append(r.Findings, Note{
				Severity: "error",
				Path:     "section/" + label,
				Message:  "the section has neither narrative nor entries nor a nilFlavor",
				Rule:     "section-not-empty",
			})
		}

		// A section claiming a template without a code.
		if len(s.TemplateIDs) > 0 && s.Code == "" {
			r.Findings = append(r.Findings, Note{
				Severity: "error",
				Path:     "section/" + label,
				Message:  "the section claims a template but has no code",
				Rule:     "section-template-has-code",
			})
		}

		// Entries without narrative.
		if hasEntries && !hasNarrative {
			r.Findings = append(r.Findings, Note{
				Severity: "warning",
				Path:     "section/" + label,
				Message:  "the section has entries but empty narrative; a viewer that renders only narrative shows the patient a blank section",
				Rule:     "section-narrative-with-entries",
			})
		}

		// Narrative without entries.
		if hasNarrative && !hasEntries && s.NilFlavor == "" {
			r.Findings = append(r.Findings, Note{
				Severity: "warning",
				Path:     "section/" + label,
				Message:  "the section has narrative but no entries, so nothing downstream can act on it",
				Rule:     "section-entries-with-narrative",
			})
		}
	}
}

// validateEntries checks entry-level conformance.
func validateEntries(r *ValidationReport, d *Document) {
	for _, s := range d.Sections {
		label := sectionLabel(s)
		walkEntries(s.Entries, func(e Entry) {
			// No code and no nilFlavor.
			if e.Code == "" && e.NilFlavor == "" {
				r.Findings = append(r.Findings, Note{
					Severity: "error",
					Path:     "section/" + label + "/entry",
					Message:  "an entry has neither a code nor a nilFlavor",
					Rule:     "entry-code-or-nilflavor",
				})
			}

			// Value without a code.
			if (e.Value != "" || e.ValueCode != "") && e.Code == "" {
				r.Findings = append(r.Findings, Note{
					Severity: "error",
					Path:     "section/" + label + "/entry",
					Message:  "an entry has a value but no code identifying what was measured",
					Rule:     "entry-value-needs-code",
				})
			}

			// No statusCode.
			if e.StatusCode == "" {
				r.Findings = append(r.Findings, Note{
					Severity: "warning",
					Path:     "section/" + label + "/entry",
					Message:  "an entry has no statusCode, so a receiver cannot tell whether it is current",
					Rule:     "entry-status-code",
				})
			}

			// No effectiveTime.
			if e.EffectiveTime == "" && e.Low == "" {
				r.Findings = append(r.Findings, Note{
					Severity: "warning",
					Path:     "section/" + label + "/entry",
					Message:  "an entry has no effectiveTime",
					Rule:     "entry-effective-time",
				})
			}

			// NegationInd.
			if e.NegationInd {
				r.Findings = append(r.Findings, Note{
					Severity: "info",
					Path:     "section/" + label + "/entry",
					Message:  "an entry carries negationInd, because a receiver that ignores it inverts the meaning",
					Rule:     "entry-negation-ind",
				})
			}
		})
	}
}

// walkEntries visits every entry in a tree.
func walkEntries(entries []Entry, fn func(Entry)) {
	for _, e := range entries {
		fn(e)
		walkEntries(e.Children, fn)
	}
}

// hasTemplate reports whether a list of template IDs includes a specific one.
func hasTemplate(ids []string, oid string) bool {
	for _, id := range ids {
		if id == oid {
			return true
		}
	}
	return false
}
