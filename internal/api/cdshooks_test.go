package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/crd"
	"github.com/biodream-llc/perfuse/internal/store"
)

const cdsCall = `{"hook":"order-sign","hookInstance":"h1","context":{"userId":"Practitioner/1","patientId":"p1","draftOrders":
{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"DeviceRequest","id":"o1","status":"draft",
"intent":"original-order","codeCodeableConcept":{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0424"}]},
"subject":{"reference":"Patient/p1"}}}]}},"prefetch":{"coverage":{"resourceType":"Bundle","type":"searchset","entry":[{"resource":
{"resourceType":"Coverage","id":"cov1","status":"active"}}]}}}`

func TestTheCRDServiceIsDiscoverableAndAnswersOnlyAnAuthenticatedCaller(t *testing.T) {
	h := newHarness(t)
	rules, err := crd.LoadRules("../../examples/crd/rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h.server.CRD = rules

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cds-services", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"crd-order-sign"`) ||
		!strings.Contains(rec.Body.String(), "Coverage?patient={{context.patientId}}") {
		t.Fatalf("discovery: %d %s", rec.Code, rec.Body.String())
	}

	call := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/cds-services/crd-order-sign", strings.NewReader(cdsCall))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(""); rec.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated call: %d", rec.Code)
	}
	if rec := call("Bearer not.a.jwt"); rec.Code != http.StatusUnauthorized {
		t.Errorf("an untrusted JWT: %d", rec.Code)
	}
	token, err := h.store.CreateAPIToken(t.Context(), "ehr", store.RoleViewer, "admin")
	if err != nil {
		t.Fatal(err)
	}
	rec = call("Bearer " + token)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "prior authorization required") || !strings.Contains(body, `"systemActions"`) ||
		!strings.Contains(body, "ext-coverage-information") {
		t.Errorf("%d %s", rec.Code, body)
	}
}

func TestAskingThisServersCRDFromTheConsole(t *testing.T) {
	h := newHarness(t)
	h.server.CRD, _ = crd.LoadRules("../../examples/crd/rules.yaml")
	rec := h.do("viewer", http.MethodPost, "/api/crd/ask", map[string]any{
		"patientId": "p1",
		"order": map[string]any{"resourceType": "ServiceRequest", "id": "s1", "status": "draft", "intent": "order",
			"code": map[string]any{"coding": []any{map[string]any{"code": "15830"}}}, "subject": map[string]any{"reference": "Patient/p1"}},
		"coverage": map[string]any{"resourceType": "Coverage", "id": "c1", "status": "active"},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Cosmetic procedure: not covered") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}
