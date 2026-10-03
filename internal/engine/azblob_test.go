package engine

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/azblob"
)

const (
	azuriteAccount = "devstoreaccount1"
	azuriteKey     = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
)

// Blobs in, blobs out, against Azurite: a good blob moves to processed/, an unparseable one to error/, and what the channel delivered
// is read back from the destination container in the tier it was written with.
func TestAnAzureBlobChannelAgainstAzurite(t *testing.T) {
	if c, err := net.DialTimeout("tcp", "127.0.0.1:10000", time.Second); err != nil {
		t.Skip("no Azurite on 127.0.0.1:10000; start it with ./scripts/interop-up.sh")
	} else {
		_ = c.Close()
	}
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	inName, outName := fmt.Sprintf("in-%d", stamp), fmt.Sprintf("out-%d", stamp)
	mk := func(container string) *azblob.Client {
		c, err := azblob.New(azblob.Config{Account: azuriteAccount, Key: azuriteKey, Container: container,
			Endpoint: "http://127.0.0.1:10000/" + azuriteAccount})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.CreateContainer(ctx); err != nil {
			t.Fatal(err)
		}
		return c
	}
	in, out := mk(inName), mk(outName)
	_ = in.Put(ctx, "inbound/good.hl7", []byte(lsADT), "")
	_ = in.Put(ctx, "inbound/bad.hl7", []byte("not hl7"), "")

	runChannel(t, fmt.Sprintf(`
name: blobs
source:
  type: azure_blob
  azure_blob:
    account: %[1]s
    key: %[2]s
    container: %[3]s
    endpoint: http://127.0.0.1:10000/%[1]s
    prefix: inbound/
    poll_interval: 1s
destinations:
  - name: archive
    type: azure_blob
    azure_blob:
      account: %[1]s
      key: %[2]s
      container: %[4]s
      endpoint: http://127.0.0.1:10000/%[1]s
      blob: archive/${control_id}.hl7
      tier: cool
`, azuriteAccount, azuriteKey, inName, outName))

	deadline := time.Now().Add(10 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if b, err := out.Get(ctx, "archive/C1.hl7", 1<<20); err == nil {
			got = b
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if string(got) != lsADT {
		t.Fatalf("the destination holds %q", got)
	}
	if h, _ := out.Properties(ctx, "archive/C1.hl7"); h.Get("x-ms-access-tier") != "Cool" {
		t.Errorf("tier %q", h.Get("x-ms-access-tier"))
	}
	time.Sleep(time.Second)
	names := func(prefix string) []string {
		bs, _, _ := in.List(ctx, prefix, "", 100)
		var n []string
		for _, b := range bs {
			n = append(n, b.Name)
		}
		return n
	}
	if n := names(""); fmt.Sprint(n) != "[error/bad.hl7 processed/good.hl7]" {
		t.Errorf("the source container holds %v", n)
	}
}
