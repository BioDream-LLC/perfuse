package engine

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/gcp"
	"github.com/biodream-llc/perfuse/internal/ibmmq"
	"github.com/biodream-llc/perfuse/internal/mqtt"
)

func listening(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Skipf("nothing at %s; start it with ./scripts/interop-up.sh", addr)
	}
	_ = c.Close()
}

// TestAnMQTTChannelRelaysBetweenTopicsOnMosquitto runs a channel from one topic to another on a real broker.
func TestAnMQTTChannelRelaysBetweenTopicsOnMosquitto(t *testing.T) {
	const addr = "127.0.0.1:1883"
	listening(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n := time.Now().UnixNano()
	in, out := fmt.Sprintf("perfuse/e2e/%d/in", n), fmt.Sprintf("perfuse/e2e/%d/out", n)

	watch, err := mqtt.Dial(ctx, mqtt.Config{Addr: addr, ClientID: fmt.Sprintf("watch-%d", n), Username: "perfuse", Password: "perfuse", CleanSession: true})
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if err := watch.Subscribe(ctx, out, 1); err != nil {
		t.Fatal(err)
	}

	runChannel(t, fmt.Sprintf(`
name: mqtt-relay-%d
source:
  type: mqtt
  mqtt:
    addr: %s
    topic: %s
    username: perfuse
    password: perfuse
    reconnect: 1s
destinations:
  - name: onward
    type: mqtt
    mqtt:
      addr: %s
      topic: %s
      username: perfuse
      password: perfuse
`, n, addr, in, addr, out))
	time.Sleep(time.Second) // the source subscribes

	if err := watch.Publish(ctx, in, []byte(lsADT), 1, false); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-watch.Messages():
		_ = m.Ack()
		if string(m.Payload) != lsADT {
			t.Errorf("delivered %q", m.Payload)
		}
	case <-ctx.Done():
		t.Fatal("nothing reached the destination topic")
	}
}

// TestAPubSubChannelRelaysBetweenTopicsOnTheEmulator runs a channel from a subscription to a topic on Google's emulator.
func TestAPubSubChannelRelaysBetweenTopicsOnTheEmulator(t *testing.T) {
	listening(t, "127.0.0.1:8085")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ps, _ := gcp.NewPubSub("http://127.0.0.1:8085", "perfuse-test", nil)
	n := time.Now().UnixNano()
	inT, inS, outT, outS := fmt.Sprintf("in%d", n), fmt.Sprintf("ins%d", n), fmt.Sprintf("out%d", n), fmt.Sprintf("outs%d", n)
	for _, p := range [][2]string{{inT, inS}, {outT, outS}} {
		if err := ps.CreateTopic(ctx, p[0]); err != nil {
			t.Fatal(err)
		}
		if err := ps.CreateSubscription(ctx, p[1], p[0], true); err != nil {
			t.Fatal(err)
		}
	}
	runChannel(t, fmt.Sprintf(`
name: pubsub-relay-%d
source:
  type: pubsub
  pubsub:
    endpoint: http://127.0.0.1:8085
    project: perfuse-test
    subscription: %s
destinations:
  - name: onward
    type: pubsub
    pubsub:
      endpoint: http://127.0.0.1:8085
      project: perfuse-test
      topic: %s
      order_by: patient
`, n, inS, outT))
	if _, err := ps.Publish(ctx, inT, gcp.Message{Data: []byte(lsADT)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		msgs, err := ps.Pull(ctx, outS, 1, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) == 1 {
			m := msgs[0]
			if string(m.Data) != lsADT || m.Attributes["hl7_message_type"] != "ADT^A01" || m.OrderingKey == "" {
				t.Errorf("delivered %+v", m)
			}
			_ = ps.Ack(ctx, outS, m.AckID)
			// The source acknowledged what it handled, so nothing is left on its subscription.
			if left, _ := ps.Pull(ctx, inS, 10, 2*time.Second); len(left) != 0 {
				t.Errorf("%d message(s) left on the source subscription", len(left))
			}
			return
		}
	}
	t.Fatal("nothing reached the destination topic")
}

// TestAGCSChannelMovesObjectsBetweenPrefixes runs a channel from one prefix to another on fake-gcs-server.
func TestAGCSChannelMovesObjectsBetweenPrefixes(t *testing.T) {
	listening(t, "127.0.0.1:4443")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bucket := fmt.Sprintf("e2e%d", time.Now().UnixNano())
	st, _ := gcp.NewStorage("http://127.0.0.1:4443", bucket, "", nil)
	if err := st.CreateBucket(ctx, "perfuse-test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "inbound/one.hl7", []byte(lsADT), ""); err != nil {
		t.Fatal(err)
	}
	runChannel(t, fmt.Sprintf(`
name: gcs-relay
source:
  type: gcs
  gcs:
    endpoint: http://127.0.0.1:4443
    bucket: %s
    prefix: inbound/
    poll_interval: 1s
destinations:
  - name: onward
    type: gcs
    gcs:
      endpoint: http://127.0.0.1:4443
      bucket: %s
      object: outbound/${control_id}.hl7
`, bucket, bucket))
	for i := 0; i < 30; i++ {
		time.Sleep(500 * time.Millisecond)
		out, _, _ := st.List(ctx, "outbound/", "", 10)
		done, _, _ := st.List(ctx, "processed/", "", 10)
		left, _, _ := st.List(ctx, "inbound/", "", 10)
		if len(out) == 1 && len(done) == 1 && len(left) == 0 {
			got, err := st.Get(ctx, out[0].Name, 1<<20)
			if err != nil || string(got) != lsADT || !strings.HasSuffix(out[0].Name, "C1.hl7") {
				t.Errorf("delivered %s: %q %v", out[0].Name, got, err)
			}
			return
		}
	}
	t.Fatal("the object was not delivered and moved to processed/")
}

// TestAnIBMMQChannelMovesAMessageBetweenQueues runs a channel from DEV.QUEUE.1 to DEV.QUEUE.3 on the developer container, over
// TLS checked against the container's own certificate.
func TestAnIBMMQChannelMovesAMessageBetweenQueues(t *testing.T) {
	listening(t, "127.0.0.1:9443")
	// 9443 is a common port, so something listening there is not proof of a queue manager: ask its REST API first.
	probe := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if resp, err := probe.Get("https://127.0.0.1:9443/ibmmq/rest/v3/login"); err != nil {
		t.Skipf("127.0.0.1:9443 is not IBM MQ's REST API (%v); start it with ./scripts/interop-up.sh", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Skip("127.0.0.1:9443 answers but is not IBM MQ's REST API (another server holds the port)")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := tls.Dial("tcp", "127.0.0.1:9443", &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "mq.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: conn.ConnectionState().PeerCertificates[0].Raw}), 0o600)
	_ = conn.Close()
	q := func(name string) *ibmmq.Client {
		c, _ := ibmmq.New(ibmmq.Config{BaseURL: "https://127.0.0.1:9443", QueueManager: "QM1", Queue: name, Username: "app",
			Password: "passw0rd", TLS: &tls.Config{InsecureSkipVerify: true}})
		for m, _ := c.Get(ctx, 0); m != nil; m, _ = c.Get(ctx, 0) {
		}
		return c
	}
	in, out := q("DEV.QUEUE.1"), q("DEV.QUEUE.3")
	if err := in.Put(ctx, []byte(lsADT), ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERFUSE_TEST_MQ_PASSWORD", "passw0rd")
	runChannel(t, fmt.Sprintf(`
name: ibmmq-relay
source:
  type: ibm_mq
  ibm_mq:
    url: https://localhost:9443
    queue_manager: QM1
    queue: DEV.QUEUE.1
    username: app
    password: ${PERFUSE_TEST_MQ_PASSWORD}
    poll_interval: 200ms
    tls: {enabled: true, ca_file: %s}
destinations:
  - name: onward
    type: ibm_mq
    ibm_mq:
      url: https://localhost:9443
      queue_manager: QM1
      queue: DEV.QUEUE.3
      username: app
      password: ${PERFUSE_TEST_MQ_PASSWORD}
      tls: {enabled: true, ca_file: %s}
`, ca, ca))
	m, err := out.Get(ctx, 15*time.Second)
	if err != nil || m == nil || string(m.Body) != lsADT {
		t.Fatalf("delivered %+v %v", m, err)
	}
	time.Sleep(time.Second) // the source removes it once the destination has it
	if left, _ := in.Browse(ctx); left != nil {
		t.Error("the message was left on the source queue")
	}
}
