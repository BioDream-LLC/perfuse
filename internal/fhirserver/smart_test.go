package fhirserver

import (
	gocrypto "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A software authorization server, so these tests verify against a real signature rather than a stub.
//
// The same reasoning as the passkey work: a stubbed verifier proves the plumbing and nothing about the checks, and every
// dangerous mistake here lives in the checks. This one signs real RS256 tokens and publishes a real JWKS.
type fakeAS struct {
	key    *rsa.PrivateKey
	issuer string
	server *httptest.Server
}

func newFakeAS(t *testing.T) *fakeAS {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	as := &fakeAS{key: key}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/smart-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 as.issuer,
			"jwks_uri":               as.issuer + "/jwks",
			"authorization_endpoint": as.issuer + "/authorize",
			"token_endpoint":         as.issuer + "/token",
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{"kty": "RSA", "kid": "test-key", "alg": "RS256", "n": n, "e": e}},
		})
	})

	as.server = httptest.NewServer(mux)
	as.issuer = as.server.URL
	t.Cleanup(as.server.Close)

	return as
}

// token signs an access token with the claims given, after applying the defaults a real one carries.
func (as *fakeAS) token(t *testing.T, claims map[string]any) string {
	t.Helper()

	base := map[string]any{
		"iss":   as.issuer,
		"sub":   "app-42",
		"aud":   "https://perfuse.example.test/fhir",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"scope": "patient/Patient.read",
	}
	for k, v := range claims {
		if v == nil {
			delete(base, k)

			continue
		}
		base[k] = v
	}

	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}

	signing := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, as.key, gocrypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}

	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// smartServer builds a FHIR server accepting tokens from as.
func smartServer(t *testing.T, as *fakeAS) http.Handler {
	t.Helper()

	srv, _ := newTestServer(t)
	auth, err := NewSMARTAuth(SMARTConfig{
		Issuer:   as.issuer,
		Audience: "https://perfuse.example.test/fhir",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth = auth

	return srv.Handler()
}

// get makes a request with a bearer token.
func get(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

// TestASMARTTokenIsAcceptedAndItsScopesHonoured is the end-to-end path against a real signature.
func TestASMARTTokenIsAcceptedAndItsScopesHonoured(t *testing.T) {
	as := newFakeAS(t)
	h := smartServer(t, as)

	t.Run("a valid token reaches what it is scoped for", func(t *testing.T) {
		rec := get(t, h, "/Patient", as.token(t, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("a valid SMART token was refused: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("and nothing else", func(t *testing.T) {
		rec := get(t, h, "/Observation", as.token(t, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("a token scoped to Patient read Observation: %d", rec.Code)
		}
	})
}

// TestASMARTTokenIsRefusedWhenAnyCheckFails covers each refusal separately.
//
// Each case is a way in if the check is missing, and the audience one is the check most often left out: without it, any
// token from the same authorization server works here - including one a patient's own app obtained for a completely
// different service, and including one issued to a client never authorised to touch clinical data. The signature is
// perfectly valid in every one of those cases, which is what makes it easy to miss.
func TestASMARTTokenIsRefusedWhenAnyCheckFails(t *testing.T) {
	as := newFakeAS(t)
	h := smartServer(t, as)

	cases := []struct {
		name   string
		claims map[string]any
		why    string
		// status and says pin the answer, not just the refusal, where the answer is what somebody acts on.
		status int
		says   string
	}{
		{
			name:   "issued for another service",
			claims: map[string]any{"aud": "https://someone-elses-api.example.test"},
			why:    "the audience is not checked, so any token from this issuer works here",
		},
		{
			name:   "claiming another issuer",
			claims: map[string]any{"iss": "https://attacker.example.test"},
			why:    "the issuer is not checked",
		},
		{
			name:   "expired",
			claims: map[string]any{"exp": time.Now().Add(-2 * time.Hour).Unix()},
			why:    "expiry is not checked, so a leaked token works forever",
		},
		{
			name:   "carrying no scopes",
			claims: map[string]any{"scope": nil},
			why:    "a token with no scope claim is treated as unrestricted",
			// 401 and not 403, and this is the case that needs pinning rather than merely refusing.
			//
			// Scope enforcement already refuses a request from a caller with no scopes, so removing the
			// check in SMARTAuth changes nothing about who gets in - a plant proved exactly that, and the
			// first version of this test passed with the check deleted.
			//
			// What it changes is the answer. A 401 saying the authorization server issued a token with no
			// scope claim sends somebody to the identity provider; a 403 from an admitted caller looks
			// like an application bug and sends them to the app vendor. That is a day either way.
			status: http.StatusUnauthorized,
			says:   "no scopes",
		},
		{
			name:   "carrying an empty scope claim",
			claims: map[string]any{"scope": "   "},
			why:    "an empty scope claim is treated as unrestricted",
			status: http.StatusUnauthorized,
			says:   "no scopes",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := get(t, h, "/Patient", as.token(t, c.claims))
			if rec.Code == http.StatusOK {
				t.Fatalf("this token was accepted, which means %s", c.why)
			}
			if c.status != 0 && rec.Code != c.status {
				t.Errorf("the refusal is %d, want %d - the status is what tells somebody which system "+
					"to go and look at", rec.Code, c.status)
			}
			if c.says != "" && !strings.Contains(rec.Body.String(), c.says) {
				t.Errorf("the refusal does not say %q:\n%s", c.says, rec.Body.String())
			}
		})
	}

	t.Run("the audience that is enforced is the configured one", func(t *testing.T) {
		// Planting an empty audience made every valid token fail, which proves an audience is required and
		// not that the right one is checked. This configures a server expecting a different audience and
		// confirms the token minted for the first one is refused - so the value is what matters.
		srv, other := newTestServer(t)
		auth, err := NewSMARTAuth(SMARTConfig{
			Issuer:   as.issuer,
			Audience: "https://a-different-service.example.test/fhir",
		})
		if err != nil {
			t.Fatal(err)
		}
		srv.Auth = auth

		if rec := get(t, other, "/Patient", as.token(t, nil)); rec.Code == http.StatusOK {
			t.Error("a token whose audience names another service was accepted, so the configured " +
				"audience is not the one being enforced")
		}
	})

	t.Run("signed by the wrong key", func(t *testing.T) {
		// A different authorization server, publishing its own keys under its own issuer. The token names a kid
		// the configured issuer does not publish.
		other := newFakeAS(t)
		bad := other.token(t, map[string]any{"iss": as.issuer})

		if rec := get(t, h, "/Patient", bad); rec.Code == http.StatusOK {
			t.Error("a token signed by a key this issuer does not publish was accepted, so the signature " +
				"is not being checked against the configured issuer's keys")
		}
	})

	t.Run("no token at all", func(t *testing.T) {
		if rec := get(t, h, "/Patient", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("an anonymous request got %d, want 401", rec.Code)
		}
	})
}

// TestSMARTConfigurationRefusesAnUnsafeConfiguration covers what NewSMARTAuth will not accept.
//
// Both of these are values a deployment would not notice were missing, which is why they are refused at construction
// rather than defaulted.
func TestSMARTConfigurationRefusesAnUnsafeConfiguration(t *testing.T) {
	if _, err := NewSMARTAuth(SMARTConfig{Audience: "https://x.test/fhir"}); err == nil {
		t.Error("a SMART configuration with no issuer was accepted, so there is nothing to check a token against")
	}

	_, err := NewSMARTAuth(SMARTConfig{Issuer: "https://as.test"})
	if err == nil {
		t.Error("a SMART configuration with no audience was accepted, so any token from that issuer would work")
	}
	if err != nil && !strings.Contains(err.Error(), "different service") {
		t.Errorf("the refusal does not explain the risk: %v", err)
	}

	if _, err := NewSMARTAuth(SMARTConfig{Issuer: "as.test", Audience: "x"}); err == nil {
		t.Error("an issuer that is not a URL was accepted")
	}
}

// TestTheDiscoveryDocumentIsReadableWithoutATokenAndTellsTheTruth covers .well-known/smart-configuration.
func TestTheDiscoveryDocumentIsReadableWithoutATokenAndTellsTheTruth(t *testing.T) {
	as := newFakeAS(t)

	srv, _ := newTestServer(t)
	auth, err := NewSMARTAuth(SMARTConfig{Issuer: as.issuer, Audience: "https://perfuse.example.test/fhir"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth = auth
	srv.SMART = SMARTDiscovery{
		Issuer:                as.issuer,
		AuthorizationEndpoint: as.issuer + "/authorize",
		TokenEndpoint:         as.issuer + "/token",
		JWKSURI:               as.issuer + "/jwks",
	}
	h := srv.Handler()

	// Readable with no credential, which is the entire point: an app reads this before it has one.
	rec := get(t, h, "/.well-known/smart-configuration", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("the discovery document needed a token, which makes SMART impossible: %d %s",
			rec.Code, rec.Body.String())
	}

	var doc smartConfiguration
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	if doc.TokenEndpoint != as.issuer+"/token" {
		t.Errorf("the token endpoint is %q", doc.TokenEndpoint)
	}

	// It may claim the standalone patient context, which is now read and enforced. It must not claim the EHR launch
	// sequence, which Perfuse does not participate in, or an encounter context, which is not read.
	//
	// Checked as an allow-list rather than a pattern. The first version refused anything starting with "launch" or
	// "context-", which was right when none of it worked and would now quietly permit context-ehr-patient the moment
	// somebody added it to the list - a capability claiming Perfuse can obtain context it cannot.
	allowed := map[string]bool{
		"client-public":                 true,
		"client-confidential-symmetric": true,
		"context-standalone-patient":    true,
		// Added when the encounter claim started being read and enforced. The list stays an allow-list rather
		// than a prefix test, so context-ehr-patient still fails here - that one claims this server can take
		// part in an EHR launch sequence, which it cannot: it verifies a token somebody else issued.
		"context-standalone-encounter": true,
		"launch-standalone":            true,
		"permission-patient":           true,
		"permission-v1":                true,
		"permission-v2":                true,
		"sso-openid-connect":           true,
	}
	for _, c := range doc.Capabilities {
		if !allowed[c] {
			t.Errorf("the document claims %q, which this server does not implement", c)
		}
	}

	// And the capability statement must still need a token, since it lists every resource type held here.
	if rec := get(t, h, "/metadata", ""); rec.Code == http.StatusOK {
		t.Error("the capability statement is readable without a token; it lists every resource type and " +
			"search parameter, which tells an unauthenticated caller what is worth asking for")
	}
}

// TestTheDiscoveryDocumentIs404WhenSMARTIsNotConfigured covers the honest answer.
//
// An empty document listing no endpoints would leave an app unable to tell "this server does not do SMART" from "this
// server does SMART and is misconfigured", and those need different people to fix them.
func TestTheDiscoveryDocumentIs404WhenSMARTIsNotConfigured(t *testing.T) {
	srv, h := newTestServer(t)
	srv.Auth = OpenAuth{}

	if rec := get(t, h, "/.well-known/smart-configuration", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unconfigured server answered %d rather than 404", rec.Code)
	}
}
