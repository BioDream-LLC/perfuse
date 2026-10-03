package engine

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/amqp"
)

// A channel reading from one AMQP queue and sending to another, run against RabbitMQ 4 and ActiveMQ Classic: the message is looked for
// in the destination queue, and the source queue is checked empty, so acceptance after handling is what is tested.
func TestAnAMQPChannelMovesAMessageBetweenQueuesOnRealBrokers(t *testing.T) {
	brokers := []struct{ name, addr, user, pass, prefix string }{
		{"rabbitmq-4", "127.0.0.1:5672", "perfuse", "perfuse", "/queues/"},
		{"activemq-classic", "127.0.0.1:5673", "admin", "admin", ""},
	}
	ran := 0
	for _, b := range brokers {
		if c, err := net.DialTimeout("tcp", b.addr, time.Second); err != nil {
			continue
		} else {
			_ = c.Close()
		}
		ran++
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			in, out := b.prefix+"perfuse-in", b.prefix+"perfuse-out"
			if b.name == "rabbitmq-4" {
				declareRabbitQueues(t, "perfuse-in", "perfuse-out")
			}
			conn, err := amqp.Dial(ctx, amqp.Config{Addr: b.addr, Username: b.user, Password: b.pass, Timeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			drainQueue(t, conn, in)
			drainQueue(t, conn, out)

			runChannel(t, fmt.Sprintf(`
name: amqp-relay
source:
  type: amqp
  amqp:
    addr: %s
    address: %s
    username: %s
    password: %s
    reconnect: 1s
destinations:
  - name: onward
    type: amqp
    amqp:
      addr: %s
      address: %s
      username: %s
      password: %s
`, b.addr, in, b.user, b.pass, b.addr, out, b.user, b.pass))

			snd, err := conn.NewSender(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if err := snd.Send(ctx, &amqp.Message{Body: []byte(lsADT)}); err != nil {
				t.Fatal(err)
			}
			r, err := conn.NewReceiver(ctx, out, 5)
			if err != nil {
				t.Fatal(err)
			}
			rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			m, err := r.Receive(rctx)
			if err != nil {
				t.Fatalf("nothing reached the destination queue: %v", err)
			}
			_ = m.Accept()
			if string(m.Body) != lsADT || m.Subject != "ADT^A01" || m.ContentType != "application/hl7-v2" || m.MessageID != "C1" {
				t.Errorf("delivered: %+v", m)
			}
			time.Sleep(500 * time.Millisecond)
			if left := drainQueue(t, conn, in); left != 0 {
				t.Errorf("%d message(s) left on the source queue: the channel did not accept what it handled", left)
			}
		})
	}
	if ran == 0 {
		t.Skip("no AMQP 1.0 broker is running; start them with ./scripts/interop-up.sh")
	}
}

func drainQueue(t *testing.T, conn *amqp.Conn, address string) int {
	t.Helper()
	r, err := conn.NewReceiver(context.Background(), address, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	n := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		m, err := r.Receive(ctx)
		cancel()
		if err != nil {
			return n
		}
		_ = m.Accept()
		n++
	}
}

func declareRabbitQueues(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		req, _ := newRequest("PUT", "http://127.0.0.1:15672/api/queues/%2F/"+n, `{"durable":true}`)
		req.SetBasicAuth("perfuse", "perfuse")
		res, err := httpClient.Do(req)
		if err != nil {
			t.Skipf("RabbitMQ's management API is not reachable: %v", err)
		}
		_ = res.Body.Close()
	}
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

func newRequest(method, url, body string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}
