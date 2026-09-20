package hl7v3

import (
	"strings"
	"testing"
	"time"
)

// TestAnAcknowledgementIsWellFormedAndParsesBack is the strongest available check.
//
// An acknowledgement that this package cannot read back is one a receiver probably cannot either, and generating XML by hand
// makes that a real risk. Parsing it with the same parser is not proof of schema validity, but it catches every mistake that
// produces broken XML - which is the class of mistake hand-writing invites.
func TestAnAcknowledgementIsWellFormedAndParsesBack(t *testing.T) {
	original, err := Parse([]byte(pdqQuery))
	if err != nil {
		t.Fatal(err)
	}

	ack, err := AckFor(original, OutcomeDelivered, "2.16.840.1.113883.3.72.6.2", "")
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse(ack)
	if err != nil {
		t.Fatalf("the acknowledgement we generated cannot be parsed: %v\n%s", err, ack)
	}
	if parsed.InteractionID != "MCCI_IN000002UV01" {
		t.Errorf("interaction is %q", parsed.InteractionID)
	}

	// The reference back to the original is what lets a sender match this to what it sent.
	if !strings.Contains(string(ack), `extension="Q001"`) {
		t.Errorf("the acknowledgement does not reference the original message:\n%s", ack)
	}

	// Sender and receiver swap. Getting this backwards produces an acknowledgement the sender discards silently.
	if parsed.Sender.ID.Root != "2.16.840.1.113883.3.72.6.2" {
		t.Errorf("acknowledgement sender is %s, expected our own OID", parsed.Sender.ID)
	}
	if parsed.Receiver.ID.Root != "1.2.3.4.5.6.7" {
		t.Errorf("acknowledgement receiver is %s, expected the original sender", parsed.Receiver.ID)
	}

	// An acknowledgement must not itself be acknowledged, or two systems acknowledge each other for ever.
	if parsed.WantsAcknowledgement() {
		t.Error("the acknowledgement asks to be acknowledged, which is an infinite loop between two systems")
	}
}

// TestTheOutcomeMappingMatchesVersionTwo keeps the two protocols from drifting.
//
// Perfuse's v2 rule is delivered AA, filtered AA, queued AA, partial AE, failed AE, unparseable AR. A v3 sender should get
// the same meaning for the same event, otherwise the same clinical situation produces different sender behaviour depending
// on which protocol the interface happens to use.
func TestTheOutcomeMappingMatchesVersionTwo(t *testing.T) {
	cases := map[Outcome]AckType{
		OutcomeDelivered:   AckAccept,
		OutcomeFiltered:    AckAccept,
		OutcomeQueued:      AckAccept,
		OutcomePartial:     AckError,
		OutcomeFailed:      AckError,
		OutcomeUnparseable: AckReject,
	}

	for outcome, want := range cases {
		if got := AckTypeFor(outcome); got != want {
			t.Errorf("outcome %d mapped to %q, expected %q", outcome, got, want)
		}
	}

	// The two that surprise people, stated as their own assertions because both have been argued about.
	//
	// A filtered message is accepted: the filter is the receiver's own decision and the sender did nothing wrong, so
	// reporting failure would have them retry something that will be declined again.
	if AckTypeFor(OutcomeFiltered) != AckAccept {
		t.Error("a filtered message must be accepted; the sender did nothing wrong")
	}
	// A queued message is accepted: Perfuse has taken responsibility for it, and saying otherwise produces a duplicate.
	if AckTypeFor(OutcomeQueued) != AckAccept {
		t.Error("a queued message must be accepted; we have taken responsibility for it")
	}

	// An outcome nothing names is an error, not an acceptance. Accepting a message whose fate is unknown loses it
	// silently; an error at worst produces a duplicate, which somebody can see.
	if AckTypeFor(Outcome(99)) != AckError {
		t.Error("an unknown outcome was acknowledged as accepted, which loses a message silently")
	}
}

// TestAnAcknowledgementRefusesToLieAboutWhoSentIt covers the required OID.
func TestAnAcknowledgementRefusesToLieAboutWhoSentIt(t *testing.T) {
	original, err := Parse([]byte(pdqQuery))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := AckFor(original, OutcomeDelivered, "", ""); err == nil {
		t.Error("an acknowledgement was generated with no sender device; the far end cannot tell our interfaces apart")
	}

	// And with no original at all.
	if _, err := AckFor(nil, OutcomeDelivered, "1.1.1", ""); err == nil {
		t.Error("an acknowledgement was generated for no message")
	}
}

// TestTheProcessingCodeIsEchoedRatherThanAsserted covers a subtle one.
//
// Acknowledging a debugging message as production would let a test system's traffic look live in the sender's own logs, which
// is exactly the confusion the processing code exists to prevent.
func TestTheProcessingCodeIsEchoedRatherThanAsserted(t *testing.T) {
	original, err := Parse([]byte(recordAdded)) // processingCode D
	if err != nil {
		t.Fatal(err)
	}
	if original.ProcessingCode != "D" {
		t.Fatalf("the fixture is not a debugging message: %q", original.ProcessingCode)
	}

	ack, err := AckFor(original, OutcomeDelivered, "1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse(ack)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ProcessingCode != "D" {
		t.Errorf("a debugging message was acknowledged as %q; its traffic would look live to the sender",
			parsed.ProcessingCode)
	}
	if parsed.IsProduction() {
		t.Error("the acknowledgement to a debugging message reads as production")
	}
}

// TestAnUntrustedIdentifierCannotWriteIntoOurAcknowledgement is an injection test.
//
// The identifiers in an acknowledgement come from a received message, and a received message is untrusted input. A root
// containing a quotation mark would close the attribute and let the sender write elements into a document this system puts
// its own name on - and that document is logged by the sender as our statement.
func TestAnUntrustedIdentifierCannotWriteIntoOurAcknowledgement(t *testing.T) {
	hostile := strings.Replace(pdqQuery,
		`<id root="1.2.840.114350.1.13.28.1.18.5.999" extension="Q001"/>`,
		`<id root="1.1" extension="Q&quot;/&gt;&lt;injected notAnElement=&quot;1&quot;/&gt;&lt;x y=&quot;"/>`, 1)

	original, err := Parse([]byte(hostile))
	if err != nil {
		t.Fatal(err)
	}

	ack, err := AckFor(original, OutcomeDelivered, "1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}

	// The hostile content must not have become markup.
	if strings.Contains(string(ack), "<injected") {
		t.Errorf("a received identifier wrote an element into our acknowledgement:\n%s", ack)
	}

	// And the result must still be readable, which is the half that proves the escaping did not simply corrupt it.
	if _, err := Parse(ack); err != nil {
		t.Errorf("the escaped acknowledgement is not well formed: %v\n%s", err, ack)
	}
}

// TestAMessageWithNoIdentifierIsStillAcknowledgedValidly covers the awkward case.
//
// Omitting the id element entirely produces an invalid acknowledgement, and a blank one tells the sender nothing. A stated
// null flavour says "the message you sent carried none", which is the truth.
func TestAMessageWithNoIdentifierIsStillAcknowledgedValidly(t *testing.T) {
	noID := `<?xml version="1.0"?>
<PRPA_IN201305UV02 xmlns="urn:hl7-org:v3">
  <creationTime value="20260822"/>
  <sender typeCode="SND"><device classCode="DEV"><id root="5.5.5"/></device></sender>
</PRPA_IN201305UV02>`

	original, err := Parse([]byte(noID))
	if err != nil {
		t.Fatal(err)
	}
	if original.ID.Presence == Present {
		t.Fatal("the fixture has an identifier after all")
	}

	ack, err := AckFor(original, OutcomeDelivered, "1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ack), `nullFlavor="NI"`) {
		t.Errorf("a missing original identifier was not stated as null:\n%s", ack)
	}
	if _, err := Parse(ack); err != nil {
		t.Errorf("not well formed: %v", err)
	}
}

// TestADetailReachesTheSender covers the explanation on a refusal.
func TestADetailReachesTheSender(t *testing.T) {
	original, err := Parse([]byte(pdqQuery))
	if err != nil {
		t.Fatal(err)
	}

	ack, err := AckFor(original, OutcomeUnparseable, "1.1.1", "segment PID is missing a patient identifier")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(ack), "segment PID is missing") {
		t.Errorf("the explanation did not reach the acknowledgement:\n%s", ack)
	}
	if !strings.Contains(string(ack), `code="CR"`) {
		t.Errorf("an unparseable message was not rejected:\n%s", ack)
	}
}

// TestTheAcknowledgementTimestampStatesAnOffset covers the hour-out problem.
//
// A timestamp with no offset leaves the receiver guessing our timezone, and there is no reason to make anybody guess about a
// time we know exactly.
func TestTheAcknowledgementTimestampStatesAnOffset(t *testing.T) {
	original, err := Parse([]byte(pdqQuery))
	if err != nil {
		t.Fatal(err)
	}

	fixed := time.Date(2026, 8, 22, 9, 30, 0, 0, time.FixedZone("CDT", -5*3600))
	ack, err := Ack(AckOptions{
		Original:          original,
		Type:              AckAccept,
		ReceiverDeviceOID: "1.1.1",
		Now:               fixed,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(ack), "20260822093000-0500") {
		t.Errorf("the timestamp does not state an offset:\n%s", ack)
	}

	parsed, err := Parse(ack)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.CreationTime.HasOffset {
		t.Error("the generated timestamp reads back without an offset")
	}
}
