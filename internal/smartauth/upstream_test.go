package smartauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProvider is an OpenID Connect provider that signs in whoever the test says, as the subject the test says.
type fakeProvider struct {
	*httptest.Server
	key            *Key
	mu             sync.Mutex
	nonce, subject string
	issueFor       string // the code it accepts
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "idp.key"))
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{key: key, issueFor: "idp-code"}
	mux := http.NewServeMux()
	fp.Server = httptest.NewServer(mux)
	t.Cleanup(fp.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": fp.URL, "authorization_endpoint": fp.URL + "/authorize",
			"token_endpoint": fp.URL + "/token", "jwks_uri": fp.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(key.JWKS()) })
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fp.mu.Lock()
		defer fp.mu.Unlock()
		if r.PostForm.Get("code") != fp.issueFor || r.PostForm.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		now := time.Now()
		id, _ := key.Sign(map[string]any{"iss": fp.URL, "sub": fp.subject, "aud": "perfuse-smart", "nonce": fp.nonce,
			"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()})
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "x", "token_type": "Bearer", "id_token": id})
	})
	return fp
}

func upstreamFixture(t *testing.T) (*fixture, *fakeProvider) {
	t.Helper()
	fp := newFakeProvider(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "oidc.yaml")
	_ = os.WriteFile(filepath.Join(dir, "secret"), []byte("s3cret\n"), 0o600)
	_ = os.WriteFile(cfg, []byte("issuer: "+fp.URL+"\nclient_id: perfuse-smart\nclient_secret_file: "+filepath.Join(dir, "secret")+
		"\nlabel: Example ID\n"), 0o600)
	up, err := LoadUpstream(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	f := userFixture(t)
	f.srv.Users["carol"] = &User{Username: "carol", Name: "Dr Carol", FHIRUser: "Practitioner/c1", OIDCSubject: "idp-sub-carol"}
	f.srv.Upstream = up
	f.h = f.srv.Handler()
	return f, fp
}

func TestAPersonSignsInAtTheOrganisationsIdentityProvider(t *testing.T) {
	f, fp := upstreamFixture(t)
	b := browser{t, f.h}
	page := b.do(http.MethodGet, authorizeQuery(f, "openid fhirUser launch/patient user/*.rs"), nil)
	if !strings.Contains(page.Body.String(), "Sign in with Example ID") {
		t.Fatalf("no upstream button: %s", page.Body)
	}
	req := reqOf(t, page)
	start := b.do(http.MethodPost, "/oidc/start", url.Values{"req": {req}})
	if start.Code != http.StatusFound {
		t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	loc, _ := url.Parse(start.Header().Get("Location"))
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), fp.URL+"/authorize") || q.Get("client_id") != "perfuse-smart" || q.Get("code_challenge") == "" ||
		q.Get("redirect_uri") != f.srv.UpstreamRedirect() {
		t.Fatalf("authorize request: %s", loc)
	}
	fp.mu.Lock()
	fp.nonce, fp.subject = q.Get("nonce"), "idp-sub-carol"
	fp.mu.Unlock()

	cb := b.do(http.MethodGet, "/oidc/callback?"+url.Values{"state": {q.Get("state")}, "code": {"idp-code"}}.Encode(), nil)
	if !strings.Contains(cb.Body.String(), "Choose a patient") {
		t.Fatalf("a clinician signed in upstream goes on to choose a patient: %d %s", cb.Code, cb.Body)
	}
	rec := b.do(http.MethodPost, "/patient", url.Values{"req": {req}, "patient": {"p2"}})
	code := codeFrom(t, b.do(http.MethodPost, "/consent", url.Values{"req": {req}, "action": {"allow"}, "scope": {"user/*.rs"}}))
	_ = rec
	status, tok := exchange(f, code.Get("code"), verifier)
	if status != 200 || tok["patient"] != "p2" {
		t.Fatalf("token: %d %v", status, tok)
	}

	// The callback works once.
	if again := b.do(http.MethodGet, "/oidc/callback?"+url.Values{"state": {q.Get("state")}, "code": {"idp-code"}}.Encode(), nil); again.Code != 400 {
		t.Errorf("a callback was replayed: %d", again.Code)
	}
}

func TestAnUpstreamAccountLinkedToNobodyIsRefused(t *testing.T) {
	f, fp := upstreamFixture(t)
	b := browser{t, f.h}
	req := reqOf(t, b.do(http.MethodGet, authorizeQuery(f, "openid user/*.rs"), nil))
	start := b.do(http.MethodPost, "/oidc/start", url.Values{"req": {req}})
	loc, _ := url.Parse(start.Header().Get("Location"))
	fp.mu.Lock()
	fp.nonce, fp.subject = loc.Query().Get("nonce"), "someone-else"
	fp.mu.Unlock()
	cb := b.do(http.MethodGet, "/oidc/callback?"+url.Values{"state": {loc.Query().Get("state")}, "code": {"idp-code"}}.Encode(), nil)
	if cb.Code != http.StatusUnauthorized || !strings.Contains(cb.Body.String(), "not linked") {
		t.Errorf("an unlinked account: %d %s", cb.Code, cb.Body)
	}

	// A token for another sign-in attempt (a different nonce) is refused.
	start = b.do(http.MethodPost, "/oidc/start", url.Values{"req": {req}})
	loc, _ = url.Parse(start.Header().Get("Location"))
	fp.mu.Lock()
	fp.nonce, fp.subject = "a-different-attempt", "idp-sub-carol"
	fp.mu.Unlock()
	cb = b.do(http.MethodGet, "/oidc/callback?"+url.Values{"state": {loc.Query().Get("state")}, "code": {"idp-code"}}.Encode(), nil)
	if cb.Code != http.StatusUnauthorized || !strings.Contains(cb.Body.String(), "could not be verified") {
		t.Errorf("a token with another attempt's nonce: %d %s", cb.Code, cb.Body)
	}
	// An OIDC-only person has no password to guess.
	if rec := b.do(http.MethodPost, "/signin", url.Values{"req": {req}, "username": {"carol"}, "password": {""}}); rec.Code != http.StatusUnauthorized {
		t.Errorf("an OIDC-only person signed in with an empty password: %d", rec.Code)
	}
}

func TestTheUsersFileLinksUpstreamSubjects(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "u.yaml")
	_ = os.WriteFile(p, []byte("users:\n  - username: carol\n    fhir_user: Practitioner/c1\n    oidc_subject: abc\n"), 0o600)
	if u, err := LoadUsers(p); err != nil || u["carol"].OIDCSubject != "abc" {
		t.Errorf("%v %v", u, err)
	}
	_ = os.WriteFile(p, []byte("users:\n  - username: carol\n    fhir_user: Practitioner/c1\n"), 0o600)
	if _, err := LoadUsers(p); err == nil {
		t.Error("a user with neither a password nor a subject was accepted")
	}
	_ = os.WriteFile(p, []byte("users:\n  - username: a\n    fhir_user: Practitioner/1\n    oidc_subject: x\n  - username: b\n    fhir_user: Practitioner/2\n    oidc_subject: x\n"), 0o600)
	if _, err := LoadUsers(p); err == nil {
		t.Error("one subject linked to two people was accepted")
	}
}
