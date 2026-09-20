package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The v3 interaction fixture is shared with hl7v3channel_test.go as v3PDQ. Reused rather than copied, because a second copy
// would drift and this one is already known to parse - the first version of this test used a CDA ClinicalDocument, which the v3
// path correctly refuses because it is a document rather than an interaction.

// patientPersonPath walks to the demographics inside a PDQ response.
//
// Written out once here because the nesting is the awkward part of v3 and repeating it in three tests invites a typo that reads
// as a script bug.
const patientPersonPath = `msg.child("controlActProcess").child("subject").child("registrationEvent").child("subject1").child("patient").child("patientPerson")`

// TestALuaTransformerChangesTheDeliveredDocument is the end-to-end proof that Lua writes survive to a destination.
//
// # Why this is asserted on delivered bytes
//
// The unit tests prove a Lua script can mutate an xtree.Node. They cannot prove the engine hands the script the same node it
// later re-serialises, and that is exactly the kind of gap this session has found five times: the pieces work and nothing
// connects them.
//
// So this drives a real v3 channel and reads what reached the destination. If the script mutated a copy, the delivered document
// is the original and the test says so.
func TestALuaTransformerChangesTheDeliveredDocument(t *testing.T) {
	sink := &recordingSender{}
	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(`
name: lua-v3
dataType: hl7v3
hl7v3:
  acknowledge: false
scripts:
  language: lua
  transformer: |
    local person = `+patientPersonPath+`
    person.child("name").child("family").setText("REDACTED")
    person.child("birthTime").setAttr("value", "19000101")
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: http
    http:
      url: http://127.0.0.1:1/in
`), "luav3.yaml")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	c := startChannelFor(t, cfg, sink)

	if _, err := c.handle(context.Background(), []byte(v3PDQ)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() == 0 {
		t.Fatal("nothing was delivered, so the transformer cannot be judged")
	}

	delivered := sink.last()

	if strings.Contains(delivered, "Okonkwo-Hale") {
		t.Error("the delivered document still contains the original family name, so the Lua transformer mutated a copy rather than the document the engine re-serialises")
	}
	if !strings.Contains(delivered, "REDACTED") {
		t.Errorf("the delivered document does not contain the replacement:\n%s", delivered)
	}
	if !strings.Contains(delivered, "19000101") {
		t.Error("the attribute write did not reach the delivered document")
	}
	// The rest of the document has to survive. A transformer that replaced the whole thing would satisfy the assertions above.
	if !strings.Contains(delivered, "Rosalind") {
		t.Error("the given name is missing, so more was changed than the script asked for")
	}
}

// TestALuaFilterOnAV3ChannelDecidesDelivery covers the read side through the engine.
func TestALuaFilterOnAV3ChannelDecidesDelivery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		filter  string
		deliver bool
	}{
		{
			name:    "matches",
			filter:  patientPersonPath + `.child("administrativeGenderCode").attr("code") == "F"`,
			deliver: true,
		},
		{
			name:    "does not match",
			filter:  patientPersonPath + `.child("administrativeGenderCode").attr("code") == "M"`,
			deliver: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSender{}
			sink.name = "out"

			cfg, err := config.Load(strings.NewReader(`
name: lua-v3-filter
dataType: hl7v3
hl7v3:
  acknowledge: false
scripts:
  language: lua
  filter: |
    return `+tc.filter+`
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: http
    http:
      url: http://127.0.0.1:1/in
`), "luafilter.yaml")
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			c := startChannelFor(t, cfg, sink)

			if _, err := c.handle(context.Background(), []byte(v3PDQ)); err != nil {
				t.Fatalf("handling: %v", err)
			}

			got := sink.count() > 0
			if got != tc.deliver {
				t.Errorf("delivered=%v, want %v", got, tc.deliver)
			}
		})
	}
}
