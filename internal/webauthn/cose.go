package webauthn

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"errors"
	"fmt"
	"math/big"
)

// COSE keys. A registration returns the credential's public key in this format, and everything afterwards depends on reading
// it correctly - a key parsed wrongly either rejects every subsequent sign-in or, far worse, verifies a signature it should
// not.
//
// # The algorithm comes from the key, never from the assertion
//
// This is the single most important decision in the file. A signature verification takes an algorithm and a key, and if the
// algorithm is read from data the client supplies, a caller can nominate one that suits them. That is algorithm confusion,
// and it is what turned JWT libraries into a decade of advisories.
//
// So the algorithm is recorded at registration, stored beside the key, and used at every subsequent verification. The
// assertion has no say in it. There is deliberately no code path that reads an algorithm from an assertion, because the
// safest way to not make that mistake is for the function that would do it not to exist.

// Errors from key handling.
var (
	// ErrUnsupportedKey means the credential uses an algorithm this does not implement.
	ErrUnsupportedKey = errors.New("unsupported credential key")

	// ErrBadKey means the key is malformed.
	ErrBadKey = errors.New("malformed credential key")
)

// Algorithm is a COSE algorithm identifier.
//
// Negative by convention in COSE, which is why the CBOR reader handles negative integers as a first-class case rather than
// an oddity.
type Algorithm int64

// The algorithms accepted, and only these.
const (
	// AlgES256 is ECDSA with P-256 and SHA-256. What almost every platform authenticator produces: Touch ID, Windows
	// Hello, Android.
	AlgES256 Algorithm = -7

	// AlgEdDSA is Ed25519. What several hardware keys produce.
	AlgEdDSA Algorithm = -8

	// AlgES384 and AlgES512 are the larger ECDSA curves, occasionally seen.
	AlgES384 Algorithm = -35
	AlgES512 Algorithm = -36

	// AlgRS256 is RSASSA-PKCS1-v1_5 with SHA-256.
	//
	// Included because Windows Hello produced it for years and there are many credentials in the field that use it.
	// Refusing it would lock people out of their own accounts, which is a worse outcome than supporting an older
	// algorithm - the key is still 2048 bits or more and the signature is still over data we chose.
	AlgRS256 Algorithm = -257

	// AlgRS384 and AlgRS512 for completeness with RS256.
	AlgRS384 Algorithm = -258
	AlgRS512 Algorithm = -259
)

// SupportedAlgorithms lists what a registration may offer, most preferred first.
//
// Order matters: it is sent to the browser as the credential parameters, and an authenticator picks the first it supports. So
// this is how a modern authenticator is steered towards ES256 or Ed25519 rather than RSA.
//
// RSA-PSS is deliberately absent even though it is stronger than PKCS#1 v1.5, because no authenticator in the field produces
// it and an algorithm nothing produces is an untested code path guarding a credential.
var SupportedAlgorithms = []Algorithm{
	AlgES256,
	AlgEdDSA,
	AlgES384,
	AlgES512,
	AlgRS256,
	AlgRS384,
	AlgRS512,
}

// Name gives an algorithm in words, for a log or an interface.
func (a Algorithm) Name() string {
	switch a {
	case AlgES256:
		return "ES256 (ECDSA P-256, SHA-256)"
	case AlgEdDSA:
		return "EdDSA (Ed25519)"
	case AlgES384:
		return "ES384 (ECDSA P-384, SHA-384)"
	case AlgES512:
		return "ES512 (ECDSA P-521, SHA-512)"
	case AlgRS256:
		return "RS256 (RSA PKCS#1 v1.5, SHA-256)"
	case AlgRS384:
		return "RS384 (RSA PKCS#1 v1.5, SHA-384)"
	case AlgRS512:
		return "RS512 (RSA PKCS#1 v1.5, SHA-512)"
	default:
		return fmt.Sprintf("algorithm %d", int64(a))
	}
}

// Supported reports whether this algorithm is accepted.
func (a Algorithm) Supported() bool {
	for _, candidate := range SupportedAlgorithms {
		if candidate == a {
			return true
		}
	}

	return false
}

// hash gives the digest an algorithm signs over.
func (a Algorithm) hash() (crypto.Hash, bool) {
	switch a {
	case AlgES256, AlgRS256:
		return crypto.SHA256, true
	case AlgES384, AlgRS384:
		return crypto.SHA384, true
	case AlgES512, AlgRS512:
		return crypto.SHA512, true
	case AlgEdDSA:
		// Ed25519 hashes internally and takes the message itself, so there is no separate digest.
		return 0, true
	default:
		return 0, false
	}
}

// COSE key labels, from RFC 8152.
const (
	coseKeyType   = 1
	coseAlgorithm = 3

	// Elliptic curve parameters.
	coseCurve = -1
	coseX     = -2
	coseY     = -3

	// RSA parameters. The labels collide numerically with the curve ones, which is why the key type is read first and
	// the parameters are read according to it - reading them the other way round would parse an RSA modulus as an
	// elliptic curve coordinate.
	coseModulus  = -1
	coseExponent = -2
)

// COSE key types.
const (
	keyTypeOKP = 1 // Octet key pair, which is Ed25519 here.
	keyTypeEC2 = 2 // Two-coordinate elliptic curve.
	keyTypeRSA = 3
)

// COSE curve identifiers.
const (
	curveP256    = 1
	curveP384    = 2
	curveP521    = 3
	curveEd25519 = 6
)

// CredentialKey is a credential's public key and the algorithm it was registered with.
//
// The two travel together deliberately. Storing a key without its algorithm would leave a later verification to decide which
// to use, and the only data available at that point comes from the client.
type CredentialKey struct {
	// Algorithm is what this key signs with, fixed at registration.
	Algorithm Algorithm

	// Public is the parsed key.
	Public crypto.PublicKey
}

// ParseCOSEKey reads a COSE public key.
func ParseCOSEKey(data []byte) (*CredentialKey, error) {
	value, err := DecodeCBOR(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadKey, err)
	}
	if value.Kind != CBORMap {
		return nil, fmt.Errorf("%w: a key must be a CBOR map, not a %s", ErrBadKey, value.Kind)
	}

	algorithmValue, ok := value.MapEntry(coseAlgorithm)
	if !ok {
		return nil, fmt.Errorf("%w: the key states no algorithm", ErrBadKey)
	}
	algorithmInt, ok := algorithmValue.AsInt()
	if !ok {
		return nil, fmt.Errorf("%w: the algorithm is not an integer", ErrBadKey)
	}

	algorithm := Algorithm(algorithmInt)
	if !algorithm.Supported() {
		// Refused at registration rather than accepted and discovered later. A credential stored with an algorithm
		// nothing here can verify is one that will refuse its owner at every sign-in, and they will have no idea why.
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKey, algorithm.Name())
	}

	keyTypeValue, ok := value.MapEntry(coseKeyType)
	if !ok {
		return nil, fmt.Errorf("%w: the key states no type", ErrBadKey)
	}
	keyType, ok := keyTypeValue.AsInt()
	if !ok {
		return nil, fmt.Errorf("%w: the key type is not an integer", ErrBadKey)
	}

	switch keyType {
	case keyTypeEC2:
		public, err := parseEC2(value, algorithm)
		if err != nil {
			return nil, err
		}

		return &CredentialKey{Algorithm: algorithm, Public: public}, nil

	case keyTypeOKP:
		public, err := parseOKP(value, algorithm)
		if err != nil {
			return nil, err
		}

		return &CredentialKey{Algorithm: algorithm, Public: public}, nil

	case keyTypeRSA:
		public, err := parseRSA(value, algorithm)
		if err != nil {
			return nil, err
		}

		return &CredentialKey{Algorithm: algorithm, Public: public}, nil

	default:
		return nil, fmt.Errorf("%w: key type %d", ErrUnsupportedKey, keyType)
	}
}

// parseEC2 reads a two-coordinate elliptic curve key.
func parseEC2(value CBORValue, algorithm Algorithm) (*ecdsa.PublicKey, error) {
	curveValue, ok := value.MapEntry(coseCurve)
	if !ok {
		return nil, fmt.Errorf("%w: an elliptic curve key states no curve", ErrBadKey)
	}
	curveID, ok := curveValue.AsInt()
	if !ok {
		return nil, fmt.Errorf("%w: the curve is not an integer", ErrBadKey)
	}

	var curve elliptic.Curve
	switch curveID {
	case curveP256:
		curve = elliptic.P256()
	case curveP384:
		curve = elliptic.P384()
	case curveP521:
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("%w: curve %d", ErrUnsupportedKey, curveID)
	}

	// The curve has to match the algorithm.
	//
	// A P-256 key claiming ES512 would be verified with SHA-512 against a curve too small for it, and the mismatch is
	// exactly the kind of inconsistency an attacker constructs. Checked rather than trusted, because both halves come
	// from the client.
	expected := map[Algorithm]int64{AlgES256: curveP256, AlgES384: curveP384, AlgES512: curveP521}
	if want, known := expected[algorithm]; known && want != curveID {
		return nil, fmt.Errorf("%w: %s was offered with curve %d, which do not go together",
			ErrBadKey, algorithm.Name(), curveID)
	}

	xValue, ok := value.MapEntry(coseX)
	if !ok || xValue.Kind != CBORBytes {
		return nil, fmt.Errorf("%w: the key has no x coordinate", ErrBadKey)
	}
	yValue, ok := value.MapEntry(coseY)
	if !ok || yValue.Kind != CBORBytes {
		return nil, fmt.Errorf("%w: the key has no y coordinate", ErrBadKey)
	}

	// Coordinates must be exactly the field size. A shorter value would be left-padded by SetBytes into a different
	// point, and accepting one means accepting a key that is not the key the authenticator holds.
	size := (curve.Params().BitSize + 7) / 8
	if len(xValue.Bytes) != size || len(yValue.Bytes) != size {
		return nil, fmt.Errorf("%w: coordinates are %d and %d bytes where %d are required for this curve",
			ErrBadKey, len(xValue.Bytes), len(yValue.Bytes), size)
	}

	x := new(big.Int).SetBytes(xValue.Bytes)
	y := new(big.Int).SetBytes(yValue.Bytes)

	// The point must actually be on the curve.
	//
	// Not a formality. An off-curve point can leak information about a private key in some implementations, and a point
	// at infinity would verify anything. Go's own ecdsa refuses to use an invalid point, but checking here means the
	// credential is never stored in the first place.
	if !curve.IsOnCurve(x, y) {
		return nil, fmt.Errorf("%w: the public key is not a point on the stated curve", ErrBadKey)
	}

	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

// parseOKP reads an octet key pair, which here means Ed25519.
func parseOKP(value CBORValue, algorithm Algorithm) (ed25519.PublicKey, error) {
	if algorithm != AlgEdDSA {
		return nil, fmt.Errorf("%w: an octet key pair was offered with %s", ErrBadKey, algorithm.Name())
	}

	curveValue, ok := value.MapEntry(coseCurve)
	if !ok {
		return nil, fmt.Errorf("%w: an octet key pair states no curve", ErrBadKey)
	}
	curveID, ok := curveValue.AsInt()
	if !ok || curveID != curveEd25519 {
		return nil, fmt.Errorf("%w: octet key pair curve %v", ErrUnsupportedKey, curveValue)
	}

	xValue, ok := value.MapEntry(coseX)
	if !ok || xValue.Kind != CBORBytes {
		return nil, fmt.Errorf("%w: the key has no public value", ErrBadKey)
	}
	if len(xValue.Bytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: an Ed25519 key is %d bytes and this is %d",
			ErrBadKey, ed25519.PublicKeySize, len(xValue.Bytes))
	}

	return ed25519.PublicKey(xValue.Bytes), nil
}

// parseRSA reads an RSA key.
func parseRSA(value CBORValue, algorithm Algorithm) (*rsa.PublicKey, error) {
	switch algorithm {
	case AlgRS256, AlgRS384, AlgRS512:
	default:
		return nil, fmt.Errorf("%w: an RSA key was offered with %s", ErrBadKey, algorithm.Name())
	}

	modulusValue, ok := value.MapEntry(coseModulus)
	if !ok || modulusValue.Kind != CBORBytes {
		return nil, fmt.Errorf("%w: the key has no modulus", ErrBadKey)
	}
	exponentValue, ok := value.MapEntry(coseExponent)
	if !ok || exponentValue.Kind != CBORBytes {
		return nil, fmt.Errorf("%w: the key has no exponent", ErrBadKey)
	}

	// A minimum size, because a small modulus is a key that can be factored.
	//
	// 2048 bits is the floor every current guideline agrees on. Refusing a smaller one at registration means nobody ends
	// up depending on a credential that should never have been accepted - and an authenticator producing one is broken
	// in a way its owner should discover now rather than later.
	const minModulusBytes = 256
	if len(modulusValue.Bytes) < minModulusBytes {
		return nil, fmt.Errorf("%w: the RSA modulus is %d bits, and 2048 is the minimum",
			ErrBadKey, len(modulusValue.Bytes)*8)
	}
	// And a ceiling, because verification cost grows with the modulus and an enormous one is a way to make one request
	// expensive.
	const maxModulusBytes = 1024
	if len(modulusValue.Bytes) > maxModulusBytes {
		return nil, fmt.Errorf("%w: the RSA modulus is %d bits, which is beyond anything real",
			ErrBadKey, len(modulusValue.Bytes)*8)
	}

	exponent := new(big.Int).SetBytes(exponentValue.Bytes)
	if !exponent.IsInt64() {
		return nil, fmt.Errorf("%w: the RSA exponent does not fit in an integer", ErrBadKey)
	}
	e := exponent.Int64()

	// An exponent of 1 makes a signature equal to its own plaintext, which verifies anything. Even numbers are not valid
	// exponents at all. Both are refused rather than left to the verifier.
	if e < 3 || e%2 == 0 {
		return nil, fmt.Errorf("%w: the RSA exponent %d is not usable", ErrBadKey, e)
	}
	if e > 1<<31 {
		return nil, fmt.Errorf("%w: the RSA exponent is implausibly large", ErrBadKey)
	}

	key := &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulusValue.Bytes),
		E: int(e),
	}

	return key, nil
}
