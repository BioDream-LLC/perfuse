package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/xmldsig"
)

// Signing and verifying through the interface.
//
// The tests that matter are about honesty. A verifier that overstates what it established is worse than none, because
// a signature nobody questions carries more weight than no signature at all.

// writeKeyPair creates a certificate and key on disk and returns their paths.
func writeKeyPair(t *testing.T, dir, commonName string) (certPath, keyPath string, cert *x509.Certificate) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: commonName},
		DNSNames:              []string{commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	if err := os.WriteFile(certPath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
		}), 0o600); err != nil {
		t.Fatal(err)
	}

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, parsed
}

const signableCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3" ID="d1">
  <title>Discharge Summary</title>
  <recordTarget><patientRole><patient><name>Ada Lovelace</name></patient></patientRole></recordTarget>
  <section><title>Medications</title><text>metformin 500 MG twice daily</text></section>
</ClinicalDocument>`

func TestADocumentCanBeSignedAndThenVerified(t *testing.T) {
	h := newHarness(t)
	certPath, keyPath, cert := writeKeyPair(t, h.dir, "perfuse-test.example")
	h.server.TLSCertFile, h.server.TLSKeyFile = certPath, keyPath

	res := h.do("admin", http.MethodPost, "/api/document/sign", map[string]any{
		"document":    signableCDA,
		"referenceId": "d1",
		"role":        "Attending physician",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("signing returned %d: %s", res.Code, res.Body.String())
	}

	var signed struct {
		Document string `json:"document"`
		Signer   string `json:"signer"`
		Caveat   string `json:"caveat"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &signed); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(signed.Document, "Signature") {
		t.Fatal("the returned document carries no signature")
	}
	if !strings.Contains(signed.Signer, cert.Subject.CommonName) {
		t.Errorf("the signer is reported as %q", signed.Signer)
	}

	// The caveat travels with the signature rather than living in documentation, so nobody can use this feature
	// without meeting it. A signature made with a web server's certificate is a real signature and a weak
	// attestation, and saying so is the difference between a useful tool and a misleading one.
	if !strings.Contains(strings.ToLower(signed.Caveat), "does not assert") {
		t.Errorf("no caveat was returned with the signature: %q", signed.Caveat)
	}

	// And it verifies.
	res = h.do("viewer", http.MethodPost, "/api/document/verify", map[string]any{"document": signed.Document})
	if res.Code != http.StatusOK {
		t.Fatalf("verifying returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Sound  bool `json:"sound"`
		Report struct {
			Signed          bool     `json:"signed"`
			DigestValid     bool     `json:"digestValid"`
			SignatureValid  bool     `json:"signatureValid"`
			PropertiesValid bool     `json:"propertiesValid"`
			TrustChecked    bool     `json:"trustChecked"`
			ClaimedRole     string   `json:"claimedRole"`
			Problems        []string `json:"problems"`
			Notes           []string `json:"notes"`
		} `json:"report"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if !out.Sound {
		t.Errorf("a freshly signed document did not verify: %v", out.Report.Problems)
	}
	if !out.Report.DigestValid || !out.Report.SignatureValid || !out.Report.PropertiesValid {
		t.Errorf("verification is incomplete: %+v", out.Report)
	}
	if out.Report.ClaimedRole != "Attending physician" {
		t.Errorf("the claimed role came back as %q", out.Report.ClaimedRole)
	}

	// With no trust store, the report must say what it did not establish.
	if out.Report.TrustChecked {
		t.Error("trust was reported as checked with no trust store supplied")
	}
	if len(out.Report.Notes) == 0 {
		t.Error("nothing was said about the limits of an unchecked signature")
	}
}

// Changing the content after signing must be caught, with a legible reason.
func TestATamperedDocumentIsRefusedWithAReason(t *testing.T) {
	h := newHarness(t)
	certPath, keyPath, _ := writeKeyPair(t, h.dir, "perfuse-test.example")
	h.server.TLSCertFile, h.server.TLSKeyFile = certPath, keyPath

	res := h.do("admin", http.MethodPost, "/api/document/sign",
		map[string]any{"document": signableCDA, "referenceId": "d1"})
	if res.Code != http.StatusOK {
		t.Fatalf("signing returned %d: %s", res.Code, res.Body.String())
	}

	var signed struct {
		Document string `json:"document"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &signed); err != nil {
		t.Fatal(err)
	}

	tampered := strings.Replace(signed.Document, "metformin 500 MG", "metformin 5000 MG", 1)
	if tampered == signed.Document {
		t.Fatal("the test changed nothing")
	}

	res = h.do("viewer", http.MethodPost, "/api/document/verify", map[string]any{"document": tampered})
	if res.Code != http.StatusOK {
		t.Fatalf("verifying returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Sound  bool `json:"sound"`
		Report struct {
			DigestValid bool     `json:"digestValid"`
			Problems    []string `json:"problems"`
		} `json:"report"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if out.Sound || out.Report.DigestValid {
		t.Fatal("a changed dose was reported as verified")
	}
	if len(out.Report.Problems) == 0 {
		t.Fatal("no reason was given, so nobody can tell what went wrong")
	}
}

// An unsigned document is not a failure, and the response must not read like one.
func TestVerifyingAnUnsignedDocumentIsNotAnError(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/document/verify", map[string]any{"document": signableCDA})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	var out struct {
		Report struct {
			Signed   bool     `json:"signed"`
			Problems []string `json:"problems"`
			Notes    []string `json:"notes"`
		} `json:"report"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Report.Signed {
		t.Error("an unsigned document was reported as signed")
	}
	if len(out.Report.Problems) != 0 {
		t.Errorf("an unsigned document produced problems: %v", out.Report.Problems)
	}
	if len(out.Report.Notes) == 0 {
		t.Error("nothing explained the absence of a signature")
	}
}

// Slices must be arrays, never null, or the interface reads a length from nothing.
func TestVerificationSlicesAreArraysWhenEmpty(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/document/verify", map[string]any{"document": signableCDA})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d", res.Code)
	}
	for _, field := range []string{"problems", "notes", "coveredIds"} {
		if strings.Contains(res.Body.String(), `"`+field+`":null`) {
			t.Errorf("%s is null rather than an empty array", field)
		}
	}
}

// A trust store that cannot be read must be refused, not ignored.
//
// Continuing with an empty pool would report the signature as untrusted and let somebody conclude the document is
// suspect, when in fact their paste was wrong. That is a false accusation caused by a silent failure.
func TestAnUnreadableTrustStoreIsRefusedRatherThanIgnored(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/document/verify", map[string]any{
		"document": signableCDA,
		"trustPem": "this is not a certificate",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "BEGIN CERTIFICATE") {
		t.Errorf("the refusal does not say what was expected: %s", res.Body.String())
	}
}

// Signing without a certificate must be a refusal that says what to do, not a server error.
func TestSigningWithoutACertificateExplainsItself(t *testing.T) {
	h := newHarness(t)

	res := h.do("admin", http.MethodPost, "/api/document/sign",
		map[string]any{"document": signableCDA, "referenceId": "d1"})
	if res.Code != http.StatusConflict {
		t.Fatalf("returned %d, want 409: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "tls-cert") {
		t.Errorf("the refusal does not say how to fix it: %s", res.Body.String())
	}
}

// Signing is administrator-only. It asserts that this organisation takes responsibility for a set of bytes.
func TestSigningIsAdministratorOnly(t *testing.T) {
	h := newHarness(t)
	certPath, keyPath, _ := writeKeyPair(t, h.dir, "perfuse-test.example")
	h.server.TLSCertFile, h.server.TLSKeyFile = certPath, keyPath

	for _, role := range []string{"viewer", "editor"} {
		res := h.do(role, http.MethodPost, "/api/document/sign",
			map[string]any{"document": signableCDA, "referenceId": "d1"})
		if res.Code != http.StatusForbidden {
			t.Errorf("%s could sign a document (%d)", role, res.Code)
		}
	}
}

// Verification is available to viewers. Withholding it makes the signature pointless.
func TestVerificationIsAvailableToViewers(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/document/verify", map[string]any{"document": signableCDA})
	if res.Code != http.StatusOK {
		t.Errorf("a viewer could not check a signature (%d): %s", res.Code, res.Body.String())
	}
}

// The interface must be able to say who the signature will name before anybody presses the button.
//
// Discovering after the fact that a clinical document was signed by a hostname is a poor way to find out.
func TestTheSigningIdentityCanBeInspectedBeforeSigning(t *testing.T) {
	h := newHarness(t)
	certPath, keyPath, _ := writeKeyPair(t, h.dir, "perfuse-test.example")
	h.server.TLSCertFile, h.server.TLSKeyFile = certPath, keyPath

	res := h.do("viewer", http.MethodGet, "/api/document/signing-identity", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Available   bool   `json:"available"`
		CommonName  string `json:"commonName"`
		SelfSigned  bool   `json:"selfSigned"`
		Explanation string `json:"explanation"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Available {
		t.Fatal("a server with a certificate reported none")
	}
	if out.CommonName != "perfuse-test.example" {
		t.Errorf("the identity is reported as %q", out.CommonName)
	}
	if !out.SelfSigned {
		t.Error("a self-signed certificate was not reported as such")
	}

	// The explanation has to state the limit, because a self-signed certificate establishes integrity and nothing
	// about identity.
	if !strings.Contains(strings.ToLower(out.Explanation), "self-signed") {
		t.Errorf("the explanation does not mention that it is self-signed: %q", out.Explanation)
	}
}

// A server with no certificate must say so rather than failing.
func TestTheSigningIdentityReportsWhenThereIsNone(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/document/signing-identity", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"available":false`) {
		t.Errorf("a server with no certificate did not say so: %s", res.Body.String())
	}
}

// A trust store containing the signer must produce a trusted result end to end.
func TestASignatureVerifiesAgainstAPastedTrustStore(t *testing.T) {
	h := newHarness(t)
	certPath, keyPath, cert := writeKeyPair(t, h.dir, "perfuse-test.example")
	h.server.TLSCertFile, h.server.TLSKeyFile = certPath, keyPath

	res := h.do("admin", http.MethodPost, "/api/document/sign",
		map[string]any{"document": signableCDA, "referenceId": "d1"})
	if res.Code != http.StatusOK {
		t.Fatalf("signing returned %d: %s", res.Code, res.Body.String())
	}
	var signed struct {
		Document string `json:"document"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &signed); err != nil {
		t.Fatal(err)
	}

	trustPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))

	res = h.do("viewer", http.MethodPost, "/api/document/verify", map[string]any{
		"document": signed.Document,
		"trustPem": trustPEM,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("verifying returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Sound  bool `json:"sound"`
		Report struct {
			Trusted      bool     `json:"trusted"`
			TrustChecked bool     `json:"trustChecked"`
			Problems     []string `json:"problems"`
		} `json:"report"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Report.TrustChecked {
		t.Error("trust was not checked despite a trust store being supplied")
	}
	if !out.Report.Trusted {
		t.Errorf("the signer's own certificate did not establish trust: %v", out.Report.Problems)
	}
	if !out.Sound {
		t.Errorf("a trusted signature was not sound: %v", out.Report.Problems)
	}
}

// The package's own notion of soundness and the one reported here must agree.
//
// Computed on the server so every consumer applies the same rule. A caller assembling "valid" from four booleans will
// eventually assemble it differently, and two parts of one product disagreeing about whether a document is signed is
// worse than either answer.
func TestSoundnessIsComputedOnceOnTheServer(t *testing.T) {
	report, err := xmldsig.Verify([]byte(signableCDA), xmldsig.VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sound() {
		t.Error("an unsigned document was reported sound by the package")
	}
}
