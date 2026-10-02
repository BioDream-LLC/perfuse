package udap

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The documents this package signs, checked claim by claim without a network.
//
// The sandbox probe proves a real server understands them, which is the evidence that matters, but it only runs when the sandbox is up and
// it only exercises the happy shape. These check the constraints the specification puts on the claims, because a conforming server refuses
// a whole document for one wrong claim and says very little about which one.

// decodeClaims pulls the claims out of a compact JWS without verifying it. Verification is tested elsewhere; this is about what went in.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}

	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}

	return claims
}

func decodeHeader(t *testing.T, token string) map[string]any {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	if err != nil {
		t.Fatal(err)
	}

	var header map[string]any
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}

	return header
}

func TestASoftwareStatementCarriesWhatTheSpecificationRequires(t *testing.T) {
	id := aSelfSignedIdentity(t, "https://perfuse.example.org/app")
	issued := time.Unix(1_800_000_000, 0)

	token, err := id.SoftwareStatement("https://as.example.org/register", RegistrationRequest{
		ClientName: "Perfuse",
		Contacts:   []string{"mailto:ops@perfuse.example.org"},
		Scope:      "system/Patient.read",
		Now:        issued,
	})
	if err != nil {
		t.Fatal(err)
	}

	claims := decodeClaims(t, token)

	// The audience is the registration endpoint. A statement is for one-time use with one server, and this is what stops one captured in
	// transit being presented to a different authorization server.
	if claims["aud"] != "https://as.example.org/register" {
		t.Errorf("aud is %v, want the registration endpoint", claims["aud"])
	}

	if claims["iss"] != "https://perfuse.example.org/app" || claims["sub"] != claims["iss"] {
		t.Errorf("iss/sub are %v and %v, and both must be the client URI", claims["iss"], claims["sub"])
	}

	// Five minutes exactly, which the specification requires rather than merely permits.
	iat, exp := claims["iat"].(float64), claims["exp"].(float64)
	if exp-iat != 300 {
		t.Errorf("the statement lives for %.0f seconds, want exactly 300", exp-iat)
	}

	if claims["token_endpoint_auth_method"] != "private_key_jwt" {
		t.Errorf("token_endpoint_auth_method is %v", claims["token_endpoint_auth_method"])
	}

	// Either client credentials or the authorization code grant, never both. A statement naming both is refused outright.
	grants, ok := claims["grant_types"].([]any)
	if !ok || len(grants) != 1 || grants[0] != "client_credentials" {
		t.Errorf("grant_types is %v, want exactly client_credentials", claims["grant_types"])
	}

	// redirect_uris must be absent for this grant. Present, it contradicts the grant type and is refused.
	if _, present := claims["redirect_uris"]; present {
		t.Error("redirect_uris is present on a client credentials statement, which the specification forbids")
	}

	header := decodeHeader(t, token)
	if header["alg"] != "RS256" {
		t.Errorf("alg is %v", header["alg"])
	}

	if x5c, ok := header["x5c"].([]any); !ok || len(x5c) == 0 {
		t.Error("no x5c chain in the header, so a server has no certificate to check")
	}
}

func TestASoftwareStatementNeedsAMailtoContact(t *testing.T) {
	// Refused here rather than at the far end. A server enforcing this answers with a generic rejection, and the operator then goes
	// looking at their certificate.
	id := aSelfSignedIdentity(t, "https://perfuse.example.org/app")

	if _, err := id.SoftwareStatement("https://as.example.org/register", RegistrationRequest{
		ClientName: "Perfuse",
		Contacts:   []string{"https://perfuse.example.org/contact-form"},
		Scope:      "system/Patient.read",
	}); err == nil {
		t.Fatal("a statement with no mailto contact was accepted")
	}
}

func TestAnIdentityWhoseCertificateDoesNotNameItIsRefused(t *testing.T) {
	// The binding UDAP rests on. Without it any member of a community could register as any other, and a certificate would prove only that
	// somebody is a member rather than which member.
	id := aSelfSignedIdentity(t, "https://perfuse.example.org/app")
	id.ClientURI = "https://someone-else.example.org/app"

	if err := id.Validate(); err == nil {
		t.Fatal("an identity claiming a URI its certificate does not carry was accepted")
	}
}

func TestAnAuthenticationTokenCarriesThePurposeOfUse(t *testing.T) {
	id := aSelfSignedIdentity(t, "https://perfuse.example.org/app")
	issued := time.Unix(1_800_000_000, 0)

	token, err := id.AuthenticationToken(TokenRequest{
		ClientID:      "assigned-by-the-server",
		TokenEndpoint: "https://as.example.org/token",
		B2B: &B2BExtension{
			OrganizationID:   "https://perfuse.example.org",
			OrganizationName: "A Hospital",
			PurposeOfUse:     []string{"urn:oid:2.16.840.1.113883.5.8#TREAT"},
		},
		Now: issued,
	})
	if err != nil {
		t.Fatal(err)
	}

	claims := decodeClaims(t, token)

	// iss and sub are the client_id here, not the client URI. That difference between this token and the software statement is easy to get
	// wrong and produces an invalid_client that names nothing.
	if claims["iss"] != "assigned-by-the-server" || claims["sub"] != claims["iss"] {
		t.Errorf("iss/sub are %v and %v, and both must be the assigned client_id", claims["iss"], claims["sub"])
	}

	// The audience is the token endpoint, which is what stops a captured assertion being replayed at a different server.
	if claims["aud"] != "https://as.example.org/token" {
		t.Errorf("aud is %v, want the token endpoint", claims["aud"])
	}

	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat > 300 {
		t.Errorf("the token lives for %.0f seconds, and the maximum is 300", exp-iat)
	}

	extensions, ok := claims["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("extensions is %T, want an object", claims["extensions"])
	}

	b2b, ok := extensions["hl7-b2b"].(map[string]any)
	if !ok {
		t.Fatalf("hl7-b2b is %T, want an object", extensions["hl7-b2b"])
	}

	if b2b["version"] != "1" {
		t.Errorf("hl7-b2b version is %v, want the string 1", b2b["version"])
	}

	if purposes, ok := b2b["purpose_of_use"].([]any); !ok || len(purposes) == 0 {
		t.Error("no purpose_of_use reached the token, so the other side has nothing to decide disclosure on")
	}

	// Absent members must be omitted rather than sent empty. An empty subject_name is a claim that somebody is asking and declines to say
	// who, which reads worse to a receiving organisation than no claim at all.
	if _, present := b2b["subject_name"]; present {
		t.Error("subject_name was sent empty rather than omitted")
	}
}

func TestATokenRequestWithoutAPurposeIsRefused(t *testing.T) {
	// An exchange with no stated purpose is not a TEFCA exchange. Refused before the request rather than after.
	id := aSelfSignedIdentity(t, "https://perfuse.example.org/app")

	for _, tc := range []struct {
		name string
		b2b  *B2BExtension
	}{
		{"nothing at all", nil},
		{"no organisation", &B2BExtension{PurposeOfUse: []string{"urn:oid:2.16.840.1.113883.5.8#TREAT"}}},
		{"no purpose", &B2BExtension{OrganizationID: "https://perfuse.example.org"}},
		{
			// A bare name where the specification requires a URI. It looks reasonable and conforming servers refuse it.
			"organisation is not a URI",
			&B2BExtension{OrganizationID: "A Hospital", PurposeOfUse: []string{"urn:oid:2.16.840.1.113883.5.8#TREAT"}},
		},
		{
			// A named person with neither an identifier nor a role: the other side is told who is asking and nothing about their authority
			// to ask.
			"a named subject with no role or identifier",
			&B2BExtension{
				OrganizationID: "https://perfuse.example.org",
				PurposeOfUse:   []string{"urn:oid:2.16.840.1.113883.5.8#TREAT"},
				SubjectName:    "Dr Grace Hopper",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := id.AuthenticationToken(TokenRequest{
				ClientID:      "assigned",
				TokenEndpoint: "https://as.example.org/token",
				B2B:           tc.b2b,
			}); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestAnAccessTokenIsTreatedAsStaleWhenItSaysNothingAboutItsLifetime(t *testing.T) {
	// A token with no expires_in is treated as already stale rather than eternal. Cached for ever it would be used long after it was
	// revoked, and a revoked credential that still works is the failure mode this repository keeps finding.
	now := time.Unix(1_800_000_000, 0)

	if !(&AccessToken{AccessToken: "x", Obtained: now}).Expired(now) {
		t.Error("a token with no stated lifetime was treated as valid")
	}

	fresh := &AccessToken{AccessToken: "x", ExpiresIn: 300, Obtained: now}
	if fresh.Expired(now) {
		t.Error("a token issued a moment ago was treated as expired")
	}

	// And the margin, so a request is not started with a token about to die mid-flight.
	if !fresh.Expired(now.Add(290 * time.Second)) {
		t.Error("a token ten seconds from expiry was treated as usable")
	}
}
