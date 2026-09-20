package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Merging several documents into one view.
//
// The dangerous failure is merging two people's lists into one that belongs to neither, which looks entirely
// plausible. So the tests are mostly about that, and about the result being presented as a reading aid rather than as
// a record.

type mergeOut struct {
	Sources []struct {
		Position  int    `json:"position"`
		Title     string `json:"title"`
		Custodian string `json:"custodian"`
	} `json:"sources"`
	Patient struct {
		SamePatient   bool     `json:"samePatient"`
		Explanation   string   `json:"explanation"`
		Disagreements []string `json:"disagreements"`
	} `json:"patient"`
	Sections []struct {
		Kind    string `json:"kind"`
		Section struct {
			Title         string           `json:"title"`
			NarrativeText string           `json:"narrativeText"`
			Entries       []map[string]any `json:"entries"`
		} `json:"section"`
		Notes []map[string]any `json:"notes"`
	} `json:"sections"`
	Caveat string `json:"caveat"`
}

func merge(t *testing.T, h *harness, docs ...string) mergeOut {
	t.Helper()
	res := h.do("viewer", http.MethodPost, "/api/document/merge", map[string]any{"documents": docs})
	if res.Code != http.StatusOK {
		t.Fatalf("merging returned %d: %s", res.Code, res.Body.String())
	}
	var out mergeOut
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("the response is not readable: %v", err)
	}
	return out
}

// Two documents from different facilities must produce the union of their medications.
//
// This is the task the endpoint exists for: a clinician holding three summaries and working out by hand, under time
// pressure, what the patient is actually taking.
func TestMergingTwoDocumentsUnionsTheirMedications(t *testing.T) {
	h := newHarness(t)

	a := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	b := medsDoc(rootA, "MRN-1", "Lovelace", "855332|warfarin sodium 5 MG|active")

	out := merge(t, h, a, b)

	if !out.Patient.SamePatient {
		t.Fatalf("two documents for the same patient were flagged: %v", out.Patient.Disagreements)
	}

	var meds string
	for _, sec := range out.Sections {
		if strings.Contains(strings.ToLower(sec.Kind), "medication") {
			for _, e := range sec.Section.Entries {
				meds += " " + strings.ToLower(fmtAny(e["codeName"]))
			}
		}
	}

	for _, want := range []string{"metformin", "warfarin"} {
		if !strings.Contains(meds, want) {
			t.Errorf("%s is missing from the merged medications: %q", want, meds)
		}
	}
}

// The same medication from two sources must appear once.
//
// A merged list showing metformin twice reads as two prescriptions, which is how somebody ends up taking a double
// dose.
func TestTheSameMedicationFromTwoSourcesAppearsOnce(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	out := merge(t, h, doc, doc)

	count := 0
	for _, sec := range out.Sections {
		if !strings.Contains(strings.ToLower(sec.Kind), "medication") {
			continue
		}
		for _, e := range sec.Section.Entries {
			if strings.Contains(strings.ToLower(fmtAny(e["codeName"])), "metformin") {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("metformin appears %d times in the merged list; twice reads as two prescriptions", count)
	}
}

// Two different patients must be reported before anything is believed.
//
// Merging two people's lists produces one that belongs to neither, and everything else in the response is worthless if
// this is wrong.
func TestMergingDifferentPatientsIsReported(t *testing.T) {
	h := newHarness(t)

	a := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	b := medsDoc(rootA, "MRN-2", "Lovelace", "855332|warfarin sodium 5 MG|active")

	out := merge(t, h, a, b)

	if out.Patient.SamePatient {
		t.Fatal("two different identifiers under the same authority were merged as one patient")
	}
	if len(out.Patient.Disagreements) == 0 {
		t.Error("no disagreement was named, so nobody can tell which source is suspect")
	}
	// The disagreement must name which document, or with five sources nobody knows where to look.
	if !strings.Contains(strings.Join(out.Patient.Disagreements, " "), "Document") {
		t.Errorf("the disagreement does not say which document: %v", out.Patient.Disagreements)
	}
}

// A name match with no identifier must not be presented as confirmation.
func TestANameMatchWithoutAnIdentifierIsNotPresentedAsConfirmation(t *testing.T) {
	h := newHarness(t)

	a := medsDoc("", "", "Lovelace", "860975|metformin 500 MG|active")
	b := medsDoc("", "", "Lovelace", "855332|warfarin sodium 5 MG|active")

	out := merge(t, h, a, b)

	if !strings.Contains(strings.ToLower(out.Patient.Explanation), "weak evidence") {
		t.Errorf("a name-only match was not qualified: %q", out.Patient.Explanation)
	}
}

// Every source must be named, so anything in the merged view can be traced back.
//
// The first thing a clinician asks about a surprising entry is where it came from.
func TestEverySourceIsNamed(t *testing.T) {
	h := newHarness(t)

	a := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	b := medsDoc(rootA, "MRN-1", "Lovelace", "855332|warfarin sodium 5 MG|active")

	out := merge(t, h, a, b)

	if len(out.Sources) != 2 {
		t.Fatalf("%d sources were described for 2 documents", len(out.Sources))
	}
	for _, src := range out.Sources {
		if src.Position == 0 {
			t.Error("a source has no position, so nothing in the merged view can be attributed to it")
		}
		if strings.TrimSpace(src.Title) == "" {
			t.Error("a source has no title")
		}
	}
}

// The caveat must travel with the response.
//
// A merged view that looks like a clinical record and was authored by nobody is the worst possible artefact, so the
// response says what it is rather than leaving it to the interface.
func TestTheMergedViewSaysWhatItIsNot(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	out := merge(t, h, doc, doc)

	lower := strings.ToLower(out.Caveat)
	if !strings.Contains(lower, "not a clinical record") {
		t.Errorf("the response does not say what it is not: %q", out.Caveat)
	}
	if !strings.Contains(lower, "not offered as a document") {
		t.Errorf("the response does not say it is not for saving or sending: %q", out.Caveat)
	}
}

// One document is refused, because merging one with nothing is what you started with.
func TestMergingRefusesASingleDocument(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	res := h.do("viewer", http.MethodPost, "/api/document/merge", map[string]any{"documents": []string{doc}})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
}

// A refusal must say which document was the problem.
//
// "A document could not be read" when five were supplied tells nobody which to look at.
func TestAnUnreadableDocumentIsIdentifiedByPosition(t *testing.T) {
	h := newHarness(t)

	good := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	res := h.do("viewer", http.MethodPost, "/api/document/merge",
		map[string]any{"documents": []string{good, "not a document at all"}})

	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "two") {
		t.Errorf("the refusal does not say which document failed: %s", res.Body.String())
	}
}

// A ceiling, because the input arrives in a request body and each document is parsed and held.
func TestTooManyDocumentsAreRefused(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	many := make([]string, 40)
	for i := range many {
		many[i] = doc
	}

	res := h.do("viewer", http.MethodPost, "/api/document/merge", map[string]any{"documents": many})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
}

// Sections no document has must not appear as empty headings.
//
// A list of twenty headings with nothing under nineteen of them buries the one that matters.
func TestEmptySectionsAreOmitted(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	out := merge(t, h, doc, doc)

	for _, sec := range out.Sections {
		if len(sec.Section.Entries) == 0 && strings.TrimSpace(sec.Section.NarrativeText) == "" {
			t.Errorf("section %q is empty and was included anyway", sec.Kind)
		}
	}
}

// Notes must never be null, or the interface reads a length from nothing.
func TestMergeNotesAreArraysWhenEmpty(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	res := h.do("viewer", http.MethodPost, "/api/document/merge",
		map[string]any{"documents": []string{doc, doc}})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d", res.Code)
	}
	for _, field := range []string{"notes", "sections", "sources", "disagreements"} {
		if strings.Contains(res.Body.String(), `"`+field+`":null`) {
			t.Errorf("%s is null rather than an empty array", field)
		}
	}
}

func fmtAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
