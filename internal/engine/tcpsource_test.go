package engine

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A raw socket with framing that is not MLLP.
//
// Mirth's TCP Listener and TCP Sender. What this is for is equipment that speaks a socket but not HL7: an analyser, a
// scale, a bedside monitor, an older billing system. Every test here connects a real socket and asserts on what came out
// the other end, because framing errors produce plausible-looking messages rather than failures.

// tcpHarness runs a channel with a tcp source on an arbitrary port.
type tcpHarness struct {
	t    *testing.T
	e    *Engine
	sink *recordingSender
	addr string
}

func newTCPHarness(t *testing.T, sourceBlock string) *tcpHarness {
	t.Helper()

	yaml := fmt.Sprintf(`
name: device
dataType: raw
source:
  type: tcp
  tcp:
    listen: "127.0.0.1:0"
%s
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, sourceBlock)

	cfg, err := config.Load(strings.NewReader(yaml), "device.yaml")
	if err != nil {
		t.Fatalf("loading the config: %v", err)
	}

	sink := &recordingSender{name: "out"}
	e, err := New([]*config.Channel{cfg}, func(config.Destination) (Sender, error) { return sink, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Stop(ctx)
	})

	// The actual bound address, since the port was zero. Reading it from the listener rather than guessing is what lets
	// these tests run in parallel with everything else.
	var addr string
	for _, ch := range e.Channels() {
		if ch.tcpSrc != nil {
			addr = ch.tcpSrc.Addr()
		}
	}
	if addr == "" {
		t.Fatal("the channel started but no tcp listener was bound")
	}

	return &tcpHarness{t: t, e: e, sink: sink, addr: addr}
}

func (h *tcpHarness) dial() net.Conn {
	h.t.Helper()
	conn, err := net.DialTimeout("tcp", h.addr, 5*time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (h *tcpHarness) waitFor(n int) [][]byte {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.sink.all(); len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("only %d of %d messages arrived", len(h.sink.all()), n)
	return nil
}

// A device sending records ended by a control character must be understood.
//
// The commonest non-HL7 socket feed there is: STX, payload, ETX.
func TestADeviceUsingControlCharactersIsUnderstood(t *testing.T) {
	h := newTCPHarness(t, `    framing: delimited
    start_block: "\x02"
    delimiter: "\x03"`)

	conn := h.dial()
	if _, err := conn.Write([]byte("\x02R|1|Hemoglobin|13.5|g/dL\x03\x02R|2|Leukocytes|14.2|10*3/uL\x03")); err != nil {
		t.Fatal(err)
	}

	got := h.waitFor(2)

	// The framing bytes must not arrive as content. A downstream parser that receives them usually reports a trailing
	// empty field rather than the control character, which sends somebody looking in the wrong place.
	if string(got[0]) != "R|1|Hemoglobin|13.5|g/dL" {
		t.Errorf("first message is %q; framing bytes were kept or the boundary was wrong", got[0])
	}
	if string(got[1]) != "R|2|Leukocytes|14.2|10*3/uL" {
		t.Errorf("second message is %q", got[1])
	}
}

// Two messages sent in one write must not arrive as one.
//
// A socket does not preserve write boundaries, so the reader cannot use them. If it did, a peer that batched its writes
// would silently deliver joined messages.
func TestTwoMessagesInOneWriteArriveSeparately(t *testing.T) {
	h := newTCPHarness(t, `    framing: delimited
    delimiter: "\r"`)

	conn := h.dial()
	if _, err := conn.Write([]byte("first\rsecond\rthird\r")); err != nil {
		t.Fatal(err)
	}

	got := h.waitFor(3)
	for i, want := range []string{"first", "second", "third"} {
		if string(got[i]) != want {
			t.Errorf("message %d is %q, want %q", i, got[i], want)
		}
	}
}

// One message split across several writes must arrive whole.
//
// The other half of the same problem. A device writing a byte at a time is unusual but a device whose message spans two
// TCP segments is routine, and a reader that treated each read as a message would deliver halves.
func TestOneMessageSplitAcrossWritesArrivesWhole(t *testing.T) {
	h := newTCPHarness(t, `    framing: delimited
    delimiter: "\r"`)

	conn := h.dial()
	for _, part := range []string{"a message ", "split into ", "three writes\r"} {
		if _, err := conn.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
	}

	got := h.waitFor(1)
	if string(got[0]) != "a message split into three writes" {
		t.Errorf("got %q; the message was cut at a write boundary", got[0])
	}
}

// A length-prefixed stream must be read using the configured byte order.
func TestALengthPrefixedDeviceIsRead(t *testing.T) {
	h := newTCPHarness(t, `    framing: length
    length_bytes: 2
    big_endian: true`)

	conn := h.dial()
	payload := "OBX|1|NM|718-7|13.5"
	header := []byte{byte(len(payload) >> 8), byte(len(payload))}

	if _, err := conn.Write(append(header, []byte(payload)...)); err != nil {
		t.Fatal(err)
	}

	got := h.waitFor(1)
	if string(got[0]) != payload {
		t.Errorf("got %q, want %q", got[0], payload)
	}
}

// Fixed-length records must be split at exactly the record length.
func TestFixedLengthRecordsAreSplitAtTheRecordLength(t *testing.T) {
	h := newTCPHarness(t, `    framing: fixed
    record_length: 10`)

	conn := h.dial()
	if _, err := conn.Write([]byte("AAAA000001BBBB000002")); err != nil {
		t.Fatal(err)
	}

	got := h.waitFor(2)
	if string(got[0]) != "AAAA000001" || string(got[1]) != "BBBB000002" {
		t.Errorf("records arrived as %q", got)
	}
}

// A device that expects an acknowledgement byte must receive one.
//
// A device waiting for a reply that never comes usually retries the same message forever, which at the receiving end
// looks like a duplicate storm rather than a missing reply.
func TestADeviceExpectingAnAcknowledgementReceivesOne(t *testing.T) {
	h := newTCPHarness(t, `    framing: delimited
    delimiter: "\r"
    reply: ack`)

	conn := h.dial()
	if _, err := conn.Write([]byte("a record\r")); err != nil {
		t.Fatal(err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no reply arrived: %v", err)
	}
	if n != 1 || buf[0] != 0x06 {
		t.Errorf("the reply was %q, want a single ACK byte", buf[:n])
	}
}

// Perfuse sending to Perfuse must round-trip.
//
// Framing and reading are two halves of one agreement, and a mismatch between them is only visible at the far end. This
// is the cheapest way to be sure the sender and the listener agree.
func TestTheTCPSenderAndListenerAgree(t *testing.T) {
	h := newTCPHarness(t, `    framing: delimited
    start_block: "\x02"
    delimiter: "\x03"`)

	sender, err := NewTCPSender(config.TCPDest{
		Address: h.addr,
		TCPFraming: config.TCPFraming{
			Framing:    "delimited",
			StartBlock: `\x02`,
			Delimiter:  `\x03`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	msg := []byte("R|1|Glucose|5.4|mmol/L")
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("sending: %v", err)
	}

	got := h.waitFor(1)
	if string(got[0]) != string(msg) {
		t.Errorf("round trip changed the message: sent %q, received %q", msg, got[0])
	}
}

// A framing that cannot express the message must be refused, not truncated.
func TestASenderRefusesAMessageTooLargeForItsFraming(t *testing.T) {
	sender, err := NewTCPSender(config.TCPDest{
		Address:    "127.0.0.1:1",
		TCPFraming: config.TCPFraming{Framing: "fixed", RecordLength: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	err = sender.Send(context.Background(), []byte("far too long for an eight byte record"))
	if err == nil {
		t.Fatal("an oversize message was accepted for a fixed-length destination")
	}
	// And it must not have been sent. The address is deliberately unroutable, so a connection error here would mean the
	// framing check did not run first.
	if strings.Contains(err.Error(), "connecting") {
		t.Errorf("the sender dialled before checking the framing, so an impossible message caused a connection "+
			"attempt: %v", err)
	}
}

// A source with no framing must be refused at load.
//
// There is deliberately no default. Reading a stream with the wrong framing produces messages that look plausible rather
// than an error, so guessing is worse than asking.
func TestATCPSourceWithNoFramingIsRefused(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: nf
source:
  type: tcp
  tcp:
    listen: "127.0.0.1:0"
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "nf.yaml")

	if err == nil {
		t.Fatal("a tcp source with no framing was accepted")
	}
	if !strings.Contains(err.Error(), "plausible") {
		t.Errorf("the refusal does not explain why there is no default: %v", err)
	}
}

// A setting that has no effect must be reported rather than ignored.
//
// A record_length on a delimited stream means somebody believed one of the two was taking effect. Silence leaves them
// believing it, and the channel works in a way they did not intend.
func TestASettingThatHasNoEffectIsReported(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: mixed
dataType: raw
source:
  type: tcp
  tcp:
    listen: "127.0.0.1:0"
    framing: delimited
    delimiter: "\r"
    record_length: 80
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "mixed.yaml")

	if err == nil {
		t.Fatal("a record_length on a delimited stream was accepted silently")
	}
	if !strings.Contains(err.Error(), "no effect") {
		t.Errorf("the error does not say the setting is being ignored: %v", err)
	}
}

// Whole-stream framing with a reply must be refused, because the two cannot both happen.
func TestWholeStreamFramingWithAReplyIsRefused(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: whole
dataType: raw
source:
  type: tcp
  tcp:
    listen: "127.0.0.1:0"
    framing: whole
    reply: ack
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "whole.yaml")

	if err == nil {
		t.Fatal("whole-stream framing with a reply was accepted")
	}
	if !strings.Contains(err.Error(), "no open connection left") {
		t.Errorf("the refusal does not explain the contradiction: %v", err)
	}
}
