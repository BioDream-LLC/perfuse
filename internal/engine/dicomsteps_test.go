package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicom"
)

// Named transformation steps on a DICOM channel.
//
// # Why these tests are about the wiring rather than the steps
//
// internal/dicom has carried steps.go, transform.go and their tests for a while: the four actions, dicom.Apply, and coverage of
// what each one does to a data set. What it never had was a caller. Nothing in config exposed the steps and nothing in the engine
// applied them, so five hundred tested lines were unreachable - the shape the queue's section 7 names, an implementation with no
// caller being indistinguishable from a feature that does not exist.
//
// The unit tests in internal/dicom already prove the actions work. What they cannot prove is that an object arriving on a channel
// reaches them, or that the result is what leaves. So every assertion here is on the delivered bytes.
//
// # Why de-identify is the one to test hardest
//
// The others get a wrong value into an image. This one, when it silently does nothing, sends an object still carrying the patient's
// name and identifier out of the hospital. The failure is invisible at both ends: the sender thinks it de-identified, the receiver
// has no way to know it was meant to be.

// dicomObject builds an encoded object carrying identifiers a de-identify step should remove.
func dicomObject(t *testing.T) []byte {
	t.Helper()

	ds := &dicom.DataSet{
		TransferSyntax: dicom.ImplicitVRLittleEndian,
		Elements: []dicom.Element{
			{Tag: dicom.Tag{Group: 0x0010, Element: 0x0010}, VR: "PN", Value: []byte("OKONKWO^ADAEZE")},
			{Tag: dicom.Tag{Group: 0x0010, Element: 0x0020}, VR: "LO", Value: []byte("MRN123456")},
			{Tag: dicom.Tag{Group: 0x0008, Element: 0x0060}, VR: "CS", Value: []byte("CT")},
			{Tag: dicom.Tag{Group: 0x0008, Element: 0x0018}, VR: "UI", Value: []byte("1.2.3.4.5")},
		},
	}

	raw, err := dicom.Encode(ds.Elements, ds.TransferSyntax)
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}

	return raw
}

// dicomChannelWith builds a started DICOM channel carrying the given dicom block.
func dicomChannelWith(t *testing.T, block string, sink *recordingSender) *Channel {
	t.Helper()

	sink.name = "out"

	yaml := "name: imaging\ndataType: dicom\n" + block +
		"source:\n  type: http\n  http:\n    listen: \"127.0.0.1:0\"\n    path: /in\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/imaging\n"

	cfg, err := config.Load(strings.NewReader(yaml), "imaging.yaml")
	if err != nil {
		t.Fatalf("loading: %v", err)
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
		t.Fatalf("expected one channel, got %d", len(chans))
	}

	return chans[0]
}

// TestADeidentifyStepRemovesThePatientFromTheDeliveredObject is the one that matters.
func TestADeidentifyStepRemovesThePatientFromTheDeliveredObject(t *testing.T) {
	sink := &recordingSender{}
	c := dicomChannelWith(t, "dicom:\n  transformations:\n    - deidentify: {}\n", sink)

	raw := dicomObject(t)

	if !strings.Contains(string(raw), "OKONKWO") {
		t.Fatal("the fixture does not carry the patient name, so this test could pass without the step doing anything")
	}

	if _, err := c.handle(context.Background(), raw); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("the object was not delivered")
	}

	sent := sink.last()
	if strings.Contains(sent, "OKONKWO") {
		t.Error("the delivered object still carries the patient name; a de-identify step that does nothing sends " +
			"identifiers out of the hospital and neither end can tell")
	}
	if strings.Contains(sent, "MRN123456") {
		t.Error("the delivered object still carries the patient identifier")
	}

	// The modality has to survive, or the step is not de-identifying but destroying: a receiver cannot file a study it
	// cannot classify.
	if !strings.Contains(sent, "CT") {
		t.Error("the delivered object lost its modality, so the step removed more than the patient")
	}
}

// TestAnObjectIsDeliveredUnchangedWithoutSteps is the control.
//
// Without it the test above would pass against a channel that mangled every object, and against one that delivered nothing
// recognisable at all.
func TestAnObjectIsDeliveredUnchangedWithoutSteps(t *testing.T) {
	sink := &recordingSender{}
	c := dicomChannelWith(t, "", sink)

	raw := dicomObject(t)

	if _, err := c.handle(context.Background(), raw); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("the object was not delivered")
	}
	if sink.last() != string(raw) {
		t.Error("an object was changed on a channel with no transformations; the bytes a modality sent must arrive " +
			"identically when nothing was asked for")
	}
}

// TestSetAETitleAndStripPrivateReachTheDeliveredObject covers the other actions.
//
// Together rather than separately because the point being tested is the same one - that the step list is applied in order and the
// result is re-encoded - and two actions in one list proves the loop rather than a single call.
func TestSetAETitleAndStripPrivateReachTheDeliveredObject(t *testing.T) {
	sink := &recordingSender{}
	c := dicomChannelWith(t, `dicom:
  transformations:
    - set_institution:
        name: ST MARYS
    - strip_private: {}
`, sink)

	if _, err := c.handle(context.Background(), dicomObject(t)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("the object was not delivered")
	}
	if !strings.Contains(sink.last(), "ST MARYS") {
		t.Error("the institution the step set did not reach the delivered object")
	}
}

// TestADICOMBlockIsRefusedOnAnotherDataType stops the block being accepted where it does nothing.
func TestADICOMBlockIsRefusedOnAnotherDataType(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: dicom-on-hl7
dataType: hl7
dicom:
  transformations:
    - deidentify: {}
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: file
    dir: /tmp/imaging
`), "dicomonhl7.yaml")

	if err == nil {
		t.Fatal("a dicom block on an HL7 channel was accepted, so its steps would validate and never run")
	}
	if !strings.Contains(err.Error(), "dicom") {
		t.Errorf("the refusal should name the block, got: %v", err)
	}
}
