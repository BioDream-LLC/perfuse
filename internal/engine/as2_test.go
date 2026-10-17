package engine

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// writeAS2Party writes a self-signed certificate and key, as AS2 partners exchange them.
func writeAS2Party(t *testing.T, dir, name string) (certFile, keyFile string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
	return certFile, keyFile
}

func startYAML(t *testing.T, dir, name, y string) *Channel {
	t.Helper()
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, DefaultSenderFactory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })
	return ch
}

// One channel sends over AS2 to another: the message arrives decrypted, the sender counts it delivered only on the receipt,
// and a partner the receiver does not know is refused.
func TestAnAS2ChannelSendsToAnAS2Channel(t *testing.T) {
	dir := t.TempDir()
	hc, hk := writeAS2Party(t, dir, "hospital")
	pc, pk := writeAS2Party(t, dir, "payer")
	out := filepath.Join(dir, "received")
	addr := freeAddr(t)

	startYAML(t, dir, "payer", `name: payer
source:
  type: as2
  as2:
    listen: "`+addr+`"
    id: PAYER
    cert_file: `+pc+`
    key_file: `+pk+`
    partners:
      - id: HOSPITAL
        cert_file: `+hc+`
destinations:
  - name: inbox
    type: file
    dir: `+out+`
`)
	send := func(name, receipt, extra string) *Channel {
		return startYAML(t, dir, name, `name: `+name+`
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - name: payer
    type: as2
    retry: {attempts: 1}
    as2:
      url: http://`+addr+`/as2
      from: `+name+`
      to: PAYER
      cert_file: `+hc+`
      key_file: `+hk+`
      partner_cert_file: `+pc+`
      receipt: `+receipt+`
      file_name: "${control_id}.hl7"
`+extra)
	}

	hospital := send("HOSPITAL", "sync", "")
	if _, err := hospital.handle(context.Background(), []byte(lsADT)); err != nil {
		t.Fatal(err)
	}
	if got := hospital.LastOutcomeForTest(); got != Delivered {
		t.Fatalf("outcome %v", got)
	}
	files, _ := filepath.Glob(filepath.Join(out, "*"))
	if len(files) != 1 {
		t.Fatalf("received %v", files)
	}
	if b, _ := os.ReadFile(files[0]); !strings.Contains(string(b), "ADT^A01") {
		t.Errorf("received %q", b)
	}

	// Somebody the payer has no agreement with: refused, and the sender counts it failed.
	stranger := send("STRANGER", "sync", "")
	_, _ = stranger.handle(context.Background(), []byte(lsADT))
	if got := stranger.LastOutcomeForTest(); got == Delivered {
		t.Error("a message from an unknown partner counted as delivered")
	}

	// An asynchronous receipt: delivered on acceptance, and the receipt comes back to an AS2 source here.
	backAddr := freeAddr(t)
	startYAML(t, dir, "receipts", `name: receipts
source:
  type: as2
  as2:
    listen: "`+backAddr+`"
    id: HOSPITAL
    cert_file: `+hc+`
    key_file: `+hk+`
    partners:
      - id: PAYER
        cert_file: `+pc+`
destinations:
  - name: nowhere
    type: file
    dir: `+filepath.Join(dir, "unused")+`
`)
	async := startYAML(t, dir, "async", `name: async
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - name: payer
    type: as2
    as2:
      url: http://`+addr+`/as2
      from: HOSPITAL
      to: PAYER
      cert_file: `+hc+`
      key_file: `+hk+`
      partner_cert_file: `+pc+`
      receipt: async
      receipt_url: http://`+backAddr+`/as2
`)
	if _, err := async.handle(context.Background(), []byte(strings.Replace(lsADT, "|C1|", "|C2|", 1))); err != nil {
		t.Fatal(err)
	}
	if got := async.LastOutcomeForTest(); got != Delivered {
		t.Fatalf("async outcome %v", got)
	}
	// The receipt is matched and forgotten once it arrives.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		as2PendingMu.Lock()
		n := len(as2Pending)
		as2PendingMu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	as2PendingMu.Lock()
	defer as2PendingMu.Unlock()
	if len(as2Pending) != 0 {
		t.Errorf("the asynchronous receipt never arrived: %d pending", len(as2Pending))
	}
}
