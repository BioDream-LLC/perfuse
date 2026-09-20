package cda

import (
	"fmt"
	"strings"
)

// MedChange describes one difference between two medication lists.
// JSON tags because this now leaves the process. It was written as an internal type and had no caller outside the
// package, which is why it had none.
type MedChange struct {
	// Kind is "added", "removed", "unchanged" or "changed".
	Kind       string `json:"kind"`
	Medication string `json:"medication"`
	Code       string `json:"code,omitempty"`
	CodeSystem string `json:"codeSystem,omitempty"`

	// Before is nil for an addition, After is nil for a removal. Both are carried so a reader can see the entries
	// the judgement was made from rather than taking the summary on trust.
	Before *Entry `json:"before,omitempty"`
	After  *Entry `json:"after,omitempty"`

	// Detail says what changed, for "changed".
	Detail string `json:"detail,omitempty"`
}

// ReconcileReport is the result of comparing medication lists across two documents.
type ReconcileReport struct {
	Added     []MedChange `json:"added"`
	Removed   []MedChange `json:"removed"`
	Changed   []MedChange `json:"changed"`
	Unchanged []MedChange `json:"unchanged"`

	// Conflicts flags a medication that appears active in one document and completed or discontinued in the other.
	// This is the clinically dangerous case and the reason to run this at all: the two documents disagree about
	// whether the patient is currently taking something, and whichever the receiving clinician happens to read
	// decides what they believe.
	Conflicts []MedChange `json:"conflicts"`
}

// ReconcileMedications compares medication lists across two documents.
//
// It matches medications by code first (Code+CodeSystem), falling back to
// normalised name comparison (lowercase, trimmed) when either entry lacks a code.
// An entry with NegationInd true means the medication is explicitly NOT taken and
// is treated as absence, never as presence.
func ReconcileMedications(before, after *Document) ReconcileReport {
	// Initialised empty rather than left nil.
	//
	// A nil slice marshals to null, and a reader that asks how many medications changed gets an error instead of
	// zero. "No changes" and "the field is missing" are different answers, and only one of them is true here.
	report := ReconcileReport{
		Added:     []MedChange{},
		Removed:   []MedChange{},
		Changed:   []MedChange{},
		Unchanged: []MedChange{},
		Conflicts: []MedChange{},
	}

	beforeMeds := collectMedications(before)
	afterMeds := collectMedications(after)

	// Index after medications by key for matching.
	afterByCode := map[string]*Entry{}
	afterByName := map[string]*Entry{}
	afterMatched := map[string]bool{}

	for i := range afterMeds {
		e := &afterMeds[i]
		if key := medCodeKey(e); key != "" {
			afterByCode[key] = e
		}
		if name := medNormName(e); name != "" {
			afterByName[name] = e
		}
	}

	for i := range beforeMeds {
		b := &beforeMeds[i]
		var match *Entry
		var matchKey string

		// Match by code first.
		if key := medCodeKey(b); key != "" {
			if a, ok := afterByCode[key]; ok {
				match = a
				matchKey = key
			}
		}

		// Fall back to normalised name.
		if match == nil {
			if name := medNormName(b); name != "" {
				if a, ok := afterByName[name]; ok {
					match = a
					matchKey = medCodeKey(a)
					if matchKey == "" {
						matchKey = "name:" + medNormName(a)
					}
				}
			}
		}

		if match == nil {
			report.Removed = append(report.Removed, MedChange{
				Kind:       "removed",
				Medication: medName(b),
				Code:       b.Code,
				CodeSystem: b.CodeSystem,
				Before:     b,
			})
			continue
		}

		// Mark matched so we can find additions later.
		if key := medCodeKey(match); key != "" {
			afterMatched[key] = true
		}
		if name := medNormName(match); name != "" {
			afterMatched["name:"+name] = true
		}

		change := compareMedications(b, match)
		// Check if this is a status conflict.
		if isStatusConflict(b, match) {
			report.Conflicts = append(report.Conflicts, MedChange{
				Kind:       "changed",
				Medication: change.Medication,
				Code:       change.Code,
				CodeSystem: change.CodeSystem,
				Before:     b,
				After:      match,
				Detail:     change.Detail,
			})
		} else {
			switch change.Kind {
			case "unchanged":
				report.Unchanged = append(report.Unchanged, change)
			case "changed":
				report.Changed = append(report.Changed, change)
			}
		}
	}

	// Find additions: after meds that were not matched.
	for i := range afterMeds {
		a := &afterMeds[i]
		matched := false
		if key := medCodeKey(a); key != "" && afterMatched[key] {
			matched = true
		}
		if !matched {
			if name := medNormName(a); name != "" && afterMatched["name:"+name] {
				matched = true
			}
		}
		if !matched {
			report.Added = append(report.Added, MedChange{
				Kind:       "added",
				Medication: medName(a),
				Code:       a.Code,
				CodeSystem: a.CodeSystem,
				After:      a,
			})
		}
	}

	return report
}

// collectMedications returns the active medication entries from a document,
// skipping negated entries (which assert absence, not presence).
func collectMedications(d *Document) []Entry {
	if d == nil {
		return nil
	}
	var out []Entry
	for _, s := range d.Sections {
		if s.Kind != "Medications" {
			continue
		}
		for _, e := range s.Entries {
			// NegationInd true means the medication is explicitly NOT taken.
			// Treating it as presence is the most dangerous mistake available.
			if e.NegationInd {
				continue
			}
			out = append(out, e)
		}
	}
	return out
}

// medCodeKey returns a matching key from Code+CodeSystem, or empty if unavailable.
func medCodeKey(e *Entry) string {
	if e.Code == "" || e.CodeSystem == "" {
		return ""
	}
	return e.CodeSystem + "|" + e.Code
}

// medNormName returns a normalised name for fallback matching.
func medNormName(e *Entry) string {
	name := e.CodeName
	if name == "" {
		name = e.Value
	}
	if name == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// medName returns the human-readable medication name.
func medName(e *Entry) string {
	if e.CodeName != "" {
		return e.CodeName
	}
	return e.Value
}

// compareMedications compares two matched medication entries.
func compareMedications(before, after *Entry) MedChange {
	mc := MedChange{
		Medication: medName(after),
		Code:       after.Code,
		CodeSystem: after.CodeSystem,
		Before:     before,
		After:      after,
	}

	var diffs []string

	// Check status change.
	if before.StatusCode != after.StatusCode {
		diffs = append(diffs, fmt.Sprintf("status %s → %s", statusLabel(before.StatusCode), statusLabel(after.StatusCode)))
	}

	// Check dose change via Value/Unit.
	if doseString(before) != doseString(after) {
		diffs = append(diffs, fmt.Sprintf("dose %s → %s", doseString(before), doseString(after)))
	}

	// Check dose change via Children entries.
	if childDoseDesc(before) != childDoseDesc(after) {
		bd := childDoseDesc(before)
		ad := childDoseDesc(after)
		if bd == "" {
			bd = "(none)"
		}
		if ad == "" {
			ad = "(none)"
		}
		diffs = append(diffs, fmt.Sprintf("dose detail %s → %s", bd, ad))
	}

	if len(diffs) == 0 {
		mc.Kind = "unchanged"
		return mc
	}

	mc.Kind = "changed"
	mc.Detail = strings.Join(diffs, "; ")
	return mc
}

// isStatusConflict returns true when the status change implies a medication that
// was current is no longer current, or vice versa.
func isStatusConflict(before, after *Entry) bool {
	if before.StatusCode == after.StatusCode {
		return false
	}
	bActive := isActiveStatus(before.StatusCode)
	aActive := isActiveStatus(after.StatusCode)
	return bActive != aActive
}

// isActiveStatus returns true for statuses that mean a medication is current.
func isActiveStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "":
		return true
	default:
		return false
	}
}

func statusLabel(s string) string {
	if s == "" {
		return "(empty/active)"
	}
	return s
}

func doseString(e *Entry) string {
	if e.Value == "" && e.Unit == "" {
		return ""
	}
	return strings.TrimSpace(e.Value + " " + e.Unit)
}

// childDoseDesc builds a comparable description of dose-related children.
func childDoseDesc(e *Entry) string {
	var parts []string
	for _, c := range e.Children {
		if c.Value != "" || c.Unit != "" {
			parts = append(parts, strings.TrimSpace(c.Value+" "+c.Unit))
		}
	}
	return strings.Join(parts, ", ")
}

// MergeSections combines the same section from several documents, deduplicating
// entries and concatenating narratives.
//
// It collects every section whose Kind matches from all documents, unions their
// entries deduplicating by code (or normalised name when uncoded), concatenates
// narrative text with a blank line between sources, and returns Notes explaining
// anything dropped or that conflicted.
func MergeSections(kind string, docs ...*Document) (Section, []Note) {
	var notes []Note
	var sections []Section

	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, s := range d.Sections {
			if s.Kind == kind {
				sections = append(sections, s)
			}
		}
	}

	if len(sections) == 0 {
		return Section{Kind: kind, Empty: true}, notes
	}

	merged := Section{
		Kind: kind,
	}

	// Determine NilFlavor: keep it only if every source has one.
	allNil := true
	anyNil := false
	for _, s := range sections {
		if s.NilFlavor != "" {
			anyNil = true
		} else {
			allNil = false
		}
	}

	if allNil && anyNil {
		merged.NilFlavor = sections[0].NilFlavor
	} else if anyNil && !allNil {
		notes = append(notes, Note{
			Severity: "info",
			Path:     "section/" + kind,
			Message:  "a statement of absence (NilFlavor) was overridden by a positive finding from another source",
			Rule:     "merge-nilflavor-override",
		})
	}

	// Use the first non-empty title and code.
	for _, s := range sections {
		if merged.Title == "" && s.Title != "" {
			merged.Title = s.Title
		}
		if merged.Code == "" && s.Code != "" {
			merged.Code = s.Code
			merged.CodeSystem = s.CodeSystem
			merged.CodeName = s.CodeName
		}
	}

	// Concatenate narrative text with a blank line between sources.
	var narratives []string
	for _, s := range sections {
		text := strings.TrimSpace(s.NarrativeText)
		if text != "" {
			narratives = append(narratives, text)
		}
	}
	merged.NarrativeText = strings.Join(narratives, "\n\n")

	// Union entries, deduplicating by code (or normalised name).
	seen := map[string]bool{}
	for _, s := range sections {
		for _, e := range s.Entries {
			key := entryDedupeKey(e)
			if key != "" && seen[key] {
				notes = append(notes, Note{
					Severity: "info",
					Path:     "section/" + kind,
					Message:  fmt.Sprintf("duplicate entry %q dropped during merge", e.Describe()),
					Rule:     "merge-dedup",
				})
				continue
			}
			if key != "" {
				seen[key] = true
			}
			merged.Entries = append(merged.Entries, e)
		}
	}

	merged.Empty = strings.TrimSpace(merged.NarrativeText) == "" && len(merged.Entries) == 0

	return merged, notes
}

// entryDedupeKey returns a key for deduplication: code-based when available,
// normalised name otherwise. It includes dose and status so that two entries
// for the same drug at different doses or in different states are kept as
// distinct rather than silently dropped.
func entryDedupeKey(e Entry) string {
	var base string
	if e.Code != "" && e.CodeSystem != "" {
		base = e.CodeSystem + "|" + e.Code
	} else {
		name := e.CodeName
		if name == "" {
			name = e.Value
		}
		if name == "" {
			return ""
		}
		base = "name:" + strings.ToLower(strings.TrimSpace(name))
	}
	// Distinguish entries that differ by dose or status.
	dose := strings.TrimSpace(e.Value + " " + e.Unit)
	if dose != "" || e.StatusCode != "" {
		base += "|" + dose + "|" + e.StatusCode
	}
	return base
}
