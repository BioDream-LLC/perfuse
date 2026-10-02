// Package webauthn verifies passkeys.
//
// A passkey is a key pair the authenticator holds - Touch ID, Windows Hello, a YubiKey, a phone - where the private half
// never leaves the device. Signing in means the server sends a random challenge, the authenticator signs it after checking
// the user is present, and the server verifies the signature against the public key it stored at registration.
//
// The property that matters is not that it is convenient. It is that there is nothing to phish. A password can be typed into
// a convincing copy of a login page; a passkey signature is bound to the origin it was created for, so the copy gets a
// signature its server cannot use. For an integration engine holding a hospital's interfaces, that is the difference worth
// having.
//
// # What this implementation refuses to do
//
// The specification is permissive in places where being permissive is how implementations get broken. This one is not:
//
//   - The signing algorithm is fixed at registration and stored with the key. Nothing at sign-in reads an algorithm from
//     the client, because that is algorithm confusion and there is no function here that could do it.
//   - The origin is compared exactly, never by prefix or suffix. A check that accepts anything ending in the right domain
//     accepts an attacker's subdomain.
//   - A challenge is single-use and expires. It is deleted when consumed, so a captured assertion cannot be replayed.
//   - Attestation is not trusted for anything. The attestation statement is parsed to extract the credential and then its
//     signature is ignored, because verifying it properly needs a maintained root store and pretending to verify it is
//     worse than not claiming to.
//   - The sign count is checked when the authenticator provides one, and its absence is not treated as suspicious. Many
//     authenticators legitimately leave it at zero.
package webauthn

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Errors from verification.
//
// Deliberately coarse where they reach a user. A sign-in failure should not explain which check failed, because that turns
// the endpoint into an oracle for constructing a working assertion. The detail goes in the server's log instead.
var (
	// ErrVerification means an assertion or attestation did not verify. The reason is logged, not returned.
	ErrVerification = errors.New("the passkey could not be verified")

	// ErrChallenge means the challenge was wrong, missing or already used.
	ErrChallenge = errors.New("the challenge is not valid")

	// ErrOrigin means the assertion was created for a different site.
	ErrOrigin = errors.New("the assertion was created for a different origin")

	// ErrUserNotPresent means the authenticator did not confirm a person was there.
	ErrUserNotPresent = errors.New("the authenticator did not confirm user presence")

	// ErrSignCount means the sign count went backwards, which suggests a cloned authenticator.
	ErrSignCount = errors.New("the authenticator's sign count went backwards")
)

// ChallengeLength is how many random bytes a challenge carries.
//
// The specification requires at least 16. Thirty-two is used because the cost is nothing and it removes any argument about
// margin - a challenge is the only thing standing between a captured signature and a replay.
const ChallengeLength = 32

// ChallengeLifetime is how long a challenge remains usable.
//
// Two minutes. Long enough for somebody to find their security key or respond to a biometric prompt, short enough that a
// challenge captured from a network trace is useless by the time it is extracted. A challenge that never expired would let an
// assertion be replayed indefinitely.
const ChallengeLifetime = 2 * time.Minute

// Config describes the site passkeys are being registered for.
type Config struct {
	// RPID is the relying party identifier, which is a bare domain: "perfuse.example.org", never a URL and never a port.
	//
	// This is what binds a credential to a site. An authenticator will refuse to produce an assertion for an RP ID that
	// does not match the page's own domain, which is what makes a passkey unphishable - so getting it wrong does not
	// weaken security, it stops sign-in working entirely.
	RPID string

	// RPName is shown to the user by the authenticator when they register.
	RPName string

	// Origins are the exact origins accepted, scheme and port included.
	//
	// A list rather than one, because a real deployment is reached at more than one address - a hostname and its
	// alias, or an internal and external name. Compared exactly: a check that accepted anything ending in the right
	// domain would accept an attacker's subdomain, and that is a real class of bug rather than a hypothetical.
	Origins []string

	// RequireUserVerification demands that the authenticator verified the person, not merely that somebody touched it.
	//
	// The difference between "a key was plugged in and tapped" and "a fingerprint or PIN was checked". Required for a
	// passkey used on its own; optional for one used as a second factor alongside a password, where the password is the
	// thing being verified.
	RequireUserVerification bool

	// Attestation says what to require of the authenticator itself, when an operator has configured anything.
	//
	// The zero value verifies nothing and claims nothing, which is the right default: attestation answers "which
	// device", never "is this legitimate", and the origin binding does the security work either way.
	Attestation AttestationPolicy
}

// Valid checks the configuration, and refuses rather than guessing.
func (c Config) Valid() error {
	if c.RPID == "" {
		return errors.New("an RP ID is required, and it is a bare domain such as perfuse.example.org")
	}
	// A URL here is the commonest mistake and produces credentials that never work, so it is caught with an explanation
	// rather than accepted.
	for _, wrong := range []string{"http://", "https://", "/", ":"} {
		if contains(c.RPID, wrong) {
			return fmt.Errorf("the RP ID %q must be a bare domain: no scheme, no port and no path", c.RPID)
		}
	}
	if len(c.Origins) == 0 {
		return errors.New("at least one origin is required, with its scheme and port")
	}
	for _, origin := range c.Origins {
		if !contains(origin, "://") {
			return fmt.Errorf("the origin %q needs a scheme, for example https://%s", origin, c.RPID)
		}
	}

	return nil
}

// contains is strings.Contains, kept local so this file's imports stay to what it verifies with.
func contains(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}

	return false
}

// NewChallenge makes a challenge.
//
// Fails rather than falling back when randomness is unavailable. A predictable challenge means an assertion can be prepared
// in advance, which removes the entire protection.
func NewChallenge() ([]byte, error) {
	challenge := make([]byte, ChallengeLength)
	if _, err := rand.Read(challenge); err != nil {
		return nil, fmt.Errorf("no randomness available, so a challenge cannot be issued safely: %w", err)
	}

	return challenge, nil
}

// clientData is the parsed clientDataJSON.
type clientData struct {
	// Type is "webauthn.create" for registration and "webauthn.get" for authentication.
	//
	// Checked, because accepting either for either would let a registration response be replayed as a sign-in. That
	// sounds theoretical and is not: both are signed structures over a challenge, and only this field distinguishes
	// their purpose.
	Type string `json:"type"`

	// Challenge is base64url without padding.
	Challenge string `json:"challenge"`

	// Origin is the exact origin of the page that made the request.
	Origin string `json:"origin"`

	// CrossOrigin says whether the call was made from a cross-origin iframe.
	//
	// Refused when true. A passkey prompt inside an iframe controlled by somebody else is a clickjacking primitive, and
	// nothing in Perfuse embeds its own login page.
	CrossOrigin bool `json:"crossOrigin"`
}

// verifyClientData checks the parts of a response that the browser assembles.
func verifyClientData(raw []byte, expectedType string, challenge []byte, config Config) error {
	var data clientData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("%w: the client data is not JSON", ErrVerification)
	}

	if data.Type != expectedType {
		// A registration response replayed as a sign-in, or the reverse.
		return fmt.Errorf("%w: the client data says %q where %q was expected",
			ErrVerification, data.Type, expectedType)
	}

	// The challenge, compared in constant time.
	//
	// Constant time because a comparison that stops at the first wrong byte lets a challenge be discovered one byte at a
	// time by an attacker who can make repeated attempts - and a known challenge is a replayable assertion.
	presented, err := base64.RawURLEncoding.DecodeString(data.Challenge)
	if err != nil {
		return fmt.Errorf("%w: the challenge is not base64url", ErrChallenge)
	}
	if subtle.ConstantTimeCompare(presented, challenge) != 1 {
		return fmt.Errorf("%w: the challenge does not match the one issued", ErrChallenge)
	}

	// The origin, compared exactly against a list.
	//
	// Never a prefix or suffix match. A check accepting anything ending in "perfuse.example.org" accepts
	// "evil-perfuse.example.org", and one accepting a prefix accepts "https://perfuse.example.org.attacker.net".
	matched := false
	for _, allowed := range config.Origins {
		if subtle.ConstantTimeCompare([]byte(data.Origin), []byte(allowed)) == 1 {
			matched = true

			break
		}
	}
	if !matched {
		return fmt.Errorf("%w: %q is not one of the configured origins", ErrOrigin, data.Origin)
	}

	if data.CrossOrigin {
		// A passkey prompt inside somebody else's iframe is a clickjacking primitive, and nothing here embeds its own
		// login page.
		return fmt.Errorf("%w: the request was made from a cross-origin frame", ErrVerification)
	}

	return nil
}

// authenticatorData is the parsed authenticator data structure.
type authenticatorData struct {
	// RPIDHash is SHA-256 of the RP ID the authenticator believes it is signing for.
	RPIDHash []byte

	// Flags is the raw flag byte.
	Flags byte

	// SignCount is the authenticator's counter.
	SignCount uint32

	// AAGUID identifies the authenticator model, present only on registration.
	//
	// Recorded and shown, because it is how somebody recognises which of their keys a credential belongs to - "YubiKey
	// 5" rather than an opaque identifier. Never used for an access decision, since it is self-asserted without
	// attestation verification.
	AAGUID []byte

	// CredentialID is the credential's identifier, present only on registration.
	CredentialID []byte

	// CredentialPublicKey is the COSE key, present only on registration.
	CredentialPublicKey []byte
}

// Authenticator data flags.
const (
	flagUserPresent  = 0x01
	flagUserVerified = 0x04
	flagAttestedData = 0x40
	flagExtensions   = 0x80
)

// UserPresent reports whether somebody physically interacted with the authenticator.
func (a authenticatorData) UserPresent() bool { return a.Flags&flagUserPresent != 0 }

// UserVerified reports whether the authenticator checked who they were - a fingerprint, a face, a PIN.
func (a authenticatorData) UserVerified() bool { return a.Flags&flagUserVerified != 0 }

// parseAuthenticatorData reads the authenticator data.
//
// Every length is checked against the bytes remaining before use. This structure comes from the client, so a claimed
// credential length of 65535 must not read past the buffer or allocate on trust.
func parseAuthenticatorData(data []byte) (authenticatorData, error) {
	// 32 bytes of RP ID hash, one flag byte, four bytes of counter.
	const minimum = 37
	if len(data) < minimum {
		return authenticatorData{}, fmt.Errorf("%w: authenticator data is %d bytes, minimum %d",
			ErrVerification, len(data), minimum)
	}

	out := authenticatorData{
		RPIDHash:  data[:32],
		Flags:     data[32],
		SignCount: binary.BigEndian.Uint32(data[33:37]),
	}

	rest := data[minimum:]

	if out.Flags&flagAttestedData != 0 {
		// 16 bytes of AAGUID, two bytes of credential ID length, the credential ID, then the COSE key.
		if len(rest) < 18 {
			return authenticatorData{}, fmt.Errorf("%w: attested credential data is truncated", ErrVerification)
		}
		out.AAGUID = rest[:16]
		idLength := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]

		// A credential identifier is at most 1023 bytes by the specification. Checked, because a larger claim is either
		// a broken authenticator or an attempt to read past the buffer.
		if idLength > 1023 {
			return authenticatorData{}, fmt.Errorf("%w: the credential identifier claims %d bytes, and 1023 is the "+
				"maximum", ErrVerification, idLength)
		}
		if idLength > len(rest) {
			return authenticatorData{}, fmt.Errorf("%w: the credential identifier claims %d bytes and only %d remain",
				ErrVerification, idLength, len(rest))
		}
		out.CredentialID = rest[:idLength]
		rest = rest[idLength:]

		// The remainder is the COSE key, followed by extensions when the flag says so. The key is decoded to find where
		// it ends rather than assumed to run to the end of the buffer, because trailing extension data would otherwise
		// be handed to the key parser.
		if out.Flags&flagExtensions != 0 {
			consumed, err := cborItemLength(rest)
			if err != nil {
				return authenticatorData{}, fmt.Errorf("%w: the credential key could not be measured: %w",
					ErrVerification, err)
			}
			out.CredentialPublicKey = rest[:consumed]
		} else {
			out.CredentialPublicKey = rest
		}
	}

	return out, nil
}

// cborItemLength reports how many bytes the first CBOR item occupies.
//
// Needed because a credential key may be followed by extension data, and handing the combined bytes to the key parser would
// fail on the trailing-bytes check - refusing a perfectly good credential from an authenticator that happens to send
// extensions.
func cborItemLength(data []byte) (int, error) {
	_, rest, err := decodeCBOR(data, 0)
	if err != nil {
		return 0, err
	}

	return len(data) - len(rest), nil
}

// verifyAuthenticatorData checks the parts the authenticator signs.
func verifyAuthenticatorData(data authenticatorData, config Config) error {
	// The RP ID hash. This is what binds a credential to a site.
	expected := sha256.Sum256([]byte(config.RPID))
	if subtle.ConstantTimeCompare(data.RPIDHash, expected[:]) != 1 {
		return fmt.Errorf("%w: the assertion was signed for a different relying party", ErrOrigin)
	}

	// User presence. Without this an assertion could be produced by software with nobody at the keyboard, which is the
	// whole thing a security key exists to prevent.
	if !data.UserPresent() {
		return ErrUserNotPresent
	}

	if config.RequireUserVerification && !data.UserVerified() {
		// A key was touched but nobody proved who they were. Acceptable as a second factor beside a password; not
		// acceptable on its own, which is the case this configuration covers.
		return fmt.Errorf("%w: this account needs the authenticator to verify who you are - a fingerprint, face or "+
			"PIN - and it only confirmed that somebody was there", ErrVerification)
	}

	return nil
}

// signedData assembles what an authenticator signs: the authenticator data followed by the hash of the client data.
func signedData(authData, clientDataJSON []byte) []byte {
	digest := sha256.Sum256(clientDataJSON)

	out := make([]byte, 0, len(authData)+len(digest))
	out = append(out, authData...)
	out = append(out, digest[:]...)

	return out
}

// verifySignature checks a signature with the credential's own stored algorithm.
//
// The algorithm comes from the stored credential and never from the assertion. That is the single most important line in this
// package: reading it from the assertion would let a caller nominate one, which is algorithm confusion.
func verifySignature(key *CredentialKey, message, signature []byte) error {
	hashAlgorithm, ok := key.Algorithm.hash()
	if !ok {
		return fmt.Errorf("%w: no digest for %s", ErrVerification, key.Algorithm.Name())
	}

	switch public := key.Public.(type) {
	case *ecdsa.PublicKey:
		digest := digestOf(hashAlgorithm, message)

		// ECDSA signatures arrive DER-encoded here. Parsed strictly: asn1.Unmarshal refuses trailing data, and a
		// permissive parser would accept several encodings of one signature, which is signature malleability.
		var parsed struct {
			R, S *big.Int
		}
		remaining, err := asn1.Unmarshal(signature, &parsed)
		if err != nil {
			return fmt.Errorf("%w: the signature is not a valid DER sequence", ErrVerification)
		}
		if len(remaining) != 0 {
			return fmt.Errorf("%w: %d trailing byte(s) after the signature", ErrVerification, len(remaining))
		}
		// Zero or negative values are not valid signature components and some libraries have accepted them.
		if parsed.R == nil || parsed.S == nil || parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 {
			return fmt.Errorf("%w: the signature components are not positive integers", ErrVerification)
		}

		if !ecdsa.Verify(public, digest, parsed.R, parsed.S) {
			return ErrVerification
		}

		return nil

	case ed25519.PublicKey:
		// Ed25519 signs the message itself rather than a digest.
		if !ed25519.Verify(public, message, signature) {
			return ErrVerification
		}

		return nil

	case *rsa.PublicKey:
		digest := digestOf(hashAlgorithm, message)
		if err := rsa.VerifyPKCS1v15(public, hashAlgorithm, digest, signature); err != nil {
			return ErrVerification
		}

		return nil

	default:
		return fmt.Errorf("%w: no verifier for this key type", ErrVerification)
	}
}

// digestOf hashes a message with the given algorithm.
func digestOf(algorithm crypto.Hash, message []byte) []byte {
	hasher := algorithm.New()
	hasher.Write(message)

	return hasher.Sum(nil)
}
