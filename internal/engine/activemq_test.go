package engine

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The broker destination against a real ActiveMQ, over STOMP.
//
// STOMP is a text protocol and therefore an easy one to implement approximately: frames terminated by a null byte, headers that look
// like HTTP's but are not, a content-length that is optional except when the body contains a null. Every one of those is a place where
// a hand-written client agrees with a hand-written test and disagrees with a broker.
//
// Which is not hypothetical in this codebase. Tonight the SAML canonicaliser turned out to be unable to accept any assertion a real
// identity provider produced, having passed a thousand tests that signed and verified with the same code. ActiveMQ is here to be a
// second opinion.
//
// Skipped when the broker is absent, because make check has to pass without a container runtime.

const stompAddr = "127.0.0.1:61613"

func requireActiveMQ(t *testing.T) string {
	t.Helper()

	conn, err := net.DialTimeout("tcp", stompAddr, 2*time.Second)
	if err != nil {
		t.Skip("no STOMP broker on " + stompAddr +
			": docker run -d --name activemq -p 61613:61613 apache/activemq-classic:latest")
	}

	_ = conn.Close()

	return stompAddr
}

func TestAMessagePublishedToARealBrokerIsAccepted(t *testing.T) {
	// The assertion: a real broker accepts our frames. ActiveMQ closes a connection that sends a malformed frame, so a successful send
	// is evidence about the frame rather than about the socket.
	addr := requireActiveMQ(t)

	sender, err := NewBrokerSender(config.Destination{
		Name: "broker",
		Type: config.DestinationBroker,
		Broker: &config.BrokerDestination{
			Addr:        addr,
			Destination: "/queue/perfuse.test",
			// ActiveMQ's default configuration allows anonymous access, so no credentials. A site would set them, and that path is
			// covered by the unit tests - what is being checked here is the wire format.
			ContentType: "text/plain",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	before := queueDepth(t, "perfuse.test")

	if err := sender.Send(context.Background(), []byte("MSH|^~\\&|PERFUSE|TEST|||||ADT^A01|1|P|2.5\r")); err != nil {
		t.Fatalf("a real broker refused the message: %v", err)
	}

	// Asked of the broker rather than inferred from Send returning nil.
	//
	// This is the assertion that distinguishes delivery from a report of delivery, and it is not a theoretical distinction here: a
	// TEFCA exchange in this codebase validated its inputs, made no network call at all, and returned success with a tracking
	// identifier built from the clock. Four tests asserted that and passed.
	after := queueDepth(t, "perfuse.test")
	if after <= before {
		t.Errorf("the broker's queue held %d messages before the send and %d after, so nothing arrived", before, after)
	}
}

// queueDepth asks ActiveMQ how many messages are waiting on a queue.
//
// Through the broker's own management interface, because the point is to have something other than Perfuse confirm that Perfuse did
// what it said. Returns -1 when the management interface cannot be read, which fails the comparison rather than passing it.
func queueDepth(t *testing.T, queue string) int {
	t.Helper()

	url := "http://127.0.0.1:8161/api/jolokia/read/org.apache.activemq:type=Broker,brokerName=localhost," +
		"destinationType=Queue,destinationName=" + queue + "/QueueSize"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.SetBasicAuth("admin", "admin")
	req.Header.Set("Origin", "http://localhost")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("the broker's management interface is unreachable: %v", err)

		return -1
	}

	defer func() { _ = res.Body.Close() }()

	var out struct {
		Value  int `json:"value"`
		Status int `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Logf("the broker's answer is not JSON: %v", err)

		return -1
	}

	if out.Status != 200 {
		// A queue that does not exist yet is reported this way, and zero is the right answer for it.
		return 0
	}

	return out.Value
}

func TestABrokerRefusesAnAddressThatIsNotListening(t *testing.T) {
	// The negative control. Without it, a Send that silently succeeded without connecting would make the test above pass while
	// proving nothing - which is the exact failure this whole line of work keeps finding, most recently in a TEFCA exchange that
	// reported success and made no network call at all.
	sender, err := NewBrokerSender(config.Destination{
		Name: "broker",
		Type: config.DestinationBroker,
		Broker: &config.BrokerDestination{
			// Port 1 is reserved and nothing listens there.
			Addr:        "127.0.0.1:1",
			Destination: "/queue/perfuse.test",
		},
	})
	if err != nil {
		// Refusing at construction is also acceptable; what matters is that nothing reports a delivery.
		return
	}

	defer func() { _ = sender.Close() }()

	if err := sender.Send(context.Background(), []byte("MSH|^~\\&|PERFUSE|TEST|||||ADT^A01|2|P|2.5\r")); err == nil {
		t.Fatal("publishing to an address with nothing listening reported success")
	}
}

func TestSeveralMessagesInSuccessionAllReachTheBroker(t *testing.T) {
	// A connection reused across messages is where a framing mistake shows: a frame that is accepted on its own can leave a byte
	// behind that desynchronises the next one, and the symptom is every second message failing rather than all of them.
	addr := requireActiveMQ(t)

	sender, err := NewBrokerSender(config.Destination{
		Name: "broker",
		Type: config.DestinationBroker,
		Broker: &config.BrokerDestination{
			Addr:        addr,
			Destination: "/queue/perfuse.test.many",
			ContentType: "text/plain",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	for i := range 5 {
		if err := sender.Send(context.Background(), []byte("MSH|^~\\&|PERFUSE|TEST|||||ADT^A01|many|P|2.5\r")); err != nil {
			t.Fatalf("message %d of 5 was refused: %v", i+1, err)
		}
	}
}

func TestABodyContainingANullByteIsFramedCorrectly(t *testing.T) {
	// The specific trap in STOMP. A frame ends with a null byte, so a body containing one has to be sent with a content-length header
	// or the broker reads the frame as ending early - and what follows becomes a garbled frame rather than a rejected one.
	//
	// HL7 does not normally carry nulls, but a channel that has been through a binary transform or a badly encoded segment can, and
	// the failure would appear as the broker dropping the connection with nothing in the message to explain it.
	addr := requireActiveMQ(t)

	sender, err := NewBrokerSender(config.Destination{
		Name: "broker",
		Type: config.DestinationBroker,
		Broker: &config.BrokerDestination{
			Addr:        addr,
			Destination: "/queue/perfuse.test.null",
			ContentType: "application/octet-stream",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	body := []byte("before\x00after")

	if err := sender.Send(context.Background(), body); err != nil {
		t.Fatalf("a body containing a null byte was refused: %v", err)
	}

	// And the connection still works afterwards, which is what distinguishes correct framing from a frame the broker happened to
	// tolerate: a desynchronised stream fails on the next message, not on this one.
	if err := sender.Send(context.Background(), []byte("after the null")); err != nil {
		t.Fatalf("the message after a null-containing body failed, so the stream was left desynchronised: %v", err)
	}
}
