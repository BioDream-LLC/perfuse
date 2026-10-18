// Package cms is the Cryptographic Message Syntax (RFC 5652) as S/MIME and Direct secure messaging use it: SignedData
// (sign a message, verify one and say who signed it) and EnvelopedData (encrypt a message to its recipients' certificates,
// decrypt one with our key).
//
// Only what the Direct Applicability Statement and its Implementation Guide require: RSA with SHA-256 (SHA-1 accepted for
// reading only, since older HISPs still send it), RSA key transport with PKCS#1 v1.5 or OAEP, and AES-128 or AES-256 in CBC
// mode. The cryptography is the standard library's; this package is the ASN.1 around it.
package cms

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Object identifiers.
var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidSHA1          = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSA           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidRSAOAEP       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 7}

	oidAES128CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES256CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oidAES192CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidDESEDE3CBC = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}
	oidMGF1       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 8}
)

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type algorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

type encapsulatedContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type signerInfo struct {
	Version            int
	SID                issuerAndSerial
	DigestAlgorithm    algorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm algorithmIdentifier
	Signature          []byte
	UnsignedAttrs      asn1.RawValue `asn1:"optional,tag:1"`
}

type signedData struct {
	Version          int
	DigestAlgorithms asn1.RawValue `asn1:"set"`
	EncapContentInfo encapsulatedContentInfo
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

func null() asn1.RawValue { return asn1.RawValue{Tag: asn1.TagNull} }

// Sign makes a detached SignedData over content, signed by cert with key and carrying cert and chain: what an S/MIME
// multipart/signed message's application/pkcs7-signature part holds.
func Sign(content []byte, cert *x509.Certificate, key crypto.Signer, chain []*x509.Certificate) ([]byte, error) {
	if _, ok := key.Public().(*rsa.PublicKey); !ok {
		return nil, errors.New("cms: Direct requires an RSA signing key")
	}
	digest := sha256.Sum256(content)
	now := time.Now().UTC()
	ctAttr, err := setOf(oidData)
	if err != nil {
		return nil, err
	}
	mdAttr, err := setOf(digest[:])
	if err != nil {
		return nil, err
	}
	stAttr, err := setOf(now)
	if err != nil {
		return nil, err
	}
	attrs := []attribute{{Type: oidContentType, Values: ctAttr}, {Type: oidSigningTime, Values: stAttr}, {Type: oidMessageDigest, Values: mdAttr}}
	// Signed attributes are DER-encoded as a SET OF, sorted: the signature is over that encoding with the SET tag.
	attrSet, err := derSet(attrs)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(attrSet)
	sig, err := key.Sign(rand.Reader, h[:], crypto.SHA256)
	if err != nil {
		return nil, err
	}
	// Inside SignerInfo the same bytes are tagged [0] IMPLICIT.
	signed := asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: setContents(attrSet)}
	si := signerInfo{Version: 1, SID: issuerAndSerial{Issuer: asn1.RawValue{FullBytes: cert.RawIssuer}, Serial: cert.SerialNumber},
		DigestAlgorithm: algorithmIdentifier{Algorithm: oidSHA256, Parameters: null()}, SignedAttrs: signed,
		SignatureAlgorithm: algorithmIdentifier{Algorithm: oidRSA, Parameters: null()}, Signature: sig}
	digestAlgs, err := derSet([]algorithmIdentifier{{Algorithm: oidSHA256, Parameters: null()}})
	if err != nil {
		return nil, err
	}
	var certs []byte
	for _, c := range append([]*x509.Certificate{cert}, chain...) {
		certs = append(certs, c.Raw...)
	}
	sd := signedData{Version: 1, DigestAlgorithms: asn1.RawValue{FullBytes: digestAlgs}, EncapContentInfo: encapsulatedContentInfo{ContentType: oidData},
		Certificates: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certs}, SignerInfos: []signerInfo{si}}
	inner, err := asn1.Marshal(sd)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{ContentType: oidSignedData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0,
		IsCompound: true, Bytes: inner}})
}

func setOf(v any) (asn1.RawValue, error) {
	b, err := asn1.Marshal(v)
	if err != nil {
		return asn1.RawValue{}, err
	}
	return asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: b}, nil
}

// derSet encodes elements as a DER SET OF: each element encoded, sorted by encoding, wrapped in the SET tag.
func derSet[T any](elems []T) ([]byte, error) {
	var enc [][]byte
	for _, e := range elems {
		b, err := asn1.Marshal(e)
		if err != nil {
			return nil, err
		}
		enc = append(enc, b)
	}
	for i := 1; i < len(enc); i++ {
		for j := i; j > 0 && bytes.Compare(enc[j], enc[j-1]) < 0; j-- {
			enc[j], enc[j-1] = enc[j-1], enc[j]
		}
	}
	return asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: bytes.Join(enc, nil)})
}

// setContents strips the tag and length from a DER SET, leaving its elements.
func setContents(der []byte) []byte {
	var rv asn1.RawValue
	if _, err := asn1.Unmarshal(der, &rv); err != nil {
		return nil
	}
	return rv.Bytes
}

// Signer is who signed a message, after its signature checked out.
type Signer struct {
	Certificate *x509.Certificate
	SigningTime time.Time
}

// Verify checks a detached SignedData over content: the content digest, the signature over the signed attributes, and the
// signer's certificate against roots for the given time (Direct's trust anchors; intermediates come from the message). It
// returns the signer. A certificate with no path to a root is an error: Direct trust is anchored, never "any signature".
func Verify(sig, content []byte, roots *x509.CertPool, at time.Time) (*Signer, error) {
	var ci contentInfo
	if rest, err := asn1.Unmarshal(sig, &ci); err != nil || len(rest) != 0 {
		return nil, fmt.Errorf("cms: the signature is not a ContentInfo: %v", err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return nil, errors.New("cms: the signature is not SignedData")
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil, fmt.Errorf("cms: SignedData cannot be read: %w", err)
	}
	if len(sd.SignerInfos) == 0 {
		return nil, errors.New("cms: the SignedData has no signer")
	}
	certs, err := x509.ParseCertificates(sd.Certificates.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cms: the certificates cannot be read: %w", err)
	}
	if len(sd.EncapContentInfo.Content.Bytes) > 0 {
		// Encapsulated content (an opaque signature): verify that instead of the detached part.
		var octets []byte
		if _, err := asn1.Unmarshal(sd.EncapContentInfo.Content.Bytes, &octets); err == nil {
			content = octets
		}
	}
	si := sd.SignerInfos[0]
	var signer *x509.Certificate
	for _, c := range certs {
		if bytes.Equal(c.RawIssuer, si.SID.Issuer.FullBytes) && c.SerialNumber.Cmp(si.SID.Serial) == 0 {
			signer = c
		}
	}
	if signer == nil {
		return nil, errors.New("cms: the signer's certificate is not in the message")
	}
	var hash crypto.Hash
	switch {
	case si.DigestAlgorithm.Algorithm.Equal(oidSHA256):
		hash = crypto.SHA256
	case si.DigestAlgorithm.Algorithm.Equal(oidSHA1):
		hash = crypto.SHA1
	default:
		return nil, fmt.Errorf("cms: digest algorithm %v is not one Direct uses", si.DigestAlgorithm.Algorithm)
	}
	sum := func(b []byte) []byte {
		if hash == crypto.SHA1 {
			s := sha1.Sum(b)
			return s[:]
		}
		s := sha256.Sum256(b)
		return s[:]
	}
	out := &Signer{Certificate: signer}
	signedBytes := content
	if len(si.SignedAttrs.Bytes) > 0 {
		var attrs []attribute
		full := append([]byte{0x31}, si.SignedAttrs.FullBytes[1:]...) // [0] IMPLICIT back to SET for the signature
		if _, err := asn1.UnmarshalWithParams(full, &attrs, "set"); err != nil {
			return nil, fmt.Errorf("cms: the signed attributes cannot be read: %w", err)
		}
		var md []byte
		for _, a := range attrs {
			switch {
			case a.Type.Equal(oidMessageDigest):
				_, _ = asn1.Unmarshal(a.Values.Bytes, &md)
			case a.Type.Equal(oidSigningTime):
				var t time.Time
				if _, err := asn1.Unmarshal(a.Values.Bytes, &t); err == nil {
					out.SigningTime = t
				}
			}
		}
		if !bytes.Equal(md, sum(content)) {
			return nil, errors.New("cms: the message digest does not match the content: the message was changed after it was signed")
		}
		signedBytes = full
	}
	pub, ok := signer.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("cms: the signer's key is not RSA")
	}
	if err := rsa.VerifyPKCS1v15(pub, hash, sum(signedBytes), si.Signature); err != nil {
		return nil, errors.New("cms: the signature does not verify")
	}
	inter := x509.NewCertPool()
	for _, c := range certs {
		if c != signer {
			inter.AddCert(c)
		}
	}
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := signer.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, fmt.Errorf("cms: the signer %s is not trusted: %w", signer.Subject.CommonName, err)
	}
	return out, nil
}

// EnvelopedData.

type keyTransRecipientInfo struct {
	Version                int
	RID                    issuerAndSerial
	KeyEncryptionAlgorithm algorithmIdentifier
	EncryptedKey           []byte
}

type encryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm algorithmIdentifier
	EncryptedContent           asn1.RawValue `asn1:"optional,tag:0"`
}

type envelopedData struct {
	Version              int
	OriginatorInfo       asn1.RawValue           `asn1:"optional,tag:0"`
	RecipientInfos       []keyTransRecipientInfo `asn1:"set"`
	EncryptedContentInfo encryptedContentInfo
}

// Encrypt makes an EnvelopedData of content for each recipient certificate, with AES-256-CBC and RSA key transport
// (OAEP when oaep, else PKCS#1 v1.5: both are in the Direct Applicability Statement; v1.5 is what most HISPs read).
func Encrypt(content []byte, recipients []*x509.Certificate, oaep bool) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("cms: a message is encrypted to at least one recipient")
	}
	key := make([]byte, 32)
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padLen := aes.BlockSize - len(content)%aes.BlockSize
	padded := append(append([]byte{}, content...), bytes.Repeat([]byte{byte(padLen)}, padLen)...)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	var ris []keyTransRecipientInfo
	for _, rc := range recipients {
		pub, ok := rc.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("cms: recipient %s does not have an RSA key", rc.Subject.CommonName)
		}
		var ek []byte
		alg := algorithmIdentifier{Algorithm: oidRSA, Parameters: null()}
		if oaep {
			ek, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, key, nil)
			params, perr := oaepParams()
			if perr != nil {
				return nil, perr
			}
			alg = algorithmIdentifier{Algorithm: oidRSAOAEP, Parameters: params}
		} else {
			ek, err = rsa.EncryptPKCS1v15(rand.Reader, pub, key)
		}
		if err != nil {
			return nil, err
		}
		ris = append(ris, keyTransRecipientInfo{Version: 0, RID: issuerAndSerial{Issuer: asn1.RawValue{FullBytes: rc.RawIssuer},
			Serial: rc.SerialNumber}, KeyEncryptionAlgorithm: alg, EncryptedKey: ek})
	}
	ivDER, err := asn1.Marshal(iv)
	if err != nil {
		return nil, err
	}
	ed := envelopedData{Version: 0, RecipientInfos: ris, EncryptedContentInfo: encryptedContentInfo{ContentType: oidData,
		ContentEncryptionAlgorithm: algorithmIdentifier{Algorithm: oidAES256CBC, Parameters: asn1.RawValue{FullBytes: ivDER}},
		EncryptedContent:           asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: ct}}}
	inner, err := asn1.Marshal(ed)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{ContentType: oidEnvelopedData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0,
		IsCompound: true, Bytes: inner}})
}

// oaepParams are RSAES-OAEP-params with SHA-256 for the hash and MGF1 (RFC 4055).
func oaepParams() (asn1.RawValue, error) {
	sha := algorithmIdentifier{Algorithm: oidSHA256, Parameters: null()}
	shaDER, _ := asn1.Marshal(sha)
	mgf := algorithmIdentifier{Algorithm: oidMGF1, Parameters: asn1.RawValue{FullBytes: shaDER}}
	type params struct {
		Hash algorithmIdentifier `asn1:"explicit,tag:0"`
		MGF  algorithmIdentifier `asn1:"explicit,tag:1"`
	}
	b, err := asn1.Marshal(params{Hash: sha, MGF: mgf})
	return asn1.RawValue{FullBytes: b}, err
}

// ErrNotForUs is an EnvelopedData that names no recipient whose key we hold.
var ErrNotForUs = errors.New("cms: the message is not encrypted to any certificate this address holds")

// Decrypt opens an EnvelopedData with whichever of the keys is a recipient (cert and its key).
func Decrypt(der []byte, keys []KeyPair) ([]byte, error) {
	var ci contentInfo
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || len(rest) != 0 {
		// Streaming BER (indefinite lengths), as Java gateways write it.
		d, berr := ToDER(der)
		if berr != nil {
			return nil, fmt.Errorf("cms: the message is not a ContentInfo: %v", err)
		}
		if rest, err = asn1.Unmarshal(d, &ci); err != nil || len(rest) != 0 {
			return nil, fmt.Errorf("cms: the message is not a ContentInfo: %v", err)
		}
	}
	if !ci.ContentType.Equal(oidEnvelopedData) {
		return nil, errors.New("cms: the message is not EnvelopedData")
	}
	var ed envelopedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &ed); err != nil {
		return nil, fmt.Errorf("cms: EnvelopedData cannot be read: %w", err)
	}
	var cek []byte
	for _, ri := range ed.RecipientInfos {
		for _, kp := range keys {
			if !bytes.Equal(kp.Certificate.RawIssuer, ri.RID.Issuer.FullBytes) || kp.Certificate.SerialNumber.Cmp(ri.RID.Serial) != 0 {
				continue
			}
			priv, ok := kp.Key.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("cms: the recipient key is not RSA")
			}
			var err error
			switch {
			case ri.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSAOAEP):
				h := sha256.New()
				if len(ri.KeyEncryptionAlgorithm.Parameters.FullBytes) == 0 || isSHA1OAEP(ri.KeyEncryptionAlgorithm.Parameters.FullBytes) {
					h = nil
				}
				if h == nil {
					cek, err = rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, ri.EncryptedKey, nil)
				} else {
					cek, err = rsa.DecryptOAEP(h, rand.Reader, priv, ri.EncryptedKey, nil)
				}
			case ri.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSA):
				cek, err = rsa.DecryptPKCS1v15(rand.Reader, priv, ri.EncryptedKey)
			default:
				return nil, fmt.Errorf("cms: key transport %v is not one Direct uses", ri.KeyEncryptionAlgorithm.Algorithm)
			}
			if err != nil {
				return nil, errors.New("cms: the content key cannot be decrypted with this address's key")
			}
		}
		if cek != nil {
			break
		}
	}
	if cek == nil {
		return nil, ErrNotForUs
	}
	alg := ed.EncryptedContentInfo.ContentEncryptionAlgorithm
	// AES-256 is what this package writes. AES-128 and AES-192 are read, and so is Triple DES: AS2 trading partners still
	// send it (it is pyas2lib's and several commercial gateways' default), and refusing to read it would refuse the partner.
	var block cipher.Block
	var err error
	switch {
	case alg.Algorithm.Equal(oidAES128CBC) && len(cek) == 16, alg.Algorithm.Equal(oidAES192CBC) && len(cek) == 24,
		alg.Algorithm.Equal(oidAES256CBC) && len(cek) == 32:
		block, err = aes.NewCipher(cek)
	case alg.Algorithm.Equal(oidDESEDE3CBC) && len(cek) == 24:
		block, err = des.NewTripleDESCipher(cek)
	default:
		return nil, fmt.Errorf("cms: content encryption %v is not AES-CBC or Triple DES", alg.Algorithm)
	}
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	var iv []byte
	if _, err := asn1.Unmarshal(alg.Parameters.FullBytes, &iv); err != nil || len(iv) != bs {
		return nil, errors.New("cms: the IV cannot be read")
	}
	ct := ed.EncryptedContentInfo.EncryptedContent.Bytes
	if ed.EncryptedContentInfo.EncryptedContent.IsCompound {
		// Constructed OCTET STRING (BER, from some senders): concatenate its segments.
		ct = joinOctets(ct)
	}
	if len(ct) == 0 || len(ct)%bs != 0 {
		return nil, errors.New("cms: the encrypted content is not a whole number of cipher blocks")
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	n := int(out[len(out)-1])
	if n == 0 || n > bs || n > len(out) || !bytes.Equal(out[len(out)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, errors.New("cms: the decrypted content has bad padding")
	}
	return out[:len(out)-n], nil
}

func isSHA1OAEP(params []byte) bool {
	return !bytes.Contains(params, []byte{0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01})
}

func joinOctets(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		var part asn1.RawValue
		rest, err := asn1.Unmarshal(b, &part)
		if err != nil {
			return nil
		}
		out = append(out, part.Bytes...)
		b = rest
	}
	return out
}

// KeyPair is a certificate and its private key.
type KeyPair struct {
	Certificate *x509.Certificate
	Key         crypto.PrivateKey
}
