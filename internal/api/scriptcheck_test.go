package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func checkScript(t *testing.T, h *harness, role string, body any) (int, scriptCheckResponse) {
	t.Helper()
	res := h.do(role, http.MethodPost, "/api/scripts/check", body)

	var out scriptCheckResponse
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatalf("decoding the response: %v\nbody: %s", err, res.Body.String())
		}
	}
	return res.Code, out
}

func TestAValidScriptChecksClean(t *testing.T) {
	h := newHarness(t)

	code, out := checkScript(t, h, "editor", scriptCheckRequest{Kind: "transformer", Source: "var x = 1; logger.info('hello');"})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !out.OK {
		t.Errorf("a valid script reported not ok: %+v", out.Error)
	}
	if out.Error != nil {
		t.Errorf("unexpected error: %+v", out.Error)
	}
	if out.Kind != "transformer" {
		t.Errorf("kind = %q", out.Kind)
	}
}

func TestASyntaxErrorReportsItsLine(t *testing.T) {
	// A marker on the right line is the difference between an editor and a textarea with
	// a red box under it, so the position is separated from the message rather than left
	// inside it.
	h := newHarness(t)

	src := "var a = 1;\nvar b = ;\nvar c = 3;"
	code, out := checkScript(t, h, "editor", scriptCheckRequest{Kind: "transformer", Source: src})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.OK {
		t.Fatal("a script with a syntax error reported ok")
	}
	if out.Error == nil {
		t.Fatal("no error was returned")
	}
	if out.Error.Line != 2 {
		t.Errorf("error line = %d, want 2 (%q)", out.Error.Line, out.Error.Message)
	}
	if out.Error.Message == "" {
		t.Error("the error has no message")
	}
}

func TestAnEmptyScriptIsValid(t *testing.T) {
	// An empty script means "no script". Reporting a syntax error would make the pane
	// look broken while somebody is still typing the first line.
	h := newHarness(t)

	for _, src := range []string{"", "   ", "\n\t"} {
		code, out := checkScript(t, h, "editor", scriptCheckRequest{Kind: "transformer", Source: src})
		if code != http.StatusOK {
			t.Errorf("source %q: status = %d", src, code)
			continue
		}
		if !out.OK {
			t.Errorf("source %q reported not ok: %+v", src, out.Error)
		}
	}
}

func TestE4XIsTranslatedAndReported(t *testing.T) {
	// A script that was silently rewritten and then behaved differently is the hardest
	// kind of migration problem to diagnose. Showing the rewrite turns it into a
	// five-second comparison.
	h := newHarness(t)

	// for-each is E4X and has to be rewritten to run on goja.
	src := `for each (var seg in msg['PID']) { logger.info(seg); }`
	code, out := checkScript(t, h, "editor", scriptCheckRequest{Kind: "transformer", Source: src})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !out.Rewritten {
		t.Error("E4X was not reported as rewritten")
	}
	if out.Translated == "" {
		t.Error("the translated source was not returned")
	}
	if strings.Contains(out.Translated, "for each") {
		t.Errorf("the translation still contains E4X: %s", out.Translated)
	}
	if len(out.Notes) == 0 {
		t.Error("no notes were returned for a translated construct")
	}
}

func TestPlainJavaScriptIsNotReportedAsRewritten(t *testing.T) {
	// Claiming a rewrite that did not happen would send somebody looking for a
	// difference that is not there.
	h := newHarness(t)

	code, out := checkScript(t, h, "editor", scriptCheckRequest{Kind: "transformer", Source: "var x = msg;"})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.Rewritten {
		t.Error("plain JavaScript was reported as rewritten")
	}
	if len(out.Notes) != 0 {
		t.Errorf("plain JavaScript produced %d notes", len(out.Notes))
	}
}

func TestABareReturnIsLegalInBothKinds(t *testing.T) {
	// Every Mirth filter is written as a bare return, and both kinds are wrapped in a
	// function, so both accept it.
	//
	// I had originally asserted the two kinds compile differently. They do not - checked
	// live against a running server - and the endpoint's own wording claimed otherwise in
	// three places. The kind matters at run time, where the wrapper decides what to do
	// with the result, not at compile time.
	h := newHarness(t)

	for _, kind := range []string{"filter", "transformer"} {
		_, out := checkScript(t, h, "editor",
			scriptCheckRequest{Kind: kind, Source: "return msg['PID'] != null;"})
		if !out.OK {
			t.Errorf("%s: a bare return was refused: %+v", kind, out.Error)
		}
		if out.Kind != kind {
			t.Errorf("kind = %q, want %q", out.Kind, kind)
		}
	}

	// And neither requires one.
	for _, kind := range []string{"filter", "transformer"} {
		_, out := checkScript(t, h, "editor", scriptCheckRequest{Kind: kind, Source: "var x = 1;"})
		if !out.OK {
			t.Errorf("%s: a script with no return was refused: %+v", kind, out.Error)
		}
	}
}

func TestAnUnknownKindIsRefused(t *testing.T) {
	h := newHarness(t)
	res := h.do("editor", http.MethodPost, "/api/scripts/check", scriptCheckRequest{Kind: "wibble", Source: "var x=1;"})
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "transformer") {
		t.Errorf("the error should name the valid kinds: %s", res.Body.String())
	}
}

func TestAnAbsentKindDefaultsToTransformer(t *testing.T) {
	// The transformer is what a Mirth migration is mostly made of, and it is the safer
	// default: its wrapper accepts anything a filter's does.
	h := newHarness(t)

	code, out := checkScript(t, h, "editor", scriptCheckRequest{Source: "var x = 1;"})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.Kind != "transformer" {
		t.Errorf("kind = %q, want transformer", out.Kind)
	}
}

func TestAnOversizeScriptIsRefused(t *testing.T) {
	// The limit exists so a parser cannot be handed a hundred megabytes of nested
	// brackets to think about.
	h := newHarness(t)

	big := strings.Repeat("a", maxScriptCheckBytes+1)
	res := h.do("editor", http.MethodPost, "/api/scripts/check", scriptCheckRequest{Kind: "transformer", Source: big})
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", res.Code)
	}
}

func TestAViewerCannotCheckScripts(t *testing.T) {
	// It accepts arbitrary JavaScript to parse, and only somebody who can write a script
	// needs to check one.
	h := newHarness(t)
	res := h.do("viewer", http.MethodPost, "/api/scripts/check", scriptCheckRequest{Kind: "transformer", Source: "var x=1;"})
	if res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestDiagnoseScriptError(t *testing.T) {
	cases := []struct {
		in       string
		wantLine int
		wantCol  int
	}{
		{"editor: Line 3:11 Unexpected token", 3, 11},
		{"editor: line 1:0 something", 1, 0},
		{"no position here", 0, 0},
		{"", 0, 0},
	}
	for _, c := range cases {
		got := diagnoseScriptError(c.in)
		if got.Line != c.wantLine || got.Column != c.wantCol {
			t.Errorf("diagnoseScriptError(%q) = line %d col %d, want line %d col %d",
				c.in, got.Line, got.Column, c.wantLine, c.wantCol)
		}
	}
}

func TestTheDictionaryStillDefaultsToNames(t *testing.T) {
	// Every existing caller wants the names. The full document is a separate mode so
	// none of them starts receiving fifty times as much data.
	h := newHarness(t)
	res := h.do("viewer", http.MethodGet, "/api/dictionary", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}

	var out struct {
		Segments []string `json:"segments"`
		All      []any    `json:"all"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Segments) == 0 {
		t.Error("no segment names were returned")
	}
	if len(out.All) != 0 {
		t.Error("the default response carried the full dictionary")
	}
}

func TestTheWholeDictionaryComesInOneRequest(t *testing.T) {
	// A completion menu cannot make a request per keystroke: an answer that arrives after
	// the next character is worse than no answer.
	h := newHarness(t)
	res := h.do("viewer", http.MethodGet, "/api/dictionary?all=1", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}

	var out struct {
		All []struct {
			Segment     string `json:"segment"`
			Description string `json:"description"`
			Fields      []struct {
				Number      int      `json:"number"`
				Name        string   `json:"name"`
				Components  []string `json:"components"`
				Table       string   `json:"table"`
				Repeats     bool     `json:"repeats"`
				Description string   `json:"description"`
			} `json:"fields"`
		} `json:"all"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.All) < 5 {
		t.Fatalf("the dictionary returned %d segments", len(out.All))
	}

	// PID is the one an editor completes against most, so it is worth asserting rather
	// than trusting the count.
	var pid *struct {
		Segment     string `json:"segment"`
		Description string `json:"description"`
		Fields      []struct {
			Number      int      `json:"number"`
			Name        string   `json:"name"`
			Components  []string `json:"components"`
			Table       string   `json:"table"`
			Repeats     bool     `json:"repeats"`
			Description string   `json:"description"`
		} `json:"fields"`
	}
	for i := range out.All {
		if out.All[i].Segment == "PID" {
			pid = &out.All[i]
			break
		}
	}
	if pid == nil {
		t.Fatal("PID is not in the dictionary")
	}
	if len(pid.Fields) == 0 {
		t.Fatal("PID has no fields")
	}

	// Fields must arrive in order, because a completion menu shows them in the order it
	// is given and field 10 appearing between 1 and 2 is worse than no ordering at all.
	last := 0
	for _, f := range pid.Fields {
		if f.Number <= last {
			t.Errorf("PID fields are out of order: %d came after %d", f.Number, last)
		}
		last = f.Number
		if f.Name == "" {
			t.Errorf("PID-%d has no name", f.Number)
		}
	}

	// The two fields a keystroke apart that make this feature worth having.
	names := map[int]string{}
	for _, f := range pid.Fields {
		names[f.Number] = f.Name
	}
	if !strings.Contains(strings.ToLower(names[7]), "birth") {
		t.Errorf("PID-7 = %q, expected the date of birth", names[7])
	}
	if names[8] == "" {
		t.Error("PID-8 has no name")
	}
}
