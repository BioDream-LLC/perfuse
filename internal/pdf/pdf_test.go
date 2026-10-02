package pdf

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A PDF that is subtly malformed opens in one reader and not another, and the site that finds out is the one
// whose ward printer is the one that fails. So these tests check the structure by hand, and where a real PDF
// tool is available on the machine they check it against that too.

func fixed() Options {
	// A fixed timestamp so output can be compared byte for byte. Without it the only assertion possible is
	// "something was produced".
	return Options{Title: "Report", Created: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)}
}

func TestOutputIsAPDF(t *testing.T) {
	body, err := Render("hello", fixed())
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasPrefix(body, []byte("%PDF-1.4")) {
		t.Errorf("output does not start with a PDF header: %q", body[:min(16, len(body))])
	}
	if !bytes.HasSuffix(bytes.TrimSpace(body), []byte("%%EOF")) {
		t.Error("output does not end with the end-of-file marker")
	}
}

func TestTheBinaryMarkerIsPresent(t *testing.T) {
	// Without it a naive FTP client in ASCII mode corrupts the file, and an FTP destination is exactly where
	// these go.
	body, err := Render("hello", fixed())
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(body[:32], []byte{0xE2, 0xE3, 0xCF, 0xD3}) {
		t.Error("the binary marker comment is missing, so a transfer in text mode would corrupt the file")
	}
}

// TestCrossReferenceOffsetsAreCorrect is the test that matters most.
//
// The offsets are the only fiddly part of writing a PDF by hand, and a wrong one produces a file that some
// readers repair silently and others refuse. Silent repair is worse: it works everywhere in testing and fails on
// the one machine that matters.
func TestCrossReferenceOffsetsAreCorrect(t *testing.T) {
	body, err := Render("one\ntwo\nthree", fixed())
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)

	start := strings.Index(text, "xref\n")
	if start < 0 {
		t.Fatal("no cross-reference table")
	}

	// startxref must point at the xref table.
	tail := text[strings.LastIndex(text, "startxref"):]
	var declared int
	if _, err := fmt.Sscanf(tail, "startxref\n%d", &declared); err != nil {
		t.Fatalf("could not read startxref: %v", err)
	}
	if declared != start {
		t.Errorf("startxref = %d, but the xref table is at %d", declared, start)
	}

	// Every offset in the table must land on "N 0 obj".
	entry := regexp.MustCompile(`(?m)^(\d{10}) 00000 n $`)
	matches := entry.FindAllStringSubmatch(text[start:], -1)
	if len(matches) == 0 {
		t.Fatal("the cross-reference table has no entries")
	}

	for i, m := range matches {
		offset, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		if offset <= 0 || offset >= len(text) {
			t.Errorf("object %d has offset %d, which is outside the file", i+1, offset)
			continue
		}
		want := fmt.Sprintf("%d 0 obj", i+1)
		if !strings.HasPrefix(text[offset:], want) {
			got := text[offset:min(offset+20, len(text))]
			t.Errorf("object %d: offset %d points at %q, want %q", i+1, offset, got, want)
		}
	}
}

func TestLongTextPaginates(t *testing.T) {
	// A report that runs to several pages is the normal case, and a single page with the rest silently dropped
	// would be the worst possible bug here - the recipient has no way to know anything is missing.
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}

	body, err := Render(b.String(), fixed())
	if err != nil {
		t.Fatal(err)
	}

	pages := bytes.Count(body, []byte("/Type /Page /Parent"))
	if pages < 2 {
		t.Errorf("400 lines produced %d page(s)", pages)
	}

	// The last line must actually be present, not just enough pages.
	if !bytes.Contains(body, []byte("line 399")) {
		t.Error("the last line is missing, so content was dropped rather than paginated")
	}
	// And the count in the page tree must match the pages written.
	if !bytes.Contains(body, []byte(fmt.Sprintf("/Count %d", pages))) {
		t.Errorf("the page tree count does not match the %d pages written", pages)
	}
}

func TestParenthesesAndBackslashesAreEscaped(t *testing.T) {
	// These end a string literal early, and a report full of "(see note)" is exactly the input that finds it.
	body, err := Render(`Result (abnormal) \ pending`, fixed())
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(body, []byte(`\(abnormal\)`)) {
		t.Error("parentheses were not escaped, so the content stream is malformed")
	}
	if !bytes.Contains(body, []byte(`\\`)) {
		t.Error("the backslash was not escaped")
	}
}

func TestNonAsciiIsEscapedRatherThanEmitted(t *testing.T) {
	// Keeps the file valid without needing an embedded font. A patient name with an accent must not break the
	// document.
	body, err := Render("Ångström", fixed())
	if err != nil {
		t.Fatal(err)
	}

	// The high bytes must not appear raw inside the content stream.
	if bytes.Contains(body, []byte("Ångström")) {
		t.Error("non-ASCII was written raw into the content stream")
	}
	if !bytes.Contains(body, []byte(`\`)) {
		t.Error("no escape sequence was produced")
	}
}

func TestLongLinesWrapRatherThanClip(t *testing.T) {
	// A clipped line silently loses the end of a value, and a report is often the only record of it.
	long := strings.Repeat("A", 300)

	body, err := Render(long, fixed())
	if err != nil {
		t.Fatal(err)
	}

	// Several Tj operators means several lines, so it wrapped.
	if n := bytes.Count(body, []byte(" Tj")); n < 2 {
		t.Errorf("a 300-character line produced %d drawn line(s); it was clipped rather than wrapped", n)
	}
}

func TestExistingLineBreaksArePreserved(t *testing.T) {
	// The input is a report somebody laid out. Reflowing it into a paragraph would destroy the columns that make
	// it readable.
	body, err := Render("PATIENT   RESULT\nFROST     10.2\nHALE      9.8", fixed())
	if err != nil {
		t.Fatal(err)
	}

	if n := bytes.Count(body, []byte(" Tj")); n != 3 {
		t.Errorf("three lines produced %d drawn line(s)", n)
	}
}

func TestCarriageReturnsAreHandled(t *testing.T) {
	// This text may have come straight out of an HL7 message, where segments are separated by a bare carriage
	// return.
	body, err := Render("first\rsecond\rthird", fixed())
	if err != nil {
		t.Fatal(err)
	}

	if n := bytes.Count(body, []byte(" Tj")); n != 3 {
		t.Errorf("three carriage-return-separated lines produced %d drawn line(s)", n)
	}
}

func TestTabsAreExpanded(t *testing.T) {
	// A PDF has no tab stops, so a literal tab renders as nothing at all - the columns would silently collapse.
	body, err := Render("A\tB", fixed())
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(body, []byte("A\tB")) {
		t.Error("the tab was written literally and will render as nothing")
	}
	if !bytes.Contains(body, []byte("A       B")) {
		t.Error("the tab was not expanded to the next stop")
	}
}

func TestEmptyInputStillProducesAPage(t *testing.T) {
	// A report with no rows today is a real outcome, and the recipient needs the page to know the job ran.
	body, err := Render("", fixed())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("/Type /Page /Parent")) {
		t.Error("an empty report produced no page")
	}
}

func TestOutputIsReproducible(t *testing.T) {
	// Two runs over the same input must be identical, or the file cannot be compared and a change cannot be
	// reviewed.
	first, err := Render("hello\nworld", fixed())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		again, err := Render("hello\nworld", fixed())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatal("output changed between runs on identical input")
		}
	}
}

func TestAnAbsurdFontSizeIsRefused(t *testing.T) {
	// Rather than looping forever producing pages with no lines on them.
	if _, err := Render("hello", Options{FontSize: 5000}); err == nil {
		t.Fatal("a font size too large for the page was accepted")
	}
}

func TestLandscapeSwapsThePage(t *testing.T) {
	body, err := Render("hello", Options{Landscape: true, Created: fixed().Created})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("841.89 595.28")) {
		t.Error("landscape did not swap the page dimensions")
	}
}

// TestARealPDFToolAcceptsIt checks the file against something that was not written here.
//
// Skipped when no tool is available, which is most machines. Where one is, it is worth far more than every
// structural assertion above: those check what I believed the format to be, and this checks what it actually is.
func TestARealPDFToolAcceptsIt(t *testing.T) {
	var name string
	for _, candidate := range []string{"qpdf", "pdfinfo", "mutool"} {
		if path, err := exec.LookPath(candidate); err == nil {
			name = path
			break
		}
	}
	if name == "" {
		t.Skip("no PDF tool available on this machine")
	}

	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line %d with (parentheses) and a backslash \\ in it\n", i)
	}

	body, err := Render(b.String(), fixed())
	if err != nil {
		t.Fatal(err)
	}

	path := t.TempDir() + "/out.pdf"
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	var cmd *exec.Cmd
	switch {
	case strings.HasSuffix(name, "qpdf"):
		cmd = exec.Command(name, "--check", path)
	case strings.HasSuffix(name, "pdfinfo"):
		cmd = exec.Command(name, path)
	default:
		cmd = exec.Command(name, "info", path)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s rejected the file: %v\n%s", name, err, out)
	}
	t.Logf("%s accepted it:\n%s", name, out)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
