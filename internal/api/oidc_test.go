package api

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

// TestAuthMethodsWithoutProvider checks the sign-in page is told there is no federated option.
//
// The absence matters as much as the presence: a button offering a provider that is not configured would send somebody to a
// 404 and look like a broken server rather than an unconfigured one.
func TestAuthMethodsWithoutProvider(t *testing.T) {
	h := newHarness(t)

	rec := h.do("", http.MethodGet, "/api/auth/methods", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth methods answered %d", rec.Code)
	}

	body := decodeBody[authMethodsResponse](t, rec)
	if !body.Password {
		t.Error("the password form was not offered; local accounts are the way in when a provider is unreachable")
	}
	if body.OIDC != nil {
		t.Errorf("a federated option was offered with none configured: %+v", body.OIDC)
	}
}

// TestOIDCEndpointsAbsentWithoutProvider checks the sign-in endpoints refuse rather than half-work.
func TestOIDCEndpointsAbsentWithoutProvider(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/auth/oidc/start", "/auth/callback"} {
		rec := h.do("", http.MethodGet, path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d with no provider configured, want 404", path, rec.Code)
		}
	}
}

// TestReturnToRefusesOffsiteURLs proves the open redirect guard.
//
// A full URL in returnTo would let somebody send a Perfuse sign-in link that lands on a copy of the interface after a genuine
// authentication - which is the dangerous part, because the sign-in itself is real and looks entirely normal.
//
// Checked here as well as live because it is one line of string handling that a later refactor could quietly relax.
func TestReturnToRefusesOffsiteURLs(t *testing.T) {
	h := newHarness(t)
	h.server.OIDC = &OIDCConfig{
		ClientID:    "perfuse",
		RedirectURL: "http://127.0.0.1:1/auth/callback",
		Roles:       &oidc.RoleMapping{Admin: []string{"admins"}},
		Provider: &oidc.Provider{
			Issuer:   "https://issuer.example",
			AuthURL:  "https://issuer.example/auth",
			TokenURL: "https://issuer.example/token",
			JWKSURL:  "https://issuer.example/keys",
		},
	}

	for _, returnTo := range []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		"http://evil.example",
		"javascript:alert(1)",
	} {
		rec := h.do("", http.MethodGet, "/auth/oidc/start?returnTo="+returnTo, nil)
		if rec.Code != http.StatusFound {
			t.Fatalf("returnTo %q gave status %d, want a redirect to the provider", returnTo, rec.Code)
		}

		// The redirect goes to the provider either way; what matters is the attempt recorded for the callback.
		state := stateFromLocation(t, rec.Header().Get("Location"))
		attempt, ok := h.server.authAttempts.take(state)
		if !ok {
			t.Fatalf("returnTo %q recorded no attempt", returnTo)
		}
		if attempt.returnTo != "/" {
			t.Errorf("returnTo %q was kept as %q; it must be reduced to a local path", returnTo, attempt.returnTo)
		}
	}
}

// TestReturnToKeepsLocalPaths checks the guard does not reject what it should allow.
//
// Without this the previous test passes trivially by discarding every returnTo, and somebody signing in from a deep link would
// always land on the dashboard with no indication why.
func TestReturnToKeepsLocalPaths(t *testing.T) {
	h := newHarness(t)
	h.server.OIDC = &OIDCConfig{
		ClientID:    "perfuse",
		RedirectURL: "http://127.0.0.1:1/auth/callback",
		Roles:       &oidc.RoleMapping{Admin: []string{"admins"}},
		Provider: &oidc.Provider{
			Issuer:   "https://issuer.example",
			AuthURL:  "https://issuer.example/auth",
			TokenURL: "https://issuer.example/token",
			JWKSURL:  "https://issuer.example/keys",
		},
	}

	rec := h.do("", http.MethodGet, "/auth/oidc/start?returnTo=/channels", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}

	state := stateFromLocation(t, rec.Header().Get("Location"))
	attempt, ok := h.server.authAttempts.take(state)
	if !ok {
		t.Fatal("no attempt was recorded")
	}
	if attempt.returnTo != "/channels" {
		t.Errorf("a local path was reduced to %q", attempt.returnTo)
	}
}

// TestAuthAttemptExpires checks a stale attempt is not accepted.
func TestAuthAttemptExpires(t *testing.T) {
	attempts := newAuthAttempts()

	req, err := oidc.NewAuthRequest("http://127.0.0.1:1/auth/callback")
	if err != nil {
		t.Fatal(err)
	}

	// Recorded as though it started well before the lifetime.
	attempts.m[req.State] = pendingAuth{
		request:  req,
		started:  time.Now().Add(-authAttemptLifetime - time.Minute),
		returnTo: "/",
	}

	if _, ok := attempts.take(req.State); ok {
		t.Error("an expired sign-in attempt was accepted")
	}
}

// TestAuthAttemptIsSingleUse checks a state cannot be redeemed twice.
//
// An authorization code may only be used once, so a replayed callback must find nothing. Without this a captured callback URL
// could be used again.
func TestAuthAttemptIsSingleUse(t *testing.T) {
	attempts := newAuthAttempts()

	req, err := oidc.NewAuthRequest("http://127.0.0.1:1/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	if !attempts.put(req.State, pendingAuth{request: req, started: time.Now(), returnTo: "/"}) {
		t.Fatal("the attempt was not recorded")
	}

	if _, ok := attempts.take(req.State); !ok {
		t.Fatal("the first use failed")
	}
	if _, ok := attempts.take(req.State); ok {
		t.Error("the same sign-in attempt was accepted twice")
	}
}

// stateFromLocation pulls the state parameter out of a redirect.
func stateFromLocation(t *testing.T, location string) string {
	t.Helper()

	if location == "" {
		t.Fatal("the redirect had no Location header")
	}
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("the Location header %q is not a URL: %v", location, err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("the redirect to %q carries no state", location)
	}
	return state
}
