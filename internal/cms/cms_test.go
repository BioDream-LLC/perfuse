package cms

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pki is a test anchor and a Direct address certificate under it.
type pki struct {
	root, leaf *x509.Certificate
	rootKey    *rsa.PrivateKey
	key        *rsa.PrivateKey
	roots      *x509.CertPool
}

func newPKI(t *testing.T, address string) pki {
	t.Helper()
	rootKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rt := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Direct Anchor"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rt, rt, &rootKey.PublicKey, rootKey)
	root, _ := x509.ParseCertificate(rootDER)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	lt := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: address},
		EmailAddresses: []string{address}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	leafDER, _ := x509.CreateCertificate(rand.Reader, lt, root, &key.PublicKey, rootKey)
	leaf, _ := x509.ParseCertificate(leafDER)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return pki{root: root, leaf: leaf, rootKey: rootKey, key: key, roots: roots}
}

func TestSignAndVerify(t *testing.T) {
	p := newPKI(t, "sender@direct.example")
	msg := []byte("Content-Type: text/plain\r\n\r\nA referral.\r\n")
	sig, err := Sign(msg, p.leaf, p.key, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Verify(sig, msg, p.roots, time.Now())
	if err != nil || s.Certificate.Subject.CommonName != "sender@direct.example" || s.SigningTime.IsZero() {
		t.Fatalf("%v %+v", err, s)
	}
	if _, err := Verify(sig, append(msg, '!'), p.roots, time.Now()); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("a changed message: %v", err)
	}
	other := newPKI(t, "x@y")
	if _, err := Verify(sig, msg, other.roots, time.Now()); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("an untrusted signer: %v", err)
	}
}

func TestEncryptAndDecrypt(t *testing.T) {
	p := newPKI(t, "recipient@direct.example")
	for _, oaep := range []bool{false, true} {
		env, err := Encrypt([]byte("secret clinical content"), []*x509.Certificate{p.leaf}, oaep)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decrypt(env, []KeyPair{{p.leaf, p.key}})
		if err != nil || string(got) != "secret clinical content" {
			t.Fatalf("oaep=%v: %v %q", oaep, err, got)
		}
		other := newPKI(t, "someone@else.example")
		if _, err := Decrypt(env, []KeyPair{{other.leaf, other.key}}); err != ErrNotForUs {
			t.Errorf("another recipient: %v", err)
		}
	}
}

func writePEM(t *testing.T, dir, name, kind string, der []byte) string {
	f := filepath.Join(dir, name)
	if err := os.WriteFile(f, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func openssl(t *testing.T, args ...string) []byte {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command("openssl", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("openssl %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.Bytes()
}

// Both directions against OpenSSL's cms: what Perfuse signs and encrypts OpenSSL verifies and decrypts, and the reverse.
func TestInteroperatesWithOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("no openssl")
	}
	dir := t.TempDir()
	p := newPKI(t, "a@direct.example")
	rootPEM := writePEM(t, dir, "root.pem", "CERTIFICATE", p.root.Raw)
	leafPEM := writePEM(t, dir, "leaf.pem", "CERTIFICATE", p.leaf.Raw)
	keyPEM := writePEM(t, dir, "key.pem", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(p.key))
	msg := []byte("Content-Type: text/plain\r\n\r\nHello from Perfuse.\r\n")
	msgFile := filepath.Join(dir, "msg.txt")
	_ = os.WriteFile(msgFile, msg, 0o600)

	// Perfuse signs; OpenSSL verifies the detached signature against the anchor.
	sig, err := Sign(msg, p.leaf, p.key, nil)
	if err != nil {
		t.Fatal(err)
	}
	sigFile := filepath.Join(dir, "sig.der")
	_ = os.WriteFile(sigFile, sig, 0o600)
	openssl(t, "cms", "-verify", "-binary", "-inform", "DER", "-in", sigFile, "-content", msgFile, "-CAfile", rootPEM, "-purpose", "any", "-out", filepath.Join(dir, "v.txt"))

	// Perfuse encrypts; OpenSSL decrypts, with PKCS#1 v1.5 and with OAEP.
	for _, oaep := range []bool{false, true} {
		env, err := Encrypt(msg, []*x509.Certificate{p.leaf}, oaep)
		if err != nil {
			t.Fatal(err)
		}
		envFile := filepath.Join(dir, "env.der")
		_ = os.WriteFile(envFile, env, 0o600)
		got := openssl(t, "cms", "-decrypt", "-binary", "-inform", "DER", "-in", envFile, "-recip", leafPEM, "-inkey", keyPEM)
		if !bytes.Equal(got, msg) {
			t.Fatalf("OpenSSL decrypted %q (oaep=%v)", got, oaep)
		}
	}

	// OpenSSL signs (SHA-256) and encrypts (AES-256-CBC, AES-128-CBC, and OAEP); Perfuse verifies and decrypts.
	osig := openssl(t, "cms", "-sign", "-binary", "-md", "sha256", "-in", msgFile, "-signer", leafPEM, "-inkey", keyPEM, "-outform", "DER")
	if s, err := Verify(osig, msg, p.roots, time.Now()); err != nil || s.Certificate.Subject.CommonName != "a@direct.example" {
		t.Fatalf("an OpenSSL signature: %v", err)
	}
	for _, args := range [][]string{{"-aes256"}, {"-aes128"}, {"-aes256", "-keyopt", "rsa_padding_mode:oaep"}} {
		oargs := append([]string{"cms", "-encrypt", "-binary", "-in", msgFile, "-outform", "DER", "-recip", leafPEM}, args...)
		oenv := openssl(t, oargs...)
		got, err := Decrypt(oenv, []KeyPair{{p.leaf, p.key}})
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("OpenSSL %v: %v %q", args, err, got)
		}
	}
}

func FuzzDecrypt(f *testing.F) {
	p := newPKIForFuzz()
	env, _ := Encrypt([]byte("x"), []*x509.Certificate{p.leaf}, false)
	f.Add(env)
	sig, _ := Sign([]byte("x"), p.leaf, p.key, nil)
	f.Add(sig)
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Decrypt(b, []KeyPair{{p.leaf, p.key}})
		_, _ = Verify(b, []byte("x"), p.roots, time.Now())
	})
}

var fuzzPKI *pki

func newPKIForFuzz() pki {
	if fuzzPKI == nil {
		p := newPKI(&testing.T{}, "f@direct.example")
		fuzzPKI = &p
	}
	return *fuzzPKI
}
