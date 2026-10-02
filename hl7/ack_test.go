package hl7

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var ackTime = time.Date(2026, 8, 18, 12, 30, 45, 0, time.UTC)

func TestAckMirrorsAddressing(t *testing.T) {
	m := mustParse(t, adtA01)
	ack := mustParse(t, string(m.Ack(AckOptions{
		Timestamp: ackTime,
		ControlID: "ACK0001",
	})))

	// The addressing swaps. Copying it instead of mirroring it is a common bug
	// and produces an ACK the sender ignores.
	cases := map[string]string{
		"MSH-3":  "RECVAPP",
		"MSH-4":  "RECVFAC",
		"MSH-5":  "SENDAPP",
		"MSH-6":  "SENDFAC",
		"MSH-7":  "20260818123045",
		"MSH-9":  "ACK",
		"MSH-10": "ACK0001",
		"MSH-11": "P",
		"MSH-12": "2.5.1",
		"MSA-1":  "AA",
		"MSA-2":  "MSG00001",
	}
	for path, want := range cases {
		if got := ack.MustGet(path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestAckEchoesOriginalControlID(t *testing.T) {
	m := mustParse(t, adtA01)
	ack := mustParse(t, string(m.Ack(AckOptions{Timestamp: ackTime})))

	// MSA-2 is the only link between an acknowledgement and its message.
	if got, want := ack.MustGet("MSA-2"), m.ControlID(); got != want {
		t.Errorf("MSA-2 = %q, want the original control ID %q", got, want)
	}
	// And the ACK must have its own, different control ID.
	if ack.ControlID() == m.ControlID() {
		t.Error("ACK reused the original control ID in MSH-10")
	}
}

func TestAckPreservesSenderVersion(t *testing.T) {
	// A 2.3 sender gets a 2.3 acknowledgement. Answering in our preferred
	// version is rejected by strict senders.
	msg := "MSH|^~\\&|OLD|SITE|US|HERE|20260818||ADT^A04|C1|P|2.3\rPID|1||MRN9\r"
	m := mustParse(t, msg)
	ack := mustParse(t, string(m.Ack(AckOptions{Timestamp: ackTime})))

	if got, want := ack.MustGet("MSH-12"), "2.3"; got != want {
		t.Errorf("MSH-12 = %q, want %q", got, want)
	}
}

func TestAckPreservesProcessingID(t *testing.T) {
	msg := "MSH|^~\\&|A|B|C|D|20260818||ADT^A01|C1|T|2.5.1\rPID|1||MRN1\r"
	m := mustParse(t, msg)
	ack := mustParse(t, string(m.Ack(AckOptions{Timestamp: ackTime})))

	// A test-mode message must not be answered as production.
	if got, want := ack.MustGet("MSH-11"), "T"; got != want {
		t.Errorf("MSH-11 = %q, want %q", got, want)
	}
}

func TestAckErrorWithText(t *testing.T) {
	m := mustParse(t, adtA01)
	raw := m.Ack(AckOptions{
		Code:      AckError,
		Text:      "PID-3 is missing an assigning authority",
		Timestamp: ackTime,
		ErrorCode: "101",
	})
	ack := mustParse(t, string(raw))

	if got, want := ack.MustGet("MSA-1"), "AE"; got != want {
		t.Errorf("MSA-1 = %q, want %q", got, want)
	}
	if got, want := ack.MustGet("MSA-3"), "PID-3 is missing an assigning authority"; got != want {
		t.Errorf("MSA-3 = %q, want %q", got, want)
	}
	if _, ok := ack.Segment("ERR", 1); !ok {
		t.Fatal("no ERR segment")
	}
	if got, want := ack.MustGet("ERR-3"), "101"; got != want {
		t.Errorf("ERR-3 = %q, want %q", got, want)
	}
	if got, want := ack.MustGet("ERR-4"), "E"; got != want {
		t.Errorf("ERR-4 = %q, want %q", got, want)
	}
}

func TestAckTextIsEscapedAndSingleLine(t *testing.T) {
	m := mustParse(t, adtA01)
	// Error text often comes from an exception message, which can contain both
	// delimiters and newlines. Either one would corrupt the acknowledgement.
	ack := m.Ack(AckOptions{
		Code:      AckReject,
		Text:      "bad value in PID|3\nsecond line\rthird",
		Timestamp: ackTime,
	})

	body := string(ack)
	if strings.Count(body, "\r") != 2 {
		t.Errorf("acknowledgement has %d segments, want 2:\n%q", strings.Count(body, "\r"), body)
	}

	parsed := mustParse(t, body)
	got := parsed.MustGet("MSA-3")
	if want := "bad value in PID|3 second line third"; got != want {
		t.Errorf("MSA-3 = %q, want %q", got, want)
	}
}

func TestAckIncludeTriggerEvent(t *testing.T) {
	m := mustParse(t, adtA01)

	plain := mustParse(t, string(m.Ack(AckOptions{Timestamp: ackTime})))
	if got, want := plain.MustGet("MSH-9"), "ACK"; got != want {
		t.Errorf("MSH-9 = %q, want %q", got, want)
	}

	withEvent := mustParse(t, string(m.Ack(AckOptions{
		Timestamp:           ackTime,
		IncludeTriggerEvent: true,
	})))
	if got, want := withEvent.MustGet("MSH-9"), "ACK^A01"; got != want {
		t.Errorf("MSH-9 = %q, want %q", got, want)
	}
	if got, want := withEvent.MustGet("MSH-9.2"), "A01"; got != want {
		t.Errorf("MSH-9.2 = %q, want %q", got, want)
	}
}

func TestAckSenderOverride(t *testing.T) {
	m := mustParse(t, adtA01)
	ack := mustParse(t, string(m.Ack(AckOptions{
		Timestamp:          ackTime,
		SendingApplication: "PERFUSE",
		SendingFacility:    "OURFAC",
	})))

	if got, want := ack.MustGet("MSH-3"), "PERFUSE"; got != want {
		t.Errorf("MSH-3 = %q, want %q", got, want)
	}
	if got, want := ack.MustGet("MSH-4"), "OURFAC"; got != want {
		t.Errorf("MSH-4 = %q, want %q", got, want)
	}
	// The receiver is still mirrored from the original sender.
	if got, want := ack.MustGet("MSH-5"), "SENDAPP"; got != want {
		t.Errorf("MSH-5 = %q, want %q", got, want)
	}
}

func TestAckUsesSenderSeparators(t *testing.T) {
	msg := "MSH#@!$%#SENDAPP#SENDFAC#RECV#RFAC#20260818##ADT@A01#C9#P#2.5.1\rPID#1##MRN1\r"
	m := mustParse(t, msg)
	raw := m.Ack(AckOptions{Timestamp: ackTime})

	// An acknowledgement encoded with different delimiters than the message it
	// answers is unparseable by the sender.
	if !strings.HasPrefix(string(raw), "MSH#@!$%#") {
		t.Fatalf("ACK did not use the sender's delimiters: %q", raw)
	}
	ack := mustParse(t, string(raw))
	if got, want := ack.MustGet("MSA-2"), "C9"; got != want {
		t.Errorf("MSA-2 = %q, want %q", got, want)
	}
}

func TestEnhancedModeDetection(t *testing.T) {
	// MSH-15 asks for a commit acknowledgement.
	msg := "MSH|^~\\&|A|B|C|D|20260818||ADT^A01|C1|P|2.5.1|||AL|NE\rPID|1||MRN1\r"
	m := mustParse(t, msg)

	if !m.WantsEnhancedAck() {
		t.Error("WantsEnhancedAck() = false for MSH-15 = AL")
	}
	accept, application := m.AckMode()
	if accept != "AL" || application != "NE" {
		t.Errorf("AckMode() = (%q, %q), want (AL, NE)", accept, application)
	}

	plain := mustParse(t, adtA01)
	if plain.WantsEnhancedAck() {
		t.Error("WantsEnhancedAck() = true for a message with no MSH-15")
	}
}

func TestAckCodeEnhanced(t *testing.T) {
	for code, want := range map[AckCode]bool{
		AckAccept:    false,
		AckError:     false,
		AckReject:    false,
		CommitAccept: true,
		CommitError:  true,
		CommitReject: true,
	} {
		if got := code.Enhanced(); got != want {
			t.Errorf("%s.Enhanced() = %v, want %v", code, got, want)
		}
	}
}

func TestAckForUnparseableMessage(t *testing.T) {
	// A sender that transmits garbage still needs an answer, or it retries for
	// ever. There is nothing to mirror, so the ACK is minimal but valid.
	raw := AckFor(ErrNotHL7, AckOptions{
		Timestamp:          ackTime,
		ControlID:          "REJ1",
		SendingApplication: "PERFUSE",
	})

	ack := mustParse(t, string(raw))
	if got, want := ack.MustGet("MSA-1"), "AR"; got != want {
		t.Errorf("MSA-1 = %q, want %q", got, want)
	}
	if got := ack.MustGet("MSA-3"); got != ErrNotHL7.Error() {
		t.Errorf("MSA-3 = %q, want the parse error", got)
	}
	if got, want := ack.MustGet("MSH-3"), "PERFUSE"; got != want {
		t.Errorf("MSH-3 = %q, want %q", got, want)
	}
	if got := ack.MustGet("MSA-2"); got != "" {
		t.Errorf("MSA-2 = %q, want empty: the control ID is unknown because parsing is what failed", got)
	}
}

func TestAckForWrapsArbitraryError(t *testing.T) {
	raw := AckFor(errors.New("connection reset mid-message"), AckOptions{Timestamp: ackTime})
	ack := mustParse(t, string(raw))
	if got, want := ack.MustGet("MSA-3"), "connection reset mid-message"; got != want {
		t.Errorf("MSA-3 = %q, want %q", got, want)
	}
}

func TestGeneratedControlIDsAreDistinct(t *testing.T) {
	m := mustParse(t, adtA01)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		ack := mustParse(t, string(m.Ack(AckOptions{Timestamp: ackTime})))
		id := ack.ControlID()
		if id == "" {
			t.Fatal("generated an empty control ID")
		}
		if seen[id] {
			t.Fatalf("control ID %q generated twice", id)
		}
		seen[id] = true
	}
}

func TestControlIDsAreUniqueUnderConcurrency(t *testing.T) {
	// Acknowledgements are generated concurrently, one goroutine per connection.
	// A duplicated MSH-10 would give two different messages the same correlation
	// key, which is the one thing a control ID exists to prevent. This test also
	// fails under -race if the counter is not atomic.
	m := mustParse(t, adtA01)

	const workers, each = 16, 200
	ids := make(chan string, workers*each)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				ack, err := Parse(m.Ack(AckOptions{Timestamp: ackTime}))
				if err != nil {
					t.Errorf("generated an unparseable acknowledgement: %v", err)
					return
				}
				ids <- ack.ControlID()
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool, workers*each)
	for id := range ids {
		if id == "" {
			t.Fatal("generated an empty control ID")
		}
		if seen[id] {
			t.Fatalf("control ID %q was generated more than once", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*each {
		t.Errorf("got %d distinct control IDs, want %d", len(seen), workers*each)
	}
}

func TestAckIsParseable(t *testing.T) {
	// Whatever we generate has to survive our own parser, which is the cheapest
	// guard against emitting something malformed.
	m := mustParse(t, adtA01)
	for _, opts := range []AckOptions{
		{},
		{Code: AckError, Text: "problem"},
		{Code: AckReject, Text: "very bad", ErrorCode: "207"},
		{IncludeTriggerEvent: true},
		{SendingApplication: "P", SendingFacility: "F"},
	} {
		raw := m.Ack(opts)
		if _, err := Parse(raw); err != nil {
			t.Errorf("Ack(%+v) produced an unparseable message: %v\n%q", opts, err, raw)
		}
	}
}
