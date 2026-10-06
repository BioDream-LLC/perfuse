package smartauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/store"
)

const appRedirect = "https://app.example/callback"

func userFixture(t *testing.T) *fixture {
	f := newFixture(t)
	hash, _ := store.HashPassword("correct horse")
	f.srv.Users = Users{
		"amy": {Username: "amy", Name: "Amy Member", PasswordHash: hash, FHIRUser: "Patient/p1"},
		"drb": {Username: "drb", Name: "Dr B", PasswordHash: hash, FHIRUser: "Practitioner/d1"},
	}
	f.srv.Patients = func(context.Context, string) ([]PatientChoice, error) {
		return []PatientChoice{{ID: "p1", Name: "Amy Member"}, {ID: "p2", Name: "Ben Other"}}, nil
	}
	f.srv.PatientExists = func(_ context.Context, id string) bool { return id == "p1" || id == "p2" }
	f.srv.Clients["app"] = &Client{ID: "app", Name: "Health App", Kind: KindPublic, RedirectURIs: []string{appRedirect},
		Scopes: []string{"openid", "fhirUser", "profile", "launch/patient", "offline_access", "patient/*", "user/*"}}
	f.h = f.srv.Handler()
	return f
}

type browser struct {
	t *testing.T
	h http.Handler
}

func (b browser) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodGet {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	return rec
}

var reqField = regexp.MustCompile(`name="req" value="([^"]+)"`)

func reqOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := reqField.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no form on the page (%d): %s", rec.Code, rec.Body)
	}
	return m[1]
}

const verifier = "a-verifier-of-sufficient-length-0123456789abcdef"

func challenge() string {
	s := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

func authorizeQuery(f *fixture, scope string) string {
	return "/authorize?" + url.Values{"response_type": {"code"}, "client_id": {"app"}, "redirect_uri": {appRedirect},
		"scope": {scope}, "state": {"st8"}, "aud": {f.srv.Audience}, "code_challenge": {challenge()},
		"code_challenge_method": {"S256"}, "nonce": {"n1"}}.Encode()
}

// signIn runs the flow to the consent page and returns the request id there.
func signIn(t *testing.T, f *fixture, user, scope string) (browser, string) {
	b := browser{t, f.h}
	rec := b.do(http.MethodGet, authorizeQuery(f, scope), nil)
	req := reqOf(t, rec)
	rec = b.do(http.MethodPost, "/signin", url.Values{"req": {req}, "username": {user}, "password": {"correct horse"}})
	if rec.Code != 200 {
		t.Fatalf("sign in: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "Choose a patient") {
		rec = b.do(http.MethodPost, "/patient", url.Values{"req": {req}, "patient": {"p2"}})
	}
	if !strings.Contains(rec.Body.String(), "Allow access") {
		t.Fatalf("no consent page: %s", rec.Body)
	}
	return b, req
}

func codeFrom(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("want a redirect, got %d %s", rec.Code, rec.Body)
	}
	u, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.HasPrefix(u.String(), appRedirect) {
		t.Fatalf("redirected to %s", u)
	}
	return u.Query()
}

func exchange(f *fixture, code, v string) (int, map[string]any) {
	return f.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {v},
		"redirect_uri": {appRedirect}, "client_id": {"app"}})
}

func TestAMemberAuthorizesAnAppForTheirOwnRecord(t *testing.T) {
	f := userFixture(t)
	b, req := signIn(t, f, "amy", "openid fhirUser launch/patient offline_access patient/*.rs user/*.rs")
	q := codeFrom(t, b.do(http.MethodPost, "/consent", url.Values{"req": {req}, "action": {"allow"},
		"scope": {"patient/*.rs", "offline_access"}})) // user/*.rs unticked
	if q.Get("state") != "st8" || q.Get("code") == "" {
		t.Fatalf("redirect %v", q)
	}
	code, body := exchange(f, q.Get("code"), verifier)
	if code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if body["patient"] != "p1" || body["refresh_token"] == nil || body["id_token"] == nil {
		t.Errorf("token response %v", body)
	}
	if s := body["scope"].(string); strings.Contains(s, "user/") || !strings.Contains(s, "patient/*.rs") || !strings.Contains(s, "openid") {
		t.Errorf("scope %q: the unticked scope must be gone, the identity scopes kept", s)
	}
	keys, _ := oidc.NewStaticKeySet(f.srv.Key.JWKS())
	id, err := oidc.Verify(context.Background(), keys, body["id_token"].(string), oidc.VerifyOptions{Issuer: f.srv.Issuer,
		ClientID: "app", Algorithms: []string{"RS256"}, Nonce: "n1"})
	if err != nil {
		t.Fatalf("id token: %v", err)
	}
	_ = id
	at, err := oidc.Verify(context.Background(), keys, body["access_token"].(string), oidc.VerifyOptions{Issuer: f.srv.Issuer,
		ClientID: f.srv.Audience, Algorithms: []string{"RS256"}, System: true})
	if err != nil || at.Patient != "p1" {
		t.Fatalf("access token %v %+v", err, at)
	}

	// The code works once.
	if code, _ := exchange(f, q.Get("code"), verifier); code != 400 {
		t.Errorf("a code was exchanged twice: %d", code)
	}
	// The refresh token gives a new access token, and can be narrowed but not widened.
	code, body2 := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {body["refresh_token"].(string)}, "client_id": {"app"}})
	if code != 200 || body2["patient"] != "p1" || body2["access_token"] == body["access_token"] {
		t.Errorf("refresh: %d %v", code, body2)
	}
	if code, _ := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {body["refresh_token"].(string)},
		"client_id": {"app"}, "scope": {"user/*.rs"}}); code != 400 {
		t.Errorf("a refresh widened the scopes: %d", code)
	}
}

func TestAClinicianChoosesThePatient(t *testing.T) {
	f := userFixture(t)
	b, req := signIn(t, f, "drb", "launch/patient patient/Observation.rs")
	q := codeFrom(t, b.do(http.MethodPost, "/consent", url.Values{"req": {req}, "action": {"allow"}, "scope": {"patient/Observation.rs"}}))
	_, body := exchange(f, q.Get("code"), verifier)
	if body["patient"] != "p2" || body["scope"] != "patient/Observation.rs launch/patient" {
		t.Errorf("token %v", body)
	}
}

func TestPatientScopesWithoutAPatientAreDropped(t *testing.T) {
	f := userFixture(t)
	b, req := signIn(t, f, "drb", "patient/*.rs user/Patient.rs")
	q := codeFrom(t, b.do(http.MethodPost, "/consent", url.Values{"req": {req}, "action": {"allow"}, "scope": {"patient/*.rs", "user/Patient.rs"}}))
	_, body := exchange(f, q.Get("code"), verifier)
	if body["scope"] != "user/Patient.rs" || body["patient"] != nil {
		t.Errorf("token %v: patient scopes with no patient would read everyone", body)
	}
}

func TestTheFlowRefusesWhatItShould(t *testing.T) {
	f := userFixture(t)
	b := browser{t, f.h}

	// An unregistered redirect is never followed.
	rec := b.do(http.MethodGet, strings.Replace(authorizeQuery(f, "patient/*.rs"), url.QueryEscape(appRedirect), url.QueryEscape("https://evil.example/cb"), 1), nil)
	if rec.Code != 400 || rec.Header().Get("Location") != "" {
		t.Errorf("unregistered redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// A token for another FHIR server, or no PKCE, goes back to the app as an error.
	for name, query := range map[string]string{
		"aud":  strings.Replace(authorizeQuery(f, "patient/*.rs"), url.QueryEscape(f.srv.Audience), url.QueryEscape("https://other.example/fhir"), 1),
		"pkce": strings.Replace(authorizeQuery(f, "patient/*.rs"), "code_challenge_method=S256", "code_challenge_method=plain", 1),
	} {
		if q := codeFrom(t, b.do(http.MethodGet, query, nil)); q.Get("error") != "invalid_request" || q.Get("code") != "" {
			t.Errorf("%s: %v", name, q)
		}
	}
	// Wrong passwords: five, and the app is told no.
	req := reqOf(t, b.do(http.MethodGet, authorizeQuery(f, "patient/*.rs"), nil))
	for i := 0; i < 4; i++ {
		if rec := b.do(http.MethodPost, "/signin", url.Values{"req": {req}, "username": {"amy"}, "password": {"wrong"}}); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	if q := codeFrom(t, b.do(http.MethodPost, "/signin", url.Values{"req": {req}, "username": {"amy"}, "password": {"wrong"}})); q.Get("error") != "access_denied" {
		t.Errorf("fifth failure: %v", q)
	}
	// Deny.
	b2, req2 := signIn(t, f, "amy", "patient/*.rs")
	if q := codeFrom(t, b2.do(http.MethodPost, "/consent", url.Values{"req": {req2}, "action": {"deny"}})); q.Get("error") != "access_denied" {
		t.Errorf("deny: %v", q)
	}
	// A wrong PKCE verifier.
	b3, req3 := signIn(t, f, "amy", "patient/*.rs")
	q := codeFrom(t, b3.do(http.MethodPost, "/consent", url.Values{"req": {req3}, "action": {"allow"}, "scope": {"patient/*.rs"}}))
	if code, body := exchange(f, q.Get("code"), "another-verifier-0123456789abcdef0123456789"); code != 400 || body["error"] != "invalid_grant" {
		t.Errorf("wrong verifier: %d %v", code, body)
	}
}
