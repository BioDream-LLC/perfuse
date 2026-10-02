package udap

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Verification of a server's signed metadata.
//
// This is the part that matters, and the part this repository has got wrong before. The SAML verifier was written here, tested here
// against documents written here, and could not accept an assertion from any real identity provider - while a thousand lines of tests
// said it worked. A separate SAML defect was worse: the verifier preferred the certificate embedded in the document it was checking,
// which meant anybody could sign their own assertion and be believed as an administrator.
//
// Both mistakes are available in UDAP. The signature travels with its own certificate chain in the x5c header, and a verifier that
// trusts that chain because it is there has verified nothing - it has confirmed a document is internally consistent, which forgeries
// also are. So a trust anchor is required and its absence is an error rather than a warning.

// TrustAnchors are the community certificates a client is willing to believe.
//
// In UDAP a trust community operates certificate authorities and issues certificates to members; holding one is what membership means.
// Under TEFCA they come out of QHIN onboarding and cannot be self-issued, which is precisely why they must be configured rather than
// discovered.
type TrustAnchors struct {
	// Roots are the community's certificate authorities.
	Roots *x509.CertPool

	// Intermediates may be supplied out of band, though a well-behaved server sends its chain in x5c.
	Intermediates *x509.CertPool
}

// VerifyOptions controls verification of a signed metadata document.
type VerifyOptions struct {
	// BaseURL is the FHIR base URL the document was fetched from. The specification requires iss and sub to equal it, which is what
	// stops one server's signed document being replayed as another's.
	BaseURL string

	// Anchors are the trust community's certificates. Required unless AllowUnanchored is set.
	Anchors TrustAnchors

	// AllowUnanchored skips chain verification, leaving the signature and claims checked but not the issuer's membership.
	//
	// Off by default and it must stay off outside a laboratory. With it on, any party can mint a certificate, sign a metadata document
	// with it and be believed - the SAML defect this package was written not to repeat, in a different protocol. It exists so a
	// reference server in a sandbox, whose certificate authority nobody would install, can still prove the rest of the implementation
	// works.
	AllowUnanchored bool

	// Now is the clock, for tests. Zero means time.Now.
	Now time.Time

	// Leeway absorbs clock skew between two organisations. Thirty seconds when zero.
	Leeway time.Duration
}

// SignedMetadataClaims are the claims of the signed metadata JWT.
type SignedMetadataClaims struct {
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`

	IssuedAt  int64 `json:"iat"`
	ExpiresAt int64 `json:"exp"`

	JTI string `json:"jti"`

	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// permittedAlgorithms are the signature algorithms this package will verify.
//
// An explicit allowlist, because the alternative is reading the algorithm out of the document and doing what it says. That is how
// algorithm confusion works: a token claiming "none" verifies trivially, and one claiming HMAC where RSA was expected can be signed with
// the public key as the shared secret. The guide requires RS256 and permits ES256, ES384 and RS384.
var permittedAlgorithms = map[string]bool{
	"RS256": true,
	"RS384": true,
	"ES256": true,
	"ES384": true,
}

// VerifySignedMetadata checks a metadata document's signature and claims, and returns the claims it proved.
//
// The order is deliberate: nothing in the claims is read until the signature has been verified against an anchored certificate, because
// reading unverified claims is how a verifier ends up reporting the contents of a forgery.
func VerifySignedMetadata(md *Metadata, opts VerifyOptions) (*SignedMetadataClaims, error) {
	if md == nil {
		return nil, errors.New("udap: no metadata")
	}

	if strings.TrimSpace(md.SignedMetadata) == "" {
		// Required by the specification, and its absence is not a soft failure: an unsigned metadata document tells you what whoever
		// is on the path wants you to believe.
		return nil, errors.New("udap: the metadata has no signed_metadata element, so none of it is trustworthy")
	}

	header, payload, signature, signingInput, err := splitJWS(md.SignedMetadata)
	if err != nil {
		return nil, err
	}

	if !permittedAlgorithms[header.Algorithm] {
		return nil, fmt.Errorf("udap: signed_metadata uses algorithm %q, which this client will not accept", header.Algorithm)
	}

	chain, err := parseX5C(header.X5C)
	if err != nil {
		return nil, err
	}

	leaf := chain[0]

	// The chain first, then the signature. A signature that verifies against an untrusted key proves only that whoever made the
	// document also made the key.
	if err := verifyChain(leaf, chain[1:], opts); err != nil {
		return nil, err
	}

	if err := verifySignature(header.Algorithm, leaf.PublicKey, signingInput, signature); err != nil {
		return nil, fmt.Errorf("udap: signed_metadata signature does not verify: %w", err)
	}

	var claims SignedMetadataClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("udap: signed_metadata claims are not JSON: %w", err)
	}

	if err := checkClaims(&claims, leaf, opts); err != nil {
		return nil, err
	}

	// And the reason the document is signed at all: the endpoints a client uses must be the ones that were signed.
	//
	// Verifying the signature and then reading endpoints from the unsigned part would be an elaborate way of trusting the transport. An
	// attacker who can rewrite the response would leave signed_metadata untouched, change token_endpoint to their own server, and
	// collect client assertions from everyone who checked the signature and then ignored it.
	if err := endpointsAgree(md, &claims); err != nil {
		return nil, err
	}

	return &claims, nil
}

// jwsHeader is the JOSE header of a signed metadata document.
type jwsHeader struct {
	Algorithm string   `json:"alg"`
	X5C       []string `json:"x5c"`
	Type      string   `json:"typ"`
}

// splitJWS pulls a compact JWS apart without interpreting any of it.
func splitJWS(token string) (*jwsHeader, []byte, []byte, []byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil, nil, nil, fmt.Errorf("udap: signed_metadata has %d segments, want 3", len(parts))
	}

	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("udap: signed_metadata header is not base64url: %w", err)
	}

	var header jwsHeader
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("udap: signed_metadata header is not JSON: %w", err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("udap: signed_metadata payload is not base64url: %w", err)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("udap: signed_metadata signature is not base64url: %w", err)
	}

	// The signing input is the first two segments exactly as they arrived, byte for byte. Re-encoding the decoded halves would produce
	// a different string for any document not byte-identical to this library's own output, which is the mistake that made the SAML
	// canonicaliser unable to read anybody else's documents.
	signingInput := []byte(parts[0] + "." + parts[1])

	return &header, payload, signature, signingInput, nil
}

// parseX5C decodes the certificate chain from the JWT header.
func parseX5C(x5c []string) ([]*x509.Certificate, error) {
	if len(x5c) == 0 {
		return nil, errors.New("udap: signed_metadata has no x5c certificate chain, so there is no key to check it with")
	}

	out := make([]*x509.Certificate, 0, len(x5c))

	for i, encoded := range x5c {
		// Standard base64, not base64url: RFC 7515 section 4.1.6 specifies the DER as base64-encoded, and the chain is not part of the
		// URL-safe portion of the token.
		der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return nil, fmt.Errorf("udap: x5c entry %d is not base64: %w", i, err)
		}

		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("udap: x5c entry %d is not a certificate: %w", i, err)
		}

		out = append(out, cert)
	}

	return out, nil
}

// verifyChain checks the leaf against the configured trust anchors.
func verifyChain(leaf *x509.Certificate, rest []*x509.Certificate, opts VerifyOptions) error {
	if opts.AllowUnanchored {
		return nil
	}

	if opts.Anchors.Roots == nil {
		// An error, not a default. Verifying a signature against a certificate that arrived with it and calling the result trusted is
		// the SAML defect this package exists not to repeat: it authenticated an attacker as an administrator while every test passed.
		return errors.New("udap: no trust anchors are configured, so the certificate in the document cannot be checked against " +
			"anything. A certificate that travels with the signature it verifies proves only that one party made both")
	}

	intermediates := x509.NewCertPool()
	if opts.Anchors.Intermediates != nil {
		intermediates = opts.Anchors.Intermediates.Clone()
	}

	for _, cert := range rest {
		intermediates.AddCert(cert)
	}

	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         opts.Anchors.Roots,
		Intermediates: intermediates,
		CurrentTime:   clock(opts),
		// No key usage constraint. Community certificates sign JWTs rather than terminate TLS, and requiring server authentication here
		// would reject exactly the certificates the profile issues.
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("udap: the signing certificate does not chain to a configured trust anchor: %w", err)
	}

	return nil
}

// verifySignature checks a JWS signature with the algorithm named in the header.
func verifySignature(alg string, key any, signingInput, signature []byte) error {
	hash, digest := digestFor(alg, signingInput)

	switch typed := key.(type) {
	case *rsa.PublicKey:
		if !strings.HasPrefix(alg, "RS") {
			return fmt.Errorf("udap: algorithm %q with an RSA key", alg)
		}

		return rsa.VerifyPKCS1v15(typed, hash, digest, signature)

	case *ecdsa.PublicKey:
		if !strings.HasPrefix(alg, "ES") {
			return fmt.Errorf("udap: algorithm %q with an elliptic curve key", alg)
		}

		// JWS carries r and s as fixed-width halves rather than the ASN.1 sequence crypto/ecdsa produces.
		if len(signature)%2 != 0 {
			return fmt.Errorf("udap: an ES signature of %d bytes cannot be split in two", len(signature))
		}

		half := len(signature) / 2
		r := new(big.Int).SetBytes(signature[:half])
		s := new(big.Int).SetBytes(signature[half:])

		if !ecdsa.Verify(typed, digest, r, s) {
			return errors.New("udap: the elliptic curve signature is not valid for this key")
		}

		return nil

	default:
		return fmt.Errorf("udap: a %T cannot verify a signature", key)
	}
}

// digestFor hashes the signing input as the algorithm requires.
func digestFor(alg string, signingInput []byte) (crypto.Hash, []byte) {
	switch alg {
	case "RS384", "ES384":
		sum := sha512.Sum384(signingInput)

		return crypto.SHA384, sum[:]
	default:
		sum := sha256.Sum256(signingInput)

		return crypto.SHA256, sum[:]
	}
}

// checkClaims applies the specification's requirements on the signed claims.
func checkClaims(claims *SignedMetadataClaims, leaf *x509.Certificate, opts VerifyOptions) error {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")

	if claims.Issuer == "" || claims.Subject == "" {
		return errors.New("udap: signed_metadata is missing iss or sub")
	}

	if claims.Issuer != claims.Subject {
		return fmt.Errorf("udap: signed_metadata has iss %q and sub %q, which must be equal", claims.Issuer, claims.Subject)
	}

	if base != "" && strings.TrimRight(claims.Issuer, "/") != base {
		// This stops one server's signed document being served by another. Without it, a valid document from any member of the
		// community authenticates every other member's endpoints.
		return fmt.Errorf("udap: signed_metadata is issued for %q but was fetched from %q", claims.Issuer, base)
	}

	// The certificate has to name the issuer. A member holding a community certificate for one URI must not be able to sign metadata
	// claiming to be a different URI in the same community - that is the difference between authenticating a member and authenticating
	// a member as themselves.
	if err := certificateNamesURI(leaf, claims.Issuer); err != nil {
		return err
	}

	now := clock(opts)

	leeway := opts.Leeway
	if leeway == 0 {
		leeway = 30 * time.Second
	}

	if claims.ExpiresAt == 0 || claims.IssuedAt == 0 {
		return errors.New("udap: signed_metadata is missing iat or exp")
	}

	issued := time.Unix(claims.IssuedAt, 0)
	expires := time.Unix(claims.ExpiresAt, 0)

	if now.After(expires.Add(leeway)) {
		return fmt.Errorf("udap: signed_metadata expired at %s", expires.UTC().Format(time.RFC3339))
	}

	if issued.After(now.Add(leeway)) {
		return fmt.Errorf("udap: signed_metadata is issued in the future, at %s", issued.UTC().Format(time.RFC3339))
	}

	// A year, per the specification. A document signed once with a decade of life is a key compromise nobody finds out about.
	if expires.Sub(issued) > 366*24*time.Hour {
		return fmt.Errorf("udap: signed_metadata is valid for %s, longer than the permitted year", expires.Sub(issued).Round(time.Hour))
	}

	if claims.JTI == "" {
		return errors.New("udap: signed_metadata has no jti")
	}

	if claims.TokenEndpoint == "" || claims.RegistrationEndpoint == "" {
		return errors.New("udap: signed_metadata must carry both token_endpoint and registration_endpoint")
	}

	return nil
}

// certificateNamesURI checks that a certificate's subject alternative names include a URI.
func certificateNamesURI(cert *x509.Certificate, want string) error {
	target := strings.TrimRight(want, "/")

	for _, u := range cert.URIs {
		if strings.TrimRight(u.String(), "/") == target {
			return nil
		}
	}

	// Some issuers put the identifier in a DNS name or the common name instead. Accepted with the host compared rather than the whole
	// URI, because refusing a certificate the community issued would make this client unable to talk to conforming servers, and the
	// identifier is still bound to the certificate either way.
	host := hostOf(target)
	if host != "" {
		for _, dns := range cert.DNSNames {
			if strings.EqualFold(dns, host) {
				return nil
			}
		}

		if strings.EqualFold(cert.Subject.CommonName, host) {
			return nil
		}
	}

	return fmt.Errorf("udap: the signing certificate does not name %q (URIs %v, DNS %v, CN %q), so it is not that server's certificate",
		want, cert.URIs, cert.DNSNames, cert.Subject.CommonName)
}

// hostOf extracts a host from a URL-ish string without failing on odd input.
func hostOf(raw string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if idx := strings.IndexAny(trimmed, "/:"); idx >= 0 {
		trimmed = trimmed[:idx]
	}

	return trimmed
}

// endpointsAgree checks the unsigned metadata against what was signed.
func endpointsAgree(md *Metadata, claims *SignedMetadataClaims) error {
	for _, pair := range []struct {
		name     string
		unsigned string
		signed   string
	}{
		{"token_endpoint", md.TokenEndpoint, claims.TokenEndpoint},
		{"registration_endpoint", md.RegistrationEndpoint, claims.RegistrationEndpoint},
		{"authorization_endpoint", md.AuthorizationEndpoint, claims.AuthorizationEndpoint},
	} {
		// An authorization endpoint is conditional: absent from both is fine, present in only one is not.
		if pair.unsigned == "" && pair.signed == "" {
			continue
		}

		if strings.TrimRight(pair.unsigned, "/") != strings.TrimRight(pair.signed, "/") {
			return fmt.Errorf("udap: %s is %q in the document and %q in the signed part, so the document was altered after signing",
				pair.name, pair.unsigned, pair.signed)
		}
	}

	return nil
}

// clock returns the time verification should use.
func clock(opts VerifyOptions) time.Time {
	if !opts.Now.IsZero() {
		return opts.Now
	}

	return time.Now()
}
