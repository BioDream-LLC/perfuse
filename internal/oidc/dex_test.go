package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// dexProvider is a running dex, or a skip.
//
// dex is a real OpenID Connect provider rather than a mock. That distinction is the whole point: a mock verifies my
// understanding of the specification, and this verifies the specification. Every DICOM bug found today came from testing
// against somebody else's implementation, and authentication is a worse place than DICOM to be confidently wrong.
type dexProvider struct {
	issuer   string
	clientID string
	secret   string
	redirect string
	email    string
	password string
}

func startDex(t *testing.T) *dexProvider {
	t.Helper()

	binary := os.Getenv("PERFUSE_DEX")
	if binary == "" {
		binary = "/tmp/dexbin"
	}
	if _, err := os.Stat(binary); err != nil {
		t.Skip("dex is not built; build it and set PERFUSE_DEX to run the identity provider tests")
	}

	port := freePort(t)
	callbackPort := freePort(t)

	p := &dexProvider{
		issuer:   "http://127.0.0.1:" + port + "/dex",
		clientID: "perfuse",
		secret:   "perfuse-test-secret",
		redirect: "http://127.0.0.1:" + callbackPort + "/auth/callback",
		email:    "rosalind@example.test",
		password: "password",
	}

	dir := t.TempDir()
	cfg := dir + "/dex.yaml"
	// The hash is bcrypt of "password". A static password provider is used rather than a connector to something else,
	// because what is being tested is the protocol between Perfuse and the provider, not the provider's own back end.
	contents := "issuer: " + p.issuer + "\n" +
		"storage:\n  type: memory\n" +
		"web:\n  http: 127.0.0.1:" + port + "\n" +
		"oauth2:\n  skipApprovalScreen: true\n" +
		"staticClients:\n" +
		"  - id: " + p.clientID + "\n" +
		"    redirectURIs:\n      - '" + p.redirect + "'\n" +
		"    name: 'Perfuse'\n" +
		"    secret: " + p.secret + "\n" +
		"enablePasswordDB: true\n" +
		"staticPasswords:\n" +
		"  - email: \"" + p.email + "\"\n" +
		"    hash: \"$2a$10$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W\"\n" +
		"    username: \"rosalind\"\n" +
		"    userID: \"08a8684b-db88-4b73-90a9-3cd1661f5466\"\n"

	if err := os.WriteFile(cfg, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "serve", cfg)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Skipf("dex would not start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Waited for by asking for the discovery document, not by sleeping. A fixed sleep is either too short on a loaded
	// machine or wasted on an idle one.
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(p.issuer + "/.well-known/openid-configuration")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("dex never served its discovery document")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestDiscoveryAgainstRealProvider reads a real provider's configuration.
func TestDiscoveryAgainstRealProvider(t *testing.T) {
	dex := startDex(t)

	p, err := Discover(context.Background(), DefaultHTTPClient, dex.issuer)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	if p.Issuer != dex.issuer {
		t.Errorf("issuer came back as %q", p.Issuer)
	}
	if p.AuthURL == "" || p.TokenURL == "" || p.JWKSURL == "" {
		t.Errorf("an endpoint was missing: %+v", p)
	}
	if !p.SupportsAnyOf(SupportedAlgorithms) {
		t.Errorf("the provider signs with %v, none of which is implemented", p.AlgorithmsSupported)
	}
}

// TestFullSignInAgainstRealProvider drives an entire authorization code flow.
//
// This is the test that matters. It signs a person in through a real provider's own login form, exchanges the code, and
// verifies the resulting token - so the signature, the audience, the nonce and the PKCE verifier are all checked against
// something that was not written here.
func TestFullSignInAgainstRealProvider(t *testing.T) {
	dex := startDex(t)

	provider, err := Discover(context.Background(), DefaultHTTPClient, dex.issuer)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	req, err := NewAuthRequest(dex.redirect)
	if err != nil {
		t.Fatal(err)
	}

	code := signInThroughBrowser(t, provider, dex, req)

	tokens, err := Exchange(context.Background(), DefaultHTTPClient, provider,
		dex.clientID, dex.secret, code, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	keys := NewKeySet(provider.JWKSURL, DefaultHTTPClient)
	claims, err := Verify(context.Background(), keys, tokens.IDToken, VerifyOptions{
		Issuer:   provider.Issuer,
		ClientID: dex.clientID,
		Nonce:    req.Nonce,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if claims.Subject == "" {
		t.Error("the verified token has no subject")
	}
	if claims.Email != dex.email {
		t.Errorf("email came back as %q, want %q", claims.Email, dex.email)
	}
	if claims.Issuer != dex.issuer {
		t.Errorf("issuer came back as %q", claims.Issuer)
	}
	if !claims.ExpiresAt.After(time.Now()) {
		t.Error("the token is already expired")
	}
}

// TestWrongNonceIsRejected proves the nonce check is load-bearing.
//
// A real token from a real provider, rejected only because the nonce does not match the attempt. Without this check a token
// captured from one sign-in can be replayed into another, and the check is invisible until something tests it.
func TestWrongNonceIsRejected(t *testing.T) {
	dex := startDex(t)

	provider, err := Discover(context.Background(), DefaultHTTPClient, dex.issuer)
	if err != nil {
		t.Fatal(err)
	}
	req, err := NewAuthRequest(dex.redirect)
	if err != nil {
		t.Fatal(err)
	}

	code := signInThroughBrowser(t, provider, dex, req)
	tokens, err := Exchange(context.Background(), DefaultHTTPClient, provider, dex.clientID, dex.secret, code, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	keys := NewKeySet(provider.JWKSURL, DefaultHTTPClient)

	// Same token, wrong nonce.
	_, err = Verify(context.Background(), keys, tokens.IDToken, VerifyOptions{
		Issuer:   provider.Issuer,
		ClientID: dex.clientID,
		Nonce:    "not-the-nonce-that-was-sent",
	})
	if err == nil {
		t.Fatal("a token with the wrong nonce was accepted, so a captured token could be replayed")
	}
	if !strings.Contains(err.Error(), "nonce") {
		t.Errorf("the refusal was %q, which does not name the nonce", err)
	}
}

// TestWrongAudienceIsRejected proves the audience check is load-bearing.
//
// A valid token issued to a different client must not sign somebody in here. This is the check that stops any other
// application registered with the same identity provider from being a way into Perfuse.
func TestWrongAudienceIsRejected(t *testing.T) {
	dex := startDex(t)

	provider, err := Discover(context.Background(), DefaultHTTPClient, dex.issuer)
	if err != nil {
		t.Fatal(err)
	}
	req, err := NewAuthRequest(dex.redirect)
	if err != nil {
		t.Fatal(err)
	}

	code := signInThroughBrowser(t, provider, dex, req)
	tokens, err := Exchange(context.Background(), DefaultHTTPClient, provider, dex.clientID, dex.secret, code, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	keys := NewKeySet(provider.JWKSURL, DefaultHTTPClient)
	_, err = Verify(context.Background(), keys, tokens.IDToken, VerifyOptions{
		Issuer:   provider.Issuer,
		ClientID: "some-other-application",
		Nonce:    req.Nonce,
	})
	if err == nil {
		t.Fatal("a token issued for another application was accepted")
	}
}

// TestTamperedTokenIsRejected proves the signature check is load-bearing.
//
// The payload is edited in a way that leaves every other check passing: the issuer, audience, nonce and expiry are untouched
// and the JSON stays valid, so the only thing that can reject this token is the signature.
//
// The first version of this test flipped one character of the base64, which corrupted the JSON and made a different check
// reject it. It passed with signature verification disabled entirely - a test that proved nothing while appearing to prove
// the most important thing in the package. Found by disabling the check and watching it still pass.
func TestTamperedTokenIsRejected(t *testing.T) {
	dex := startDex(t)

	provider, err := Discover(context.Background(), DefaultHTTPClient, dex.issuer)
	if err != nil {
		t.Fatal(err)
	}
	req, err := NewAuthRequest(dex.redirect)
	if err != nil {
		t.Fatal(err)
	}

	code := signInThroughBrowser(t, provider, dex, req)
	tokens, err := Exchange(context.Background(), DefaultHTTPClient, provider, dex.clientID, dex.secret, code, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	parts := strings.Split(tokens.IDToken, ".")
	if len(parts) != 3 {
		t.Fatalf("the token has %d parts", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}

	// Escalation as an attacker would attempt it: keep the identity checks satisfied and change what the token says about
	// the person. Groups are what a role is derived from, so this is the edit that matters.
	claims["email"] = "attacker@example.test"
	claims["groups"] = []string{"perfuse-admins"}

	edited, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	// The original signature, over the original payload, attached to the edited one.
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(edited) + "." + parts[2]

	keys := NewKeySet(provider.JWKSURL, DefaultHTTPClient)
	claimsOut, err := Verify(context.Background(), keys, tampered, VerifyOptions{
		Issuer:   provider.Issuer,
		ClientID: dex.clientID,
		Nonce:    req.Nonce,
	})
	if err == nil {
		t.Fatalf("a token with an edited payload was accepted, granting groups %v to %q",
			claimsOut.Groups, claimsOut.Email)
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("the token was rejected by %q rather than by its signature, so this does not test the signature", err)
	}
}

// TestWrongPKCEVerifierIsRejected proves PKCE is load-bearing.
//
// The provider must refuse the exchange when the verifier does not match the challenge sent with the authentication request.
// This is what makes a stolen authorization code useless, and it is enforced at the provider rather than here - so the only
// way to know it is working is to try it against a real one.
func TestWrongPKCEVerifierIsRejected(t *testing.T) {
	dex := startDex(t)

	provider, err := Discover(context.Background(), DefaultHTTPClient, dex.issuer)
	if err != nil {
		t.Fatal(err)
	}
	req, err := NewAuthRequest(dex.redirect)
	if err != nil {
		t.Fatal(err)
	}

	code := signInThroughBrowser(t, provider, dex, req)

	// Same code, different verifier.
	wrong := *req
	wrong.Verifier = "a-completely-different-verifier-of-sufficient-length-to-be-legal"

	if _, err := Exchange(context.Background(), DefaultHTTPClient, provider,
		dex.clientID, dex.secret, code, &wrong); err == nil {
		t.Fatal("the provider accepted a code exchange with the wrong PKCE verifier")
	}
}

// signInThroughBrowser performs the provider's own login and returns the authorization code.
//
// A cookie jar and no redirect following, so the callback can be intercepted rather than served. This is what a browser does,
// minus the rendering - dex's login form is a plain POST, so it can be driven directly.
func signInThroughBrowser(t *testing.T, provider *Provider, dex *dexProvider, req *AuthRequest) string {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	var captured string
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if strings.HasPrefix(r.URL.String(), dex.redirect) {
				captured = r.URL.Query().Get("code")
				if errParam := r.URL.Query().Get("error"); errParam != "" {
					return fmt.Errorf("the provider returned an error: %s", errParam)
				}
				// Stopped here on purpose: following it would try to reach a callback nobody is serving.
				return http.ErrUseLastResponse
			}
			if len(via) > 15 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
		Timeout: 20 * time.Second,
	}

	authURL := req.AuthURL(provider, dex.clientID, nil)
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("opening the sign-in page: %v", err)
	}
	loginURL := resp.Request.URL.String()
	_ = resp.Body.Close()

	if captured != "" {
		return captured
	}

	// dex's local password form. Posted to whatever URL the redirects landed on, which is where the form itself points.
	form := url.Values{}
	form.Set("login", dex.email)
	form.Set("password", dex.password)

	resp, err = client.PostForm(loginURL, form)
	if err != nil {
		t.Fatalf("submitting the sign-in form: %v", err)
	}
	_ = resp.Body.Close()

	if captured == "" {
		t.Fatalf("no authorization code was returned; the flow ended at %s with status %s",
			resp.Request.URL, resp.Status)
	}

	return captured
}
