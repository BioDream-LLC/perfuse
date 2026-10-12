package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/mllp"
)

// A tenant over its limits: the message is refused before anything happens to it, and the sender is left to resend.

func TestARefusedMessageIsNotDeliveredAndGetsNoAcknowledgement(t *testing.T) {
	cfg, err := config.Load(strings.NewReader(twoDestinations), "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	senders := map[string]*recordingSender{}
	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		s := &recordingSender{name: d.Name}
		senders[d.Name] = s
		return s, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	over := errors.New("this organisation's message rate limit is reached")
	ch.SetAdmission(func() error { return over })

	if _, err := ch.HandleForTest(context.Background(), []byte(adtA01)); !errors.Is(err, ErrNotAdmitted) || !errors.Is(err, over) {
		t.Fatalf("handle returned %v, want ErrNotAdmitted wrapping the reason", err)
	}
	for name, s := range senders {
		if s.count() != 0 {
			t.Errorf("a refused message reached %s", name)
		}
	}

	// Over MLLP the sender gets no acknowledgement - not an AE or AR, which some senders take as "do not resend".
	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })
	c := &mllp.Client{Addr: ch.server.Addrs(), Timeout: 500 * time.Millisecond}
	defer c.Close()
	if reply, err := c.Send(context.Background(), []byte(adtA01)); err == nil {
		t.Errorf("a refused message was acknowledged: %q", reply)
	}
}
