package mllp

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const sampleMsg = "MSH|^~\\&|A|B|C|D|20260818||ADT^A01|1|P|2.5.1\rPID|1||MRN1||Doe^Jane\r"

func framed(msgs ...string) []byte {
	var buf bytes.Buffer
	for _, m := range msgs {
		buf.Write(Frame([]byte(m)))
	}
	return buf.Bytes()
}

func TestReadSingleMessage(t *testing.T) {
	r := NewReader(bytes.NewReader(framed(sampleMsg)), 0)

	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != sampleMsg {
		t.Errorf("payload = %q, want %q", got, sampleMsg)
	}
	// Framing bytes must not appear in the payload.
	if bytes.ContainsAny(got, string([]byte{StartBlock, EndBlock})) {
		t.Error("payload still contains framing bytes")
	}

	if _, err := r.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Errorf("second read err = %v, want io.EOF", err)
	}
}

func TestReadMultipleMessages(t *testing.T) {
	msgs := []string{"MSH|1\r", "MSH|2\r", "MSH|3\r"}
	r := NewReader(bytes.NewReader(framed(msgs...)), 0)

	for i, want := range msgs {
		got, err := r.ReadMessage()
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if string(got) != want {
			t.Errorf("message %d = %q, want %q", i, got, want)
		}
	}
	if r.Discarded() != 0 {
		t.Errorf("Discarded() = %d, want 0 for a clean stream", r.Discarded())
	}
}

func TestResyncPastJunk(t *testing.T) {
	// A peer that emits stray bytes between frames is misbehaving, but dropping
	// a hospital feed over a stray newline is worse than skipping it.
	var buf bytes.Buffer
	buf.WriteString("\r\n garbage \r\n")
	buf.Write(Frame([]byte(sampleMsg)))

	r := NewReader(bytes.NewReader(buf.Bytes()), 0)
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != sampleMsg {
		t.Errorf("payload = %q", got)
	}
	if r.Discarded() == 0 {
		t.Error("Discarded() = 0, want the skipped bytes to be counted")
	}
}

func TestMissingTrailingCarriageReturn(t *testing.T) {
	// Some senders omit the CR after the end block.
	raw := append([]byte{StartBlock}, sampleMsg...)
	raw = append(raw, EndBlock)

	r := NewReader(bytes.NewReader(raw), 0)
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != sampleMsg {
		t.Errorf("payload = %q", got)
	}
}

func TestUnterminatedFrameIsNotAMessage(t *testing.T) {
	// A stream that ends mid-frame must not yield a partial message. Handing
	// half an HL7 message to a clinical system is worse than handing it none.
	raw := append([]byte{StartBlock}, "MSH|^~\\&|A|B"...)

	r := NewReader(bytes.NewReader(raw), 0)
	got, err := r.ReadMessage()
	if err == nil {
		t.Fatalf("ReadMessage returned %q, want an error", got)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if got != nil {
		t.Errorf("payload = %q, want nil", got)
	}
}

func TestStartBlockInsideFrameDiscardsPartial(t *testing.T) {
	// The sender never terminated the first frame. The partial message is
	// unusable, so the new frame wins.
	var buf bytes.Buffer
	buf.WriteByte(StartBlock)
	buf.WriteString("PARTIAL MESSAGE")
	buf.Write(Frame([]byte(sampleMsg)))

	r := NewReader(bytes.NewReader(buf.Bytes()), 0)
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != sampleMsg {
		t.Errorf("payload = %q, want the complete message", got)
	}
	if r.Discarded() == 0 {
		t.Error("the abandoned partial message was not counted as discarded")
	}
}

func TestSizeLimitAndResync(t *testing.T) {
	big := strings.Repeat("X", 5000)
	stream := framed(big, sampleMsg)

	r := NewReader(bytes.NewReader(stream), 1000)

	_, err := r.ReadMessage()
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}

	// The reader must remain usable. An oversized message from one sender should
	// not desynchronise the stream and take out every message behind it.
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read after oversized message: %v", err)
	}
	if string(got) != sampleMsg {
		t.Errorf("payload = %q, want %q", got, sampleMsg)
	}
}

func TestEmptyFrame(t *testing.T) {
	raw := []byte{StartBlock, EndBlock, CarriageReturn}
	r := NewReader(bytes.NewReader(raw), 0)

	if _, err := r.ReadMessage(); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("err = %v, want ErrEmptyMessage", err)
	}
}

func TestWriteMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteMessage([]byte(sampleMsg)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	got := buf.Bytes()
	if got[0] != StartBlock {
		t.Errorf("first byte = %#x, want %#x", got[0], StartBlock)
	}
	if got[len(got)-2] != EndBlock || got[len(got)-1] != CarriageReturn {
		t.Errorf("trailer = %#x, want %#x", got[len(got)-2:], []byte{EndBlock, CarriageReturn})
	}
	if string(got[1:len(got)-2]) != sampleMsg {
		t.Errorf("payload = %q", got[1:len(got)-2])
	}
}

func TestWriteDoesNotDoubleFrame(t *testing.T) {
	// Forwarding a message that came off the wire is the normal case, so an
	// already-framed payload must not gain a second set of framing bytes.
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteMessage(Frame([]byte(sampleMsg))); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if n := bytes.Count(buf.Bytes(), []byte{StartBlock}); n != 1 {
		t.Errorf("found %d start blocks, want 1", n)
	}
	if n := bytes.Count(buf.Bytes(), []byte{EndBlock}); n != 1 {
		t.Errorf("found %d end blocks, want 1", n)
	}
}

func TestWriteEmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteMessage(nil); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("err = %v, want ErrEmptyMessage", err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes for an empty message", buf.Len())
	}
}

func TestRoundTrip(t *testing.T) {
	msgs := []string{
		sampleMsg,
		"MSH|^~\\&|LAB|X|EHR|Y|20260818||ORU^R01|2|P|2.5.1\rOBX|1|NM|GLU^Glucose^LN||95|mg/dL\r",
		"MSH|1\r",
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, m := range msgs {
		if err := w.WriteMessage([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), 0)
	for i, want := range msgs {
		got, err := r.ReadMessage()
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if string(got) != want {
			t.Errorf("message %d = %q, want %q", i, got, want)
		}
	}
}

func TestTrimFraming(t *testing.T) {
	cases := map[string]string{
		"plain":                  "plain",
		"\x0bframed\x1c\r":       "framed",
		"\x0bno trailing cr\x1c": "no trailing cr",
		"\x0bstart only":         "start only",
		"end only\x1c\r":         "end only",
	}
	for in, want := range cases {
		if got := string(TrimFraming([]byte(in))); got != want {
			t.Errorf("TrimFraming(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFramingPreservesFinalSegmentTerminator(t *testing.T) {
	// HL7 terminates every segment with a carriage return, and MLLP's trailer is
	// an end block followed by a carriage return. Stripping a trailing CR
	// without checking for the end block deletes the last segment's terminator
	// and silently changes the message.
	msg := "MSH|^~\\&|A|B|C|D|20260818||ADT^A01|1|P|2.5.1\rPID|1||MRN1\r"

	if got := string(TrimFraming([]byte(msg))); got != msg {
		t.Errorf("TrimFraming altered an unframed message:\n got %q\nwant %q", got, msg)
	}

	out := Frame([]byte(msg))
	if got := string(out[1 : len(out)-2]); got != msg {
		t.Errorf("Frame altered the payload:\n got %q\nwant %q", got, msg)
	}

	r := NewReader(bytes.NewReader(out), 0)
	got, err := r.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != msg {
		t.Errorf("round trip lost the terminator:\n got %q\nwant %q", got, msg)
	}
}

func TestFrameKeepsPayloadIntact(t *testing.T) {
	// Framing must not alter the message. An engine has to be able to forward
	// exactly what it received.
	out := Frame([]byte(sampleMsg))
	if string(out[1:len(out)-2]) != sampleMsg {
		t.Errorf("payload changed during framing: %q", out[1:len(out)-2])
	}
}

func BenchmarkReadMessage(b *testing.B) {
	stream := framed(strings.Repeat(sampleMsg, 8))
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()

	for b.Loop() {
		r := NewReader(bytes.NewReader(stream), 0)
		if _, err := r.ReadMessage(); err != nil {
			b.Fatal(err)
		}
	}
}
