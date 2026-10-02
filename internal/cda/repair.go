package cda

import (
	"fmt"
	"strings"
)

// Repairing a document that is valid and unreadable.
//
// # The failure this exists for
//
// C-CDA carries every clinical fact twice: coded entries a machine imports, and narrative a clinician reads. They
// are not alternatives, and almost every viewer in the field renders the narrative and ignores the entries
// entirely.
//
// So a section with entries and an empty narrative passes the schema, passes a receiver's import, is counted as a
// successful exchange by both ends, and displays as a blank page. The patient's medication list is in the file and
// invisible. Nobody is told, because nothing is technically wrong: the document is conformant, the transfer
// succeeded, and the only signal is a clinician looking at an empty Medications heading and concluding the patient
// takes nothing.
//
// That conclusion is the harm. "No medications" and "we failed to render the medications" look identical on
// screen, and one of them gets somebody prescribed a drug that interacts with what they are already taking.
//
// # Why repair rather than only report
//
// The narrative can be reconstructed from the entries, because the entries are the data the narrative was supposed
// to describe. That is what Generate already does for any section a caller leaves empty. Reporting "this will
// display blank" to somebody who cannot fix the sending system is worth much less than handing them a document
// that displays.
//
// # What this deliberately does not do
//
// It does not invent clinical content. Every repair here is derived from data already in the document: narrative
// from entries, and structure the schema requires. A document missing a patient's name is reported, not filled in.
// The moment a repair tool guesses at clinical facts it becomes a liability, and no downstream reader can tell
// which parts of a document were asserted by a clinician and which by a program.

// RepairKind identifies what was wrong, so a caller can group or suppress by cause.
type RepairKind string

const (
	// RepairNarrativeGenerated means a section had entries and no narrative, so it would have displayed blank.
	RepairNarrativeGenerated RepairKind = "narrative-generated"

	// RepairNarrativeIncomplete means the narrative mentioned fewer facts than the entries contain.
	RepairNarrativeIncomplete RepairKind = "narrative-incomplete"

	// RepairCustodianAdded means the document lacked the custodian C-CDA requires.
	RepairCustodianAdded RepairKind = "custodian-added"

	// RepairNotAttempted means something is wrong that cannot be derived from what the document contains.
	RepairNotAttempted RepairKind = "not-attempted"
)

// RepairAction is one thing that was changed, or one thing that could not be.
type RepairAction struct {
	Kind RepairKind `json:"kind"`

	// Section is the section title, empty for document-level repairs.
	Section string `json:"section,omitempty"`

	// What was wrong, in terms of what a person would have seen.
	Problem string `json:"problem"`

	// Consequence is what would have happened if it were left alone. Separate from Problem because the problem
	// is technical and the consequence is clinical, and only one of them makes anybody act.
	Consequence string `json:"consequence"`

	// Action is what was done about it, or why nothing was.
	Action string `json:"action"`

	// Fixed distinguishes a repair from a report.
	Fixed bool `json:"fixed"`

	// Recovered is the narrative text that was reconstructed, so somebody can check it before trusting it. A
	// repair nobody can inspect is a repair nobody should accept.
	Recovered string `json:"recovered,omitempty"`
}

// RepairReport is what was found and what was done.
type RepairReport struct {
	Repairs []RepairAction `json:"repairs"`

	// Fixed and Reported are counted separately because they lead to different actions: one means the document is
	// now usable, the other means somebody has to go back to the sender.
	Fixed    int `json:"fixed"`
	Reported int `json:"reported"`

	// InvisibleEntries is the count of coded facts that no reader would have seen. This is the number that matters
	// and the reason to run this at all.
	InvisibleEntries int `json:"invisibleEntries"`

	// Document is the repaired XML, empty when nothing could be repaired.
	Document string `json:"document,omitempty"`
}

// Usable reports whether the repaired document would now display its content.
func (r RepairReport) Usable() bool { return r.InvisibleEntries == 0 || r.Fixed > 0 }

// RepairOptions controls what the repaired document claims.
type RepairOptions struct {
	// CustodianName is used when the document has no custodian. C-CDA requires one and Generate refuses without
	// it, so a repair of a document missing its custodian needs the caller to say who is taking responsibility -
	// which is the correct place for that decision, since it is an assertion about an organisation.
	CustodianName string

	// Indent writes the result readably. Off for anything going on a wire.
	Indent bool
}

// Repair finds content that would be invisible to a reader and rebuilds it from the coded entries.
//
// The report is returned even when nothing could be repaired, because "this document will display blank and here
// is why" is the finding, and the caller has to be able to act on it either way.
func Repair(d *Document, opts RepairOptions) (RepairReport, error) {
	if d == nil {
		return RepairReport{}, fmt.Errorf("no document to repair")
	}

	report := RepairReport{Repairs: []RepairAction{}}

	// Worked on a copy. Repairing in place would mean a caller who displays the report next to the original is
	// actually displaying it next to the repair, and the comparison that justifies the change is destroyed by
	// making it.
	work := *d
	work.Sections = make([]Section, len(d.Sections))
	copy(work.Sections, d.Sections)

	for i := range work.Sections {
		s := &work.Sections[i]
		if len(s.Entries) == 0 {
			continue
		}

		name := s.Title
		if name == "" {
			name = s.CodeName
		}
		if name == "" {
			name = "an untitled section"
		}

		if strings.TrimSpace(s.NarrativeText) == "" {
			report.InvisibleEntries += len(s.Entries)

			// Cleared so Generate rebuilds it. It only generates narrative for sections that have none, which is
			// exactly the condition here.
			s.NarrativeText = ""
			s.NarrativeHTML = ""

			report.Repairs = append(report.Repairs, RepairAction{
				Kind:    RepairNarrativeGenerated,
				Section: name,
				Problem: fmt.Sprintf("%s holds %s but no narrative text",
					name, plural(len(s.Entries), "coded entry", "coded entries")),
				Consequence: "Most viewers render the narrative and ignore the entries, so this section would " +
					"have displayed as empty. A clinician reading it would conclude there is nothing to report, " +
					"which is indistinguishable from a section that genuinely has nothing in it.",
				Action: "Narrative rebuilt from the coded entries.",
				Fixed:  true,
			})
			continue
		}

		// A narrative that exists but omits facts the entries contain.
		//
		// Partial narrative is more dangerous than none, because a section that displays three of five medications
		// looks complete. Nobody counts. This is reported rather than repaired: overwriting narrative a clinician
		// may have written and attested is not a program's decision, and the attested narrative is the legal
		// content of the document.
		if missing := entriesNotInNarrative(*s); len(missing) > 0 {
			report.InvisibleEntries += len(missing)
			report.Repairs = append(report.Repairs, RepairAction{
				Kind:    RepairNarrativeIncomplete,
				Section: name,
				Problem: fmt.Sprintf("%s describes some of its entries but not %s",
					name, strings.Join(missing, ", ")),
				Consequence: "A section that shows three of five medications looks complete, because nobody " +
					"counts. This is worse than a blank section, which at least looks wrong.",
				Action: "Left alone and reported. The narrative is the attested content of the document and may " +
					"have been written by a clinician; a program overwriting it would replace a human assertion " +
					"with a derived one and leave no way to tell which is which.",
				Fixed:     false,
				Recovered: strings.Join(missing, ", "),
			})
		}
	}

	custodian := opts.CustodianName
	if strings.TrimSpace(custodian) == "" {
		custodian = strings.TrimSpace(d.Custodian)
	}
	if strings.TrimSpace(custodian) == "" {
		report.Repairs = append(report.Repairs, RepairAction{
			Kind:    RepairNotAttempted,
			Problem: "the document names no custodian",
			Consequence: "C-CDA requires one, so a receiver may reject the document outright. The custodian is " +
				"the organisation answerable for the content.",
			Action: "Not filled in. Naming the organisation responsible for a clinical document is an assertion " +
				"about that organisation, not a detail a program should decide.",
			Fixed: false,
		})
	}

	for _, rep := range report.Repairs {
		if rep.Fixed {
			report.Fixed++
		} else {
			report.Reported++
		}
	}

	// Regenerated only when there is something to fix and enough to fix it with. A caller who gets no document
	// back still gets the report, which is the part that tells them what to do.
	if report.Fixed > 0 && strings.TrimSpace(custodian) != "" {
		out, err := Generate(&work, GenerateOptions{
			DocumentType:  "CCD",
			CustodianName: custodian,
			Indent:        opts.Indent,
		})
		if err != nil {
			// Reported rather than returned as a failure. The findings above are still true and still worth
			// having, and a repair tool that answers "no" to everything because one step failed is less useful
			// than one that says what it found and why it could not act.
			report.Repairs = append(report.Repairs, RepairAction{
				Kind:        RepairNotAttempted,
				Problem:     "the repaired document could not be written",
				Consequence: "The findings above still hold; only the corrected file is missing.",
				Action:      err.Error(),
			})
			report.Fixed = 0
			report.Reported++
			return report, nil
		}
		report.Document = string(out)
	}

	return report, nil
}

// entriesNotInNarrative returns descriptions of entries the narrative does not mention.
//
// Matching is deliberately generous: it looks for the entry's display name in the narrative, case-insensitively,
// and treats a hit as described. A stricter check would flag every narrative that words things differently from
// the code system, which is most well-written narrative, and the report would become noise.
func entriesNotInNarrative(s Section) []string {
	narrative := strings.ToLower(s.NarrativeText + " " + s.NarrativeHTML)

	var missing []string
	for _, e := range s.Entries {
		name := strings.TrimSpace(e.CodeName)
		if name == "" {
			name = strings.TrimSpace(e.Describe())
		}
		// Skipped rather than guessed at. An entry with no display name cannot be looked for in prose, and
		// reporting it as absent would be reporting the limits of this check as a fault in the document.
		if name == "" {
			continue
		}

		// Very short names are skipped for the same reason: a two-character code appears inside unrelated words
		// and would be found in any narrative at all.
		if len([]rune(name)) < 4 {
			continue
		}

		if !strings.Contains(narrative, strings.ToLower(name)) {
			missing = append(missing, name)
		}
	}
	return missing
}
