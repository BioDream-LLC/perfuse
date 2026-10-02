package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The trace answers "why did this message come out like that". It is only useful if it answers
// completely: a stage that says "the filter excluded it" without saying what the filter looked at
// has moved the question rather than answered it.

func traceChannel(t *testing.T, yaml string) *config.Channel {
	t.Helper()
	c, err := config.Load(strings.NewReader(yaml), "(test)")
	if err != nil {
		t.Fatalf("loading the fixture: %v", err)
	}
	return c
}

const traceBase = `name: traced
source:
  type: mllp
  listen: 127.0.0.1:17601
destinations:
  - name: out
    type: file
    dir: /tmp/traced
`

const traceMsg = "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|CTRL1|P|2.5\r" +
	"PID|1||0001234^^^MRN||SMITH^JOHN||19700101|1\r"

func TestATraceShowsEveryStage(t *testing.T) {
	tr, err := TraceMessage(context.Background(), traceChannel(t, traceBase), []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"parse", "filter", "transform"}
	got := make([]string, 0, len(tr.Stages))
	for _, s := range tr.Stages {
		got = append(got, s.Stage)
	}
	for _, w := range want {
		if !hasStage(got, w) {
			t.Errorf("the trace has no %q stage: %v", w, got)
		}
	}
	if !tr.Accepted {
		t.Error("a passthrough channel did not accept the message")
	}
}

func TestTheParseStageSaysWhatItRead(t *testing.T) {
	// The first thing anybody checks: did it even read the message as the type expected.
	tr, err := TraceMessage(context.Background(), traceChannel(t, traceBase), []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	stage := stageNamed(t, tr, "parse")
	if !strings.Contains(stage.Detail, "ADT^A01") {
		t.Errorf("the parse stage does not name the message type: %q", stage.Detail)
	}
	if !strings.Contains(stage.Detail, "PID") {
		t.Errorf("the parse stage does not list the segments: %q", stage.Detail)
	}
}

func TestAnUnreadableMessageStopsAtParse(t *testing.T) {
	tr, err := TraceMessage(context.Background(), traceChannel(t, traceBase),
		[]byte("this is not a message"))
	if err != nil {
		t.Fatal(err)
	}

	if len(tr.Stages) != 1 {
		t.Fatalf("expected only a parse stage, got %d", len(tr.Stages))
	}
	if tr.Stages[0].OK {
		t.Error("an unreadable message reported a successful parse")
	}
	if tr.Accepted {
		t.Error("an unreadable message was reported as accepted")
	}
}

func TestAFilterThatExcludesSaysWhatItLookedAt(t *testing.T) {
	// The whole point. "The filter excluded it" moves the question; "the filter read MSH-9.1 and
	// found ADT" answers it.
	cfg := traceChannel(t, strings.Replace(traceBase, "destinations:",
		"filter: \"MSH-9.1 == 'ORU'\"\ndestinations:", 1))

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	stage := stageNamed(t, tr, "filter")
	if stage.OK {
		t.Fatal("the filter accepted a message it should have excluded")
	}
	if len(stage.Reads) == 0 {
		t.Fatal("the filter stage does not say what it looked at, which leaves the question " +
			"unanswered")
	}

	found := false
	for _, r := range stage.Reads {
		if r.Path == "MSH-9.1" {
			found = true
			if r.Value != "ADT" {
				t.Errorf("MSH-9.1 was reported as %q, want ADT", r.Value)
			}
		}
	}
	if !found {
		t.Errorf("MSH-9.1 is not among the values read: %+v", stage.Reads)
	}

	// And it must explain that this is not an error.
	if !strings.Contains(stage.Detail, "acknowledged as accepted") {
		t.Errorf("the detail does not explain that the sender was told the message was fine: %q",
			stage.Detail)
	}
}

func TestAnAbsentFieldIsDistinguishedFromAnEmptyOne(t *testing.T) {
	// Filters turn on the difference and people routinely conflate them, which is exactly the
	// confusion a trace should remove rather than reproduce.
	cfg := traceChannel(t, strings.Replace(traceBase, "destinations:",
		"filter: \"PID-19 exists\"\ndestinations:", 1))

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	stage := stageNamed(t, tr, "filter")
	for _, r := range stage.Reads {
		if r.Path == "PID-19" {
			if r.Present {
				t.Error("PID-19 is absent from the fixture but was reported as present")
			}
			return
		}
	}
	t.Errorf("PID-19 is not among the values read: %+v", stage.Reads)
}

func TestTheTransformStageShowsEachChange(t *testing.T) {
	cfg := traceChannel(t, traceBase+`transformations:
  - description: normalise the sex code
    map:
      path: PID-8
      table:
        "1": M
`)

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	stage := stageNamed(t, tr, "transform")
	if len(stage.Changes) == 0 {
		t.Fatal("no changes were recorded")
	}

	c := stage.Changes[0]
	if c.From != "1" || c.To != "M" {
		t.Errorf("the change reports %q becoming %q, want 1 becoming M", c.From, c.To)
	}
	if !strings.Contains(c.Path, "PID-8") {
		t.Errorf("the change does not name the path: %q", c.Path)
	}
}

func TestStepsThatRanAndChangedNothingSaySo(t *testing.T) {
	// A real and confusing outcome: an empty change list otherwise reads as though the steps did
	// not run at all, and somebody goes looking for a deployment problem.
	cfg := traceChannel(t, traceBase+`transformations:
  - when: "PID-19 exists"
    set:
      path: PID-8
      value: F
`)

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	stage := stageNamed(t, tr, "transform")
	if !stage.OK {
		t.Fatalf("the transform stage failed: %s", stage.Detail)
	}
	if !strings.Contains(stage.Detail, "changed nothing") {
		t.Errorf("the detail does not explain the empty change list: %q", stage.Detail)
	}
}

func TestTheTraceReportsWhichDestinationsWouldReceiveIt(t *testing.T) {
	cfg := traceChannel(t, traceBase+`  - name: results-only
    type: file
    dir: /tmp/traced-oru
    filter: "MSH-9.1 == 'ORU'"
`)

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	if len(tr.Destinations) != 2 {
		t.Fatalf("destinations = %+v", tr.Destinations)
	}

	byName := map[string]TraceDestination{}
	for _, d := range tr.Destinations {
		byName[d.Name] = d
	}

	if !byName["out"].Would {
		t.Error("the unfiltered destination was not reported as receiving the message")
	}
	if byName["results-only"].Would {
		t.Error("a destination whose filter does not match was reported as receiving it")
	}
	if !strings.Contains(byName["results-only"].Why, "excluded") {
		t.Errorf("why = %q", byName["results-only"].Why)
	}
	if len(byName["results-only"].Reads) == 0 {
		t.Error("the destination filter does not say what it looked at")
	}
}

func TestADisabledDestinationSaysItIsDisabled(t *testing.T) {
	// A second, enabled destination is needed: the loader refuses a channel where every
	// destination is disabled, which is a good rule and one my first fixture fell foul of.
	cfg := traceChannel(t, traceBase+`    enabled: false
  - name: live
    type: file
    dir: /tmp/traced-live
`)

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]TraceDestination{}
	for _, d := range tr.Destinations {
		byName[d.Name] = d
	}

	if byName["out"].Would {
		t.Error("a disabled destination was reported as receiving the message")
	}
	if !strings.Contains(byName["out"].Why, "disabled") {
		t.Errorf("why = %q", byName["out"].Why)
	}
	if !byName["live"].Would {
		t.Error("the enabled destination was not reported as receiving the message")
	}
}

func TestAScriptedChannelSaysTheTraceIsIncomplete(t *testing.T) {
	// A script can change anything, so a trace showing only the declarative steps would describe
	// part of the pipeline as though it were all of it - and somebody would conclude a field was
	// untouched when a script had rewritten it.
	cfg := traceChannel(t, traceBase+`scripts:
  transformer: |
    msg['PID']['PID.8']['PID.8.1'] = 'F';
`)

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Caveat == "" {
		t.Fatal("a scripted channel produced a trace with no caveat")
	}
	if !strings.Contains(tr.Caveat, "declarative steps only") {
		t.Errorf("caveat = %q", tr.Caveat)
	}
}

func TestTracingAnX12ChannelIsRefusedNotFaked(t *testing.T) {
	// An X12 channel runs none of this machinery, and a trace that silently described nothing
	// would be worse than one explaining why it cannot help.
	cfg := traceChannel(t, `name: claims
dataType: x12
source:
  type: http
  http:
    listen: 127.0.0.1:17602
    path: /claims
destinations:
  - name: out
    type: file
    dir: /tmp/claims
`)

	_, err := TraceMessage(context.Background(), cfg, []byte("ISA*00*"))
	if err == nil {
		t.Fatal("tracing an X12 channel was allowed")
	}
	if !strings.Contains(err.Error(), "HL7 channels") {
		t.Errorf("error = %v", err)
	}
}

func TestTheTraceKeepsBothVersionsOfTheMessage(t *testing.T) {
	// Side by side is how somebody checks the change was the one they meant.
	cfg := traceChannel(t, traceBase+`transformations:
  - set:
      path: PID-8
      value: F
`)

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Input != traceMsg {
		t.Error("the input was not preserved exactly")
	}
	if tr.Output == "" {
		t.Fatal("no output was produced")
	}
	if tr.Output == tr.Input {
		t.Error("the output is identical to the input despite a step that changes a field")
	}
}

func TestATraceCannotDeliver(t *testing.T) {
	// The property that makes it safe to point at a real production message.
	cfg := traceChannel(t, traceBase)

	ch, err := newShadowChannel(cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.dests) != 0 {
		t.Fatalf("a traced channel has %d senders; it must have none", len(ch.dests))
	}
}

func stageNamed(t *testing.T, tr *Trace, name string) TraceStage {
	t.Helper()
	for _, s := range tr.Stages {
		if s.Stage == name {
			return s
		}
	}
	t.Fatalf("the trace has no %q stage", name)
	return TraceStage{}
}

func hasStage(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
