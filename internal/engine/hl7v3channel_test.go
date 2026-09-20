package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/shadow"
)

// A PDQ query response, the shape a real feed sends.
const v3PDQ = `<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.72" extension="MSG00042"/>
  <creationTime value="20260822110000"/>
  <interactionId extension="PRPA_IN201306UV02"/>
  <processingCode code="P"/>
  <acceptAckCode code="AL"/>
  <controlActProcess classCode="CACT"><subject><registrationEvent><subject1>
    <patient classCode="PAT">
      <id root="2.16.840.1.113883.3.72.5.9.1" extension="PIX1234"/>
      <patientPerson>
        <name><given>Rosalind</given><family>Okonkwo-Hale</family></name>
        <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/>
        <birthTime value="19551014"/>
      </patientPerson>
    </patient>
  </subject1></registrationEvent></subject></controlActProcess>
</PRPA_IN201306UV02>`

// v3Channel builds a running v3 channel with a capturing destination.
//
// Shaped after newX12Channel so the two read the same way, and using the same captureDest rather than a second collector - a test
// helper per data type is how two of them quietly start behaving differently.
func v3Channel(t *testing.T, filter string) (*Channel, *captureDest) {
	t.Helper()

	acknowledge := true
	cfg := &config.Channel{
		Name:     "pdq",
		DataType: config.DataHL7v3,
		HL7v3: &config.HL7v3Options{
			Filter:       filter,
			Acknowledge:  &acknowledge,
			SenderDevice: "PERFUSE",
			SenderOID:    "2.16.840.1.113883.3.999",
		},
		Source: config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
		},
		Destinations: []config.Destination{
			{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("the channel is not valid: %v", err)
	}

	cap := &captureDest{}
	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return cap, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}

	return ch, cap
}

// TestAV3MessageIsReceivedRoutedAndAcknowledged is the end-to-end path.
//
// Until this passed, everything built for v3 was a library: paths could be worked out and nothing could carry a message.
func TestAV3MessageIsReceivedRoutedAndAcknowledged(t *testing.T) {
	ch, sink := v3Channel(t, "")

	ack, err := ch.handle(context.Background(), []byte(v3PDQ))
	if err != nil {
		t.Fatalf("handling failed: %v", err)
	}

	// It was delivered, unchanged. A v3 channel relays the document; nothing rewrites it.
	if len(sink.messages()) != 1 {
		t.Fatalf("the destination received %d messages", len(sink.messages()))
	}
	if string(sink.messages()[0]) != v3PDQ {
		t.Error("the document was altered on the way through")
	}

	// And it was acknowledged, with the identifier of the message it answers.
	if len(ack) == 0 {
		t.Fatal("no acknowledgement was produced")
	}
	text := string(ack)
	if !strings.Contains(text, "MCCI_IN000002UV01") {
		t.Errorf("the acknowledgement is not an MCCI_IN000002UV01: %s", text)
	}
	if !strings.Contains(text, "MSG00042") {
		t.Errorf("the acknowledgement does not name the message it answers: %s", text)
	}
	// CA, because everything was delivered.
	if !strings.Contains(text, `"CA"`) && !strings.Contains(text, "CA") {
		t.Errorf("the acknowledgement is not an accept: %s", text)
	}
}

// TestAV3FilterExcludesAMessageAndStillAcknowledges covers the point of the whole exercise.
//
// A filtered message is acknowledged positively, matching the v2 and X12 rule. The sender did nothing wrong, and a negative
// acknowledgement would make them retry a message we deliberately excluded.
func TestAV3FilterExcludesAMessageAndStillAcknowledges(t *testing.T) {
	// Only male patients, which this message is not.
	ch, sink := v3Channel(t, `//administrativeGenderCode@code == "M"`)

	ack, err := ch.handle(context.Background(), []byte(v3PDQ))
	if err != nil {
		t.Fatalf("handling failed: %v", err)
	}

	if len(sink.messages()) != 0 {
		t.Errorf("a filtered message was delivered anyway: %d", len(sink.messages()))
	}
	if len(ack) == 0 {
		t.Fatal("a filtered message was not acknowledged")
	}
	if strings.Contains(string(ack), "CR") {
		t.Errorf("a filtered message was rejected rather than accepted: %s", string(ack))
	}
}

// TestAV3FilterLetsAMatchingMessageThrough is the other half.
func TestAV3FilterLetsAMatchingMessageThrough(t *testing.T) {
	ch, sink := v3Channel(t, `//administrativeGenderCode@code == "F" and //birthTime@value < "19600101"`)

	if _, err := ch.handle(context.Background(), []byte(v3PDQ)); err != nil {
		t.Fatalf("handling failed: %v", err)
	}

	if len(sink.messages()) != 1 {
		t.Errorf("a matching message was not delivered: %d", len(sink.messages()))
	}
}

// TestAV3FilterOnANullFlavourWorksEndToEnd covers the construct that only exists in v3.
func TestAV3FilterOnANullFlavourWorksEndToEnd(t *testing.T) {
	const declined = `<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">
	  <id root="1.2.3" extension="MSG00043"/>
	  <interactionId extension="PRPA_IN201306UV02"/>
	  <controlActProcess><subject><registrationEvent><subject1><patient>
	    <patientPerson><birthTime nullFlavor="ASKU"/></patientPerson>
	  </patient></subject1></registrationEvent></subject></controlActProcess>
	</PRPA_IN201306UV02>`

	// Route only the ones where the patient was asked and did not know, which is a real triage case: those need a human,
	// and a message with no birth date at all needs a different one.
	ch, sink := v3Channel(t, `//birthTime nullflavor "ASKU"`)

	if _, err := ch.handle(context.Background(), []byte(declined)); err != nil {
		t.Fatalf("handling failed: %v", err)
	}
	if len(sink.messages()) != 1 {
		t.Errorf("the null-flavoured message was not routed: %d", len(sink.messages()))
	}

	// And the ordinary message, which has a real birth date, is not.
	if _, err := ch.handle(context.Background(), []byte(v3PDQ)); err != nil {
		t.Fatalf("handling failed: %v", err)
	}
	if len(sink.messages()) != 1 {
		t.Errorf("a message with a real birth date was routed by a nullFlavor filter: %d", len(sink.messages()))
	}
}

// TestAnUnreadableV3DocumentIsRejectedWithoutAnAcknowledgement covers the parse failure.
//
// No acknowledgement, deliberately: a v3 acknowledgement carries the identifier of the message it answers, and a document we could not
// parse has none we can trust. Inventing one or leaving it blank would leave a receiver unable to place the reply.
func TestAnUnreadableV3DocumentIsRejectedWithoutAnAcknowledgement(t *testing.T) {
	ch, sink := v3Channel(t, "")

	ack, err := ch.handle(context.Background(), []byte("MSH|^~\\&|LAB|HOSP|EMR|HOSP|20260822||ADT^A01|1|P|2.5"))
	if err == nil {
		t.Error("a v2 message was accepted by a v3 channel")
	}
	if len(ack) != 0 {
		t.Errorf("an unreadable document was acknowledged: %s", string(ack))
	}
	if len(sink.messages()) != 0 {
		t.Error("an unreadable document was delivered")
	}
}

// TestASoapEnvelopeIsUnwrapped covers how IHE actually sends these.
//
// A channel receiving ITI-47 over HTTP gets a SOAP envelope, not the interaction. Requiring an author to strip it by hand would mean a
// script in every such channel.
func TestASoapEnvelopeIsUnwrapped(t *testing.T) {
	wrapped := `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
	  <soap:Header/>
	  <soap:Body>` + strings.TrimPrefix(v3PDQ, `<?xml version="1.0"?>`) + `</soap:Body>
	</soap:Envelope>`

	ch, sink := v3Channel(t, `//administrativeGenderCode@code == "F"`)

	if _, err := ch.handle(context.Background(), []byte(wrapped)); err != nil {
		t.Fatalf("a SOAP-wrapped message failed: %v", err)
	}
	// The filter had to see inside the envelope for this to be delivered.
	if len(sink.messages()) != 1 {
		t.Errorf("a SOAP-wrapped message was not delivered: %d", len(sink.messages()))
	}
}

// TestTurningOffAcknowledgementIsHonoured covers the option.
func TestTurningOffAcknowledgementIsHonoured(t *testing.T) {
	off := false
	cfg := &config.Channel{
		Name:     "pdq-quiet",
		DataType: config.DataHL7v3,
		HL7v3:    &config.HL7v3Options{Acknowledge: &off},
		Source: config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
		},
		Destinations: []config.Destination{
			{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}
	if err := cfg.Validate(); err != nil {
		// With acknowledgement off, no sender identity is required - which is the point of the pointer.
		t.Fatalf("a channel with acknowledgement off did not validate: %v", err)
	}

	cap := &captureDest{}
	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return cap, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}

	ack, err := ch.handle(context.Background(), []byte(v3PDQ))
	if err != nil {
		t.Fatal(err)
	}
	if len(ack) != 0 {
		t.Errorf("acknowledgement is off but one was produced: %s", string(ack))
	}
	if len(cap.messages()) != 1 {
		t.Error("the message was not delivered")
	}
}

// TestASenderAskingForNoAcknowledgementIsHonoured covers acceptAckCode NE.
//
// Answering anyway is how two systems end up acknowledging each other's acknowledgements.
func TestASenderAskingForNoAcknowledgementIsHonoured(t *testing.T) {
	never := strings.Replace(v3PDQ, `<acceptAckCode code="AL"/>`, `<acceptAckCode code="NE"/>`, 1)
	if never == v3PDQ {
		t.Fatal("the test fixture did not contain an acceptAckCode to change")
	}

	ch, _ := v3Channel(t, "")

	ack, err := ch.handle(context.Background(), []byte(never))
	if err != nil {
		t.Fatal(err)
	}
	if len(ack) != 0 {
		t.Errorf("the sender asked for no acknowledgement and got one: %s", string(ack))
	}
}

// v3Recorder collects records so a test can assert what reaches the message browser and the metric labels.
type v3Recorder struct {
	mu   sync.Mutex
	seen []MessageRecord
}

func (r *v3Recorder) RecordMessage(_ context.Context, record MessageRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seen = append(r.seen, record)
}

func (r *v3Recorder) records() []MessageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]MessageRecord, len(r.seen))
	copy(out, r.seen)

	return out
}

// TestTheRecordDescribesAV3MessageWithoutPatientData covers what goes in the browser and the metric labels.
func TestTheRecordDescribesAV3MessageWithoutPatientData(t *testing.T) {
	ch, _ := v3Channel(t, "")

	rec := &v3Recorder{}
	ch.SetRecorder(rec)

	if _, err := ch.handle(context.Background(), []byte(v3PDQ)); err != nil {
		t.Fatal(err)
	}

	records := rec.records()
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}
	r := records[0]

	if r.MessageType != "PRPA_IN201306UV02" {
		t.Errorf("MessageType = %q, want the interaction", r.MessageType)
	}
	if r.ControlID != "MSG00042" {
		t.Errorf("ControlID = %q, want the message identifier", r.ControlID)
	}

	// Nothing identifying a patient may reach these fields: they end up in metric labels and logs.
	for _, field := range []string{r.MessageType, r.ControlID, r.TriggerEvent} {
		for _, leak := range []string{"Rosalind", "Okonkwo", "PIX1234", "19551014"} {
			if strings.Contains(field, leak) {
				t.Errorf("%q reached a record field that becomes a metric label", leak)
			}
		}
	}
}

// v3TransformChannel builds a v3 channel with declarative steps.
func v3TransformChannel(t *testing.T, steps []hl7v3.Step) (*Channel, *captureDest) {
	t.Helper()

	acknowledge := true
	cfg := &config.Channel{
		Name:     "pdq",
		DataType: config.DataHL7v3,
		HL7v3: &config.HL7v3Options{
			Acknowledge:     &acknowledge,
			SenderDevice:    "PERFUSE",
			SenderOID:       "2.16.840.1.113883.3.999",
			Transformations: steps,
		},
		Source: config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
		},
		Destinations: []config.Destination{
			{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("the channel is not valid: %v", err)
	}

	cap := &captureDest{}
	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return cap, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}

	return ch, cap
}

// TestAV3TransformationChangesWhatIsDelivered is the end-to-end path for transformations.
//
// The property that matters is the last one: what the destination receives has to be the transformed document. A
// transformation that changes an in-memory tree and delivers the original bytes is the most plausible way to build this
// wrong, and it would pass every unit test in the hl7v3 package.
func TestAV3TransformationChangesWhatIsDelivered(t *testing.T) {
	ch, sink := v3TransformChannel(t, []hl7v3.Step{
		{
			Description: "mask the birth date for a research feed",
			NullFlavor:  &hl7v3.V3NullFlavorStep{Path: "//birthTime", Reason: "MSK"},
		},
	})

	ack, err := ch.handle(context.Background(), []byte(v3PDQ))
	if err != nil {
		t.Fatalf("handling failed: %v", err)
	}
	if !strings.Contains(string(ack), "CA") {
		t.Errorf("the acknowledgement is not a CA:\n%s", ack)
	}

	got := sink.messages()
	if len(got) != 1 {
		t.Fatalf("the destination received %d messages", len(got))
	}

	body := string(got[0])
	if !strings.Contains(body, `nullFlavor="MSK"`) {
		t.Errorf("the delivered document does not carry the masked birth date, so the transformation "+
			"changed a tree in memory and delivered the original bytes:\n%s", body)
	}
	if strings.Contains(body, "19551014") {
		t.Errorf("the original birth date is still in the delivered document:\n%s", body)
	}
}

// TestAFailingV3TransformationDoesNotDeliver covers the failure path.
//
// A half-transformed clinical message delivered as though it were complete is worse than one that did not go: the receiver
// cannot tell it is looking at a partial record, and the sender is told it succeeded.
func TestAFailingV3TransformationDoesNotDeliver(t *testing.T) {
	ch, sink := v3TransformChannel(t, []hl7v3.Step{
		{
			Description: "this cannot work on this message",
			Copy:        &hl7v3.V3CopyStep{From: "//deceasedTime", To: "//birthTime@value"},
		},
	})

	ack, err := ch.handle(context.Background(), []byte(v3PDQ))
	if err != nil {
		t.Fatalf("handling returned an error rather than a negative acknowledgement: %v", err)
	}

	if got := sink.messages(); len(got) != 0 {
		t.Errorf("a message was delivered despite the transformation failing:\n%s", got[0])
	}

	// CE rather than CA. The v3 codes are CA, CE and CR, not the v2 AA, AE and AR - this test asserted AE first and
	// the code was right.
	if !strings.Contains(string(ack), `code="CE"`) {
		t.Errorf("the acknowledgement does not report the failure:\n%s", ack)
	}

	// And it says which step, because "transformation failed" sends somebody to read the whole channel.
	if !strings.Contains(string(ack), "this cannot work on this message") {
		t.Errorf("the acknowledgement does not name the step that failed:\n%s", ack)
	}
}

// TestAV3CandidateCanBeComparedAgainstTheLiveChannel is the whole of item 12's shadow half.
//
// Refused at load before this, because there was no v3 transform path and no XML-aware diff. Both exist now, so a v3 change can be
// checked empirically instead of by reading it - which is the difference between "I think this is safe" and "this altered four
// fields on 231 of 400 messages".
func TestAV3CandidateCanBeComparedAgainstTheLiveChannel(t *testing.T) {
	base := func(steps string) *config.Channel {
		t.Helper()
		yes := true
		cfg := &config.Channel{
			Name:     "pdq",
			DataType: config.DataHL7v3,
			HL7v3: &config.HL7v3Options{
				Acknowledge:  &yes,
				SenderDevice: "PERFUSE",
				SenderOID:    "2.16.840.1.113883.3.999",
			},
			Source: config.Source{
				Type: config.SourceHTTP,
				HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
			},
			Destinations: []config.Destination{
				{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
			},
		}
		if steps == "mask" {
			cfg.HL7v3.Transformations = []hl7v3.Step{{
				Description: "mask the birth date",
				NullFlavor:  &hl7v3.V3NullFlavorStep{Path: "//birthTime", Reason: "MSK"},
			}}
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("the channel is not valid: %v", err)
		}

		return cfg
	}

	report, err := Replay(context.Background(), base(""), base("mask"),
		[][]byte{[]byte(v3PDQ)}, shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatalf("replaying a v3 channel: %v", err)
	}

	if report.Examined != 1 {
		t.Fatalf("examined %d messages, want 1 - a v3 document was not readable by the replay path",
			report.Examined)
	}
	if report.Changed != 1 {
		t.Fatalf("reported %d changed, want 1: %+v", report.Changed, report)
	}

	// And the difference names a v3 path rather than an HL7 v2 one. A report full of segment paths on a v3 channel
	// would be worse than no report, because every path in it would be wrong and none would look wrong.
	var paths []string
	for p := range report.FieldCounts {
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		t.Fatalf("no fields were reported as differing: %+v", report)
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("the report names %q, which is not a v3 path - the v2 diff was used on a v3 "+
				"channel", p)
		}
	}
}

// v3ScriptChannel builds a v3 channel that runs scripts.
func v3ScriptChannel(t *testing.T, filter, transformer string) (*Channel, *captureDest) {
	t.Helper()

	yes := true
	cfg := &config.Channel{
		Name:     "pdq",
		DataType: config.DataHL7v3,
		HL7v3: &config.HL7v3Options{
			Acknowledge:  &yes,
			SenderDevice: "PERFUSE",
			SenderOID:    "2.16.840.1.113883.3.999",
		},
		Scripts: &config.Scripts{Filter: filter, Transformer: transformer},
		Source: config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
		},
		Destinations: []config.Destination{
			{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the channel is not valid: %v", err)
	}

	cap := &captureDest{}
	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return cap, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}

	return ch, cap
}

// TestAV3ChannelCanRunScripts covers item 11.
//
// Refused at load before this, on the grounds that the script layer serialises HL7 v2 only. The reason turned out to be narrower than it
// looked: the script layer already works on an XML tree because Mirth's scripts do, and it is the v2 conversion into and out of that tree
// that did not apply. A v3 document is already a tree.
func TestAV3ChannelCanRunScripts(t *testing.T) {
	t.Run("a filter script can exclude a message", func(t *testing.T) {
		ch, sink := v3ScriptChannel(t,
			`return msg['controlActProcess']['subject']['registrationEvent']['subject1']['patient']`+
				`['patientPerson']['administrativeGenderCode']['@code'].toString() == 'M'`, "")

		if _, err := ch.handle(context.Background(), []byte(v3PDQ)); err != nil {
			t.Fatal(err)
		}
		if got := sink.messages(); len(got) != 0 {
			t.Errorf("a message the filter script rejected was delivered anyway:\n%s", got[0])
		}
	})

	t.Run("a filter script can accept a message", func(t *testing.T) {
		ch, sink := v3ScriptChannel(t,
			`return msg['controlActProcess']['subject']['registrationEvent']['subject1']['patient']`+
				`['patientPerson']['administrativeGenderCode']['@code'].toString() == 'F'`, "")

		if _, err := ch.handle(context.Background(), []byte(v3PDQ)); err != nil {
			t.Fatal(err)
		}
		if got := sink.messages(); len(got) != 1 {
			t.Fatalf("the destination received %d messages, want 1", len(got))
		}
	})

	t.Run("a transformer script changes what is delivered", func(t *testing.T) {
		// The property that matters, and the one a plumbing-only implementation would miss: the script mutates a tree
		// in memory, and the destination sends bytes.
		ch, sink := v3ScriptChannel(t, "",
			`msg['controlActProcess']['subject']['registrationEvent']['subject1']['patient']`+
				`['patientPerson']['administrativeGenderCode']['@code'] = 'M';`)

		if _, err := ch.handle(context.Background(), []byte(v3PDQ)); err != nil {
			t.Fatal(err)
		}

		got := sink.messages()
		if len(got) != 1 {
			t.Fatalf("the destination received %d messages", len(got))
		}
		body := string(got[0])
		if !strings.Contains(body, `code="M"`) {
			t.Errorf("the delivered document does not carry the script's change, so the script mutated a "+
				"tree and the original bytes were sent:\n%s", body)
		}
	})

	t.Run("a script that fails does not deliver", func(t *testing.T) {
		ch, sink := v3ScriptChannel(t, "", `throw new Error('deliberate');`)

		ack, err := ch.handle(context.Background(), []byte(v3PDQ))
		if err != nil {
			t.Fatalf("handling returned an error rather than a negative acknowledgement: %v", err)
		}
		if got := sink.messages(); len(got) != 0 {
			t.Errorf("a message was delivered despite the script failing:\n%s", got[0])
		}
		if !strings.Contains(string(ack), `code="CE"`) {
			t.Errorf("the acknowledgement does not report the failure:\n%s", ack)
		}
	})
}
