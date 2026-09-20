package xmldsig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"
)

// Signing and verifying.
//
// The round trip is the least interesting test here. What matters is what happens when something is wrong, because
// every dangerous failure in a verifier is a failure of generosity: it accepts something it should not, reports the
// document as sound, and nothing downstream ever questions it.

const testSubject = "Dr Grace Hopper"

// signer builds a self-signed certificate and its key.
func signer(t *testing.T, notBefore, notAfter time.Time, useEC bool) (*x509.Certificate, any) {
	t.Helper()

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(4242),
		Subject:               pkix.Name{CommonName: testSubject, Organization: []string{"Example Hospital"}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	var pub, priv any
	if useEC {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, priv = &k.PublicKey, k
	} else {
		// 2048 rather than smaller: a test that signs with a key nobody would accept in production is not testing
		// the thing that will run.
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		pub, priv = &k.PublicKey, k
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, priv
}

const doc = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3" ID="d1">
  <title>Discharge Summary</title>
  <recordTarget><patientRole><patient><name>Ada Lovelace</name></patient></patientRole></recordTarget>
  <section><title>Medications</title><text>metformin 500 MG twice daily</text></section>
</ClinicalDocument>`

func sign(t *testing.T, source string, opts SignOptions) []byte {
	t.Helper()
	out, err := Sign([]byte(source), opts)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	return out
}

func TestASignedDocumentVerifies(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key:         key.(*rsa.PrivateKey),
		Certificate: cert,
		ReferenceID: "d1",
		SigningTime: now,
		Role:        "Attending physician responsible for this discharge",
	})

	// It must still be a parseable document, or nothing downstream can read it.
	if _, err := Canonicalise(signed, true, nil); err != nil {
		t.Fatalf("the signed document no longer parses: %v", err)
	}

	r, err := Verify(signed, VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}

	if !r.Signed {
		t.Fatal("the signature was not found")
	}
	if !r.DigestValid {
		t.Errorf("the digest did not verify: %v", r.Problems)
	}
	if !r.SignatureValid {
		t.Errorf("the signature did not verify: %v", r.Problems)
	}
	if !r.PropertiesValid {
		t.Errorf("the XAdES properties did not verify: %v", r.Problems)
	}
	if !r.ValidAtSigningTime {
		t.Errorf("the certificate was reported invalid at signing time: %v", r.Problems)
	}
	if !r.Sound() {
		t.Errorf("a correctly signed document was not reported as sound: %v", r.Problems)
	}

	// The circumstances have to survive, or XAdES has bought nothing over plain XMLDSig.
	if !r.SigningTime.Equal(now) {
		t.Errorf("the signing time came back as %s, want %s", r.SigningTime, now)
	}
	if !strings.Contains(r.ClaimedRole, "Attending physician") {
		t.Errorf("the claimed role did not survive: %q", r.ClaimedRole)
	}
	if !strings.Contains(r.Signer, testSubject) {
		t.Errorf("the signer is reported as %q", r.Signer)
	}
}

func TestAnECDSASignedDocumentVerifies(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), true)

	signed := sign(t, doc, SignOptions{
		Key:         key.(*ecdsa.PrivateKey),
		Certificate: cert,
		ReferenceID: "d1",
		SigningTime: now,
	})

	r, err := Verify(signed, VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !r.SignatureValid {
		t.Errorf("an ECDSA signature did not verify: %v", r.Problems)
	}
	if !r.DigestValid {
		t.Errorf("the digest did not verify: %v", r.Problems)
	}
}

// Changing one character of clinical content must break the digest.
//
// This is the entire point. A dose changed from 500 to 5000 is the difference between a therapeutic dose and a
// dangerous one, and the whole apparatus exists to make that detectable.
func TestChangingTheDoseBreaksTheSignature(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	})

	tampered := strings.Replace(string(signed), "metformin 500 MG", "metformin 5000 MG", 1)
	if tampered == string(signed) {
		t.Fatal("the test did not change anything")
	}

	r, err := Verify([]byte(tampered), VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.DigestValid {
		t.Fatal("a changed dose was not detected")
	}
	if r.Sound() {
		t.Fatal("a tampered document was reported as sound")
	}

	// The reason has to be legible. "Signature invalid" sends somebody to check their configuration.
	joined := strings.Join(r.Problems, " ")
	if !strings.Contains(joined, "changed since it was signed") {
		t.Errorf("the report does not explain what is wrong: %v", r.Problems)
	}
}

// Rewriting the claimed role must be detected.
//
// Otherwise the role is decoration: a document could be signed by a records clerk and read as signed by a consultant,
// with the cryptography reporting everything as fine.
func TestRewritingTheClaimedRoleIsDetected(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
		Role: "Medical records clerk",
	})

	tampered := strings.Replace(string(signed), "Medical records clerk", "Consultant cardiologist", 1)
	if tampered == string(signed) {
		t.Fatal("the test did not change anything")
	}

	r, err := Verify([]byte(tampered), VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.PropertiesValid {
		t.Fatal("a rewritten claimed role was not detected, so the role is decoration")
	}
	if r.Sound() {
		t.Fatal("a document with an altered role was reported as sound")
	}
}

// Rewriting the signing time must be detected, for the same reason.
func TestRewritingTheSigningTimeIsDetected(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	})

	tampered := strings.Replace(string(signed), now.Format(time.RFC3339), "2020-01-01T00:00:00Z", 1)
	if tampered == string(signed) {
		t.Fatal("the signing time was not in the document in the expected form")
	}

	r, err := Verify([]byte(tampered), VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.PropertiesValid {
		t.Fatal("a rewritten signing time was not detected")
	}
}

// A signature and a document that came from different signings must not verify against each other.
func TestASignatureFromAnotherDocumentDoesNotVerify(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	other := strings.Replace(doc, "Ada Lovelace", "Charles Babbage", 1)

	signedA := string(sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	}))
	signedB := string(sign(t, other, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	}))

	// The signature element from B grafted onto A.
	sigA := signedA[strings.Index(signedA, "<ds:Signature") : strings.Index(signedA, "</ds:Signature>")+len("</ds:Signature>")]
	sigB := signedB[strings.Index(signedB, "<ds:Signature") : strings.Index(signedB, "</ds:Signature>")+len("</ds:Signature>")]

	grafted := strings.Replace(signedA, sigA, sigB, 1)

	r, err := Verify([]byte(grafted), VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.DigestValid {
		t.Fatal("a signature from a different document verified")
	}
}

// An unsigned document must be reported as unsigned, not as invalid.
//
// Most documents are unsigned. Reporting them as failures makes the whole check useless, because everybody learns to
// ignore it.
func TestAnUnsignedDocumentIsNotAFailure(t *testing.T) {
	r, err := Verify([]byte(doc), VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Signed {
		t.Error("an unsigned document was reported as signed")
	}
	if len(r.Problems) != 0 {
		t.Errorf("an unsigned document produced problems: %v", r.Problems)
	}
	if len(r.Notes) == 0 {
		t.Error("nothing was said about the absence of a signature")
	}
}

// With no trust store, the report must say trust was not checked rather than that it failed.
//
// Conflating "not checked" with "checked and failed" is how a verifier reports every document as untrusted and trains
// everybody to disregard the field.
func TestWithoutATrustStoreTheReportSaysSo(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	})

	r, err := Verify(signed, VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.TrustChecked {
		t.Error("trust was reported as checked with no trust store supplied")
	}
	if r.Trusted {
		t.Error("an unchecked certificate was reported as trusted")
	}

	// And it must still be sound, because the cryptography is sound and that is a separate question.
	if !r.Sound() {
		t.Errorf("a sound signature was reported unsound only because no trust store was given: %v", r.Problems)
	}

	joined := strings.Join(r.Notes, " ")
	if !strings.Contains(joined, "self-signed") {
		t.Errorf("the report does not explain what an unchecked signature does not establish: %v", r.Notes)
	}
}

// With a trust store, an unknown certificate must fail trust while the cryptography still passes.
func TestAnUnknownCertificateFailsTrustButNotTheMaths(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	})

	// A trust store containing somebody else entirely.
	stranger, _ := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)
	roots := x509.NewCertPool()
	roots.AddCert(stranger)

	r, err := Verify(signed, VerifyOptions{Roots: roots, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !r.TrustChecked {
		t.Error("trust was not checked despite a trust store being given")
	}
	if r.Trusted {
		t.Error("a certificate that chains to nothing trusted was reported as trusted")
	}
	if !r.SignatureValid || !r.DigestValid {
		t.Errorf("the cryptography failed as well, which is a different fault: %v", r.Problems)
	}
	if r.Sound() {
		t.Error("an untrusted document was reported as sound with a trust store present")
	}
}

// A certificate in the trust store must verify.
func TestATrustedCertificateVerifies(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	})

	roots := x509.NewCertPool()
	roots.AddCert(cert)

	r, err := Verify(signed, VerifyOptions{Roots: roots, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Trusted {
		t.Errorf("a certificate in the trust store was not trusted: %v", r.Problems)
	}
	if !r.Sound() {
		t.Errorf("a trusted, correctly signed document was not sound: %v", r.Problems)
	}
}

// A document signed with a certificate that had already expired must be reported.
func TestSigningWithAnExpiredCertificateIsReported(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Valid last year, used today.
	cert, key := signer(t, now.AddDate(-2, 0, 0), now.AddDate(-1, 0, 0), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	})

	r, err := Verify(signed, VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.ValidAtSigningTime {
		t.Error("signing with an expired certificate was not reported")
	}
	if r.Sound() {
		t.Error("a document signed with an expired certificate was reported as sound")
	}

	// The digest and signature are still fine, and saying so is what makes the report useful: it distinguishes an
	// altered document from an administrative lapse.
	if !r.DigestValid || !r.SignatureValid {
		t.Errorf("the cryptography was reported broken as well: %v", r.Problems)
	}
}

// An archived document whose certificate has since expired must still verify.
//
// Time passing is not tampering. Reporting every old document as broken makes an archive unusable.
func TestAnArchivedDocumentWhoseCertificateHasSinceExpiredStillVerifies(t *testing.T) {
	signedAt := time.Date(2020, 6, 1, 9, 0, 0, 0, time.UTC)
	cert, key := signer(t, signedAt.AddDate(0, -1, 0), signedAt.AddDate(1, 0, 0), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: signedAt,
	})

	roots := x509.NewCertPool()
	roots.AddCert(cert)

	// Verified six years later.
	r, err := Verify(signed, VerifyOptions{Roots: roots, Now: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if !r.ValidAtSigningTime {
		t.Errorf("a document signed while its certificate was valid was reported otherwise: %v", r.Problems)
	}
	if !r.Trusted {
		t.Errorf("an archived document was reported untrusted because time has passed: %v", r.Problems)
	}
	if !r.Sound() {
		t.Errorf("an archived document was reported unsound: %v", r.Problems)
	}
}

// Substituting a different certificate carrying the same key must be detected.
//
// Without the certificate digest in the signed properties, a signature verifies against any certificate with the same
// public key - so somebody can replace a certificate naming a records clerk with a self-signed one naming a
// consultant, and every cryptographic check still passes.
func TestSubstitutingACertificateWithTheSameKeyIsDetected(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cert, key := signer(t, now.Add(-time.Hour), now.Add(24*time.Hour), false)

	signed := sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
		Role: "Medical records clerk",
	})

	// A second certificate for the same key, naming somebody more senior.
	rsaKey := key.(*rsa.PrivateKey)
	impostorTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(9999),
		Subject:               pkix.Name{CommonName: "Professor A Consultant"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, impostorTmpl, impostorTmpl, &rsaKey.PublicKey, rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	impostor, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	swapped := strings.Replace(string(signed),
		base64Of(cert.Raw), base64Of(impostor.Raw), 1)
	if swapped == string(signed) {
		t.Fatal("the certificate was not swapped")
	}

	r, err := Verify([]byte(swapped), VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}

	// The signature itself still verifies, because the key is the same. That is exactly why the certificate digest
	// is needed, and the report must catch it there.
	if r.PropertiesValid {
		t.Fatal("a substituted certificate was accepted, so the signature can be reattributed to anybody")
	}
	if r.Sound() {
		t.Fatal("a document with a substituted certificate was reported as sound")
	}

	joined := strings.Join(r.Problems, " ")
	if !strings.Contains(joined, "substituted") {
		t.Errorf("the report does not explain the substitution: %v", r.Problems)
	}
}

// Signing must refuse without a certificate.
func TestSigningRefusesWithoutACertificate(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Sign([]byte(doc), SignOptions{Key: k, ReferenceID: "d1"}); err == nil {
		t.Error("a signature was produced with no certificate, which nobody could attribute to anybody")
	}
}

// Signing must refuse when the referenced element does not exist.
func TestSigningRefusesAnUnknownReference(t *testing.T) {
	now := time.Now()
	cert, key := signer(t, now.Add(-time.Hour), now.Add(time.Hour), false)

	if _, err := Sign([]byte(doc), SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "nonexistent",
	}); err == nil {
		t.Error("a signature was produced over an element that does not exist")
	}
}

// The signature must go inside the root element, so the result is one document rather than two.
func TestTheSignatureIsPlacedInsideTheRootElement(t *testing.T) {
	now := time.Now()
	cert, key := signer(t, now.Add(-time.Hour), now.Add(time.Hour), false)

	signed := string(sign(t, doc, SignOptions{
		Key: key.(*rsa.PrivateKey), Certificate: cert, ReferenceID: "d1", SigningTime: now,
	}))

	if !strings.HasSuffix(strings.TrimSpace(signed), "</ClinicalDocument>") {
		t.Errorf("the signed document does not end with the root closing tag")
	}
	if strings.Index(signed, "<ds:Signature") > strings.LastIndex(signed, "</ClinicalDocument>") {
		t.Error("the signature was placed outside the root element, producing two top-level elements")
	}
}

// base64Of matches how the signature writes a certificate, so a textual replacement finds it.
func base64Of(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
