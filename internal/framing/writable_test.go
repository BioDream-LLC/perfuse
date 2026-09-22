package framing

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A framing that can be read and not written has to be refused when a channel is
// loaded, not discovered when a message is sent.
//
// A tcp destination framed as mllp validated cleanly, reported the channel valid,
// and then failed every delivery with "framing mllp cannot be written here". Five
// attempts later the message was gone. MLLP is the obvious thing for someone
// sending HL7 over a socket to reach for, so this was not an exotic mistake.
//
// Two guards. CanFrame has to agree with what Frame actually does, because a
// predicate that drifts from the code it describes is worse than no predicate. And
// every mode this package declares has to appear in the table below, so adding a
// sixth framing forces a decision about whether it can be written rather than
// inheriting one.

// writableSample gives Frame the settings each mode needs to succeed. A mode that
// fails here for want of a record length would look unwritable for the wrong reason.
var writableSample = map[Mode]Settings{
	ModeMLLP:           {Mode: ModeMLLP},
	ModeDelimited:      {Mode: ModeDelimited, Delimiter: []byte("\r")},
	ModeFixed:          {Mode: ModeFixed, RecordLength: 256},
	ModeLengthPrefixed: {Mode: ModeLengthPrefixed, LengthBytes: 4},
	ModeWhole:          {Mode: ModeWhole},
}

func TestCanFrameAgreesWithFrame(t *testing.T) {
	msg := []byte("MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5\rPID|1||X^^^A^MR")

	for mode, settings := range writableSample {
		_, err := Frame(msg, settings)
		framed := err == nil
		if got := CanFrame(mode); got != framed {
			t.Errorf("CanFrame(%q) is %v but Frame %s. The predicate and the writer have diverged, "+
				"which means configuration is either refusing something that works or accepting "+
				"something that cannot", mode, got, describe(err))
		}
	}
}

func describe(err error) string {
	if err == nil {
		return "succeeds"
	}
	return "fails with " + err.Error()
}

func TestEveryDeclaredModeIsClassified(t *testing.T) {
	// Read from source rather than from a second hand-written list, because two lists
	// of the same thing is the defect this guard exists to prevent.
	src, err := os.ReadFile("framing.go")
	if err != nil {
		t.Fatalf("reading framing.go: %v", err)
	}

	decl := regexp.MustCompile(`(?m)^\s*(Mode[A-Za-z]+)\s+Mode\s*=\s*"([^"]+)"`)
	found := decl.FindAllStringSubmatch(string(src), -1)
	if len(found) < 5 {
		t.Fatalf("found %d mode declarations in framing.go, expected at least the five that exist. "+
			"The pattern has probably stopped matching, which would make this guard pass by "+
			"checking nothing", len(found))
	}

	for _, m := range found {
		mode := Mode(m[2])
		if _, ok := writableSample[mode]; !ok {
			t.Errorf("framing %q (%s) is declared but not in writableSample, so nothing decides "+
				"whether a destination may use it. Add it, and if it cannot be written make sure "+
				"the destination validation refuses it", m[2], m[1])
		}
	}

	// And the reverse, so a mode deleted from the package does not leave a stale entry
	// here quietly asserting something about nothing.
	declared := map[Mode]bool{}
	for _, m := range found {
		declared[Mode(m[2])] = true
	}
	for mode := range writableSample {
		if !declared[mode] {
			t.Errorf("writableSample mentions %q, which framing.go no longer declares", mode)
		}
	}
}

func TestCanReadAgreesWithTheReader(t *testing.T) {
	for mode, settings := range writableSample {
		rd := NewReader(strings.NewReader("short"), settings)
		_, err := rd.ReadMessage()
		// Only the refusal matters here. A mode that can be read may still fail on this
		// deliberately truncated input, so an unsupported-mode error is what is checked
		// rather than success.
		unsupported := err != nil && strings.Contains(err.Error(), "cannot be read here")
		if CanRead(mode) == unsupported {
			t.Errorf("CanRead(%q) is %v but the reader %s. Configuration would either refuse a "+
				"framing that works or accept one that resets every connection",
				mode, CanRead(mode), describe(err))
		}
	}
}

func TestMLLPIsNeitherReadNorWrittenHere(t *testing.T) {
	// Stated as its own case because this exact combination lost messages in both
	// directions while reporting the channel valid. MLLP lives in its own source and
	// destination types, which own the connection and the acknowledgement; the generic
	// socket code here does not implement it in either direction.
	//
	// If that ever changes, the config validation refusing it has to be revisited in the
	// same change, or it will reject a configuration that works.
	if CanRead(ModeMLLP) {
		t.Error("mllp reports readable; revisit the tcp and serial source validation that refuses it")
	}
	if CanFrame(ModeMLLP) {
		t.Error("mllp reports writable; revisit the tcp destination validation that refuses it")
	}
}
