package smartauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

type fixture struct {
	srv       *Server
	clientKey *Key
	h         http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	serverKey, err := LoadOrCreateKey(filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := LoadOrCreateKey(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	var jwks map[string]any
	_ = json.Unmarshal(clientKey.JWKS(), &jwks)
	c := &Client{ID: "dtr-client", Kind: KindBackend, Scopes: []string{"system/*.rs", "system/Claim.c"}, JWKS: jwks}
	c.keys, _ = oidc.NewStaticKeySet(clientKey.JWKS())
	srv := &Server{Issuer: "https://payer.example/auth", Audience: "https://payer.example/fhir", Key: serverKey,
		Clients: Clients{c.ID: c}}
	return &fixture{srv: srv, clientKey: clientKey, h: srv.Handler()}
}

func (f *fixture) assertion(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{"iss": "dtr-client", "sub": "dtr-client", "aud": f.srv.TokenURL(),
		"exp": now.Add(4 * time.Minute).Unix(), "iat": now.Unix(), "jti": randomID()}
	if mutate != nil {
		mutate(claims)
	}
	a, err := f.clientKey.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fixture) token(form url.Values) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func backendForm(assertion, scope string) url.Values {
	return url.Values{"grant_type": {"client_credentials"}, "client_assertion_type": {jwtBearer},
		"client_assertion": {assertion}, "scope": {scope}}
}

func TestABackendClientGetsATokenTheFHIREndpointAccepts(t *testing.T) {
	f := newFixture(t)
	code, body := f.token(backendForm(f.assertion(t, nil), "system/*.rs patient/*.rs system/Claim.c"))
	if code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if body["scope"] != "system/*.rs system/Claim.c" || body["token_type"] != "bearer" || body["expires_in"] != float64(300) {
		t.Errorf("response %v; want the system scopes only, five minutes", body)
	}
	keys, _ := oidc.NewStaticKeySet(f.srv.Key.JWKS())
	c, err := oidc.Verify(context.Background(), keys, body["access_token"].(string), oidc.VerifyOptions{
		Issuer: f.srv.Issuer, ClientID: f.srv.Audience, Algorithms: []string{"RS256"}, System: true})
	if err != nil || c.Scope != "system/*.rs system/Claim.c" || c.Subject != "dtr-client" {
		t.Fatalf("the access token: %v %+v", err, c)
	}
}

func TestAClientAssertionIsUsedOnce(t *testing.T) {
	f := newFixture(t)
	a := f.assertion(t, nil)
	if code, _ := f.token(backendForm(a, "system/*.rs")); code != 200 {
		t.Fatal("first use refused")
	}
	if code, body := f.token(backendForm(a, "system/*.rs")); code != 401 || body["error"] != "invalid_client" {
		t.Fatalf("replay: %d %v", code, body)
	}
}

func TestBadAssertionsAreRefused(t *testing.T) {
	f := newFixture(t)
	other, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "other.key"))
	forged, _ := other.Sign(map[string]any{"iss": "dtr-client", "sub": "dtr-client", "aud": f.srv.TokenURL(),
		"exp": time.Now().Add(time.Minute).Unix(), "jti": "x"})
	cases := map[string]string{
		"for another server": f.assertion(t, func(c map[string]any) { c["aud"] = "https://elsewhere.example/token" }),
		"too long-lived":     f.assertion(t, func(c map[string]any) { c["exp"] = time.Now().Add(time.Hour).Unix() }),
		"expired":            f.assertion(t, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"sub not the client": f.assertion(t, func(c map[string]any) { c["sub"] = "someone" }),
		"unknown client":     f.assertion(t, func(c map[string]any) { c["iss"], c["sub"] = "nobody", "nobody" }),
		"signed by another":  forged,
	}
	for name, a := range cases {
		if code, body := f.token(backendForm(a, "system/*.rs")); code != 401 {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	if code, body := f.token(backendForm(f.assertion(t, nil), "system/Patient.cud")); code != 400 || body["error"] != "invalid_scope" {
		t.Errorf("unregistered scope: %d %v", code, body)
	}
	if code, body := f.token(url.Values{"grant_type": {"password"}}); code != 400 || body["error"] != "unsupported_grant_type" {
		t.Errorf("password grant: %d %v", code, body)
	}
}
