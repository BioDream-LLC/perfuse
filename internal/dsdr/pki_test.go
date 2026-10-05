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
	"strings"
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
	tsaCert      *x509.Certificate
	tsaKey       *rsa.PrivateKey
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

	// A time-stamping authority, with its own certificate from the test CA, that signs its tokens as RFC 3161 says. It used not to
	// sign them at all, because Perfuse did not check the TSA's signature; the EU DSS validator refused those time-stamps, which is
	// how that gap was found.
	tsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	tsaTmpl := &x509.Certificate{SerialNumber: big.NewInt(77), Subject: pkix.Name{CommonName: "Test Time-Stamping Authority"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		OCSPServer: []string{p.ocspURL},
		// RFC 3161 requires the timeStamping extended key usage, critical; Go's template cannot mark it so, hence the raw form.
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 37}, Critical: true,
			Value: mustMarshal([]asn1.ObjectIdentifier{{1, 3, 6, 1, 5, 5, 7, 3, 8}})}}}
	tsaDER, _ := x509.CreateCertificate(rand.Reader, tsaTmpl, p.root, &tsaKey.PublicKey, rootKey)
	p.tsaCert, _ = x509.ParseCertificate(tsaDER)
	p.tsaKey = tsaKey
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
		sd := p.signTSTInfo(t, info)
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

// signTSTInfo wraps a TSTInfo in a CMS SignedData signed by the test TSA, with the signed attributes RFC 3161 and RFC 5816 ask
// for: content type, message digest and the ESS signing certificate.
func (p *testPKI) signTSTInfo(t *testing.T, info []byte) []byte {
	t.Helper()
	sha := asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	tstOID := asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	digest := sha256.Sum256(info)
	certHash := sha256.Sum256(p.tsaCert.Raw)
	attr := func(oid asn1.ObjectIdentifier, value any) []byte {
		v, _ := asn1.Marshal(value)
		out, _ := asn1.Marshal(struct {
			Type   asn1.ObjectIdentifier
			Values asn1.RawValue
		}{oid, asn1.RawValue{Class: 0, Tag: 17, IsCompound: true, Bytes: v}})
		return out
	}
	essCert, _ := asn1.Marshal(struct{ Certs []struct{ Hash []byte } }{[]struct{ Hash []byte }{{certHash[:]}}})
	var attrs []byte
	attrs = append(attrs, attr(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}, tstOID)...)
	attrs = append(attrs, attr(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}, digest[:])...)
	attrs = append(attrs, attr(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 47}, asn1.RawValue{FullBytes: essCert})...)
	setDER, _ := asn1.Marshal(asn1.RawValue{Class: 0, Tag: 17, IsCompound: true, Bytes: attrs})
	h := sha256.Sum256(setDER)
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.tsaKey, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := asn1.Marshal(p.tsaCert.SerialNumber)
	ias, _ := asn1.Marshal(struct {
		Issuer asn1.RawValue
		Serial asn1.RawValue
	}{asn1.RawValue{FullBytes: p.tsaCert.RawIssuer}, asn1.RawValue{FullBytes: serial}})
	shaAlg, _ := asn1.Marshal(struct{ Algorithm asn1.ObjectIdentifier }{sha})
	rsaAlg, _ := asn1.Marshal(struct {
		Algorithm asn1.ObjectIdentifier
		Params    asn1.RawValue
	}{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}, asn1.NullRawValue})
	sigOctets, _ := asn1.Marshal(sig)
	var si []byte
	v1, _ := asn1.Marshal(1)
	si = append(si, v1...)
	si = append(si, ias...)
	si = append(si, shaAlg...)
	si = append(si, append([]byte{0xA0}, setDER[1:]...)...)
	si = append(si, rsaAlg...)
	si = append(si, sigOctets...)
	siDER, _ := asn1.Marshal(asn1.RawValue{Class: 0, Tag: 16, IsCompound: true, Bytes: si})
	encap, _ := asn1.Marshal(struct {
		Type    asn1.ObjectIdentifier
		Content []byte `asn1:"explicit,tag:0"`
	}{tstOID, info})
	v3, _ := asn1.Marshal(3)
	var body []byte
	body = append(body, v3...)
	digests, _ := asn1.Marshal(asn1.RawValue{Class: 0, Tag: 17, IsCompound: true, Bytes: shaAlg})
	body = append(body, digests...)
	body = append(body, encap...)
	certs, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: p.tsaCert.Raw})
	body = append(body, certs...)
	signers, _ := asn1.Marshal(asn1.RawValue{Class: 0, Tag: 17, IsCompound: true, Bytes: siDER})
	body = append(body, signers...)
	out, _ := asn1.Marshal(asn1.RawValue{Class: 0, Tag: 16, IsCompound: true, Bytes: body})
	return out
}

func TestATimeStampMustBeSignedByATrustedTSA(t *testing.T) {
	p := newTestPKI(t, false)
	info, _ := asn1.Marshal(struct {
		Version int
		Policy  asn1.ObjectIdentifier
	}{1, asn1.ObjectIdentifier{1, 2, 3}})
	wrap := func(sd []byte) []byte {
		tok, _ := asn1.Marshal(struct {
			Type    asn1.ObjectIdentifier
			Content asn1.RawValue
		}{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}, asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: sd}})
		return tok
	}
	good := wrap(p.signTSTInfo(t, info))
	if _, err := verifyTimestampSignature(good, p.roots(), time.Now()); err != nil {
		t.Fatalf("a properly signed time-stamp: %v", err)
	}
	bad := append([]byte(nil), good...)
	bad[len(bad)-1] ^= 0xff
	if _, err := verifyTimestampSignature(bad, p.roots(), time.Now()); err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("a time-stamp with a broken TSA signature: %v", err)
	}
	if _, err := verifyTimestampSignature(good, x509.NewCertPool(), time.Now()); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("a TSA nobody trusts: %v", err)
	}
}

func mustMarshal(v any) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
