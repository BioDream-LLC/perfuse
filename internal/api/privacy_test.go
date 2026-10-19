package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/privacy"
	"github.com/biodream-llc/perfuse/internal/store"
)

func TestDataProtectionEndpoints(t *testing.T) {
	h := newHarness(t)
	svc, err := privacy.Open(t.Context(), h.server.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	h.server.Privacy = svc
	h.handler = h.server.Handler()
	if res := h.do(string(store.RoleEditor), "GET", "/api/privacy", nil); res.Code != http.StatusForbidden {
		t.Fatalf("an editor read the data protection requests: %d", res.Code)
	}
	res := h.do(string(store.RoleAdmin), "POST", "/api/privacy/requests", map[string]any{"kind": "erasure", "subject": "letter 12",
		"identifiers": []string{"https://fhir.nhs.uk/Id/nhs-number|943 476 5919"}})
	if res.Code != http.StatusOK {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	// Recorded by this administrator, so not approvable by them; not approved, so not erasable.
	if res := h.do(string(store.RoleAdmin), "POST", "/api/privacy/requests/1/approve", nil); res.Code != http.StatusBadRequest {
		t.Fatalf("self-approval: %d %s", res.Code, res.Body.String())
	}
	if res := h.do(string(store.RoleAdmin), "POST", "/api/privacy/requests/1/erase", nil); res.Code != http.StatusConflict {
		t.Fatalf("erase unapproved: %d", res.Code)
	}
	// A restriction places a hold.
	if res := h.do(string(store.RoleAdmin), "POST", "/api/privacy/requests", map[string]any{"kind": "restriction", "subject": "r",
		"identifiers": []string{"MRN-55"}}); res.Code != http.StatusOK {
		t.Fatal(res.Body.String())
	}
	sum := h.do(string(store.RoleAdmin), "GET", "/api/privacy", nil).Body.String()
	if !strings.Contains(sum, `"kind":"erasure"`) || !strings.Contains(sum, "restriction of processing, request 2") {
		t.Fatalf("summary: %s", sum)
	}
	// The audit log names the request with the number masked.
	audit := h.do(string(store.RoleAdmin), "GET", "/api/audit", nil).Body.String()
	if strings.Contains(audit, "9434765919") || !strings.Contains(audit, "privacy.request") {
		t.Fatalf("audit: %s", audit)
	}
	md := h.do(string(store.RoleAdmin), "GET", "/api/privacy/register", nil)
	if md.Code != http.StatusOK || !strings.Contains(md.Body.String(), "# Record of processing activities") {
		t.Fatalf("register: %d %s", md.Code, md.Body.String())
	}
	if csv := h.do(string(store.RoleAdmin), "GET", "/api/privacy/register?format=csv", nil); !strings.HasPrefix(csv.Body.String(), "controller,dpo") {
		t.Fatalf("csv: %s", csv.Body.String())
	}
}
