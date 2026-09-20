package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// TestADelimitedScriptChangesTheDeliveredRow proves the generic path stage reaches a third format.
//
// The point of the stage being generic over steps.Accessor is that a format with an accessor gets scripting without a second
// implementation. That is only true if each handler actually calls it, which is what this asserts - on delivered bytes, because
// the whole shape this session kept finding is correct pieces with nothing joining them.
func TestADelimitedScriptChangesTheDeliveredRow(t *testing.T) {
	sink := &recordingSender{}
	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(`
name: delimited-scripted
dataType: delimited
delimited:
  delimiter: ","
  has_header: true
  split: true
scripts:
  language: lua
  transformer: |
    if msg.get("Ward") == "ICU" then
      msg.set("Surname", "REDACTED")
    end
source:
  type: file
  file:
    root: /tmp
    dir: in
    raw: true
destinations:
  - name: out
    type: file
    dir: /tmp/delimscript
`), "delimscript.yaml")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	c := startChannelFor(t, cfg, sink)

	if _, err := c.handle(context.Background(),
		[]byte("PatientID,Surname,Ward\nP001,Okonkwo,ICU\nP002,Nakamura,HDU\n")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	all := sink.all()
	if len(all) != 2 {
		t.Fatalf("got %d delivered row(s), want 2 - split is on so each row is its own message", len(all))
	}

	joined := strings.Join([]string{string(all[0]), string(all[1])}, "\n")

	if strings.Contains(joined, "Okonkwo") {
		t.Error("the ICU row still carries the original surname, so the script wrote to a copy")
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("the replacement is absent:\n%s", joined)
	}
	// The conditional has to have skipped the HDU row. If the script ran unconditionally both would be redacted.
	if !strings.Contains(joined, "Nakamura") {
		t.Error("the HDU row was also redacted, so the condition did not see that row's own Ward value")
	}
}

// TestADelimitedFilterScriptDropsRowsIndividually is the property split delivers.
//
// A file of five thousand rows should be able to lose one without losing the rest. That only works if the script sees one row per
// message, which is the same reason the declarative steps run per row.
func TestADelimitedFilterScriptDropsRowsIndividually(t *testing.T) {
	sink := &recordingSender{}
	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(`
name: delimited-filtered
dataType: delimited
delimited:
  delimiter: ","
  has_header: true
  split: true
scripts:
  language: lua
  filter: |
    return msg.get("Ward") == "ICU"
source:
  type: file
  file:
    root: /tmp
    dir: in
    raw: true
destinations:
  - name: out
    type: file
    dir: /tmp/delimfilter
`), "delimfilter.yaml")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	c := startChannelFor(t, cfg, sink)

	if _, err := c.handle(context.Background(),
		[]byte("PatientID,Surname,Ward\nP001,Okonkwo,ICU\nP002,Nakamura,HDU\nP003,Adeyemi,ICU\n")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	all := sink.all()
	if len(all) != 2 {
		t.Fatalf("got %d delivered row(s), want 2 - the HDU row should be excluded and the two ICU rows kept", len(all))
	}

	joined := strings.Join([]string{string(all[0]), string(all[1])}, "\n")
	if strings.Contains(joined, "Nakamura") {
		t.Error("the HDU row was delivered")
	}
	for _, want := range []string{"Okonkwo", "Adeyemi"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s was excluded, so the filter dropped more than the one row that did not match", want)
		}
	}
}

// TestAnNcpdpScriptChangesTheDeliveredClaim covers the pharmacy format.
//
// Fields are addressed by the standard's own two-character identifiers, which is the vocabulary a pharmacy integrator already
// uses - C2 is the cardholder identifier, not "the second field of the fourth segment".
func TestAnNcpdpScriptChangesTheDeliveredClaim(t *testing.T) {
	sink := &recordingSender{}
	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(`
name: ncpdp-scripted
dataType: ncpdp
scripts:
  language: lua
  transformer: |
    msg.set("01-CB", "REDACTED")
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
`), "ncpdpscript.yaml")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	c := startChannelFor(t, cfg, sink)

	if _, err := c.handle(context.Background(), pharmacyClaim()); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if sink.count() == 0 {
		t.Fatal("nothing was delivered")
	}

	delivered := sink.last()
	if strings.Contains(delivered, "SMITH") {
		t.Error("the delivered transmission still carries the original surname, so the script wrote to a copy")
	}
	if !strings.Contains(delivered, "REDACTED") {
		t.Errorf("the replacement is absent from the delivered transmission")
	}
}

// TestAnNcpdpFilterScriptDecidesDelivery covers the read side on the pharmacy format.
func TestAnNcpdpFilterScriptDecidesDelivery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		deliver bool
	}{
		{"keeps a matching BIN", `return msg.get("A1") == "610097"`, true},
		{"excludes another BIN", `return msg.get("A1") == "999999"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSender{}
			sink.name = "out"

			cfg, err := config.Load(strings.NewReader(`
name: ncpdp-filtered
dataType: ncpdp
scripts:
  language: lua
  filter: |
    `+tc.body+`
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
`), "ncpdpfilter.yaml")
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			c := startChannelFor(t, cfg, sink)

			if _, err := c.handle(context.Background(), pharmacyClaim()); err != nil {
				t.Fatalf("handling: %v", err)
			}

			if got := sink.count() > 0; got != tc.deliver {
				t.Errorf("delivered=%v, want %v", got, tc.deliver)
			}
		})
	}
}
