package fhirserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const obsCat = "http://terminology.hl7.org/CodeSystem/observation-category"

func putObservation(t *testing.T, h http.Handler, id, category string) {
	t.Helper()
	body := `{"resourceType":"Observation","id":"` + id + `","status":"final",
	  "category":[{"coding":[{"system":"` + obsCat + `","code":"` + category + `"}]}],
	  "code":{"text":"x"},"subject":{"reference":"Patient/p1"}}`
	req := httptest.NewRequest(http.MethodPut, "/Observation/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		t.Fatalf("PUT %s: %d %s", id, rec.Code, rec.Body)
	}
}

func scopedGet(t *testing.T, srv *Server, scopes []string, path string) (int, map[string]any) {
	t.Helper()
	srv.Auth = fixedCaller{Caller{Name: "app", Scopes: scopes}}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func bundleIDs(b map[string]any) []string {
	var ids []string
	entries, _ := b["entry"].([]any)
	for _, e := range entries {
		r, _ := e.(map[string]any)["resource"].(map[string]any)
		ids = append(ids, r["id"].(string))
	}
	return ids
}

func TestAGranularScopeReadsOnlyWhatItsFilterFinds(t *testing.T) {
	srv, h := newTestServer(t)
	putObservation(t, h, "lab1", "laboratory")
	putObservation(t, h, "vs1", "vital-signs")
	lab := []string{"patient/Observation.rs?category=" + obsCat + "|laboratory"}

	code, b := scopedGet(t, srv, lab, "/Observation")
	if ids := bundleIDs(b); code != 200 || len(ids) != 1 || ids[0] != "lab1" {
		t.Fatalf("search: %d %v, want only lab1", code, ids)
	}
	if b["total"] != float64(1) {
		t.Errorf("total = %v, want 1: the count must not reveal what the scope hides", b["total"])
	}
	if _, b := scopedGet(t, srv, lab, "/Observation?category=vital-signs"); len(bundleIDs(b)) != 0 {
		t.Errorf("asking for vital signs by name found %v", bundleIDs(b))
	}
	if code, _ := scopedGet(t, srv, lab, "/Observation/vs1"); code != http.StatusNotFound {
		t.Errorf("read of an out-of-scope Observation: %d, want 404", code)
	}
	if code, _ := scopedGet(t, srv, lab, "/Observation/lab1"); code != 200 {
		t.Errorf("read of an in-scope Observation: %d", code)
	}

	// Two filters are either-or; an unfiltered read of the type lifts them.
	both := append(lab, "patient/Observation.rs?category=vital-signs")
	if _, b := scopedGet(t, srv, both, "/Observation"); len(bundleIDs(b)) != 2 {
		t.Errorf("two filters: %v, want both", bundleIDs(b))
	}
	if _, b := scopedGet(t, srv, append(lab, "patient/Observation.rs"), "/Observation"); len(bundleIDs(b)) != 2 {
		t.Errorf("an unfiltered scope beside a filtered one should read everything: %v", bundleIDs(b))
	}
	// A filter this cannot evaluate refuses rather than allows.
	if _, b := scopedGet(t, srv, []string{"patient/Observation.rs?date=ge2020"}, "/Observation"); len(bundleIDs(b)) != 0 {
		t.Errorf("an unevaluable filter found %v", bundleIDs(b))
	}
}

// An include is a read by another route: it may not return what the caller could not read directly.
func TestIncludesAreLimitedLikeReads(t *testing.T) {
	srv, h := newTestServer(t)
	putObservation(t, h, "lab1", "laboratory")
	req := httptest.NewRequest(http.MethodPut, "/Patient/p1", strings.NewReader(`{"resourceType":"Patient","id":"p1"}`))
	req.Header.Set("Content-Type", "application/fhir+json")
	h.ServeHTTP(httptest.NewRecorder(), req)

	_, b := scopedGet(t, srv, []string{"patient/Observation.rs"}, "/Observation?_include=Observation:subject")
	if ids := bundleIDs(b); len(ids) != 1 {
		t.Errorf("a token without Patient read got the included Patient: %v", ids)
	}
	_, b = scopedGet(t, srv, []string{"patient/Observation.rs", "patient/Patient.rs"}, "/Observation?_include=Observation:subject")
	if ids := bundleIDs(b); len(ids) != 2 {
		t.Errorf("with Patient read the include should be there: %v", ids)
	}
}

// A token limited to a patient still reads its user's own Practitioner record, and nothing else outside the patient.
func TestAPatientLimitedTokenReadsItsOwnUser(t *testing.T) {
	srv, h := newTestServer(t)
	for _, body := range []string{`{"resourceType":"Practitioner","id":"d1"}`, `{"resourceType":"Practitioner","id":"d2"}`} {
		id := body[strings.Index(body, `"id":"`)+6 : strings.LastIndex(body, `"`)]
		req := httptest.NewRequest(http.MethodPut, "/Practitioner/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/fhir+json")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	read := func(path string) int {
		srv.Auth = fixedCaller{Caller{Name: "app", Scopes: []string{"user/*.rs"}, Patient: "p1", FHIRUser: "Practitioner/d1"}}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if c := read("/Practitioner/d1"); c != 200 {
		t.Errorf("own Practitioner: %d", c)
	}
	if c := read("/Practitioner/d2"); c != 404 {
		t.Errorf("another Practitioner under a patient-limited token: %d", c)
	}
}
