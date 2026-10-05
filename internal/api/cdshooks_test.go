package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestAskingForADTRPackageFromTheConsole(t *testing.T) {
	h := newHarness(t)
	rec := h.do("viewer", http.MethodPost, "/api/dtr/package", map[string]any{"order": map[string]any{"resourceType": "DeviceRequest"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("no FHIR endpoint: %d %s", rec.Code, rec.Body.String())
	}
	var got []byte
	h.server.DTRPackage = func(_ context.Context, params []byte) (int, []byte) {
		got = params
		return http.StatusOK, []byte(`{"resourceType":"Parameters","parameter":[]}`)
	}
	rec = h.do("viewer", http.MethodPost, "/api/dtr/package", map[string]any{
		"order":    map[string]any{"resourceType": "DeviceRequest", "id": "o"},
		"coverage": map[string]any{"resourceType": "Coverage", "id": "c"},
	})
	if rec.Code != http.StatusOK || !strings.Contains(string(got), `"name":"coverage"`) || !strings.Contains(string(got), `"name":"order"`) {
		t.Errorf("%d %s; sent %s", rec.Code, rec.Body.String(), got)
	}
}

func TestAnUnknownCDSServicePathAnswersInJSON(t *testing.T) {
	h := newHarness(t)
	rec := h.do("", http.MethodGet, "/cds-services/cds-services", nil)
	if rec.Code != http.StatusMethodNotAllowed || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("%d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestDiscoveryDeclaresTheCRDVersionAndConfigurationOptions(t *testing.T) {
	h := newHarness(t)
	h.server.CRD, _ = crd.LoadRules("../../examples/crd/rules.yaml")
	rec := h.do("", http.MethodGet, "/cds-services", nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"davinci-crd.version":["2.2"]`) || !strings.Contains(body, `"code":"coverage-info"`) || !strings.Contains(body, `"type":"boolean"`) {
		t.Errorf("%s", body)
	}
}

// TestAnEHRsJWTWithoutASubjectIsAcceptedOnce is the CDS Hooks client JWT as the specification defines it and as the Inferno CRD
// test kit sends it: iss, aud, exp, iat and jti, and no sub. Every such call used to be refused for having no subject. Its jti
// is remembered, so the same token is refused a second time.
func TestAnEHRsJWTWithoutASubjectIsAcceptedOnce(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	pad := func(b []byte) []byte { return append(make([]byte, 48-len(b)), b...) }
	jwks := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-384","kid":"k1","alg":"ES384","use":"sig","x":%q,"y":%q}]}`,
		b64(pad(key.X.Bytes())), b64(pad(key.Y.Bytes())))
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(jwks)) }))
	defer keys.Close()

	h := newHarness(t)
	h.server.CRD, _ = crd.LoadRules("../../examples/crd/rules.yaml")
	h.server.CDSClients = []*CDSClient{{Issuer: "https://ehr.test", JWKSURL: keys.URL + "/jwks.json"}}

	header := b64([]byte(`{"alg":"ES384","typ":"JWT","kid":"k1"}`))
	now := time.Now().Unix()
	payload := b64([]byte(fmt.Sprintf(`{"iss":"https://ehr.test","aud":%q,"exp":%d,"iat":%d,"jti":"once-only"}`,
		h.server.publicBase()+"/cds-services/crd-order-sign", now+300, now)))
	digest := sha512.Sum384([]byte(header + "." + payload))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	jwt := header + "." + payload + "." + b64(append(pad(r.Bytes()), pad(s.Bytes())...))

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/cds-services/crd-order-sign", strings.NewReader(cdsCall))
		req.Header.Set("Authorization", "Bearer "+jwt)
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("a JWT with no sub: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "replay") {
		t.Errorf("the same jti again: %d %s", rec.Code, rec.Body.String())
	}
}
