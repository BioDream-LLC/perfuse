package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Password handling.
//
// PBKDF2-HMAC-SHA256 from the standard library, at the iteration count OWASP
// recommends for that construction. Argon2id would be preferable, but it is not
// in the standard library and one dependency for password hashing is a poor
// trade when PBKDF2 at this cost is still a NIST-approved choice.
//
// The stored form records its own parameters, so the cost can be raised later
// without invalidating existing passwords: an old hash keeps verifying with the
// iteration count it was created with.

const (
	// DefaultIterations is the OWASP recommendation for PBKDF2-HMAC-SHA256.
	DefaultIterations = 600_000
	saltLength        = 16
	keyLength         = 32

	// MinPasswordLength is enforced on every password. Length beats composition
	// rules, which mostly teach people to append an exclamation mark.
	MinPasswordLength = 12
)

// HashIterations is the cost used for new password hashes.
//
// It is a variable rather than a constant so that it can be raised on capable
// hardware, and lowered in tests where a few hundred milliseconds per hash turns
// a fast suite into a slow one. Existing hashes are unaffected either way: each
// one records the cost it was created with, and verifying uses that.
//
// Lowering it in production weakens every password created afterwards.
var HashIterations = DefaultIterations

// ErrPasswordTooShort is returned when a password does not meet the minimum.
var ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)

// HashPassword produces the stored representation of a password.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	key, err := pbkdf2.Key(sha256.New, password, salt, HashIterations, keyLength)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		HashIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a stored hash.
//
// The comparison is constant time. It also reports whether the hash was made
// with outdated parameters, so a caller can transparently upgrade it on a
// successful login.
func VerifyPassword(stored, password string) (ok bool, needsRehash bool) {
	iterations, salt, want, err := parseHash(stored)
	if err != nil {
		return false, false
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false, false
	}

	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false
	}
	return true, iterations < HashIterations
}

func parseHash(stored string) (iterations int, salt, key []byte, err error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return 0, nil, nil, errors.New("store: unrecognised password hash format")
	}

	iterations, err = strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return 0, nil, nil, errors.New("store: bad iteration count in password hash")
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[2]); err != nil {
		return 0, nil, nil, errors.New("store: bad salt in password hash")
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[3]); err != nil {
		return 0, nil, nil, errors.New("store: bad key in password hash")
	}
	return iterations, salt, key, nil
}

// Session tokens.
//
// A token is 32 random bytes. Only its SHA-256 hash is stored, so a leaked copy
// of the database does not hand over live sessions: an attacker would have to
// reverse the hash to produce a usable cookie. Session lookup is by hash, which
// is why no constant-time comparison is needed here — the database does an exact
// match on a value the attacker cannot construct from the stored form.

const sessionTokenBytes = 32

// newSessionToken returns a token to give the client and the hash to store.
func newSessionToken() (token, hash string, err error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// GeneratePassword returns a random password for first-run admin creation.
//
// A generated password printed once is safer than a documented default, which is
// what actually gets left in place on an internet-facing box.
func GeneratePassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	const length = 20

	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(rand.Reader, bigLen(len(alphabet)))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}
