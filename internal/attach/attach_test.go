package attach

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// A message shaped like the ones this feature exists for: a report with an embedded document in OBX-5.5.
func messageWith(payload string) []byte {
	return []byte(
		"MSH|^~\\&|RAD|SITEA|EPIC|SITEB|20260821120000||ORU^R01^ORU_R01|MSG0001|P|2.5.1\r" +
			"PID|1||MRN0001^^^SITEA^MR||FROST^IVY||19910228|F\r" +
			"OBR|1||ACC123|CHEST^Chest X-ray\r" +
			"OBX|1|ED|REPORT^Report||^application^pdf^Base64^" + payload + "||||||F\r")
}

func bigPayload(n int) string {
	return strings.Repeat("QUJDREVG", n/8+1)[:n]
}

func store(attachments []Attachment) Lookup {
	held := map[Digest][]byte{}
	for _, a := range attachments {
		held[a.Digest] = a.Payload
	}
	return func(d Digest) ([]byte, bool) {
		payload, ok := held[d]
		return payload, ok
	}
}

func TestARoundTripReproducesTheMessageExactly(t *testing.T) {
	// The property everything else depends on. If extraction and reassembly are not exactly inverse, a document is
	// silently altered between arriving and being delivered - and nobody finds out until somebody opens the record.
	payload := bigPayload(50000)
	original := messageWith(payload)

	rules := []Rule{{Path: "OBX-5.5"}}
	if err := rules[0].Validate(); err != nil {
		t.Fatal(err)
	}

	result, err := Extract(original, rules)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extracted != 1 {
		t.Fatalf("extracted %d payloads, want 1", result.Extracted)
	}

	back, err := Reassemble(result.Message, store(result.Attachments))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(back, original) {
		t.Error("the round trip did not reproduce the message byte for byte")
	}
}

func TestExtractionActuallyShrinksTheMessage(t *testing.T) {
	// The entire point. Worth asserting, because a rule that matches nothing produces no error and no benefit, and
	// the difference is invisible without measuring.
	payload := bigPayload(100000)
	original := messageWith(payload)

	rules := []Rule{{Path: "OBX-5.5", MinBytes: 1000}}
	result, err := Extract(original, rules)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Message) >= len(original)/10 {
		t.Errorf("the message went from %d to %d bytes, which is not a saving worth the machinery",
			len(original), len(result.Message))
	}
	if result.Saved < 90000 {
		t.Errorf("saved = %d bytes, want most of the payload", result.Saved)
	}
	if !strings.Contains(string(result.Message), TokenPrefix) {
		t.Error("the rewritten message has no token in it")
	}
	// The parts that are not the payload must survive, or the message is no longer evidence of what arrived.
	if !strings.Contains(string(result.Message), "MRN0001") {
		t.Error("the patient identifier did not survive extraction")
	}
	if !strings.Contains(string(result.Message), "MSG0001") {
		t.Error("the control ID did not survive extraction")
	}
}

func TestTheSamePayloadTwiceIsStoredOnce(t *testing.T) {
	// Content addressing buys deduplication, and in document workflows resends are common enough that this is a real
	// saving rather than a theoretical one.
	payload := bigPayload(20000)

	first, err := Extract(messageWith(payload), []Rule{{Path: "OBX-5.5", MinBytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Extract(messageWith(payload), []Rule{{Path: "OBX-5.5", MinBytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}

	if first.Attachments[0].Digest != second.Attachments[0].Digest {
		t.Error("the same payload produced two different digests, so it would be stored twice")
	}
}

func TestSmallValuesAreLeftAlone(t *testing.T) {
	// OBX-5 holds a numeric result on one message and an embedded document on the next. Moving a short value out
	// costs a row and a lookup to save nothing.
	original := messageWith("A")

	result, err := Extract(original, []Rule{{Path: "OBX-5.5", MinBytes: DefaultMinBytes}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Extracted != 0 {
		t.Errorf("extracted %d payloads from a message with none worth extracting", result.Extracted)
	}
	if !bytes.Equal(result.Message, original) {
		t.Error("a message with nothing to extract was rewritten anyway")
	}
}

func TestAMissingAttachmentRefusesRatherThanDelivers(t *testing.T) {
	// The failure that matters most. A receiver handed a literal token where a report should be will file it as a
	// report, and nobody finds out until somebody opens the record.
	result, err := Extract(messageWith(bigPayload(20000)), []Rule{{Path: "OBX-5.5", MinBytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}

	empty := func(Digest) ([]byte, bool) { return nil, false }
	_, err = Reassemble(result.Message, empty)

	if err == nil {
		t.Fatal("a message with a missing attachment was reassembled successfully")
	}

	var missing *ErrMissingAttachment
	if !errors.As(err, &missing) {
		t.Fatalf("error is %T, want *ErrMissingAttachment so a caller can act on it", err)
	}
	if !strings.Contains(err.Error(), "placeholder where a document should be") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}
}

func TestATruncatedTokenIsReportedAsCorruptionNotAbsence(t *testing.T) {
	// Different cause, different fix. An unterminated token means something truncated the message after it was
	// stored, which points at the store rather than at the attachment.
	broken := []byte("MSH|^~\\&|A|B|C|D|20260821||ORU^R01|1|P|2.5.1\rOBX|1|ED|R||" + TokenPrefix + "abc123\r")

	_, err := Reassemble(broken, func(Digest) ([]byte, bool) { return []byte("x"), true })
	if err == nil {
		t.Fatal("an unterminated token was accepted")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("the error does not name the likely cause: %v", err)
	}
}

func TestAMessageWithNoTokensIsReturnedUnchanged(t *testing.T) {
	// The common case, and it must not allocate a rewritten copy of every message that has no attachments.
	original := messageWith("short")

	back, err := Reassemble(original, func(Digest) ([]byte, bool) {
		t.Error("the store was consulted for a message with no tokens")
		return nil, false
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, original) {
		t.Error("a message with no tokens came back different")
	}
}

func TestTokensCanBeListedWithoutThePayloads(t *testing.T) {
	// Needed to work out what a queued message depends on, so an attachment is not swept away while something still
	// needs it.
	result, err := Extract(messageWith(bigPayload(20000)), []Rule{{Path: "OBX-5.5", MinBytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}

	tokens := Tokens(result.Message)
	if len(tokens) != 1 {
		t.Fatalf("found %d tokens, want 1", len(tokens))
	}
	if tokens[0] != result.Attachments[0].Digest {
		t.Error("the listed token does not match the extracted attachment")
	}

	if Tokens(messageWith("short")) != nil {
		t.Error("a message with no attachments reported tokens")
	}
}

func TestAnUnparseableMessageIsLeftAloneRatherThanFailing(t *testing.T) {
	// It still has to be stored and acknowledged as unparseable. Failing extraction would turn a message the engine
	// already knows how to handle into an error somewhere else.
	rubbish := []byte("this is not an HL7 message at all")

	result, err := Extract(rubbish, []Rule{{Path: "OBX-5.5"}})
	if err != nil {
		t.Fatalf("extraction failed on an unparseable message: %v", err)
	}
	if !bytes.Equal(result.Message, rubbish) {
		t.Error("an unparseable message was modified")
	}
}

func TestARuleInMSHIsRefused(t *testing.T) {
	// MSH carries the control ID and message type everything routes on. Extracting from it would leave a stored
	// message that cannot be read without a database lookup.
	rule := Rule{Path: "MSH-3"}

	err := rule.Validate()
	if err == nil {
		t.Fatal("an attachment rule in MSH was accepted")
	}
	if !strings.Contains(err.Error(), "routes on") {
		t.Errorf("the error does not explain why: %v", err)
	}
}

func TestATinyThresholdIsRefused(t *testing.T) {
	// It would extract ordinary field values, making the store larger rather than smaller - the opposite of the
	// feature's purpose, and slow in a way that would be blamed on something else.
	rule := Rule{Path: "OBX-5.5", MinBytes: 4}

	err := rule.Validate()
	if err == nil {
		t.Fatal("a 4-byte threshold was accepted")
	}
	if !strings.Contains(err.Error(), "larger rather than smaller") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}
}

func TestARuleWithNoPathIsRefused(t *testing.T) {
	if err := (&Rule{}).Validate(); err == nil {
		t.Error("a rule with no path was accepted")
	}
}

func TestARuleWithAnUnparseablePathIsRefused(t *testing.T) {
	if err := (&Rule{Path: "not a path at all!!"}).Validate(); err == nil {
		t.Error("a rule with an unparseable path was accepted")
	}
}

func TestTheDigestIsNotTruncatedForLookup(t *testing.T) {
	// Short() is for logs and interfaces. A truncated digest can collide, and a collision here would attach one
	// patient's document to another patient's record.
	d := Compute([]byte("some payload"))

	if len(d) != 64 {
		t.Errorf("digest is %d characters, want a full sha256 hex string", len(d))
	}
	if len(d.Short()) != 12 {
		t.Errorf("Short() is %d characters, want 12", len(d.Short()))
	}
	if strings.Contains(d.Token(), d.Short()+TokenSuffix) {
		t.Error("the token appears to carry a shortened digest, which could collide")
	}
}

func TestTwoDifferentPayloadsInOneMessageBothSurvive(t *testing.T) {
	// A report with two embedded documents. If only the first is handled, the second is delivered as-is and the
	// saving silently does not happen.
	first := bigPayload(20000)
	second := bigPayload(30000)
	original := []byte(
		"MSH|^~\\&|RAD|A|EPIC|B|20260821||ORU^R01|1|P|2.5.1\r" +
			"OBX|1|ED|R1||^application^pdf^Base64^" + first + "||||||F\r" +
			"OBX|2|ED|R2||^application^pdf^Base64^" + second + "||||||F\r")

	// Two rules, one per OBX occurrence, since each is its own segment.
	result, err := Extract(original, []Rule{{Path: "OBX-5.5", MinBytes: 1000}, {Path: "OBX[2]-5.5", MinBytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Reassemble(result.Message, store(result.Attachments))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, original) {
		t.Errorf("round trip failed with two attachments (extracted %d)", result.Extracted)
	}
}
