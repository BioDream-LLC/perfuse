package dsdr

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/xmldsig"
)

// Sign adds a DSDR signature to a CDA document.
func Sign(doc []byte, o Options) (*Result, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Second)
	cert := o.Chain[0]

	root, err := scanRoot(doc)
	if err != nil {
		return nil, err
	}
	as := o.As
	if as == "" {
		as = "authenticator"
		if la := root.child("legalAuthenticator"); la != nil && !la.signed {
			as = "legalAuthenticator"
		}
	}
	if as == "legalAuthenticator" {
		la := root.child("legalAuthenticator")
		if la == nil {
			return nil, errors.New("dsdr: the document has no legalAuthenticator to sign as; sign as an authenticator, or add one")
		}
		if la.signed {
			return nil, errors.New("dsdr: the legalAuthenticator already carries a signature; sign as an authenticator")
		}
		if la.afterSignatureCode < 0 {
			return nil, errors.New("dsdr: the legalAuthenticator has no signatureCode, after which the signature belongs")
		}
	}
	purpose := PurposeByCode(o.Purpose)
	if purpose == nil {
		purpose = PurposeByCode(map[bool]string{true: "8.2.1.1", false: "8.2.1.2"}[as == "legalAuthenticator"])
	}
	name := strings.TrimSpace(o.SignerName)
	if name == "" {
		name = cert.Subject.CommonName
	}

	digest, err := contentDigest(doc, true, sha256.New)
	if err != nil {
		return nil, err
	}

	sigAlg, hash, err := signatureAlgorithm(o.Key)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("dsdr-%d", now.UnixNano())
	props := signedProperties(id, now, o.Chain[0], o.Role, o.RoleDisplay, purpose)
	propsCanon, err := xmldsig.CanonicaliseElement(wrap(props), id+"-props", true, nil)
	if err != nil {
		return nil, fmt.Errorf("dsdr: canonicalising the signed properties: %w", err)
	}
	propsDigest := sha256.Sum256(propsCanon)

	signedInfo := fmt.Sprintf(`<ds:SignedInfo xmlns:ds="%s">`+
		`<ds:CanonicalizationMethod Algorithm="%s"></ds:CanonicalizationMethod>`+
		`<ds:SignatureMethod Algorithm="%s"></ds:SignatureMethod>`+
		`<ds:Reference Id="%s-ref" URI=""><ds:Transforms>`+
		`<ds:Transform Algorithm="%s"><dsig-xpath:XPath xmlns:dsig-xpath="%s" Filter="subtract">%s</dsig-xpath:XPath></ds:Transform>`+
		`<ds:Transform Algorithm="%s"></ds:Transform></ds:Transforms>`+
		`<ds:DigestMethod Algorithm="%s"></ds:DigestMethod><ds:DigestValue>%s</ds:DigestValue></ds:Reference>`+
		`<ds:Reference Type="%s" URI="#%s-props"><ds:Transforms><ds:Transform Algorithm="%s"></ds:Transform></ds:Transforms>`+
		`<ds:DigestMethod Algorithm="%s"></ds:DigestMethod><ds:DigestValue>%s</ds:DigestValue></ds:Reference>`+
		`</ds:SignedInfo>`,
		nsDSig, algExcC14N, sigAlg, id, algFilter2, algFilter2, esc(excludeParticipants), algExcC14N, algSHA256, b64(digest),
		typeSignedProperties, id, algExcC14N, algSHA256, b64(propsDigest[:]))
	siCanon, err := xmldsig.CanonicaliseNamed(wrap(signedInfo), "SignedInfo", nsDSig, true)
	if err != nil {
		return nil, fmt.Errorf("dsdr: canonicalising SignedInfo: %w", err)
	}
	h := hash.New()
	h.Write(siCanon)
	sigBytes, err := o.Key.Sign(rand.Reader, h.Sum(nil), hash)
	if err != nil {
		return nil, fmt.Errorf("dsdr: signing: %w", err)
	}
	if pub, ok := o.Key.Public().(*ecdsa.PublicKey); ok {
		// XML-DSig carries ECDSA as r and s side by side, each the size of the curve (RFC 4051), where Go produces DER.
		var rs struct{ R, S *big.Int }
		if _, err := asn1.Unmarshal(sigBytes, &rs); err != nil {
			return nil, fmt.Errorf("dsdr: reading the ECDSA signature: %w", err)
		}
		size := (pub.Curve.Params().BitSize + 7) / 8
		sigBytes = append(rs.R.FillBytes(make([]byte, size)), rs.S.FillBytes(make([]byte, size))...)
	}
	sigValue := fmt.Sprintf(`<ds:SignatureValue xmlns:ds="%s" Id="%s-value">%s</ds:SignatureValue>`, nsDSig, id, b64(sigBytes))

	unsigned, level, missing := o.longTerm(id, sigValue, now)

	var sig strings.Builder
	fmt.Fprintf(&sig, `<ds:Signature xmlns:ds="%s" Id="%s">`, nsDSig, id)
	sig.WriteString(signedInfo)
	sig.WriteString(sigValue)
	sig.WriteString(`<ds:KeyInfo><ds:X509Data>`)
	for _, c := range o.Chain {
		fmt.Fprintf(&sig, `<ds:X509Certificate>%s</ds:X509Certificate>`, b64(c.Raw))
	}
	sig.WriteString(`</ds:X509Data></ds:KeyInfo>`)
	fmt.Fprintf(&sig, `<ds:Object><xades:QualifyingProperties xmlns:xades="%s" Target="#%s">`, nsXAdES, id)
	sig.WriteString(props)
	sig.WriteString(unsigned)
	sig.WriteString(`</xades:QualifyingProperties></ds:Object></ds:Signature>`)

	content := fmt.Sprintf(`<digitalSignature xmlns="%s"><authorizedSigner>%s</authorizedSigner></digitalSignature>`, NSSDTC, sig.String())

	role := o.Role
	if o.RoleDisplay != "" {
		role = o.RoleDisplay + " (" + o.Role + ")"
	}
	thumb := fmt.Sprintf("Digitally signed by Authorized Signer %s on %s at %s UTC as %s for the purpose of %s.",
		name, now.Format("2006-01-02"), now.Format("15:04"), role, purpose.Display)

	p := root.prefix
	sigText := fmt.Sprintf(`<sdtc:signatureText xmlns:sdtc="%s" mediaType="application" representation="B64">`+
		`<%sthumbnail mediaType="text/plain" representation="TXT">%s</%sthumbnail>%s</sdtc:signatureText>`,
		NSSDTC, p, esc(thumb), p, b64([]byte(content)))

	var out []byte
	if as == "legalAuthenticator" {
		at := root.child("legalAuthenticator").afterSignatureCode
		out = splice(doc, at, sigText)
	} else {
		var person strings.Builder
		fmt.Fprintf(&person, `<%sauthenticator><%stime value="%s"/><%ssignatureCode code="S"/>%s<%sassignedEntity>`,
			p, p, now.Format("20060102150405-0700"), p, sigText, p)
		if o.NPI != "" {
			fmt.Fprintf(&person, `<%sid root="2.16.840.1.113883.4.6" extension="%s"/>`, p, esc(o.NPI))
		} else {
			fmt.Fprintf(&person, `<%sid nullFlavor="UNK"/>`, p)
		}
		fmt.Fprintf(&person, `<%sassignedPerson><%sname>%s</%sname></%sassignedPerson></%sassignedEntity></%sauthenticator>`,
			p, p, personName(p, name), p, p, p, p)
		out = splice(doc, root.authenticatorAt(), person.String())
	}

	// The digest must not have moved: the participants are outside it, which is the property every later signer depends on.
	check, err := contentDigest(out, true, sha256.New)
	if err != nil || !bytes.Equal(check, digest) {
		return nil, errors.New("dsdr: adding the signature changed the signed content; this is a fault in the signer, not the document")
	}
	return &Result{Document: out, Participant: as, Level: level, Conforms: level == "X-L", Missing: missing, Thumbnail: thumb,
		SignedAt: now}, nil
}

func personName(p, name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return esc(name)
	}
	return fmt.Sprintf(`<%sgiven>%s</%sgiven><%sfamily>%s</%sfamily>`, p, esc(strings.Join(parts[:len(parts)-1], " ")), p, p,
		esc(parts[len(parts)-1]), p)
}

func signedProperties(id string, when time.Time, cert *x509.Certificate, role, roleDisplay string, purpose *Purpose) string {
	certDigest := sha256.Sum256(cert.Raw)
	var b strings.Builder
	fmt.Fprintf(&b, `<xades:SignedProperties xmlns:xades="%s" xmlns:ds="%s" Id="%s-props"><xades:SignedSignatureProperties>`,
		nsXAdES, nsDSig, id)
	fmt.Fprintf(&b, `<xades:SigningTime>%s</xades:SigningTime>`, when.Format(time.RFC3339))
	fmt.Fprintf(&b, `<xades:SigningCertificate><xades:Cert><xades:CertDigest><ds:DigestMethod Algorithm="%s"></ds:DigestMethod>`+
		`<ds:DigestValue>%s</ds:DigestValue></xades:CertDigest><xades:IssuerSerial><ds:X509IssuerName>%s</ds:X509IssuerName>`+
		`<ds:X509SerialNumber>%s</ds:X509SerialNumber></xades:IssuerSerial></xades:Cert></xades:SigningCertificate>`,
		algSHA256, b64(certDigest[:]), esc(cert.Issuer.String()), cert.SerialNumber.String())
	// The guide requires the element and allows the implied form, whose policy is the guide itself agreed between the parties.
	b.WriteString(`<xades:SignaturePolicyIdentifier><xades:SignaturePolicyImplied></xades:SignaturePolicyImplied></xades:SignaturePolicyIdentifier>`)
	claimed := role
	if roleDisplay != "" {
		claimed = role + " - " + roleDisplay
	}
	fmt.Fprintf(&b, `<xades:SignerRole><xades:ClaimedRoles><xades:ClaimedRole>%s</xades:ClaimedRole></xades:ClaimedRoles></xades:SignerRole>`,
		esc(claimed))
	fmt.Fprintf(&b, `<SignaturePurpose xmlns="%s">%s - %s</SignaturePurpose>`, NSSDTC, purpose.Code, esc(purpose.Display))
	b.WriteString(`</xades:SignedSignatureProperties><xades:SignedDataObjectProperties>`)
	fmt.Fprintf(&b, `<xades:DataObjectFormat ObjectReference="#%s-ref"><xades:Description>HL7 CDA R2 document</xades:Description>`+
		`<xades:MimeType>text/xml</xades:MimeType></xades:DataObjectFormat>`, id)
	if purpose.OID != "" {
		fmt.Fprintf(&b, `<xades:CommitmentTypeIndication><xades:CommitmentTypeId><xades:Identifier Qualifier="OIDAsURN">urn:oid:%s</xades:Identifier>`+
			`<xades:Description>%s</xades:Description></xades:CommitmentTypeId><xades:AllSignedDataObjects></xades:AllSignedDataObjects>`+
			`</xades:CommitmentTypeIndication>`, purpose.OID, esc(purpose.Display))
	}
	b.WriteString(`</xades:SignedDataObjectProperties></xades:SignedProperties>`)
	return b.String()
}

// contentDigest is section 3.1.2: the document without its legalAuthenticator and authenticator participants, canonicalised.
func contentDigest(doc []byte, exclusive bool, newHash func() hash.Hash) ([]byte, error) {
	stripped, err := xmldsig.StripSignature(doc, "legalAuthenticator", NSCDA)
	if err != nil {
		return nil, fmt.Errorf("dsdr: the document is not well-formed XML: %w", err)
	}
	if stripped, err = xmldsig.StripSignature(stripped, "authenticator", NSCDA); err != nil {
		return nil, err
	}
	canon, err := xmldsig.Canonicalise(stripped, exclusive, nil)
	if err != nil {
		return nil, fmt.Errorf("dsdr: canonicalising the document: %w", err)
	}
	h := newHash()
	h.Write(canon)
	return h.Sum(nil), nil
}

func signatureAlgorithm(k crypto.Signer) (string, crypto.Hash, error) {
	switch k.Public().(type) {
	case *rsa.PublicKey:
		return algRSASHA256, crypto.SHA256, nil
	case *ecdsa.PublicKey:
		return algECSHA256, crypto.SHA256, nil
	}
	return "", 0, fmt.Errorf("dsdr: a %T key cannot make an XML signature; use RSA or ECDSA", k.Public())
}

// rootScan is what Sign needs to know about the document's header.
type rootScan struct {
	prefix   string // the prefix the document uses for urn:hl7-org:v3, with its colon, or ""
	children []childScan
}

type childScan struct {
	name               string
	start, end         int
	signed             bool
	afterSignatureCode int
}

func (r *rootScan) child(name string) *childScan {
	for i := range r.children {
		if r.children[i].name == name {
			return &r.children[i]
		}
	}
	return nil
}

// authenticatorAt is where a new authenticator goes: after the last signer, or before what CDA puts after them.
func (r *rootScan) authenticatorAt() int {
	at := -1
	for _, c := range r.children {
		if c.name == "legalAuthenticator" || c.name == "authenticator" {
			at = c.end
		}
	}
	if at >= 0 {
		return at
	}
	for _, c := range r.children {
		switch c.name {
		case "participant", "inFulfillmentOf", "documentationOf", "relatedDocument", "authorization", "componentOf", "component":
			return c.start
		}
	}
	return r.children[len(r.children)-1].end
}

func scanRoot(doc []byte) (*rootScan, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = true
	r := &rootScan{}
	depth := 0
	var cur *childScan
	for {
		before := int(dec.InputOffset())
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("dsdr: the document is not well-formed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if t.Name.Local != "ClinicalDocument" {
					return nil, fmt.Errorf("dsdr: this is a %s, not a CDA ClinicalDocument", t.Name.Local)
				}
				ns := ""
				for _, a := range t.Attr {
					if a.Name.Space == "" && a.Name.Local == "xmlns" && t.Name.Space == "" {
						ns = a.Value
					}
					if a.Name.Space == "xmlns" && a.Name.Local == t.Name.Space && t.Name.Space != "" {
						ns = a.Value
					}
				}
				if ns != NSCDA {
					return nil, fmt.Errorf("dsdr: the ClinicalDocument is not in the CDA namespace %s", NSCDA)
				}
				if t.Name.Space != "" {
					r.prefix = t.Name.Space + ":"
				}
			}
			if depth == 2 {
				r.children = append(r.children, childScan{name: t.Name.Local, start: before, afterSignatureCode: -1})
				cur = &r.children[len(r.children)-1]
			}
			if depth == 3 && cur != nil && t.Name.Local == "signatureText" {
				cur.signed = true
			}
		case xml.EndElement:
			if depth == 3 && cur != nil && t.Name.Local == "signatureCode" {
				cur.afterSignatureCode = int(dec.InputOffset())
			}
			if depth == 2 && cur != nil {
				cur.end = int(dec.InputOffset())
			}
			depth--
		}
	}
	if len(r.children) == 0 {
		return nil, errors.New("dsdr: the ClinicalDocument is empty")
	}
	return r, nil
}

func splice(doc []byte, at int, s string) []byte {
	out := make([]byte, 0, len(doc)+len(s))
	out = append(out, doc[:at]...)
	out = append(out, s...)
	return append(out, doc[at:]...)
}

func wrap(fragment string) []byte { return []byte("<w>" + fragment + "</w>") }

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
