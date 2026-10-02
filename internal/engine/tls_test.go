package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// An end-to-end test of MLLP over TLS: a real handshake between a real channel
// listener and a real sender.
//
// Configuration tests can only prove that a tls.Config was built. Whether a
// message actually crosses an encrypted connection, and whether a wrong client
// certificate is genuinely refused, needs a handshake.

type tlsFixture struct {
	dir      string
	caPath   string
	certPath string
	keyPath  string
	// A second authority, for proving that a certificate from the wrong CA is
	// rejected. A test that only ever uses one CA cannot tell verification from
	// no verification.
	otherCAPath   string
	otherCertPath string
	otherKeyPath  string
}

func newTLSFixture(t *testing.T) tlsFixture {
	t.Helper()
	dir := t.TempDir()

	f := tlsFixture{dir: dir}

	caCert, caKey := mintCA(t, "Perfuse Test CA")
	f.caPath = write(t, dir, "ca.crt", pemCert(caCert.Raw))

	leaf, leafKey := mintLeaf(t, "127.0.0.1", caCert, caKey)
	f.certPath = write(t, dir, "node.crt", pemCert(leaf.Raw))
	f.keyPath = write(t, dir, "node.key", pemKey(t, leafKey))

	otherCA, otherKey := mintCA(t, "Somebody Else CA")
	f.otherCAPath = write(t, dir, "other-ca.crt", pemCert(otherCA.Raw))

	otherLeaf, otherLeafKey := mintLeaf(t, "127.0.0.1", otherCA, otherKey)
	f.otherCertPath = write(t, dir, "other.crt", pemCert(otherLeaf.Raw))
	f.otherKeyPath = write(t, dir, "other.key", pemKey(t, otherLeafKey))

	return f
}

func mintCA(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func mintLeaf(t *testing.T, host string, ca *x509.Certificate, caKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func pemKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tlsChannel starts a channel listening with TLS and returns its address.
func tlsChannel(t *testing.T, source *tlsconf.Settings, capture *captureDest) string {
	t.Helper()

	// Port zero, then read back what was bound. A fixed port makes a test suite
	// fail when something else on the machine happens to be using it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := &config.Channel{
		Name: "tls-channel",
		Source: config.Source{
			Type:   config.SourceMLLP,
			Listen: addr,
			TLS:    source,
			Ack:    config.Ack{When: config.AckOnDelivery},
		},
		Destinations: []config.Destination{{
			Name: "capture", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
			Retry: config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })

	return addr
}

// captureDest records what the channel forwarded.
type captureDest struct {
	mu  sync.Mutex
	got [][]byte
	// fail makes the destination reject, so failure handling can be tested without
	// a real receiver that has to break on cue.
	fail bool
}

// messages returns a copy, so a test can read while the poller is still writing.
func (c *captureDest) messages() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.got))
	copy(out, c.got)
	return out
}

func (c *captureDest) Send(ctx context.Context, raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return fmt.Errorf("this destination was told to fail by the test")
	}
	stored := make([]byte, len(raw))
	copy(stored, raw)
	c.got = append(c.got, stored)
	return nil
}
func (c *captureDest) Describe() string { return "capture" }
func (c *captureDest) Close() error     { return nil }

const tlsTestMessage = "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819100000-0500||ADT^A01^ADT_A01|TLS1|P|2.5.1\r" +
	"PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|F\r"

func sendOverTLS(t *testing.T, addr string, settings *tlsconf.Settings) ([]byte, error) {
	t.Helper()

	sender, err := NewMLLPSender(config.Destination{
		Name: "out", Type: config.DestinationMLLP,
		Address: addr, Timeout: 5 * time.Second,
		TLS: settings,
	})
	if err != nil {
		return nil, err
	}
	defer sender.Close()

	return nil, sender.Send(context.Background(), []byte(tlsTestMessage))
}

func TestAMessageCrossesTLS(t *testing.T) {
	f := newTLSFixture(t)
	capture := &captureDest{}

	addr := tlsChannel(t, &tlsconf.Settings{
		Enabled:  true,
		CertFile: f.certPath,
		KeyFile:  f.keyPath,
	}, capture)

	if _, err := sendOverTLS(t, addr, &tlsconf.Settings{
		Enabled: true,
		CAFile:  f.caPath,
	}); err != nil {
		t.Fatalf("sending over TLS failed: %v", err)
	}

	if len(capture.got) != 1 {
		t.Fatalf("the channel forwarded %d messages", len(capture.got))
	}
	parsed, err := hl7.Parse(capture.got[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ControlID() != "TLS1" {
		t.Errorf("control id = %q", parsed.ControlID())
	}
}

func TestAnUntrustedServerCertificateIsRefused(t *testing.T) {
	f := newTLSFixture(t)
	capture := &captureDest{}

	// The listener presents a certificate from one authority; the sender trusts a
	// different one. Without this test, a configuration that verified nothing
	// would look identical to one that verified correctly.
	addr := tlsChannel(t, &tlsconf.Settings{
		Enabled:  true,
		CertFile: f.certPath,
		KeyFile:  f.keyPath,
	}, capture)

	_, err := sendOverTLS(t, addr, &tlsconf.Settings{
		Enabled: true,
		CAFile:  f.otherCAPath,
	})
	if err == nil {
		t.Fatal("a certificate from an untrusted authority should be refused")
	}
	if len(capture.got) != 0 {
		t.Error("nothing should have been forwarded")
	}
}

func TestSkipVerifyAcceptsAnUntrustedCertificate(t *testing.T) {
	f := newTLSFixture(t)
	capture := &captureDest{}

	addr := tlsChannel(t, &tlsconf.Settings{
		Enabled:  true,
		CertFile: f.certPath,
		KeyFile:  f.keyPath,
	}, capture)

	// It exists because refusing to offer it would send people to stunnel or to
	// plain TCP, which is worse. This asserts it does what it says, so the warning
	// attached to it is accurate.
	if _, err := sendOverTLS(t, addr, &tlsconf.Settings{
		Enabled:            true,
		InsecureSkipVerify: true,
	}); err != nil {
		t.Fatalf("skip-verify should have accepted the certificate: %v", err)
	}
	if len(capture.got) != 1 {
		t.Error("the message should have been forwarded")
	}
}

func TestMutualTLSAcceptsTheRightClientCertificate(t *testing.T) {
	f := newTLSFixture(t)
	capture := &captureDest{}

	addr := tlsChannel(t, &tlsconf.Settings{
		Enabled:           true,
		CertFile:          f.certPath,
		KeyFile:           f.keyPath,
		CAFile:            f.caPath,
		RequireClientCert: true,
	}, capture)

	if _, err := sendOverTLS(t, addr, &tlsconf.Settings{
		Enabled:  true,
		CAFile:   f.caPath,
		CertFile: f.certPath,
		KeyFile:  f.keyPath,
	}); err != nil {
		t.Fatalf("mutual TLS with the right certificate failed: %v", err)
	}
	if len(capture.got) != 1 {
		t.Error("the message should have been forwarded")
	}
}

func TestMutualTLSRefusesAClientWithNoCertificate(t *testing.T) {
	f := newTLSFixture(t)
	capture := &captureDest{}

	addr := tlsChannel(t, &tlsconf.Settings{
		Enabled:           true,
		CertFile:          f.certPath,
		KeyFile:           f.keyPath,
		CAFile:            f.caPath,
		RequireClientCert: true,
	}, capture)

	// This is the assertion that separates real mutual TLS from the appearance of
	// it. A server that requested a certificate without verifying would accept this.
	_, err := sendOverTLS(t, addr, &tlsconf.Settings{
		Enabled: true,
		CAFile:  f.caPath,
	})
	if err == nil {
		t.Fatal("a client with no certificate should be refused")
	}
	if len(capture.got) != 0 {
		t.Error("nothing should have been forwarded")
	}
}

func TestMutualTLSRefusesACertificateFromTheWrongAuthority(t *testing.T) {
	f := newTLSFixture(t)
	capture := &captureDest{}

	addr := tlsChannel(t, &tlsconf.Settings{
		Enabled:           true,
		CertFile:          f.certPath,
		KeyFile:           f.keyPath,
		CAFile:            f.caPath,
		RequireClientCert: true,
	}, capture)

	// A valid certificate, correctly presented, signed by somebody we do not
	// trust. This is the case a naive implementation lets through.
	_, err := sendOverTLS(t, addr, &tlsconf.Settings{
		Enabled:  true,
		CAFile:   f.caPath,
		CertFile: f.otherCertPath,
		KeyFile:  f.otherKeyPath,
	})
	if err == nil {
		t.Fatal("a client certificate from an untrusted authority should be refused")
	}
	if len(capture.got) != 0 {
		t.Error("nothing should have been forwarded")
	}
}

func TestAPlainSenderCannotTalkToATLSListener(t *testing.T) {
	f := newTLSFixture(t)
	capture := &captureDest{}

	addr := tlsChannel(t, &tlsconf.Settings{
		Enabled:  true,
		CertFile: f.certPath,
		KeyFile:  f.keyPath,
	}, capture)

	// Worth asserting so that turning TLS on cannot silently keep accepting plain
	// connections, which would make the whole setting decorative.
	sender, err := NewMLLPSender(config.Destination{
		Name: "plain", Type: config.DestinationMLLP,
		Address: addr, Timeout: 2 * time.Second,
		Retry: config.Retry{Attempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	if err := sender.Send(context.Background(), []byte(tlsTestMessage)); err == nil {
		t.Error("a plain MLLP sender should not be able to talk to a TLS listener")
	}
	if len(capture.got) != 0 {
		t.Error("nothing should have been forwarded")
	}
}

func TestATLSSenderCannotTalkToAPlainListener(t *testing.T) {
	capture := &captureDest{}
	f := newTLSFixture(t)

	// No TLS on the listener.
	addr := tlsChannel(t, nil, capture)

	_, err := sendOverTLS(t, addr, &tlsconf.Settings{
		Enabled: true,
		CAFile:  f.caPath,
	})
	if err == nil {
		t.Error("a TLS sender should not succeed against a plain listener")
	}
}

func TestTLSMisconfigurationStopsTheChannelStarting(t *testing.T) {
	// Better than a channel that starts, looks healthy and refuses every
	// connection.
	cfg := &config.Channel{
		Name: "broken-tls",
		Source: config.Source{
			Type: config.SourceMLLP, Listen: "127.0.0.1:0",
			TLS: &tlsconf.Settings{
				Enabled:  true,
				CertFile: filepath.Join(t.TempDir(), "missing.crt"),
				KeyFile:  filepath.Join(t.TempDir(), "missing.key"),
			},
		},
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP, Address: "127.0.0.1:1",
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a missing certificate should be caught at validation")
	}
	if !strings.Contains(err.Error(), "cert_file") {
		t.Errorf("the error should name the setting: %v", err)
	}
}
