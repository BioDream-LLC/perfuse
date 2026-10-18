package pgp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func newKey(t *testing.T, name string) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity(name, "", name+"@example.org", &packet.Config{RSABits: 2048, Algorithm: packet.PubKeyAlgoRSA})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func armoredPublic(t *testing.T, e *openpgp.Entity) []byte {
	t.Helper()
	var b bytes.Buffer
	w, _ := armor.Encode(&b, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.Bytes()
}

func armoredSecret(t *testing.T, e *openpgp.Entity, passphrase string) []byte {
	t.Helper()
	if passphrase != "" {
		if err := e.EncryptPrivateKeys([]byte(passphrase), nil); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	w, _ := armor.Encode(&b, openpgp.PrivateKeyType, nil)
	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.Bytes()
}

func TestSealAndOpen(t *testing.T) {
	us, payer := newKey(t, "perfuse"), newKey(t, "payer")
	msg := []byte("ISA*00*...~")

	for _, arm := range []bool{true, false} {
		sealed, err := Seal(msg, SealOptions{To: openpgp.EntityList{payer}, Signer: us, Armor: arm, Compress: true, FileName: "claims.x12"})
		if err != nil {
			t.Fatal(err)
		}
		if !IsPGP(sealed) {
			t.Errorf("armor=%v: not recognised as PGP", arm)
		}
		got, err := Open(sealed, OpenOptions{Keys: openpgp.EntityList{payer}, Verify: openpgp.EntityList{us}, RequireSignature: true})
		if err != nil {
			t.Fatalf("armor=%v: %v", arm, err)
		}
		if !bytes.Equal(got.Data, msg) || got.FileName != "claims.x12" || !got.Encrypted || !strings.Contains(got.SignedBy, "perfuse") {
			t.Errorf("armor=%v: %+v", arm, got)
		}
	}
	if IsPGP([]byte("MSH|^~\\&|")) {
		t.Error("an HL7 message taken for PGP")
	}
}

func TestOpenRefuses(t *testing.T) {
	us, payer, stranger := newKey(t, "perfuse"), newKey(t, "payer"), newKey(t, "stranger")
	msg := []byte("hello")

	// Encrypted to somebody else.
	sealed, _ := Seal(msg, SealOptions{To: openpgp.EntityList{stranger}})
	if _, err := Open(sealed, OpenOptions{Keys: openpgp.EntityList{payer}}); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("wrong key: %v", err)
	}
	// Unsigned when a signature is required.
	sealed, _ = Seal(msg, SealOptions{To: openpgp.EntityList{payer}})
	if _, err := Open(sealed, OpenOptions{Keys: openpgp.EntityList{payer}, Verify: openpgp.EntityList{us}, RequireSignature: true}); err == nil ||
		!strings.Contains(err.Error(), "not signed") {
		t.Errorf("unsigned: %v", err)
	}
	// Signed by a key that is not the sender's.
	sealed, _ = Seal(msg, SealOptions{To: openpgp.EntityList{payer}, Signer: stranger})
	if _, err := Open(sealed, OpenOptions{Keys: openpgp.EntityList{payer}, Verify: openpgp.EntityList{us}, RequireSignature: true}); err == nil {
		t.Error("a stranger's signature was accepted")
	}
	// Signed only, not encrypted.
	signed, _ := Seal(msg, SealOptions{Signer: us})
	if _, err := Open(signed, OpenOptions{Verify: openpgp.EntityList{us}}); err == nil || !strings.Contains(err.Error(), "not encrypted") {
		t.Errorf("signed only: %v", err)
	}
	if got, err := Open(signed, OpenOptions{Verify: openpgp.EntityList{us}, AllowUnencrypted: true, RequireSignature: true}); err != nil ||
		!bytes.Equal(got.Data, msg) {
		t.Errorf("signed only, allowed: %v", err)
	}
	// A flipped byte in the ciphertext.
	sealed, _ = Seal(bytes.Repeat([]byte("x"), 4000), SealOptions{To: openpgp.EntityList{payer}})
	sealed[len(sealed)-40] ^= 1
	if _, err := Open(sealed, OpenOptions{Keys: openpgp.EntityList{payer}}); err == nil {
		t.Error("a modified message was opened")
	}
}

func TestKeysFromFiles(t *testing.T) {
	e := newKey(t, "payer")
	pub := armoredPublic(t, e)
	sec := armoredSecret(t, e, "s3cret")
	list, err := ParseKeys(pub)
	if err != nil || len(list) != 1 || list[0].PrivateKey != nil {
		t.Fatalf("public: %v", err)
	}
	if err := Unlock(list, nil); err == nil {
		t.Error("a public key unlocked")
	}
	list, err = ParseKeys(sec)
	if err != nil {
		t.Fatal(err)
	}
	if err := Unlock(list, nil); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("no passphrase: %v", err)
	}
	if err := Unlock(list, []byte("wrong")); err == nil {
		t.Error("a wrong passphrase unlocked")
	}
	if err := Unlock(list, []byte("s3cret")); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseKeys([]byte("not a key")); err == nil {
		t.Error("garbage parsed")
	}
}

// Against GnuPG both ways: a payer's gpg must read what Perfuse writes, and Perfuse must read what gpg writes.
func TestInteroperatesWithGnuPG(t *testing.T) {
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("gpg not installed")
	}
	// Short and private: gpg-agent's socket path must fit a Unix socket name, which a macOS test directory does not. Windows
	// has no /tmp, and its gpg-agent takes no Unix socket, so its own temporary directory serves there.
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = ""
	}
	home, err := os.MkdirTemp(base, "pfgpg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	run := func(stdin []byte, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(gpg, append([]string{"--homedir", home, "--batch", "--yes", "--pinentry-mode", "loopback", "--passphrase", ""}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			if strings.Contains(errb.String(), "gpg-agent") && strings.Contains(errb.String(), "not installed") {
				// Amazon Linux's gnupg2-minimal, for one, has no agent, and gpg cannot make a key without it.
				t.Skip("this gpg has no gpg-agent: " + strings.TrimSpace(errb.String()))
			}
			t.Fatalf("gpg %v: %v\n%s", args, err, errb.String())
		}
		return out.Bytes()
	}
	t.Cleanup(func() { _ = exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run() })
	run(nil, "--quick-gen-key", "Payer <payer@example.org>", "rsa3072", "sign,encrypt", "never")
	gpgPub, err := ParseKeys(run(nil, "--armor", "--export", "payer@example.org"))
	if err != nil {
		t.Fatal(err)
	}

	us := newKey(t, "perfuse")
	pubFile := filepath.Join(home, "perfuse.asc")
	if err := os.WriteFile(pubFile, armoredPublic(t, us), 0o600); err != nil {
		t.Fatal(err)
	}
	run(nil, "--import", pubFile)

	// Perfuse to gpg: encrypted to gpg's key, signed by ours.
	msg := []byte("ISA*00*          *00*          *ZZ*SENDER~")
	sealed, err := Seal(msg, SealOptions{To: gpgPub, Signer: us, Armor: true, Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := run(sealed, "--trust-model", "always", "--decrypt"); !bytes.Equal(got, msg) {
		t.Errorf("gpg read %q", got)
	}

	// gpg to Perfuse: encrypted to our key, signed by gpg's.
	plain := []byte("MSH|^~\\&|LAB|||20261017||ORU^R01|1|P|2.5\r")
	fromGPG := run(plain, "--trust-model", "always", "--recipient", "perfuse@example.org", "--local-user", "payer@example.org",
		"--sign", "--encrypt")
	got, err := Open(fromGPG, OpenOptions{Keys: openpgp.EntityList{us}, Verify: gpgPub, RequireSignature: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, plain) || !strings.Contains(got.SignedBy, "payer@example.org") {
		t.Errorf("read %q, signed by %q", got.Data, got.SignedBy)
	}
	armored := run(plain, "--trust-model", "always", "--armor", "--recipient", "perfuse@example.org", "--encrypt")
	if got, err := Open(armored, OpenOptions{Keys: openpgp.EntityList{us}}); err != nil || !bytes.Equal(got.Data, plain) {
		t.Errorf("armored from gpg: %v", err)
	}
}
