package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// Declarative transformations on a SCRIPT channel.
//
// # Why the steps are hl7v3.Step
//
// A prescription and a v3 document are both XML addressed by the same path grammar, and internal/eprescribe already borrows
// hl7v3.Path for its channel filter with a comment saying v3 and CDA already use it. A third step type would have been a third set
// of answers to what //a/b[2]@c means.
//
// The steps that carry v3-only meaning are refused rather than quietly available. nullflavor is the one: it writes the attribute
// stating why a v3 value is absent, and a prescription has no equivalent, so a step writing it would produce a document the
// pharmacy system does not understand.
//
// # The case worth testing hardest
//
// A channel with steps and no transformer script. runTreeScriptStage returns the original bytes unless a transformer ran, because it
// has no way to know the tree changed underneath it - so without an explicit re-serialise the steps would apply to the tree and the
// bytes that arrived would be delivered. Work done, reported, discarded. That is the shape this project keeps finding, and it is why
// the first test here is the steps-only one rather than the combined one.

// newSteppedPrescriptionChannel builds a SCRIPT channel with a script block.
func newSteppedPrescriptionChannel(t *testing.T, block string, sink *recordingSender) *Channel {
	t.Helper()

	sink.name = "out"

	yaml := fmt.Sprintf(`
name: script-stepped
dataType: script
%s
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, block)

	cfg, err := config.Load(strings.NewReader(yaml), "scriptsteps.yaml")
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

// TestADeclarativeStepAloneChangesTheDeliveredPrescription is the one that catches the discard.
func TestADeclarativeStepAloneChangesTheDeliveredPrescription(t *testing.T) {
	sink := &recordingSender{}
	c := newSteppedPrescriptionChannel(t, `script:
  transformations:
    - set:
        path: //DrugDescription
        value: REDACTED`, sink)

	if _, err := c.handle(context.Background(), prescription(t, "C48676")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("the prescription was not delivered")
	}

	sent := sink.last()
	if !strings.Contains(sent, "REDACTED") {
		t.Errorf("the delivered prescription does not carry the step's change, so the step ran against a tree that was never "+
			"serialised:\n%s", sent)
	}
	if strings.Contains(sent, "Oxycodone") {
		t.Errorf("the delivered prescription still carries the original drug description:\n%s", sent)
	}
}

// TestStepsRunBeforeScriptsOnAPrescription fixes the order.
//
// The step writes a value and the transformer reads it back. If the script ran first it would read the original, so this fails on
// the wrong order rather than merely asserting both ran.
func TestStepsRunBeforeScriptsOnAPrescription(t *testing.T) {
	sink := &recordingSender{}
	c := newSteppedPrescriptionChannel(t, `script:
  transformations:
    - set:
        path: //DrugDescription
        value: FROM-STEP
scripts:
  language: lua
  transformer: |
    local rx = msg.child("Body").child("NewRx").child("MedicationPrescribed")
    local seen = rx.child("DrugDescription").text()
    rx.ensure("Note").setText("script saw: " .. seen)`, sink)

	if _, err := c.handle(context.Background(), prescription(t, "C48676")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("the prescription was not delivered")
	}

	sent := sink.last()
	if !strings.Contains(sent, "script saw: FROM-STEP") {
		t.Errorf("the script did not see the step's value, so the two ran in the wrong order or against different trees:\n%s",
			sent)
	}
}

// TestNullFlavorIsRefusedOnAPrescription keeps a v3 construct out of a format that has no equivalent.
func TestNullFlavorIsRefusedOnAPrescription(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: script-nullflavor
dataType: script
script:
  transformations:
    - nullflavor:
        path: //DrugDescription
        reason: NI
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "nullflavor.yaml")

	if err == nil {
		t.Fatal("a nullflavor step was accepted on a SCRIPT channel; it would write a v3 attribute a pharmacy system does not read")
	}
	if !strings.Contains(err.Error(), "clear") || !strings.Contains(err.Error(), "remove") {
		t.Errorf("the refusal should name what to use instead, got: %v", err)
	}
}

// TestAScriptBlockIsRefusedOnAnotherDataType stops the block being accepted where it does nothing.
//
// This was accepted when the block was first added, because the existing per-format refusals are written one arm at a time in a
// switch on dataType and a new block has to be added to every arm. The check for this one is outside the switch instead.
func TestAScriptBlockIsRefusedOnAnotherDataType(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: script-on-hl7
dataType: hl7
script:
  transformations:
    - set:
        path: //DrugDescription
        value: X
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "scriptonhl7.yaml")

	if err == nil {
		t.Fatal("a script block on an HL7 channel was accepted, so its transformations would validate and never run")
	}
	if !strings.Contains(err.Error(), "script") {
		t.Errorf("the refusal should name the block, got: %v", err)
	}
}
