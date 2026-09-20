package webauthn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"strings"
	"testing"
	"time"
)

// A software attestation authority, so these tests verify against real certificates and real signatures.
//
// The same reasoning as the software authenticator used for the passkey work: a stub proves the plumbing and nothing about the
// checks, and every dangerous mistake in certificate verification is in the checks.

type attestationCA struct {
	rootKey  *ecdsa.PrivateKey
	rootCert *x509.Certificate
	pool     *x509.CertPool
}

func newAttestationCA(t *testing.T) *attestationCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Authenticator Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return &attestationCA{rootKey: key, rootCert: cert, pool: pool}
}

// leaf issues an attestation certificate, optionally carrying an AAGUID extension.
func (ca *attestationCA) leaf(t *testing.T, aaguid []byte) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Test Authenticator"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	if len(aaguid) == 16 {
		wrapped, err := asn1.Marshal(aaguid)
		if err != nil {
			t.Fatal(err)
		}
		tmpl.ExtraExtensions = []pkix.Extension{{Id: aaguidExtension, Value: wrapped}}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.rootCert, &key.PublicKey, ca.rootKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return cert, key
}

// packedStatement builds a packed attestation statement signed by key over authData||clientDataHash.
func packedStatement(t *testing.T, cert *x509.Certificate, key *ecdsa.PrivateKey, authData, hash []byte) CBORValue {
	t.Helper()

	signed := append(append([]byte{}, authData...), hash...)
	sum := sha256Of(signed)

	sig, err := ecdsa.SignASN1(rand.Reader, key, sum)
	if err != nil {
		t.Fatal(err)
	}

	return CBORValue{
		Kind: CBORMap,
		Map: []CBORPair{
			{Key: CBORValue{Kind: CBORText, Text: "alg"}, Value: CBORValue{Kind: CBORInt, Int: -7}},
			{Key: CBORValue{Kind: CBORText, Text: "sig"}, Value: CBORValue{Kind: CBORBytes, Bytes: sig}},
			{Key: CBORValue{Kind: CBORText, Text: "x5c"}, Value: CBORValue{
				Kind:  CBORArray,
				Array: []CBORValue{{Kind: CBORBytes, Bytes: cert.Raw}},
			}},
		},
	}
}

func sha256Of(b []byte) []byte { return clientDataHashOf(b) }

var (
	testAAGUID  = []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	otherAAGUID = []byte{0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00}
)

// TestAttestationIsNotVerifiedWithoutRoots pins the default, which is the position this replaced.
//
// The zero policy verifies nothing and claims nothing. Attestation answers "which device", never "is this legitimate" - the origin
// binding, the challenge and the key staying on the authenticator do that work regardless.
func TestAttestationIsNotVerifiedWithoutRoots(t *testing.T) {
	verified, err := verifyAttestation(AttestationPolicy{}, "none", CBORValue{}, nil, nil, testAAGUID)
	if err != nil {
		t.Fatalf("an unconfigured policy refused a registration: %v", err)
	}
	if verified {
		t.Error("an unconfigured policy reported the model as verified, which would make an unattested " +
			"credential look attested")
	}
}

// TestAnAllowListWithoutRootsIsRefusedAtStartup covers the configuration that reads as a control and is not one.
//
// The AAGUID inside a registration is self-asserted. Without a verified attestation a browser can claim any model it likes, so an
// allow-list on its own is theatre - and the operator who configured it would believe they had restricted their estate to two
// thousand approved keys.
func TestAnAllowListWithoutRootsIsRefusedAtStartup(t *testing.T) {
	policy := AttestationPolicy{AllowedAAGUIDs: map[string]bool{normaliseAAGUID(testAAGUID): true}}

	err := policy.Validate()
	if err == nil {
		t.Fatal("an allow-list with no roots was accepted, so an operator would believe they had restricted " +
			"which authenticators may enrol when they had not")
	}
	if !strings.Contains(err.Error(), "self-asserted") {
		t.Errorf("the refusal does not explain why the list would decide nothing: %v", err)
	}
}

// TestAVerifiedAttestationIsAccepted is the happy path against a real chain and a real signature.
func TestAVerifiedAttestationIsAccepted(t *testing.T) {
	ca := newAttestationCA(t)
	cert, key := ca.leaf(t, testAAGUID)

	authData := []byte("authenticator data, whatever it contains")
	hash := clientDataHashOf([]byte(`{"type":"webauthn.create"}`))

	verified, err := verifyAttestation(
		AttestationPolicy{Roots: ca.pool},
		"packed", packedStatement(t, cert, key, authData, hash), authData, hash, testAAGUID)
	if err != nil {
		t.Fatalf("a valid attestation was refused: %v", err)
	}
	if !verified {
		t.Error("a valid attestation did not report the model as verified")
	}
}

// TestAttestationIsRefusedWhenAnyCheckFails covers each way in.
func TestAttestationIsRefusedWhenAnyCheckFails(t *testing.T) {
	ca := newAttestationCA(t)
	authData := []byte("authenticator data")
	hash := clientDataHashOf([]byte(`{"type":"webauthn.create"}`))

	t.Run("no attestation at all", func(t *testing.T) {
		// The case that matters most. An operator who configured roots requires a known device; accepting a
		// registration that asserts nothing satisfies the letter of the configuration and none of its purpose.
		_, err := verifyAttestation(AttestationPolicy{Roots: ca.pool}, "none", CBORValue{},
			authData, hash, testAAGUID)
		if err == nil {
			t.Fatal("an authenticator providing no attestation was accepted under a policy requiring one")
		}

		// The message is asserted, not just the refusal, and a plant showed why: removing the "none" branch
		// still refuses the registration - it falls through to the unhandled-format check - but the message
		// becomes "attestation format \"none\" is not verified by this server", which sends somebody to look
		// for a configuration problem that does not exist. The right answer is that this authenticator did not
		// attest at all.
		if !strings.Contains(err.Error(), "provided no attestation") {
			t.Errorf("the refusal does not say the authenticator attested nothing, so it reads as a "+
				"server configuration problem: %v", err)
		}
	})

	t.Run("a format this server does not verify", func(t *testing.T) {
		// Refused rather than skipped: treating an unhandled format as acceptable would let the policy be
		// bypassed by presenting one.
		cert, key := ca.leaf(t, testAAGUID)
		_, err := verifyAttestation(AttestationPolicy{Roots: ca.pool}, "tpm",
			packedStatement(t, cert, key, authData, hash), authData, hash, testAAGUID)
		if err == nil {
			t.Error("an unverified attestation format was accepted, which is a way past the policy")
		}
	})

	t.Run("a certificate from another authority", func(t *testing.T) {
		other := newAttestationCA(t)
		cert, key := other.leaf(t, testAAGUID)

		_, err := verifyAttestation(AttestationPolicy{Roots: ca.pool}, "packed",
			packedStatement(t, cert, key, authData, hash), authData, hash, testAAGUID)
		if err == nil {
			t.Error("a certificate that does not chain to a configured root was accepted")
		}
	})

	t.Run("a signature over different data", func(t *testing.T) {
		cert, key := ca.leaf(t, testAAGUID)
		statement := packedStatement(t, cert, key, []byte("some other authenticator data"), hash)

		_, err := verifyAttestation(AttestationPolicy{Roots: ca.pool}, "packed",
			statement, authData, hash, testAAGUID)
		if err == nil {
			t.Error("a signature over data other than this registration was accepted, so the statement " +
				"could be replayed from another enrolment")
		}
	})

	t.Run("a model that does not match its certificate", func(t *testing.T) {
		// Without this the allow-list checks a self-asserted value: a device holding a genuine certificate for
		// one model could report another, and an operator who allowed only the second would have admitted the
		// first.
		cert, key := ca.leaf(t, testAAGUID)

		_, err := verifyAttestation(AttestationPolicy{Roots: ca.pool}, "packed",
			packedStatement(t, cert, key, authData, hash), authData, hash, otherAAGUID)
		if err == nil {
			t.Error("an authenticator reporting a model its certificate does not attest to was accepted")
		}
		if err != nil && !strings.Contains(err.Error(), "attests to") {
			t.Errorf("the refusal does not explain the mismatch: %v", err)
		}
	})

	t.Run("a model not on the allow-list", func(t *testing.T) {
		cert, key := ca.leaf(t, otherAAGUID)

		_, err := verifyAttestation(AttestationPolicy{
			Roots:          ca.pool,
			AllowedAAGUIDs: map[string]bool{normaliseAAGUID(testAAGUID): true},
		}, "packed", packedStatement(t, cert, key, authData, hash), authData, hash, otherAAGUID)

		if err == nil {
			t.Fatal("an authenticator model outside the allow-list was accepted")
		}
		// It names the model, so an operator can add it if they meant to.
		if !strings.Contains(err.Error(), formatAAGUID(otherAAGUID)) {
			t.Errorf("the refusal does not name the model: %v", err)
		}
	})

	t.Run("an expired certificate", func(t *testing.T) {
		cert, key := ca.leaf(t, testAAGUID)

		_, err := verifyAttestation(AttestationPolicy{
			Roots: ca.pool,
			// Two days on, by which time both the leaf and the root have expired.
			Now: func() time.Time { return time.Now().Add(48 * time.Hour) },
		}, "packed", packedStatement(t, cert, key, authData, hash), authData, hash, testAAGUID)

		if err == nil {
			t.Error("an expired attestation certificate was accepted")
		}
	})

	t.Run("a statement with no certificate", func(t *testing.T) {
		// Self attestation. Refused under a policy for the same reason "none" is: an operator requiring a known
		// device is not served by a device vouching for itself.
		statement := CBORValue{Kind: CBORMap, Map: []CBORPair{
			{Key: CBORValue{Kind: CBORText, Text: "sig"}, Value: CBORValue{Kind: CBORBytes, Bytes: []byte{1}}},
		}}

		_, err := verifyAttestation(AttestationPolicy{Roots: ca.pool}, "packed",
			statement, authData, hash, testAAGUID)
		if err == nil {
			t.Error("a self-attested registration was accepted under a policy requiring a known device")
		}
	})
}

// TestACertificateWithoutTheAAGUIDExtensionStillVerifies covers the deliberate leniency.
//
// A device that omits the extension is still presenting a chain that verifies, so the chain is honoured and the model is left
// unconfirmed rather than the registration refused for a missing field. Refusing would break enrolment for real hardware to enforce
// a check that adds nothing when there is no allow-list.
func TestACertificateWithoutTheAAGUIDExtensionStillVerifies(t *testing.T) {
	ca := newAttestationCA(t)
	cert, key := ca.leaf(t, nil)

	authData := []byte("authenticator data")
	hash := clientDataHashOf([]byte(`{"type":"webauthn.create"}`))

	verified, err := verifyAttestation(AttestationPolicy{Roots: ca.pool}, "packed",
		packedStatement(t, cert, key, authData, hash), authData, hash, testAAGUID)
	if err != nil {
		t.Fatalf("a chain without the AAGUID extension was refused: %v", err)
	}
	if !verified {
		t.Error("a verified chain did not report the model as verified")
	}
}
