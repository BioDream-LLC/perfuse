package dsdr

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"
)

// testPKI is a root CA, a signer certificate naming an OCSP responder, a running responder, and a time-stamping authority.
type testPKI struct {
	root, signer *x509.Certificate
	rootKey      crypto.Signer
	signerKey    crypto.Signer
	ocspURL      string
	tsaURL       string
	revoked      bool
}

func newTestPKI(t *testing.T, ec bool) *testPKI {
	t.Helper()
	p := &testPKI{}
	rootKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Now()
	rootTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Healthcare CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, _ := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	p.root, _ = x509.ParseCertificate(der)
	p.rootKey = rootKey

	ocspSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		tmpl := ocsp.Response{Status: ocsp.Good, SerialNumber: req.SerialNumber, ThisUpdate: time.Now(), NextUpdate: time.Now().Add(time.Hour)}
		if p.revoked {
			tmpl.Status, tmpl.RevokedAt = ocsp.Revoked, time.Now().Add(-time.Minute)
		}
		resp, _ := ocsp.CreateResponse(p.root, p.root, tmpl, p.rootKey)
		_, _ = w.Write(resp)
	}))
	t.Cleanup(ocspSrv.Close)
	p.ocspURL = ocspSrv.URL

	var pub crypto.PublicKey
	if ec {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		p.signerKey, pub = k, &k.PublicKey
	} else {
		k, _ := rsa.GenerateKey(rand.Reader, 2048)
		p.signerKey, pub = k, &k.PublicKey
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "Pat Clinician"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment,
		OCSPServer: []string{ocspSrv.URL}}
	der, _ = x509.CreateCertificate(rand.Reader, leaf, p.root, pub, rootKey)
	p.signer, _ = x509.ParseCertificate(der)

	// A time-stamping authority that answers with a token carrying the right imprint. It does not sign it: Perfuse does not check
	// the TSA's signature, and the real-TSA test uses OpenSSL's.
	tsa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req timeStampReq
		if _, err := asn1.Unmarshal(body, &req); err != nil {
			w.WriteHeader(400)
			return
		}
		info, _ := asn1.Marshal(struct {
			Version        int
			Policy         asn1.ObjectIdentifier
			MessageImprint messageImprint
			Serial         *big.Int
			GenTime        time.Time `asn1:"generalized"`
			Nonce          *big.Int
		}{1, asn1.ObjectIdentifier{1, 2, 3}, req.MessageImprint, big.NewInt(7), time.Now().UTC().Truncate(time.Second), req.Nonce})
		sd, _ := asn1.Marshal(struct {
			Version int
			Digests asn1.RawValue
			Encap   struct {
				Type    asn1.ObjectIdentifier
				Content []byte `asn1:"explicit,tag:0"`
			}
			Signers asn1.RawValue
		}{Version: 3, Digests: asn1.RawValue{Class: 0, Tag: 17, IsCompound: true},
			Encap: struct {
				Type    asn1.ObjectIdentifier
				Content []byte `asn1:"explicit,tag:0"`
			}{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}, info},
			Signers: asn1.RawValue{Class: 0, Tag: 17, IsCompound: true}})
		token, _ := asn1.Marshal(struct {
			Type    asn1.ObjectIdentifier
			Content asn1.RawValue
		}{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}, asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: sd}})
		resp, _ := asn1.Marshal(struct {
			Status pkiStatusInfo
			Token  asn1.RawValue
		}{pkiStatusInfo{Status: 0}, asn1.RawValue{FullBytes: token}})
		w.Header().Set("Content-Type", "application/timestamp-reply")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(tsa.Close)
	p.tsaURL = tsa.URL
	_ = sha256.New
	return p
}

func (p *testPKI) options() Options {
	return Options{Key: p.signerKey, Chain: []*x509.Certificate{p.signer, p.root}, Role: "207R00000X", RoleDisplay: "Internal Medicine",
		TSAURL: p.tsaURL, NPI: "1234567893"}
}

func (p *testPKI) roots() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(p.root)
	return pool
}
