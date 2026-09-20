package xmldsig

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Signing and verifying, built on the canonicalisation in this package.
//
// # What a signature on a clinical document asserts
//
// That a named person or organisation, at a named time, took responsibility for a specific set of bytes. All three
// parts matter and each fails differently: without the identity nobody knows who signed, without the time a
// signature made with a since-revoked key cannot be judged, and without exactness about the bytes the signature
// covers something other than what anybody read.
//
// # Why XAdES rather than plain XMLDSig
//
// Plain XMLDSig proves the bytes have not changed and says nothing about when it was signed or in what capacity.
// XAdES adds the signing time, a binding to the specific certificate rather than merely a compatible key, and the
// role the signer was acting in - which in a clinical document is the difference between the author of a note and
// the organisation transmitting it.
//
// The level implemented is XAdES-BES: the baseline, where the properties are present and signed. The longer-term
// levels add timestamps from a third party and revocation evidence gathered at signing time, which need an external
// timestamp authority. Rather than pretend, verification says which level it found and what that does and does not
// establish.

// Algorithm identifiers, as they appear in a signature.
const (
	SigRSASHA256   = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
	SigECDSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256"
	DigestSHA256   = "http://www.w3.org/2001/04/xmlenc#sha256"

	transformEnveloped = "http://www.w3.org/2000/09/xmldsig#enveloped-signature"

	nsDSig  = "http://www.w3.org/2000/09/xmldsig#"
	nsXAdES = "http://uri.etsi.org/01903/v1.3.2#"
)

// SignOptions describes the signature to produce.
type SignOptions struct {
	// Key is the signing key. RSA and ECDSA are supported; both are used in healthcare, ECDSA increasingly.
	Key crypto.Signer

	// Certificate is the signer's certificate. Required, not optional: a signature whose verifier has to already
	// possess the certificate is a signature that can only be checked by somebody who already knows the answer.
	Certificate *x509.Certificate

	// Chain is the intermediate certificates, so a verifier can build a path without having to fetch anything.
	// Omitting them makes verification depend on the verifier's network, which on a segregated hospital network
	// means verification fails.
	Chain []*x509.Certificate

	// ReferenceID is the ID attribute of the element being signed. Empty signs the whole document.
	ReferenceID string

	// SigningTime is recorded in the signed properties. Zero means now.
	//
	// Overridable because a signature containing the current time cannot be compared byte for byte, and a test that
	// cannot assert on its own output is a test that proves very little.
	SigningTime time.Time

	// Role is the capacity the signer was acting in, free text as the standard allows.
	//
	// Worth filling in. "Dr A Hopper" and "Dr A Hopper, as the attending physician responsible for this discharge"
	// are different assertions, and only the second tells a reader why this person's signature is the relevant one.
	Role string

	// Inclusive uses inclusive canonicalisation. Exclusive is the default and the right answer for anything that
	// might be wrapped, which is every clinical document that travels.
	Inclusive bool
}

// Sign returns the document with an enveloped signature added before the closing tag of the root element.
func Sign(doc []byte, opts SignOptions) ([]byte, error) {
	if opts.Key == nil {
		return nil, fmt.Errorf("signing: no key")
	}
	if opts.Certificate == nil {
		return nil, fmt.Errorf("signing: no certificate. A signature a verifier cannot attribute to anybody " +
			"proves only that some key signed something")
	}

	when := opts.SigningTime
	if when.IsZero() {
		when = time.Now()
	}

	c14nAlg := C14NExclusive
	if opts.Inclusive {
		c14nAlg = C14NInclusive
	}

	// The digest is over the document with no signature in it, which is what the enveloped-signature transform
	// means. At this point there is none, so the document is canonicalised as it stands.
	digestSource, err := CanonicaliseElement(doc, opts.ReferenceID, !opts.Inclusive, nil)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(digestSource)

	sigAlg, err := algorithmFor(opts.Key)
	if err != nil {
		return nil, err
	}

	reference := "#" + opts.ReferenceID
	if opts.ReferenceID == "" {
		// An empty URI means the whole document, which is what the standard says and what every verifier expects.
		reference = ""
	}

	// The certificate digest, which is what turns "a compatible key signed this" into "this certificate signed
	// this".
	//
	// Without it a signature verifies against any certificate carrying the same public key, and an attacker who can
	// get a certificate issued for a key they do not control - or who simply substitutes a self-signed certificate
	// carrying the same key - changes who the signature appears to be from.
	certDigest := sha256.Sum256(opts.Certificate.Raw)

	props := buildSignedProperties(when, opts.Role, opts.Certificate, certDigest[:])

	// The properties are themselves referenced and signed, or they are decoration an attacker can rewrite.
	propsCanon, err := CanonicaliseElement([]byte(wrapForCanon(props)), "sig-props", !opts.Inclusive, nil)
	if err != nil {
		return nil, fmt.Errorf("signing: canonicalising the signed properties: %w", err)
	}
	propsDigest := sha256.Sum256(propsCanon)

	signedInfo := buildSignedInfo(c14nAlg, sigAlg, reference, digest[:], propsDigest[:])

	// SignedInfo is canonicalised before signing, because that is the element the signature covers - not the
	// document. This indirection is what allows one signature to cover several references.
	signedInfoCanon, err := Canonicalise([]byte(wrapForCanon(signedInfo)), !opts.Inclusive, nil)
	if err != nil {
		return nil, fmt.Errorf("signing: canonicalising SignedInfo: %w", err)
	}
	signedInfoCanon = unwrapForCanon(signedInfoCanon)

	sum := sha256.Sum256(signedInfoCanon)
	sigValue, err := opts.Key.Sign(rand.Reader, sum[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}

	signature := assembleSignature(signedInfo, base64.StdEncoding.EncodeToString(sigValue),
		opts.Certificate, opts.Chain, props)

	return insertBeforeRootClose(doc, signature)
}

func algorithmFor(key crypto.Signer) (string, error) {
	switch key.Public().(type) {
	case *rsa.PublicKey:
		return SigRSASHA256, nil
	case *ecdsa.PublicKey:
		return SigECDSASHA256, nil
	default:
		// Named rather than falling through to a generic failure. "unsupported key" sends somebody looking at their
		// configuration; naming the type tells them what they actually have.
		return "", fmt.Errorf("signing: keys of type %T cannot be used for an XML signature", key.Public())
	}
}

func buildSignedInfo(c14nAlg, sigAlg, reference string, digest, propsDigest []byte) string {
	var b strings.Builder

	fmt.Fprintf(&b, `<ds:SignedInfo xmlns:ds="%s">`, nsDSig)
	fmt.Fprintf(&b, `<ds:CanonicalizationMethod Algorithm="%s"></ds:CanonicalizationMethod>`, c14nAlg)
	fmt.Fprintf(&b, `<ds:SignatureMethod Algorithm="%s"></ds:SignatureMethod>`, sigAlg)

	// The document reference, carrying the enveloped-signature transform: the signature is inside what it signs, so
	// the digest is computed over the document with the signature removed.
	fmt.Fprintf(&b, `<ds:Reference URI="%s">`, reference)
	b.WriteString(`<ds:Transforms>`)
	fmt.Fprintf(&b, `<ds:Transform Algorithm="%s"></ds:Transform>`, transformEnveloped)
	fmt.Fprintf(&b, `<ds:Transform Algorithm="%s"></ds:Transform>`, c14nAlg)
	b.WriteString(`</ds:Transforms>`)
	fmt.Fprintf(&b, `<ds:DigestMethod Algorithm="%s"></ds:DigestMethod>`, DigestSHA256)
	fmt.Fprintf(&b, `<ds:DigestValue>%s</ds:DigestValue>`, base64.StdEncoding.EncodeToString(digest))
	b.WriteString(`</ds:Reference>`)

	// The signed properties reference. Type identifies it as XAdES properties, which is how a verifier knows to
	// treat it as more than another signed object.
	fmt.Fprintf(&b, `<ds:Reference Type="http://uri.etsi.org/01903#SignedProperties" URI="#sig-props">`)
	fmt.Fprintf(&b, `<ds:DigestMethod Algorithm="%s"></ds:DigestMethod>`, DigestSHA256)
	fmt.Fprintf(&b, `<ds:DigestValue>%s</ds:DigestValue>`, base64.StdEncoding.EncodeToString(propsDigest))
	b.WriteString(`</ds:Reference>`)

	b.WriteString(`</ds:SignedInfo>`)
	return b.String()
}

func buildSignedProperties(when time.Time, role string, cert *x509.Certificate, certDigest []byte) string {
	var b strings.Builder

	fmt.Fprintf(&b, `<xades:SignedProperties xmlns:xades="%s" xmlns:ds="%s" Id="sig-props">`, nsXAdES, nsDSig)
	b.WriteString(`<xades:SignedSignatureProperties>`)

	// UTC always. A signing time in local time with no offset cannot be compared against a certificate's validity
	// window without knowing where the signer was, and nothing in the document says.
	fmt.Fprintf(&b, `<xades:SigningTime>%s</xades:SigningTime>`, when.UTC().Format(time.RFC3339))

	b.WriteString(`<xades:SigningCertificate><xades:Cert>`)
	b.WriteString(`<xades:CertDigest>`)
	fmt.Fprintf(&b, `<ds:DigestMethod Algorithm="%s"></ds:DigestMethod>`, DigestSHA256)
	fmt.Fprintf(&b, `<ds:DigestValue>%s</ds:DigestValue>`, base64.StdEncoding.EncodeToString(certDigest))
	b.WriteString(`</xades:CertDigest>`)

	// Issuer and serial as well as the digest. The digest proves which certificate; these two let a verifier find
	// it in a store when it was not attached, and they are what an auditor reads.
	b.WriteString(`<xades:IssuerSerial>`)
	fmt.Fprintf(&b, `<ds:X509IssuerName>%s</ds:X509IssuerName>`, escapeXMLText(cert.Issuer.String()))
	fmt.Fprintf(&b, `<ds:X509SerialNumber>%s</ds:X509SerialNumber>`, cert.SerialNumber.String())
	b.WriteString(`</xades:IssuerSerial>`)
	b.WriteString(`</xades:Cert></xades:SigningCertificate>`)

	if strings.TrimSpace(role) != "" {
		b.WriteString(`<xades:SignerRole><xades:ClaimedRoles>`)
		fmt.Fprintf(&b, `<xades:ClaimedRole>%s</xades:ClaimedRole>`, escapeXMLText(role))
		b.WriteString(`</xades:ClaimedRoles></xades:SignerRole>`)
	}

	b.WriteString(`</xades:SignedSignatureProperties>`)
	b.WriteString(`</xades:SignedProperties>`)
	return b.String()
}

func assembleSignature(signedInfo, sigValue string, cert *x509.Certificate,
	chain []*x509.Certificate, props string) string {

	var b strings.Builder
	fmt.Fprintf(&b, `<ds:Signature xmlns:ds="%s">`, nsDSig)
	b.WriteString(signedInfo)
	fmt.Fprintf(&b, `<ds:SignatureValue>%s</ds:SignatureValue>`, sigValue)

	// The certificate and its chain travel with the signature.
	//
	// A verifier that has to fetch them depends on its network, and on a segregated hospital network that means
	// verification simply fails - with an error about connectivity rather than about the signature.
	b.WriteString(`<ds:KeyInfo><ds:X509Data>`)
	fmt.Fprintf(&b, `<ds:X509Certificate>%s</ds:X509Certificate>`,
		base64.StdEncoding.EncodeToString(cert.Raw))
	for _, c := range chain {
		fmt.Fprintf(&b, `<ds:X509Certificate>%s</ds:X509Certificate>`,
			base64.StdEncoding.EncodeToString(c.Raw))
	}
	b.WriteString(`</ds:X509Data></ds:KeyInfo>`)

	fmt.Fprintf(&b, `<ds:Object><xades:QualifyingProperties xmlns:xades="%s" Target="">`, nsXAdES)
	b.WriteString(props)
	b.WriteString(`</xades:QualifyingProperties></ds:Object>`)

	b.WriteString(`</ds:Signature>`)
	return b.String()
}

// insertBeforeRootClose places the signature as the last child of the root element.
//
// Textual insertion, but at a position found by parsing rather than by searching for a string. Searching for the last
// closing tag would be defeated by a closing tag inside a CDATA section or a comment, and a signature inserted in the
// wrong place produces a document that does not parse - or worse, one that parses differently.
func insertBeforeRootClose(doc []byte, signature string) ([]byte, error) {
	end, err := rootCloseOffset(doc)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(doc)+len(signature))
	out = append(out, doc[:end]...)
	out = append(out, signature...)
	out = append(out, doc[end:]...)
	return out, nil
}

func escapeXMLText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// wrapForCanon and unwrapForCanon let a standalone fragment be canonicalised.
//
// The canonicaliser works on documents. A fragment such as SignedInfo is not one on its own, so it is wrapped in a
// throwaway root, canonicalised, and the wrapper removed. The wrapper declares nothing, so under exclusive rules it
// contributes nothing to the fragment's canonical form - which is the property that makes this safe rather than a
// trick.
func wrapForCanon(fragment string) string {
	return "<canon-wrapper>" + fragment + "</canon-wrapper>"
}

func unwrapForCanon(canon []byte) []byte {
	const open, close = "<canon-wrapper>", "</canon-wrapper>"
	s := string(canon)
	s = strings.TrimPrefix(s, open)
	s = strings.TrimSuffix(s, close)
	return []byte(s)
}
