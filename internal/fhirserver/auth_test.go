package fhirserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/authlimit"
)

// This file exists because of a hole, not a feature.
//
// Until it was written, perfuse fhir serve accepted a patient record with no credentials and returned 201, then served
// it back on a plain GET. Anybody who could reach the port could read and change every record. These tests are the
// guard, and each one is checked by planting a violation rather than trusted because it passes.

// testToken is long enough to be accepted by the length check.
const testToken = "test-token-abcdefghijklmnop"

func staticServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer(nil, "http://example.test/fhir", nil)
	s.Auth = &StaticAuth{Tokens: map[string]string{testToken: "test"}}
	return s
}

// probe sends a request through the auth wrapper and reports the status.
//
// The inner handler records whether it ran at all, which is the property that matters: a refusal that still executed the
// handler would have already read or written the database.
func probe(t *testing.T, s *Server, method, path, authHeader string) (int, bool) {
	t.Helper()

	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.RemoteAddr = "198.51.100.7:5000"

	rec := httptest.NewRecorder()
	s.requireAuth(inner).ServeHTTP(rec, req)

	return rec.Code, reached
}

// TestNoCredentialsAreRefused is the exact request that used to succeed.
func TestNoCredentialsAreRefused(t *testing.T) {
	s := staticServer(t)

	status, reached := probe(t, s, http.MethodPost, "/Patient", "")
	if status != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", status)
	}
	if reached {
		t.Error("the handler ran anyway, so the record would have been written before the refusal")
	}
}

// TestReadsAlsoNeedCredentials covers the other half of the same hole.
func TestReadsAlsoNeedCredentials(t *testing.T) {
	s := staticServer(t)

	status, reached := probe(t, s, http.MethodGet, "/Patient", "")
	if status != http.StatusUnauthorized || reached {
		t.Errorf("an unauthenticated read got %d and reached=%v, want 401 and false", status, reached)
	}
}

// TestMetadataNeedsCredentialsToo covers the route most likely to be left open by mistake.
//
// The capability statement lists every resource type and search parameter this server holds, which tells an
// unauthenticated caller exactly what is worth asking for.
func TestMetadataNeedsCredentialsToo(t *testing.T) {
	s := staticServer(t)

	status, _ := probe(t, s, http.MethodGet, "/metadata", "")
	if status != http.StatusUnauthorized {
		t.Errorf("unauthenticated /metadata got %d, want 401", status)
	}
}

// TestAValidTokenIsAccepted covers the ordinary case.
func TestAValidTokenIsAccepted(t *testing.T) {
	s := staticServer(t)

	status, reached := probe(t, s, http.MethodGet, "/Patient", "Bearer "+testToken)
	if status != http.StatusOK || !reached {
		t.Errorf("a valid token got %d and reached=%v, want 200 and true", status, reached)
	}
}

// TestAWrongTokenIsRefused covers a wrong credential.
func TestAWrongTokenIsRefused(t *testing.T) {
	s := staticServer(t)

	status, reached := probe(t, s, http.MethodGet, "/Patient", "Bearer wrong-token-aaaaaaaaaaaaaa")
	if status != http.StatusUnauthorized || reached {
		t.Errorf("a wrong token got %d and reached=%v, want 401 and false", status, reached)
	}
}

// TestBasicAuthIsNamedRatherThanJustRefused covers a common mistake.
//
// "Not valid" would send somebody looking for a wrong password rather than a wrong scheme.
func TestBasicAuthIsNamedRatherThanJustRefused(t *testing.T) {
	s := staticServer(t)

	req := httptest.NewRequest(http.MethodGet, "/Patient", nil)
	req.SetBasicAuth("someone", "something")
	req.RemoteAddr = "198.51.100.8:5000"

	rec := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not a bearer token") {
		t.Errorf("the body should name the scheme, got: %s", rec.Body.String())
	}
}

// TestANilAuthenticatorRefusesEverything covers a wiring mistake.
//
// A nil field is a mistake, and the safe reading of a mistake on this path is that nobody gets in. The alternative -
// nil meaning open - is how the hole existed in the first place.
func TestANilAuthenticatorRefusesEverything(t *testing.T) {
	s := NewServer(nil, "http://example.test/fhir", nil)
	// Auth deliberately left nil.

	status, reached := probe(t, s, http.MethodGet, "/Patient", "Bearer "+testToken)
	if status == http.StatusOK || reached {
		t.Errorf("a nil authenticator allowed a request: status %d, reached=%v", status, reached)
	}
}

// TestTheConstructorLeavesAuthNil is the guard against the hole coming back.
//
// If NewServer ever defaults to OpenAuth, an unauthenticated server becomes the easiest thing to build again - which is
// exactly how this happened. The default has to be the safe one even though it is the less convenient one.
func TestTheConstructorLeavesAuthNil(t *testing.T) {
	s := NewServer(nil, "http://example.test/fhir", nil)
	if s.Auth != nil {
		t.Errorf("NewServer set Auth to %T; it must be left nil so a caller has to choose, and so forgetting "+
			"refuses rather than serves", s.Auth)
	}
}

// TestAReadOnlyTokenCannotWrite covers the role split.
func TestAReadOnlyTokenCannotWrite(t *testing.T) {
	s := NewServer(nil, "http://example.test/fhir", nil)
	s.Auth = &StaticAuth{Tokens: map[string]string{testToken: "test"}, ReadOnly: true}

	if status, _ := probe(t, s, http.MethodGet, "/Patient", "Bearer "+testToken); status != http.StatusOK {
		t.Errorf("a read-only token could not read: %d", status)
	}

	status, reached := probe(t, s, http.MethodPost, "/Patient", "Bearer "+testToken)
	if status != http.StatusForbidden || reached {
		t.Errorf("a read-only token wrote: status %d, reached=%v, want 403 and false", status, reached)
	}
}

// TestEveryWritingMethodIsTreatedAsAWrite covers a method nobody thought about.
//
// Defaulting an unrecognised method to read would mean anything new is treated as harmless. PATCH is the one that
// matters today, since FHIR uses it.
func TestEveryWritingMethodIsTreatedAsAWrite(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !isWrite(method) {
			t.Errorf("%s is not treated as a write", method)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if isWrite(method) {
			t.Errorf("%s is treated as a write", method)
		}
	}
	// An invented method, standing in for one added to HTTP later.
	if !isWrite("FROBNICATE") {
		t.Error("an unrecognised method is treated as a read; it must default to a write")
	}
}

// TestGuessingIsThrottled covers an unlimited guessing rate.
//
// One token reaches every patient record on this server, so the difference between throttled and not is the difference
// between a token being hard to guess and being guessable overnight.
func TestGuessingIsThrottled(t *testing.T) {
	s := staticServer(t)

	throttled := false
	for i := 0; i < authlimit.DefaultThreshold+3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/Patient", nil)
		req.Header.Set("Authorization", "Bearer wrong-guess-number-aaaa")
		req.RemoteAddr = "203.0.113.9:6000"

		rec := httptest.NewRecorder()
		s.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)

		if strings.Contains(rec.Body.String(), "too many failed attempts") {
			throttled = true
			break
		}
	}

	if !throttled {
		t.Errorf("guessing was never throttled after %d attempts", authlimit.DefaultThreshold+3)
	}
}

// The throttle's own behaviour - growth, expiry, clearing on success, and not overflowing - is tested in
// internal/authlimit, which owns it. Both this server and the console sign-in use that one implementation, so testing it
// twice would mean two places to update and one of them going stale.

// TestBearerAuthMapsRolesToWriteAccess covers the store-backed scheme.
func TestBearerAuthMapsRolesToWriteAccess(t *testing.T) {
	cases := []struct {
		role      string
		wantWrite bool
	}{
		{"viewer", false},
		{"editor", true},
		{"admin", true},
		{"platform", true},
	}

	for _, tc := range cases {
		role := tc.role
		auth := &BearerAuth{Lookup: func(context.Context, string) (string, string, error) {
			return "someone", role, nil
		}}

		req := httptest.NewRequest(http.MethodGet, "/Patient", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.RemoteAddr = "192.0.2.30:1000"

		caller, err := auth.Authenticate(req)
		if err != nil {
			t.Fatalf("role %s: %v", role, err)
		}
		if caller.Write != tc.wantWrite {
			t.Errorf("role %s got write=%v, want %v", role, caller.Write, tc.wantWrite)
		}
	}
}

// TestATokenIsNeverLogged covers the log itself.
//
// A log is copied into tickets and pasted into chat. Even a prefix narrows a search enough to matter, so none of it is
// recorded.
func TestATokenIsNeverLogged(t *testing.T) {
	var captured strings.Builder
	s := staticServer(t)
	s.Log = testLogger(&captured)

	secret := "a-very-distinctive-secret-value"
	_, _ = probe(t, s, http.MethodGet, "/Patient", "Bearer "+secret)

	if strings.Contains(captured.String(), secret) {
		t.Errorf("the token appeared in the log: %s", captured.String())
	}
	// Even the first few characters. A prefix is enough to confirm a guess.
	if strings.Contains(captured.String(), secret[:8]) {
		t.Errorf("a prefix of the token appeared in the log: %s", captured.String())
	}
}

// TestNoCredentialsIsDistinguishableFromABadOne covers the log's usefulness.
//
// "Somebody forgot the header" and "somebody is trying tokens" need different responses, and a single message for both
// makes the second invisible.
func TestNoCredentialsIsDistinguishableFromABadOne(t *testing.T) {
	auth := &StaticAuth{Tokens: map[string]string{testToken: "test"}}

	req := httptest.NewRequest(http.MethodGet, "/Patient", nil)
	req.RemoteAddr = "192.0.2.60:1000"
	if _, err := auth.Authenticate(req); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("a missing header gave %v, want ErrNoCredentials", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/Patient", nil)
	req2.Header.Set("Authorization", "Bearer definitely-not-the-token")
	req2.RemoteAddr = "192.0.2.61:1000"
	if _, err := auth.Authenticate(req2); errors.Is(err, ErrNoCredentials) {
		t.Error("a wrong token was reported as missing credentials")
	}
}

// TestForwardedForCannotDefeatTheThrottle covers a header anybody can set.
//
// Throttling by X-Forwarded-For would mean a guesser sets a different value on each request and is never throttled.
func TestForwardedForCannotDefeatTheThrottle(t *testing.T) {
	s := staticServer(t)

	throttled := false
	for i := 0; i < authlimit.DefaultThreshold+3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/Patient", nil)
		req.Header.Set("Authorization", "Bearer wrong-guess-aaaaaaaaaaa")
		// A different claimed origin every time, from the same real address.
		req.Header.Set("X-Forwarded-For", "10.0.0."+string(rune('1'+i)))
		req.RemoteAddr = "203.0.113.44:7000"

		rec := httptest.NewRecorder()
		s.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)

		if strings.Contains(rec.Body.String(), "too many failed attempts") {
			throttled = true
			break
		}
	}

	if !throttled {
		t.Error("changing X-Forwarded-For defeated the throttle, so a guesser is never limited")
	}
}

// testLogger writes log output into a buffer for inspection.
func testLogger(w *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestHandlerItselfEnforcesAuth goes through the real entry point.
//
// This test exists because the ones above did not catch the hole being put back.
//
// They call requireAuth directly, which proves the middleware works but never that Handler installs it. Restoring the
// original bug - returning the bare mux from Handler - left every one of them passing. The middleware was correct and
// unreachable, which is indistinguishable from having no middleware at all.
//
// So this one uses Handler(), the same thing cmd/perfuse mounts. It is the only test here that would have caught the
// hole as it actually existed.
func TestHandlerItselfEnforcesAuth(t *testing.T) {
	srv, handler := newTestServer(t)

	// The harness sets OpenAuth for the behaviour tests; replaced here so there is something to enforce.
	srv.Auth = &StaticAuth{Tokens: map[string]string{testToken: "test"}}
	handler = srv.Handler()

	// Every route, because wrapping the mux is what protects a route added later. Listing them individually is how
	// one gets forgotten, and this checks the wrapping rather than the list.
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/metadata"},
		{http.MethodGet, "/Patient"},
		{http.MethodPost, "/Patient"},
		{http.MethodGet, "/Patient/123"},
		{http.MethodPut, "/Patient/123"},
		{http.MethodDelete, "/Patient/123"},
		{http.MethodPost, "/Patient/$validate"},
		{http.MethodPost, "/"},
	}

	for _, route := range routes {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"resourceType":"Patient"}`))
		req.Header.Set("Content-Type", "application/fhir+json")
		req.RemoteAddr = "198.51.100.200:9000"

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without credentials, want 401", route.method, route.path, rec.Code)
		}
	}

	// And the same routes work with a token, so the test cannot pass by everything being broken.
	req := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.RemoteAddr = "198.51.100.201:9000"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("an authenticated /metadata returned %d, want 200; if this fails the test above proves nothing",
			rec.Code)
	}
}

// scopedAuth is an authenticator granting exactly the scopes given, for testing enforcement.
type scopedAuth struct{ scopes []string }

func (a scopedAuth) Authenticate(*http.Request) (*Caller, error) {
	// Write is true so that the per-resource scope check is what refuses a write, not the blanket read-only check.
	// Otherwise these tests would pass with no scope enforcement at all.
	return &Caller{Name: "smart-app", Scopes: a.scopes, Write: true}, nil
}

func (a scopedAuth) Describe() string { return "a scoped token" }

// TestSMARTScopesAreEnforced is the fix for a hole that existed for as long as scope parsing did.
//
// Caller.Allows was written, correct, and called from nowhere. So a SMART token granted patient/Observation.read could read
// and write every Patient on the server - and the scopes were recorded and reported in the audit log throughout, which
// makes it worse rather than better: an authorization server issuing a narrow grant, and an operator reading a log showing
// it, would both have been misled.
func TestSMARTScopesAreEnforced(t *testing.T) {
	serve := func(t *testing.T, scopes []string) http.Handler {
		t.Helper()
		srv, _ := newTestServer(t)
		srv.Auth = scopedAuth{scopes: scopes}

		return srv.Handler()
	}

	t.Run("a scope for one type does not reach another", func(t *testing.T) {
		h := serve(t, []string{"patient/Observation.read"})

		if rec := do(t, h, http.MethodGet, "/Patient", nil); rec.Code != http.StatusForbidden {
			t.Errorf("reading Patient with only patient/Observation.read got %d, want 403: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("a read scope does not permit a write", func(t *testing.T) {
		h := serve(t, []string{"patient/Patient.read"})

		rec := do(t, h, http.MethodPost, "/Patient", json.RawMessage(samplePatientJSON("MRN1")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("creating a Patient with only a read scope got %d, want 403: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("the granted scope works", func(t *testing.T) {
		h := serve(t, []string{"patient/Patient.read"})

		if rec := do(t, h, http.MethodGet, "/Patient", nil); rec.Code != http.StatusOK {
			t.Errorf("reading Patient with patient/Patient.read got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a write scope permits a write", func(t *testing.T) {
		h := serve(t, []string{"user/Patient.write", "user/Patient.read"})

		rec := do(t, h, http.MethodPost, "/Patient", json.RawMessage(samplePatientJSON("MRN2")))
		if rec.Code != http.StatusCreated {
			t.Errorf("creating a Patient with a write scope got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a wildcard covers every type", func(t *testing.T) {
		h := serve(t, []string{"user/*.read"})

		if rec := do(t, h, http.MethodGet, "/Observation", nil); rec.Code != http.StatusOK {
			t.Errorf("reading Observation with user/*.read got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("the version 2 letter form is honoured", func(t *testing.T) {
		// patient/Observation.rs in SMART v2 means read and search. An implementation handling only the v1
		// spelling would refuse a token a real authorization server had issued.
		//
		// Observation rather than Condition, which this server does not support - the first version of this
		// test read a 404 from the handler and reported it as a scope failure, which would have been a
		// confusing way to discover the scope check was fine.
		h := serve(t, []string{"patient/Observation.rs"})

		if rec := do(t, h, http.MethodGet, "/Observation", nil); rec.Code != http.StatusOK {
			t.Errorf("reading Observation with patient/Observation.rs got %d: %s", rec.Code, rec.Body.String())
		}
		rec := do(t, h, http.MethodPost, "/Observation", json.RawMessage(`{"resourceType":"Observation"}`))
		if rec.Code != http.StatusForbidden {
			t.Errorf("creating an Observation with only read and search letters got %d, want 403", rec.Code)
		}
	})

	t.Run("no FHIR scopes at all reaches nothing", func(t *testing.T) {
		// openid and fhirUser are legitimate scopes that grant no FHIR access. A caller holding only those
		// must not be treated as unscoped and therefore unrestricted.
		h := serve(t, []string{"openid", "fhirUser", "offline_access"})

		if rec := do(t, h, http.MethodGet, "/Patient", nil); rec.Code != http.StatusForbidden {
			t.Errorf("a token with no FHIR scopes read Patient anyway: %d", rec.Code)
		}
	})

	t.Run("the refusal does not enumerate what the token can reach", func(t *testing.T) {
		// A client cannot widen its own grant, so listing the scopes it holds helps somebody probing with a
		// stolen token more than it helps its owner.
		h := serve(t, []string{"patient/Observation.read", "patient/MedicationRequest.read"})

		rec := do(t, h, http.MethodGet, "/Patient", nil)
		if body := rec.Body.String(); strings.Contains(body, "MedicationRequest") {
			t.Errorf("the refusal lists other scopes the token holds:\n%s", body)
		}
	})

	t.Run("metadata is not treated as a resource type", func(t *testing.T) {
		// /metadata addresses no resource type, so requiring a scope for it would invent a grant no
		// authorization server issues and make the capability statement unreachable for every SMART app.
		h := serve(t, []string{"patient/Patient.read"})

		if rec := do(t, h, http.MethodGet, "/metadata", nil); rec.Code != http.StatusOK {
			t.Errorf("the capability statement was refused for want of a scope: %d %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("a token that predates SMART is unaffected", func(t *testing.T) {
		// AllScopes exists for exactly this: an existing Perfuse API token has no scopes and must keep
		// working, or upgrading to this build would break every running integration.
		srv, h := newTestServer(t)
		srv.Auth = OpenAuth{}

		if rec := do(t, h, http.MethodGet, "/Patient", nil); rec.Code != http.StatusOK {
			t.Errorf("an unscoped token was refused: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// TestABundleEntryIsScopeChecked closes the one route scopes did not cover.
//
// The middleware skips a POST to the base URL because a bundle addresses no single resource type. So a caller scoped to read
// Observations could post a transaction creating Patients, and every other route was checked - which made this the way
// through rather than an oversight somebody would trip over.
func TestABundleEntryIsScopeChecked(t *testing.T) {
	bundle := func(resourceType string) json.RawMessage {
		return json.RawMessage(`{
			"resourceType": "Bundle",
			"type": "transaction",
			"entry": [{
				"request": {"method": "POST", "url": "/` + resourceType + `"},
				"resource": ` + samplePatientJSON("MRNB1") + `
			}]
		}`)
	}

	t.Run("a read-only scope cannot post a bundle", func(t *testing.T) {
		srv, h := newTestServer(t)
		srv.Auth = scopedAuth{scopes: []string{"patient/Patient.read"}}

		rec := do(t, h, http.MethodPost, "/", bundle("Patient"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("a bundle creating a Patient with only a read scope got %d, want 403: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("a scope for another type cannot post this bundle", func(t *testing.T) {
		srv, h := newTestServer(t)
		srv.Auth = scopedAuth{scopes: []string{"user/Observation.write"}}

		rec := do(t, h, http.MethodPost, "/", bundle("Patient"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("a bundle creating a Patient with only an Observation write scope got %d, want 403: %s",
				rec.Code, rec.Body.String())
		}
		// It says which entry, because a bundle of two hundred entries with a flat refusal is unactionable.
		if !strings.Contains(rec.Body.String(), "entry 0") {
			t.Errorf("the refusal does not say which entry was refused:\n%s", rec.Body.String())
		}
	})

	t.Run("the right scope works", func(t *testing.T) {
		srv, h := newTestServer(t)
		srv.Auth = scopedAuth{scopes: []string{"user/Patient.write", "user/Patient.read"}}

		rec := do(t, h, http.MethodPost, "/", bundle("Patient"))
		if rec.Code != http.StatusOK {
			t.Errorf("a bundle with the right scope got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("nothing is written when one entry is refused", func(t *testing.T) {
		// The reason the check sits in the parse pass rather than the apply loop. A transaction refused halfway
		// through would leave earlier entries applied, from a bundle the caller was never allowed to send.
		srv, h := newTestServer(t)
		srv.Auth = scopedAuth{scopes: []string{"user/Patient.write", "user/Patient.read"}}

		two := json.RawMessage(`{
			"resourceType": "Bundle",
			"type": "transaction",
			"entry": [
				{"request": {"method": "POST", "url": "/Patient"}, "resource": ` +
			samplePatientJSON("MRNOK") + `},
				{"request": {"method": "POST", "url": "/Observation"}, "resource":
					{"resourceType": "Observation", "status": "final",
					 "code": {"coding": [{"system": "http://loinc.org", "code": "1234-5"}]}}}
			]
		}`)

		if rec := do(t, h, http.MethodPost, "/", two); rec.Code != http.StatusForbidden {
			t.Fatalf("a bundle with an out-of-scope entry got %d, want 403: %s", rec.Code, rec.Body.String())
		}

		// And the in-scope entry did not land.
		//
		// Counted from the bundle rather than looked for as a string in the body: a search response echoes its
		// own query in the self link, so the identifier appears whether or not anything matched. The first
		// version of this check searched for "MRNOK" and failed against correct code.
		srv.Auth = scopedAuth{scopes: []string{"user/Patient.read"}}
		rec := do(t, h, http.MethodGet, "/Patient", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("searching afterwards: %d %s", rec.Code, rec.Body.String())
		}
		if entries, ok := tree(t, rec)["entry"].([]any); ok && len(entries) != 0 {
			t.Errorf("%d resource(s) were written even though the bundle was refused", len(entries))
		}
	})
}
