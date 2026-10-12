package ibmmq

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// queue is DEV.QUEUE.2 on the developer container's QM1, or a skip. ./scripts/interop-up.sh starts it.
func queue(t *testing.T, user, pass string) *Client {
	t.Helper()
	base := os.Getenv("PERFUSE_IBMMQ")
	if base == "" {
		base = "https://127.0.0.1:9443"
	}
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "https://"), time.Second)
	if err != nil {
		t.Skip("no IBM MQ is running; start it with ./scripts/interop-up.sh")
	}
	_ = c.Close()
	// The developer container's certificate is self-signed; a site gives tls.ca_file.
	cl, err := New(Config{BaseURL: base, QueueManager: "QM1", Queue: "DEV.QUEUE.2", Username: user, Password: pass,
		TLS: &tls.Config{InsecureSkipVerify: true}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func TestAgainstIBMMQ(t *testing.T) {
	cl := queue(t, "app", "passw0rd")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for { // empty the queue first
		m, err := cl.Get(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if m == nil {
			break
		}
	}

	big := "MSH|^~\\&|BIG\r" + strings.Repeat("OBX|1|TX|||x\r", 20000)
	for _, b := range []string{"MSH|^~\\&|ONE\r", big} {
		if err := cl.Put(ctx, []byte(b), "C1"); err != nil {
			t.Fatal(err)
		}
	}

	// Browsing does not remove: twice, the same first message.
	a, err := cl.Browse(ctx)
	if err != nil || a == nil {
		t.Fatalf("browse: %v %v", a, err)
	}
	b, err := cl.Browse(ctx)
	if err != nil || b == nil || b.ID != a.ID || string(a.Body) != "MSH|^~\\&|ONE\r" {
		t.Fatalf("browsing again: %+v %v", b, err)
	}
	if gone, err := cl.Remove(ctx, a.ID); err != nil || !gone {
		t.Fatalf("remove: %v %v", gone, err)
	}
	if gone, err := cl.Remove(ctx, a.ID); err != nil || gone {
		t.Errorf("removing it again: %v %v, want not found", gone, err)
	}
	next, err := cl.Browse(ctx)
	if err != nil || next == nil || len(next.Body) != len(big) {
		t.Fatalf("the large message: %v", err)
	}
	if _, err := cl.Remove(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := cl.Browse(ctx); err != nil || m != nil {
		t.Errorf("the queue should be empty: %+v %v", m, err)
	}

	var e *Error
	if err := queue(t, "app", "wrong").Put(ctx, []byte("x"), ""); !errors.As(err, &e) || e.Status != 401 {
		t.Errorf("a wrong password: %v, want 401", err)
	}
}
