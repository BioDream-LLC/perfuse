package cda

import (
	"bytes"
	"strings"
	"testing"
)

// Rendering a clinical document for somebody to read, print, fax or file.
//
// The tests that matter here are about what is on the page and what deliberately is not. A PDF that opens and shows
// the wrong thing is worse than one that fails to open, because nobody investigates it.

func TestTheNarrativeIsWhatGetsRendered(t *testing.T) {
	d, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}

	out, err := RenderPDF(d, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Fatalf("the output is not a PDF, it begins %q", out[:min2(16, len(out))])
	}

	// The patient must be identifiable beyond doubt. A printout with a name and no date of birth cannot be told
	// apart from another patient of the same surname on the same ward, which is the commonest complaint about
	// rendered clinical documents.
	body := string(out)
	for _, want := range []string{"Patient", "Date of birth"} {
		if !strings.Contains(body, want) {
			t.Errorf("the printout has no %q line", want)
		}
	}
}

// A section with coded entries and no narrative must print as visibly unattested, not blank and not filled in.
//
// Filling it in from the codes would put content on a printout that no clinician wrote or approved, on a page that
// will be filed and read years later by somebody with no way to know which parts a program invented.
//
// Leaving it blank is the failure this whole line of work is about: a blank section reads as "nothing to report",
// and that is what gets somebody prescribed a drug that interacts with what they are already taking.
func TestASectionWithNoNarrativePrintsAsUnattestedRatherThanBlank(t *testing.T) {
	d, err := Parse([]byte(invisibleMedication))
	if err != nil {
		t.Fatal(err)
	}

	out, err := RenderPDF(d, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)

	// It must say something about the section being unattested and how much is behind it.
	if !strings.Contains(body, "no narrative") {
		t.Error("a section with no narrative printed without saying so")
	}
	if !strings.Contains(body, "2 coded") {
		t.Error("the printout does not say how many coded facts are behind the empty section")
	}

	// And it must not have quietly rendered the drug names as though a clinician had written them.
	for _, drug := range []string{"metformin", "warfarin"} {
		if strings.Contains(strings.ToLower(body), drug) {
			t.Errorf("%s was printed as narrative, but no clinician wrote or approved it", drug)
		}
	}
}

// Asking for the codes prints them, labelled as a restatement rather than as the narrative.
func TestCodesArePrintedOnlyWhenAskedFor(t *testing.T) {
	d, err := Parse([]byte(invisibleMedication))
	if err != nil {
		t.Fatal(err)
	}

	with, err := RenderPDF(d, RenderOptions{IncludeCodes: true})
	if err != nil {
		t.Fatal(err)
	}

	// The drug name appears now, in a table that is labelled as the coded entries.
	if !strings.Contains(strings.ToLower(string(with)), "metformin") {
		t.Error("the coded entries were requested and the medication is not on the page")
	}
	if !strings.Contains(string(with), "Coded entries behind this section") {
		t.Error("the coded entries are not labelled as such, so they read as attested narrative")
	}
}

// Narrative held as a table must not collapse into one line.
//
// Deleting markup without replacing it joins cells: "metformin500 MGactive" is nonsense that looks plausible, and
// plausible nonsense on a medication list is worse than obviously broken output.
func TestATableInTheNarrativeDoesNotCollapseIntoOneLine(t *testing.T) {
	html := `<table><thead><tr><th>Medication</th><th>Dose</th></tr></thead>` +
		`<tbody><tr><td>metformin</td><td>500 MG</td></tr>` +
		`<tr><td>warfarin</td><td>5 MG</td></tr></tbody></table>`

	text := stripTags(html)

	if strings.Contains(text, "metformin500") || strings.Contains(text, "MGwarfarin") {
		t.Errorf("cells were joined together: %q", text)
	}

	// Each row on its own line, so the doses stay with their drugs.
	lines := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines < 3 {
		t.Errorf("a two-row table with a header rendered as %d lines: %q", lines, text)
	}
}

// A date nobody can parse must survive onto the page.
//
// Blanking it loses the only clue as to what the sending system meant, and an unreadable date is still evidence.
func TestAnUnrecognisedDateIsKept(t *testing.T) {
	for _, in := range []string{"not-a-date", "2026", "00000000"} {
		if got := formatCDADate(in); got != in {
			t.Errorf("formatCDADate(%q) = %q; an unreadable date is still evidence and must not be changed",
				in, got)
		}
	}
}

func TestADateIsRenderedForAPersonToRead(t *testing.T) {
	if got := formatCDADate("20260818120000-0500"); !strings.Contains(got, "August") {
		t.Errorf("formatCDADate gave %q, which nobody reads as a date", got)
	}
	if got := formatCDADate("19151210"); got != "10 December 1915" {
		t.Errorf("formatCDADate gave %q, want 10 December 1915", got)
	}
}

// A document with nothing in it must still render, because "the file would not open" and "the document was empty"
// send somebody looking in completely different places.
func TestAnAlmostEmptyDocumentStillRenders(t *testing.T) {
	d, err := Parse([]byte(`<?xml version="1.0"?>
<ClinicalDocument xmlns="urn:hl7-org:v3"><title>Nothing Much</title></ClinicalDocument>`))
	if err != nil {
		t.Fatal(err)
	}

	out, err := RenderPDF(d, RenderOptions{})
	if err != nil {
		t.Fatalf("a sparse document failed to render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Error("the output is not a PDF")
	}
	if !strings.Contains(string(out), "Nothing Much") {
		t.Error("the title did not reach the page")
	}
}

// Every printout must say what produced it.
//
// Somebody finding a clinical document in a file in three years needs to know where it came from, and a rendered
// document with no provenance cannot be audited.
func TestThePrintoutSaysWhatProducedIt(t *testing.T) {
	d, err := Parse([]byte(sampleCCD))
	if err != nil {
		t.Fatal(err)
	}

	out, err := RenderPDF(d, RenderOptions{Footer: "Printed at Example Hospital."})
	if err != nil {
		t.Fatal(err)
	}

	body := string(out)
	if !strings.Contains(body, "Perfuse") {
		t.Error("the printout does not say what rendered it")
	}
	if !strings.Contains(body, "Printed at Example Hospital") {
		t.Error("the site's own provenance note did not reach the page")
	}
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
