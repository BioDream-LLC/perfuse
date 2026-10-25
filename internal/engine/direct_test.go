package engine

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/direct"
)

type directPKI struct {
	dir        string
	ca         *x509.Certificate
	caKey      *rsa.PrivateKey
	anchorFile string
}

func newDirectPKI(t *testing.T) *directPKI {
	t.Helper()
	dir := t.TempDir()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Direct Anchor"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	ca, _ := x509.ParseCertificate(der)
	f := filepath.Join(dir, "anchors.pem")
	_ = os.WriteFile(f, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	return &directPKI{dir: dir, ca: ca, caKey: k, anchorFile: f}
}

// issue writes a certificate and key for an address (or, with host, a TLS server certificate) and returns the files.
func (p *directPKI) issue(t *testing.T, name string, host bool) (certFile, keyFile string, kp cms.KeyPair) {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	if host {
		tpl.DNSNames = []string{name}
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		tpl.EmailAddresses = []string{name}
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, p.ca, &k.PublicKey, p.caKey)
	c, _ := x509.ParseCertificate(der)
	certFile = filepath.Join(p.dir, strings.ReplaceAll(name, "@", "_")+".crt")
	keyFile = filepath.Join(p.dir, strings.ReplaceAll(name, "@", "_")+".key")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
	return certFile, keyFile, cms.KeyPair{Certificate: c, Key: k}
}

func freePort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	_ = ln.Close()
	return a
}

// One Perfuse sends a document as Direct through a HISP stand-in; another Perfuse, the recipient's edge, receives it from the
// HISP, opens it and passes the document on, then sends a "processed" notification back that the sender can open.
func TestADirectMessageTravelsFromOnePerfuseToAnotherThroughAHISP(t *testing.T) {
	p := newDirectPKI(t)
	senderCert, senderKey, senderPair := p.issue(t, "referrals@direct.hospital.example", false)
	recipCert, recipKey, _ := p.issue(t, "intake@direct.clinic.example", false)
	hispCert, hispKey, _ := p.issue(t, "localhost", true)
	roots := x509.NewCertPool()
	roots.AddCert(p.ca)
	directSMTPRoots = roots
	t.Cleanup(func() { directSMTPRoots = nil })

	// The HISP's listener first: the channel's port is picked and released, and a listener opened on port 0 after that could be
	// given the same number back (CI, postgres job, once), so the channel then found its address taken.
	hln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hln.Close()

	// The recipient's channel.
	listen := freePort(t)
	cfg, err := config.Load(strings.NewReader(`name: direct-in
dataType: raw
source:
  type: direct
  direct:
    address: intake@direct.clinic.example
    cert_file: `+recipCert+`
    key_file: `+recipKey+`
    anchors_file: `+p.anchorFile+`
    listen: "`+listen+`"
    smtp_host: localhost:HISPPORT
destinations:
  - name: out
    type: file
    dir: /tmp/unused
`), "in.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// The HISP: STARTTLS, routes the clinic's mail to the clinic's edge and keeps the hospital's (the MDN).
	pair, _ := tls.LoadX509KeyPair(hispCert, hispKey)
	var mu sync.Mutex
	var forHospital [][]byte
	hisp := &direct.Receiver{Hostname: "localhost", TLS: &tls.Config{Certificates: []tls.Certificate{pair}}, RequireTLS: true,
		Accept: func(string) bool { return true },
		Deliver: func(ctx context.Context, from string, to []string, raw []byte) error {
			if to[0] == "intake@direct.clinic.example" {
				return smtp.SendMail(listen, nil, from, to, raw)
			}
			mu.Lock()
			forHospital = append(forHospital, raw)
			mu.Unlock()
			return nil
		}}
	go func() { _ = hisp.Serve(hln) }()
	_, hport, _ := net.SplitHostPort(hln.Addr().String())
	cfg.Source.Direct.SMTPHost = "localhost:" + hport

	sink := &recordingSender{}
	startChannelFor(t, cfg, sink)

	// The sender.
	certDir := t.TempDir()
	rc, _ := os.ReadFile(recipCert)
	_ = os.WriteFile(filepath.Join(certDir, "intake@direct.clinic.example.pem"), rc, 0o600)
	s, err := NewDirectSender(config.Destination{Name: "d", Type: config.DestinationDirect, Direct: &config.DirectDestination{
		DirectAddress: config.DirectAddress{Address: "referrals@direct.hospital.example", CertFile: senderCert, KeyFile: senderKey, AnchorsFile: p.anchorFile},
		To:            []string{"intake@direct.clinic.example"}, Subject: "Referral", Text: "Attached.", RecipientCertsDir: certDir,
		SMTPHost: "localhost:" + hport, RequestMDN: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), []byte("A referral for DOE, JOHN.")); err != nil {
		t.Fatal(err)
	}
	if sink.count() != 1 || sink.last() != "A referral for DOE, JOHN." {
		t.Fatalf("the recipient's channel got %d messages: %q", sink.count(), sink.last())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(forHospital)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(forHospital) != 1 {
		t.Fatal("no delivery notification came back")
	}
	mdn, err := direct.Open(forHospital[0], []cms.KeyPair{senderPair}, roots)
	if err != nil || mdn.MDN == nil || mdn.MDN.Kind != "processed" {
		t.Fatalf("the notification: %v %+v", err, mdn)
	}
}

// Without a certificate for the recipient, nothing is sent: Direct never falls back to unencrypted mail.
func TestDirectRefusesARecipientWithNoCertificate(t *testing.T) {
	p := newDirectPKI(t)
	c, k, _ := p.issue(t, "a@direct.example", false)
	s, err := NewDirectSender(config.Destination{Name: "d", Direct: &config.DirectDestination{
		DirectAddress: config.DirectAddress{Address: "a@direct.example", CertFile: c, KeyFile: k, AnchorsFile: p.anchorFile},
		To:            []string{"b@nowhere.example"}, SMTPHost: "127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	s.lookup = func(context.Context, string, string) ([]*x509.Certificate, error) { return nil, nil }
	if err := s.Send(context.Background(), []byte("x")); err == nil || !strings.Contains(err.Error(), "no certificate was found") {
		t.Fatalf("%v", err)
	}
	// A certificate from an anchor the sender does not trust is not used either.
	other := newDirectPKI(t)
	_, _, kp := other.issue(t, "b@nowhere.example", false)
	s.lookup = func(context.Context, string, string) ([]*x509.Certificate, error) {
		return []*x509.Certificate{kp.Certificate}, nil
	}
	if err := s.Send(context.Background(), []byte("x")); err == nil || !strings.Contains(err.Error(), "no usable certificate") {
		t.Fatalf("%v", err)
	}
}
