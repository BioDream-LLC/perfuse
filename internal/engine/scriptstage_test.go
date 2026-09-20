package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The filter and transformer scripts on a SCRIPT channel.
//
// # Why these are separate from the declarative filter tests
//
// A prescription is XML, so it gets the tree binding v3 uses rather than the path binding X12 and NCPDP got: the script sees the
// document itself. That was the last gap in the slot table, and it turned out to need no new code - runTreeScriptStage already
// took an xtree.Node and knew nothing about the format, and eprescribe.ParseTree already existed for the declarative filter.
//
// So what needs proving here is not that a tree binding works, which the v3 tests cover, but that a prescription reaches it: that
// the branch in handlePharmacy is wired, that a refusal stops delivery, and that a transformed document is re-checked before it is
// sent. The last of those is the one that matters most, because it is the guard that makes handing a script a whole document safe.

// newScriptedPrescriptionChannel builds a SCRIPT channel with a scripts block.
//
// Separate from newFilteredScriptChannel rather than growing a parameter, because the two configure different things - one the
// expression filter, one the script slots - and a single helper taking both would need a caller to pass an empty string for
// whichever it did not want.
func newScriptedPrescriptionChannel(t *testing.T, scripts string, sink *recordingSender) *Channel {
	t.Helper()

	sink.name = "out"

	// The scripts block is indented into place by the caller's raw string, so the fixture only supplies the slots.
	yaml := fmt.Sprintf(`
name: script-scripted
dataType: script
scripts:
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
`, scripts)

	cfg, err := config.Load(strings.NewReader(yaml), "script.yaml")
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
		t.Fatalf("expected one channel, got %d", len(chans))
	}

	return chans[0]
}

// TestATransformerScriptThatBreaksThePrescriptionFailsRatherThanDelivering is the important one.
//
// A script holding a whole document can write something that serialises into text no parser will read - an element name with a
// space in it is the easy case. runTreeScriptStage re-parses its own output for exactly this reason, and without that check the
// damage would surface at the receiver, where it reads as the receiver's problem rather than as a script fault here.
//
// append is the way in, because it takes the element name as a string and does not validate it. That is deliberate on its part:
// the check belongs at the end of the stage, once, rather than in every writer.
func TestATransformerScriptThatBreaksThePrescriptionFailsRatherThanDelivering(t *testing.T) {
	sink := &recordingSender{}
	c := newScriptedPrescriptionChannel(t, `  language: lua
  transformer: |
    msg.append("not a valid name")`, sink)

	if _, err := c.handle(context.Background(), prescription(t, "C48676")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 0 {
		t.Fatalf("a prescription the transformer made unparseable was delivered anyway: %q", sink.last())
	}
	if got := c.lastOutcome.get(); got != Failed {
		t.Errorf("outcome was %v, want Failed: a document that no longer parses is a failure, not a filtered message - "+
			"the sender wrote something wrong and needs to know", got)
	}
}

// TestAFilterScriptExcludesAPrescription proves the filter reaches the tree and its answer is honoured.
//
// The script reads a nested element rather than returning a bare false, because a bare false would pass even if the tree were
// never bound - and an unbound tree is the failure this branch existed to fix.
func TestAFilterScriptExcludesAPrescription(t *testing.T) {
	sink := &recordingSender{}
	c := newScriptedPrescriptionChannel(t, `  language: lua
  filter: |
    local coded = msg.child("Body").child("NewRx").child("MedicationPrescribed").child("DrugCoded")
    return coded.child("DEASchedule").text() ~= "C48675"`, sink)

	if _, err := c.handle(context.Background(), prescription(t, "C48675")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 0 {
		t.Fatalf("a schedule II prescription the filter script rejected was delivered")
	}
	if got := c.lastOutcome.get(); got != Filtered {
		t.Errorf("outcome was %v, want Filtered", got)
	}

	// The same channel must still pass what the filter accepts, or the assertion above would also hold for a filter that
	// rejected everything - including one whose tree was empty.
	if _, err := c.handle(context.Background(), prescription(t, "C48676")); err != nil {
		t.Fatalf("handling the schedule III prescription: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("a schedule III prescription was not delivered; the filter is rejecting everything, which would make the " +
			"assertion above pass for the wrong reason")
	}
}

// TestATransformerScriptChangesTheDeliveredPrescription asserts on the delivered bytes.
//
// On what was sent rather than on the tree, because a transformer that edits a tree nobody re-serialises is the exact defect this
// project keeps finding: a change that is made, reported, and never reaches the wire.
func TestATransformerScriptChangesTheDeliveredPrescription(t *testing.T) {
	sink := &recordingSender{}
	c := newScriptedPrescriptionChannel(t, `  language: lua
  transformer: |
    local rx = msg.child("Body").child("NewRx").child("MedicationPrescribed")
    rx.child("DrugDescription").setText("REDACTED")`, sink)

	if _, err := c.handle(context.Background(), prescription(t, "C48676")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("the prescription was not delivered")
	}

	sent := sink.last()
	if !strings.Contains(sent, "REDACTED") {
		t.Errorf("the delivered prescription does not carry the transformer's change: %q", sent)
	}
	if strings.Contains(sent, "Oxycodone") {
		t.Errorf("the delivered prescription still carries the original drug description, so the change was made to a tree "+
			"that was not the one serialised: %q", sent)
	}
}

// TestAPrescriptionScriptCanBeWrittenInJavaScript records a difference between the two bindings.
//
// SCRIPT gets both languages because it goes through the tree binding. The path binding X12, NCPDP and delimited use refuses
// anything but Lua, on the grounds that a second implementation of the same path vocabulary would drift precisely where it
// matters - a neutralised write, an ambiguous path.
//
// Worth asserting rather than leaving implied, because the two are configured identically in the channel file and somebody
// reading only scriptSlotsRun would have no way to tell that language availability differs between them.
func TestAPrescriptionScriptCanBeWrittenInJavaScript(t *testing.T) {
	sink := &recordingSender{}
	c := newScriptedPrescriptionChannel(t, `  language: javascript
  transformer: |
    msg['Body']['NewRx']['MedicationPrescribed']['DrugDescription'] = 'REDACTED';`, sink)

	if _, err := c.handle(context.Background(), prescription(t, "C48676")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("the prescription was not delivered; a JavaScript transformer should run on a SCRIPT channel")
	}
	if sent := sink.last(); !strings.Contains(sent, "REDACTED") {
		t.Errorf("the JavaScript transformer's change did not reach the wire: %q", sent)
	}
}
