package xmldsig

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"time"
)

// cryptoSHA256 is crypto.SHA256, named here so verify.go does not need the crypto import for one constant.
const cryptoSHA256 = 5 // crypto.SHA256

// The parsed shape of a signature.
//
// Parsed for reading and kept as raw bytes for hashing. Both are needed and neither substitutes for the other: the
// parsed form is how the values are read, and the raw form is what gets canonicalised - because re-serialising a
// parsed structure produces different bytes and therefore a digest that never matches. That single mistake is the
// commonest reason a verifier rejects valid signatures.
type signature struct {
	XMLName xml.Name `xml:"http://www.w3.org/2000/09/xmldsig# Signature"`

	SignedInfo struct {
		CanonicalizationMethod struct {
			Algorithm string `xml:"Algorithm,attr"`
		} `xml:"CanonicalizationMethod"`
		SignatureMethod struct {
			Algorithm string `xml:"Algorithm,attr"`
		} `xml:"SignatureMethod"`
		References []reference `xml:"Reference"`
	} `xml:"SignedInfo"`

	SignatureValue string `xml:"SignatureValue"`

	KeyInfo struct {
		X509Data struct {
			Certificates []string `xml:"X509Certificate"`
		} `xml:"X509Data"`
	} `xml:"KeyInfo"`

	Object struct {
		QualifyingProperties struct {
			SignedProperties struct {
				ID    string `xml:"Id,attr"`
				Props struct {
					SigningTime        time.Time `xml:"SigningTime"`
					SigningCertificate struct {
						Cert struct {
							Digest struct {
								Value string `xml:"DigestValue"`
							} `xml:"CertDigest"`
							IssuerSerial struct {
								IssuerName   string `xml:"X509IssuerName"`
								SerialNumber string `xml:"X509SerialNumber"`
							} `xml:"IssuerSerial"`
						} `xml:"Cert"`
					} `xml:"SigningCertificate"`
					SignerRole struct {
						Claimed struct {
							Roles []string `xml:"ClaimedRole"`
						} `xml:"ClaimedRoles"`
					} `xml:"SignerRole"`
				} `xml:"SignedSignatureProperties"`
			} `xml:"SignedProperties"`
		} `xml:"QualifyingProperties"`
	} `xml:"Object"`

	// Kept out of the XML mapping.
	RawSignedInfo       string    `xml:"-"`
	RawSignedProperties string    `xml:"-"`
	SignedPropertiesID  string    `xml:"-"`
	SigningTime         time.Time `xml:"-"`
	ClaimedRole         string    `xml:"-"`
	CertDigest          string    `xml:"-"`
	Certificates        [][]byte  `xml:"-"`
}

type reference struct {
	URI        string `xml:"URI,attr"`
	Type       string `xml:"Type,attr"`
	Transforms struct {
		Transforms []struct {
			Algorithm string `xml:"Algorithm,attr"`
		} `xml:"Transform"`
	} `xml:"Transforms"`
	DigestMethod struct {
		Algorithm string `xml:"Algorithm,attr"`
	} `xml:"DigestMethod"`
	DigestValue string `xml:"DigestValue"`
}

func (r reference) HasTransform(alg string) bool {
	for _, t := range r.Transforms.Transforms {
		if t.Algorithm == alg {
			return true
		}
	}
	return false
}

// C14NAlgorithm returns the canonicalisation named among the transforms.
//
// A reference may declare its canonicalisation as a transform rather than relying on SignedInfo's, and a verifier
// that only reads the latter computes the digest the wrong way for signatures that do.
func (r reference) C14NAlgorithm() string {
	for _, t := range r.Transforms.Transforms {
		if t.Algorithm == C14NInclusive || t.Algorithm == C14NExclusive {
			return t.Algorithm
		}
	}
	return ""
}

// ContentReference returns the reference covering the document rather than the signed properties.
func (s *signature) ContentReference() *reference {
	for i := range s.SignedInfo.References {
		r := &s.SignedInfo.References[i]
		if r.Type == "http://uri.etsi.org/01903#SignedProperties" {
			continue
		}
		return r
	}
	return nil
}

// PropertiesReference returns the reference covering the XAdES signed properties.
func (s *signature) PropertiesReference() *reference {
	for i := range s.SignedInfo.References {
		r := &s.SignedInfo.References[i]
		if r.Type == "http://uri.etsi.org/01903#SignedProperties" {
			return r
		}
		// Matched by URI as a fallback, because some signers omit the Type attribute. A verifier that insists on it
		// reports the properties as unsigned when they are signed, which is a false accusation of tampering.
		if r.URI == "#"+s.SignedPropertiesID && s.SignedPropertiesID != "" {
			return r
		}
	}
	return nil
}

// extractElement returns the first element with the given local name and namespace, exactly as it appears.
//
// Byte-exact, taken from the source rather than reserialised. The bytes are what get hashed, so any reformatting -
// even reordering attributes into canonical order - would change the digest. Canonicalisation happens afterwards and
// deliberately, on bytes that came out of the file unchanged.
func extractElement(doc []byte, localName, namespace string) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity

	scopes := []map[string]string{}
	depth := 0
	start := -1
	startDepth := 0

	for {
		before := dec.InputOffset()

		tok, err := dec.RawToken()
		if err == io.EOF {
			if start >= 0 {
				return nil, fmt.Errorf("the %s element was never closed", localName)
			}
			return nil, nil
		}
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++

			declared := map[string]string{}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					declared[""] = a.Value
				case a.Name.Space == "xmlns":
					declared[a.Name.Local] = a.Value
				}
			}
			scopes = append(scopes, declared)

			if start < 0 && t.Name.Local == localName {
				// Matched on the namespace URI, not the prefix. A document using ds: and one using sig: are both
				// valid and mean the same thing, and a verifier that only handles one rejects half the world.
				if namespace == "" || mergeScopes(scopes)[t.Name.Space] == namespace {
					start = int(before)
					startDepth = depth
				}
			}

		case xml.EndElement:
			if start >= 0 && depth == startDepth {
				return doc[start:int(dec.InputOffset())], nil
			}
			depth--
			if len(scopes) > 0 {
				scopes = scopes[:len(scopes)-1]
			}
		}
	}
}
