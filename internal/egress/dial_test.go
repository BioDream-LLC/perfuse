package egress

import (
	"context"
	"net"
	"strings"
	"testing"
)

// TestTheDialerRefusesAMetadataAddressAtConnectTime is the DNS-rebinding case: whatever a name resolved to when it was
// checked, the address the socket is about to reach is what the dialer judges.
func TestTheDialerRefusesAMetadataAddressAtConnectTime(t *testing.T) {
	previous := SetDefault(Policy{})
	defer SetDefault(previous)

	_, err := Dialer(0).DialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("dialling the metadata address: %v, want a refusal naming it", err)
	}

	// An ordinary address is not the dialer's business.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := Dialer(0).DialContext(context.Background(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dialling loopback: %v", err)
	}
	c.Close()
}
