package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/biodream-llc/perfuse/internal/pgp"
)

// PGPSend protects what a destination delivers with OpenPGP: encrypted to the receiver's public key, signed with ours, or
// both. Payers, clearinghouses and state agencies often take files over SFTP only this way.
type PGPSend struct {
	// EncryptTo are the receivers' public key files (armored or binary). The message is readable by any of them. Empty: the
	// message is signed only.
	EncryptTo []string `yaml:"encrypt_to,omitempty"`
	// SignWith is our secret key file, to sign each message. Empty: encrypted only.
	SignWith string `yaml:"sign_with,omitempty"`
	// Passphrase unlocks SignWith when it is protected. Prefer an environment variable reference, ${NAME}.
	Passphrase string `yaml:"passphrase,omitempty"`
	// Armor writes ASCII armor (-----BEGIN PGP MESSAGE-----) rather than binary, for receivers that ask for .asc files.
	Armor bool `yaml:"armor,omitempty"`
	// NoCompress turns off compressing before encrypting (gpg compresses by default).
	NoCompress bool `yaml:"no_compress,omitempty"`
	// FileName goes in the message as the original file name, which some receivers use for what they decrypt.
	FileName string `yaml:"file_name,omitempty"`

	to     openpgp.EntityList
	signer *openpgp.Entity
}

func (p *PGPSend) validate(where string) []error {
	if p == nil {
		return nil
	}
	var errs []error
	if len(p.EncryptTo) == 0 && strings.TrimSpace(p.SignWith) == "" {
		errs = append(errs, fmt.Errorf("%s: pgp needs encrypt_to (receivers' public keys), sign_with (our secret key), or both", where))
	}
	for i, f := range p.EncryptTo {
		if strings.TrimSpace(f) == "" {
			errs = append(errs, fmt.Errorf("%s: pgp.encrypt_to[%d] is empty", where, i))
		}
	}
	return errs
}

// PGPReceive opens what a source receives with OpenPGP: decrypted with our secret key, and its signature checked against
// the senders' public keys.
type PGPReceive struct {
	// DecryptWith is our secret key file. Required unless allow_unencrypted is on and messages are only signed.
	DecryptWith string `yaml:"decrypt_with,omitempty"`
	// Passphrase unlocks DecryptWith when it is protected. Prefer an environment variable reference, ${NAME}.
	Passphrase string `yaml:"passphrase,omitempty"`
	// VerifyWith are the senders' public key files. A signature by any of them is checked and recorded.
	VerifyWith []string `yaml:"verify_with,omitempty"`
	// RequireSignature refuses a message not signed by one of verify_with.
	RequireSignature bool `yaml:"require_signature,omitempty"`
	// AllowUnencrypted accepts a message that is signed but not encrypted.
	AllowUnencrypted bool `yaml:"allow_unencrypted,omitempty"`
	// PassPlain lets a message that is not PGP at all through unchanged, for a folder that receives both. Off: such a
	// message is refused, so a sender who forgot to encrypt is told.
	PassPlain bool `yaml:"pass_plain,omitempty"`

	keys, verify openpgp.EntityList
}

func (p *PGPReceive) validate(where string) []error {
	if p == nil {
		return nil
	}
	var errs []error
	if strings.TrimSpace(p.DecryptWith) == "" && !p.AllowUnencrypted {
		errs = append(errs, fmt.Errorf("%s: pgp needs decrypt_with (our secret key), or allow_unencrypted for messages that are only signed", where))
	}
	if p.RequireSignature && len(p.VerifyWith) == 0 {
		errs = append(errs, fmt.Errorf("%s: pgp.require_signature needs verify_with (the senders' public keys)", where))
	}
	return errs
}

func resolveIn(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

func readKeys(dir, path string) (openpgp.EntityList, error) {
	return pgp.ReadKeys(resolveIn(dir, strings.TrimSpace(path)))
}

// load reads and unlocks the keys now, so a missing file or a wrong passphrase stops the channel loading rather than
// failing on the first message.
func (p *PGPSend) load(dir, where string) []error {
	var errs []error
	p.to = nil
	for _, f := range p.EncryptTo {
		list, err := readKeys(dir, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: pgp.encrypt_to %s: %w", where, f, err))
			continue
		}
		p.to = append(p.to, list...)
	}
	if strings.TrimSpace(p.SignWith) != "" {
		list, err := readKeys(dir, p.SignWith)
		if err == nil {
			err = pgp.Unlock(list[:1], []byte(resolveSecret(p.Passphrase)))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: pgp.sign_with %s: %w", where, p.SignWith, err))
		} else {
			p.signer = list[0]
		}
	}
	return errs
}

// Seal protects one message as the block says. The keys must have been loaded with the channel.
func (p *PGPSend) Seal(data []byte) ([]byte, error) {
	if len(p.to) == 0 && p.signer == nil {
		return nil, fmt.Errorf("pgp: the keys were not loaded with the channel")
	}
	return pgp.Seal(data, pgp.SealOptions{To: p.to, Signer: p.signer, Armor: p.Armor, Compress: !p.NoCompress, FileName: p.FileName})
}

// Recipients describes the keys messages are encrypted to, for the interface.
func (p *PGPSend) Recipients() []string {
	out := make([]string, 0, len(p.to))
	for _, e := range p.to {
		out = append(out, pgp.Describe(e))
	}
	return out
}

func (p *PGPReceive) load(dir, where string) []error {
	var errs []error
	p.keys, p.verify = nil, nil
	if strings.TrimSpace(p.DecryptWith) != "" {
		list, err := readKeys(dir, p.DecryptWith)
		if err == nil {
			err = pgp.Unlock(list, []byte(resolveSecret(p.Passphrase)))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: pgp.decrypt_with %s: %w", where, p.DecryptWith, err))
		} else {
			p.keys = list
		}
	}
	for _, f := range p.VerifyWith {
		list, err := readKeys(dir, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: pgp.verify_with %s: %w", where, f, err))
			continue
		}
		p.verify = append(p.verify, list...)
	}
	return errs
}

// Open decrypts and verifies one received message. A message that is not PGP is passed through when pass_plain is on.
func (p *PGPReceive) Open(data []byte) (out []byte, signedBy string, err error) {
	if !pgp.IsPGP(data) {
		if p.PassPlain {
			return data, "", nil
		}
		return nil, "", fmt.Errorf("pgp: the message is not an OpenPGP message, and this source only takes encrypted ones")
	}
	o, err := pgp.Open(data, pgp.OpenOptions{Keys: p.keys, Verify: p.verify, RequireSignature: p.RequireSignature,
		AllowUnencrypted: p.AllowUnencrypted})
	if err != nil {
		return nil, "", err
	}
	return o.Data, o.SignedBy, nil
}
