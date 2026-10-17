package fhirserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
