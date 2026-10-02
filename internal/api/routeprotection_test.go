package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// Whether every route is protected, asked of the server rather than of a list.
//
// This used to walk a list of paths written by hand, which made its own claim untrue: it said an
// endpoint registered without an authorisation check would fail immediately, but a new endpoint was
// invisible until somebody remembered to add a line - and somebody remembering is what the test exists
// to replace. Adding the branding endpoints proved the point, because the test kept passing while
// covering none of them.
//
// Now the server reports what it registered, so a new route is covered the moment it exists.

// publicRoutes are the endpoints deliberately served without authentication.
//
// An allowlist, and the only place one is granted. A route that appears unauthenticated and is not
// here fails the test, so making something public is a decision somebody writes down with a reason
// rather than an omission nobody notices.
var publicRoutes = map[string]string{
	// The catch-all for API paths nothing else claimed, which answers 404 in JSON.
	//
	// Public because refusing to say a path does not exist, until somebody proves who they are, tells them nothing they did not already know: the
	// routes are all in this repository. What it does reveal is the difference between a path that does not exist (404) and one that exists but
	// needs a session (401), which is a small enumeration oracle. That is accepted deliberately, against the alternative it replaced - which was
	// answering 200 with the entire web application and letting the browser fail to parse it.
	//
	// It reads nothing and changes nothing. It echoes the method and path back inside a JSON error, and nothing else.
	" /api/":                "says an API path does not exist, which the source of this project already says; reveals no state and changes nothing",
	"POST /api/login":       "the sign-in form itself",
	"GET /api/auth/methods": "which sign-in methods to offer, needed to draw the sign-in page",
	"GET /auth/oidc/start":  "begins an external sign-in redirect",
	"GET /auth/saml/start":  "begins a SAML sign-in redirect",

	// A POST, and the only unauthenticated one besides the login form. It has to be: the identity provider renders a form in the
	// user's browser and submits it here, so the request comes from another origin and can carry no Perfuse header. What stands in
	// for authentication is the document itself - the signature by the one configured certificate, the audience, the destination,
	// the conditions, the one-time assertion id, and the InResponseTo naming a request this server sent and has not yet answered.
	// Nothing in it is trusted until all of those hold.
	"POST /auth/saml/acs":              "consumes the identity provider's signed response; verified as a document rather than as a session",
	"GET /auth/callback":               "receives the external sign-in redirect",
	"GET /api/health":                  "liveness for a load balancer, which has no session",
	"GET /livez":                       "liveness probe",
	"GET /readyz":                      "readiness probe",
	"GET /metrics":                     "scrape endpoint, guarded separately by token or by being explicitly opened",
	"POST /api/passkeys/signin/begin":  "starts a passkey sign-in, before there is a session",
	"POST /api/passkeys/signin/finish": "completes a passkey sign-in",

	// Branding is readable before sign-in on purpose: the sign-in page is the first thing a customer
	// sees, and an unbranded one defeats the feature. What it exposes is a product name, a colour and
	// an image that an administrator chose in order to show them to people.
	"GET /api/branding":       "product name and colour, needed to brand the sign-in page",
	"GET /api/branding/logo":  "the logo shown on the sign-in page",
	"HEAD /api/branding/logo": "cache validation for the logo",
}

// TestEveryRouteIsEitherProtectedOrDeliberatelyPublic is the guard that matters most in this file.
//
// This server serves PHI. A route registered without an authorisation check is an unauthenticated
// disclosure, and the failure mode is silence: it works, and nobody notices what it did not ask for.
func TestEveryRouteIsEitherProtectedOrDeliberatelyPublic(t *testing.T) {
	h := newHarness(t)

	var unexpected []string
	for _, r := range h.server.Routes() {
		key := r.Method + " " + r.Pattern
		if r.Authenticated() {
			if _, listed := publicRoutes[key]; listed {
				t.Errorf("%s is listed as public but is behind a %s check; remove it from publicRoutes", key, r.Role)
			}
			continue
		}
		if _, ok := publicRoutes[key]; !ok {
			unexpected = append(unexpected, key)
		}
	}
	sort.Strings(unexpected)

	if len(unexpected) > 0 {
		t.Errorf("these routes are served without authentication and are not on the public list:\n  %s\n\n"+
			"If one of them should be public, add it to publicRoutes with the reason. If not, wrap it in "+
			"s.require with a minimum role.", strings.Join(unexpected, "\n  "))
	}
}

// A protected route must actually refuse an anonymous caller. Being wrapped is not the same as
// working, and this is what proves the wrapper does its job.
func TestProtectedRoutesRefuseAnonymousCallers(t *testing.T) {
	h := newHarness(t)

	for _, r := range h.server.Routes() {
		if !r.Authenticated() {
			continue
		}
		// A pattern with a wildcard needs a value, or the router does not match the registration and
		// the 404 would look like a pass.
		path := substituteWildcards(r.Pattern)

		t.Run(r.Method+" "+r.Pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.Method, path, nil)
			req.Header.Set("X-Perfuse-Request", "1") // so a 403 means authorisation, not the CSRF check
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
				t.Errorf("anonymous request returned %d, want 401 or 403", rec.Code)
			}
		})
	}
}

// Every state-changing route must require the cross-site header, or a logged-in operator's browser can
// be made to perform it by another site.
func TestStateChangingRoutesRequireTheRequestHeader(t *testing.T) {
	h := newHarness(t)
	sess := h.tokens["admin"]

	for _, r := range h.server.Routes() {
		if !r.Authenticated() || !isStateChanging(r.Method) {
			continue
		}
		path := substituteWildcards(r.Pattern)

		t.Run(r.Method+" "+r.Pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.Method, path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess})
			// Deliberately no X-Perfuse-Request header.
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("request without the cross-site header returned %d, want 403", rec.Code)
			}
		})
	}
}

// substituteWildcards fills a registered pattern with plausible values.
func substituteWildcards(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			parts[i] = "x"
		}
	}
	return strings.Join(parts, "/")
}
