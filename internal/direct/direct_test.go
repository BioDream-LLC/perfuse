package direct

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
	"github.com/biodream-llc/perfuse/internal/xds"
)

type anchor struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
	pool *x509.CertPool
}

func newAnchor(t *testing.T, name string) anchor {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	c, _ := x509.ParseCertificate(der)
	p := x509.NewCertPool()
	p.AddCert(c)
	return anchor{c, k, p}
}

// issue makes an address-bound (an e-mail address) or domain-bound (a dNSName) Direct certificate.
func (a anchor) issue(t *testing.T, address string, domainBound bool) cms.KeyPair {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: address},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	if domainBound {
		tpl.DNSNames = []string{address[strings.IndexByte(address, '@')+1:]}
	} else {
		tpl.EmailAddresses = []string{address}
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, a.cert, &k.PublicKey, a.key)
	c, _ := x509.ParseCertificate(der)
	return cms.KeyPair{Certificate: c, Key: k}
}

func TestADirectMessageIsProtectedAndOpened(t *testing.T) {
	trust := newAnchor(t, "Test HISP Anchor")
	alice := trust.issue(t, "alice@direct.hospital.example", false)
	bob := trust.issue(t, "bob@direct.clinic.example", true)
	m := Message{From: "alice@direct.hospital.example", To: []string{"bob@direct.clinic.example"}, Subject: "Referral — Doe",
		Text: "Please see the attached summary.", DispositionNotification: true,
		Attachments: []Attachment{{Name: "summary.xml", MimeType: "text/xml", Content: []byte("<ClinicalDocument/>")}}}
	raw, err := Protect(m, alice, nil, []*x509.Certificate{bob.Certificate})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("ClinicalDocument")) || !bytes.Contains(raw, []byte("smime-type=enveloped-data")) {
		t.Fatal("the content is visible outside the encryption")
	}
	got, err := Open(raw, []cms.KeyPair{bob}, trust.pool)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Referral — Doe" || got.Text != "Please see the attached summary.\r\n" && got.Text != "Please see the attached summary." ||
		len(got.Attachments) != 1 || string(got.Attachments[0].Content) != "<ClinicalDocument/>" || got.SignerAddress != "alice@direct.hospital.example" {
		t.Fatalf("%+v", got)
	}
	// The sender can read its own copy.
	if _, err := Open(raw, []cms.KeyPair{alice}, trust.pool); err != nil {
		t.Errorf("the sender's copy: %v", err)
	}
	// Someone else cannot.
	eve := trust.issue(t, "eve@elsewhere.example", false)
	if _, err := Open(raw, []cms.KeyPair{eve}, trust.pool); err != cms.ErrNotForUs {
		t.Errorf("another key: %v", err)
	}
	// Signed by a certificate from an anchor the recipient does not trust.
	rogue := newAnchor(t, "Rogue")
	mallory := rogue.issue(t, "alice@direct.hospital.example", false)
	raw2, _ := Protect(m, mallory, nil, []*x509.Certificate{bob.Certificate})
	if _, err := Open(raw2, []cms.KeyPair{bob}, trust.pool); err == nil || !strings.Contains(err.Error(), "not signed by a trusted") {
		t.Errorf("an untrusted anchor: %v", err)
	}
	// Signed by a trusted certificate for another address: From is not who signed.
	carol := trust.issue(t, "carol@direct.hospital.example", false)
	raw3, _ := Protect(m, carol, nil, []*x509.Certificate{bob.Certificate})
	if _, err := Open(raw3, []cms.KeyPair{bob}, trust.pool); err == nil || !strings.Contains(err.Error(), "not for alice") {
		t.Errorf("a signer that is not the sender: %v", err)
	}
	// A plain e-mail is not Direct.
	if _, err := Open([]byte("From: a@b\r\nTo: c@d\r\nContent-Type: text/plain\r\n\r\nhi\r\n"), []cms.KeyPair{bob}, trust.pool); err == nil {
		t.Error("an unencrypted message was opened")
	}
}

func TestAnXDMPackageRoundTrips(t *testing.T) {
	m := xds.DocumentMeta{PatientID: "DWMRN1^^^&1.2.3&ISO", SourceID: "1.2.3.4", Title: "Summary", MimeType: "text/xml",
		Class: xds.Code{Code: "34133-9", Scheme: "2.16.840.1.113883.6.1"}, Type: xds.Code{Code: "34133-9", Scheme: "2.16.840.1.113883.6.1"},
		Format: xds.Code{Code: "urn:hl7-org:sdwg:ccda-structuredBody:2.1", Scheme: "1.3.6.1.4.1.19376.1.2.3"}, Confidentiality: xds.Code{Code: "N", Scheme: "2.16.840.1.113883.5.25"},
		FacilityType: xds.Code{Code: "225732001", Scheme: "2.16.840.1.113883.6.96"}, PracticeSetting: xds.Code{Code: "394802001", Scheme: "2.16.840.1.113883.6.96"},
		ContentType: xds.Code{Code: "34133-9", Scheme: "2.16.840.1.113883.6.1"}}
	req, err := xds.Build(m, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	zipped, err := XDMFor(req, "DOC0001.XML", []byte("<ClinicalDocument>xdm</ClinicalDocument>"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := ReadXDM(zipped)
	if err != nil || len(docs) != 1 || docs[0].PatientID != m.PatientID || docs[0].TypeCode != "34133-9" || string(docs[0].Content) != "<ClinicalDocument>xdm</ClinicalDocument>" {
		t.Fatalf("%v %+v", err, docs)
	}
	if _, err := ReadXDM([]byte("not a zip")); err == nil {
		t.Error("not a zip")
	}
}

// OpenSSL's smime decrypts and verifies what Perfuse protects, as a HISP or another edge system would.
func TestOpenSSLOpensWhatPerfuseSends(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("no openssl")
	}
	trust := newAnchor(t, "Anchor")
	alice := trust.issue(t, "alice@direct.hospital.example", false)
	bob := trust.issue(t, "bob@direct.clinic.example", false)
	raw, err := Protect(Message{From: "alice@direct.hospital.example", To: []string{"bob@direct.clinic.example"}, Subject: "Hi",
		Text: "Interop."}, alice, nil, []*x509.Certificate{bob.Certificate})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w := func(n string, b []byte) string { f := filepath.Join(dir, n); _ = os.WriteFile(f, b, 0o600); return f }
	msg := w("msg.eml", raw)
	cert := w("bob.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: bob.Certificate.Raw}))
	key := w("bob.key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(bob.Key.(*rsa.PrivateKey))}))
	root := w("root.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: trust.cert.Raw}))
	var out, errb bytes.Buffer
	cmd := exec.Command("openssl", "smime", "-decrypt", "-in", msg, "-recip", cert, "-inkey", key)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("decrypt: %v %s", err, errb.String())
	}
	signed := w("signed.eml", out.Bytes())
	out.Reset()
	errb.Reset()
	cmd = exec.Command("openssl", "smime", "-verify", "-in", signed, "-CAfile", root, "-purpose", "any")
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil || !strings.Contains(errb.String(), "Verification successful") || !bytes.Contains(out.Bytes(), []byte("Interop.")) {
		t.Fatalf("verify: %v %s %s", err, errb.String(), out.String())
	}
}

// A CERT record served by a stub DNS server is found for the address, and a domain-bound one for its domain.
func TestCertificatesAreFoundInDNS(t *testing.T) {
	trust := newAnchor(t, "Anchor")
	addr := trust.issue(t, "bob@direct.clinic.example", false)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			name := ""
			for off := 12; q[off] != 0; off += 1 + int(q[off]) {
				name += string(q[off+1:off+1+int(q[off])]) + "."
			}
			resp := append([]byte{}, q...)
			resp[2], resp[3] = 0x81, 0x80
			if name != "bob.direct.clinic.example." {
				resp[3] = 0x83 // NXDOMAIN
				_, _ = pc.WriteTo(resp, from)
				continue
			}
			binary.BigEndian.PutUint16(resp[6:], 1)
			rd := append([]byte{0, 1, 0, 0, 0}, addr.Certificate.Raw...)
			resp = append(resp, 0xC0, 12)
			resp = binary.BigEndian.AppendUint16(resp, typeCERT)
			resp = binary.BigEndian.AppendUint16(resp, 1)
			resp = binary.BigEndian.AppendUint32(resp, 300)
			resp = binary.BigEndian.AppendUint16(resp, uint16(len(rd)))
			resp = append(resp, rd...)
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	certs, err := DNSCertificates(context.Background(), "bob@direct.clinic.example", pc.LocalAddr().String())
	if err != nil || len(certs) != 1 || !certs[0].Equal(addr.Certificate) {
		t.Fatalf("%v %d", err, len(certs))
	}
	certs, err = DNSCertificates(context.Background(), "nobody@nowhere.example", pc.LocalAddr().String())
	if err != nil || len(certs) != 0 {
		t.Errorf("an unknown address: %v %d", err, len(certs))
	}
}

func FuzzOpen(f *testing.F) {
	f.Add([]byte("From: a@b\r\nContent-Type: application/pkcs7-mime\r\n\r\nMIIB\r\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Open(b, nil, x509.NewCertPool())
		_, _ = ReadXDM(b)
		_, _ = parseCERT(b, 0)
	})
}
