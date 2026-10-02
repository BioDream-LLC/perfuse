package pdf

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A PDF is read by seeking to the trailer, reading the cross-reference table, and jumping to byte offsets.
//
// So a single wrong offset makes the whole file unopenable, and the reader's error message says nothing useful about
// which object is wrong. That failure cannot be caught by looking at the output, which is why these tests parse the
// structure rather than checking that some text appears somewhere.

var xrefEntry = regexp.MustCompile(`(?m)^(\d{10}) 00000 n $`)

// checkStructure verifies every offset in the cross-reference table points at the object it claims.
func checkStructure(t *testing.T, out []byte) int {
	t.Helper()

	if !bytes.HasPrefix(out, []byte("%PDF-1.4")) {
		t.Fatalf("the file does not start with a PDF header, it starts %q", out[:min(16, len(out))])
	}
	if !bytes.HasSuffix(bytes.TrimRight(out, "\n"), []byte("%%EOF")) {
		t.Error("the file does not end with the end-of-file marker, which some readers require")
	}

	// startxref must point at the word xref, or a reader cannot find the table at all.
	idx := bytes.LastIndex(out, []byte("startxref"))
	if idx < 0 {
		t.Fatal("there is no startxref, so no reader can find the cross-reference table")
	}
	var xrefPos int
	if _, err := fmt.Sscanf(string(out[idx:]), "startxref\n%d", &xrefPos); err != nil {
		t.Fatalf("startxref is not readable: %v", err)
	}
	if xrefPos <= 0 || xrefPos >= len(out) {
		t.Fatalf("startxref points to %d, which is outside a file of %d bytes", xrefPos, len(out))
	}
	if !bytes.HasPrefix(out[xrefPos:], []byte("xref")) {
		t.Fatalf("startxref points at %q rather than the cross-reference table",
			out[xrefPos:min(xrefPos+20, len(out))])
	}

	// Every offset must land on "N 0 obj" for the right N. This is the check that catches an arithmetic slip.
	matches := xrefEntry.FindAllStringSubmatch(string(out), -1)
	if len(matches) == 0 {
		t.Fatal("the cross-reference table has no entries")
	}
	for i, m := range matches {
		off, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("offset %q is not a number", m[1])
		}
		if off <= 0 || off >= len(out) {
			t.Fatalf("object %d has offset %d, outside a file of %d bytes", i+1, off, len(out))
		}
		want := fmt.Sprintf("%d 0 obj", i+1)
		if !bytes.HasPrefix(out[off:], []byte(want)) {
			t.Fatalf("object %d should be at %d but that byte begins %q — every reader will reject this file",
				i+1, off, out[off:min(off+24, len(out))])
		}
	}

	// The declared size must match, and the free-list head must be exactly as the specification requires.
	if !bytes.Contains(out, []byte("0000000000 65535 f ")) {
		t.Error("the free-list head is missing or malformed")
	}
	if want := fmt.Sprintf("/Size %d", len(matches)+1); !bytes.Contains(out, []byte(want)) {
		t.Errorf("the trailer does not declare %s", want)
	}

	return len(matches)
}

// Every stream's declared length must match the bytes that follow it.
//
// A reader that trusts a wrong /Length reads into the next object and renders nothing, or renders the following
// object's instructions as text. This is a silent corruption: the file opens.
func checkStreamLengths(t *testing.T, out []byte) {
	t.Helper()

	rest := out
	for {
		i := bytes.Index(rest, []byte("/Length "))
		if i < 0 {
			return
		}
		var declared int
		if _, err := fmt.Sscanf(string(rest[i:]), "/Length %d", &declared); err != nil {
			t.Fatalf("a stream length is not readable: %v", err)
		}

		start := bytes.Index(rest[i:], []byte("stream\n"))
		if start < 0 {
			return
		}
		start += i + len("stream\n")

		end := bytes.Index(rest[start:], []byte("\nendstream"))
		if end < 0 {
			t.Fatal("a stream has no endstream")
		}
		if end != declared {
			t.Errorf("a stream declares /Length %d and contains %d bytes; a reader will read past the end of it",
				declared, end)
		}
		rest = rest[start+end:]
	}
}

func TestAStructuredDocumentIsStructurallyValid(t *testing.T) {
	d := New("Continuity of Care Document")
	d.SetMeta("Example Hospital", "Ada Lovelace", "CCD")
	d.KeyValue("Patient", "Ada Lovelace", 10)
	d.KeyValue("Born", "10 December 1915", 10)
	d.Rule()
	d.Heading("Medications", 1)
	d.Paragraph("The patient takes metformin 500 MG twice daily.", Regular, 10, 0)
	d.Table(
		[]string{"Medication", "Status", "Started"},
		[][]string{
			{"metformin 500 MG", "active", "2024-01-04"},
			{"warfarin sodium 5 MG", "active", "2025-11-20"},
		},
		9,
	)
	d.Note("Rendered from the narrative of the source document.")

	out := d.Bytes()
	objects := checkStructure(t, out)
	checkStreamLengths(t, out)

	if objects < 8 {
		t.Errorf("only %d objects were written; a one-page document needs the catalogue, page tree, info, "+
			"four fonts, a content stream and a page", objects)
	}
}

// Content long enough to need several pages must produce several pages, with the page tree agreeing.
//
// A page tree whose /Count disagrees with its /Kids leaves readers showing the wrong number of pages, and a
// clinician who prints "page 1 of 1" and gets three sheets has no way to know whether a fourth went missing.
func TestLongContentPaginatesAndThePageTreeAgrees(t *testing.T) {
	d := New("Long")
	for i := 0; i < 200; i++ {
		d.Paragraph(fmt.Sprintf("Line %d of a long clinical narrative that runs past the bottom of a page.", i),
			Regular, 10, 0)
	}

	out := d.Bytes()
	checkStructure(t, out)
	checkStreamLengths(t, out)

	pages := bytes.Count(out, []byte("/Type /Page "))
	if pages < 3 {
		t.Fatalf("200 paragraphs produced %d pages", pages)
	}

	var count int
	i := bytes.Index(out, []byte("/Count "))
	if i < 0 {
		t.Fatal("the page tree declares no count")
	}
	if _, err := fmt.Sscanf(string(out[i:]), "/Count %d", &count); err != nil {
		t.Fatal(err)
	}
	if count != pages {
		t.Errorf("the page tree says %d pages and %d page objects were written", count, pages)
	}

	// The Kids array extracted properly rather than by slicing between two markers, because the generator writes
	// Count before Kids and my first version of this check sliced backwards - it reported a disagreement that did
	// not exist, which is the worse kind of test failure.
	kidsArray := regexp.MustCompile(`/Kids \[([^\]]*)\]`).FindStringSubmatch(string(out))
	if kidsArray == nil {
		t.Fatal("the page tree has no Kids array")
	}
	if kids := strings.Count(kidsArray[1], " 0 R"); kids != pages {
		t.Errorf("the page tree lists %d children for %d pages", kids, pages)
	}
}

// A heading must never be the last thing on a page.
//
// In a clinical document a heading alone at the foot of a page reads as a section with nothing in it, which is
// exactly the same misreading as an empty narrative: the reader concludes there is nothing to report.
func TestAHeadingIsNotLeftAloneAtTheFootOfAPage(t *testing.T) {
	// Filled to just short of a page break, then a heading, so the heading falls at the boundary.
	for fill := 40; fill < 80; fill++ {
		d := New("Boundary")
		for i := 0; i < fill; i++ {
			d.Paragraph("Filler line.", Regular, 10, 0)
		}
		before := len(d.pages)
		d.Heading("Medications", 1)
		d.Paragraph("metformin 500 MG", Regular, 10, 0)

		// If the heading started a page, its body text must be on the same one.
		if len(d.pages) > before {
			body := d.pages[len(d.pages)-1].String()
			if !strings.Contains(body, "Medications") {
				t.Fatalf("with %d filler lines the heading and its body ended up on different pages", fill)
			}
		}

		checkStructure(t, d.Bytes())
	}
}

// A single word wider than the column must be broken, not run off the paper.
//
// This happens with coded identifiers and with URLs in provenance notes, and the part that runs off the edge is the
// part that distinguishes one identifier from another.
func TestAWordWiderThanTheColumnIsBroken(t *testing.T) {
	long := strings.Repeat("2.16.840.1.113883.19.5.99999.1", 8)

	lines := wrapWidth(long, Regular, 10, contentWidth)
	if len(lines) < 2 {
		t.Fatalf("a word of %d characters was not broken; it would run off the page", len([]rune(long)))
	}
	for i, l := range lines {
		if w := textWidth(l, Regular, 10); w > contentWidth+0.01 {
			t.Errorf("line %d is %.1f points wide in a %.1f point column", i, w, contentWidth)
		}
	}
}

// Text that would break the file must be neutralised, not passed through.
//
// A patient's name containing a bracket is not exotic - it is how systems record a preferred name - and an
// unescaped one ends the text literal early and corrupts every instruction after it.
func TestTextThatWouldBreakTheFileIsEscaped(t *testing.T) {
	d := New("Escaping")
	d.KeyValue("Patient", `Ada (Lovelace) \ Byron`, 10)
	d.Paragraph("A tab\there and a control\x01character.", Regular, 10, 0)

	out := d.Bytes()
	checkStructure(t, out)
	checkStreamLengths(t, out)

	body := string(out)

	// The brackets must be escaped wherever they appear in content, never bare.
	if strings.Contains(body, "(Ada (Lovelace)") {
		t.Error("a bracket in a patient name was written unescaped, which corrupts everything after it")
	}
	if !strings.Contains(body, `\(Lovelace\)`) {
		t.Error("the brackets were not escaped")
	}
	if strings.Contains(body, "\x01") {
		t.Error("a control character was written into the file")
	}
}

// An empty document must still be a valid file.
//
// A caller with nothing to render should get an openable empty document rather than a file a reader rejects, because
// "the document would not open" and "the document was empty" send somebody looking in different places.
func TestAnEmptyDocumentIsStillValid(t *testing.T) {
	out := New("Nothing").Bytes()
	checkStructure(t, out)
	checkStreamLengths(t, out)

	if pages := bytes.Count(out, []byte("/Type /Page ")); pages != 1 {
		t.Errorf("an empty document produced %d pages, want 1", pages)
	}
}

// Column widths must fit the page even when the content wants more.
//
// A table whose last column falls beyond the paper edge loses that column silently, and in a medication table the
// last column is often the dose.
func TestATableTooWideForThePageIsScaledRatherThanClipped(t *testing.T) {
	d := New("Wide")
	d.Table(
		[]string{"Medication", "Instructions", "Prescriber", "Started", "Stopped"},
		[][]string{{
			strings.Repeat("metformin hydrochloride extended release ", 3),
			strings.Repeat("take one tablet twice daily with food ", 3),
			"Dr Grace Hopper, Department of Internal Medicine",
			"2024-01-04", "2025-11-20",
		}},
		9,
	)

	out := d.Bytes()
	checkStructure(t, out)
	checkStreamLengths(t, out)

	// Every Td x-coordinate must be inside the printable area.
	for _, m := range regexp.MustCompile(`([\d.]+) [\d.]+ Td`).FindAllStringSubmatch(string(out), -1) {
		x, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if x > pageWidth-marginRight {
			t.Errorf("text is placed at x=%.1f, past the right margin at %.1f", x, pageWidth-marginRight)
		}
	}
}
