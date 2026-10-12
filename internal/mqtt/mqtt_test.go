package mqtt

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRemainingLengthsEncodeAndDecode(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 2097151, 2097152} {
		p := packet(typePublish<<4, make([]byte, n))
		first, body, err := readPacket(bufio.NewReader(bytes.NewReader(p)), 1<<22)
		if err != nil || first != typePublish<<4 || len(body) != n {
			t.Errorf("%d bytes: %v, came back as %d", n, err, len(body))
		}
	}
	if _, _, err := readPacket(bufio.NewReader(bytes.NewReader([]byte{0x30, 0xff, 0xff, 0xff, 0xff, 0x7f})), 1<<30); err == nil {
		t.Error("a five-byte remaining length was read")
	}
	if _, _, err := readPacket(bufio.NewReader(bytes.NewReader(packet(0x30, make([]byte, 100)))), 50); err == nil {
		t.Error("a packet over the limit was read")
	}
}

func TestTopicsAndFilters(t *testing.T) {
	for _, ok := range []string{"hl7/adt", "a/+/c", "a/#", "#", "+"} {
		if err := validTopic(ok, true); err != nil {
			t.Errorf("filter %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a/b#", "a+/b", "a/#/c"} {
		if validTopic(bad, true) == nil {
			t.Errorf("filter %q was allowed", bad)
		}
	}
	if validTopic("a/+", false) == nil {
		t.Error("a wildcard was allowed in a topic published to")
	}
}

// broker is the address of a running broker, or a skip. ./scripts/interop-up.sh starts Mosquitto with user perfuse.
func broker(t *testing.T) string {
	addr := os.Getenv("PERFUSE_MQTT")
	if addr == "" {
		addr = "127.0.0.1:1883"
	}
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Skip("no MQTT broker is running; start one with ./scripts/interop-up.sh")
	}
	_ = c.Close()
	return addr
}

func TestAgainstABroker(t *testing.T) {
	addr := broker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topic := fmt.Sprintf("perfuse/test/%d", time.Now().UnixNano())
	cfg := func(id string) Config {
		return Config{Addr: addr, ClientID: id, Username: "perfuse", Password: "perfuse", Timeout: 5 * time.Second}
	}

	if _, err := Dial(ctx, Config{Addr: addr, ClientID: "x", Username: "perfuse", Password: "wrong", CleanSession: true}); !errors.As(err, new(ConnackError)) {
		t.Fatalf("a wrong password: %v, want the broker's refusal", err)
	}

	// A persistent session subscribes, then goes away.
	sub, err := Dial(ctx, cfg("perfuse-test-sub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Subscribe(ctx, topic+"/#", 1); err != nil {
		t.Fatal(err)
	}
	_ = sub.Close()

	// Messages published while it is away, one of them large.
	pub, err := Dial(ctx, Config{Addr: addr, ClientID: "perfuse-test-pub", Username: "perfuse", Password: "perfuse", CleanSession: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	big := []byte("MSH|^~\\&|BIG\r" + strings.Repeat("OBX|1|TX|||x\r", 20000))
	for _, m := range [][]byte{[]byte("MSH|^~\\&|ONE\r"), big, []byte("MSH|^~\\&|THREE\r")} {
		if err := pub.Publish(ctx, topic+"/adt", m, 1, false); err != nil {
			t.Fatal(err)
		}
	}

	// It comes back, takes the first and does not acknowledge it: a failed handling.
	sub, err = Dial(ctx, cfg("perfuse-test-sub"))
	if err != nil {
		t.Fatal(err)
	}
	first := next(t, sub)
	if !bytes.Contains(first.Payload, []byte("ONE")) {
		t.Fatalf("first message: %.30q", first.Payload)
	}
	_ = sub.Close()

	// Again: the unacknowledged one is sent again, then the rest, in order.
	sub, err = Dial(ctx, cfg("perfuse-test-sub"))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var got []string
	for i := 0; i < 3; i++ {
		m := next(t, sub)
		got = append(got, string(m.Payload[9:12]))
		if i == 1 && len(m.Payload) != len(big) {
			t.Errorf("the large message came back as %d bytes, sent %d", len(m.Payload), len(big))
		}
		if err := m.Ack(); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(got, ",") != "ONE,BIG,THR" {
		t.Errorf("received %v", got)
	}
}

func next(t *testing.T, c *Conn) *Message {
	t.Helper()
	select {
	case m, ok := <-c.Messages():
		if !ok {
			t.Fatalf("the connection ended: %v", c.Err())
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no message")
	}
	return nil
}
