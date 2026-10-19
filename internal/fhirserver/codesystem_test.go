package fhirserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/terminology"
)

type fakeCodes map[string]struct {
	display string
	active  bool
}

func (f fakeCodes) LookupCode(_ context.Context, system, _, code string) (string, string, bool, bool, error) {
	c, ok := f[system+"|"+code]
	return c.display, "v1", c.active, ok, nil
}

func (f fakeCodes) HasSystem(_ context.Context, system string) bool {
	return system == "http://loinc.org"
}

func TestCodeSystemLookupAndValidate(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.Codes = fakeCodes{"http://loinc.org|2345-7": {"Glucose", true}, "http://loinc.org|1-8": {"Old", false}}
	h := srv.Handler()
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code, strings.Join(strings.Fields(rec.Body.String()), "")
	}
	if code, body := get("/CodeSystem/$lookup?system=http://loinc.org&code=2345-7"); code != 200 || !strings.Contains(body, `"valueString":"Glucose"`) {
		t.Errorf("lookup %d %s", code, body)
	}
	if code, _ := get("/CodeSystem/$lookup?system=http://loinc.org&code=0000-0"); code != 404 {
		t.Errorf("missing code %d", code)
	}
	if code, _ := get("/CodeSystem/$lookup?system=http://snomed.info/sct&code=1"); code != 404 {
		t.Errorf("system not imported %d", code)
	}
	for q, want := range map[string]string{
		"system=http://loinc.org&code=2345-7":                 `"name":"result","valueBoolean":true`,
		"coding=http://loinc.org|2345-7&display=glucose":      `"name":"result","valueBoolean":true`,
		"system=http://loinc.org&code=2345-7&display=Insulin": `"valueBoolean":false`,
		"system=http://loinc.org&code=1-8":                    "nolongeractive",
		"url=http://snomed.info/sct&code=1":                   "cannotbechecked",
	} {
		if code, body := get("/CodeSystem/$validate-code?" + q); code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s", q, code, body)
		}
	}
	srv.Codes = nil
	if code, _ := get("/CodeSystem/$lookup?system=http://loinc.org&code=2345-7"); code != 404 {
		t.Errorf("without code systems %d", code)
	}
}

type designatedCodes struct{ fakeCodes }

func (designatedCodes) Designations(_ context.Context, system, version, code string) ([]terminology.Designation, error) {
	return []terminology.Designation{
		{Language: "en", Use: "fsn", Value: "Anemia (disorder)"},
		{Language: "sv", Use: "preferred", Value: "anemi", Preferred: true},
		{Language: "en-GB", Use: "preferred", Value: "Anaemia", Preferred: true},
	}, nil
}

// A code with names in other languages answers $lookup with them as designations, and displayLanguage picks one.
func TestLookupGivesDesignationsAndDisplayLanguage(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.Codes = designatedCodes{fakeCodes{"http://loinc.org|271737000": {"Anemia", true}}}
	h := srv.Handler()
	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return strings.Join(strings.Fields(rec.Body.String()), "")
	}
	body := get("/CodeSystem/$lookup?system=http://loinc.org&code=271737000&displayLanguage=sv-SE")
	if !strings.Contains(body, `"name":"display","valueString":"anemi"`) || !strings.Contains(body, `"name":"designation"`) ||
		!strings.Contains(body, `"code":"900000000000003001"`) {
		t.Fatal(body)
	}
	if body := get("/CodeSystem/$lookup?system=http://loinc.org&code=271737000&displayLanguage=en-GB"); !strings.Contains(body, `"name":"display","valueString":"Anaemia"`) {
		t.Fatal(body)
	}
	if body := get("/CodeSystem/$lookup?system=http://loinc.org&code=271737000"); !strings.Contains(body, `"name":"display","valueString":"Anemia"`) {
		t.Fatal(body)
	}
}
