package smartauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/store"
)

func memberTokens(t *testing.T, f *fixture) map[string]any {
	b, req := signIn(t, f, "amy", "openid fhirUser launch/patient offline_access patient/*.rs")
	q := codeFrom(t, b.do(http.MethodPost, "/consent", url.Values{"req": {req}, "action": {"allow"}, "scope": {"patient/*.rs", "offline_access"}}))
	_, body := exchange(f, q.Get("code"), verifier)
	return body
}

func (f *fixture) post(path string, form url.Values, user, pass string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, path, stringsReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var body map[string]any
	_ = jsonUnmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestIntrospectionAndRevocation(t *testing.T) {
	f := userFixture(t)
	hash, _ := store.HashPassword("resource-server-secret")
	f.srv.Clients["rs"] = &Client{ID: "rs", Kind: KindSymmetric, SecretHash: hash, Scopes: []string{"openid"},
		RedirectURIs: []string{"https://rs.example/cb"}}
	f.h = f.srv.Handler()
	tok := memberTokens(t, f)
	access := tok["access_token"].(string)

	if code, _ := f.post("/introspect", url.Values{"token": {access}, "client_id": {"app"}}, "", ""); code != 401 {
		t.Errorf("a public client introspected: %d", code)
	}
	code, body := f.post("/introspect", url.Values{"token": {access}}, "rs", "resource-server-secret")
	if code != 200 || body["active"] != true || body["patient"] != "p1" || body["client_id"] != "app" || body["fhirUser"] == nil {
		t.Fatalf("introspect: %d %v", code, body)
	}
	if _, body := f.post("/introspect", url.Values{"token": {"not-a-token"}}, "rs", "resource-server-secret"); body["active"] != false {
		t.Errorf("garbage introspected as %v", body)
	}

	keys, _ := oidc.NewStaticKeySet(f.srv.Key.JWKS())
	auth, _ := fhirserver.NewSMARTAuth(fhirserver.SMARTConfig{Issuer: f.srv.Issuer, Audience: f.srv.Audience, Keys: keys, Revoked: f.srv.Revoked})
	req := httptest.NewRequest(http.MethodGet, "/Patient/p1", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	if _, err := auth.Authenticate(req); err != nil {
		t.Fatalf("before revocation: %v", err)
	}
	// Another client cannot revoke this app's token; the app can.
	f.post("/revoke", url.Values{"token": {access}}, "rs", "resource-server-secret")
	if _, err := auth.Authenticate(req); err != nil {
		t.Fatal("another client revoked the app's token")
	}
	if code, _ := f.post("/revoke", url.Values{"token": {access}, "client_id": {"app"}}, "", ""); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if _, err := auth.Authenticate(req); err == nil {
		t.Error("a revoked token was accepted by the FHIR endpoint")
	}
	if _, body := f.post("/introspect", url.Values{"token": {access}}, "rs", "resource-server-secret"); body["active"] != false {
		t.Errorf("a revoked token introspected active")
	}
	refresh := tok["refresh_token"].(string)
	f.post("/revoke", url.Values{"token": {refresh}, "client_id": {"app"}}, "", "")
	if code, _ := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"app"}}); code != 400 {
		t.Errorf("a revoked refresh token still worked: %d", code)
	}
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
