package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestATraceReportsStepsThatChangedNothing is the part of a trace people need most and no output had.
//
// A step addressing a field the sender does not populate is not an error. Nothing fails, no log line appears, and the message
// goes on unchanged - for years. The only prior evidence was that the count of changes was lower than somebody expected, which
// requires an expectation precise enough to notice.
func TestATraceReportsStepsThatChangedNothing(t *testing.T) {
	cfg := traceChannel(t, traceBase+`transformations:
  - description: this one works
    set:
      path: PID-8
      value: M
  - description: this addresses a field the sender never sends
    trim:
      path: PID-19
  - description: this one is switched off by its condition
    when: 'MSH-9.2 == "A03"'
    set:
      path: PID-30
      value: "Y"
`)

	tr, err := TraceMessage(context.Background(), cfg, []byte(traceMsg))
	if err != nil {
		t.Fatal(err)
	}

	var stage *TraceStage
	for i := range tr.Stages {
		if tr.Stages[i].Stage == "transform" {
			stage = &tr.Stages[i]
		}
	}
	if stage == nil {
		t.Fatal("there is no transform stage in the trace")
	}

	if len(stage.Changes) != 1 {
		t.Errorf("the trace lists %d changes, want 1", len(stage.Changes))
	}

	if len(stage.Skipped) != 2 {
		t.Fatalf("the trace lists %d skipped steps, want 2 - a step that ran and did nothing is invisible, "+
			"which is the commonest fault in an interface that appears to work", len(stage.Skipped))
	}

	joined := ""
	for _, sk := range stage.Skipped {
		joined += sk.Step + "|" + sk.Path + "|" + sk.Why + "\n"
	}

	// The absent-field case names the path, because that is what somebody compares against a real message.
	if !strings.Contains(joined, "PID-19") {
		t.Errorf("the skipped step does not name the path it addressed:\n%s", joined)
	}
	if !strings.Contains(joined, "does not carry") {
		t.Errorf("the reason does not say the field was absent:\n%s", joined)
	}

	// The false-condition case is distinguished from it, because the fixes are different: one is a wrong path and
	// the other is a condition that was written deliberately.
	if !strings.Contains(joined, "condition was false") {
		t.Errorf("a step skipped by its condition is not distinguished from one that found nothing:\n%s", joined)
	}
}

// TestATraceSaysWhenTheChannelHasChangedSinceTheMessageArrived covers a gap in what the existing trace claimed.
//
// A trace runs the channel as configured now. For a message from five minutes ago that is the same thing; for one from last
// week, on a channel edited since, it is not - and nothing said so.
//
// The failure is specific and expensive. Somebody investigating why a message was delivered, on a channel whose filter was
// tightened yesterday, is shown a trace saying it would be filtered, and reasonably concludes the delivery record is wrong.
func TestATraceSaysWhenTheChannelHasChangedSinceTheMessageArrived(t *testing.T) {
	cfg := traceChannel(t, traceBase)

	arrived := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

	t.Run("changed after the message arrived", func(t *testing.T) {
		changed := arrived.Add(48 * time.Hour)

		tr, err := TraceStoredMessage(context.Background(), cfg, []byte(traceMsg), arrived, changed)
		if err != nil {
			t.Fatal(err)
		}

		if !tr.Stale {
			t.Error("the trace is not marked stale even though the channel changed after the message arrived")
		}
		if !strings.Contains(tr.Caveat, "not a record of what it") {
			t.Errorf("the caveat does not say the trace is not a record of what happened:\n%s", tr.Caveat)
		}
		// Both dates, so somebody can see the gap rather than take the claim on trust.
		if !strings.Contains(tr.Caveat, "17 August") || !strings.Contains(tr.Caveat, "15 August") {
			t.Errorf("the caveat does not give both dates:\n%s", tr.Caveat)
		}
	})

	t.Run("unchanged since the message arrived", func(t *testing.T) {
		changed := arrived.Add(-48 * time.Hour)

		tr, err := TraceStoredMessage(context.Background(), cfg, []byte(traceMsg), arrived, changed)
		if err != nil {
			t.Fatal(err)
		}

		if tr.Stale {
			t.Error("a trace of an unchanged channel is marked stale, which would train somebody to ignore " +
				"the warning")
		}
		if tr.Caveat != "" {
			t.Errorf("an unnecessary caveat was added:\n%s", tr.Caveat)
		}
	})

	t.Run("an unknown change time adds no warning", func(t *testing.T) {
		// Guessing "changed" would put a caveat on every trace, which is the same as having none.
		tr, err := TraceStoredMessage(context.Background(), cfg, []byte(traceMsg), arrived, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if tr.Stale || tr.Caveat != "" {
			t.Errorf("an unknown change time produced a warning:\n%s", tr.Caveat)
		}
	})

	t.Run("the script caveat is kept as well", func(t *testing.T) {
		// Two caveats can apply at once, and losing either would be worse than showing both.
		scripted := traceChannel(t, traceBase+`scripts:
  filter: |
    return true
`)

		tr, err := TraceStoredMessage(context.Background(), scripted, []byte(traceMsg),
			arrived, arrived.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(tr.Caveat, "JavaScript") {
			t.Errorf("the script caveat was lost when the staleness one was added:\n%s", tr.Caveat)
		}
		if !strings.Contains(tr.Caveat, "was changed on") {
			t.Errorf("the staleness caveat is missing:\n%s", tr.Caveat)
		}
		// Staleness first: it changes how the whole trace should be read, and a reader who forms a conclusion
		// and finds the note afterwards has already formed it.
		if strings.Index(tr.Caveat, "was changed on") > strings.Index(tr.Caveat, "JavaScript") {
			t.Error("the staleness caveat comes after the script one, so it is read too late")
		}
	})
}
