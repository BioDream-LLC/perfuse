package udap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

// Verification against a document a real UDAP server produced.
//
// The document in testdata/fhirlabs-metadata.json was fetched from https://fhirlabs.net/fhir/r4/.well-known/udap, a public sandbox built
// on the udap-dotnet reference implementation. Its signing certificate is issued by the EMR Direct Test PKI, which is the UDAP test
// trust community, and that community publishes its certificate authorities - so the chain in testdata is the real one and the
// verification here is anchored rather than merely self-consistent.
//
// This is the test the SAML work did not have until far too late. A thousand lines of SAML tests passed, five of them about signature
// canonicalisation, while no real identity provider could authenticate anybody: the canonicaliser dropped namespace prefixes, so it
// could only ever read documents it had written itself. Nothing caught it because nothing outside the building had ever been asked.
//
// The fixture is deliberately committed rather than fetched at test time. A test that reaches the internet fails when the internet does,
// and a sandbox that goes away should not take the evidence with it.
//
// Refresh with:
//
//	curl -s https://fhirlabs.net/fhir/r4/.well-known/udap -o internal/udap/testdata/fhirlabs-metadata.json
//
// Note that the sandbox signs with a sixty second lifetime, so any refreshed fixture needs the frozen clock below moved with it.

// The instant the committed fixture was signed, near enough. Its iat is 1789798876 and its exp sixty seconds later.
var fixtureSignedAt = time.Unix(1789798880, 0)

func loadRealMetadata(t *testing.T) *Metadata {
	t.Helper()

	raw, err := os.ReadFile("testdata/fhirlabs-metadata.json")
	if err != nil {
		t.Fatal(err)
	}

	var md Metadata
	if err := json.Unmarshal(raw, &md); err != nil {
		t.Fatal(err)
	}

	return &md
}

// realAnchors builds the EMR Direct test community's pools from the committed certificates.
func realAnchors(t *testing.T) TrustAnchors {
	t.Helper()

	roots := x509.NewCertPool()

	rootPEM, err := os.ReadFile("testdata/emrdirect-test-root.pem")
	if err != nil {
		t.Fatal(err)
	}

	if !roots.AppendCertsFromPEM(rootPEM) {
		t.Fatal("the committed root certificate did not parse")
	}

	intermediates := x509.NewCertPool()

	subPEM, err := os.ReadFile("testdata/emrdirect-test-subca.pem")
	if err != nil {
		t.Fatal(err)
	}

	if !intermediates.AppendCertsFromPEM(subPEM) {
		t.Fatal("the committed sub-CA certificate did not parse")
	}

	return TrustAnchors{Roots: roots, Intermediates: intermediates}
}

func TestARealServersSignedMetadataVerifiesAgainstTheRealTrustCommunity(t *testing.T) {
	// The whole point of the package, with nothing relaxed: a real document, a real certificate chain, and a real community root.
	md := loadRealMetadata(t)

	claims, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: "https://fhirlabs.net/fhir/r4",
		Anchors: realAnchors(t),
		Now:     fixtureSignedAt,
	})
	if err != nil {
		t.Fatalf("a real UDAP server's signed metadata did not verify: %v", err)
	}

	if claims.Issuer != "https://fhirlabs.net/fhir/r4" {
		t.Errorf("iss is %q", claims.Issuer)
	}

	if claims.TokenEndpoint != "https://securedcontrols.net/connect/token" {
		t.Errorf("token endpoint is %q", claims.TokenEndpoint)
	}

	if claims.RegistrationEndpoint == "" {
		t.Error("no registration endpoint came back")
	}
}

func TestTheRealDocumentIsRefusedWithoutTrustAnchors(t *testing.T) {
	// The SAML defect, in the protocol that would have allowed it again.
	//
	// The SAML verifier preferred the certificate embedded in the response it was checking, so anybody could sign their own assertion
	// and be believed as an administrator. Every test passed. The equivalent here is trusting the x5c chain because it is present, and
	// the only defence is refusing to proceed when nothing has been configured to check it against.
	md := loadRealMetadata(t)

	_, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: "https://fhirlabs.net/fhir/r4",
		Now:     fixtureSignedAt,
	})
	if err == nil {
		t.Fatal("a document was accepted with no trust anchors configured, which means the certificate inside it was trusted")
	}

	// The specific message, not just any refusal mentioning trust anchors.
	//
	// The first version of this matched "trust anchor", and both refusals contain that phrase: the one for no anchors configured and the
	// one for a chain that does not verify. Disabling the check therefore did not fail this test - Go falls back to the system root store
	// when Roots is nil, the EMR Direct test authority is not in it, and the refusal arrived from the chain check instead. On a machine
	// where that authority had been installed, the check would have been gone and nothing would have said so.
	if !strings.Contains(err.Error(), "no trust anchors are configured") {
		t.Errorf("the refusal came from somewhere other than the missing-anchor check: %v", err)
	}
}

func TestTheRealDocumentIsRefusedByTheWrongCommunity(t *testing.T) {
	// A trust community that is not the one the server belongs to. This is what makes membership mean something: without it a
	// certificate from any authority at all would do, and the community would be decoration.
	//
	// The anchor is a throwaway authority generated here. The first version of this test put the leaf itself in the root pool and
	// expected a refusal, which was wrong about Go rather than about UDAP: a certificate present in a CertPool is pinned, and pinning a
	// certificate is a legitimate way to trust it. The mistake is worth recording because it is an easy one to make in the other
	// direction - a verifier handed a pool built from a document's own chain would trust everything in it.
	md := loadRealMetadata(t)

	unrelated := x509.NewCertPool()
	unrelated.AddCert(anUnrelatedAuthority(t))

	if _, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: "https://fhirlabs.net/fhir/r4",
		Anchors: TrustAnchors{Roots: unrelated},
		Now:     fixtureSignedAt,
	}); err == nil {
		t.Fatal("a document verified against a trust community that did not issue its certificate")
	}
}

// anUnrelatedAuthority makes a self-signed certificate authority that has issued nothing.
func anUnrelatedAuthority(t *testing.T) *x509.Certificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "An Authority Nobody Uses"},
		NotBefore:             fixtureSignedAt.Add(-time.Hour),
		NotAfter:              fixtureSignedAt.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return cert
}

func TestATamperedEndpointIsCaught(t *testing.T) {
	// The reason signing the metadata is worth anything.
	//
	// Verifying the signature and then reading the endpoints out of the unsigned part of the document would be an elaborate way of
	// trusting the transport. An attacker able to rewrite the response leaves signed_metadata alone, points token_endpoint at their own
	// server, and collects client assertions from everybody who checked the signature and then ignored what it covered.
	md := loadRealMetadata(t)
	md.TokenEndpoint = "https://attacker.example.invalid/connect/token"

	_, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: "https://fhirlabs.net/fhir/r4",
		Anchors: realAnchors(t),
		Now:     fixtureSignedAt,
	})
	if err == nil {
		t.Fatal("the token endpoint was changed and the document still verified, so the signature covers nothing that is used")
	}

	if !strings.Contains(err.Error(), "altered after signing") {
		t.Errorf("the refusal does not say the document was altered: %v", err)
	}
}

func TestADocumentForAnotherServerIsRefused(t *testing.T) {
	// Replay across servers. Without the check that iss equals the base URL it was fetched from, a valid document from any member of a
	// community would authenticate every other member's endpoints - so one compromised member could redirect everyone.
	md := loadRealMetadata(t)

	_, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: "https://someone-else.example.org/fhir/r4",
		Anchors: realAnchors(t),
		Now:     fixtureSignedAt,
	})
	if err == nil {
		t.Fatal("a document issued for one server verified for another")
	}
}

func TestAnExpiredDocumentIsRefused(t *testing.T) {
	// The sandbox signs with a sixty second window, so the committed fixture is expired for almost all of history. Checked explicitly
	// because a verifier that ignores exp would pass every other test in this file.
	md := loadRealMetadata(t)

	_, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: "https://fhirlabs.net/fhir/r4",
		Anchors: realAnchors(t),
		Now:     fixtureSignedAt.Add(2 * time.Hour),
	})
	if err == nil {
		t.Fatal("an expired document verified")
	}

	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("the refusal does not mention expiry: %v", err)
	}
}

func TestTheRealMetadataAdvertisesWhatTEFCANeeds(t *testing.T) {
	// A positive control on the fixture itself, and a check that the accessors read the document rather than guessing.
	//
	// If a future edit broke the field tags, every test above would still pass - a document that fails to parse verifies nothing and
	// reports nothing - so something has to assert the contents arrived.
	md := loadRealMetadata(t)

	if !md.SupportsClientCredentials() {
		t.Error("the fixture does not advertise client_credentials, which is the grant TEFCA business-to-business exchange uses")
	}

	if !md.SupportsB2BExtension() {
		t.Error("the fixture does not advertise hl7-b2b, which is where purpose of use travels")
	}

	if !md.SupportsDynamicRegistration() {
		t.Error("the fixture does not advertise udap_dcr")
	}

	if len(md.TokenEndpointAuthMethodsSupported) == 0 ||
		!containsFold(md.TokenEndpointAuthMethodsSupported, "private_key_jwt") {
		t.Errorf("token endpoint auth methods are %v, want private_key_jwt", md.TokenEndpointAuthMethodsSupported)
	}
}

func TestPlainHTTPIsRefusedBeforeAnythingIsFetched(t *testing.T) {
	// Every assurance in UDAP rests on certificates, and a metadata document fetched over plain HTTP can be replaced wholesale in
	// transit, signature and all, by an attacker who simply supplies their own. Refused at the URL rather than after the request.
	if _, err := MetadataURL("http://example.org/fhir/r4", ""); err == nil {
		t.Fatal("a plain HTTP base URL was accepted")
	}

	// Localhost is permitted, because that is where reference servers and tests live.
	if _, err := MetadataURL("http://127.0.0.1:8080/fhir/r4", ""); err != nil {
		t.Errorf("localhost over HTTP should be usable for testing: %v", err)
	}
}
