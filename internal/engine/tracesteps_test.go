package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

const stepTraceMsg = "MSH|^~\\&|SENDER|HOSP|RECV|CLINIC|20260828120000||ADT^A01|MSG1|P|2.5\r" +
	"PID|1||12345^^^HOSP^MR||SMITH^JOHN||19800101|M\r" +
	"PV1|1|I|WARD^01^01\r"

func stepTrace(t *testing.T, yaml string) []TraceStep {
	t.Helper()

	cfg, err := config.Load(strings.NewReader(yaml), "steps.yaml")
	if err != nil {
		t.Fatalf("loading the channel: %v", err)
	}

	tr, err := TraceMessage(context.Background(), cfg, []byte(stepTraceMsg))
	if err != nil {
		t.Fatalf("TraceMessage: %v", err)
	}

	for _, st := range tr.Stages {
		if st.Stage == "transform" {
			return st.Steps
		}
	}
	t.Fatal("the trace has no transform stage")
	return nil
}

func stepNamed(t *testing.T, steps []TraceStep, label string) TraceStep {
	t.Helper()
	for _, s := range steps {
		if s.Label == label {
			return s
		}
	}
	var have []string
	for _, s := range steps {
		have = append(have, s.Label)
	}
	t.Fatalf("no step labelled %q; the trace has %v", label, have)
	return TraceStep{}
}

// The distinction the per-step view exists for.
//
// A step whose condition was false and a step that addressed a field the message does not carry look identical from
// outside: the message comes out unchanged either way. One is the step working exactly as written. The other is almost
// always the bug, and it is the one that costs an afternoon because nothing anywhere reports it.
func TestATraceDistinguishesAFalseConditionFromAnAbsentField(t *testing.T) {
	steps := stepTrace(t, `
name: steps
source:
  type: mllp
  listen: "127.0.0.1:0"
transformations:
  - description: Rewrite the facility
    set:
      path: MSH-4
      value: PERFUSE
  # Reads a field this message does not carry. Deliberately a copy rather than a set: set creates whatever it
  # addresses, so it always succeeds and could never demonstrate this.
  - description: Carry across the custom identifier
    copy:
      from: ZZZ-1
      to: PID-4
  - description: Only for outpatients
    set:
      path: PV1-19
      value: OUTPATIENT
    when: PV1-2 == "O"
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(steps))
	}

	if got := stepNamed(t, steps, "Rewrite the facility"); got.Outcome != "changed" {
		t.Errorf("a step that changed a present field reported %q, want changed (detail: %s)", got.Outcome, got.Detail)
	}

	absent := stepNamed(t, steps, "Carry across the custom identifier")
	if absent.Outcome != "no-effect" {
		t.Errorf("a step reading an absent field reported %q, want no-effect (detail: %s)", absent.Outcome, absent.Detail)
	}
	if absent.Path == "" {
		t.Error("the step did not name the path it addressed, which is what somebody compares against a real message")
	}

	skipped := stepNamed(t, steps, "Only for outpatients")
	if skipped.Outcome != "skipped" {
		t.Errorf("a step whose condition was false reported %q, want skipped (detail: %s)", skipped.Outcome, skipped.Detail)
	}
	if !strings.Contains(skipped.Detail, "condition") {
		t.Errorf("the detail does not blame the condition: %q", skipped.Detail)
	}

	if absent.Outcome == skipped.Outcome {
		t.Error("an absent field and a false condition produced the same outcome, which is the confusion this exists to end")
	}
}

// Each step keeps the message as it stood after it ran.
//
// Storing only deltas would mean the message at any point in the middle has to be reconstructed by replaying, and the
// thing somebody wants to look at is the message immediately before the step that broke it.
func TestEachStepCarriesTheMessageAsItStoodThere(t *testing.T) {
	steps := stepTrace(t, `
name: steps
source:
  type: mllp
  listen: "127.0.0.1:0"
transformations:
  - description: First
    set:
      path: MSH-4
      value: ONEONE
  - description: Second
    set:
      path: MSH-6
      value: TWOTWO
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	first := stepNamed(t, steps, "First")
	second := stepNamed(t, steps, "Second")

	if !strings.Contains(first.Message, "ONEONE") {
		t.Error("the first step's snapshot does not contain its own change")
	}
	// The ordering is the whole point: it is what lets somebody say which step a value came from.
	if strings.Contains(first.Message, "TWOTWO") {
		t.Error("the first step's snapshot already holds the second step's change, so the snapshots are not per step")
	}
	if !strings.Contains(second.Message, "ONEONE") || !strings.Contains(second.Message, "TWOTWO") {
		t.Error("the second step's snapshot is missing one of the two changes")
	}
}

// A change has to be described in words. Two blobs of pipe-delimited text side by side is not an explanation.
func TestAStepDescribesItsChangeInWords(t *testing.T) {
	steps := stepTrace(t, `
name: steps
source:
  type: mllp
  listen: "127.0.0.1:0"
transformations:
  - description: Rewrite the facility
    set:
      path: MSH-4
      value: PERFUSE
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	e := stepNamed(t, steps, "Rewrite the facility")
	for _, want := range []string{"HOSP", "PERFUSE", "MSH-4"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("the detail omits %q, so the reader has to go back to the message: %q", want, e.Detail)
		}
	}
}

// Steps are numbered from one and in order, so an entry can be cited in a conversation.
func TestStepsAreNumberedInOrderFromOne(t *testing.T) {
	steps := stepTrace(t, `
name: steps
source:
  type: mllp
  listen: "127.0.0.1:0"
transformations:
  - description: One
    set:
      path: MSH-4
      value: a
  - description: Two
    set:
      path: MSH-6
      value: b
  - description: Three
    set:
      path: MSH-10
      value: c
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(steps))
	}
	for i, s := range steps {
		if s.Number != i+1 {
			t.Errorf("step at position %d is numbered %d", i, s.Number)
		}
	}
}

// An unnamed step still has to be readable.
//
// Falling back to "step 3" would make a trace of unnamed steps no better than counting them by hand, and unnamed steps
// are the common case.
func TestAnUnnamedStepIsStillDescribed(t *testing.T) {
	steps := stepTrace(t, `
name: steps
source:
  type: mllp
  listen: "127.0.0.1:0"
transformations:
  - set:
      path: MSH-4
      value: PERFUSE
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(steps))
	}
	if steps[0].Label == "" || steps[0].Label == "step 1" {
		t.Errorf("an unnamed step is labelled %q, which tells the reader nothing", steps[0].Label)
	}
	// It should describe what it does, so MSH-4 ought to appear somewhere in the label.
	if !strings.Contains(steps[0].Label, "MSH-4") {
		t.Errorf("the label does not say what the step addresses: %q", steps[0].Label)
	}
}

// A channel with no transformations produces no steps rather than an empty-looking stage.
func TestAChannelWithNoTransformationsHasNoSteps(t *testing.T) {
	steps := stepTrace(t, `
name: steps
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	if len(steps) != 0 {
		t.Errorf("got %d steps for a channel with no transformations", len(steps))
	}
}

// The per-step view must agree with the stage-level view, or the trace disagrees with itself.
//
// Two independent code paths reporting on the same pipeline is how a report comes to contradict itself, and somebody
// then has to work out which half to believe.
func TestThePerStepViewAgreesWithTheStageTotal(t *testing.T) {
	cfg, err := config.Load(strings.NewReader(`
name: steps
source:
  type: mllp
  listen: "127.0.0.1:0"
transformations:
  - description: One
    set:
      path: MSH-4
      value: a
  - description: Two
    copy:
      from: ZZZ-1
      to: PID-4
  - description: Three
    set:
      path: MSH-6
      value: c
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "steps.yaml")
	if err != nil {
		t.Fatal(err)
	}

	tr, err := TraceMessage(context.Background(), cfg, []byte(stepTraceMsg))
	if err != nil {
		t.Fatal(err)
	}

	var stage TraceStage
	for _, st := range tr.Stages {
		if st.Stage == "transform" {
			stage = st
		}
	}

	var changed int
	for _, s := range stage.Steps {
		if s.Outcome == "changed" {
			changed++
		}
	}
	if changed != len(stage.Changes) {
		t.Errorf("the per-step view reports %d changed steps but the stage reports %d changes; "+
			"the trace disagrees with itself", changed, len(stage.Changes))
	}
}
