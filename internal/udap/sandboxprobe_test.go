package udap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// What a real authorization server makes of documents this package produces.
//
// This is the closest thing available to the Keycloak test that found the SAML defect, and it is worth being precise about what it can and
// cannot show. The certificate below is self-signed, so no trust community vouches for it, and the sandbox will refuse it. That refusal is
// the measurement.
//
// A server that refuses with "certificate not trusted", "untrusted issuer" or similar has read the software statement, verified its
// signature, walked the chain and reached a decision about membership. Everything this package builds was therefore well formed enough to
// be understood. A server that refuses with a parse error, a missing claim or an invalid signature is telling us something is wrong here,
// and that is the outcome worth catching before somebody spends a day on it during onboarding.
//
// It is the difference between being turned away at the door and being told the letter is illegible.
//
// Skipped when the sandbox cannot be reached.

const sandboxRegistration = "https://securedcontrols.net/connect/register"

// aSelfSignedIdentity makes a usable identity that no community has endorsed.
func aSelfSignedIdentity(t *testing.T, clientURI string) *Identity {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := url.Parse(clientURI)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: clientURI, Organization: []string{"Perfuse (self asserted, not a community member)"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// The subject alternative name URI is the part that matters. UDAP binds the client's identity to this entry, and a certificate
		// without it is refused by a conforming server before anything else is looked at.
		URIs:                  []*url.URL{parsed},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return &Identity{ClientURI: clientURI, PrivateKey: key, Chain: []*x509.Certificate{cert}}
}

func TestARealAuthorizationServerUnderstandsOurRegistration(t *testing.T) {
	if os.Getenv("PERFUSE_SKIP_NETWORK") != "" {
		t.Skip("PERFUSE_SKIP_NETWORK is set")
	}

	id := aSelfSignedIdentity(t, "https://perfuse.example.org/udap-conformance-probe")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := id.Register(ctx, &http.Client{Timeout: 20 * time.Second}, sandboxRegistration, RegistrationRequest{
		ClientName: "Perfuse conformance probe",
		Contacts:   []string{"mailto:nobody@perfuse.example.org"},
		Scope:      "system/Patient.read system/DocumentReference.read",
	})
	if err == nil {
		// Astonishing rather than good. A server that registers an unvouched-for client has no trust community at all, and that would be
		// a finding about the sandbox worth recording loudly.
		t.Fatal("the sandbox registered a self-signed client, which means its trust community checks nothing")
	}

	message := err.Error()

	// Recorded rather than asserted on tightly. The exact wording is the sandbox's to choose and will change; what matters is which kind
	// of refusal it is, and a human reading the test output can tell immediately.
	t.Logf("the sandbox refused a self-signed registration with: %s", message)

	// What must not happen is a complaint about the shape of the document rather than about the certificate.
	if looksLikeOurFault(message) {
		t.Errorf("the sandbox objected to the document rather than to the certificate, so something here is malformed: %s", message)
	}
}

// looksLikeOurFault reports whether a refusal blames the document rather than the certificate.
//
// Kept deliberately narrow. A refusal naming the certificate, the trust community or the issuer is the expected outcome and proves the
// document was read; a refusal naming the software statement, a claim or the signature means this package produced something a conforming
// server could not use.
func looksLikeOurFault(message string) bool {
	lower := strings.ToLower(message)

	// Anything about trust is the expected answer, and it takes precedence: some servers report an untrusted certificate inside an
	// invalid_software_statement error code, and reading only the code would call a correct document malformed.
	for _, trust := range []string{"trust", "anchor", "certificate", "issuer", "community", "not registered", "unknown client"} {
		if strings.Contains(lower, trust) {
			return false
		}
	}

	for _, ours := range []string{"parse", "missing", "malformed", "invalid signature", "invalid_request", "claim"} {
		if strings.Contains(lower, ours) {
			return true
		}
	}

	return false
}

func TestTheSandboxRefusesATokenRequestFromAnUnregisteredClient(t *testing.T) {
	// The other half of the flow, exercised the same way. Without a registration there is no client_id, so this uses one the server has
	// never seen - which must be refused with an OAuth error rather than with a parse failure.
	//
	// An invalid_client answer is the proof: it means the form was decoded, the assertion was read as a JWT, and the server got as far as
	// looking the client up and not finding it.
	if os.Getenv("PERFUSE_SKIP_NETWORK") != "" {
		t.Skip("PERFUSE_SKIP_NETWORK is set")
	}

	id := aSelfSignedIdentity(t, "https://perfuse.example.org/udap-conformance-probe")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := id.RequestToken(ctx, &http.Client{Timeout: 20 * time.Second}, TokenRequest{
		ClientID:      "a-client-id-this-server-has-never-issued",
		TokenEndpoint: "https://securedcontrols.net/connect/token",
		Scope:         "system/Patient.read",
		B2B: &B2BExtension{
			OrganizationID:   "https://perfuse.example.org",
			OrganizationName: "Perfuse conformance probe",
			PurposeOfUse:     []string{"urn:oid:2.16.840.1.113883.5.8#TREAT"},
		},
	})
	if err == nil {
		t.Fatal("the sandbox issued an access token to a client it has never registered")
	}

	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("the refusal was not an OAuth error response, which suggests the request never got that far: %v", err)
	}

	t.Logf("the sandbox refused an unregistered client with %q (%s)", tokenErr.Code, tokenErr.Description)

	// invalid_client is the expected code. Anything about the request itself means the form or the assertion was wrong.
	if tokenErr.Code == "invalid_request" {
		t.Errorf("the sandbox called the request itself invalid, so the token request this package builds is malformed: %s", tokenErr.Raw)
	}
}
