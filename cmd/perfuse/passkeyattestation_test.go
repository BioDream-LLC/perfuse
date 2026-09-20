package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestThePasskeyAttestationPolicyRefusesWhatCannotMeanWhatItSays covers each way the configuration misleads.
//
// Every case here is one where the operator would have believed they had a control and would not have had one. That is the whole
// reason these are refused at startup rather than defaulted: a security setting that silently does nothing is worse than its
// absence, because its absence is visible.
func TestThePasskeyAttestationPolicyRefusesWhatCannotMeanWhatItSays(t *testing.T) {
	t.Run("nothing configured verifies nothing", func(t *testing.T) {
		policy, err := passkeyAttestationPolicy("", "")
		if err != nil {
			t.Fatal(err)
		}
		if policy.Configured() {
			t.Error("an unconfigured policy reports itself as verifying attestation")
		}
	})

	t.Run("a model list without roots is refused", func(t *testing.T) {
		// The AAGUID in a registration is self-asserted, so a list on its own checks a claim rather than a
		// certificate. An operator who set this would believe they had restricted their estate to approved keys.
		_, err := passkeyAttestationPolicy("", "01020304-0506-0708-090a-0b0c0d0e0f10")
		if err == nil {
			t.Fatal("an allow-list with no roots was accepted")
		}
		if !strings.Contains(err.Error(), "self-asserted") {
			t.Errorf("the refusal does not explain why the list decides nothing: %v", err)
		}
	})

	t.Run("a roots file with no certificates is refused", func(t *testing.T) {
		// Left as an empty pool, Configured would report false - so attestation would not be verified at all on
		// a server whose command line says it is. That is the worst available outcome for a control like this.
		dir := t.TempDir()
		path := filepath.Join(dir, "roots.pem")
		if err := os.WriteFile(path, []byte("this is not a certificate\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := passkeyAttestationPolicy(path, "")
		if err == nil {
			t.Fatal("a roots file containing no certificates was accepted, so attestation would silently " +
				"not be verified")
		}
		if !strings.Contains(err.Error(), "not be verified at all") {
			t.Errorf("the refusal does not say what would happen: %v", err)
		}
	})

	t.Run("a missing roots file is refused", func(t *testing.T) {
		if _, err := passkeyAttestationPolicy(filepath.Join(t.TempDir(), "absent.pem"), ""); err == nil {
			t.Error("a roots file that does not exist was accepted")
		}
	})

	t.Run("a truncated model identifier is refused", func(t *testing.T) {
		// A short AAGUID matches nothing, so every enrolment would be refused and the operator would be looking
		// at their authenticators rather than at their typo.
		roots := writeTestRoots(t)

		_, err := passkeyAttestationPolicy(roots, "0102030405")
		if err == nil {
			t.Fatal("a truncated model identifier was accepted")
		}
		if !strings.Contains(err.Error(), "32 hex digits") {
			t.Errorf("the refusal does not say what the right form is: %v", err)
		}
	})

	t.Run("both spellings of a model identifier are accepted", func(t *testing.T) {
		// Vendors publish them with dashes and people paste them in whatever case they were given. A comparison
		// requiring one exact spelling would fail for a reason nobody could see in their own configuration.
		roots := writeTestRoots(t)

		policy, err := passkeyAttestationPolicy(roots,
			"01020304-0506-0708-090A-0B0C0D0E0F10, ffeeddccbbaa99887766554433221100")
		if err != nil {
			t.Fatal(err)
		}
		if len(policy.AllowedAAGUIDs) != 2 {
			t.Fatalf("read %d models, want 2", len(policy.AllowedAAGUIDs))
		}
		if !policy.AllowedAAGUIDs["01020304050607080a0b0c0d0e0f10"] &&
			!policy.AllowedAAGUIDs["0102030405060708090a0b0c0d0e0f10"] {
			t.Errorf("the dashed uppercase form was not normalised: %v", policy.AllowedAAGUIDs)
		}
	})
}

// writeTestRoots writes a PEM file containing one real self-signed certificate.
//
// Generated rather than pasted. The first version pasted a fabricated PEM block on the grounds that this helper only needs something
// AppendCertsFromPEM accepts - which was true and useless, because a fabricated block is exactly what it does not accept. Two subtests
// then failed for that reason rather than for anything they were testing.
func writeTestRoots(t *testing.T) string {
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

	path := filepath.Join(t.TempDir(), "roots.pem")
	blob := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}
