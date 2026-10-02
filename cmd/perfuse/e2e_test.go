package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/mllp"
)

const testADT = "MSH|^~\\&|SENDAPP|SENDFAC|RECVAPP|RECVFAC|20260818120000||ADT^A01^ADT_A01|CTRL1|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN123456^^^SENDFAC^MR||Doe^Jane^Q||19800101|F\r"

const testORU = "MSH|^~\\&|LAB|SENDFAC|EHR|RECVFAC|20260818120500||ORU^R01^ORU_R01|CTRL2|P|2.5.1\r" +
	"PID|1||MRN123456^^^SENDFAC^MR||Doe^Jane^Q||19800101|F\r" +
	"OBX|1|NM|GLU^Glucose^LN||95|mg/dL|70-110|N\r"

// listenerFor starts an in-process MLLP server that behaves the way the listen
// command does, and returns its address.
func listenerFor(t *testing.T, h mllp.Handler) string {
	t.Helper()

	srv := &mllp.Server{Handler: h, Logger: quietTestLogger()}
	ln := mustListen(t)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestSendReceivesAcknowledgements(t *testing.T) {
	var seen []string
	addr := listenerFor(t, mllp.HandlerFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
		m, err := hl7.Parse(raw)
		if err != nil {
			return hl7.AckFor(err, hl7.AckOptions{SendingApplication: "TEST"}), nil
		}
		seen = append(seen, m.ControlID())
		return m.Ack(hl7.AckOptions{Code: hl7.AckAccept, SendingApplication: "TEST"}), nil
	}))

	dir := t.TempDir()
	path := filepath.Join(dir, "messages.hl7")
	if err := os.WriteFile(path, []byte(testADT+testORU), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := run([]string{"send", "-addr", addr, "-timeout", "5s", path}, &out, &errOut); err != nil {
		t.Fatalf("send: %v\n%s", err, errOut.String())
	}

	got := out.String()
	for _, want := range []string{"ADT^A01 CTRL1", "ORU^R01 CTRL2", "AA", "2 sent: 2 accepted, 0 rejected, 0 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
	if len(seen) != 2 {
		t.Errorf("server saw %d messages, want 2", len(seen))
	}
}

func TestSendReportsRejection(t *testing.T) {
	addr := listenerFor(t, mllp.HandlerFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
		m, err := hl7.Parse(raw)
		if err != nil {
			return nil, err
		}
		return m.Ack(hl7.AckOptions{
			Code: hl7.AckReject,
			Text: "unsupported message type",
		}), nil
	}))

	dir := t.TempDir()
	path := filepath.Join(dir, "one.hl7")
	if err := os.WriteFile(path, []byte(testADT), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := run([]string{"send", "-addr", addr, "-timeout", "5s", path}, &out, &errOut)

	// A rejection is a non-zero exit, so this is usable in a script.
	if !errors.Is(err, errBlocking) {
		t.Fatalf("err = %v, want errBlocking", err)
	}
	got := out.String()
	if !strings.Contains(got, "AR") || !strings.Contains(got, "unsupported message type") {
		t.Errorf("output did not report the rejection\n%s", got)
	}
	if !strings.Contains(got, "0 accepted, 1 rejected") {
		t.Errorf("summary wrong\n%s", got)
	}
}

func TestSendStopOnError(t *testing.T) {
	var count int
	addr := listenerFor(t, mllp.HandlerFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
		count++
		m, _ := hl7.Parse(raw)
		return m.Ack(hl7.AckOptions{Code: hl7.AckError, Text: "no"}), nil
	}))

	dir := t.TempDir()
	path := filepath.Join(dir, "two.hl7")
	if err := os.WriteFile(path, []byte(testADT+testORU), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := run([]string{"send", "-addr", addr, "-timeout", "5s", "-stop-on-error", path}, &out, &errOut); err == nil {
		t.Fatal("expected an error")
	}
	if count != 1 {
		t.Errorf("server saw %d messages, want 1: sending should have stopped", count)
	}
}

func TestSendRequiresAddr(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"send", "nonexistent.hl7"}, &out, &errOut); err == nil {
		t.Error("expected an error when -addr is missing")
	}
}

func TestSplitMessages(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		got := splitMessages([]byte(testADT))
		if len(got) != 1 {
			t.Fatalf("got %d messages, want 1", len(got))
		}
		if string(got[0]) != testADT {
			t.Errorf("message = %q", got[0])
		}
	})

	t.Run("concatenated", func(t *testing.T) {
		got := splitMessages([]byte(testADT + testORU))
		if len(got) != 2 {
			t.Fatalf("got %d messages, want 2", len(got))
		}
		if string(got[0]) != testADT {
			t.Errorf("first = %q, want %q", got[0], testADT)
		}
		if string(got[1]) != testORU {
			t.Errorf("second = %q, want %q", got[1], testORU)
		}
	})

	t.Run("mllp framed", func(t *testing.T) {
		var buf bytes.Buffer
		buf.Write(mllp.Frame([]byte(testADT)))
		buf.Write(mllp.Frame([]byte(testORU)))

		got := splitMessages(buf.Bytes())
		if len(got) != 2 {
			t.Fatalf("got %d messages, want 2", len(got))
		}
		if string(got[0]) != testADT {
			t.Errorf("first = %q", got[0])
		}
	})

	t.Run("windows line endings", func(t *testing.T) {
		crlf := strings.ReplaceAll(testADT, "\r", "\r\n")
		got := splitMessages([]byte(crlf))
		if len(got) != 1 {
			t.Fatalf("got %d messages, want 1", len(got))
		}
		// Terminators must be normalised to CR, which is what HL7 requires.
		if bytes.Contains(got[0], []byte("\n")) {
			t.Errorf("line feeds survived normalisation: %q", got[0])
		}
	})

	t.Run("no messages", func(t *testing.T) {
		if got := splitMessages([]byte("this is not HL7")); len(got) != 0 {
			t.Errorf("got %d messages, want 0", len(got))
		}
	})
}

func TestDescribeAck(t *testing.T) {
	m, err := hl7.ParseString(testADT)
	if err != nil {
		t.Fatal(err)
	}

	code, text := describeAck(m.Ack(hl7.AckOptions{Code: hl7.AckAccept}))
	if code != "AA" || text != "accepted" {
		t.Errorf("got (%q, %q), want (AA, accepted)", code, text)
	}

	code, text = describeAck(m.Ack(hl7.AckOptions{Code: hl7.AckError, Text: "bad PID"}))
	if code != "AE" || text != "bad PID" {
		t.Errorf("got (%q, %q), want (AE, bad PID)", code, text)
	}

	code, _ = describeAck([]byte("not an hl7 message"))
	if code != "?" {
		t.Errorf("code = %q, want ? for an unparseable acknowledgement", code)
	}
}

func TestSanitiseFilename(t *testing.T) {
	// Message types arrive over the wire, so a sender must not be able to choose
	// a path on our disk.
	cases := map[string]string{
		"ADT_A01":               "ADT_A01",
		"../../etc/passwd":      "______etc_passwd",
		"ORU^R01":               "ORU_R01",
		"":                      "UNKNOWN",
		"with/slash":            "with_slash",
		strings.Repeat("A", 50): strings.Repeat("A", 32),
	}
	for in, want := range cases {
		if got := sanitise(in); got != want {
			t.Errorf("sanitise(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMessageSinkWritesReplayableFile(t *testing.T) {
	dir := t.TempDir()
	sink := &messageSink{dir: dir}

	m, err := hl7.ParseString(testADT)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sink.write(m, []byte(testADT)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// One file per day and message type, not one per message: a busy feed would
	// otherwise produce millions of files.
	if len(entries) != 1 {
		t.Fatalf("got %d files, want 1: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0].Name(), "ADT_A01") {
		t.Errorf("filename = %q, want it to name the message type", entries[0].Name())
	}

	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	// The file is MLLP-framed so it can be replayed byte for byte.
	got := splitMessages(raw)
	if len(got) != 3 {
		t.Fatalf("replaying the file yielded %d messages, want 3", len(got))
	}
	if string(got[0]) != testADT {
		t.Errorf("replayed message = %q", got[0])
	}
}

func TestListenWritesReceivedMessages(t *testing.T) {
	// End to end: a server storing to disk, a client sending to it, then the
	// stored file replayed back through the splitter.
	dir := t.TempDir()
	sink := &messageSink{dir: dir}

	addr := listenerFor(t, mllp.HandlerFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
		m, err := hl7.Parse(raw)
		if err != nil {
			return hl7.AckFor(err, hl7.AckOptions{}), nil
		}
		if err := sink.write(m, raw); err != nil {
			return m.Ack(hl7.AckOptions{Code: hl7.AckError, Text: "storage failed"}), nil
		}
		return m.Ack(hl7.AckOptions{Code: hl7.AckAccept}), nil
	}))

	src := filepath.Join(t.TempDir(), "in.hl7")
	if err := os.WriteFile(src, []byte(testADT+testORU), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := run([]string{"send", "-addr", addr, "-timeout", "5s", src}, &out, &errOut); err != nil {
		t.Fatalf("send: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d files, want one per message type: %v", len(entries), entries)
	}

	var total int
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		total += len(splitMessages(raw))
	}
	if total != 2 {
		t.Errorf("stored %d messages, want 2", total)
	}
}

func TestListenRejectsGarbageButStaysUp(t *testing.T) {
	addr := listenerFor(t, mllp.HandlerFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
		m, err := hl7.Parse(raw)
		if err != nil {
			return hl7.AckFor(err, hl7.AckOptions{SendingApplication: "PERFUSE"}), nil
		}
		return m.Ack(hl7.AckOptions{Code: hl7.AckAccept}), nil
	}))

	c := &mllp.Client{Addr: addr, Timeout: 5 * time.Second}
	defer c.Close()

	// Something that frames correctly but is not HL7. The sender must still get
	// an answer, or it retries for ever.
	reply, err := c.Send(context.Background(), []byte("this is not an HL7 message"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	code, _ := describeAck(reply)
	if code != "AR" {
		t.Errorf("code = %q, want AR", code)
	}

	// And the connection is still usable for a valid message afterwards.
	reply, err = c.Send(context.Background(), []byte(testADT))
	if err != nil {
		t.Fatalf("Send after rejection: %v", err)
	}
	if code, _ := describeAck(reply); code != "AA" {
		t.Errorf("code = %q, want AA", code)
	}
}
