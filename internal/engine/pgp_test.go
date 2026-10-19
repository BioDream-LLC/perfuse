package engine

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/pgp"
)

func writeKey(t *testing.T, path string, e *openpgp.Entity, secret bool, passphrase string) {
	t.Helper()
	var b bytes.Buffer
	kind := openpgp.PublicKeyType
	if secret {
		kind = openpgp.PrivateKeyType
	}
	w, _ := armor.Encode(&b, kind, nil)
	if secret {
		c := *e
		if passphrase != "" {
			if err := c.EncryptPrivateKeys([]byte(passphrase), nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.SerializePrivateWithoutSigning(w, nil); err != nil {
			t.Fatal(err)
		}
	} else if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pgpKey(t *testing.T, name string) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity(name, "", name+"@example.org", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A payer drops encrypted, signed files; the channel opens them, and its destination encrypts what it writes to the
// payer and signs it. A file that is not encrypted is refused rather than delivered.
func TestAChannelOpensAndSealsPGP(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	us, payer := pgpKey(t, "perfuse"), pgpKey(t, "payer")
	writeKey(t, filepath.Join(dir, "our-secret.asc"), us, true, "open sesame")
	writeKey(t, filepath.Join(dir, "payer.asc"), payer, false, "")
	t.Setenv("PF_TEST_PGP_PASS", "open sesame")
	for _, d := range []string{"inbox", "out", "errors"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	channel := `name: payer-drop
source:
  type: file
  file:
    root: ` + dir + `
    dir: inbox
    pattern: "*.pgp"
    poll_interval: 1s
    stable_for: 0s
    error_dir: errors
  pgp:
    decrypt_with: our-secret.asc
    passphrase: ${PF_TEST_PGP_PASS}
    verify_with: [payer.asc]
    require_signature: true
destinations:
  - name: back
    type: file
    dir: ` + filepath.Join(dir, "out") + `
    file_name: "${control_id}.hl7.asc"
    pgp:
      encrypt_to: [payer.asc]
      sign_with: our-secret.asc
      passphrase: ${PF_TEST_PGP_PASS}
      armor: true
`
	path := filepath.Join(dir, "payer-drop.yaml")
	if err := os.WriteFile(path, []byte(channel), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, DefaultSenderFactory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Stop(t.Context()) })

	sealed, err := pgp.Seal([]byte(lsADT), pgp.SealOptions{To: openpgp.EntityList{us}, Signer: payer})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inbox", "a.pgp"), sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inbox", "plain.pgp"), []byte(lsADT), 0o600); err != nil {
		t.Fatal(err)
	}

	var files []string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		files, _ = filepath.Glob(filepath.Join(dir, "out", "*"))
		errs, _ := filepath.Glob(filepath.Join(dir, "errors", "*"))
		if len(files) > 0 && len(errs) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(files) != 1 {
		left, _ := filepath.Glob(filepath.Join(dir, "*", "*"))
		t.Fatalf("delivered %v; files %v", files, left)
	}
	out, _ := os.ReadFile(files[0])
	if !bytes.HasPrefix(out, []byte("-----BEGIN PGP MESSAGE-----")) {
		t.Fatalf("not armored: %.60q", out)
	}
	opened, err := pgp.Open(out, pgp.OpenOptions{Keys: openpgp.EntityList{payer}, Verify: openpgp.EntityList{us}, RequireSignature: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(opened.Data), "ADT^A01") {
		t.Errorf("payer read %q", opened.Data)
	}
	if errs, _ := filepath.Glob(filepath.Join(dir, "errors", "plain.pgp*")); len(errs) != 1 {
		t.Errorf("the unencrypted file was not refused: %v", errs)
	}
}

func TestPGPKeysAreCheckedWhenTheChannelLoads(t *testing.T) {
	dir := t.TempDir()
	us := pgpKey(t, "perfuse")
	writeKey(t, filepath.Join(dir, "our-secret.asc"), us, true, "right")
	load := func(block string) error {
		path := filepath.Join(dir, "c.yaml")
		y := "name: c\nsource: {type: mllp, listen: \"127.0.0.1:0\"}\ndestinations:\n  - name: out\n    type: file\n    dir: " + dir +
			"\n    pgp:\n" + block
		if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := config.LoadFile(path)
		return err
	}
	for _, c := range []struct{ block, want string }{
		{"      sign_with: our-secret.asc\n      passphrase: wrong\n", "passphrase is wrong"},
		{"      sign_with: our-secret.asc\n", "none was given"},
		{"      encrypt_to: [missing.asc]\n", "missing.asc"},
		{"      armor: true\n", "needs encrypt_to"},
	} {
		if err := load(c.block); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.block, err)
		}
	}
	if err := load("      sign_with: our-secret.asc\n      passphrase: right\n"); err != nil {
		t.Errorf("a right passphrase: %v", err)
	}
}

// An HTTP sender whose message will not open is told so: 400 and the reason, not 500, since the fault is its own.
func TestAnHTTPSenderIsToldWhyPGPRefusedIt(t *testing.T) {
	dir := t.TempDir()
	us, payer := pgpKey(t, "perfuse"), pgpKey(t, "payer")
	writeKey(t, filepath.Join(dir, "ours.asc"), us, true, "")
	writeKey(t, filepath.Join(dir, "payer.asc"), payer, false, "")
	addr := freeAddr(t)
	y := "name: h\nsource:\n  type: http\n  http:\n    listen: \"" + addr + "\"\n    path: /in\n  pgp:\n    decrypt_with: ours.asc\n" +
		"    verify_with: [payer.asc]\n    require_signature: true\ndestinations:\n  - name: out\n    type: file\n    dir: " + filepath.Join(dir, "out") + "\n"
	path := filepath.Join(dir, "h.yaml")
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, DefaultSenderFactory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Stop(t.Context()) })

	post := func(body []byte) (int, string) {
		res, err := http.Post("http://"+addr+"/in", "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	unsigned, err := pgp.Seal([]byte(lsADT), pgp.SealOptions{To: openpgp.EntityList{us}})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := post(unsigned); code != http.StatusBadRequest || !strings.Contains(body, "not signed") {
		// Seen once on Windows arm64 ("decryption with session key failed: parsing error") and never in 30,000 local
		// tries: the message and its throwaway key, so it can be replayed.
		key, _ := os.ReadFile(filepath.Join(dir, "ours.asc"))
		t.Errorf("unsigned: %d %s\nmessage (base64): %s\nkey:\n%s", code, body, base64.StdEncoding.EncodeToString(unsigned), key)
	}
	signed, err := pgp.Seal([]byte(lsADT), pgp.SealOptions{To: openpgp.EntityList{us}, Signer: payer})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := post(signed); code != http.StatusOK {
		t.Errorf("signed: %d %s", code, body)
	}
}
