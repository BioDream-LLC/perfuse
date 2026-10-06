package fhirserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixedCaller struct{ c Caller }

func (f fixedCaller) Authenticate(*http.Request) (*Caller, error) { c := f.c; return &c, nil }
func (fixedCaller) Describe() string                              { return "test" }

// A read-only token (a DTR client holding system/*.rs) may call the operations that compute an answer, and still may not write.
func TestAReadOnlyTokenMayCallReadOperationsButNotWrite(t *testing.T) {
	srv, _ := payerFixture(t)
	srv.Auth = fixedCaller{Caller{Name: "dtr-client", Scopes: []string{"system/*.rs"}, Write: false}}
	h := srv.Handler()
	for path, wantRefused := range map[string]bool{
		"/Questionnaire/$questionnaire-package": false,
		"/Questionnaire/$next-question":         false,
		"/ValueSet/$expand":                     false,
		"/Questionnaire":                        true,
	} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"resourceType":"Parameters"}`))
		req.Header.Set("Content-Type", "application/fhir+json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if refused := rec.Code == http.StatusForbidden; refused != wantRefused {
			t.Errorf("POST %s: status %d, want refused=%v: %s", path, rec.Code, wantRefused, rec.Body)
		}
	}
}
