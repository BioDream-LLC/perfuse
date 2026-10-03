package dsdr

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/biodream-llc/perfuse/internal/xmldsig"
)

// The long-term half of XAdES-X-L: what lets a payer verify a signature years after the certificate expired, without
// reaching anything on the network. XAdES builds it in layers, and the result names the last layer reached:
//
//	T    a time-stamp over the signature value, from a time-stamping authority
//	C    references to the CA certificates and the revocation data used
//	X    a time-stamp over the signature and those references
//	X-L  the certificates and revocation data themselves

// revocation is one OCSP response or CRL gathered for a certificate in the chain.
type revocation struct {
	ocsp     []byte
	ocspResp *ocsp.Response
	crl      []byte
	crlList  *x509.RevocationList
}

func (o *Options) longTerm(id, sigValue string, now time.Time) (string, string, []string) {
	var missing []string
	level := "EPES"

	var sigTS, refs, sigAndRefs []byte
	var sigTSXML, certRefsXML, revRefsXML, sigAndRefsXML string

	sigCanon, _ := xmldsig.CanonicaliseElement(wrap(sigValue), id+"-value", true, nil)
	if o.TSAURL == "" {
		missing = append(missing, "no time-stamping authority was configured, so there is no signature time-stamp (XAdES-T) and nothing above it")
	} else if tok, err := o.timestamp(sigCanon); err != nil {
		missing = append(missing, "the time-stamping authority did not give a time-stamp: "+err.Error())
	} else {
		sigTS = tok
		sigTSXML = timestampElement("SignatureTimeStamp", id+"-sigts", tok)
		level = "T"
	}

	// Revocation data for every certificate a CA issued: the signer and each intermediate.
	var revs []revocation
	revOK := len(o.Chain) > 1
	if len(o.Chain) == 1 {
		missing = append(missing, "the certificate is self-signed, so there is no certification path and no CA to ask whether it was revoked (ESMD-5, ESMD-6)")
	}
	for i := 0; i+1 < len(o.Chain); i++ {
		rv, err := o.revocationFor(o.Chain[i], o.Chain[i+1], now)
		if err != nil {
			missing = append(missing, fmt.Sprintf("no revocation status for %s: %v (ESMD-6)", o.Chain[i].Subject.CommonName, err))
			revOK = false
			continue
		}
		revs = append(revs, rv)
	}

	if revOK {
		certRefsXML = completeCertificateRefs(id, o.Chain[1:])
		revRefsXML = completeRevocationRefs(id, revs)
		if level == "T" {
			level = "C"
		}
	}

	if level == "C" {
		for _, frag := range []string{sigValue, sigTSXML, certRefsXML, revRefsXML} {
			c, err := xmldsig.Canonicalise(wrap(frag), true, nil)
			if err == nil {
				refs = append(refs, bytes.TrimSuffix(bytes.TrimPrefix(c, []byte("<w>")), []byte("</w>"))...)
			}
		}
		if tok, err := o.timestamp(refs); err != nil {
			missing = append(missing, "the time-stamping authority did not time-stamp the references (XAdES-X): "+err.Error())
		} else {
			sigAndRefs = tok
			sigAndRefsXML = timestampElement("SigAndRefsTimeStamp", id+"-refsts", tok)
			level = "X"
		}
	}

	var b strings.Builder
	b.WriteString(`<xades:UnsignedProperties><xades:UnsignedSignatureProperties>`)
	b.WriteString(sigTSXML)
	b.WriteString(certRefsXML)
	b.WriteString(revRefsXML)
	b.WriteString(sigAndRefsXML)
	if len(o.Chain) > 0 {
		b.WriteString(`<xades:CertificateValues>`)
		for _, c := range o.Chain {
			fmt.Fprintf(&b, `<xades:EncapsulatedX509Certificate>%s</xades:EncapsulatedX509Certificate>`, b64(c.Raw))
		}
		b.WriteString(`</xades:CertificateValues>`)
	}
	if len(revs) > 0 {
		b.WriteString(`<xades:RevocationValues>`)
		var crls, ocsps []string
		for _, r := range revs {
			if r.crl != nil {
				crls = append(crls, b64(r.crl))
			}
			if r.ocsp != nil {
				ocsps = append(ocsps, b64(r.ocsp))
			}
		}
		if len(crls) > 0 {
			b.WriteString(`<xades:CRLValues>`)
			for _, c := range crls {
				fmt.Fprintf(&b, `<xades:EncapsulatedCRLValue>%s</xades:EncapsulatedCRLValue>`, c)
			}
			b.WriteString(`</xades:CRLValues>`)
		}
		if len(ocsps) > 0 {
			b.WriteString(`<xades:OCSPValues>`)
			for _, c := range ocsps {
				fmt.Fprintf(&b, `<xades:EncapsulatedOCSPValue>%s</xades:EncapsulatedOCSPValue>`, c)
			}
			b.WriteString(`</xades:OCSPValues>`)
		}
		b.WriteString(`</xades:RevocationValues>`)
	}
	b.WriteString(`</xades:UnsignedSignatureProperties></xades:UnsignedProperties>`)

	if level == "X" && revOK {
		level = "X-L"
	}
	_ = sigTS
	_ = sigAndRefs
	if missing == nil {
		missing = []string{}
	}
	return b.String(), level, missing
}

func timestampElement(name, id string, token []byte) string {
	return fmt.Sprintf(`<xades:%s xmlns:xades="%s" xmlns:ds="%s" Id="%s"><ds:CanonicalizationMethod Algorithm="%s"></ds:CanonicalizationMethod>`+
		`<xades:EncapsulatedTimeStamp>%s</xades:EncapsulatedTimeStamp></xades:%s>`, name, nsXAdES, nsDSig, id, algExcC14N, b64(token), name)
}

func completeCertificateRefs(id string, cas []*x509.Certificate) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<xades:CompleteCertificateRefs xmlns:xades="%s" xmlns:ds="%s" Id="%s-certrefs"><xades:CertRefs>`, nsXAdES, nsDSig, id)
	for _, c := range cas {
		d := sha256.Sum256(c.Raw)
		fmt.Fprintf(&b, `<xades:Cert><xades:CertDigest><ds:DigestMethod Algorithm="%s"></ds:DigestMethod><ds:DigestValue>%s</ds:DigestValue>`+
			`</xades:CertDigest><xades:IssuerSerial><ds:X509IssuerName>%s</ds:X509IssuerName><ds:X509SerialNumber>%s</ds:X509SerialNumber>`+
			`</xades:IssuerSerial></xades:Cert>`, algSHA256, b64(d[:]), esc(c.Issuer.String()), c.SerialNumber)
	}
	b.WriteString(`</xades:CertRefs></xades:CompleteCertificateRefs>`)
	return b.String()
}

func completeRevocationRefs(id string, revs []revocation) string {
	var crl, oc strings.Builder
	for _, r := range revs {
		if r.crl != nil {
			d := sha256.Sum256(r.crl)
			fmt.Fprintf(&crl, `<xades:CRLRef><xades:DigestAlgAndValue><ds:DigestMethod Algorithm="%s"></ds:DigestMethod><ds:DigestValue>%s</ds:DigestValue>`+
				`</xades:DigestAlgAndValue><xades:CRLIdentifier><xades:Issuer>%s</xades:Issuer><xades:IssueTime>%s</xades:IssueTime></xades:CRLIdentifier></xades:CRLRef>`,
				algSHA256, b64(d[:]), esc(r.crlList.Issuer.String()), r.crlList.ThisUpdate.UTC().Format(time.RFC3339))
		}
		if r.ocsp != nil {
			d := sha256.Sum256(r.ocsp)
			responder := ""
			if len(r.ocspResp.ResponderKeyHash) > 0 {
				responder = `<xades:ByKey>` + b64(r.ocspResp.ResponderKeyHash) + `</xades:ByKey>`
			} else {
				var name pkix.RDNSequence
				_, _ = asn1.Unmarshal(r.ocspResp.RawResponderName, &name)
				responder = `<xades:ByName>` + esc(name.String()) + `</xades:ByName>`
			}
			fmt.Fprintf(&oc, `<xades:OCSPRef><xades:OCSPIdentifier><xades:ResponderID>%s</xades:ResponderID><xades:ProducedAt>%s</xades:ProducedAt>`+
				`</xades:OCSPIdentifier><xades:DigestAlgAndValue><ds:DigestMethod Algorithm="%s"></ds:DigestMethod><ds:DigestValue>%s</ds:DigestValue>`+
				`</xades:DigestAlgAndValue></xades:OCSPRef>`, responder, r.ocspResp.ProducedAt.UTC().Format(time.RFC3339), algSHA256, b64(d[:]))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<xades:CompleteRevocationRefs xmlns:xades="%s" xmlns:ds="%s" Id="%s-revrefs">`, nsXAdES, nsDSig, id)
	if crl.Len() > 0 {
		b.WriteString(`<xades:CRLRefs>` + crl.String() + `</xades:CRLRefs>`)
	}
	if oc.Len() > 0 {
		b.WriteString(`<xades:OCSPRefs>` + oc.String() + `</xades:OCSPRefs>`)
	}
	b.WriteString(`</xades:CompleteRevocationRefs>`)
	return b.String()
}

// revocationFor asks the certificate's OCSP responder, and falls back to its CRL.
func (o *Options) revocationFor(cert, issuer *x509.Certificate, now time.Time) (revocation, error) {
	var problems []string
	for _, url := range cert.OCSPServer {
		req, err := ocsp.CreateRequest(cert, issuer, &ocsp.RequestOptions{Hash: 0})
		if err != nil {
			return revocation{}, err
		}
		res, err := o.httpClient().Post(url, "application/ocsp-request", bytes.NewReader(req))
		if err != nil {
			problems = append(problems, "OCSP: "+err.Error())
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		_ = res.Body.Close()
		parsed, err := ocsp.ParseResponseForCert(raw, cert, issuer)
		if err != nil {
			problems = append(problems, "OCSP: "+err.Error())
			continue
		}
		if parsed.Status == ocsp.Revoked {
			return revocation{}, fmt.Errorf("the OCSP responder says it was revoked on %s", parsed.RevokedAt.Format(time.RFC3339))
		}
		if parsed.Status != ocsp.Good {
			problems = append(problems, "OCSP: the responder does not know the certificate")
			continue
		}
		return revocation{ocsp: raw, ocspResp: parsed}, nil
	}
	for _, url := range cert.CRLDistributionPoints {
		res, err := o.httpClient().Get(url)
		if err != nil {
			problems = append(problems, "CRL: "+err.Error())
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		_ = res.Body.Close()
		list, err := x509.ParseRevocationList(raw)
		if err != nil {
			problems = append(problems, "CRL: "+err.Error())
			continue
		}
		if err := list.CheckSignatureFrom(issuer); err != nil {
			problems = append(problems, "CRL: not signed by the issuer: "+err.Error())
			continue
		}
		for _, e := range list.RevokedCertificateEntries {
			if e.SerialNumber.Cmp(cert.SerialNumber) == 0 {
				return revocation{}, fmt.Errorf("the CRL lists it as revoked on %s", e.RevocationTime.Format(time.RFC3339))
			}
		}
		if !list.NextUpdate.IsZero() && now.After(list.NextUpdate) {
			problems = append(problems, "CRL: out of date")
			continue
		}
		return revocation{crl: raw, crlList: list}, nil
	}
	if len(problems) == 0 {
		return revocation{}, errors.New("the certificate names no OCSP responder or CRL")
	}
	return revocation{}, errors.New(strings.Join(problems, "; "))
}

// RFC 3161.

var oidSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}

type messageImprint struct {
	HashAlgorithm pkix.AlgorithmIdentifier
	HashedMessage []byte
}

type timeStampReq struct {
	Version        int
	MessageImprint messageImprint
	Nonce          *big.Int `asn1:"optional"`
	CertReq        bool     `asn1:"optional"`
}

type pkiStatusInfo struct {
	Status       int
	StatusString []asn1.RawValue `asn1:"optional"`
	FailInfo     asn1.BitString  `asn1:"optional"`
}

type timeStampResp struct {
	Status pkiStatusInfo
	Token  asn1.RawValue `asn1:"optional"`
}

func (o *Options) timestamp(data []byte) ([]byte, error) {
	sum := sha256.Sum256(data)
	nonce, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	req, err := asn1.Marshal(timeStampReq{Version: 1, MessageImprint: messageImprint{
		HashAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}, HashedMessage: sum[:]},
		Nonce: nonce, CertReq: true})
	if err != nil {
		return nil, err
	}
	res, err := o.httpClient().Post(o.TSAURL, "application/timestamp-query", bytes.NewReader(req))
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", res.Status)
	}
	var resp timeStampResp
	if _, err := asn1.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("not a time-stamp response: %w", err)
	}
	if resp.Status.Status > 1 || len(resp.Token.FullBytes) == 0 {
		return nil, fmt.Errorf("refused, status %d", resp.Status.Status)
	}
	info, err := parseTimestamp(resp.Token.FullBytes)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(info.imprint, sum[:]) {
		return nil, errors.New("the time-stamp is for different data")
	}
	if info.nonce != nil && info.nonce.Cmp(nonce) != 0 {
		return nil, errors.New("the time-stamp answers a different request")
	}
	return resp.Token.FullBytes, nil
}

// tstInfo is the part of a time-stamp token that says what was stamped and when.
type tstInfo struct {
	imprint []byte
	genTime time.Time
	nonce   *big.Int
}

// parseTimestamp reads a TimeStampToken: a CMS SignedData whose content is a TSTInfo. The TSA's own signature over it is not
// checked here; that needs the TSA's certificate and trust in it, which belong to the recipient's policy.
func parseTimestamp(token []byte) (*tstInfo, error) {
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(token, &ci); err != nil {
		return nil, fmt.Errorf("the time-stamp token is not CMS: %w", err)
	}
	// SignedData: version, digestAlgorithms, encapContentInfo, then optional certificates, CRLs, and the signer infos.
	sd, err := sequenceItems(ci.Content.Bytes)
	if err != nil || len(sd) < 3 {
		return nil, errors.New("the time-stamp token is not SignedData")
	}
	var encap struct {
		Type    asn1.ObjectIdentifier
		Content []byte `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(sd[2].FullBytes, &encap); err != nil {
		return nil, fmt.Errorf("the time-stamp token has no content: %w", err)
	}
	// TSTInfo: version, policy, messageImprint, serialNumber, genTime, then optional accuracy, ordering, nonce, tsa, extensions.
	info, err := sequenceItems(encap.Content)
	if err != nil || len(info) < 5 {
		return nil, errors.New("the time-stamp token carries no TSTInfo")
	}
	var mi messageImprint
	if _, err := asn1.Unmarshal(info[2].FullBytes, &mi); err != nil {
		return nil, fmt.Errorf("the time-stamp has no message imprint: %w", err)
	}
	var gen time.Time
	if _, err := asn1.UnmarshalWithParams(info[4].FullBytes, &gen, "generalized"); err != nil {
		return nil, fmt.Errorf("the time-stamp has no time: %w", err)
	}
	out := &tstInfo{imprint: mi.HashedMessage, genTime: gen}
	for _, v := range info[5:] {
		if v.Class == asn1.ClassUniversal && v.Tag == asn1.TagInteger {
			n := new(big.Int)
			if _, err := asn1.Unmarshal(v.FullBytes, &n); err == nil {
				out.nonce = n
			}
		}
	}
	return out, nil
}

// sequenceItems splits a DER SEQUENCE's contents into its elements.
func sequenceItems(der []byte) ([]asn1.RawValue, error) {
	var seq asn1.RawValue
	if _, err := asn1.Unmarshal(der, &seq); err != nil {
		return nil, err
	}
	var out []asn1.RawValue
	rest := seq.Bytes
	for len(rest) > 0 {
		var v asn1.RawValue
		next, err := asn1.Unmarshal(rest, &v)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		rest = next
	}
	return out, nil
}
