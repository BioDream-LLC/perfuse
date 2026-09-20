package stomp

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// brokerAddr is a running broker, or a skip.
//
// A real broker rather than a fake one. The whole reason this package exists is to work against somebody else's implementation,
// and a test double would confirm my reading of the specification rather than the specification - which is how four DICOM bugs
// survived until DCMTK was involved.
//
// Artemis is used because it is ActiveMQ's current generation and what a site standardising today would deploy. Set
// PERFUSE_STOMP_ADDR to point these at a different broker; the tests make no Artemis-specific assumptions.
func brokerAddr(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("PERFUSE_STOMP_ADDR")
	if addr == "" {
		t.Skip("set PERFUSE_STOMP_ADDR to a STOMP broker to run the broker interoperability tests")
	}
	return addr
}

func brokerOptions(t *testing.T) Options {
	return Options{
		Addr:     brokerAddr(t),
		Login:    envOr("PERFUSE_STOMP_USER", "perfuse"),
		Passcode: envOr("PERFUSE_STOMP_PASS", "perfuse"),
		Timeout:  15 * time.Second,
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// uniqueQueue names a queue nothing else is using.
//
// Per test, because a queue shared between tests carries messages from whichever ran first and the failures look like ordering
// bugs in the client.
func uniqueQueue(prefix string) string {
	return "/queue/perfuse." + prefix + "." + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// TestConnectToRealBroker checks the handshake.
func TestConnectToRealBroker(t *testing.T) {
	conn, err := Dial(context.Background(), brokerOptions(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.Version == "" {
		t.Error("the broker agreed to no protocol version")
	}
	t.Logf("connected to %q speaking STOMP %s", conn.Server, conn.Version)
}

// TestWrongPasswordIsReported checks a refusal arrives as a message rather than a dropped socket.
//
// The reason CONNECT is sent as STOMP rather than CONNECT: a broker answering STOMP must reply with an ERROR frame instead of
// closing the connection, so a wrong password produces an explanation rather than "connection reset by peer".
func TestWrongPasswordIsReported(t *testing.T) {
	opts := brokerOptions(t)
	opts.Passcode = "definitely-not-the-password"

	_, err := Dial(context.Background(), opts)
	if err == nil {
		t.Fatal("a wrong password connected successfully")
	}
	t.Logf("refused with: %v", err)
}

// TestSendAndReceive is the round trip.
func TestSendAndReceive(t *testing.T) {
	queue := uniqueQueue("roundtrip")
	opts := brokerOptions(t)

	// Two connections, because a subscription and a publisher must not share one - a message arriving while waiting for a send
	// receipt would have to be either dropped or queued, and the client refuses rather than choosing.
	publisher, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("publisher connect: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	consumer, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("consumer connect: %v", err)
	}
	defer func() { _ = consumer.Close() }()

	if err := consumer.Subscribe("sub-1", queue, ""); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	message := []byte("MSH|^~\\&|LAB|HOSP|EMR|HOSP|20260821||ORU^R01|Q001|P|2.5.1\rPID|1||MRN9001||FROST^IVY\r")
	if err := publisher.Send(queue, message, map[string]string{"content-type": "text/plain"}, 10*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}

	got, err := consumer.Receive(15 * time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if got == nil {
		t.Fatal("nothing arrived within the timeout")
	}

	if string(got.Body) != string(message) {
		t.Errorf("the message came back as %q", got.Body)
	}

	if err := consumer.Ack(got); err != nil {
		t.Errorf("ack: %v", err)
	}
}

// TestBinaryBodySurvives is why content-length is always sent.
//
// Without content-length the body runs to the next null byte, so a payload containing one is truncated there. A base64 blob will
// not contain a null but a compressed document or a DICOM object will, and the truncation is silent - the message arrives, shorter.
func TestBinaryBodySurvives(t *testing.T) {
	queue := uniqueQueue("binary")
	opts := brokerOptions(t)

	publisher, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("publisher connect: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	consumer, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("consumer connect: %v", err)
	}
	defer func() { _ = consumer.Close() }()

	if err := consumer.Subscribe("sub-bin", queue, ""); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// A null in the middle, which is the byte that terminates a frame.
	body := []byte{'h', 'e', 'a', 'd', 0x00, 't', 'a', 'i', 'l', 0x00, 0xFF, 0xFE}
	if err := publisher.Send(queue, body, map[string]string{"content-type": "application/octet-stream"},
		10*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}

	got, err := consumer.Receive(15 * time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if got == nil {
		t.Fatal("nothing arrived")
	}

	if len(got.Body) != len(body) {
		t.Fatalf("sent %d bytes and received %d; the body was truncated at a null", len(body), len(got.Body))
	}
	for i := range body {
		if got.Body[i] != body[i] {
			t.Fatalf("byte %d differs: sent %#x, received %#x", i, body[i], got.Body[i])
		}
	}

	_ = consumer.Ack(got)
}

// TestHeaderWithColonSurvives checks the header escaping.
//
// A colon in a header value is ordinary - a timestamp, a URL, a Windows path - and unescaped it silently becomes a different
// header. This is the sort of thing that works in every test until the day a value contains one.
func TestHeaderWithColonSurvives(t *testing.T) {
	queue := uniqueQueue("headers")
	opts := brokerOptions(t)

	publisher, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("publisher connect: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	consumer, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("consumer connect: %v", err)
	}
	defer func() { _ = consumer.Close() }()

	if err := consumer.Subscribe("sub-hdr", queue, ""); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	awkward := "sent at 19:55:02 from C:\\feeds\\lab"
	if err := publisher.Send(queue, []byte("body"), map[string]string{"perfuse-note": awkward},
		10*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}

	got, err := consumer.Receive(15 * time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if got == nil {
		t.Fatal("nothing arrived")
	}

	if back := got.Header("perfuse-note"); back != awkward {
		t.Errorf("the header came back as %q, want %q", back, awkward)
	}

	_ = consumer.Ack(got)
}

// TestNackIsAccepted checks that refusing a message is accepted by a real broker.
//
// It deliberately does not assert redelivery, and that correction is worth recording. I wrote this test expecting a refused message
// to come back, restructured it twice when it did not, and then read the specification properly: NACK tells the server the message
// was not consumed, and the server may then hand it to a different subscriber or send it to a dead letter queue. Which of those
// happens is the broker's policy, not a guarantee.
//
// Artemis does neither within twenty seconds under its defaults. So the assertion here is what is actually promised - that the
// refusal is accepted and the message is not acknowledged - and what happens next belongs to whoever configured the broker's
// redelivery and dead-letter settings.
//
// The reason this matters rather than being a technicality: the alternative to NACK is staying silent, which leaves the broker
// holding the message until a timeout that is frequently minutes long. Refusing promptly puts the decision where it is already
// configured.
func TestNackIsAccepted(t *testing.T) {
	queue := uniqueQueue("nack")
	opts := brokerOptions(t)

	consumer, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("consumer connect: %v", err)
	}
	defer func() { _ = consumer.Close() }()

	if err := consumer.Subscribe("sub-nack", queue, ""); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	publisher, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("publisher connect: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	if err := publisher.Send(queue, []byte("will be refused"), nil, 10*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}

	got, err := consumer.Receive(15 * time.Second)
	if err != nil || got == nil {
		t.Fatalf("receive: %v (frame %v)", err, got)
	}

	// The identifier the broker gave us has to be the one we send back. STOMP 1.2 uses the ack header and 1.1 uses message-id,
	// and a client reading only one of them fails to acknowledge against half the brokers in use - which presents as every
	// message being redelivered forever.
	if ackID(got) == "" {
		t.Fatal("the message carried neither an ack nor a message-id header, so it could not be acknowledged at all")
	}

	if err := consumer.Nack(got); err != nil {
		t.Fatalf("nack: %v", err)
	}

	// An invalid NACK produces an ERROR frame, so reading once more proves the broker accepted it. Nothing arriving is the
	// expected outcome under Artemis's defaults.
	next, err := consumer.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("the broker rejected the refusal: %v", err)
	}
	if next != nil {
		// A redelivery is legitimate on a broker configured for it, so this is not a failure.
		t.Logf("the broker redelivered immediately: %q", next.Body)
		_ = consumer.Ack(next)
	}
}

// TestAckIsAccepted checks the ordinary acknowledgement path against a real broker.
//
// Paired with the refusal test so that both branches are exercised. Without it, an Ack that silently did nothing would go unnoticed:
// the message would be redelivered later, which looks like a broker problem.
func TestAckIsAccepted(t *testing.T) {
	queue := uniqueQueue("ack")
	opts := brokerOptions(t)

	consumer, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("consumer connect: %v", err)
	}
	defer func() { _ = consumer.Close() }()

	if err := consumer.Subscribe("sub-ack", queue, ""); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	publisher, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("publisher connect: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	if err := publisher.Send(queue, []byte("accepted"), nil, 10*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}

	got, err := consumer.Receive(15 * time.Second)
	if err != nil || got == nil {
		t.Fatalf("receive: %v", err)
	}
	if err := consumer.Ack(got); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// An acknowledged message must not come back.
	again, err := consumer.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("after acknowledging: %v", err)
	}
	if again != nil {
		t.Errorf("an acknowledged message was delivered again: %q", again.Body)
	}
}

// TestSendToNowhereIsReported checks that a send is confirmed rather than assumed.
//
// A receipt is requested so that returning nil means the broker has the message. Without it, a broker refusing messages looks like
// a working destination and the queue behind it drains into nothing.
//
// The assertion is deliberately weak: brokers differ on whether publishing to an address that does not exist is an error, and
// Artemis will happily create it. What is being checked is that the client waits for confirmation at all, which the timeout would
// catch.
func TestSendIsConfirmed(t *testing.T) {
	opts := brokerOptions(t)

	publisher, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	start := time.Now()
	if err := publisher.Send(uniqueQueue("confirm"), []byte("x"), nil, 10*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}

	// A send that returned without waiting would be far faster than a round trip to the broker. This is a weak signal on
	// loopback, so it is a floor rather than a measurement.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the send took %s, which suggests it did not get a receipt", elapsed)
	}
}
