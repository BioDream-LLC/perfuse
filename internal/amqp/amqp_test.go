package amqp

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTheCodecRoundTripsEveryShapeAPerformativeUses(t *testing.T) {
	in := []any{nil, true, false, UByte(7), UShort(300), UInt(0), UInt(9), UInt(70000), ULong(0), ULong(200), ULong(1 << 40),
		int32(-5), int32(100000), int64(-2), int64(1 << 40), "short", strings.Repeat("x", 300), Symbol("amqp:accepted:list"),
		[]byte{1, 2, 3}, []any{}, []any{UInt(1), "two"}, map[any]any{"k": UInt(1)}, Described{Descriptor: ULong(0x24), Value: []any{}},
		time.UnixMilli(1790000000000).UTC()}
	for _, v := range in {
		b := Encode(nil, v)
		got, n, err := Decode(b)
		if err != nil || n != len(b) {
			t.Fatalf("%#v: %v (read %d of %d)", v, err, n, len(b))
		}
		if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", v) {
			t.Errorf("%#v came back as %#v", v, got)
		}
	}
	syms := Encode(nil, []Symbol{"a", "bc"})
	got, _, err := Decode(syms)
	if err != nil || fmt.Sprint(got) != "[a bc]" {
		t.Errorf("symbol array: %v %v", got, err)
	}
	if _, _, err := Decode([]byte{0xb1, 0, 0, 0, 9, 'x'}); err == nil {
		t.Error("a truncated string decoded")
	}
}

func TestAMessageRoundTripsItsSections(t *testing.T) {
	m := &Message{Body: []byte("MSH|^~\\&|A\r"), ContentType: "application/hl7-v2", MessageID: "C1", Subject: "ADT^A01",
		Properties: map[string]any{"facility": "H"}}
	got, err := decodeMessage(encodeMessage(m))
	if err != nil || !bytes.Equal(got.Body, m.Body) || got.ContentType != m.ContentType || got.MessageID != "C1" ||
		got.Subject != "ADT^A01" || got.Properties["facility"] != "H" {
		t.Fatalf("%+v %v", got, err)
	}
}

// Brokers, started by ./scripts/interop-up.sh. Each test runs against every one that answers.
type broker struct{ name, addr, user, pass, address string }

func brokers(t *testing.T) []broker {
	t.Helper()
	all := []broker{
		{"rabbitmq-4", "127.0.0.1:5672", "perfuse", "perfuse", "/queues/perfuse-test"},
		{"activemq-classic", "127.0.0.1:5673", "admin", "admin", "perfuse-test"},
	}
	if v := os.Getenv("PERFUSE_AMQP"); v != "" {
		all = nil
		for _, b := range strings.Split(v, ";") {
			p := strings.Split(b, ",")
			all = append(all, broker{p[0], p[1], p[2], p[3], p[4]})
		}
	}
	var up []broker
	for _, b := range all {
		if c, err := net.DialTimeout("tcp", b.addr, time.Second); err == nil {
			_ = c.Close()
			up = append(up, b)
		}
	}
	if len(up) == 0 {
		t.Skip("no AMQP 1.0 broker is running; start them with ./scripts/interop-up.sh")
	}
	return up
}

func TestSendAndReceiveInOrderAgainstRealBrokers(t *testing.T) {
	if os.Getenv("AMQP_DEBUG") != "" {
		debugFrames = func(dir string, v any) {
			code, f := descriptorCode(v)
			out := fmt.Sprintf("%#v", f)
			if len(out) > 160 {
				out = out[:160]
			}
			t.Logf("%s 0x%02x %s", dir, code, out)
		}
	}
	for _, b := range brokers(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			c, err := Dial(ctx, Config{Addr: b.addr, Username: b.user, Password: b.pass, Timeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()

			// Drain anything an earlier run left.
			drain, err := c.NewReceiver(ctx, b.address, 50)
			if err != nil {
				t.Fatal(err)
			}
			for {
				dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				m, err := drain.Receive(dctx)
				cancel()
				if err != nil {
					break
				}
				_ = m.Accept()
			}
			_ = drain.Close()

			s, err := c.NewSender(ctx, b.address)
			if err != nil {
				t.Fatal(err)
			}
			big := bytes.Repeat([]byte("OBX|1|ED|PDF^Report||"), 12000) // ~250 KB: many frames
			bodies := [][]byte{[]byte("MSH|first\r"), []byte("MSH|second\r"), big}
			for i, body := range bodies {
				if err := s.Send(ctx, &Message{Body: body, ContentType: "application/hl7-v2", MessageID: fmt.Sprint("m", i)}); err != nil {
					t.Fatalf("send %d: %v", i, err)
				}
			}

			r, err := c.NewReceiver(ctx, b.address, 2)
			if err != nil {
				t.Fatal(err)
			}
			receive := func() *Message {
				t.Helper()
				rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				m, err := r.Receive(rctx)
				if err != nil {
					t.Fatalf("receive: %v", err)
				}
				return m
			}
			// The first arrives first. Released, it comes back - and where it comes back is the broker's to decide: ActiveMQ redelivers
			// it at once, RabbitMQ after what was already queued. So the other three are checked as a set.
			first := receive()
			if !bytes.Equal(first.Body, bodies[0]) {
				t.Fatalf("the first message was %d bytes", len(first.Body))
			}
			if err := first.Release(); err != nil {
				t.Fatal(err)
			}
			seen := map[int]int{}
			for i := 0; i < 3; i++ {
				m := receive()
				for j, want := range bodies {
					if bytes.Equal(m.Body, want) {
						seen[j]++
					}
				}
				if err := m.Accept(); err != nil {
					t.Fatal(err)
				}
			}
			if seen[0] != 1 || seen[1] != 1 || seen[2] != 1 {
				t.Fatalf("after the release, received %v (index: count); want the released one back and the other two once", seen)
			}
		})
	}
}
