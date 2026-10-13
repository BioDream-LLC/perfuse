package fhirserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestAClientIsHeldToItsWritesAMinute(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.WriteLimits = WriteLimits{PerMinute: 2}
	h := srv.Handler()
	patient := `{"resourceType": "Patient", "name": [{"family": "Limit"}]}`
	for i := 0; i < 2; i++ {
		if rec := do(t, h, http.MethodPost, "/Patient", patient); rec.Code != http.StatusCreated {
			t.Fatalf("write %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	rec := do(t, h, http.MethodPost, "/Patient", patient)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "OperationOutcome") {
		t.Fatalf("third write: %d %q %s, want 429 with Retry-After and an OperationOutcome", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	// Reads and searches are not writes.
	if rec := do(t, h, http.MethodGet, "/Patient?family=Limit", nil); rec.Code != http.StatusOK {
		t.Errorf("a search after the limit: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/Patient/_search", "family=Limit"); rec.Code == http.StatusTooManyRequests {
		t.Errorf("a POST search counted as a write")
	}
}

func TestAFullServerRefusesNewResourcesButNotDeletes(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.WriteLimits = WriteLimits{MaxResources: 1}
	h := srv.Handler()
	if rec := do(t, h, http.MethodPut, "/Patient/one", `{"resourceType": "Patient", "id": "one"}`); rec.Code >= 300 {
		t.Fatalf("first resource: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPost, "/Patient", `{"resourceType": "Patient"}`); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("a resource past the limit: %d, want 507", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/Patient/one", nil); rec.Code >= 300 {
		t.Errorf("a delete on a full server: %d", rec.Code)
	}
}
