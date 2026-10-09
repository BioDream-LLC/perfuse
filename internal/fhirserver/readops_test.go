package fhirserver

import (
	"context"

	"github.com/biodream-llc/perfuse/internal/fhir"
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

// POST [type]/_search is a search: a read-only token may make it. Inferno's US Core suite repeats every search this way.
func TestAReadOnlyTokenMaySearchByPost(t *testing.T) {
	srv, _ := payerFixture(t)
	srv.Auth = fixedCaller{Caller{Name: "app", Scopes: []string{"system/*.rs"}, Write: false}}
	req := httptest.NewRequest("POST", "/Patient/_search", strings.NewReader("_count=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST _search with a read token: %d %s", rec.Code, rec.Body)
	}
}

// The fhirUser scope lets an app read the signed-in person's own resource, whatever resource scopes it holds - and nothing else.
func TestTheFHIRUserScopeReadsTheUsersOwnResourceOnly(t *testing.T) {
	srv, h0 := newTestServer(t)
	_ = h0
	for _, id := range []string{"me", "other"} {
		p := &fhir.Patient{Name: []fhir.HumanName{{Family: id}}}
		p.SetResourceID(id)
		if _, err := srv.Store.Put(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	srv.Auth = fixedCaller{Caller{Name: "app", Patient: "me", FHIRUser: "Patient/me",
		Scopes: []string{"fhirUser", "patient/Observation.rs?category=laboratory"}}}
	h := srv.Handler()
	for path, want := range map[string]int{"/Patient/me": 200, "/Patient/other": 403, "/Patient?_id=me": 403} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s: %d, want %d", path, rec.Code, want)
		}
	}
}
