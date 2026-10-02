package tefca

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Facilitated FHIR exchange.
//
// The security layer it stands on is verified in internal/udap against a live reference server, which refused a registration signed here
// with "Untrusted: Certificate is not a member of community" - meaning the document was parsed, its signature checked and its chain walked
// before membership was judged. These tests cover the layer above: that the exchange refuses to start misconfigured, that discovery works
// end to end against a real server, and that the patient-matching rules apply on the real path and not only on the stub.

// aParticipant is a valid TEFCAConfig with certificate paths pointing at a generated pair.
func aParticipant(t *testing.T, dir, clientURI string) TEFCAConfig {
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
		Subject:      pkix.Name{CommonName: clientURI},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// The subject alternative name URI is what binds this certificate to this client. A certificate without it is refused by a
		// conforming server before anything else is looked at.
		URIs:                  []*url.URL{parsed},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client.key")

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}

	return TEFCAConfig{
		OrganizationName:  "A Synthetic Hospital",
		OrganizationOID:   "2.16.840.1.113883.3.9999",
		QHINEndpoint:      "https://qhin.example.invalid",
		ParticipantType:   "provider",
		CertificatePath:   certPath,
		KeyPath:           keyPath,
		SupportedPurposes: []string{"treatment"},
	}
}

// anExchangeConfig is a valid configuration pointing at the public sandbox.
func anExchangeConfig(clientURI string) ExchangeConfig {
	return ExchangeConfig{
		PartnerFHIRBase: "https://fhirlabs.net/fhir/r4",
		ClientURI:       clientURI,
		TrustAnchorPath: "testdata/emrdirect-test-community.pem",
		ClientName:      "Perfuse",
		Contacts:        []string{"mailto:ops@perfuse.example.org"},
		Scope:           "system/Patient.read",
	}
}

func TestAnExchangeRefusesToStartWithoutTrustAnchors(t *testing.T) {
	// The refusal that matters most, and the one that mirrors a real defect here.
	//
	// The SAML verifier preferred the certificate embedded in the response it was checking, so anybody could sign their own assertion and
	// be believed as an administrator - and every test passed. The equivalent in UDAP is verifying a partner's signed metadata against the
	// certificate that arrived inside it, which proves only that one party made both. Refused at construction, so a participant finds out
	// when the server starts rather than when a clinician is waiting.
	dir := t.TempDir()
	clientURI := "https://perfuse.example.org/app"

	cfg := anExchangeConfig(clientURI)
	cfg.TrustAnchorPath = ""

	_, err := NewExchange(aParticipant(t, dir, clientURI), cfg, nil)
	if err == nil {
		t.Fatal("an exchange started with no trust anchors configured")
	}

	if !strings.Contains(err.Error(), "trust_anchor_path") {
		t.Errorf("the refusal does not name the missing setting: %v", err)
	}
}

func TestAnExchangeRefusesACertificateThatDoesNotNameTheClient(t *testing.T) {
	// Without this binding, any member of a trust community could register as any other and a certificate would prove that somebody is a
	// member rather than which member. A partner enforcing it answers with a generic rejection, so it is checked here where the message can
	// say what is actually wrong.
	dir := t.TempDir()

	participant := aParticipant(t, dir, "https://perfuse.example.org/app")

	cfg := anExchangeConfig("https://somebody-else.example.org/app")

	_, err := NewExchange(participant, cfg, nil)
	if err == nil {
		t.Fatal("an exchange started with a certificate that does not name its client URI")
	}

	if !strings.Contains(err.Error(), "does not name") {
		t.Errorf("the refusal does not explain the mismatch: %v", err)
	}
}

func TestAnExchangeRefusesContactsWithNoMailtoAddress(t *testing.T) {
	// The specification requires one, and the reason is operational rather than formal: when an exchange starts failing at three in the
	// morning, the other organisation needs somebody to tell.
	dir := t.TempDir()
	clientURI := "https://perfuse.example.org/app"

	cfg := anExchangeConfig(clientURI)
	cfg.Contacts = []string{"https://perfuse.example.org/support"}

	if _, err := NewExchange(aParticipant(t, dir, clientURI), cfg, nil); err == nil {
		t.Fatal("an exchange started with no mailto contact")
	}
}

func TestDiscoveryAgainstARealPartnerVerifiesEndToEnd(t *testing.T) {
	// The whole discovery path through the exchange rather than through the udap package directly: fetch, verify against the real trust
	// community, and check the partner offers what a TEFCA exchange needs.
	//
	// Skipped when the sandbox cannot be reached, because it is somebody else's machine.
	if os.Getenv("PERFUSE_SKIP_NETWORK") != "" {
		t.Skip("PERFUSE_SKIP_NETWORK is set")
	}

	dir := t.TempDir()
	clientURI := "https://perfuse.example.org/app"

	exchange, err := NewExchange(aParticipant(t, dir, clientURI), anExchangeConfig(clientURI), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	md, claims, err := exchange.Discover(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "fetching") || strings.Contains(err.Error(), "dial") {
			t.Skipf("could not reach the sandbox: %v", err)
		}

		t.Fatalf("discovery against a real partner failed: %v", err)
	}

	if claims.TokenEndpoint == "" || claims.RegistrationEndpoint == "" {
		t.Error("the verified claims carry no endpoints")
	}

	// The endpoints used later come from the verified claims, never the document body. Asserted here because a future change reading the
	// unsigned copy would make the signature decorative and no other test would notice.
	if claims.TokenEndpoint != md.TokenEndpoint {
		t.Errorf("verified token endpoint %q differs from the document's %q, which should have been refused",
			claims.TokenEndpoint, md.TokenEndpoint)
	}

	if !md.SupportsB2BExtension() {
		t.Error("the partner does not advertise hl7-b2b, so a purpose of use could not be stated")
	}
}

func TestDiscoveryRefusesAPartnerWhoseMetadataIsNotAnchored(t *testing.T) {
	// The negative control for the test above. Pointed at the same real server with a trust community that did not issue its certificate,
	// discovery must fail - otherwise the anchoring in the test above proves nothing.
	if os.Getenv("PERFUSE_SKIP_NETWORK") != "" {
		t.Skip("PERFUSE_SKIP_NETWORK is set")
	}

	dir := t.TempDir()
	clientURI := "https://perfuse.example.org/app"

	// The client's own self-signed certificate as the trust anchor: a real certificate, from no community, that issued nothing.
	participant := aParticipant(t, dir, clientURI)

	cfg := anExchangeConfig(clientURI)
	cfg.TrustAnchorPath = participant.CertificatePath

	exchange, err := NewExchange(participant, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := exchange.Discover(ctx); err == nil {
		t.Fatal("discovery succeeded against a trust community that did not issue the partner's certificate")
	}
}

func TestThePatientMatchingRulesApplyOnBothPaths(t *testing.T) {
	// The rules were written inline in the stub and are now shared, because a rule enforced on one route and not the other is worse than
	// no rule: it reads as protection everywhere and exists in one place.
	//
	// Matching on a name alone does not fail loudly when it is wrong. It returns somebody else's medical record, and the person reading it
	// cannot tell.
	for _, tc := range []struct {
		name string
		req  QueryRequest
	}{
		{"nothing to match on", QueryRequest{Purpose: "treatment"}},
		{"a name with no date of birth", QueryRequest{PatientName: "Hopper^Grace", Purpose: "treatment"}},
		{"an invalid purpose", QueryRequest{PatientID: "MRN1", Purpose: "curiosity"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateQuery(tc.req); err == nil {
				t.Error("accepted")
			}
		})
	}

	// And the positive control, without which the above would pass against validation that refused everything.
	if err := validateQuery(QueryRequest{PatientID: "MRN1", Purpose: "treatment"}); err != nil {
		t.Errorf("a query with an identifier and a valid purpose was refused: %v", err)
	}

	if err := validateQuery(QueryRequest{PatientName: "Hopper^Grace", DOB: "1906-12-09", Purpose: "treatment"}); err != nil {
		t.Errorf("a query with a name and a date of birth was refused: %v", err)
	}
}

func TestAnOrganisationIdentifierIsRenderedAsAURI(t *testing.T) {
	// The specification requires a URI, and an OID is not one until it is written as a urn:oid. A bare dotted number looks perfectly
	// reasonable and is refused by conforming servers without explanation.
	if got := organizationURI("2.16.840.1.113883.3.9999"); got != "urn:oid:2.16.840.1.113883.3.9999" {
		t.Errorf("organizationURI gave %q", got)
	}

	// Already a URI, left alone.
	if got := organizationURI("https://hospital.example.org"); got != "https://hospital.example.org" {
		t.Errorf("organizationURI rewrote a URI to %q", got)
	}
}

func TestAPurposeOfUseIsRenderedAsAURI(t *testing.T) {
	// Same reasoning: the preferred form is a code system URI with the code after a hash.
	got := purposeURI("treatment")
	if !strings.HasPrefix(got, "urn:oid:2.16.840.1.113883.5.8#") {
		t.Errorf("purposeURI gave %q, which is not the HL7 PurposeOfUse code system", got)
	}
}
