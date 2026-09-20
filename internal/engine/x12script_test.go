package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// x12Claim is one 837 with a single claim. SE01 must equal the ST-through-SE segment count.
const x12Claim = "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       *260831*1200*^*00501*000000001*0*P*:~" +
	"GS*HC*SENDER*RECEIVER*20260831*1200*1*X*005010X222A1~ST*837*0001*005010X222A1~" +
	"BHT*0019*00*0123*20260831*1200*CH~CLM*PATIENT001*500***11:B:1*Y*A*Y*Y~" +
	"REF*D9*CLAIM123~SE*5*0001~GE*1*1~IEA*1*000000001~"

func x12ScriptChannel(t *testing.T, scripts string, sink *recordingSender) *Channel {
	t.Helper()

	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(`
name: x12-scripted
dataType: x12
`+scripts+`
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: tcp
    tcp:
      address: 127.0.0.1:1
      framing: length
`), "x12script.yaml")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	return startChannelFor(t, cfg, sink)
}

// TestAnX12TransformerScriptChangesTheDeliveredClaim is the end-to-end proof.
//
// The unit tests prove the path binding reads and writes through a steps.Accessor. They cannot prove the X12 handler invokes it,
// or that what the script produced is what gets sent - and that gap is the shape this session found five times over: correct
// pieces, nothing joining them.
//
// So this asserts on the bytes that reached the destination.
func TestAnX12TransformerScriptChangesTheDeliveredClaim(t *testing.T) {
	sink := &recordingSender{}
	c := x12ScriptChannel(t, `scripts:
  language: lua
  transformer: |
    msg.set("REF02", "SCRIPTED")`, sink)

	if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() == 0 {
		t.Fatal("nothing was delivered, so the script cannot be judged")
	}

	delivered := sink.last()
	if strings.Contains(delivered, "CLAIM123") {
		t.Error("the delivered interchange still carries the original REF02, so the script wrote to a copy the handler discarded")
	}
	if !strings.Contains(delivered, "SCRIPTED") {
		t.Errorf("the delivered interchange does not carry the written value:\n%s", delivered)
	}
	// The rest has to survive: a transformer that replaced the whole interchange would satisfy both assertions above.
	if !strings.Contains(delivered, "PATIENT001") {
		t.Error("the claim identifier is missing, so more changed than the script asked for")
	}
	if !strings.Contains(delivered, "ISA*00*") {
		t.Error("the envelope is missing from the delivered interchange")
	}
}

// TestAnX12FilterScriptDecidesDelivery covers the read side, both verdicts.
func TestAnX12FilterScriptDecidesDelivery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		deliver bool
	}{
		{"keeps a matching claim", `return msg.get("CLM02") == "500"`, true},
		{"excludes a non-matching claim", `return msg.get("CLM02") == "999"`, false},
		{"reads a segment other than CLM", `return msg.get("REF02") == "CLAIM123"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSender{}
			c := x12ScriptChannel(t, `scripts:
  language: lua
  filter: |
    `+tc.body, sink)

			if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
				t.Fatalf("handling: %v", err)
			}

			if got := sink.count() > 0; got != tc.deliver {
				t.Errorf("delivered=%v, want %v", got, tc.deliver)
			}
		})
	}
}

// TestAnX12ScriptSeesTheDeclarativeStepsResult fixes the order between the two.
//
// The declarative steps are the configuration and a script exists for what they cannot express, so the script should see the
// result of them. The alternative - script first - would make a step silently undo what a script had just decided, which is the
// harder of the two to diagnose because both halves look correct in isolation.
func TestAnX12ScriptSeesTheDeclarativeStepsResult(t *testing.T) {
	sink := &recordingSender{}
	c := x12ScriptChannel(t, `x12:
  transformations:
    - set:
        path: REF02
        value: FROM-STEP
scripts:
  language: lua
  transformer: |
    if msg.get("REF02") == "FROM-STEP" then
      msg.set("REF02", "STEP-THEN-SCRIPT")
    else
      msg.set("REF02", "SCRIPT-RAN-FIRST")
    end`, sink)

	if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() == 0 {
		t.Fatal("nothing was delivered")
	}

	delivered := sink.last()
	if strings.Contains(delivered, "SCRIPT-RAN-FIRST") {
		t.Error("the script ran before the declarative steps, so a step can silently undo what a script decided")
	}
	if !strings.Contains(delivered, "STEP-THEN-SCRIPT") {
		t.Errorf("neither ordering marker is present:\n%s", delivered)
	}
}

// TestAnX12ScriptWritingAnOverlongIsaElementFails is the invariant a tree projection would have lost.
//
// ISA element widths are semantic: the segment is fixed width and a value that does not fit cannot be sent. x12.Set knows that,
// and routing the script through the same accessor means the script inherits it rather than needing its own copy.
func TestAnX12ScriptWritingAnOverlongIsaElementFails(t *testing.T) {
	sink := &recordingSender{}
	c := x12ScriptChannel(t, `scripts:
  language: lua
  transformer: |
    msg.set("ISA06", "THIS VALUE IS FAR TOO LONG FOR A FIXED WIDTH ISA ELEMENT")`, sink)

	if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
		t.Logf("handling returned %v, which is acceptable", err)
	}

	if sink.count() != 0 {
		t.Error("an interchange was delivered after a script wrote a value the format cannot carry; the write should have stopped the message")
	}
}

// TestAnX12ScriptWithAMistypedPathFails attributes the mistake to the script rather than the sender.
func TestAnX12ScriptWithAMistypedPathFails(t *testing.T) {
	sink := &recordingSender{}
	c := x12ScriptChannel(t, `scripts:
  language: lua
  transformer: |
    msg.set("NOT A PATH AT ALL", "x")`, sink)

	if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
		t.Logf("handling returned %v, which is acceptable", err)
	}

	if sink.count() != 0 {
		t.Error("an interchange was delivered after a script used a path that does not compile")
	}
}

// TestAnX12TransformerScriptCanBeWrittenInJavaScript is the end-to-end half of the parity work.
//
// # Why this needs an engine test and not just the parity table
//
// The table in internal/script proves both languages give the same answers through the binding. It cannot prove that a JavaScript
// script reaches the binding at all on a real channel: RunPathScript dispatches on language, and a dispatch that sent JavaScript
// somewhere else - or refused it, as it used to - would leave every table row passing.
//
// That is the same gap this project keeps finding. Correct pieces, and nothing asserting they are joined.
func TestAnX12TransformerScriptCanBeWrittenInJavaScript(t *testing.T) {
	sink := &recordingSender{}
	c := x12ScriptChannel(t, `scripts:
  language: javascript
  transformer: |
    msg.set("REF02", "SCRIPTED");`, sink)

	if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() == 0 {
		t.Fatal("nothing was delivered, so the script cannot be judged")
	}

	delivered := sink.last()
	if strings.Contains(delivered, "CLAIM123") {
		t.Error("the delivered interchange still carries the original REF02, so the JavaScript script wrote to a copy the " +
			"handler discarded")
	}
	if !strings.Contains(delivered, "SCRIPTED") {
		t.Errorf("the delivered interchange does not carry the written value, so a JavaScript script did not reach the path "+
			"binding:\n%s", delivered)
	}
}

// TestAJavaScriptFilterScriptExcludesAnX12Claim covers the other slot.
//
// A transformer failing to run leaves an unchanged message, which the test above catches. A filter failing to run leaves a
// *delivered* message, which is the more expensive direction: the claim the filter existed to exclude goes out.
func TestAJavaScriptFilterScriptExcludesAnX12Claim(t *testing.T) {
	sink := &recordingSender{}
	c := x12ScriptChannel(t, `scripts:
  language: javascript
  filter: |
    return msg.get("REF02") !== "CLAIM123";`, sink)

	if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() != 0 {
		t.Fatalf("a claim the JavaScript filter rejected was delivered: %q", sink.last())
	}
	if got := c.lastOutcome.get(); got != Filtered {
		t.Errorf("outcome was %v, want Filtered", got)
	}
}
