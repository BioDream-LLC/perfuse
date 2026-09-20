package api

import (
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Comparing two medication lists across a transition of care.
//
// # Why this is the dangerous moment
//
// A patient moves between facilities and each end holds a medication list. The lists disagree, because one was
// updated during the admission and the other was not, or because a drug was stopped and the stop never propagated.
// Whichever list the receiving clinician happens to read decides what they believe the patient is taking.
//
// The case that hurts is not a medication present in one list and absent from the other, which is at least visible.
// It is a medication that appears active in one and discontinued in the other. Both documents are internally
// consistent, both are conformant, and they contradict each other about whether the patient is currently taking
// something. Nothing in either file says so.
//
// # Reachable, at last
//
// This was the third of three capabilities in the CDA package that were written, tested and had no caller. 416
// lines, correct, and unusable, which is the same as absent.
func (s *Server) handleReconcileDocuments(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		// Before is the earlier document, After the later one. Named rather than positional because getting them
		// the wrong way round inverts every finding - an added drug reads as a stopped one - and a report that is
		// confidently backwards is worse than no report.
		Before string `json:"before"`
		After  string `json:"after"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if body.Before == "" || body.After == "" {
		s.fail(w, r, http.StatusBadRequest,
			"supply both documents: the earlier one as before and the later one as after")
		return
	}

	before, err := cda.Parse([]byte(body.Before))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the earlier document could not be read: "+err.Error())
		return
	}

	after, err := cda.Parse([]byte(body.After))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the later document could not be read: "+err.Error())
		return
	}

	report := cda.ReconcileMedications(before, after)

	// Whether the two documents are even about the same patient.
	//
	// Reconciling two different people's medication lists produces a report full of additions and removals that
	// looks exactly like a patient whose therapy changed completely. It is a plausible mistake to make with two
	// files on a desktop, and the consequence of not noticing is a reconciliation performed against a stranger's
	// list. So the identifiers are compared and the answer is reported rather than assumed.
	match, why := patientsMatch(before, after)

	s.ok(w, map[string]any{
		"report": report,
		"patient": map[string]any{
			"samePatient": match,
			"explanation": why,
			"before":      before.Patient.Name(),
			"after":       after.Patient.Name(),
		},
	})
}

// patientsMatch compares the record targets of two documents.
//
// # Two MRNs are only comparable when they share a root
//
// An identifier in CDA is a root plus an extension: the root names the assigning authority, the extension is the
// number that authority issued. "MRN 12345" from two different hospitals is two different patients who happen to
// share a number, and treating those as a match is the single most common cause of a wrongly merged record.
//
// So this looks for a root both documents carry and compares the extensions within it. Identifiers under roots only
// one side has are not evidence either way, and saying so is more useful than a confident answer derived from
// namespaces that were never comparable.
//
// # Why the name is only a fallback
//
// A name match is weak evidence and a name mismatch is weak evidence: people marry, systems truncate, and one end
// may hold a preferred name. So the name is used only when no shared root exists, and the report says that is what
// happened rather than presenting it as confirmation.
func patientsMatch(before, after *cda.Document) (bool, string) {
	byRoot := map[string]string{}
	for _, id := range before.Patient.Identifiers {
		if id.Root != "" && id.Extension != "" {
			byRoot[id.Root] = id.Extension
		}
	}

	var shared, agreed, disagreed []string
	for _, id := range after.Patient.Identifiers {
		if id.Root == "" || id.Extension == "" {
			continue
		}
		prior, ok := byRoot[id.Root]
		if !ok {
			continue
		}
		shared = append(shared, id.Root)
		if prior == id.Extension {
			agreed = append(agreed, id.Extension)
		} else {
			disagreed = append(disagreed, prior+" and "+id.Extension)
		}
	}

	switch {
	case len(disagreed) > 0:
		return false, "The two documents give different identifiers under the same assigning authority (" +
			strings.Join(disagreed, "; ") + "), so this is very likely a comparison between two different " +
			"people. A reconciliation performed against the wrong list is worse than none."

	case len(agreed) > 0:
		return true, "Both documents carry the same patient identifier under a shared assigning authority."

	case len(shared) == 0 && (len(before.Patient.Identifiers) > 0 || len(after.Patient.Identifiers) > 0):
		// The honest answer, and the one a confident tool gets wrong.
		return false, "The documents carry identifiers, but none from the same assigning authority, so their " +
			"numbers are not comparable. An MRN means nothing without knowing who issued it: the same number " +
			"from two hospitals is two different patients."
	}

	bn, an := before.Patient.Name(), after.Patient.Name()
	if bn == "" || an == "" {
		return false, "At least one document does not identify its patient, so nothing here confirms the two " +
			"lists belong to the same person."
	}
	if strings.EqualFold(bn, an) {
		return true, "The names match, but neither document carries an identifier that could confirm it. Names " +
			"are weak evidence: people marry, systems truncate, and one end may hold a preferred name."
	}
	return false, "The names differ (" + bn + " and " + an + ") and there is no shared identifier to check " +
		"them against."
}
