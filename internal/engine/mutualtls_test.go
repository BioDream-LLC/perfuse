package engine

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// Mutual TLS against a real server that demands a client certificate.
//
// Everything else about client certificates in this codebase is Go talking to Go: a test server built with crypto/tls, a client built
// with crypto/tls, and agreement between them. That agreement is worth less than it looks. The SAML canonicaliser passed a thousand
// lines of self-agreeing tests while being unable to accept any assertion a real identity provider produced, and mutual TLS is the
// same kind of surface - the part that matters is what the other implementation does with what we send.
//
// So this runs against nginx, which is OpenSSL. It is skipped when the server is not there, because a check that needs a container is
// a check people stop running, and the skip says how to start it rather than passing quietly.
//
// This also matters for TEFCA, where mutual TLS is the transport and not an option. That exchange is not implemented and is refused
// rather than faked; when it is written, this is the part that will already have been proven.

const mtlsAddr = "127.0.0.1:9443"

// mtlsCerts finds the certificates the nginx container was started with.
func mtlsCerts(t *testing.T) (caFile, certFile, keyFile string, ok bool) {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", false
	}

	dir := filepath.Join(home, "mtls-test")
	caFile = filepath.Join(dir, "ca.crt")
	certFile = filepath.Join(dir, "client.crt")
	keyFile = filepath.Join(dir, "client.key")

	for _, p := range []string{caFile, certFile, keyFile} {
		if _, err := os.Stat(p); err != nil {
			return "", "", "", false
		}
	}

	return caFile, certFile, keyFile, true
}

func requireMutualTLSServer(t *testing.T) (caFile, certFile, keyFile string) {
	t.Helper()

	caFile, certFile, keyFile, ok := mtlsCerts(t)
	if !ok {
		t.Skip("no client certificates: run scripts/mtls-server.sh to start the nginx that demands one")
	}

	conn, err := net.DialTimeout("tcp", mtlsAddr, 2*time.Second)
	if err != nil {
		t.Skip("nothing listening on " + mtlsAddr + ": run scripts/mtls-server.sh")
	}

	_ = conn.Close()

	return caFile, certFile, keyFile
}

func TestAClientCertificateIsActuallyPresentedToARealServer(t *testing.T) {
	// The assertion: a destination configured with a client certificate is accepted by a server that requires one. Delivery
	// succeeding is the proof, because nginx answers 400 to anything that does not present a certificate it can verify.
	caFile, certFile, keyFile := requireMutualTLSServer(t)

	sender, err := NewHTTPSender(config.Destination{
		Name: "receiver",
		Type: config.DestinationHTTP,
		HTTP: &config.HTTPDestination{
			URL: "https://" + mtlsAddr + "/",
			TLS: &tlsconf.Settings{
				Enabled:  true,
				CAFile:   caFile,
				CertFile: certFile,
				KeyFile:  keyFile,
			},
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	if err := sender.Send(context.Background(), []byte("MSH|^~\\&|PERFUSE|TEST|||||ADT^A01|1|P|2.5\r")); err != nil {
		t.Fatalf("delivery to a server requiring a client certificate failed: %v", err)
	}
}

func TestWithoutAClientCertificateTheRealServerRefusesUs(t *testing.T) {
	// The negative control, and the reason the test above means anything.
	//
	// Without it, a server that had quietly stopped requiring certificates would make the first test pass while proving nothing -
	// which is exactly the shape of failure this file exists to avoid. nginx answers 400 rather than closing the connection, so the
	// failure has to be recognised as a rejection and not as a network error.
	caFile, _, _ := requireMutualTLSServer(t)

	sender, err := NewHTTPSender(config.Destination{
		Name: "receiver",
		Type: config.DestinationHTTP,
		HTTP: &config.HTTPDestination{
			URL: "https://" + mtlsAddr + "/",
			TLS: &tlsconf.Settings{
				Enabled: true,
				CAFile:  caFile,
			},
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	err = sender.Send(context.Background(), []byte("MSH|^~\\&|PERFUSE|TEST|||||ADT^A01|2|P|2.5\r"))
	if err == nil {
		t.Fatal("a server requiring a client certificate accepted a request without one, so the first test proves nothing")
	}

	// 400 is nginx's answer to a missing client certificate. Named explicitly so that a connection refused - the server not running -
	// cannot be mistaken for the server enforcing anything.
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("refused, but not with the 400 that means no client certificate: %v", err)
	}
}

func TestTheServerCertificateIsVerifiedAgainstTheConfiguredAuthority(t *testing.T) {
	// The other direction of the same handshake. Perfuse must reject a certificate it cannot verify rather than proceeding, and the
	// default has to be verification: an integration engine that trusted any certificate would carry patient data to whoever
	// answered the address.
	_, certFile, keyFile := requireMutualTLSServer(t)

	sender, err := NewHTTPSender(config.Destination{
		Name: "receiver",
		Type: config.DestinationHTTP,
		HTTP: &config.HTTPDestination{
			URL: "https://" + mtlsAddr + "/",
			TLS: &tlsconf.Settings{
				// No CAFile, so the system roots apply and a certificate signed by a local test authority is not among them.
				Enabled:  true,
				CertFile: certFile,
				KeyFile:  keyFile,
			},
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	err = sender.Send(context.Background(), []byte("MSH|^~\\&|PERFUSE|TEST|||||ADT^A01|3|P|2.5\r"))
	if err == nil {
		t.Fatal("a certificate signed by an unknown authority was accepted")
	}

	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Errorf("refused, but not for a certificate reason: %v", err)
	}
}

func TestWhatWeSendIsWhatOpenSSLExpects(t *testing.T) {
	// A handshake at the protocol level, separate from any destination, so a failure points at the TLS settings rather than at the
	// HTTP sender built on them. Reads back what the server made of our certificate.
	caFile, certFile, keyFile := requireMutualTLSServer(t)

	cfg, err := tlsconf.ForSender(&tlsconf.Settings{
		Enabled:  true,
		CAFile:   caFile,
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg == nil {
		t.Fatal("ForSender returned no configuration for enabled settings")
	}

	// The certificate has to actually be in the configuration. A settings block that loaded nothing would fail the handshake below
	// for a reason that reads like a server problem.
	if len(cfg.Certificates) != 1 {
		t.Fatalf("the client configuration carries %d certificates, want 1", len(cfg.Certificates))
	}

	conn, err := tls.Dial("tcp", mtlsAddr, cfg)
	if err != nil {
		t.Fatalf("the handshake with OpenSSL failed: %v", err)
	}

	defer func() { _ = conn.Close() }()

	state := conn.ConnectionState()
	if state.Version < tls.VersionTLS12 {
		t.Errorf("negotiated TLS version %x, which is below 1.2", state.Version)
	}

	if len(state.PeerCertificates) == 0 {
		t.Error("the server presented no certificate")
	}
}
