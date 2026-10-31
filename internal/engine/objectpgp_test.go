package engine

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/biodream-llc/perfuse/internal/azblob"
	"github.com/biodream-llc/perfuse/internal/gcp"
	"github.com/biodream-llc/perfuse/internal/pgp"
)

// These tests guard what an object-store source writes back when its objects are encrypted.
//
// The GCS and Azure Blob sources replaced the fetched bytes with the decrypted content and then wrote "the same bytes"
// to the processed or error prefix, so a bucket set up to hold PHI encrypted received a plaintext copy of every object.
// The S3 source copied the original object and was right; these two now do the same.

func pgpSourceKeys(t *testing.T) (dir string, us, payer *openpgp.Entity) {
	t.Helper()
	dir = t.TempDir()
	us, payer = pgpKey(t, "perfuse"), pgpKey(t, "payer")
	writeKey(t, filepath.Join(dir, "our-secret.asc"), us, true, "open sesame")
	writeKey(t, filepath.Join(dir, "payer.asc"), payer, false, "")
	t.Setenv("PF_TEST_PGP_PASS", "open sesame")
	return dir, us, payer
}

func stillSealed(t *testing.T, what string, got []byte) {
	t.Helper()
	if bytes.Contains(got, []byte("MSH|")) || bytes.Contains(got, []byte("ADT^A01")) {
		t.Fatalf("%s holds the decrypted message: %.60q", what, got)
	}
}

func TestAGCSSourceMovesTheEncryptedObjectNotThePlaintext(t *testing.T) {
	listening(t, "127.0.0.1:4443")
	keys, us, payer := pgpSourceKeys(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bucket := fmt.Sprintf("pgp%d", time.Now().UnixNano())
	st, _ := gcp.NewStorage("http://127.0.0.1:4443", bucket, "", nil)
	if err := st.CreateBucket(ctx, "perfuse-test"); err != nil {
		t.Fatal(err)
	}
	sealed, err := pgp.Seal([]byte(lsADT), pgp.SealOptions{To: openpgp.EntityList{us}, Signer: payer})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "inbound/one.pgp", sealed, ""); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	runChannel(t, fmt.Sprintf(`
name: gcs-pgp
source:
  type: gcs
  gcs:
    endpoint: http://127.0.0.1:4443
    bucket: %s
    prefix: inbound/
    poll_interval: 1s
  pgp:
    decrypt_with: %s
    passphrase: ${PF_TEST_PGP_PASS}
    verify_with: [%s]
destinations:
  - name: onward
    type: file
    dir: %s
`, bucket, filepath.Join(keys, "our-secret.asc"), filepath.Join(keys, "payer.asc"), out))
	for range 40 {
		time.Sleep(500 * time.Millisecond)
		done, _, _ := st.List(ctx, "processed/", "", 10)
		if len(done) == 1 {
			got, err := st.Get(ctx, done[0].Name, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			stillSealed(t, done[0].Name, got)
			if !bytes.Equal(got, sealed) {
				t.Errorf("processed/ holds %d bytes, not the %d-byte object that arrived", len(got), len(sealed))
			}
			return
		}
	}
	t.Fatal("the object was not moved to processed/")
}

func TestAnAzureBlobSourceMovesTheEncryptedBlobNotThePlaintext(t *testing.T) {
	listening(t, "127.0.0.1:10000")
	keys, us, payer := pgpSourceKeys(t)
	ctx := context.Background()
	container := fmt.Sprintf("pgp-%d", time.Now().UnixNano())
	in, err := azblob.New(azblob.Config{Account: azuriteAccount, Key: azuriteKey, Container: container,
		Endpoint: "http://127.0.0.1:10000/" + azuriteAccount})
	if err != nil {
		t.Fatal(err)
	}
	if err := in.CreateContainer(ctx); err != nil {
		t.Fatal(err)
	}
	sealed, err := pgp.Seal([]byte(lsADT), pgp.SealOptions{To: openpgp.EntityList{us}, Signer: payer})
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Put(ctx, "inbound/one.pgp", sealed, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PF_TEST_AZURITE_KEY", azuriteKey)
	out := t.TempDir()
	runChannel(t, fmt.Sprintf(`
name: azure-pgp
source:
  type: azure_blob
  azure_blob:
    account: %s
    key: ${PF_TEST_AZURITE_KEY}
    container: %s
    endpoint: http://127.0.0.1:10000/%s
    prefix: inbound/
    poll_interval: 1s
  pgp:
    decrypt_with: %s
    passphrase: ${PF_TEST_PGP_PASS}
    verify_with: [%s]
destinations:
  - name: onward
    type: file
    dir: %s
`, azuriteAccount, container, azuriteAccount, filepath.Join(keys, "our-secret.asc"), filepath.Join(keys, "payer.asc"), out))
	for range 40 {
		time.Sleep(500 * time.Millisecond)
		done, _, _ := in.List(ctx, "processed/", "", 10)
		if len(done) == 1 {
			got, err := in.Get(ctx, done[0].Name, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			stillSealed(t, done[0].Name, got)
			return
		}
	}
	t.Fatal("the blob was not moved to processed/")
}
