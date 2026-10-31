// Package pgp encrypts, signs, decrypts and verifies OpenPGP messages (RFC 9580, and RFC 4880 for older peers), for
// payers, clearinghouses and state agencies that exchange files over SFTP or email under PGP.
//
// The cryptography is github.com/ProtonMail/go-crypto, the maintained fork of golang.org/x/crypto/openpgp; nothing here
// implements a primitive. This package only loads keys and decides what a message must carry.
package pgp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// ReadKeys reads armored or binary keys from a file: a public key ring, a secret key, or several concatenated.
func ReadKeys(path string) (openpgp.EntityList, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseKeys(b)
}

// ParseKeys reads armored or binary keys.
func ParseKeys(b []byte) (openpgp.EntityList, error) {
	trimmed := bytes.TrimSpace(b)
	var list openpgp.EntityList
	var err error
	if bytes.HasPrefix(trimmed, []byte("-----BEGIN PGP")) {
		list, err = openpgp.ReadArmoredKeyRing(bytes.NewReader(trimmed))
	} else {
		list, err = openpgp.ReadKeyRing(bytes.NewReader(b))
	}
	if err != nil {
		return nil, fmt.Errorf("not an OpenPGP key: %w", err)
	}
	if len(list) == 0 {
		return nil, errors.New("no OpenPGP key in it")
	}
	return list, nil
}

// Unlock decrypts every secret key and subkey in list with passphrase. Keys that are not protected are left as they are.
func Unlock(list openpgp.EntityList, passphrase []byte) error {
	for _, e := range list {
		if e.PrivateKey == nil {
			return fmt.Errorf("key %s has no secret part: it is a public key", KeyID(e))
		}
		if e.PrivateKey.Encrypted {
			if len(passphrase) == 0 {
				return fmt.Errorf("key %s is protected by a passphrase and none was given", KeyID(e))
			}
			if err := e.PrivateKey.Decrypt(passphrase); err != nil {
				return fmt.Errorf("key %s: the passphrase is wrong", KeyID(e))
			}
		}
		for _, sub := range e.Subkeys {
			if sub.PrivateKey != nil && sub.PrivateKey.Encrypted {
				if err := sub.PrivateKey.Decrypt(passphrase); err != nil {
					return fmt.Errorf("a subkey of %s: the passphrase is wrong", KeyID(e))
				}
			}
		}
	}
	return nil
}

// KeyID is a key's fingerprint in upper-case hex, as gpg prints it.
func KeyID(e *openpgp.Entity) string {
	if e == nil || e.PrimaryKey == nil {
		return "(none)"
	}
	return strings.ToUpper(fmt.Sprintf("%x", e.PrimaryKey.Fingerprint))
}

// Describe names a key by its first user id and fingerprint.
func Describe(e *openpgp.Entity) string {
	for name := range e.Identities {
		return name + " " + KeyID(e)
	}
	return KeyID(e)
}

// SealOptions says how to protect an outgoing message.
type SealOptions struct {
	// To are the recipients' public keys; the message is readable by any of them.
	To openpgp.EntityList
	// Signer, when set, signs the message (an unlocked secret key).
	Signer *openpgp.Entity
	// Armor writes ASCII armor ("-----BEGIN PGP MESSAGE-----") rather than binary.
	Armor bool
	// FileName is the literal data packet's file name, which some receivers use to name what they decrypt.
	FileName string
	// Compress compresses before encrypting (ZLIB), as gpg does by default.
	Compress bool
}

// config returns the defaults Perfuse writes with: AES-256, SHA-256, and the profile older receivers read. go-crypto writes
// RFC 4880 packets unless told to write version 6 keys, which none of ours are, so a payer's gpg 2.2 can read the result.
func config(compress bool) *packet.Config {
	c := &packet.Config{DefaultCipher: packet.CipherAES256, DefaultHash: 0}
	if compress {
		c.DefaultCompressionAlgo = packet.CompressionZLIB
		c.CompressionConfig = &packet.CompressionConfig{Level: 6}
	}
	return c
}

// Seal encrypts (and optionally signs) data. With no recipients and a signer it writes a signed message only.
func Seal(data []byte, o SealOptions) ([]byte, error) {
	if len(o.To) == 0 && o.Signer == nil {
		return nil, errors.New("nothing to do: no recipient to encrypt to and no key to sign with")
	}
	var out bytes.Buffer
	var w io.Writer = &out
	var armored io.WriteCloser
	if o.Armor {
		var err error
		armored, err = armor.Encode(&out, "PGP MESSAGE", nil)
		if err != nil {
			return nil, err
		}
		w = armored
	}
	hints := &openpgp.FileHints{IsBinary: true, FileName: o.FileName, ModTime: time.Now()}
	cfg := config(o.Compress)
	var plain io.WriteCloser
	var err error
	if len(o.To) > 0 {
		plain, err = openpgp.Encrypt(w, o.To, o.Signer, hints, cfg)
	} else {
		plain, err = openpgp.Sign(w, o.Signer, hints, cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("pgp: %w", err)
	}
	if _, err := plain.Write(data); err != nil {
		return nil, err
	}
	if err := plain.Close(); err != nil {
		return nil, err
	}
	if armored != nil {
		if err := armored.Close(); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// OpenOptions says what an incoming message needs.
type OpenOptions struct {
	// Keys are our unlocked secret keys, for decrypting.
	Keys openpgp.EntityList
	// Verify are the senders' public keys a signature is checked against.
	Verify openpgp.EntityList
	// RequireSignature refuses a message not signed by one of Verify.
	RequireSignature bool
	// AllowUnencrypted accepts a message that is signed but not encrypted.
	AllowUnencrypted bool
}

// Opened is what an incoming message held.
type Opened struct {
	Data      []byte
	FileName  string
	Encrypted bool
	// SignedBy is the verified signer, or "" when the message was not signed by a known key.
	SignedBy string
}

// IsPGP reports whether data looks like an OpenPGP message, armored or binary.
func IsPGP(data []byte) bool {
	t := bytes.TrimSpace(data)
	if bytes.HasPrefix(t, []byte("-----BEGIN PGP MESSAGE-----")) {
		return true
	}
	if len(data) == 0 || data[0]&0x80 == 0 {
		return false
	}
	// The first packet of a message: a public-key or symmetric session key, a one-pass signature, a signature,
	// compressed or literal data.
	var tag byte
	if data[0]&0x40 != 0 {
		tag = data[0] & 0x3f
	} else {
		tag = (data[0] >> 2) & 0x0f
	}
	switch tag {
	case 1, 2, 3, 4, 8, 11:
		return true
	}
	return false
}

// Open decrypts and verifies a message.
func Open(data []byte, o OpenOptions) (*Opened, error) {
	var r io.Reader = bytes.NewReader(data)
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN PGP")) {
		block, err := armor.Decode(bytes.NewReader(bytes.TrimSpace(data)))
		if err != nil {
			return nil, fmt.Errorf("pgp: the armor could not be read: %w", err)
		}
		r = block.Body
	}
	ring := append(append(openpgp.EntityList{}, o.Keys...), o.Verify...)
	md, err := openpgp.ReadMessage(r, ring, nil, config(false))
	if err != nil {
		if errors.Is(err, pgperrors.ErrKeyIncorrect) {
			return nil, fmt.Errorf("pgp: the message is encrypted to a key this channel does not hold: %w", err)
		}
		return nil, fmt.Errorf("pgp: %w", err)
	}
	if !md.IsEncrypted && !o.AllowUnencrypted {
		return nil, errors.New("pgp: the message is not encrypted, and this channel only takes encrypted ones")
	}
	body, err := io.ReadAll(md.UnverifiedBody)
	if err != nil {
		// A modified ciphertext or a bad signature surfaces here, after the whole body has been read.
		return nil, fmt.Errorf("pgp: %w", err)
	}
	out := &Opened{Data: body, Encrypted: md.IsEncrypted}
	if md.LiteralData != nil {
		out.FileName = md.LiteralData.FileName
	}
	if md.IsSigned {
		if md.SignatureError != nil {
			return nil, fmt.Errorf("pgp: the signature does not verify: %w", md.SignatureError)
		}
		if md.SignedBy != nil && isOneOf(md.SignedBy.Entity, o.Verify) {
			out.SignedBy = Describe(md.SignedBy.Entity)
		}
	}
	if o.RequireSignature && out.SignedBy == "" {
		if md.IsSigned {
			return nil, fmt.Errorf("pgp: signed by key %X, which is not one of the senders' keys", md.SignedByKeyId)
		}
		return nil, errors.New("pgp: the message is not signed, and this channel requires a signature")
	}
	return out, nil
}

func isOneOf(e *openpgp.Entity, list openpgp.EntityList) bool {
	if e == nil {
		return false
	}
	for _, k := range list {
		if bytes.Equal(k.PrimaryKey.Fingerprint, e.PrimaryKey.Fingerprint) {
			return true
		}
	}
	return false
}
