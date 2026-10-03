// Package dsdr signs and verifies CDA documents as the HL7 Implementation Guide for CDA Release 2: Digital Signatures and
// Delegation of Rights, Release 1 (October 2014) describes - the electronic signature standard HIPAA adopts for claims
// attachments in CMS-0053-F, 45 CFR 162.2002(e), with compliance from 26 May 2028.
//
// What the guide asks for, and where it is done here:
//
//   - The digest is over the whole document, leaving out every legalAuthenticator and authenticator element (section
//     3.1.2), so later signers do not break earlier signatures. Expressed in the signature as an XPath Filter 2.0 subtract
//     transform, which any XML-DSig implementation can evaluate.
//   - The signature is XAdES-X-L (3.1.1, ESMD-1 to ESMD-6): the signing certificate, UTC signing time, a role from the NUCC
//     provider taxonomy, a signature purpose from ASTM E1762, the certification path in CertificateValues and an OCSP or CRL
//     response in RevocationValues, with the time-stamps X-L is built on.
//   - It is carried base64 in sdtc:signatureText on the participant who signed, with a plain-text thumbnail (3.3, ESMD-7 to
//     ESMD-16), inside digitalSignature/authorizedSigner.
//
// Where the guide is silent, the choices made are named: it does not give a namespace for its own elements (digitalSignature,
// authorizedSigner, SignaturePurpose), so they are put in urn:hl7-org:sdtc; and the purpose is also stated as a XAdES
// CommitmentTypeIndication with the ASTM OID, which XAdES tools read and the guide's own element they do not.
//
// Delegation of rights (a delegated signer with a SAML assertion) is not produced; a signature carrying one is reported as such.
package dsdr

import (
	"crypto"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Namespaces.
const (
	NSCDA   = "urn:hl7-org:v3"
	NSSDTC  = "urn:hl7-org:sdtc"
	nsDSig  = "http://www.w3.org/2000/09/xmldsig#"
	nsXAdES = "http://uri.etsi.org/01903/v1.3.2#"

	algExcC14N   = "http://www.w3.org/2001/10/xml-exc-c14n#"
	algC14N      = "http://www.w3.org/TR/2001/REC-xml-c14n-20010315"
	algFilter2   = "http://www.w3.org/2002/06/xmldsig-filter2"
	algSHA256    = "http://www.w3.org/2001/04/xmlenc#sha256"
	algSHA512    = "http://www.w3.org/2001/04/xmlenc#sha512"
	algSHA1      = "http://www.w3.org/2000/09/xmldsig#sha1"
	algRSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
	algRSASHA512 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha512"
	algECSHA256  = "http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256"

	typeSignedProperties = "http://uri.etsi.org/01903#SignedProperties"

	// The participants left out of the digest, as an XPath Filter 2.0 expression.
	excludeParticipants = `//*[local-name()='legalAuthenticator' or local-name()='authenticator'][namespace-uri()='urn:hl7-org:v3']`
)

// Purpose is a signature purpose from ASTM E1762-95, as the guide's Appendix E lists them.
type Purpose struct {
	Code    string // 8.2.1.1
	Display string
	// OID is the same purpose in the ASTM OID arc that XAdES commitment types and FHIR use. The guide's list and the OID arc
	// agree up to Addendum and then diverge - the OID arc has Modification where the guide has Administrative - so this is a
	// table, not arithmetic. Empty for Other, which has no OID.
	OID string
}

// Purposes is Appendix E.
var Purposes = []Purpose{
	{"8.2.1.1", "Author's signature", "1.2.840.10065.1.12.1.1"},
	{"8.2.1.2", "Coauthor's signature", "1.2.840.10065.1.12.1.2"},
	{"8.2.1.3", "Co-participant's signature", "1.2.840.10065.1.12.1.3"},
	{"8.2.1.4", "Transcriptionist/Recorder signature", "1.2.840.10065.1.12.1.4"},
	{"8.2.1.5", "Verification signature", "1.2.840.10065.1.12.1.5"},
	{"8.2.1.6", "Validation signature", "1.2.840.10065.1.12.1.6"},
	{"8.2.1.7", "Consent signature", "1.2.840.10065.1.12.1.7"},
	{"8.2.1.8", "Witness signature", "1.2.840.10065.1.12.1.8"},
	{"8.2.1.9", "Event witness signature", "1.2.840.10065.1.12.1.9"},
	{"8.2.1.10", "Identity witness signature", "1.2.840.10065.1.12.1.10"},
	{"8.2.1.11", "Consent witness signature", "1.2.840.10065.1.12.1.11"},
	{"8.2.1.12", "Interpreter signature", "1.2.840.10065.1.12.1.12"},
	{"8.2.1.13", "Review signature", "1.2.840.10065.1.12.1.13"},
	{"8.2.1.14", "Source signature", "1.2.840.10065.1.12.1.14"},
	{"8.2.1.15", "Addendum signature", "1.2.840.10065.1.12.1.15"},
	{"8.2.1.16", "Administrative signature", "1.2.840.10065.1.12.1.17"},
	{"8.2.1.17", "Timestamp signature", "1.2.840.10065.1.12.1.18"},
	{"8.2.1.18", "Other", ""},
}

// PurposeByCode finds a purpose; nil when the code is not in Appendix E.
func PurposeByCode(code string) *Purpose {
	for i := range Purposes {
		if Purposes[i].Code == code {
			return &Purposes[i]
		}
	}
	return nil
}

// Options describes a signature.
type Options struct {
	// Key signs. RSA or ECDSA.
	Key crypto.Signer
	// Chain is the signing certificate followed by the CA certificates up to and including the root. The guide wants the
	// whole path in the signature (ESMD-5), so a recipient can verify without fetching anything.
	Chain []*x509.Certificate

	// SignerName is the person signing, for the thumbnail and for a new participant. Defaults to the certificate's common name.
	SignerName string
	// NPI identifies the signer on a new participant. The guide expects it in the certificate's altName too (3.4.1 d).
	NPI string
	// Role is a NUCC provider taxonomy code, e.g. 207Q00000X; RoleDisplay its name. Required (ESMD-3).
	Role, RoleDisplay string
	// Purpose is an Appendix E code. Defaults to 8.2.1.1, Author's signature, for a legal authenticator and 8.2.1.2,
	// Coauthor's signature, otherwise - the guide's own multiple-signer example.
	Purpose string
	// As is legalAuthenticator or authenticator. Empty signs the document's legalAuthenticator if it has one not yet
	// signed, and adds an authenticator otherwise.
	As string

	// TSAURL is an RFC 3161 time-stamping authority. XAdES-X-L is built on time-stamps; without one the signature stops at
	// XAdES-C and the result says so.
	TSAURL string
	// HTTP fetches time-stamps, OCSP responses and CRLs. Defaults to a client with a 15 second timeout.
	HTTP *http.Client
	// Now overrides the clock.
	Now time.Time
}

// Result is a signed document and what it amounts to.
type Result struct {
	// Document is the signed document. Not printed by String, so a failing test or a log line does not dump it.
	Document []byte `json:"-"`
	// Participant is legalAuthenticator or authenticator.
	Participant string `json:"participant"`
	// Level is the XAdES form reached: BES, EPES, T, C, X or X-L. Only X-L meets the guide.
	Level string `json:"level"`
	// Conforms is whether the signature meets every SHALL of the guide that a signer controls.
	Conforms bool `json:"conforms"`
	// Missing says what stopped it short of X-L, in plain terms.
	Missing   []string  `json:"missing"`
	Thumbnail string    `json:"thumbnail"`
	SignedAt  time.Time `json:"signedAt"`
}

func (o *Options) httpClient() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (o *Options) check() error {
	if o.Key == nil || len(o.Chain) == 0 {
		return fmt.Errorf("dsdr: a key and the signing certificate are needed")
	}
	if strings.TrimSpace(o.Role) == "" {
		return fmt.Errorf("dsdr: the signer's role is required (ESMD-3): a NUCC provider taxonomy code such as 207Q00000X")
	}
	if o.Purpose != "" && PurposeByCode(o.Purpose) == nil {
		return fmt.Errorf("dsdr: %q is not a signature purpose from the guide's Appendix E (8.2.1.1 to 8.2.1.18)", o.Purpose)
	}
	switch o.As {
	case "", "legalAuthenticator", "authenticator":
	default:
		return fmt.Errorf("dsdr: sign as legalAuthenticator or authenticator, not %q", o.As)
	}
	return nil
}

func (r Result) String() string {
	return fmt.Sprintf("%s signed at XAdES-%s (conforms %v); missing %v", r.Participant, r.Level, r.Conforms, r.Missing)
}
