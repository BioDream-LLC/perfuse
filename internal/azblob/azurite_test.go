package azblob

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// Azurite's well-known development account: published by Microsoft, and valid nowhere but the emulator.
const (
	devAccount = "devstoreaccount1"
	devKey     = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
)

func azurite(t *testing.T, tier string) *Client {
	t.Helper()
	if c, err := net.DialTimeout("tcp", "127.0.0.1:10000", time.Second); err != nil {
		t.Skip("no Azurite on 127.0.0.1:10000; start it with ./scripts/interop-up.sh")
	} else {
		_ = c.Close()
	}
	c, err := New(Config{Account: devAccount, Key: devKey, Container: fmt.Sprintf("perfuse-%d", time.Now().UnixNano()),
		Endpoint: "http://127.0.0.1:10000/" + devAccount, Tier: tier})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CreateContainer(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

// Against Azurite, which checks Shared Key signatures the way the service does: a wrong key is refused.
func TestPutListGetDeleteAgainstAzurite(t *testing.T) {
	c := azurite(t, "")
	ctx := context.Background()
	for _, n := range []string{"in/b.hl7", "in/a b.hl7", "out/c.hl7"} {
		if err := c.Put(ctx, n, []byte("body of "+n), "application/hl7-v2"); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := c.List(ctx, "in/", "", 100)
	if err != nil || len(got) != 2 || got[0].Name != "in/a b.hl7" || got[0].Size == 0 {
		t.Fatalf("%+v %v", got, err)
	}
	body, err := c.Get(ctx, "in/a b.hl7", 1<<20)
	if err != nil || string(body) != "body of in/a b.hl7" {
		t.Fatalf("%q %v", body, err)
	}
	if err := c.Delete(ctx, "in/a b.hl7"); err != nil {
		t.Fatal(err)
	}
	if left, _, _ := c.List(ctx, "in/", "", 100); len(left) != 1 {
		t.Errorf("after delete: %+v", left)
	}

	wrong, _ := New(Config{Account: devAccount, Key: "d3Jvbmcta2V5LXdyb25nLWtleQ==", Container: c.cfg.Container, Endpoint: c.cfg.Endpoint})
	if err := wrong.Put(ctx, "x", []byte("x"), ""); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a wrong key was accepted: %v", err)
	}
}

func TestTheAccessTierReachesTheBlobAgainstAzurite(t *testing.T) {
	c := azurite(t, "Cool")
	ctx := context.Background()
	if err := c.Put(ctx, "archive/x.hl7", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	h, err := c.Properties(ctx, "archive/x.hl7")
	if err != nil || h.Get("x-ms-access-tier") != "Cool" {
		t.Errorf("tier %q %v", h.Get("x-ms-access-tier"), err)
	}
}
