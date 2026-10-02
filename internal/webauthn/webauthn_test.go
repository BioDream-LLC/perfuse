package webauthn

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// These tests act as a software authenticator: a real key, real authenticator data, a real signature. Anything less would be
// asserting that the parser reads bytes rather than that the verification rejects what it should.
//
// Every check then gets its own test that tampers with exactly one thing and confirms the refusal. That is the only way to
// know a check is load-bearing rather than decorative - and it has caught four decorative tests already tonight in other
// packages.

var testConfig = Config{
	RPID:    "perfuse.example.org",
	RPName:  "Perfuse",
	Origins: []string{"https://perfuse.example.org"},
}

// softAuthenticator is a WebAuthn authenticator implemented in software.
type softAuthenticator struct {
	key          *ecdsa.PrivateKey
	credentialID []byte
	signCount    uint32
	aaguid       []byte
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}

	return &softAuthenticator{
		key:          key,
		credentialID: id,
		aaguid:       make([]byte, 16),
	}
}

// coseKey encodes the public key as COSE, which is what an authenticator returns.
func (a *softAuthenticator) coseKey() []byte {
	x := a.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.key.PublicKey.Y.FillBytes(make([]byte, 32))

	// A five-entry map: kty 2 (EC2), alg -7 (ES256), crv 1 (P-256), x, y.
	var b []byte
	b = append(b, 0xa5) // map of 5

	b = append(b, 0x01, 0x02)       // 1: 2
	b = append(b, 0x03, 0x26)       // 3: -7
	b = append(b, 0x20, 0x01)       // -1: 1
	b = append(b, 0x21, 0x58, 0x20) // -2: bytes(32)
	b = append(b, x...)
	b = append(b, 0x22, 0x58, 0x20) // -3: bytes(32)
	b = append(b, y...)

	return b
}

// authData builds authenticator data.
func (a *softAuthenticator) authData(rpID string, flags byte, includeCredential bool) []byte {
	hash := sha256.Sum256([]byte(rpID))

	out := make([]byte, 0, 128)
	out = append(out, hash[:]...)
	out = append(out, flags)

	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, a.signCount)
	out = append(out, count...)

	if includeCredential {
		out = append(out, a.aaguid...)
		length := make([]byte, 2)
		binary.BigEndian.PutUint16(length, uint16(len(a.credentialID)))
		out = append(out, length...)
		out = append(out, a.credentialID...)
		out = append(out, a.coseKey()...)
	}

	return out
}

// attestationObject wraps authenticator data in the "none" attestation format.
func attestationObject(authData []byte) []byte {
	// A three-entry map: fmt "none", attStmt {}, authData bytes.
	var b []byte
	b = append(b, 0xa3)

	b = append(b, 0x63, 'f', 'm', 't')                     // "fmt"
	b = append(b, 0x64, 'n', 'o', 'n', 'e')                // "none"
	b = append(b, 0x67, 'a', 't', 't', 'S', 't', 'm', 't') // "attStmt"
	b = append(b, 0xa0)                                    // empty map
	b = append(b, 0x68, 'a', 'u', 't', 'h', 'D', 'a', 't', 'a')
	b = append(b, encodeByteString(authData)...)

	return b
}

// encodeByteString writes a CBOR byte string header and content.
func encodeByteString(data []byte) []byte {
	var out []byte
	switch {
	case len(data) < 24:
		out = append(out, byte(0x40|len(data)))
	case len(data) < 256:
		out = append(out, 0x58, byte(len(data)))
	default:
		out = append(out, 0x59, byte(len(data)>>8), byte(len(data)))
	}

	return append(out, data...)
}

// clientDataFor builds a clientDataJSON.
func clientDataFor(kind string, challenge []byte, origin string, crossOrigin bool) []byte {
	data := map[string]any{
		"type":        kind,
		"challenge":   base64.RawURLEncoding.EncodeToString(challenge),
		"origin":      origin,
		"crossOrigin": crossOrigin,
	}
	encoded, _ := json.Marshal(data)

	return encoded
}

// register produces a registration response.
func (a *softAuthenticator) register(challenge []byte, origin string, flags byte) RegistrationResponse {
	clientData := clientDataFor("webauthn.create", challenge, origin, false)
	authData := a.authData(testConfig.RPID, flags, true)

	var response RegistrationResponse
	response.Type = "public-key"
	response.ID = base64.RawURLEncoding.EncodeToString(a.credentialID)
	response.RawID = response.ID
	response.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(clientData)
	response.Response.AttestationObject = base64.RawURLEncoding.EncodeToString(attestationObject(authData))

	return response
}

// authenticate produces a sign-in response, signing properly.
func (a *softAuthenticator) authenticate(
	t *testing.T, challenge []byte, origin, rpID string, flags byte,
) AuthenticationResponse {
	t.Helper()

	clientData := clientDataFor("webauthn.get", challenge, origin, false)
	authData := a.authData(rpID, flags, false)

	digest := sha256.Sum256(clientData)
	message := append(append([]byte{}, authData...), digest[:]...)
	signed := sha256.Sum256(message)

	signature, err := ecdsa.SignASN1(rand.Reader, a.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}

	var response AuthenticationResponse
	response.Type = "public-key"
	response.ID = base64.RawURLEncoding.EncodeToString(a.credentialID)
	response.RawID = response.ID
	response.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(clientData)
	response.Response.AuthenticatorData = base64.RawURLEncoding.EncodeToString(authData)
	response.Response.Signature = base64.RawURLEncoding.EncodeToString(signature)

	return response
}

// registerAndStore does a full registration and returns the credential.
func registerAndStore(t *testing.T, a *softAuthenticator) (*Credential, []byte) {
	t.Helper()

	_, challenge, err := BeginRegistration(testConfig, []byte("user-handle-1"), "rturner", "Robert Turner", nil)
	if err != nil {
		t.Fatal(err)
	}

	credential, err := FinishRegistration(testConfig, challenge,
		a.register(challenge, "https://perfuse.example.org", flagUserPresent|flagUserVerified|flagAttestedData))
	if err != nil {
		t.Fatal(err)
	}

	return credential, challenge
}

func TestARealPasskeyRegistersAndSignsIn(t *testing.T) {
	authenticator := newSoftAuthenticator(t)

	credential, _ := registerAndStore(t, authenticator)

	if credential.Algorithm != AlgES256 {
		t.Errorf("algorithm is %s", credential.Algorithm.Name())
	}
	if len(credential.PublicKey) == 0 {
		t.Error("no public key was stored")
	}
	if !credential.UserVerified {
		t.Error("the user-verified flag was not recorded")
	}

	// Now sign in with it.
	_, challenge, err := BeginAuthentication(testConfig, []Credential{*credential})
	if err != nil {
		t.Fatal(err)
	}

	result, err := FinishAuthentication(testConfig, challenge, credential,
		authenticator.authenticate(t, challenge, "https://perfuse.example.org", testConfig.RPID,
			flagUserPresent|flagUserVerified))
	if err != nil {
		t.Fatalf("a genuine sign-in was refused: %v", err)
	}
	if !result.UserVerified {
		t.Error("the sign-in did not report user verification")
	}
}

// TestASignatureFromADifferentKeyIsRefused is the check everything else rests on.
func TestASignatureFromADifferentKeyIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	// A different authenticator, signing the same challenge.
	impostor := newSoftAuthenticator(t)
	impostor.credentialID = authenticator.credentialID

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = FinishAuthentication(testConfig, challenge, credential,
		impostor.authenticate(t, challenge, "https://perfuse.example.org", testConfig.RPID, flagUserPresent))
	if err == nil {
		t.Fatal("a signature from a different key was accepted")
	}
	if !errors.Is(err, ErrVerification) {
		t.Errorf("the refusal was %v", err)
	}
}

// TestAChallengeFromADifferentSignInIsRefused covers replay.
func TestAChallengeFromADifferentSignInIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	// An assertion made for one challenge, presented against another.
	_, first, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := authenticator.authenticate(t, first, "https://perfuse.example.org", testConfig.RPID, flagUserPresent)

	if _, err := FinishAuthentication(testConfig, second, credential, response); err == nil {
		t.Fatal("an assertion for one challenge verified against another")
	} else if !errors.Is(err, ErrChallenge) {
		t.Errorf("the refusal was %v, expected a challenge error", err)
	}

	// An empty challenge must be refused rather than treated as nothing to check. That reading would mean a sign-in
	// nobody challenged.
	if _, err := FinishAuthentication(testConfig, nil, credential, response); err == nil {
		t.Error("an assertion verified with no challenge at all")
	}
}

// TestAnAssertionForAnotherOriginIsRefused is the anti-phishing property.
//
// A convincing copy of the login page gets a signature bound to its own origin, which this server cannot use. The comparison
// must be exact: a suffix match accepts an attacker's subdomain and a prefix match accepts a domain that merely starts the
// same way.
func TestAnAssertionForAnotherOriginIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	hostile := []string{
		"https://evil.example.org",
		"https://evil-perfuse.example.org",         // suffix of the real one is not enough
		"https://perfuse.example.org.attacker.net", // prefix of the real one is not enough
		"http://perfuse.example.org",               // wrong scheme
		"https://perfuse.example.org:8443",         // wrong port
		"https://Perfuse.Example.Org",              // different case is a different origin
	}

	for _, origin := range hostile {
		_, challenge, err := BeginAuthentication(testConfig, nil)
		if err != nil {
			t.Fatal(err)
		}

		_, err = FinishAuthentication(testConfig, challenge, credential,
			authenticator.authenticate(t, challenge, origin, testConfig.RPID, flagUserPresent))
		if err == nil {
			t.Errorf("an assertion from %s was accepted", origin)

			continue
		}
		if !errors.Is(err, ErrOrigin) && !errors.Is(err, ErrVerification) {
			t.Errorf("%s was refused with %v", origin, err)
		}
	}
}

// TestAnAssertionForAnotherRelyingPartyIsRefused covers the RP ID hash.
//
// The authenticator signs a hash of the domain it believes it is signing for. A server that does not check it would accept a
// signature produced for an entirely different site.
func TestAnAssertionForAnotherRelyingPartyIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The client data says the right origin; the signed authenticator data says a different relying party.
	_, err = FinishAuthentication(testConfig, challenge, credential,
		authenticator.authenticate(t, challenge, "https://perfuse.example.org", "attacker.net", flagUserPresent))
	if err == nil {
		t.Fatal("an assertion signed for a different relying party was accepted")
	}
	if !errors.Is(err, ErrOrigin) {
		t.Errorf("the refusal was %v", err)
	}
}

// TestAnAssertionWithNobodyPresentIsRefused covers the presence flag.
//
// Without it an assertion could be produced by software with nobody at the keyboard, which is the whole thing a security key
// exists to prevent.
func TestAnAssertionWithNobodyPresentIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Flags with no user-present bit.
	_, err = FinishAuthentication(testConfig, challenge, credential,
		authenticator.authenticate(t, challenge, "https://perfuse.example.org", testConfig.RPID, 0))
	if err == nil {
		t.Fatal("an assertion with no user present was accepted")
	}
	if !errors.Is(err, ErrUserNotPresent) {
		t.Errorf("the refusal was %v", err)
	}
}

// TestUserVerificationIsEnforcedWhenRequired covers the passwordless case.
//
// Presence means a key was touched. Verification means a fingerprint, face or PIN was checked. For a passkey used on its own
// the difference is whether a stolen key is enough to sign in.
func TestUserVerificationIsEnforcedWhenRequired(t *testing.T) {
	strict := testConfig
	strict.RequireUserVerification = true

	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	_, challenge, err := BeginAuthentication(strict, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Presence only.
	_, err = FinishAuthentication(strict, challenge, credential,
		authenticator.authenticate(t, challenge, "https://perfuse.example.org", strict.RPID, flagUserPresent))
	if err == nil {
		t.Fatal("a presence-only assertion satisfied a user-verification requirement")
	}
	// The message has to explain what to do, because somebody is standing at a login page.
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("the refusal does not say what is needed: %v", err)
	}

	// And with verification it works.
	_, challenge, err = BeginAuthentication(strict, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FinishAuthentication(strict, challenge, credential,
		authenticator.authenticate(t, challenge, "https://perfuse.example.org", strict.RPID,
			flagUserPresent|flagUserVerified)); err != nil {
		t.Errorf("a verified assertion was refused: %v", err)
	}
}

// TestARegistrationResponseCannotBeReplayedAsASignIn covers the type field.
//
// Both are signed structures over a challenge, and only the type distinguishes their purpose. Accepting either for either
// would let a registration be replayed as a sign-in.
func TestARegistrationResponseCannotBeReplayedAsASignIn(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A sign-in response whose client data claims to be a registration.
	clientData := clientDataFor("webauthn.create", challenge, "https://perfuse.example.org", false)
	authData := authenticator.authData(testConfig.RPID, flagUserPresent, false)

	digest := sha256.Sum256(clientData)
	message := append(append([]byte{}, authData...), digest[:]...)
	signed := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, authenticator.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}

	var response AuthenticationResponse
	response.Type = "public-key"
	response.RawID = base64.RawURLEncoding.EncodeToString(authenticator.credentialID)
	response.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(clientData)
	response.Response.AuthenticatorData = base64.RawURLEncoding.EncodeToString(authData)
	response.Response.Signature = base64.RawURLEncoding.EncodeToString(signature)

	// The signature is genuine; only the type is wrong.
	if _, err := FinishAuthentication(testConfig, challenge, credential, response); err == nil {
		t.Fatal("a response claiming webauthn.create was accepted as a sign-in")
	}
}

// TestACrossOriginRequestIsRefused covers clickjacking.
func TestACrossOriginRequestIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}

	clientData := clientDataFor("webauthn.get", challenge, "https://perfuse.example.org", true)
	authData := authenticator.authData(testConfig.RPID, flagUserPresent, false)

	digest := sha256.Sum256(clientData)
	message := append(append([]byte{}, authData...), digest[:]...)
	signed := sha256.Sum256(message)
	signature, _ := ecdsa.SignASN1(rand.Reader, authenticator.key, signed[:])

	var response AuthenticationResponse
	response.Type = "public-key"
	response.RawID = base64.RawURLEncoding.EncodeToString(authenticator.credentialID)
	response.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(clientData)
	response.Response.AuthenticatorData = base64.RawURLEncoding.EncodeToString(authData)
	response.Response.Signature = base64.RawURLEncoding.EncodeToString(signature)

	if _, err := FinishAuthentication(testConfig, challenge, credential, response); err == nil {
		t.Error("an assertion from a cross-origin frame was accepted")
	}
}

// TestACloneIsFlaggedButNotRefused covers the sign counter.
//
// A counter going backwards means two authenticators hold the same private key. Reported rather than refused, because many
// authenticators legitimately keep it at zero and refusing every non-advance would lock out working hardware.
func TestACloneIsFlaggedButNotRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	authenticator.signCount = 10

	credential, _ := registerAndStore(t, authenticator)
	credential.SignCount = 10

	// A sign-in reporting a lower counter.
	authenticator.signCount = 5

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := FinishAuthentication(testConfig, challenge, credential,
		authenticator.authenticate(t, challenge, "https://perfuse.example.org", testConfig.RPID, flagUserPresent))
	if err != nil {
		t.Fatalf("a backwards counter refused the sign-in outright: %v", err)
	}
	if !result.CloneWarning {
		t.Error("a backwards sign counter was not flagged")
	}

	// A zero counter on both sides must not be flagged, because that is the common legitimate case.
	authenticator.signCount = 0
	credential.SignCount = 0

	_, challenge, err = BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = FinishAuthentication(testConfig, challenge, credential,
		authenticator.authenticate(t, challenge, "https://perfuse.example.org", testConfig.RPID, flagUserPresent))
	if err != nil {
		t.Fatal(err)
	}
	if result.CloneWarning {
		t.Error("an authenticator that keeps its counter at zero was flagged as a clone")
	}
}

// TestATamperedSignatureIsRefused covers each byte of the signature mattering.
func TestATamperedSignatureIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := authenticator.authenticate(t, challenge, "https://perfuse.example.org", testConfig.RPID,
		flagUserPresent)

	original, err := base64.RawURLEncoding.DecodeString(response.Response.Signature)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a bit in the middle, which lands inside a signature component.
	tampered := append([]byte{}, original...)
	tampered[len(tampered)/2] ^= 0x01
	response.Response.Signature = base64.RawURLEncoding.EncodeToString(tampered)

	if _, err := FinishAuthentication(testConfig, challenge, credential, response); err == nil {
		t.Error("a tampered signature was accepted")
	}

	// Trailing bytes after a valid signature must be refused, because a permissive parser accepting several encodings of
	// one signature is signature malleability.
	response.Response.Signature = base64.RawURLEncoding.EncodeToString(append(append([]byte{}, original...), 0x00))
	if _, err := FinishAuthentication(testConfig, challenge, credential, response); err == nil {
		t.Error("a signature with a trailing byte was accepted")
	}
}

// TestATamperedAuthenticatorDataIsRefused covers what the signature covers.
func TestATamperedAuthenticatorDataIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	credential, _ := registerAndStore(t, authenticator)

	_, challenge, err := BeginAuthentication(testConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := authenticator.authenticate(t, challenge, "https://perfuse.example.org", testConfig.RPID,
		flagUserPresent)

	authData, err := base64.RawURLEncoding.DecodeString(response.Response.AuthenticatorData)
	if err != nil {
		t.Fatal(err)
	}

	// Set the user-verified flag that the authenticator did not set. Somebody trying to satisfy a verification
	// requirement they cannot meet would do exactly this.
	authData[32] |= flagUserVerified
	response.Response.AuthenticatorData = base64.RawURLEncoding.EncodeToString(authData)

	if _, err := FinishAuthentication(testConfig, challenge, credential, response); err == nil {
		t.Error("authenticator data with a flag added after signing was accepted")
	}
}

// TestAnUnsupportedAlgorithmIsRefusedAtRegistration covers the front door for keys.
//
// Refused at registration rather than discovered later. A credential stored with an algorithm nothing can verify refuses its
// owner at every sign-in with no explanation.
func TestAnUnsupportedAlgorithmIsRefusedAtRegistration(t *testing.T) {
	// A COSE key claiming an algorithm nothing here implements.
	var key []byte
	key = append(key, 0xa3)
	key = append(key, 0x01, 0x02)             // kty: EC2
	key = append(key, 0x03, 0x39, 0x01, 0x00) // alg: -257... actually a large negative
	key = append(key, 0x20, 0x01)             // crv: P-256

	if _, err := ParseCOSEKey(key); err == nil {
		t.Error("a key with an unimplemented algorithm was accepted")
	}
}

// TestAKeyWhoseCurveDisagreesWithItsAlgorithmIsRefused covers a constructed inconsistency.
func TestAKeyWhoseCurveDisagreesWithItsAlgorithmIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)

	// A real P-256 key, relabelled as ES512.
	key := authenticator.coseKey()
	for i := 0; i+1 < len(key); i++ {
		if key[i] == 0x03 && key[i+1] == 0x26 { // alg: -7
			key[i+1] = 0x38 // start of a one-byte negative
			key = append(key[:i+2], append([]byte{0x23}, key[i+2:]...)...)

			break
		}
	}

	if _, err := ParseCOSEKey(key); err == nil {
		t.Error("a P-256 key claiming ES512 was accepted")
	}
}

// TestAnOffCurvePointIsRefused covers point validation.
//
// An off-curve point can leak information about a private key in some implementations, and a point at infinity would verify
// anything.
func TestAnOffCurvePointIsRefused(t *testing.T) {
	authenticator := newSoftAuthenticator(t)
	key := authenticator.coseKey()

	// Corrupt the last byte of the y coordinate, which moves the point off the curve.
	key[len(key)-1] ^= 0xff

	if _, err := ParseCOSEKey(key); err == nil {
		t.Error("a point that is not on the curve was accepted as a public key")
	} else if !errors.Is(err, ErrBadKey) {
		t.Errorf("the refusal was %v", err)
	}
}

// TestAWeakRSAKeyIsRefused covers the modulus floor and the exponent.
func TestAWeakRSAKeyIsRefused(t *testing.T) {
	// A 1024-bit key, which is below every current guideline.
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}

	key := rsaCOSEKey(weak.N.Bytes(), []byte{0x01, 0x00, 0x01})
	if _, err := ParseCOSEKey(key); err == nil {
		t.Error("a 1024-bit RSA key was accepted")
	}

	// A proper size but an exponent of 1, which makes a signature equal its own plaintext and verifies anything.
	strong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key = rsaCOSEKey(strong.N.Bytes(), []byte{0x01})
	if _, err := ParseCOSEKey(key); err == nil {
		t.Error("an RSA exponent of 1 was accepted, which verifies any signature")
	}

	// And an even exponent, which is not a valid exponent at all.
	key = rsaCOSEKey(strong.N.Bytes(), []byte{0x04})
	if _, err := ParseCOSEKey(key); err == nil {
		t.Error("an even RSA exponent was accepted")
	}

	// The genuine article must still work, or this test is only proving that RSA is refused.
	key = rsaCOSEKey(strong.N.Bytes(), []byte{0x01, 0x00, 0x01})
	parsed, err := ParseCOSEKey(key)
	if err != nil {
		t.Fatalf("a 2048-bit RSA key with exponent 65537 was refused: %v", err)
	}
	if parsed.Algorithm != AlgRS256 {
		t.Errorf("algorithm is %s", parsed.Algorithm.Name())
	}
}

// rsaCOSEKey builds a COSE RSA key.
func rsaCOSEKey(modulus, exponent []byte) []byte {
	var b []byte
	b = append(b, 0xa4)
	b = append(b, 0x01, 0x03)             // kty: RSA
	b = append(b, 0x03, 0x39, 0x01, 0x00) // alg: -257
	b = append(b, 0x20)                   // -1: modulus
	b = append(b, encodeByteString(modulus)...)
	b = append(b, 0x21) // -2: exponent
	b = append(b, encodeByteString(exponent)...)

	return b
}

// TestAnEd25519KeyWorks covers the other common algorithm.
func TestAnEd25519KeyWorks(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var key []byte
	key = append(key, 0xa4)
	key = append(key, 0x01, 0x01) // kty: OKP
	key = append(key, 0x03, 0x27) // alg: -8
	key = append(key, 0x20, 0x06) // crv: Ed25519
	key = append(key, 0x21)
	key = append(key, encodeByteString(public)...)

	parsed, err := ParseCOSEKey(key)
	if err != nil {
		t.Fatalf("an Ed25519 key was refused: %v", err)
	}
	if parsed.Algorithm != AlgEdDSA {
		t.Errorf("algorithm is %s", parsed.Algorithm.Name())
	}

	// A wrong-length public value must be refused rather than padded into a different key.
	key = key[:len(key)-len(public)-2]
	key = append(key, encodeByteString(public[:16])...)
	if _, err := ParseCOSEKey(key); err == nil {
		t.Error("a short Ed25519 key was accepted")
	}
}

// TestAMisconfiguredRelyingPartyIsRefused covers the commonest deployment mistake.
//
// An RP ID given as a URL produces credentials that never work, and the failure is opaque. Refused with an explanation
// instead.
func TestAMisconfiguredRelyingPartyIsRefused(t *testing.T) {
	bad := []Config{
		{RPID: "", Origins: []string{"https://x.example.org"}},
		{RPID: "https://perfuse.example.org", Origins: []string{"https://perfuse.example.org"}},
		{RPID: "perfuse.example.org:8443", Origins: []string{"https://perfuse.example.org"}},
		{RPID: "perfuse.example.org/login", Origins: []string{"https://perfuse.example.org"}},
		{RPID: "perfuse.example.org", Origins: nil},
		{RPID: "perfuse.example.org", Origins: []string{"perfuse.example.org"}},
	}

	for _, config := range bad {
		if err := config.Valid(); err == nil {
			t.Errorf("the configuration %+v was accepted", config)
		}
	}

	good := Config{RPID: "perfuse.example.org", Origins: []string{"https://perfuse.example.org"}}
	if err := good.Valid(); err != nil {
		t.Errorf("a correct configuration was refused: %v", err)
	}
}

// TestChallengesAreRandomAndLongEnough covers the one thing preventing replay.
func TestChallengesAreRandomAndLongEnough(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 200; i++ {
		challenge, err := NewChallenge()
		if err != nil {
			t.Fatal(err)
		}
		if len(challenge) != ChallengeLength {
			t.Fatalf("a challenge is %d bytes, expected %d", len(challenge), ChallengeLength)
		}
		if ChallengeLength < 16 {
			t.Fatal("the specification requires at least 16 bytes")
		}

		encoded := base64.RawURLEncoding.EncodeToString(challenge)
		if seen[encoded] {
			t.Fatal("a challenge repeated, which makes an assertion replayable")
		}
		seen[encoded] = true
	}
}
