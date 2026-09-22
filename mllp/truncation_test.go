package mllp

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// A frame abandoned part way through has to say how far it got.
//
// The refusal itself was already right: a truncated frame is never returned as a
// message, because the segments before the cut are complete and well formed and a
// receiver has no way to know the ones carrying the actual results never arrived.
//
// What was missing was the number, and only on the paths that matter most. The byte
// count was reported when the peer closed tidily and omitted for every other read
// failure - which is to say, omitted for a sender that crashed, a process that was
// killed, and a cable that was pulled. Those connections logged messages=0 and
// nothing else, so an operator could not tell an empty health check from a lab result
// thrown away nine tenths of the way through.

// halfThenError yields some bytes and then fails, the way a reset connection does.
type halfThenError struct {
	data []byte
	pos  int
	err  error
}

func (h *halfThenError) Read(p []byte) (int, error) {
	if h.pos >= len(h.data) {
		return 0, h.err
	}
	n := copy(p, h.data[h.pos:])
	h.pos += n
	return n, nil
}

func TestATruncatedFrameIsNeverReturnedAsAMessage(t *testing.T) {
	partial := append([]byte{StartBlock}, []byte("MSH|^~\\&|A|B|C|D|20260101||ORU^R01|1|P|2.5\rOBX|1|NM|GLU")...)

	for name, failure := range map[string]error{
		"clean close":      io.EOF,
		"connection reset": errors.New("read: connection reset by peer"),
		"read timeout":     errors.New("i/o timeout"),
		"broken pipe":      errors.New("write: broken pipe"),
	} {
		r := NewReader(&halfThenError{data: partial, err: failure}, 0)
		msg, err := r.ReadMessage()

		if err == nil {
			t.Errorf("%s: a truncated frame was returned as a message (%q). The segments before "+
				"the cut parse, so a receiver cannot tell the results are missing", name, msg)
			continue
		}
		if msg != nil {
			t.Errorf("%s: an error was returned alongside %d bytes of payload; callers that check "+
				"the payload first would deliver half a message", name, len(msg))
		}
	}
}

func TestATruncatedFrameReportsHowFarItGot(t *testing.T) {
	body := "MSH|^~\\&|A|B|C|D|20260101||ORU^R01|1|P|2.5\rOBX|1|NM|GLU"
	partial := append([]byte{StartBlock}, []byte(body)...)

	for name, failure := range map[string]error{
		"clean close":      io.EOF,
		"connection reset": errors.New("read: connection reset by peer"),
		"read timeout":     errors.New("i/o timeout"),
	} {
		r := NewReader(&halfThenError{data: partial, err: failure}, 0)
		_, err := r.ReadMessage()
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		want := fmt.Sprintf("%d bytes into a frame", len(body))
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: the error does not say how far the frame got. Wanted %q, got %q. "+
				"Without it the connection log says messages=0 and an operator cannot tell "+
				"an empty connection from an abandoned message", name, want, err)
		}
	}
}

func TestACleanCloseMidFrameIsNotMistakenForATidyOne(t *testing.T) {
	// The server logs "closed by peer" for a bare EOF and "read failed" otherwise. A
	// frame cut short by EOF must take the second path, or a sender dying mid-message
	// is recorded as a normal disconnection.
	partial := append([]byte{StartBlock}, []byte("MSH|^~\\&|A")...)
	r := NewReader(&halfThenError{data: partial, err: io.EOF}, 0)
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, io.EOF) {
		t.Error("a frame truncated by EOF reports as a plain EOF, which the server logs as the " +
			"peer closing tidily between messages. A sender that died mid-message would be " +
			"recorded as a normal disconnection")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("expected ErrUnexpectedEOF so callers can tell truncation from a tidy close, got %v", err)
	}
}

func TestAnEmptyConnectionReportsNoFrame(t *testing.T) {
	// The reverse guard. If every close reported a byte count, the count would stop
	// meaning anything: an empty connection must not claim an abandoned frame.
	r := NewReader(&halfThenError{data: nil, err: io.EOF}, 0)
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "into a frame") {
		t.Errorf("a connection that sent nothing reported an abandoned frame: %v", err)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("an empty connection should report a plain EOF so it is logged as a tidy close, got %v", err)
	}
}
