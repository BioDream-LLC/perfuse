package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// POST [base] is a transaction: the base without a trailing slash reaches the FHIR handler at "/", not a redirect.
func TestTheFHIRBaseItselfIsServed(t *testing.T) {
	var got []string
	mux := http.NewServeMux()
	mountFHIR(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = append(got, r.Method+" "+r.URL.Path) }))
	for _, c := range []struct{ method, path string }{{"POST", "/fhir"}, {"POST", "/fhir/"}, {"GET", "/fhir/Patient/1"}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path+"?x=1", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	want := []string{"POST /", "POST /", "GET /Patient/1"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("the FHIR handler saw %v, want %v", got, want)
	}
}
