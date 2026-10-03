package dsdr

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // only to read signatures that used it, which are reported as weak
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"strings"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/biodream-llc/perfuse/internal/xmldsig"
)

// Report is what checking one signature found.
type Report struct {
	Participant string    `json:"participant"`
	Thumbnail   string    `json:"thumbnail"`
	Signer      string    `json:"signer"`
	Issuer      string    `json:"issuer"`
	SigningTime time.Time `json:"signingTime"`
	Role        string    `json:"role"`
	Purpose     string    `json:"purpose"`
	Level       string    `json:"level"`

	// DigestValid: the document, outside the signers, is as it was signed.
	DigestValid bool `json:"digestValid"`
	// SignatureValid: the key in the certificate made the signature.
	SignatureValid bool `json:"signatureValid"`
	// PropertiesValid: the signing time, role and purpose are covered by the signature, and name this certificate.
	PropertiesValid bool `json:"propertiesValid"`
	// TrustChecked and Trusted: the certificate chains to a root the caller trusts, as of the signing time.
	TrustChecked bool `json:"trustChecked"`
	Trusted      bool `json:"trusted"`
	// Revocation is what the embedded OCSP response or CRL says about the signing certificate.
	Revocation string `json:"revocation"`
	// TimeStamped is the time-stamping authority's time for the signature, when there is a time-stamp.
	TimeStamped *time.Time `json:"timeStamped,omitempty"`

	Problems []string `json:"problems"`
	Notes    []string `json:"notes"`
}

// Sound is whether the signature holds: integrity, origin, its own properties, and trust when trust was checked.
func (r Report) Sound() bool {
	return r.DigestValid && r.SignatureValid && r.PropertiesValid && (!r.TrustChecked || r.Trusted) &&
		!strings.HasPrefix(r.Revocation, "revoked")
}

// VerifyOptions controls verification.
type VerifyOptions struct {
	// Roots is what the recipient trusts. Nil checks everything but trust, and the report says so.
	Roots *x509.CertPool
}

// Verify checks every DSDR signature in a CDA document. No signatures is not an error: an empty list.
func Verify(doc []byte, opts VerifyOptions) ([]Report, error) {
	texts, err := findSignatureTexts(doc)
	if err != nil {
		return nil, err
	}
	reports := []Report{}
	for _, st := range texts {
		reports = append(reports, verifyOne(doc, st, opts))
	}
	return reports, nil
}

type signatureText struct {
	participant, mediaType, representation string
	thumbnail, thumbMedia, thumbRep        string
	content                                string
}

func findSignatureTexts(doc []byte) ([]signatureText, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = true
	var out []signatureText
	depth := 0
	participant := ""
	var cur *signatureText
	inThumb := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("dsdr: the document is not well-formed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 {
				participant = ""
				if t.Name.Space == NSCDA && (t.Name.Local == "legalAuthenticator" || t.Name.Local == "authenticator") {
					participant = t.Name.Local
				}
			}
			if depth == 3 && participant != "" && t.Name.Space == NSSDTC && t.Name.Local == "signatureText" {
				cur = &signatureText{participant: participant}
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "mediaType":
						cur.mediaType = a.Value
					case "representation":
						cur.representation = a.Value
					}
				}
			}
			if depth == 4 && cur != nil && t.Name.Local == "thumbnail" {
				inThumb = true
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "mediaType":
						cur.thumbMedia = a.Value
					case "representation":
						cur.thumbRep = a.Value
					}
				}
			}
		case xml.CharData:
			if cur != nil {
				if inThumb {
					cur.thumbnail += string(t)
				} else if depth == 3 {
					cur.content += string(t)
				}
			}
		case xml.EndElement:
			if depth == 4 && inThumb {
				inThumb = false
			}
			if depth == 3 && cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			depth--
		}
	}
	return out, nil
}

// The parts of a signature read by structure. encoding/xml matches these by local name, so the prefix a signer chose does not matter.
type xSignature struct {
	SignedInfo struct {
		C14N      xAlg `xml:"CanonicalizationMethod"`
		SigMethod xAlg `xml:"SignatureMethod"`
		Refs      []struct {
			ID         string `xml:"Id,attr"`
			URI        string `xml:"URI,attr"`
			Type       string `xml:"Type,attr"`
			Transforms []struct {
				Algorithm string `xml:"Algorithm,attr"`
				XPath     []struct {
					Filter string `xml:"Filter,attr"`
					Expr   string `xml:",chardata"`
				} `xml:"XPath"`
			} `xml:"Transforms>Transform"`
			DigestMethod xAlg   `xml:"DigestMethod"`
			DigestValue  string `xml:"DigestValue"`
		} `xml:"Reference"`
	} `xml:"SignedInfo"`
	SignatureValue string   `xml:"SignatureValue"`
	Certificates   []string `xml:"KeyInfo>X509Data>X509Certificate"`
	Props          struct {
		Signed struct {
			ID   string `xml:"Id,attr"`
			Time string `xml:"SignedSignatureProperties>SigningTime"`
			Cert []struct {
				DigestMethod xAlg   `xml:"CertDigest>DigestMethod"`
				DigestValue  string `xml:"CertDigest>DigestValue"`
			} `xml:"SignedSignatureProperties>SigningCertificate>Cert"`
			Policy  *struct{} `xml:"SignedSignatureProperties>SignaturePolicyIdentifier"`
			Roles   []string  `xml:"SignedSignatureProperties>SignerRole>ClaimedRoles>ClaimedRole"`
			Purpose string    `xml:"SignedSignatureProperties>SignaturePurpose"`
			Commit  []string  `xml:"SignedDataObjectProperties>CommitmentTypeIndication>CommitmentTypeId>Identifier"`
		} `xml:"SignedProperties"`
		Unsigned struct {
			SigTS      []string  `xml:"UnsignedSignatureProperties>SignatureTimeStamp>EncapsulatedTimeStamp"`
			CertRefs   *struct{} `xml:"UnsignedSignatureProperties>CompleteCertificateRefs"`
			RevRefs    *struct{} `xml:"UnsignedSignatureProperties>CompleteRevocationRefs"`
			RefsTS     []string  `xml:"UnsignedSignatureProperties>SigAndRefsTimeStamp>EncapsulatedTimeStamp"`
			CertValues []string  `xml:"UnsignedSignatureProperties>CertificateValues>EncapsulatedX509Certificate"`
			OCSP       []string  `xml:"UnsignedSignatureProperties>RevocationValues>OCSPValues>EncapsulatedOCSPValue"`
			CRL        []string  `xml:"UnsignedSignatureProperties>RevocationValues>CRLValues>EncapsulatedCRLValue"`
		} `xml:"UnsignedProperties"`
	} `xml:"Object>QualifyingProperties"`
}

type xAlg struct {
	Algorithm string `xml:"Algorithm,attr"`
}

func verifyOne(doc []byte, st signatureText, opts VerifyOptions) Report {
	r := Report{Participant: st.participant, Thumbnail: strings.TrimSpace(st.thumbnail), Problems: []string{}, Notes: []string{}}
	problem := func(f string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(f, a...)) }
	note := func(f string, a ...any) { r.Notes = append(r.Notes, fmt.Sprintf(f, a...)) }

	if st.representation != "B64" {
		problem("signatureText is not base64 (representation %q, ESMD-12)", st.representation)
	}
	switch st.mediaType {
	case "application":
	case "text/xml":
		note("signatureText says mediaType text/xml, as the guide's figures and the C-CDA example do; its conformance statement ESMD-13 says application")
	default:
		problem("signatureText has mediaType %q; the guide says application (ESMD-13)", st.mediaType)
	}
	if r.Thumbnail == "" {
		problem("there is no thumbnail describing the signature in words (ESMD-16)")
	} else if st.thumbMedia != "text/plain" || st.thumbRep != "TXT" {
		problem("the thumbnail is not plain text (ESMD-14, ESMD-15)")
	}

	content, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(st.content), ""))
	if err != nil {
		problem("the signature is not valid base64: %v", err)
		return r
	}
	if bytes.Contains(content, []byte("delegatedSigner")) {
		note("this is a delegated signature; Perfuse checks the delegated signer's signature and does not verify the delegation of rights")
	} else if !bytes.Contains(content, []byte("authorizedSigner")) {
		problem("the signature is not inside digitalSignature/authorizedSigner (ESMD-7)")
	}
	rawSig, err := xmldsig.Element(content, "Signature", nsDSig)
	if err != nil || rawSig == nil {
		problem("there is no XML signature inside the signatureText")
		return r
	}
	var sig xSignature
	if err := xml.Unmarshal(rawSig, &sig); err != nil {
		problem("the XML signature could not be read: %v", err)
		return r
	}
	if len(sig.Certificates) == 0 {
		problem("the signature carries no certificate (ESMD-1)")
		return r
	}
	var chain []*x509.Certificate
	for _, c := range append(sig.Certificates, sig.Props.Unsigned.CertValues...) {
		if der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c), "")); err == nil {
			if cert, err := x509.ParseCertificate(der); err == nil && !containsCert(chain, cert) {
				chain = append(chain, cert)
			}
		}
	}
	if len(chain) == 0 {
		problem("the signing certificate could not be read")
		return r
	}
	cert := chain[0]
	r.Signer, r.Issuer = cert.Subject.String(), cert.Issuer.String()

	exclusive := sig.SignedInfo.C14N.Algorithm != algC14N
	var propsRef, contentRef bool
	for _, ref := range sig.SignedInfo.Refs {
		h, weak := hashFor(ref.DigestMethod.Algorithm)
		if h == nil {
			problem("a reference uses the digest %s, which Perfuse does not support", ref.DigestMethod.Algorithm)
			continue
		}
		if weak {
			note("a reference uses SHA-1, which is no longer considered safe against forgery")
		}
		want, _ := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(ref.DigestValue), ""))
		switch {
		case ref.Type == typeSignedProperties || strings.HasSuffix(ref.Type, "#SignedProperties"):
			propsRef = true
			id := strings.TrimPrefix(ref.URI, "#")
			canon, err := xmldsig.CanonicaliseElement(content, id, refExclusive(ref.Transforms, exclusive), nil)
			if err != nil || id != sig.Props.Signed.ID {
				problem("the signed properties reference does not resolve to the signature's SignedProperties")
				continue
			}
			hh := h()
			hh.Write(canon)
			if !bytes.Equal(hh.Sum(nil), want) {
				problem("the signing time, role or purpose has been changed since signing")
				continue
			}
			r.PropertiesValid = true
		case ref.URI == "":
			contentRef = true
			ok := false
			for _, t := range ref.Transforms {
				for _, x := range t.XPath {
					if t.Algorithm == algFilter2 && x.Filter == "subtract" && strings.Contains(x.Expr, "legalAuthenticator") &&
						strings.Contains(x.Expr, "authenticator") {
						ok = true
					}
				}
			}
			if !ok {
				problem("the document reference does not leave out the legalAuthenticator and authenticator participants, which the guide requires (3.1.2) - or does so in a form Perfuse cannot evaluate")
				continue
			}
			got, err := contentDigest(doc, refExclusive(ref.Transforms, exclusive), h)
			if err != nil {
				problem("%v", err)
				continue
			}
			if !bytes.Equal(got, want) {
				problem("the document has been changed since it was signed")
				continue
			}
			r.DigestValid = true
		default:
			note("a reference to %q was not checked", ref.URI)
		}
	}
	if !contentRef {
		problem("the signature does not cover the document")
	}
	if !propsRef {
		problem("the signature does not cover its own properties, so the signing time, role and purpose could have been rewritten")
		r.PropertiesValid = false
	}

	// The signature over SignedInfo.
	canonSI, err := xmldsig.CanonicaliseNamed(content, "SignedInfo", nsDSig, exclusive)
	if err != nil {
		problem("SignedInfo could not be canonicalised: %v", err)
	} else if err := checkSignatureValue(sig.SignedInfo.SigMethod.Algorithm, canonSI, sig.SignatureValue, cert); err != nil {
		problem("%v", err)
	} else {
		r.SignatureValid = true
	}

	// The properties.
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(sig.Props.Signed.Time)); err == nil {
		r.SigningTime = t.UTC()
	} else {
		problem("there is no UTC signing time (ESMD-2)")
		r.PropertiesValid = false
	}
	if len(sig.Props.Signed.Cert) == 0 {
		problem("the signed properties do not name the signing certificate (ESMD-1)")
		r.PropertiesValid = false
	} else if h, _ := hashFor(sig.Props.Signed.Cert[0].DigestMethod.Algorithm); h != nil {
		hh := h()
		hh.Write(cert.Raw)
		want, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(sig.Props.Signed.Cert[0].DigestValue))
		if !bytes.Equal(hh.Sum(nil), want) {
			problem("the signed properties name a different certificate from the one carried, so who signed is not established")
			r.PropertiesValid = false
		}
	}
	if sig.Props.Signed.Policy == nil {
		problem("there is no SignaturePolicyIdentifier, which the guide requires")
	}
	if len(sig.Props.Signed.Roles) == 0 {
		problem("there is no signer role (ESMD-3)")
	} else {
		r.Role = strings.TrimSpace(sig.Props.Signed.Roles[0])
	}
	r.Purpose = strings.TrimSpace(sig.Props.Signed.Purpose)
	if r.Purpose == "" {
		for _, c := range sig.Props.Signed.Commit {
			for _, p := range Purposes {
				if p.OID != "" && strings.TrimSpace(c) == "urn:oid:"+p.OID {
					r.Purpose = p.Code + " - " + p.Display
				}
			}
		}
	}
	if r.Purpose == "" {
		problem("there is no signature purpose (ESMD-4)")
	}

	r.Level = levelOf(sig)
	at := r.SigningTime

	// Time-stamps: the TSA's time is better evidence of when than the signer's own clock.
	if len(sig.Props.Unsigned.SigTS) > 0 {
		sv, _ := xmldsig.CanonicaliseNamed(content, "SignatureValue", nsDSig, true)
		tok, _ := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(sig.Props.Unsigned.SigTS[0]), ""))
		info, err := parseTimestamp(tok)
		sum := sha256.Sum256(sv)
		switch {
		case err != nil:
			problem("the signature time-stamp could not be read: %v", err)
		case !bytes.Equal(info.imprint, sum[:]):
			problem("the signature time-stamp is not for this signature")
		default:
			t := info.genTime.UTC()
			r.TimeStamped = &t
			if !r.SigningTime.IsZero() && t.Before(r.SigningTime.Add(-5*time.Minute)) {
				problem("the time-stamp is earlier than the claimed signing time")
			}
			note("the time-stamping authority's own signature on its time-stamp is not checked by Perfuse")
		}
	}

	// Revocation, from what the signature carries - X-L's point is that nothing is fetched.
	if len(chain) > 1 {
		r.Revocation = revocationStatus(chain[0], chain[1], sig.Props.Unsigned.OCSP, sig.Props.Unsigned.CRL, at)
		if r.Revocation == "" {
			r.Revocation = "unknown: the signature carries no revocation data for the signing certificate"
		}
	} else {
		r.Revocation = "unknown: the certificate is self-signed"
	}

	if opts.Roots == nil {
		note("no trust store was given, so this establishes integrity and origin with the carried certificate, not that the signer is anybody in particular")
	} else {
		r.TrustChecked = true
		inter := x509.NewCertPool()
		for _, c := range chain[1:] {
			inter.AddCert(c)
		}
		if at.IsZero() {
			at = time.Now()
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: opts.Roots, Intermediates: inter, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			problem("the certificate does not chain to a trusted root as of the signing time: %v", err)
		} else {
			r.Trusted = true
		}
	}
	if r.Level != "X-L" {
		note("the signature is XAdES-%s; the guide asks for XAdES-X-L", r.Level)
	}
	return r
}

func containsCert(list []*x509.Certificate, c *x509.Certificate) bool {
	for _, x := range list {
		if x.Equal(c) {
			return true
		}
	}
	return false
}

func refExclusive(ts []struct {
	Algorithm string `xml:"Algorithm,attr"`
	XPath     []struct {
		Filter string `xml:"Filter,attr"`
		Expr   string `xml:",chardata"`
	} `xml:"XPath"`
}, def bool) bool {
	for _, t := range ts {
		switch t.Algorithm {
		case algExcC14N, algExcC14N + "WithComments":
			return true
		case algC14N, algC14N + "#WithComments":
			return false
		}
	}
	return def
}

func levelOf(s xSignature) string {
	u := s.Props.Unsigned
	level := "BES"
	if s.Props.Signed.Policy != nil {
		level = "EPES"
	}
	if len(u.SigTS) == 0 {
		return level
	}
	level = "T"
	if u.CertRefs == nil || u.RevRefs == nil {
		return level
	}
	level = "C"
	if len(u.RefsTS) == 0 {
		return level
	}
	level = "X"
	if len(u.CertValues) == 0 || len(u.OCSP)+len(u.CRL) == 0 {
		return level
	}
	return "X-L"
}

func hashFor(alg string) (func() hash.Hash, bool) {
	switch alg {
	case algSHA256:
		return sha256.New, false
	case algSHA512:
		return sha512.New, false
	case algSHA1:
		return sha1.New, true
	}
	return nil, false
}

func checkSignatureValue(alg string, signedInfo []byte, value string, cert *x509.Certificate) error {
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
	if err != nil {
		return fmt.Errorf("the signature value is not base64: %w", err)
	}
	var h crypto.Hash
	switch alg {
	case algRSASHA256, algECSHA256:
		h = crypto.SHA256
	case algRSASHA512:
		h = crypto.SHA512
	default:
		return fmt.Errorf("the signature algorithm %s is not supported", alg)
	}
	hh := h.New()
	hh.Write(signedInfo)
	sum := hh.Sum(nil)
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(pub, h, sum, raw); err != nil {
			return errors.New("the signature was not made by the key in the signing certificate")
		}
	case *ecdsa.PublicKey:
		// XML-DSig carries ECDSA as r and s side by side (RFC 4051), not DER.
		n := len(raw) / 2
		if n == 0 || !ecdsa.Verify(pub, sum, new(big.Int).SetBytes(raw[:n]), new(big.Int).SetBytes(raw[n:])) {
			return errors.New("the signature was not made by the key in the signing certificate")
		}
	default:
		return fmt.Errorf("the certificate's %T key cannot verify an XML signature", cert.PublicKey)
	}
	return nil
}

func revocationStatus(cert, issuer *x509.Certificate, ocsps, crls []string, at time.Time) string {
	for _, o := range ocsps {
		der, _ := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(o), ""))
		res, err := ocsp.ParseResponseForCert(der, cert, issuer)
		if err != nil {
			continue
		}
		switch res.Status {
		case ocsp.Good:
			return fmt.Sprintf("good: the CA's OCSP responder said so at %s", res.ProducedAt.UTC().Format(time.RFC3339))
		case ocsp.Revoked:
			if !at.IsZero() && at.Before(res.RevokedAt) {
				return fmt.Sprintf("good at signing: revoked later, on %s", res.RevokedAt.UTC().Format(time.RFC3339))
			}
			return fmt.Sprintf("revoked on %s, per the CA's OCSP responder", res.RevokedAt.UTC().Format(time.RFC3339))
		}
	}
	for _, c := range crls {
		der, _ := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c), ""))
		list, err := x509.ParseRevocationList(der)
		if err != nil || list.CheckSignatureFrom(issuer) != nil {
			continue
		}
		for _, e := range list.RevokedCertificateEntries {
			if e.SerialNumber.Cmp(cert.SerialNumber) == 0 {
				return fmt.Sprintf("revoked on %s, per the CA's CRL", e.RevocationTime.UTC().Format(time.RFC3339))
			}
		}
		return fmt.Sprintf("good: not on the CA's CRL issued %s", list.ThisUpdate.UTC().Format(time.RFC3339))
	}
	return ""
}
