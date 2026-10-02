// Package profile_test rather than profile.
//
// These tests assert that a proposed channel actually loads, which needs the config package - and config depends on
// contract, which depends on profile. An external test package is the way out: it may import a package that imports the
// package under test.
package profile_test

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/profile"
)

const labResult = "MSH|^~\\&|LABSYS|QUESTLAB|CLINICEHR|RIVERSIDE|20260828093000||ORU^R01|LAB00219|P|2.5\r" +
	"PID|1||A4471^^^RIVERSIDE^MR||DOE^JANE||19750614|F\r" +
	"OBR|1|ORD9912|ACC5521|CBC^COMPLETE BLOOD COUNT^L|||20260828084500\r" +
	"OBX|1|NM|WBC^WHITE BLOOD CELL COUNT^L||7.2|10*3/uL|4.0-11.0|N|||F\r" +
	"OBX|2|NM|HGB^HAEMOGLOBIN^L||13.4|g/dL|12.0-16.0|N|||F\r"

// The clinic case: one sample message, and a channel that loads.
//
// A proposal that does not load is worse than no proposal. Somebody without an interface analyst cannot tell whether the
// failure is theirs or the tool's, and the whole point is that they do not have one.
func TestAChannelProposedFromOneSampleActuallyLoads(t *testing.T) {
	reading, err := profile.ReadSample(labResult, "quest-labs")
	if err != nil {
		t.Fatal(err)
	}
	if reading.Messages != 1 {
		t.Fatalf("read %d messages, want 1", reading.Messages)
	}

	cfg, err := config.Load(strings.NewReader(reading.YAML), "proposed.yaml")
	if err != nil {
		t.Fatalf("the proposed channel does not load:\n%v\n---\n%s", err, reading.YAML)
	}
	if cfg.Name != "quest-labs" {
		t.Errorf("channel name = %q, want quest-labs", cfg.Name)
	}
}

// One message must be described as low confidence, and the reasons must be specific.
//
// The danger with one sample is not that it tells you little - it is that it looks like it tells you a lot. Every field
// present looks mandatory, every code looks like the whole set, and nothing repeats.
func TestOneSampleIsReportedAsLowConfidenceWithSpecificReasons(t *testing.T) {
	reading, err := profile.ReadSample(labResult, "quest-labs")
	if err != nil {
		t.Fatal(err)
	}

	if reading.Confidence != "low" {
		t.Errorf("confidence from one message = %q, want low", reading.Confidence)
	}
	if len(reading.Unknowable) < 3 {
		t.Errorf("only %d things listed as unknowable from a single message: %v",
			len(reading.Unknowable), reading.Unknowable)
	}

	joined := strings.Join(reading.Unknowable, " | ")
	// The specific traps, named. A generic "this is only one message" warning is one somebody skims.
	for _, want := range []string{"optional", "repeat", "codes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the limits do not mention %q: %s", want, joined)
		}
	}

	// Observed and guessed must both be populated and must be separate lists. A reader who cannot tell which is which
	// has to verify everything or trust everything, and both are worse than knowing where to look.
	if len(reading.Observed) == 0 {
		t.Error("nothing was reported as observed, so the reader cannot tell what the sample actually showed")
	}
	if len(reading.Guessed) == 0 {
		t.Error("nothing was reported as guessed, which implies the whole channel was derived from the sample")
	}
}

// A Z-segment must be called out, because it is the thing a specification never mentions and an integration always has
// to handle.
func TestALocalSegmentIsCalledOut(t *testing.T) {
	withZ := labResult + "ZPI|1|LOCALVALUE|SOMETHING\r"

	reading, err := profile.ReadSample(withZ, "with-local")
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(reading.Observed, " | ")
	if !strings.Contains(joined, "ZPI") {
		t.Errorf("a Z-segment was not mentioned: %s", joined)
	}
	if !strings.Contains(joined, "not in the HL7 standard") {
		t.Errorf("the Z-segment was listed but not explained: %s", joined)
	}
}

// Several messages pasted as one block must be split.
func TestSeveralMessagesPastedTogetherAreSplit(t *testing.T) {
	two := labResult + strings.Replace(labResult, "LAB00219", "LAB00220", 1)

	reading, err := profile.ReadSample(two, "two")
	if err != nil {
		t.Fatal(err)
	}
	if reading.Messages != 2 {
		t.Errorf("read %d messages from a two-message paste, want 2", reading.Messages)
	}
}

// What somebody actually pastes: newlines from a mail client, not carriage returns.
//
// Refusing a message because it arrived with the wrong line ending would be an obstruction rather than a check. Nobody
// pasting out of Outlook knows which one they have.
func TestAMessagePastedWithNewlinesIsAccepted(t *testing.T) {
	for name, text := range map[string]string{
		"unix":    strings.ReplaceAll(labResult, "\r", "\n"),
		"windows": strings.ReplaceAll(labResult, "\r", "\r\n"),
		"mllp":    "\x0b" + labResult + "\x1c\x0d",
	} {
		reading, err := profile.ReadSample(text, "pasted")
		if err != nil {
			t.Errorf("%s line endings were refused: %v", name, err)
			continue
		}
		if reading.Messages != 1 {
			t.Errorf("%s: read %d messages, want 1", name, reading.Messages)
		}
	}
}

// An email covering note above the message must not become a segment.
func TestProseBeforeTheMessageIsIgnored(t *testing.T) {
	pasted := "Hi,\n\nHere's the sample you asked for. Let me know if you need anything else.\n\nRegards,\nPat\n\n" +
		strings.ReplaceAll(labResult, "\r", "\n")

	reading, err := profile.ReadSample(pasted, "from-email")
	if err != nil {
		t.Fatalf("a message pasted out of an email was refused: %v", err)
	}
	if reading.Messages != 1 {
		t.Errorf("read %d messages, want 1", reading.Messages)
	}
	if _, err := config.Load(strings.NewReader(reading.YAML), "p.yaml"); err != nil {
		t.Errorf("the proposal from an emailed sample does not load: %v", err)
	}
}

// Text with no message in it is refused with something actionable.
func TestTextWithNoMessageIsRefusedHelpfully(t *testing.T) {
	_, err := profile.ReadSample("Please find attached our HL7 interface specification version 4.2.", "nope")
	if err == nil {
		t.Fatal("prose with no message in it was accepted")
	}
	// The commonest thing somebody has instead of a sample is the specification document, and the error should say so
	// rather than only that no message was found.
	if !strings.Contains(err.Error(), "specification") {
		t.Errorf("the refusal does not address the likely cause: %v", err)
	}
}

// A sample whose separators have been replaced parses into nonsense, and that must be said.
//
// This test started out asserting such a message is refused. It passed - and it passed because HL7 takes the field
// separator from MSH-1, so a message with every pipe replaced by a look-alike character parses perfectly and yields
// fields that are all wrong. Nothing errors anywhere. A channel built from it would be built against a message nobody
// sent.
//
// So the assertion is not that it is refused - refusing would be wrong, since a sender may genuinely use another
// separator - but that the reader is told, and that confidence drops.
func TestASampleWithReplacedSeparatorsIsFlaggedRatherThanTrusted(t *testing.T) {
	mangled := strings.ReplaceAll(labResult, "|", "\u2502")

	reading, err := profile.ReadSample(mangled, "mangled")
	if err != nil {
		t.Fatalf("a message with an unusual separator was refused, but that is legal HL7: %v", err)
	}

	joined := strings.Join(reading.Unknowable, " | ")
	if !strings.Contains(joined, "word processor") {
		t.Errorf("nothing warns that the separators may have been substituted: %s", joined)
	}
	if reading.Confidence != "low" {
		t.Errorf("confidence = %q despite a suspect field separator", reading.Confidence)
	}

	// A normal sample must not be flagged, or the warning is noise.
	clean, err := profile.ReadSample(labResult, "clean")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(clean.Unknowable, " | "), "word processor") {
		t.Error("an ordinary pipe-delimited sample was flagged for its separator")
	}
}

// More samples must raise the stated confidence, or the number is decoration.
func TestMoreSamplesRaiseTheStatedConfidence(t *testing.T) {
	one, err := profile.ReadSample(labResult, "one")
	if err != nil {
		t.Fatal(err)
	}

	var many strings.Builder
	for i := range 150 {
		many.WriteString(strings.Replace(labResult, "LAB00219", "LAB"+string(rune('A'+i%26))+"0000", 1))
	}
	lots, err := profile.ReadSample(many.String(), "lots")
	if err != nil {
		t.Fatal(err)
	}

	if one.Confidence == lots.Confidence {
		t.Errorf("one sample and %d samples both report %q confidence", lots.Messages, one.Confidence)
	}
	if lots.Confidence != "reasonable" {
		t.Errorf("confidence from %d samples = %q", lots.Messages, lots.Confidence)
	}
	// Even at the top there must be a stated limit. A tool that claims to know everything about a feed from a sample
	// is the thing this is trying not to be.
	if len(lots.Unknowable) == 0 {
		t.Error("a large sample reported nothing as unknowable, which no sample can honestly claim")
	}
}

// An unparseable message among good ones is excluded and reported, not silently dropped.
func TestAnUnparseableMessageIsReportedRatherThanDropped(t *testing.T) {
	// Genuinely unparseable: an MSH too short to hold the encoding characters, which is what a paste cut off partway
	// through looks like. Most malformations do not fail - HL7 is extremely tolerant - and finding one that does took
	// checking rather than guessing.
	mixed := labResult + "MSH|\r"

	reading, err := profile.ReadSample(mixed, "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if reading.Unreadable == 0 {
		t.Error("a message that could not be parsed was not reported")
	}
	joined := strings.Join(reading.Unknowable, " | ")
	if !strings.Contains(joined, "could not be parsed") {
		t.Errorf("the unparseable message is not mentioned in the limits: %s", joined)
	}
}
