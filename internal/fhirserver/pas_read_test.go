package fhirserver

import (
	"encoding/json"
	"net/http"
	"testing"
)

// patientOnlyAuth is a SMART patient launch: every scope, but for one patient.
type patientOnlyAuth struct{}

func (patientOnlyAuth) Authenticate(*http.Request) (*Caller, error) {
	return &Caller{Patient: "p1", AllScopes: true}, nil
}
func (patientOnlyAuth) Describe() string { return "test patient token" }

func pasGet(t *testing.T, h http.Handler, path string) (int, map[string]any, string) {
	t.Helper()
	rec := payerDo(t, h, "GET", path, "", nil)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header().Get("ETag")
}

// An id-only PAS notification names Bundle/<id>; the provider must be able to fetch it, and each version as it was sent.
func TestAPASAnswerCanBeReadByTheIdItsNotificationNames(t *testing.T) {
	f := newPASFixture(t)
	_, submitted := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("R1", "2"))
	id := str(claimResponseOf(t, submitted)["id"])

	code, b, etag := pasGet(t, f.h, "/Bundle/"+id)
	if code != http.StatusOK || actionCodes(claimResponseOf(t, b)) != "A4" || etag != `W/"1"` {
		t.Fatalf("pended read: %d %s %v", code, etag, b)
	}
	pasPost(t, f.h, "/Claim/$decide", `{"resourceType":"Parameters","parameter":[{"name":"claimResponse","valueString":"`+id+
		`"},{"name":"decision","valueCode":"approve"}]}`)

	if code, b, etag = pasGet(t, f.h, "/Bundle/"+id); code != http.StatusOK || actionCodes(claimResponseOf(t, b)) != "A1" || etag != `W/"2"` {
		t.Fatalf("decided read: %d %s %v", code, etag, b)
	}
	if code, b, _ = pasGet(t, f.h, "/Bundle/"+id+"/_history/1"); code != http.StatusOK || actionCodes(claimResponseOf(t, b)) != "A4" {
		t.Fatalf("version 1 should be the pended answer: %d %v", code, b)
	}
	if code, _, _ = pasGet(t, f.h, "/Bundle/"+id+"/_history/9"); code != http.StatusNotFound {
		t.Fatalf("a version that never existed: %d", code)
	}
	if code, cr, _ := pasGet(t, f.h, "/ClaimResponse/"+id); code != http.StatusOK || cr["resourceType"] != "ClaimResponse" || actionCodes(cr) != "A1" {
		t.Fatalf("ClaimResponse read: %d %v", code, cr)
	}
	// Anything else is still the ordinary read.
	if code, out, _ := pasGet(t, f.h, "/Bundle/not-a-pas-id"); code != http.StatusNotFound || out["resourceType"] != "OperationOutcome" {
		t.Fatalf("unknown Bundle: %d %v", code, out)
	}
}

func TestAPatientTokenCannotReadPASAnswers(t *testing.T) {
	f := newPASFixture(t)
	_, submitted := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("R2", "3"))
	id := str(claimResponseOf(t, submitted)["id"])
	srv := NewServer(f.store, "http://example.test/fhir", nil)
	srv.Auth = patientOnlyAuth{}
	srv.PAS = &PAS{Decide: pasRules}
	h := srv.Handler()
	for _, p := range []string{"/Bundle/" + id, "/ClaimResponse/" + id, "/Bundle/" + id + "/_history/1"} {
		if code, _, _ := pasGet(t, h, p); code != http.StatusNotFound {
			t.Errorf("%s with a patient token: %d, want 404", p, code)
		}
	}
}
