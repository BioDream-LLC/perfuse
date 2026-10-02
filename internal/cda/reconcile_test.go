package cda

import (
	"strings"
	"testing"
)

// helper to build a minimal document with a Medications section.
func medDoc(entries ...Entry) *Document {
	return &Document{
		Sections: []Section{
			{Kind: "Medications", Entries: entries},
		},
	}
}

func TestReconcileMedications_NegatedNotReportedAsAdded(t *testing.T) {
	// A negated medication means it is explicitly NOT taken. It must never
	// appear as "added" — inverting that is the most dangerous mistake.
	before := medDoc()
	after := medDoc(Entry{
		Kind:        "SubstanceAdministration",
		Code:        "197361",
		CodeSystem:  "RxNorm",
		CodeName:    "Warfarin",
		NegationInd: true,
		StatusCode:  "active",
	})

	report := ReconcileMedications(before, after)

	for _, mc := range report.Added {
		if strings.EqualFold(mc.Medication, "warfarin") {
			t.Fatal("negated medication was reported as added; NegationInd must suppress presence")
		}
	}
	if len(report.Added) != 0 {
		t.Errorf("expected no additions (negated entry should be treated as absence), got %d", len(report.Added))
	}
}

func TestReconcileMedications_NegatedNotReportedAsRemoved(t *testing.T) {
	// A negated medication in the before document is not "present" either.
	before := medDoc(Entry{
		Kind:        "SubstanceAdministration",
		Code:        "197361",
		CodeSystem:  "RxNorm",
		CodeName:    "Warfarin",
		NegationInd: true,
		StatusCode:  "active",
	})
	after := medDoc()

	report := ReconcileMedications(before, after)

	if len(report.Removed) != 0 {
		t.Errorf("expected no removals (negated entry is absence), got %d", len(report.Removed))
	}
}

func TestReconcileMedications_StatusConflictLandsInConflicts(t *testing.T) {
	// Active in one document, completed in the other: this is the clinically
	// dangerous case and must land in Conflicts, not Unchanged.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "completed",
	})

	report := ReconcileMedications(before, after)

	if len(report.Conflicts) == 0 {
		t.Fatal("expected a conflict for active→completed, got none")
	}
	if len(report.Unchanged) != 0 {
		t.Errorf("status conflict must not land in Unchanged, got %d", len(report.Unchanged))
	}
	found := false
	for _, c := range report.Conflicts {
		if strings.Contains(c.Medication, "Warfarin") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected Warfarin in Conflicts")
	}
}

func TestReconcileMedications_AbortedConflict(t *testing.T) {
	// Active → aborted is also a conflict.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "310965",
		CodeSystem: "RxNorm",
		CodeName:   "Metformin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "310965",
		CodeSystem: "RxNorm",
		CodeName:   "Metformin",
		StatusCode: "aborted",
	})

	report := ReconcileMedications(before, after)

	if len(report.Conflicts) == 0 {
		t.Fatal("expected a conflict for active→aborted")
	}
}

func TestReconcileMedications_DoseChangeDetected(t *testing.T) {
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
		Value:      "5",
		Unit:       "mg",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
		Value:      "10",
		Unit:       "mg",
	})

	report := ReconcileMedications(before, after)

	if len(report.Changed) == 0 {
		t.Fatal("expected a change for dose 5mg→10mg, got none")
	}
	mc := report.Changed[0]
	if mc.Kind != "changed" {
		t.Errorf("expected Kind=changed, got %q", mc.Kind)
	}
	if !strings.Contains(mc.Detail, "dose") {
		t.Errorf("Detail should mention dose, got %q", mc.Detail)
	}
	if !strings.Contains(mc.Detail, "5 mg") || !strings.Contains(mc.Detail, "10 mg") {
		t.Errorf("Detail should contain old and new dose, got %q", mc.Detail)
	}
}

func TestReconcileMedications_DoseChangeViaChildren(t *testing.T) {
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
		Children: []Entry{
			{Kind: "Observation", Value: "5", Unit: "mg"},
		},
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
		Children: []Entry{
			{Kind: "Observation", Value: "10", Unit: "mg"},
		},
	})

	report := ReconcileMedications(before, after)

	if len(report.Changed) == 0 {
		t.Fatal("expected a change for dose via children, got none")
	}
	if !strings.Contains(report.Changed[0].Detail, "dose detail") {
		t.Errorf("expected Detail to mention dose detail, got %q", report.Changed[0].Detail)
	}
}

func TestReconcileMedications_MatchByCodeWhenNamesDiffer(t *testing.T) {
	// Same code, different display names: must still match.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin Sodium",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})

	report := ReconcileMedications(before, after)

	if len(report.Unchanged) != 1 {
		t.Fatalf("expected 1 unchanged (matched by code despite name difference), got %d unchanged, %d added, %d removed",
			len(report.Unchanged), len(report.Added), len(report.Removed))
	}
}

func TestReconcileMedications_MatchByNameWhenCodesAbsent(t *testing.T) {
	// No codes, but same name: must match by normalised name.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		CodeName:   "Metformin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		CodeName:   "  metformin  ",
		StatusCode: "active",
	})

	report := ReconcileMedications(before, after)

	if len(report.Unchanged) != 1 {
		t.Fatalf("expected 1 unchanged (matched by normalised name), got %d unchanged, %d added, %d removed",
			len(report.Unchanged), len(report.Added), len(report.Removed))
	}
}

func TestReconcileMedications_AddedAndRemoved(t *testing.T) {
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "310965",
		CodeSystem: "RxNorm",
		CodeName:   "Metformin",
		StatusCode: "active",
	})

	report := ReconcileMedications(before, after)

	if len(report.Removed) != 1 || report.Removed[0].Medication != "Warfarin" {
		t.Errorf("expected Warfarin removed, got %+v", report.Removed)
	}
	if len(report.Added) != 1 || report.Added[0].Medication != "Metformin" {
		t.Errorf("expected Metformin added, got %+v", report.Added)
	}
}

// --- MergeSections tests ---

func TestMergeSections_DeduplicatesByCode(t *testing.T) {
	doc1 := &Document{
		Sections: []Section{{
			Kind: "Allergies",
			Entries: []Entry{
				{Code: "7980", CodeSystem: "RxNorm", CodeName: "Penicillin"},
				{Code: "2670", CodeSystem: "RxNorm", CodeName: "Codeine"},
			},
			NarrativeText: "Source 1 allergies",
		}},
	}
	doc2 := &Document{
		Sections: []Section{{
			Kind: "Allergies",
			Entries: []Entry{
				{Code: "7980", CodeSystem: "RxNorm", CodeName: "Penicillin"},
				{Code: "3423", CodeSystem: "RxNorm", CodeName: "Sulfa"},
			},
			NarrativeText: "Source 2 allergies",
		}},
	}

	merged, notes := MergeSections("Allergies", doc1, doc2)

	if len(merged.Entries) != 3 {
		t.Fatalf("expected 3 entries (Penicillin deduplicated), got %d", len(merged.Entries))
	}

	// Check that a dedup note was recorded.
	foundDedup := false
	for _, n := range notes {
		if n.Rule == "merge-dedup" {
			foundDedup = true
			break
		}
	}
	if !foundDedup {
		t.Error("expected a merge-dedup note for the duplicated Penicillin entry")
	}

	// Narrative should contain both sources.
	if !strings.Contains(merged.NarrativeText, "Source 1") || !strings.Contains(merged.NarrativeText, "Source 2") {
		t.Errorf("merged narrative should contain both sources, got: %q", merged.NarrativeText)
	}
}

func TestMergeSections_DeduplicatesByName(t *testing.T) {
	doc1 := &Document{
		Sections: []Section{{
			Kind: "Allergies",
			Entries: []Entry{
				{CodeName: "Penicillin"},
			},
		}},
	}
	doc2 := &Document{
		Sections: []Section{{
			Kind: "Allergies",
			Entries: []Entry{
				{CodeName: "penicillin"},
			},
		}},
	}

	merged, notes := MergeSections("Allergies", doc1, doc2)

	if len(merged.Entries) != 1 {
		t.Fatalf("expected 1 entry (deduplicated by normalised name), got %d", len(merged.Entries))
	}

	foundDedup := false
	for _, n := range notes {
		if n.Rule == "merge-dedup" {
			foundDedup = true
		}
	}
	if !foundDedup {
		t.Error("expected a merge-dedup note")
	}
}

func TestMergeSections_NilFlavorDroppedWhenRealEntryExists(t *testing.T) {
	// One source says "no known allergies", the other has a real entry.
	// The merged section drops the NilFlavor and records a Note.
	doc1 := &Document{
		Sections: []Section{{
			Kind:      "Allergies",
			NilFlavor: "NI",
		}},
	}
	doc2 := &Document{
		Sections: []Section{{
			Kind: "Allergies",
			Entries: []Entry{
				{Code: "7980", CodeSystem: "RxNorm", CodeName: "Penicillin"},
			},
			NarrativeText: "Penicillin allergy",
		}},
	}

	merged, notes := MergeSections("Allergies", doc1, doc2)

	if merged.NilFlavor != "" {
		t.Errorf("expected NilFlavor to be dropped, got %q", merged.NilFlavor)
	}

	foundOverride := false
	for _, n := range notes {
		if n.Rule == "merge-nilflavor-override" {
			foundOverride = true
			break
		}
	}
	if !foundOverride {
		t.Error("expected a merge-nilflavor-override note when a positive finding overrides a NilFlavor")
	}

	if len(merged.Entries) != 1 {
		t.Errorf("expected 1 entry from the real source, got %d", len(merged.Entries))
	}
}

func TestMergeSections_NilFlavorKeptWhenAllSourcesHaveIt(t *testing.T) {
	doc1 := &Document{
		Sections: []Section{{Kind: "Allergies", NilFlavor: "NI"}},
	}
	doc2 := &Document{
		Sections: []Section{{Kind: "Allergies", NilFlavor: "NI"}},
	}

	merged, notes := MergeSections("Allergies", doc1, doc2)

	if merged.NilFlavor != "NI" {
		t.Errorf("expected NilFlavor to be kept when all sources agree, got %q", merged.NilFlavor)
	}

	for _, n := range notes {
		if n.Rule == "merge-nilflavor-override" {
			t.Error("should not record an override note when all sources have NilFlavor")
		}
	}
	_ = notes
}

func TestMergeSections_NarrativeConcatenation(t *testing.T) {
	doc1 := &Document{
		Sections: []Section{{Kind: "Medications", NarrativeText: "First source"}},
	}
	doc2 := &Document{
		Sections: []Section{{Kind: "Medications", NarrativeText: "Second source"}},
	}

	merged, _ := MergeSections("Medications", doc1, doc2)

	if !strings.Contains(merged.NarrativeText, "First source") {
		t.Error("missing first source narrative")
	}
	if !strings.Contains(merged.NarrativeText, "Second source") {
		t.Error("missing second source narrative")
	}
	// Should have a blank line between.
	if !strings.Contains(merged.NarrativeText, "\n\n") {
		t.Error("narratives should be separated by a blank line")
	}
}

func TestMergeSections_EmptyWhenNoMatch(t *testing.T) {
	doc := &Document{
		Sections: []Section{{Kind: "Medications"}},
	}

	merged, _ := MergeSections("Allergies", doc)
	if !merged.Empty {
		t.Error("expected Empty=true when no section matches")
	}
}

// === AUDIT TESTS - negation cases 3 and 4 (the clinically critical ones) ===

func TestReconcileMedications_PresentThenNegated_IsRemoval(t *testing.T) {
	// Case 3: Drug is present in before, negated in after. This means the drug
	// was stopped. It MUST be reported as removed.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:        "SubstanceAdministration",
		Code:        "197361",
		CodeSystem:  "RxNorm",
		CodeName:    "Warfarin",
		NegationInd: true,
		StatusCode:  "active",
	})

	report := ReconcileMedications(before, after)

	if len(report.Removed) != 1 {
		t.Fatalf("Case 3: present→negated must be reported as removed; got %d removed, %d added, %d unchanged, %d conflicts",
			len(report.Removed), len(report.Added), len(report.Unchanged), len(report.Conflicts))
	}
	if report.Removed[0].Medication != "Warfarin" {
		t.Errorf("expected Warfarin removed, got %q", report.Removed[0].Medication)
	}
}

func TestReconcileMedications_NegatedThenPresent_IsAddition(t *testing.T) {
	// Case 4: Drug is negated in before, present in after. This means the drug
	// was started. It MUST be reported as added.
	before := medDoc(Entry{
		Kind:        "SubstanceAdministration",
		Code:        "197361",
		CodeSystem:  "RxNorm",
		CodeName:    "Warfarin",
		NegationInd: true,
		StatusCode:  "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})

	report := ReconcileMedications(before, after)

	if len(report.Added) != 1 {
		t.Fatalf("Case 4: negated→present must be reported as added; got %d added, %d removed, %d unchanged, %d conflicts",
			len(report.Added), len(report.Removed), len(report.Unchanged), len(report.Conflicts))
	}
	if report.Added[0].Medication != "Warfarin" {
		t.Errorf("expected Warfarin added, got %q", report.Added[0].Medication)
	}
}

// === AUDIT TEST - code system must match ===

func TestReconcileMedications_SameCodeDifferentSystem_NoMatch(t *testing.T) {
	// Code 1191 in RxNorm is aspirin. Code 1191 in a different system is a
	// completely different concept. They must NOT match.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "1191",
		CodeSystem: "RxNorm",
		CodeName:   "Aspirin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "1191",
		CodeSystem: "SNOMED CT",
		CodeName:   "Some SNOMED concept",
		StatusCode: "active",
	})

	report := ReconcileMedications(before, after)

	// They should NOT be matched - one should be removed, one added.
	if len(report.Unchanged) != 0 {
		t.Fatalf("same code in different code systems must not match; got %d unchanged", len(report.Unchanged))
	}
	if len(report.Removed) != 1 {
		t.Errorf("expected 1 removed (Aspirin from RxNorm), got %d", len(report.Removed))
	}
	if len(report.Added) != 1 {
		t.Errorf("expected 1 added (SNOMED concept), got %d", len(report.Added))
	}
}

// === AUDIT TEST - name matching must not pair clinically distinct drugs ===

func TestReconcileMedications_SimilarNames_NoFalseMatch(t *testing.T) {
	// Hydralazine and hydroxyzine are different drugs that a fuzzy match might
	// pair. Verify exact-normalized-name matching does not pair them.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		CodeName:   "Hydralazine",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		CodeName:   "Hydroxyzine",
		StatusCode: "active",
	})

	report := ReconcileMedications(before, after)

	if len(report.Unchanged) != 0 || len(report.Changed) != 0 {
		t.Fatal("Hydralazine and Hydroxyzine must NOT match; they are different drugs")
	}
	if len(report.Removed) != 1 || len(report.Added) != 1 {
		t.Errorf("expected 1 removed + 1 added, got %d removed + %d added", len(report.Removed), len(report.Added))
	}
}

func TestReconcileMedications_MetoprololFormulations_NoMatch(t *testing.T) {
	// Metoprolol tartrate and metoprolol succinate have different dosing
	// schedules and must not be treated as the same drug.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		CodeName:   "Metoprolol Tartrate",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		CodeName:   "Metoprolol Succinate",
		StatusCode: "active",
	})

	report := ReconcileMedications(before, after)

	if len(report.Unchanged) != 0 || len(report.Changed) != 0 {
		t.Fatal("Metoprolol Tartrate and Metoprolol Succinate must NOT match; they are different formulations with different dosing")
	}
	if len(report.Removed) != 1 || len(report.Added) != 1 {
		t.Errorf("expected 1 removed + 1 added, got %d removed + %d added", len(report.Removed), len(report.Added))
	}
}

// === AUDIT TEST - status handling: suspended, held, unknown ===

func TestReconcileMedications_SuspendedIsNotActive(t *testing.T) {
	// A suspended medication is NOT current. Active→suspended must be a conflict.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "suspended",
	})

	report := ReconcileMedications(before, after)

	if len(report.Conflicts) == 0 {
		t.Fatal("active→suspended must be a conflict; suspended is not current")
	}
}

func TestReconcileMedications_HeldIsNotActive(t *testing.T) {
	// A held medication is NOT current.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "held",
	})

	report := ReconcileMedications(before, after)

	if len(report.Conflicts) == 0 {
		t.Fatal("active→held must be a conflict; held is not current")
	}
}

func TestReconcileMedications_EmptyStatusTreatedAsActive_IsDangerous(t *testing.T) {
	// An empty status being treated as active means a medication with no stated
	// status is assumed current. This is the DANGEROUS default. Verify what the
	// code actually does: if before has status "active" and after has status "",
	// are they treated as equivalent (no conflict)?
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "", // unknown/empty
	})

	report := ReconcileMedications(before, after)

	// The SAFE behavior: unknown status should NOT be silently treated as active.
	// It should be flagged as a conflict or at minimum as changed. Treating it as
	// unchanged is dangerous because the patient may have stopped the medication.
	if len(report.Unchanged) != 0 {
		t.Fatal("DEFECT: empty/unknown status is silently treated as equivalent to active; this is the dangerous default")
	}
	// Acceptable outcomes: Conflicts or Changed. Either flags it for human review.
	if len(report.Conflicts) == 0 && len(report.Changed) == 0 {
		t.Error("expected either a conflict or a change when comparing active vs empty status")
	}
}

func TestReconcileMedications_UnknownStatusNotTreatedAsActive(t *testing.T) {
	// A completely unrecognised status must not be treated as active.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "active",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "197361",
		CodeSystem: "RxNorm",
		CodeName:   "Warfarin",
		StatusCode: "obsolete", // unrecognised status
	})

	report := ReconcileMedications(before, after)

	// active → unrecognised must be a conflict (different active/inactive states).
	if len(report.Conflicts) == 0 {
		t.Fatal("active→unrecognised status must be a conflict")
	}
}

// === AUDIT TEST - dose change detection with different units ===

func TestReconcileMedications_DoseUnitDifference_Detected(t *testing.T) {
	// 81 mg vs 81 mcg is a thousandfold difference. Must be detected as changed.
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "1191",
		CodeSystem: "RxNorm",
		CodeName:   "Aspirin",
		StatusCode: "active",
		Value:      "81",
		Unit:       "mg",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "1191",
		CodeSystem: "RxNorm",
		CodeName:   "Aspirin",
		StatusCode: "active",
		Value:      "81",
		Unit:       "mcg",
	})

	report := ReconcileMedications(before, after)

	if len(report.Changed) == 0 {
		t.Fatal("81 mg vs 81 mcg must be detected as changed (thousandfold difference)")
	}
	if !strings.Contains(report.Changed[0].Detail, "dose") {
		t.Errorf("expected dose change detail, got %q", report.Changed[0].Detail)
	}
}

func TestReconcileMedications_SameDoseDifferentExpression_Flagged(t *testing.T) {
	// 81 mg vs 0.081 g - same dose but different expression. The code does
	// string comparison so this WILL be flagged as changed, which is the safe
	// behavior (flags for human review).
	before := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "1191",
		CodeSystem: "RxNorm",
		CodeName:   "Aspirin",
		StatusCode: "active",
		Value:      "81",
		Unit:       "mg",
	})
	after := medDoc(Entry{
		Kind:       "SubstanceAdministration",
		Code:       "1191",
		CodeSystem: "RxNorm",
		CodeName:   "Aspirin",
		StatusCode: "active",
		Value:      "0.081",
		Unit:       "g",
	})

	report := ReconcileMedications(before, after)

	// String comparison means these look different, which flags for review. SAFE.
	if len(report.Changed) == 0 {
		t.Fatal("81 mg vs 0.081 g must be detected as changed (safe: flags for human review)")
	}
}

// === AUDIT TEST - MergeSections deduplication does not drop distinct entries ===

func TestMergeSections_SameCodeDifferentDose_NotDeduplicated(t *testing.T) {
	// Two entries with the same code but different doses are clinically distinct
	// (e.g. a patient taking the same drug at two different doses for different
	// indications, or a dose change). They must NOT be deduplicated.
	doc1 := &Document{
		Sections: []Section{{
			Kind: "Medications",
			Entries: []Entry{
				{Code: "197361", CodeSystem: "RxNorm", CodeName: "Warfarin", Value: "5", Unit: "mg"},
			},
		}},
	}
	doc2 := &Document{
		Sections: []Section{{
			Kind: "Medications",
			Entries: []Entry{
				{Code: "197361", CodeSystem: "RxNorm", CodeName: "Warfarin", Value: "10", Unit: "mg"},
			},
		}},
	}

	merged, _ := MergeSections("Medications", doc1, doc2)

	if len(merged.Entries) < 2 {
		t.Fatalf("DEFECT: two entries with same code but different doses were deduplicated; got %d entries, expected 2", len(merged.Entries))
	}
}

// === AUDIT TEST - MergeSections output determinism ===

func TestMergeSections_OutputIsDeterministic(t *testing.T) {
	// Run the merge multiple times and verify the output is always the same.
	doc1 := &Document{
		Sections: []Section{{
			Kind: "Medications",
			Entries: []Entry{
				{Code: "197361", CodeSystem: "RxNorm", CodeName: "Warfarin"},
				{Code: "310965", CodeSystem: "RxNorm", CodeName: "Metformin"},
				{Code: "314076", CodeSystem: "RxNorm", CodeName: "Lisinopril"},
			},
			NarrativeText: "Source 1",
		}},
	}
	doc2 := &Document{
		Sections: []Section{{
			Kind: "Medications",
			Entries: []Entry{
				{Code: "860975", CodeSystem: "RxNorm", CodeName: "Atorvastatin"},
				{Code: "197361", CodeSystem: "RxNorm", CodeName: "Warfarin"},
			},
			NarrativeText: "Source 2",
		}},
	}

	var firstResult []string
	for run := 0; run < 20; run++ {
		merged, _ := MergeSections("Medications", doc1, doc2)
		var names []string
		for _, e := range merged.Entries {
			names = append(names, e.CodeName)
		}
		if run == 0 {
			firstResult = names
		} else {
			if len(names) != len(firstResult) {
				t.Fatalf("run %d: different number of entries (%d vs %d)", run, len(names), len(firstResult))
			}
			for i := range names {
				if names[i] != firstResult[i] {
					t.Fatalf("run %d: non-deterministic output; position %d is %q but was %q on first run", run, i, names[i], firstResult[i])
				}
			}
		}
	}
}

func TestReconcileMedications_BothNegated_NoReporting(t *testing.T) {
	// Negated in both documents: the drug is explicitly not taken in either.
	// It should not appear anywhere in the report.
	before := medDoc(Entry{
		Kind:        "SubstanceAdministration",
		Code:        "197361",
		CodeSystem:  "RxNorm",
		CodeName:    "Warfarin",
		NegationInd: true,
		StatusCode:  "active",
	})
	after := medDoc(Entry{
		Kind:        "SubstanceAdministration",
		Code:        "197361",
		CodeSystem:  "RxNorm",
		CodeName:    "Warfarin",
		NegationInd: true,
		StatusCode:  "active",
	})

	report := ReconcileMedications(before, after)

	total := len(report.Added) + len(report.Removed) + len(report.Changed) + len(report.Unchanged) + len(report.Conflicts)
	if total != 0 {
		t.Errorf("both-negated drug should not appear in report at all; got %d total entries (added=%d removed=%d changed=%d unchanged=%d conflicts=%d)",
			total, len(report.Added), len(report.Removed), len(report.Changed), len(report.Unchanged), len(report.Conflicts))
	}
}
