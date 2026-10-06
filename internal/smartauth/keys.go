// Package smartauth is a SMART App Launch authorization server: it issues the access tokens Perfuse's FHIR endpoint accepts.
//
// Written against the standard library, like internal/oidc, which verifies what this signs. The tokens are JWTs signed RS256,
// the one algorithm every SMART client and the specification's ID token rules can rely on.
package smartauth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
)

// Key is the server's signing key.
type Key struct {
	private *rsa.PrivateKey
	// ID is the key's JWK thumbprint (RFC 7638), so a token names the key it was signed with and a rotated key is told apart.
	ID string
}

// LoadOrCreateKey reads a PEM private key, or creates a 2048-bit RSA key there (mode 0600) when the file does not exist. A key
// file anyone else may read is refused: whoever holds it can sign tokens for every patient.
func LoadOrCreateKey(path string) (*Key, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
		return newKey(k), nil
	}
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s can be read by other users (mode %v); it signs tokens, so make it 0600", path, info.Mode().Perm())
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s is not a PEM private key", path)
	}
	var parsed any
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("%s holds a %s; an RSA private key is needed", path, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok || k.N.BitLen() < 2048 {
		return nil, fmt.Errorf("%s: an RSA key of at least 2048 bits is needed", path)
	}
	return newKey(k), nil
}

func newKey(k *rsa.PrivateKey) *Key {
	key := &Key{private: k}
	// RFC 7638: the SHA-256 of the required members in lexical order, with no whitespace.
	n, e := b64(k.N.Bytes()), b64(big.NewInt(int64(k.E)).Bytes())
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	key.ID = b64(sum[:])
	return key
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// JWKS is the public half, as /auth/jwks serves it.
func (k *Key) JWKS() []byte {
	doc := map[string]any{"keys": []any{map[string]string{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": k.ID,
		"n": b64(k.private.N.Bytes()), "e": b64(big.NewInt(int64(k.private.E)).Bytes()),
	}}}
	out, _ := json.Marshal(doc)
	return out
}

// Sign returns a compact RS256 JWT carrying the claims.
func (k *Key) Sign(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.ID})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed := b64(header) + "." + b64(body)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.private, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signed + "." + b64(sig), nil
}
