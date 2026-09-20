package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Combining several documents into one summary.
//
// # The problem this solves
//
// A patient has been seen at three facilities. Each sends a summary, and a clinician now holds three documents that
// each claim to describe the same person's medications, problems and allergies. Reading all three and working out the
// union by hand, under time pressure, is exactly the task that goes wrong.
//
// This unions the sections: entries deduplicated by code, narratives concatenated with their source named, and notes
// explaining anything that conflicted or was dropped.
//
// # What it is not
//
// It is not a merged clinical record and does not claim to be. It is a reading aid: a view of what all the sources say
// together, with disagreements surfaced rather than resolved. Resolving a disagreement about whether a patient takes
// a drug is a clinical judgement, and a program that made it silently would produce a document that looks like a
// medical record and was authored by nobody.
//
// That is why the result is presented as a comparison rather than offered as a document to save or send. The
// generator could produce conformant XML from it and deliberately is not asked to: nobody attested it.
//
// # The last of the three
//
// MergeSections was the third capability in the CDA package written, tested and never called. All three are now
// reachable.
func (s *Server) handleMergeDocuments(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		// Documents are the sources, in no particular order.
		Documents []string `json:"documents"`

		// Sections limits which sections to merge. Empty merges every section any document has.
		Sections []string `json:"sections"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if len(body.Documents) < 2 {
		s.fail(w, r, http.StatusBadRequest,
			"supply at least two documents. Merging one with nothing produces the document you started with")
		return
	}

	// A ceiling, because each document is parsed and held in memory and this endpoint takes its input from a
	// request body. Without a limit somebody can post two hundred documents and find out how much memory this
	// server has.
	const maxDocuments = 20
	if len(body.Documents) > maxDocuments {
		s.fail(w, r, http.StatusBadRequest,
			"that is more documents than this will merge at once. Twenty is the limit, and a summary drawn from "+
				"more sources than that is not a summary anybody reads")
		return
	}

	docs := make([]*cda.Document, 0, len(body.Documents))
	for i, raw := range body.Documents {
		if strings.TrimSpace(raw) == "" {
			s.fail(w, r, http.StatusBadRequest, "document "+ordinal(i+1)+" is empty")
			return
		}
		doc, err := cda.Parse([]byte(raw))
		if err != nil {
			// Numbered, because "a document could not be read" when five were supplied tells nobody which to look at.
			s.fail(w, r, http.StatusBadRequest, "document "+ordinal(i+1)+" could not be read: "+err.Error())
			return
		}
		docs = append(docs, doc)
	}

	// Whether these are all the same patient, before anything is merged.
	//
	// Merging two people's medication lists produces one list that belongs to neither, and it looks entirely
	// plausible. This is the single most dangerous thing this endpoint could do quietly, so it is answered first and
	// reported at the top.
	agreement := patientAgreement(docs)

	kinds := body.Sections
	if len(kinds) == 0 {
		kinds = sectionKindsAcross(docs)
	}

	merged := make([]map[string]any, 0, len(kinds))
	for _, kind := range kinds {
		section, notes := cda.MergeSections(kind, docs...)

		// A section no document had is skipped rather than reported as empty. A list of twenty headings with nothing
		// under nineteen of them buries the one that matters.
		if len(section.Entries) == 0 && strings.TrimSpace(section.NarrativeText) == "" {
			continue
		}

		if notes == nil {
			notes = []cda.Note{}
		}
		merged = append(merged, map[string]any{
			"kind":    kind,
			"section": section,
			"notes":   notes,
		})
	}

	s.ok(w, map[string]any{
		"sources":  describeSources(docs),
		"patient":  agreement,
		"sections": merged,

		// Stated in the response, not only in the interface, because an API consumer needs it as much as a person.
		"caveat": "This is a reading aid, not a clinical record. Entries from different sources are shown together " +
			"with their disagreements surfaced rather than resolved, and nothing here has been reviewed or " +
			"approved by a clinician. It is deliberately not offered as a document to save or send.",
	})
}

// patientAgreement reports whether every source describes the same person.
func patientAgreement(docs []*cda.Document) map[string]any {
	// Compared pairwise against the first, because that is what a reader is doing: taking one document as the subject
	// and asking whether the others are about the same person.
	disagreements := []string{}
	unconfirmed := 0

	for i := 1; i < len(docs); i++ {
		same, why := patientsMatch(docs[0], docs[i])
		if !same {
			disagreements = append(disagreements, "Document "+ordinal(i+1)+": "+why)
		} else if strings.Contains(why, "names match") {
			// A name match with no identifier is agreement of the weakest kind, counted separately so it is not
			// presented as confirmation.
			unconfirmed++
		}
	}

	out := map[string]any{
		"samePatient":   len(disagreements) == 0,
		"disagreements": disagreements,
	}

	switch {
	case len(disagreements) > 0:
		out["explanation"] = "At least one source may describe a different person. Merging two people's lists " +
			"produces one that belongs to neither, and it looks entirely plausible."
	case unconfirmed > 0:
		out["explanation"] = "The sources agree on the patient's name but not all of them carry an identifier that " +
			"could confirm it. Names are weak evidence: people marry, systems truncate, and one end may hold a " +
			"preferred name."
	default:
		out["explanation"] = "Every source carries the same patient identifier under a shared assigning authority."
	}
	return out
}

// describeSources names each document so a reader can attribute anything in the merged view.
//
// A merged summary whose lines cannot be traced back to a source is a summary nobody can check, and the first thing a
// clinician asks about a surprising entry is where it came from.
func describeSources(docs []*cda.Document) []map[string]any {
	out := make([]map[string]any, 0, len(docs))
	for i, d := range docs {
		title := strings.TrimSpace(d.Title)
		if title == "" {
			title = strings.TrimSpace(d.TypeName)
		}
		if title == "" {
			title = "Untitled document"
		}
		out = append(out, map[string]any{
			"position":  i + 1,
			"title":     title,
			"custodian": d.Custodian,
			"effective": d.EffectiveTime,
			"patient":   d.Patient.Name(),
			"sections":  len(d.Sections),
		})
	}
	return out
}

// sectionKindsAcross lists every recognised section kind any document has.
func sectionKindsAcross(docs []*cda.Document) []string {
	seen := map[string]bool{}
	for _, d := range docs {
		for _, s := range d.Sections {
			if s.Kind != "" {
				seen[s.Kind] = true
			}
		}
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}

	// Sorted so the same inputs produce the same order. An unstable order makes two runs look like different results.
	sort.Strings(out)
	return out
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "one"
	case 2:
		return "two"
	case 3:
		return "three"
	case 4:
		return "four"
	case 5:
		return "five"
	}
	// strconv for anything larger. The words exist only because "document one could not be read" reads better in a
	// refusal than "document 1", and past five the digit is clearer anyway.
	return strconv.Itoa(n)
}
