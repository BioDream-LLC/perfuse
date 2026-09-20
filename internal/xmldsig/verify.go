package xmldsig

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// Verifying a signature, which is the half that matters more.
//
// Signing is something a site chooses to do. Verifying is something it has to do to every signed document that
// arrives, and it is where the dangerous failures live, because they are all failures of generosity: a verifier that
// accepts too much reports a document as trustworthy when it is not, and nothing downstream ever questions it.
//
// The failures guarded against here, each of which has defeated real implementations:
//
//   - Accepting a signature whose digest covers only part of the document, so the rest can be changed freely.
//   - Taking the first element matching a referenced identifier when two exist, so the verified content and the used
//     content differ. Refused during canonicalisation.
//   - Trusting the KeyInfo. The certificate in the document is the signer's claim about who they are, not evidence.
//     It must be checked against something the verifier already trusts, and if there is nothing to check it against
//     that must be said rather than glossed over.
//   - Verifying the signature and not the signed properties, so the signing time and the signer's claimed role can
//     be rewritten by anybody.
//   - Reporting a valid signature over content that a since-revoked or then-expired certificate signed.
//
// # What a report has to distinguish
//
// "Valid" is four separate questions and answering them as one is how a verifier misleads people. The bytes are
// intact, or not. The signature was made by the key in that certificate, or not. That certificate is one this site
// trusts, or unknown. And the certificate was valid at the moment of signing, or not. A document can pass the first
// two and fail the second two, and that is a completely different situation from a broken digest.

// Report is what verification found.
type Report struct {
	// Signed is whether a signature was present at all. False is not a failure: most documents are unsigned.
	Signed bool `json:"signed"`

	// DigestValid means the content has not changed since it was signed.
	DigestValid bool `json:"digestValid"`

	// SignatureValid means the signature over SignedInfo was made by the key in the certificate.
	SignatureValid bool `json:"signatureValid"`

	// PropertiesValid means the XAdES signed properties are covered by the signature, so the signing time and
	// claimed role cannot have been rewritten.
	PropertiesValid bool `json:"propertiesValid"`

	// Trusted means the certificate chains to something this site trusts. Separate from the cryptography, because a
	// mathematically perfect signature from an unknown certificate proves only that somebody has a key.
	Trusted bool `json:"trusted"`

	// TrustChecked is false when no trust store was supplied, so Trusted being false means "not checked" rather than
	// "checked and failed". Conflating those is how a verifier reports every document as untrusted and teaches
	// everybody to ignore the field.
	TrustChecked bool `json:"trustChecked"`

	// ValidAtSigningTime is whether the certificate was inside its validity window when it claims to have signed.
	ValidAtSigningTime bool `json:"validAtSigningTime"`

	Signer      string    `json:"signer,omitempty"`
	Issuer      string    `json:"issuer,omitempty"`
	SigningTime time.Time `json:"signingTime,omitempty"`
	ClaimedRole string    `json:"claimedRole,omitempty"`
	Level       string    `json:"level,omitempty"`

	// CoveredIDs lists what the signature actually covers, so a reader can see whether that is the whole document.
	CoveredIDs []string `json:"coveredIds"`

	// Problems are the reasons this is not simply valid, in plain terms. Never nil.
	Problems []string `json:"problems"`

	// Notes are things worth knowing that are not failures.
	Notes []string `json:"notes"`
}

// Sound reports whether everything checkable passed.
//
// Trust is included only when it was checked, so a site with no trust store gets an honest answer about the
// cryptography rather than a permanent failure it learns to ignore.
func (r Report) Sound() bool {
	if !r.Signed || !r.DigestValid || !r.SignatureValid || !r.PropertiesValid || !r.ValidAtSigningTime {
		return false
	}
	if r.TrustChecked && !r.Trusted {
		return false
	}
	return true
}

// VerifyOptions controls verification.
type VerifyOptions struct {
	// Roots is what this site trusts. Nil means trust is not checked, and the report says so rather than reporting
	// everything as untrusted.
	Roots *x509.CertPool

	// Intermediates supplements any certificates carried in the document.
	Intermediates *x509.CertPool

	// Now overrides the current time, for tests and for verifying a document as of a past date.
	Now time.Time
}

// Verify checks the signature in a document.
//
// A report is always returned, even alongside an error, because "there is a signature and here is what is wrong with
// it" is more useful than an error on its own.
func Verify(doc []byte, opts VerifyOptions) (Report, error) {
	r := Report{CoveredIDs: []string{}, Problems: []string{}, Notes: []string{}}

	sig, err := findSignature(doc)
	if err != nil {
		return r, err
	}
	if sig == nil {
		r.Notes = append(r.Notes, "This document carries no signature. Most do not; it is not a fault.")
		return r, nil
	}
	r.Signed = true

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	// The certificate first, because everything else is judged against it.
	if len(sig.Certificates) == 0 {
		r.Problems = append(r.Problems,
			"The signature carries no certificate, so there is no way to tell who made it. The signature may be "+
				"mathematically sound and still attributable to nobody.")
		return r, nil
	}

	cert, err := x509.ParseCertificate(sig.Certificates[0])
	if err != nil {
		r.Problems = append(r.Problems, "The certificate in the signature could not be read: "+err.Error())
		return r, nil
	}
	r.Signer = cert.Subject.String()
	r.Issuer = cert.Issuer.String()

	// The digest over the referenced content, computed the way the signer says they computed it.
	if err := checkDigest(doc, sig, &r); err != nil {
		r.Problems = append(r.Problems, err.Error())
	} else {
		r.DigestValid = true
	}

	// The signature over SignedInfo.
	if err := checkSignature(sig, cert); err != nil {
		r.Problems = append(r.Problems, err.Error())
	} else {
		r.SignatureValid = true
	}

	// The XAdES properties, and whether they are actually signed.
	checkProperties(doc, sig, cert, &r)

	// Trust, kept separate from the cryptography.
	if opts.Roots == nil {
		r.Notes = append(r.Notes,
			"No trust store was supplied, so this reports only that the signature is internally consistent. It "+
				"does not establish that the signer is anybody in particular: a self-signed certificate produces "+
				"exactly the same result.")
	} else {
		r.TrustChecked = true

		pool := opts.Intermediates
		if pool == nil {
			pool = x509.NewCertPool()
		}
		for _, raw := range sig.Certificates[1:] {
			if c, err := x509.ParseCertificate(raw); err == nil {
				pool.AddCert(c)
			}
		}

		// Verified as of the signing time when there is one, not as of now.
		//
		// A document signed two years ago with a certificate that has since expired is not a forgery, and reporting
		// it as untrusted because time has passed makes every archived document look broken.
		at := now
		if !r.SigningTime.IsZero() {
			at = r.SigningTime
		}

		if _, err := cert.Verify(x509.VerifyOptions{
			Roots:         opts.Roots,
			Intermediates: pool,
			CurrentTime:   at,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		}); err != nil {
			r.Problems = append(r.Problems,
				"The certificate does not chain to anything this site trusts: "+err.Error())
		} else {
			r.Trusted = true
		}
	}

	// Whether the certificate was usable when it claims to have signed.
	switch {
	case r.SigningTime.IsZero():
		r.Problems = append(r.Problems,
			"The signature states no signing time, so there is no way to tell whether the certificate was valid "+
				"when it was used.")
	case r.SigningTime.Before(cert.NotBefore):
		r.Problems = append(r.Problems, fmt.Sprintf(
			"The signature claims to have been made on %s, before its certificate became valid on %s. Either the "+
				"clock was wrong or the time was chosen.",
			r.SigningTime.UTC().Format(time.RFC3339), cert.NotBefore.UTC().Format(time.RFC3339)))
	case r.SigningTime.After(cert.NotAfter):
		r.Problems = append(r.Problems, fmt.Sprintf(
			"The signature claims to have been made on %s, after its certificate expired on %s.",
			r.SigningTime.UTC().Format(time.RFC3339), cert.NotAfter.UTC().Format(time.RFC3339)))
	default:
		r.ValidAtSigningTime = true
	}

	// Revocation is not checked, and that is said rather than left for somebody to assume.
	//
	// Checking it properly means fetching a CRL or asking an OCSP responder, which on a segregated network cannot be
	// done - and a verifier that silently skips revocation while reporting "valid" is making a claim it has not
	// earned.
	r.Notes = append(r.Notes,
		"Revocation was not checked. Doing so needs a CRL or an OCSP responder, which is not reachable from a "+
			"segregated network. A signature can be sound and its certificate revoked.")

	r.Level = levelOf(r)
	return r, nil
}

func levelOf(r Report) string {
	if !r.SigningTime.IsZero() {
		// BES: the baseline, where the properties are present and signed. The higher levels need a timestamp from a
		// third party, which is what turns "the signer says they signed it then" into evidence.
		return "XAdES-BES (signing time is the signer's own claim, not independently timestamped)"
	}
	return "XMLDSig (no XAdES properties; nothing states when this was signed or in what capacity)"
}

// checkDigest recomputes the digest over the referenced content with the signature removed.
func checkDigest(doc []byte, sig *signature, r *Report) error {
	ref := sig.ContentReference()
	if ref == nil {
		return fmt.Errorf("the signature has no reference to any content, so it covers nothing")
	}

	id := strings.TrimPrefix(ref.URI, "#")
	if id != "" {
		r.CoveredIDs = append(r.CoveredIDs, id)
	} else {
		r.Notes = append(r.Notes, "The signature covers the whole document.")
	}

	// The enveloped-signature transform: the signature cannot cover itself, so it is removed first. Required, not
	// optional - without it the digest could never match, because the digest was computed before the signature
	// existed.
	if !ref.HasTransform(transformEnveloped) {
		r.Notes = append(r.Notes,
			"The reference does not declare the enveloped-signature transform, which is unusual for a signature "+
				"inside the document it signs.")
	}

	stripped, err := StripSignature(doc, "Signature", nsDSig)
	if err != nil {
		return fmt.Errorf("could not remove the signature to recompute the digest: %w", err)
	}

	exclusive := ref.C14NAlgorithm() != C14NInclusive

	canon, err := CanonicaliseElement(stripped, id, exclusive, nil)
	if err != nil {
		return fmt.Errorf("could not canonicalise the signed content: %w", err)
	}

	want, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ref.DigestValue))
	if err != nil {
		return fmt.Errorf("the digest in the signature is not valid base64: %w", err)
	}

	got := sha256.Sum256(canon)
	if !bytes.Equal(got[:], want) {
		return fmt.Errorf("the content has changed since it was signed: the digest in the signature does not " +
			"match the document")
	}
	return nil
}

func checkSignature(sig *signature, cert *x509.Certificate) error {
	if strings.TrimSpace(sig.RawSignedInfo) == "" {
		return fmt.Errorf("the signature has no SignedInfo, so there is nothing to check")
	}

	exclusive := sig.SignedInfo.CanonicalizationMethod.Algorithm != C14NInclusive

	canon, err := Canonicalise([]byte(wrapForCanon(sig.RawSignedInfo)), exclusive, nil)
	if err != nil {
		return fmt.Errorf("could not canonicalise SignedInfo: %w", err)
	}
	canon = unwrapForCanon(canon)

	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(sig.SignatureValue), ""))
	if err != nil {
		return fmt.Errorf("the signature value is not valid base64: %w", err)
	}

	sum := sha256.Sum256(canon)

	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(pub, cryptoSHA256, sum[:], raw); err != nil {
			return fmt.Errorf("the signature was not made by the key in this certificate: %w", err)
		}
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(pub, sum[:], raw) {
			return fmt.Errorf("the signature was not made by the key in this certificate")
		}
	default:
		return fmt.Errorf("this certificate carries a %T key, which cannot verify an XML signature", cert.PublicKey)
	}
	return nil
}

// checkProperties reads the XAdES properties and confirms the signature covers them.
//
// Both halves are needed. Reading them without confirming they are signed means reporting a signing time and a
// claimed role that anybody who can edit the file can change - which is worse than reporting nothing, because it
// looks like evidence.
func checkProperties(doc []byte, sig *signature, cert *x509.Certificate, r *Report) {
	props := sig.RawSignedProperties
	if strings.TrimSpace(props) == "" {
		r.Problems = append(r.Problems,
			"The signature carries no XAdES properties, so nothing states when it was made or in what capacity. "+
				"It proves the bytes are intact and nothing about the circumstances.")
		return
	}

	r.SigningTime = sig.SigningTime
	r.ClaimedRole = sig.ClaimedRole

	ref := sig.PropertiesReference()
	if ref == nil {
		r.Problems = append(r.Problems,
			"The XAdES properties are present but the signature does not reference them, so the signing time and "+
				"the claimed role are unsigned. Anybody who can edit this file can change them.")
		return
	}

	exclusive := sig.SignedInfo.CanonicalizationMethod.Algorithm != C14NInclusive

	canon, err := CanonicaliseElement([]byte(wrapForCanon(props)), sig.SignedPropertiesID, exclusive, nil)
	if err != nil {
		r.Problems = append(r.Problems, "The signed properties could not be canonicalised: "+err.Error())
		return
	}

	want, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ref.DigestValue))
	if err != nil {
		r.Problems = append(r.Problems, "The properties digest is not valid base64.")
		return
	}

	got := sha256.Sum256(canon)
	if !bytes.Equal(got[:], want) {
		r.Problems = append(r.Problems,
			"The XAdES properties do not match the digest in the signature, so the signing time or the claimed "+
				"role has been altered since signing.")
		return
	}
	r.PropertiesValid = true

	// The certificate digest, which is what binds the signature to this certificate rather than to any certificate
	// carrying the same key.
	if sig.CertDigest != "" {
		want, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig.CertDigest))
		if err == nil {
			got := sha256.Sum256(cert.Raw)
			if !bytes.Equal(got[:], want) {
				r.Problems = append(r.Problems,
					"The certificate attached to this signature is not the one the signer named. Somebody has "+
						"substituted a certificate carrying the same public key, which changes who the signature "+
						"appears to be from.")
				r.PropertiesValid = false
			}
		}
	} else {
		r.Notes = append(r.Notes,
			"The signed properties do not name a certificate, so this signature verifies against any certificate "+
				"carrying the same public key.")
	}
}

// findSignature locates and parses the first ds:Signature in a document.
func findSignature(doc []byte) (*signature, error) {
	raw, err := extractElement(doc, "Signature", nsDSig)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}

	var s signature
	if err := xml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("the signature could not be read: %w", err)
	}

	// The raw SignedInfo and SignedProperties are kept as bytes as well as parsed, because they have to be
	// canonicalised exactly as they were written. Re-serialising a parsed structure would produce different bytes
	// and a digest that never matches - which is the commonest way a verifier is wrong.
	if si, err := extractElement(raw, "SignedInfo", nsDSig); err == nil && si != nil {
		s.RawSignedInfo = string(si)
	}
	if sp, err := extractElement(raw, "SignedProperties", nsXAdES); err == nil && sp != nil {
		s.RawSignedProperties = string(sp)
	}

	s.SignedPropertiesID = s.Object.QualifyingProperties.SignedProperties.ID
	s.SigningTime = s.Object.QualifyingProperties.SignedProperties.Props.SigningTime
	s.CertDigest = s.Object.QualifyingProperties.SignedProperties.Props.SigningCertificate.Cert.Digest.Value

	if roles := s.Object.QualifyingProperties.SignedProperties.Props.SignerRole.Claimed.Roles; len(roles) > 0 {
		s.ClaimedRole = roles[0]
	}

	for _, b := range s.KeyInfo.X509Data.Certificates {
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b), ""))
		if err != nil {
			continue
		}
		s.Certificates = append(s.Certificates, der)
	}

	return &s, nil
}
