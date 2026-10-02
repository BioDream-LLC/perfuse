package api

import (
	"net/http"
	"strings"
	"testing"
)

// A rendered clinical document goes onto a desktop, into a fax queue, and into a file.
//
// So the response has to be a real PDF with headers that make a browser save it rather than cache it, and a filename
// somebody can still make sense of six months later.

func TestRenderingADocumentReturnsAPDFWithSensibleHeaders(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/document/pdf", map[string]any{"document": conformantCDA})
	if res.Code != http.StatusOK {
		t.Fatalf("rendering returned %d: %s", res.Code, res.Body.String())
	}

	body := res.Body.Bytes()
	if !strings.HasPrefix(string(body), "%PDF") {
		t.Fatalf("the response is not a PDF, it begins %q", body[:min3(24, len(body))])
	}
	if len(body) < 500 {
		t.Errorf("the PDF is only %d bytes, which is too small to contain a document", len(body))
	}

	if got := res.Header().Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type is %q; a browser will not treat that as a PDF", got)
	}

	// attachment, so it is a deliberate save rather than something that opens in a tab and stays in the cache.
	disp := res.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disp, "attachment;") {
		t.Errorf("Content-Disposition is %q, want an attachment", disp)
	}

	// The filename has to mean something in a folder later. A folder of document-1.pdf through document-40.pdf is
	// a folder nobody can use.
	if !strings.Contains(strings.ToLower(disp), "lovelace") {
		t.Errorf("the filename does not identify the patient: %q", disp)
	}

	// Nothing about a patient belongs in an intermediate cache.
	if got := res.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control is %q; a rendered clinical document must not be cached", got)
	}
}

// A name that could rewrite the response header must not reach it.
//
// A newline in a filename ends the header and lets everything after it be interpreted as a new one. Patient names
// come from other people's systems and are not trustworthy input.
func TestAPatientNameCannotRewriteTheResponseHeader(t *testing.T) {
	hostile := strings.Replace(conformantCDA,
		"<given>Ada</given><family>Lovelace</family>",
		"<given>Ada</given><family>Lovelace\"\r\nX-Injected: yes</family>", 1)

	h := newHarness(t)
	res := h.do("viewer", http.MethodPost, "/api/document/pdf", map[string]any{"document": hostile})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", res.Code, res.Body.String())
	}

	if res.Header().Get("X-Injected") != "" {
		t.Fatal("a patient name injected a response header")
	}

	// The filename itself, with the surrounding quotes removed. My first version of this trimmed only the leading
	// quote and then reported the closing one as an injected character, which is a test failing on its own
	// arithmetic rather than on the code.
	disp := res.Header().Get("Content-Disposition")
	inner := strings.TrimSuffix(strings.TrimPrefix(disp, `attachment; filename="`), `"`)

	for _, bad := range []string{"\r", "\n", `"`, ";"} {
		if strings.Contains(inner, bad) {
			t.Errorf("the filename contains %q, which can rewrite the header: %q", bad, disp)
		}
	}
}

// A document that cannot be parsed is the caller's mistake, and the message should name the reason.
func TestRenderingRefusesSomethingThatIsNotADocument(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/document/pdf", map[string]any{"document": "not xml at all"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if strings.HasPrefix(res.Body.String(), "%PDF") {
		t.Error("a PDF was returned for something that is not a document")
	}
}

// An empty request must be refused rather than producing an empty PDF.
func TestRenderingRefusesAnEmptyRequest(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/document/pdf", map[string]any{})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
}

// The filename builder must not produce something a filesystem or a shell will choke on.
func TestFilenamePartsAreSafe(t *testing.T) {
	cases := map[string]string{
		"Ada Lovelace":           "Ada-Lovelace",
		"  Ada   Lovelace  ":     "Ada-Lovelace",
		"Ada/../../etc/passwd":   "Adaetcpasswd",
		"Ada\r\nX-Injected: yes": "AdaX-Injected-yes",
		"O'Brien-Smith":          "OBrien-Smith",
		"../..":                  "",
		"!!!":                    "",
		"Continuity of Care Doc": "Continuity-of-Care-Doc",
	}

	for in, want := range cases {
		if got := safeFilenamePart(in); got != want {
			t.Errorf("safeFilenamePart(%q) = %q, want %q", in, got, want)
		}
	}
}

// A very long name must be truncated without leaving a trailing separator.
func TestALongNameIsTruncatedCleanly(t *testing.T) {
	got := safeFilenamePart(strings.Repeat("Lovelace ", 20))
	if len([]rune(got)) > 48 {
		t.Errorf("the name is %d characters, which some systems will truncate again and collide", len([]rune(got)))
	}
	if strings.HasSuffix(got, "-") || strings.HasPrefix(got, "-") {
		t.Errorf("the truncated name has a dangling separator: %q", got)
	}
}

func min3(a, b int) int {
	if a < b {
		return a
	}
	return b
}
