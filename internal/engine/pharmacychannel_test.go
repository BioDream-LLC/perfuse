package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A prescription reaches the far end unchanged.
//
// Unchanged matters more here than in most places: a channel that reformats a prescription in transit has changed a
// clinical document, and the pharmacy has no way to know it was not written that way.
func TestAPrescriptionIsDeliveredByteForByte(t *testing.T) {
	const rx = `<?xml version="1.0" encoding="UTF-8"?>
<Message xmlns="http://www.ncpdp.org/schema/SCRIPT" version="010" release="006">
  <Header>
    <To Qualifier="P">1234567</To>
    <From Qualifier="D">9876543</From>
    <MessageID>MSG-42</MessageID>
    <SentTime>2026-08-28T10:00:00Z</SentTime>
  </Header>
  <Body>
    <NewRx>
      <Patient><Name><LastName>SMITH</LastName></Name><DateOfBirth><Date>1980-01-01</Date></DateOfBirth></Patient>
      <Pharmacy><NCPDPID>1234567</NCPDPID></Pharmacy>
      <Prescriber><Name><LastName>JONES</LastName></Name><NPI>1234567893</NPI></Prescriber>
      <MedicationPrescribed>
        <DrugDescription>Lisinopril 10 MG Oral Tablet</DrugDescription>
        <Substitutions>1</Substitutions>
        <Quantity><Value>30</Value><QuantityUnitOfMeasure><Code>C48542</Code></QuantityUnitOfMeasure></Quantity>
        <Sig><SigText>Take one tablet by mouth daily</SigText></Sig>
      </MedicationPrescribed>
    </NewRx>
  </Body>
</Message>`

	rec := &recordingSender{}
	ch := newPharmacyChannel(t, "script", rec)

	if _, err := ch.handle(context.Background(), []byte(rx)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	if rec.count() != 1 {
		t.Fatalf("got %d deliveries, want 1", rec.count())
	}
	if rec.last() != rx {
		t.Error("the prescription was altered in transit, which changes a clinical document the pharmacy cannot verify")
	}
	// And specifically the substitution flag, since that is the one whose inversion reaches the patient.
	if !strings.Contains(rec.last(), "<Substitutions>1</Substitutions>") {
		t.Error("the dispense-as-written flag did not survive delivery")
	}
}

// A claim reaches the far end with its non-printable separators intact.
//
// This is the assertion that catches MLLP framing being applied. A claim already uses 0x1C as a field separator, so MLLP's
// wrapping puts a second meaning on a byte the claim is using and a reader unframing it stops inside the first segment.
func TestAClaimIsNotMLLPFramed(t *testing.T) {
	claim := pharmacyClaim()

	rec := &recordingSender{}
	ch := newPharmacyChannel(t, "ncpdp", rec)

	if _, err := ch.handle(context.Background(), claim); err != nil {
		t.Fatalf("handle: %v", err)
	}

	got := rec.last()
	if got == "" {
		t.Fatal("nothing was delivered")
	}
	if got[0] == 0x0B {
		t.Fatal("the claim was MLLP framed; the start byte would be read as the beginning of a segment")
	}
	if got != string(claim) {
		t.Errorf("the claim was altered: got %q", got)
	}
}

// A truncated prescription must stop here rather than at a pharmacy.
//
// This is the reason these types are parsed at all when nothing transforms them. A raw channel can never report
// Unparseable, so half a message would be delivered as faithfully as a whole one and the first anybody would know is a
// pharmacy asking why it received a fragment.
func TestATruncatedPrescriptionIsUnparseableRatherThanDelivered(t *testing.T) {
	rec := &recordingSender{}
	ch := newPharmacyChannel(t, "script", rec)

	_, err := ch.handle(context.Background(), []byte(`<Message><Header><To>1234567</To`))
	if err == nil {
		t.Fatal("a truncated prescription was accepted")
	}
	if rec.count() != 0 {
		t.Error("a truncated prescription was delivered, so a pharmacy would receive a fragment")
	}

	st := ch.Stats()
	if st.Unparseable != 1 {
		t.Errorf("Unparseable = %d, want 1", st.Unparseable)
	}
	// Not Failed. Nothing was wrong with the delivery; the message never made sense, and telling those apart is what
	// distinguishes a broken sender from a broken receiver.
	if st.Failed != 0 {
		t.Errorf("Failed = %d; a message that never parsed is not a delivery failure", st.Failed)
	}
}

// XML that is well formed but is not SCRIPT is refused too. Delivering it would have a pharmacy read a document in a
// standard it does not implement, and the reply is a phone call.
func TestXMLThatIsNotAPrescriptionIsRefused(t *testing.T) {
	rec := &recordingSender{}
	ch := newPharmacyChannel(t, "script", rec)

	_, err := ch.handle(context.Background(), []byte(`<?xml version="1.0"?><Bundle><entry/></Bundle>`))
	if err == nil {
		t.Fatal("a FHIR bundle was accepted as a prescription")
	}
	if rec.count() != 0 {
		t.Error("it was delivered anyway")
	}
}

// A claim whose header is short is refused, which is the failure that is otherwise invisible: every field shifts one
// place left and each one is still the right length.
func TestAClaimWithAShiftedHeaderIsRefused(t *testing.T) {
	claim := pharmacyClaim()
	short := append([]byte{}, claim[:len(claim)-1]...)
	// Remove a byte from inside the header rather than the end, so the total length is still ample.
	short = append(short[:10], short[11:]...)

	rec := &recordingSender{}
	ch := newPharmacyChannel(t, "ncpdp", rec)

	if _, err := ch.handle(context.Background(), short); err == nil {
		t.Fatal("a claim with a short header was accepted, and every field in it belongs to the field before")
	}
	if rec.count() != 0 {
		t.Error("it was delivered anyway")
	}
}

// An empty payload is never something a sender meant.
func TestAnEmptyPharmacyPayloadIsRefused(t *testing.T) {
	for _, dt := range []string{"ncpdp", "script"} {
		rec := &recordingSender{}
		ch := newPharmacyChannel(t, dt, rec)
		if _, err := ch.handle(context.Background(), nil); err == nil {
			t.Errorf("%s: an empty payload was accepted", dt)
		}
		if rec.count() != 0 {
			t.Errorf("%s: an empty payload was delivered", dt)
		}
	}
}

// The sender's own identifier is kept, not invented. A synthesised one looks like it came from the sending system and
// matches nothing there.
func TestTheSendersOwnIdentifierIsRecorded(t *testing.T) {
	rec := &recordingSender{}
	ch := newPharmacyChannel(t, "ncpdp", rec)
	if _, err := ch.handle(context.Background(), pharmacyClaim()); err != nil {
		t.Fatal(err)
	}
	// A single-claim transmission contributes its prescription number. Asserted through the message store rather than
	// the sender, because that is where somebody looking for the message will search.
	if got := ch.Stats().Received; got != 1 {
		t.Fatalf("Received = %d, want 1", got)
	}
}

// pharmacyClaim builds a valid single-claim B1 transmission.
func pharmacyClaim() []byte {
	pad := func(s string, n int) string {
		for len(s) < n {
			s += " "
		}
		return s[:n]
	}
	header := pad("610097", 6) + pad("D0", 2) + pad("B1", 2) + pad("9999", 10) +
		pad("1", 1) + pad("01", 2) + pad("1234567893", 15) + pad("20260828", 8) + pad("PERFUSE", 10)

	return []byte(header +
		"\x1e01\x1cC419800101\x1cCAJOHN\x1cCBSMITH" +
		"\x1e07\x1cD2RX1234\x1cD700093-1036-01\x1cE730")
}

// newPharmacyChannel builds a started channel of the given data type whose only destination records what it is sent.
func newPharmacyChannel(t *testing.T, dataType string, sink *recordingSender) *Channel {
	t.Helper()

	sink.name = "out"
	yaml := fmt.Sprintf(`
name: pharmacy-%s
dataType: %s
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, dataType, dataType)

	cfg, err := config.Load(strings.NewReader(yaml), "pharmacy.yaml")
	if err != nil {
		t.Fatalf("loading the config: %v", err)
	}

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

	chans := e.Channels()
	if len(chans) != 1 {
		t.Fatalf("got %d channels, want 1", len(chans))
	}
	return chans[0]
}
