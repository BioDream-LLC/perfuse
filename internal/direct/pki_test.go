package direct

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
}

func makeCA(t *testing.T, name string, parent *testCA, aia string) testCA {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	signer, signKey := tpl, k
	if parent != nil {
		signer, signKey = parent.cert, parent.key
		if aia != "" {
			tpl.IssuingCertificateURL = []string{aia}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, signer, &k.PublicKey, signKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return testCA{c, k}
}

func (ca testCA) leaf(t *testing.T, serial int64, address, aia, crl string) *x509.Certificate {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: address}, EmailAddresses: []string{address},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	if aia != "" {
		tpl.IssuingCertificateURL = []string{aia}
	}
	if crl != "" {
		tpl.CRLDistributionPoints = []string{crl}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &k.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

// The checks ONC's certificate discovery tool makes in D17 and D18: a certificate revoked on its issuer's CRL is refused,
// and one whose intermediate issuer is only reachable through its AIA extension is accepted once that is fetched.
func TestRecipientCertificatesAreCheckedForRevocationAndIssuers(t *testing.T) {
	root := makeCA(t, "Root", nil, "")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	inter := makeCA(t, "Intermediate", &root, "")
	mux.HandleFunc("/inter.cer", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(inter.cert.Raw) })
	crl, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().Add(-time.Minute),
		NextUpdate: time.Now().Add(time.Hour), RevokedCertificateEntries: []x509.RevocationListEntry{{SerialNumber: big.NewInt(66), RevocationTime: time.Now().Add(-time.Minute)}}},
		root.cert, root.key)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("/root.crl", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(crl) })
	pool := x509.NewCertPool()
	pool.AddCert(root.cert)
	k := &Checker{Anchors: pool}
	ctx := context.Background()

	if err := k.Check(ctx, root.leaf(t, 65, "ok@direct.example", "", srv.URL+"/root.crl")); err != nil {
		t.Errorf("an unrevoked certificate: %v", err)
	}
	if err := k.Check(ctx, root.leaf(t, 66, "gone@direct.example", "", srv.URL+"/root.crl")); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("a revoked certificate was accepted: %v", err)
	}
	if err := k.Check(ctx, root.leaf(t, 67, "down@direct.example", "", srv.URL+"/missing.crl")); err != nil {
		t.Errorf("a CRL that cannot be fetched should not stop delivery: %v", err)
	}
	if err := k.Check(ctx, inter.leaf(t, 68, "deep@direct.example", srv.URL+"/inter.cer", "")); err != nil {
		t.Errorf("the intermediate named by AIA was not fetched: %v", err)
	}
	if err := k.Check(ctx, inter.leaf(t, 69, "lost@direct.example", srv.URL+"/nothing.cer", "")); err == nil {
		t.Error("a certificate with no reachable issuer was accepted")
	}
	// A CRL signed by someone else is ignored, not believed.
	other := makeCA(t, "Other", nil, "")
	forged, _ := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().Add(-time.Minute),
		NextUpdate: time.Now().Add(time.Hour), RevokedCertificateEntries: []x509.RevocationListEntry{{SerialNumber: big.NewInt(70), RevocationTime: time.Now()}}},
		other.cert, other.key)
	mux.HandleFunc("/forged.crl", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(forged) })
	if err := k.Check(ctx, root.leaf(t, 70, "fine@direct.example", "", srv.URL+"/forged.crl")); err != nil {
		t.Errorf("a CRL its issuer did not sign was believed: %v", err)
	}
}

// ONC's certificate discovery tool answers SERVFAIL for an address-bound name it holds nothing for (D2): the domain-bound
// certificate must still be found.
func TestADomainCertificateIsFoundAfterAFailedAddressLookup(t *testing.T) {
	trust := newAnchor(t, "Anchor")
	dom := trust.issue(t, "anyone@direct.clinic.example", true)
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
			if name != "direct.clinic.example." {
				resp[3] = 0x82 // SERVFAIL
				_, _ = pc.WriteTo(resp, from)
				continue
			}
			binary.BigEndian.PutUint16(resp[6:], 1)
			rd := append([]byte{0, 1, 0, 0, 0}, dom.Certificate.Raw...)
			resp = append(resp, 0xC0, 12)
			resp = binary.BigEndian.AppendUint16(resp, typeCERT)
			resp = binary.BigEndian.AppendUint16(resp, 1)
			resp = binary.BigEndian.AppendUint32(resp, 300)
			resp = binary.BigEndian.AppendUint16(resp, uint16(len(rd)))
			resp = append(resp, rd...)
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	certs, err := DNSCertificates(context.Background(), "anyone@direct.clinic.example", pc.LocalAddr().String())
	if err != nil || len(certs) != 1 || !certs[0].Equal(dom.Certificate) {
		t.Fatalf("%v %d", err, len(certs))
	}
}
