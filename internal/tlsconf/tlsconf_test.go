package tlsconf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// certOpts describes a certificate to mint for a test.
type certOpts struct {
	CommonName string
	Hosts      []string
	NotBefore  time.Time
	NotAfter   time.Time
	IsCA       bool
	RSABits    int // zero means ECDSA
	Parent     *x509.Certificate
	ParentKey  any
}

// mint creates a certificate and returns the PEM for it and its key.
func mint(t *testing.T, o certOpts) (certPEM, keyPEM []byte, cert *x509.Certificate, key any) {
	t.Helper()

	if o.NotBefore.IsZero() {
		o.NotBefore = time.Now().Add(-time.Hour)
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = time.Now().Add(365 * 24 * time.Hour)
	}

	var pub any
	if o.RSABits > 0 {
		k, err := rsa.GenerateKey(rand.Reader, o.RSABits)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: o.CommonName},
		NotBefore:    o.NotBefore,
		NotAfter:     o.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}
	if o.IsCA {
		tmpl.IsCA = true
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	for _, h := range o.Hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}

	parent, parentKey := tmpl, key
	if o.Parent != nil {
		parent, parentKey = o.Parent, o.ParentKey
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, cert, key
}

// writeCert mints a certificate into a directory and returns the two paths.
func writeCert(t *testing.T, dir, name string, o certOpts) (certPath, keyPath string) {
	t.Helper()
	certPEM, keyPEM, _, _ := mint(t, o)

	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestDisabledSettingsProduceNoConfig(t *testing.T) {
	cfg, err := ForListener(&Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Error("disabled TLS should produce no config")
	}
	if (&Settings{}).IsEnabled() {
		t.Error("empty settings should not be enabled")
	}
}

func TestCertificateFilesNamedButNotEnabledIsRefused(t *testing.T) {
	// Almost always somebody who meant to turn it on, and silently serving plain
	// MLLP after they configured certificates is the wrong outcome.
	errs := (&Settings{CertFile: "a.crt", KeyFile: "a.key"}).Validate(true)
	if len(errs) == 0 {
		t.Fatal("naming certificate files without enabling TLS should be refused")
	}
	if !strings.Contains(errs[0].Error(), "enabled: true") {
		t.Errorf("the error should say what to do: %v", errs[0])
	}
}

func TestRequireClientCertWithoutAnAuthorityIsRefused(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "server", certOpts{
		CommonName: "registry.example.org", Hosts: []string{"registry.example.org"},
	})

	// This is the failure mode the package exists to prevent: requesting a
	// certificate and not verifying it looks identical to mutual TLS in every log
	// and accepts anything at all.
	errs := (&Settings{
		Enabled: true, CertFile: certPath, KeyFile: keyPath,
		RequireClientCert: true,
	}).Validate(true)

	if len(errs) == 0 {
		t.Fatal("requiring client certificates with no CA should be refused")
	}
	joined := errorsText(errs)
	if !strings.Contains(joined, "theatre") {
		t.Errorf("the error should explain why: %s", joined)
	}
}

func TestMutualTLSUsesRequireAndVerify(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, caParsed, caSigner := mint(t, certOpts{CommonName: "Test CA", IsCA: true})
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, caCert, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = caKey

	serverPEM, serverKeyPEM, _, _ := mint(t, certOpts{
		CommonName: "registry.example.org", Hosts: []string{"registry.example.org"},
		Parent: caParsed, ParentKey: caSigner,
	})
	certPath := filepath.Join(dir, "s.crt")
	keyPath := filepath.Join(dir, "s.key")
	os.WriteFile(certPath, serverPEM, 0o600)
	os.WriteFile(keyPath, serverKeyPEM, 0o600)

	cfg, err := ForListener(&Settings{
		Enabled: true, CertFile: certPath, KeyFile: keyPath,
		CAFile: caPath, RequireClientCert: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// RequestClientCert would ask for a certificate and accept whatever arrived.
	if cfg.ClientAuth.String() != "RequireAndVerifyClientCert" {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Error("no client CA pool was built")
	}
}

func TestInsecureSkipVerifyIsRefusedOnAListener(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "s", certOpts{CommonName: "x", Hosts: []string{"x"}})

	// It means nothing on a server, and its presence suggests a misunderstanding
	// worth correcting before it is copied onto a sender where it means everything.
	errs := (&Settings{
		Enabled: true, CertFile: certPath, KeyFile: keyPath,
		InsecureSkipVerify: true,
	}).Validate(true)

	if len(errs) == 0 {
		t.Fatal("insecure_skip_verify on a listener should be refused")
	}
	if !strings.Contains(errorsText(errs), "require_client_cert") {
		t.Errorf("the error should point at what they probably wanted: %s", errorsText(errs))
	}
}

func TestSkipVerifyAndCAFileTogetherIsRefused(t *testing.T) {
	dir := t.TempDir()
	caPath, _ := writeCert(t, dir, "ca", certOpts{CommonName: "CA", IsCA: true})

	// The authority would never be consulted, so one of the two settings is a
	// mistake and guessing which would be worse than asking.
	errs := (&Settings{
		Enabled: true, CAFile: caPath, InsecureSkipVerify: true,
	}).Validate(false)
	if len(errs) == 0 {
		t.Fatal("skip-verify together with a CA file should be refused")
	}
}

func TestObsoleteTLSVersionsAreRefusedWithAdvice(t *testing.T) {
	for _, v := range []string{"1.0", "1.1"} {
		errs := (&Settings{Enabled: true, MinVersion: v,
			CertFile: "x", KeyFile: "y"}).Validate(false)
		if len(errs) == 0 {
			t.Fatalf("min_version %s should be refused", v)
		}
		// Somebody setting 1.0 has a reason, and the useful answer is where to put
		// the compatibility rather than a bare refusal.
		if !strings.Contains(errorsText(errs), "in front of Perfuse") {
			t.Errorf("the error should suggest an alternative: %s", errorsText(errs))
		}
	}
}

func TestDefaultMinimumIsTLS12(t *testing.T) {
	// Not 1.3. A hospital interface engine has to talk to systems that will never
	// support 1.3, and refusing them means the feed runs unencrypted instead.
	got, err := parseVersion("")
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x0303 {
		t.Errorf("default min version = %x, want TLS 1.2", got)
	}
}

func TestMissingFilesAreCaughtAtValidation(t *testing.T) {
	// A channel that fails to start is far better than one that starts and refuses
	// every connection.
	errs := (&Settings{
		Enabled:  true,
		CertFile: "/nonexistent/a.crt",
		KeyFile:  "/nonexistent/a.key",
	}).Validate(true)

	if len(errs) < 2 {
		t.Errorf("both missing files should be reported, got %d: %s", len(errs), errorsText(errs))
	}
}

func TestWarningsSayWhatIsNotProtected(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "s", certOpts{CommonName: "x", Hosts: []string{"x"}})

	listener := (&Settings{Enabled: true, CertFile: certPath, KeyFile: keyPath}).Warnings(true)
	if len(listener) == 0 {
		t.Fatal("a listener without client certificates should warn")
	}
	// The distinction matters: TLS here protects the traffic, not access.
	if !strings.Contains(strings.Join(listener, " "), "protects the traffic, not access") {
		t.Errorf("warnings = %v", listener)
	}

	sender := (&Settings{Enabled: true, InsecureSkipVerify: true}).Warnings(false)
	joined := strings.Join(sender, " ")
	if !strings.Contains(joined, "impersonation") {
		t.Errorf("skip-verify should warn about impersonation, got %v", sender)
	}
	if !strings.Contains(joined, "server_name") {
		t.Error("the warning should point at the honest alternative")
	}
}

// --- describing -------------------------------------------------------------

func TestDescribeReportsDaysRemaining(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeCert(t, dir, "soon", certOpts{
		CommonName: "expiring.example.org",
		Hosts:      []string{"expiring.example.org"},
		NotAfter:   time.Now().Add(9 * 24 * time.Hour),
	})

	certs, err := Describe(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 {
		t.Fatalf("certs = %d", len(certs))
	}
	c := certs[0]

	// Days rather than a date, because "expires 2026-11-04" requires arithmetic
	// and "expires in 9 days" does not.
	if c.DaysRemaining < 8 || c.DaysRemaining > 9 {
		t.Errorf("DaysRemaining = %d, want about 9", c.DaysRemaining)
	}
	if c.Status != "expiring" {
		t.Errorf("Status = %q, want expiring", c.Status)
	}
	if c.Subject != "expiring.example.org" {
		t.Errorf("Subject = %q", c.Subject)
	}
}

func TestDescribeReportsAnExpiredCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeCert(t, dir, "old", certOpts{
		CommonName: "old.example.org",
		Hosts:      []string{"old.example.org"},
		NotBefore:  time.Now().Add(-60 * 24 * time.Hour),
		NotAfter:   time.Now().Add(-5 * 24 * time.Hour),
	})

	certs, _ := Describe(certPath)
	if certs[0].Status != "expired" {
		t.Errorf("Status = %q", certs[0].Status)
	}
	if certs[0].DaysRemaining >= 0 {
		t.Errorf("DaysRemaining = %d, want negative", certs[0].DaysRemaining)
	}
}

func TestDescribeNotesAMissingSubjectAlternativeName(t *testing.T) {
	dir := t.TempDir()
	// A common name and no SANs. Verification has not used the common name for
	// years, so this certificate cannot verify against any hostname however
	// reasonable it looks.
	certPath, _ := writeCert(t, dir, "cn-only", certOpts{CommonName: "legacy.example.org"})

	certs, _ := Describe(certPath)
	if len(certs[0].Hosts) != 0 {
		t.Fatalf("hosts = %v", certs[0].Hosts)
	}
	if !strings.Contains(strings.Join(certs[0].Notes, " "), "common name is not used") {
		t.Errorf("notes = %v", certs[0].Notes)
	}
}

func TestDescribeNotesAWeakRSAKey(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeCert(t, dir, "weak", certOpts{
		CommonName: "weak.example.org", Hosts: []string{"weak.example.org"},
		RSABits: 1024,
	})

	certs, _ := Describe(certPath)
	if certs[0].KeyType != "RSA" || certs[0].KeyBits != 1024 {
		t.Fatalf("key = %s %d", certs[0].KeyType, certs[0].KeyBits)
	}
	// The other end may refuse it even though this end is perfectly happy, which is
	// a confusing failure to debug from this side.
	if !strings.Contains(strings.Join(certs[0].Notes, " "), "2048") {
		t.Errorf("notes = %v", certs[0].Notes)
	}
}

func TestDescribeNotesSelfSigned(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeCert(t, dir, "self", certOpts{
		CommonName: "self.example.org", Hosts: []string{"self.example.org"},
	})

	certs, _ := Describe(certPath)
	if !certs[0].SelfSigned {
		t.Error("should be reported as self-signed")
	}
	if !strings.Contains(strings.Join(certs[0].Notes, " "), "this exact certificate") {
		t.Errorf("notes = %v", certs[0].Notes)
	}
}

func TestDescribeReadsEveryCertificateInAChain(t *testing.T) {
	// An intermediate expiring breaks the connection just as thoroughly as the leaf,
	// so reporting only the first would miss it.
	caPEM, _, caParsed, caKey := mint(t, certOpts{CommonName: "Chain CA", IsCA: true})
	leafPEM, _, _, _ := mint(t, certOpts{
		CommonName: "leaf.example.org", Hosts: []string{"leaf.example.org"},
		Parent: caParsed, ParentKey: caKey,
	})

	bundle := append(append([]byte{}, leafPEM...), caPEM...)
	certs, err := DescribePEM(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("want 2 certificates from a chain, got %d", len(certs))
	}
	if !certs[1].IsCA {
		t.Error("the second certificate should be the authority")
	}
}

func TestDescribeIgnoresAPrivateKeyInTheSameFile(t *testing.T) {
	// Combined cert-and-key files are normal and are not an error.
	certPEM, keyPEM, _, _ := mint(t, certOpts{
		CommonName: "combined.example.org", Hosts: []string{"combined.example.org"},
	})
	combined := append(append([]byte{}, certPEM...), keyPEM...)

	certs, err := DescribePEM(combined)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 {
		t.Errorf("certs = %d", len(certs))
	}
}

func TestDescribeRefusesSomethingThatIsNotPEM(t *testing.T) {
	if _, err := DescribePEM([]byte("this is not a certificate")); err == nil {
		t.Error("non-PEM input should be refused")
	}
}

func TestSummarisePromotesExpiryToTheConnection(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "s", certOpts{
		CommonName: "soon.example.org", Hosts: []string{"soon.example.org"},
		NotAfter: time.Now().Add(10 * 24 * time.Hour),
	})

	sum := Summarise(&Settings{
		Enabled: true, CertFile: certPath, KeyFile: keyPath,
	}, true)

	if !sum.Enabled {
		t.Fatal("should be enabled")
	}
	// Promoted from a field on a certificate to a warning on the whole connection,
	// because expiry is the commonest cause of an interface stopping and should not
	// be something you have to go looking for.
	if !strings.Contains(strings.Join(sum.Warnings, " "), "expires in") {
		t.Errorf("warnings = %v", sum.Warnings)
	}
}

func TestSummariseReportsAnExpiredCertificateAsAProblem(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCert(t, dir, "s", certOpts{
		CommonName: "dead.example.org", Hosts: []string{"dead.example.org"},
		NotBefore: time.Now().Add(-60 * 24 * time.Hour),
		NotAfter:  time.Now().Add(-1 * 24 * time.Hour),
	})

	sum := Summarise(&Settings{
		Enabled: true, CertFile: certPath, KeyFile: keyPath,
	}, true)

	// A problem rather than a warning: connections are failing right now.
	if !strings.Contains(strings.Join(sum.Problems, " "), "failing now") {
		t.Errorf("problems = %v", sum.Problems)
	}
}

func TestSummariseReportsMutualTLS(t *testing.T) {
	dir := t.TempDir()
	caPEM, _, caParsed, caKey := mint(t, certOpts{CommonName: "CA", IsCA: true})
	caPath := filepath.Join(dir, "ca.crt")
	os.WriteFile(caPath, caPEM, 0o600)

	leafPEM, leafKeyPEM, _, _ := mint(t, certOpts{
		CommonName: "s.example.org", Hosts: []string{"s.example.org"},
		Parent: caParsed, ParentKey: caKey,
	})
	certPath := filepath.Join(dir, "s.crt")
	keyPath := filepath.Join(dir, "s.key")
	os.WriteFile(certPath, leafPEM, 0o600)
	os.WriteFile(keyPath, leafKeyPEM, 0o600)

	sum := Summarise(&Settings{
		Enabled: true, CertFile: certPath, KeyFile: keyPath,
		CAFile: caPath, RequireClientCert: true,
	}, true)

	if !sum.MutualTLS {
		t.Error("should report mutual TLS")
	}
	if len(sum.Authorities) != 1 {
		t.Errorf("authorities = %d", len(sum.Authorities))
	}
	if len(sum.Problems) != 0 {
		t.Errorf("a correct mutual TLS setup should have no problems: %v", sum.Problems)
	}
}

func errorsText(errs []error) string {
	var parts []string
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}
