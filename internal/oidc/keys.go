package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// KeySet holds an identity provider's signing keys.
//
// Cached, because fetching them on every sign-in would make the identity provider a hard dependency of every page load. Keys
// are refetched when a token names a key not held, which is how key rotation is handled without a schedule: providers rotate
// on their own timetable and publish the new key before using it, so the trigger is a token this cannot verify rather than a
// clock.
type KeySet struct {
	url    string
	client *http.Client

	mu      sync.RWMutex
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

// NewKeySet prepares a key set for a JWKS endpoint.
func NewKeySet(jwksURL string, client *http.Client) *KeySet {
	if client == nil {
		client = DefaultHTTPClient
	}
	return &KeySet{url: jwksURL, client: client, keys: map[string]crypto.PublicKey{}}
}

// minRefetchInterval bounds how often keys are refetched.
//
// Without it, tokens naming an unknown key would each trigger a fetch, and anybody can send an unlimited number of those - so
// the rotation mechanism would double as a way to make Perfuse hammer its own identity provider.
const minRefetchInterval = 30 * time.Second

// jwk is one key from a JWKS document.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`

	// RSA.
	N string `json:"n"`
	E string `json:"e"`

	// EC.
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// Key returns the public key with an identifier, fetching if it is not held.
func (s *KeySet) Key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	s.mu.RLock()
	key, ok := s.keys[kid]
	last := s.fetched
	s.mu.RUnlock()

	if ok {
		return key, nil
	}

	if !last.IsZero() && time.Since(last) < minRefetchInterval {
		// Refused rather than fetched. A token naming a key the provider does not publish is either very stale or forged,
		// and either way refetching on demand would let anyone drive traffic at the identity provider.
		return nil, fmt.Errorf("oidc: the token was signed with key %q, which the identity provider does not publish", kid)
	}

	if err := s.fetch(ctx); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key, ok = s.keys[kid]
	if !ok {
		return nil, fmt.Errorf("oidc: the token was signed with key %q, which is not among the %d key(s) the identity "+
			"provider publishes", kid, len(s.keys))
	}

	return key, nil
}

// fetch reloads the key set.
func (s *KeySet) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: could not fetch signing keys from %s: %w", s.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: the identity provider answered %s for its signing keys", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("oidc: the signing key document could not be read: %w", err)
	}

	parsed := make(map[string]crypto.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		// Encryption keys are skipped rather than rejected. A provider publishing both in one document is normal, and
		// failing on the first one that is not for signatures would make sign-in impossible against a correct provider.
		if k.Use != "" && k.Use != "sig" {
			continue
		}

		key, err := k.publicKey()
		if err != nil {
			// One unreadable key does not spoil the set: a provider may publish a type this does not implement alongside
			// one it does, and refusing everything would turn a partial mismatch into a total outage.
			continue
		}
		if k.Kid == "" {
			// A single unnamed key is legal, and a token from such a provider carries no kid either. Stored under the
			// empty string so the lookup finds it.
			parsed[""] = key
			continue
		}
		parsed[k.Kid] = key
	}

	if len(parsed) == 0 {
		return fmt.Errorf("oidc: the identity provider published no signing keys this can use; it may be using an " +
			"algorithm other than RS256, RS384, RS512, ES256, ES384 or ES512")
	}

	s.mu.Lock()
	s.keys = parsed
	s.fetched = time.Now()
	s.mu.Unlock()

	return nil
}

// publicKey converts a JWK to a public key.
func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := base64URLBigInt(k.N)
		if err != nil {
			return nil, err
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.E, "="))
		if err != nil {
			return nil, err
		}
		if len(eBytes) == 0 || len(eBytes) > 8 {
			return nil, fmt.Errorf("oidc: an RSA exponent of %d bytes is not usable", len(eBytes))
		}
		// Left-padded to eight bytes so it can be read as a big-endian integer. Exponents are conventionally three bytes
		// (65537) but the format does not require that.
		padded := make([]byte, 8)
		copy(padded[8-len(eBytes):], eBytes)
		e := binary.BigEndian.Uint64(padded)
		if e > 1<<31 {
			return nil, fmt.Errorf("oidc: an RSA exponent that large is not usable")
		}

		key := &rsa.PublicKey{N: n, E: int(e)}
		if key.N.BitLen() < 2048 {
			// Refused. A key shorter than 2048 bits is below what any current guidance accepts, and accepting it would
			// mean the weakest provider decides how strong Perfuse's authentication is.
			return nil, fmt.Errorf("oidc: an RSA signing key of %d bits is too weak to trust", key.N.BitLen())
		}
		return key, nil

	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("oidc: elliptic curve %q is not implemented", k.Crv)
		}

		x, err := base64URLBigInt(k.X)
		if err != nil {
			return nil, err
		}
		y, err := base64URLBigInt(k.Y)
		if err != nil {
			return nil, err
		}

		key := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		if !curve.IsOnCurve(x, y) {
			// Checked rather than assumed. A point not on the curve is not a key, and some verification paths given one
			// will happily return true for a forged signature.
			return nil, fmt.Errorf("oidc: the published elliptic curve point is not on curve %s", k.Crv)
		}
		return key, nil
	}

	return nil, fmt.Errorf("oidc: key type %q is not implemented", k.Kty)
}

func base64URLBigInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("oidc: a key value was not base64url: %w", err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("oidc: a key value was empty")
	}
	return new(big.Int).SetBytes(b), nil
}

// verify checks a signature over signed with the algorithm named.
//
// The algorithm comes from the token header, which is the classic mistake in this area, so it is constrained twice: only these
// six are implemented, and the caller separately states which it will accept. "none" and the HMAC family are absent entirely
// rather than rejected by name - the algorithm confusion attack works by getting a verifier to treat the public key as an
// HMAC secret, and a verifier that cannot do HMAC at all cannot be talked into it.
func verify(alg string, key crypto.PublicKey, signed, signature []byte) error {
	switch alg {
	case "RS256", "RS384", "RS512":
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("oidc: the token says %s but the signing key is not RSA", alg)
		}
		hash, digest := digestFor(alg, signed)
		return rsa.VerifyPKCS1v15(rsaKey, hash, digest, signature)

	case "PS256", "PS384", "PS512":
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("oidc: the token says %s but the signing key is not RSA", alg)
		}
		hash, digest := digestFor(alg, signed)
		return rsa.VerifyPSS(rsaKey, hash, digest, signature, nil)

	case "ES256", "ES384", "ES512":
		ecKey, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("oidc: the token says %s but the signing key is not an elliptic curve key", alg)
		}
		hash, digest := digestFor(alg, signed)
		_ = hash

		// Two fixed-width integers, not an ASN.1 sequence. This is where JWT and most other uses of ECDSA differ, and
		// getting it wrong produces a verifier that rejects every valid signature.
		size := (ecKey.Curve.Params().BitSize + 7) / 8
		if len(signature) != 2*size {
			return fmt.Errorf("oidc: an %s signature should be %d bytes and this one is %d", alg, 2*size, len(signature))
		}
		r := new(big.Int).SetBytes(signature[:size])
		s := new(big.Int).SetBytes(signature[size:])

		if !ecdsa.Verify(ecKey, digest, r, s) {
			return fmt.Errorf("oidc: the token signature does not verify")
		}
		return nil
	}

	return fmt.Errorf("oidc: signing algorithm %q is not implemented; this accepts RS256, RS384, RS512, PS256, PS384, "+
		"PS512, ES256, ES384 and ES512", alg)
}

// digestFor hashes the signing input for an algorithm.
func digestFor(alg string, signed []byte) (crypto.Hash, []byte) {
	switch alg[2:] {
	case "384":
		sum := sha512.Sum384(signed)
		return crypto.SHA384, sum[:]
	case "512":
		sum := sha512.Sum512(signed)
		return crypto.SHA512, sum[:]
	default:
		sum := sha256.Sum256(signed)
		return crypto.SHA256, sum[:]
	}
}

// SupportedAlgorithms are the signing algorithms this implements.
var SupportedAlgorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}
