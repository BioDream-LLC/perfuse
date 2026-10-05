package dsdr

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"time"
)

var (
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidRSA           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	digestOIDs       = map[string]crypto.Hash{
		"1.3.14.3.2.26":          crypto.SHA1,
		"2.16.840.1.101.3.4.2.1": crypto.SHA256,
		"2.16.840.1.101.3.4.2.2": crypto.SHA384,
		"2.16.840.1.101.3.4.2.3": crypto.SHA512,
	}
	signatureHashes = map[string]crypto.Hash{
		"1.2.840.113549.1.1.5":  crypto.SHA1,   // sha1WithRSAEncryption
		"1.2.840.113549.1.1.11": crypto.SHA256, // sha256WithRSAEncryption
		"1.2.840.113549.1.1.12": crypto.SHA384,
		"1.2.840.113549.1.1.13": crypto.SHA512,
		"1.2.840.10045.4.3.2":   crypto.SHA256, // ecdsa-with-SHA256
		"1.2.840.10045.4.3.3":   crypto.SHA384,
		"1.2.840.10045.4.3.4":   crypto.SHA512,
	}
)

type algorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial asn1.RawValue
}

// verifyTimestampSignature checks the time-stamping authority's own signature on a TimeStampToken (RFC 3161, a CMS
// SignedData): the messageDigest attribute is the digest of the TSTInfo, the signer's signature over the signed attributes
// verifies with the certificate the token names, that certificate is for time-stamping, and, when roots are given, it chains
// to one at the time of the stamp. It returns the TSA's certificate.
//
// Perfuse did not check this before. The EU DSS validator, given a signature whose time-stamp nobody had signed, refused the
// time-stamp outright, while Perfuse's verifier counted it as XAdES-T evidence: a forged time would have passed.
func verifyTimestampSignature(token []byte, roots *x509.CertPool, at time.Time) (*x509.Certificate, error) {
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(token, &ci); err != nil {
		return nil, fmt.Errorf("not CMS: %w", err)
	}
	sd, err := sequenceItems(ci.Content.Bytes)
	if err != nil || len(sd) < 4 {
		return nil, errors.New("not SignedData")
	}
	var encap struct {
		Type    asn1.ObjectIdentifier
		Content []byte `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(sd[2].FullBytes, &encap); err != nil {
		return nil, fmt.Errorf("no content: %w", err)
	}
	var certs []*x509.Certificate
	signerInfos := sd[len(sd)-1]
	for _, item := range sd[3 : len(sd)-1] {
		if item.Class == asn1.ClassContextSpecific && item.Tag == 0 {
			if certs, err = x509.ParseCertificates(item.Bytes); err != nil {
				return nil, fmt.Errorf("its certificates cannot be read: %w", err)
			}
		}
	}
	if signerInfos.Tag != asn1.TagSet {
		return nil, errors.New("it has no signer infos")
	}
	infos, err := sequenceItemsOf(signerInfos.Bytes)
	if err != nil {
		return nil, err
	}
	if len(infos) != 1 {
		return nil, fmt.Errorf("it is signed by %d signers; a time-stamp is signed by its TSA alone", len(infos))
	}
	si, err := sequenceItems(infos[0].FullBytes)
	if err != nil || len(si) < 5 {
		return nil, errors.New("its signer info cannot be read")
	}
	// version, sid, digestAlgorithm, [0] signedAttrs, signatureAlgorithm, signature
	sid := si[1]
	var digestAlg algorithmIdentifier
	if _, err := asn1.Unmarshal(si[2].FullBytes, &digestAlg); err != nil {
		return nil, err
	}
	idx := 3
	var signedAttrs asn1.RawValue
	if si[3].Class == asn1.ClassContextSpecific && si[3].Tag == 0 {
		signedAttrs = si[3]
		idx = 4
	}
	if len(signedAttrs.FullBytes) == 0 {
		return nil, errors.New("it has no signed attributes, which RFC 3161 requires")
	}
	if len(si) < idx+2 {
		return nil, errors.New("its signer info is incomplete")
	}
	var sigAlg algorithmIdentifier
	if _, err := asn1.Unmarshal(si[idx].FullBytes, &sigAlg); err != nil {
		return nil, err
	}
	var signature []byte
	if _, err := asn1.Unmarshal(si[idx+1].FullBytes, &signature); err != nil {
		return nil, err
	}

	h, ok := digestOIDs[digestAlg.Algorithm.String()]
	if !ok {
		return nil, fmt.Errorf("its digest algorithm %s is not supported", digestAlg.Algorithm)
	}
	content := h.New()
	content.Write(encap.Content)
	var digest []byte
	attrs, err := sequenceItemsOf(signedAttrs.Bytes)
	if err != nil {
		return nil, err
	}
	for _, a := range attrs {
		var attr struct {
			Type   asn1.ObjectIdentifier
			Values asn1.RawValue `asn1:"set"`
		}
		if _, err := asn1.Unmarshal(a.FullBytes, &attr); err != nil {
			continue
		}
		if attr.Type.Equal(oidMessageDigest) {
			_, _ = asn1.Unmarshal(attr.Values.Bytes, &digest)
		}
	}
	if !bytes.Equal(digest, content.Sum(nil)) {
		return nil, errors.New("its messageDigest is not the digest of its TSTInfo")
	}

	var cert *x509.Certificate
	for _, c := range certs {
		if signerMatches(sid, c) {
			cert = c
		}
	}
	if cert == nil {
		return nil, errors.New("it does not carry the certificate of the TSA that signed it")
	}
	// The signature is over the signed attributes encoded as a SET, not with the [0] tag they carry in the token.
	der := append([]byte{0x31}, signedAttrs.FullBytes[1:]...)
	sh := h
	if hh, ok := signatureHashes[sigAlg.Algorithm.String()]; ok {
		sh = hh
	} else if !sigAlg.Algorithm.Equal(oidRSA) {
		return nil, fmt.Errorf("its signature algorithm %s is not supported", sigAlg.Algorithm)
	}
	sum := sh.New()
	sum.Write(der)
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pub, sh, sum.Sum(nil), signature)
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(pub, sum.Sum(nil), signature) {
			err = errors.New("ECDSA verification failed")
		}
	default:
		err = errors.New("the TSA's key type is not supported")
	}
	if err != nil {
		return nil, fmt.Errorf("the TSA's signature does not verify: %w", err)
	}
	stamping := false
	for _, u := range cert.ExtKeyUsage {
		stamping = stamping || u == x509.ExtKeyUsageTimeStamping
	}
	if !stamping {
		return cert, errors.New("the TSA's certificate is not for time-stamping (RFC 3161 requires the timeStamping key usage)")
	}
	for _, e := range cert.Extensions {
		if e.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 37}) && !e.Critical {
			return cert, errors.New("the TSA's certificate does not mark its extended key usage critical, which RFC 3161 requires")
		}
	}
	if roots != nil {
		inter := x509.NewCertPool()
		for _, c := range certs {
			inter.AddCert(c)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping}}); err != nil {
			return cert, fmt.Errorf("the TSA's certificate is not trusted: %w", err)
		}
	}
	return cert, nil
}

func signerMatches(sid asn1.RawValue, c *x509.Certificate) bool {
	if sid.Class == asn1.ClassContextSpecific && sid.Tag == 0 {
		return bytes.Equal(sid.Bytes, c.SubjectKeyId)
	}
	var ias issuerAndSerial
	if _, err := asn1.Unmarshal(sid.FullBytes, &ias); err != nil {
		return false
	}
	var serial asn1.RawValue
	serialDER, _ := asn1.Marshal(c.SerialNumber)
	_, _ = asn1.Unmarshal(serialDER, &serial)
	return bytes.Equal(ias.Issuer.FullBytes, c.RawIssuer) && bytes.Equal(ias.Serial.Bytes, serial.Bytes)
}

// sequenceItemsOf splits the contents of a constructed value into its elements.
func sequenceItemsOf(content []byte) ([]asn1.RawValue, error) {
	var out []asn1.RawValue
	for len(content) > 0 {
		var v asn1.RawValue
		rest, err := asn1.Unmarshal(content, &v)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		content = rest
	}
	return out, nil
}

// tsaCertificates are the certificates a time-stamp token carries: the TSA's, and any of its chain the TSA included.
func tsaCertificates(token []byte) []*x509.Certificate {
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(token, &ci); err != nil {
		return nil
	}
	sd, err := sequenceItems(ci.Content.Bytes)
	if err != nil || len(sd) < 4 {
		return nil
	}
	for _, item := range sd[3 : len(sd)-1] {
		if item.Class == asn1.ClassContextSpecific && item.Tag == 0 {
			certs, _ := x509.ParseCertificates(item.Bytes)
			return certs
		}
	}
	return nil
}
