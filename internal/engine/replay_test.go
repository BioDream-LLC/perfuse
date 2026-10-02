package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/shadow"
)

// Replay is the feature most likely to be trusted with a decision, so its counts have to be
// right and its silence has to mean something. A report saying nothing changed is a licence to
// deploy, which means a bug that under-reports is worse here than almost anywhere else.

func replayChannel(t *testing.T, yaml string) *config.Channel {
	t.Helper()
	c, err := config.Load(strings.NewReader(yaml), "(test)")
	if err != nil {
		t.Fatalf("loading the fixture: %v", err)
	}
	return c
}

// base is a channel that passes everything through untouched.
const replayBase = `name: replay
source:
  type: mllp
  listen: 127.0.0.1:17401
destinations:
  - name: out
    type: file
    dir: /tmp/replay
`

func replayMessages(n int) [][]byte {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		// Sex alternates so a mapping change affects a known fraction, which is what makes
		// the counts checkable rather than merely plausible.
		sex := "1"
		if i%2 == 1 {
			sex = "2"
		}
		msg := "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|CTRL" +
			string(rune('A'+i%26)) + itoaSmall(i) + "|P|2.5\r" +
			"PID|1||00012345||SMITH^JOHN||19700101|" + sex + "\r"
		out = append(out, []byte(msg))
	}
	return out
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestReplayReportsNoChangeWhenNothingChanged(t *testing.T) {
	// The most consequential case. A report of no differences is read as permission to
	// deploy, so it must not be what a broken comparison produces.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase)

	rep, err := Replay(context.Background(), current, candidate, replayMessages(20),
		shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	if rep.Examined != 20 {
		t.Errorf("examined = %d, want 20", rep.Examined)
	}
	if rep.Changed != 0 {
		t.Errorf("changed = %d, want 0: %+v", rep.Changed, rep.Differences)
	}
	if rep.Identical != 20 {
		t.Errorf("identical = %d, want 20", rep.Identical)
	}
}

func TestReplayCountsOnlyTheMessagesAChangeTouches(t *testing.T) {
	// The number that makes the feature worth having: not "this changes something" but
	// "this changes exactly these, out of that many".
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase+`transformations:
  - map:
      path: PID-8
      table:
        "1": M
`)

	rep, err := Replay(context.Background(), current, candidate, replayMessages(20),
		shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	// Ten of the twenty carry sex 1 and are rewritten; the others carry 2, which the table
	// does not mention and so leaves alone.
	if rep.Changed != 10 {
		t.Errorf("changed = %d, want 10: %+v", rep.Changed, rep.Differences)
	}
	if rep.Identical != 10 {
		t.Errorf("identical = %d, want 10", rep.Identical)
	}
	if got := rep.FieldCounts["PID-8"]; got != 10 {
		t.Errorf("PID-8 count = %d, want 10 (counts were %v)", got, rep.FieldCounts)
	}
}

func TestReplayNamesTheFieldAndBothValues(t *testing.T) {
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase+`transformations:
  - set:
      path: PID-8
      value: F
`)

	rep, err := Replay(context.Background(), current, candidate, replayMessages(1),
		shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Differences) != 1 {
		t.Fatalf("differences = %+v", rep.Differences)
	}

	d := rep.Differences[0]
	if d.ControlID == "" {
		t.Error("the difference does not identify the message, so it cannot be found again")
	}
	if d.MessageType != "ADT^A01" {
		t.Errorf("message type = %q", d.MessageType)
	}

	var found bool
	for _, f := range d.Fields {
		if f.Path == "PID-8" {
			found = true
			if f.Live != "1" || f.Candidate != "F" {
				t.Errorf("PID-8: live %q candidate %q", f.Live, f.Candidate)
			}
		}
	}
	if !found {
		t.Errorf("PID-8 is not among the differences: %+v", d.Fields)
	}
}

func TestReplayReportsAChangeInWhatIsAccepted(t *testing.T) {
	// The kind of difference with no field to compare, and the easiest to under-report. A
	// candidate that starts excluding messages is a data-loss change and has to be loud.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase+"filter: \"PID-8 == 'F'\"\n")

	rep, err := Replay(context.Background(), current, candidate, replayMessages(10),
		shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	// None of the fixtures carry F, so the candidate excludes all ten.
	if rep.Changed != 10 {
		t.Errorf("changed = %d, want 10", rep.Changed)
	}
	if len(rep.Differences) == 0 {
		t.Fatal("no differences were reported")
	}
	v := rep.Differences[0].Verdict
	if !strings.Contains(v, "excludes") {
		t.Errorf("verdict = %q, expected it to say the candidate excludes the message", v)
	}
}

func TestReplayReportsWhenOnlyOneVersionFails(t *testing.T) {
	// A candidate whose transformation faults on real traffic is exactly what this exists to
	// catch, and there is no field diff to notice it by.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase+`transformations:
  - map:
      path: PID-8
      table:
        "9": X
      strict: true
`)

	rep, err := Replay(context.Background(), current, candidate, replayMessages(4),
		shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 4 {
		t.Errorf("changed = %d, want 4: %+v", rep.Changed, rep.Differences)
	}
	if len(rep.Differences) == 0 {
		t.Fatal("no differences reported")
	}
	if !strings.Contains(rep.Differences[0].Verdict, "candidate fails") {
		t.Errorf("verdict = %q", rep.Differences[0].Verdict)
	}
}

func TestReplayCountsUnreadableMessagesSeparately(t *testing.T) {
	// A store full of unparseable messages means the sample is not representative. Silently
	// skipping them would make the report look reassuring for the wrong reason.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase)

	messages := append(replayMessages(3), []byte("this is not a message"))

	rep, err := Replay(context.Background(), current, candidate, messages,
		shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unparseable != 1 {
		t.Errorf("unparseable = %d, want 1", rep.Unparseable)
	}
	if rep.Examined != 3 {
		t.Errorf("examined = %d, want 3: an unreadable message is not a difference", rep.Examined)
	}
}

func TestReplayBoundsItsExamples(t *testing.T) {
	// A candidate that rewrites everything would otherwise produce a report too long to
	// read, burying the case somebody needed.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase+`transformations:
  - set:
      path: PID-8
      value: F
`)

	rep, err := Replay(context.Background(), current, candidate, replayMessages(200),
		shadow.DiffOptions{}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Differences) > maxReplayExamples {
		t.Errorf("examples = %d, above the bound of %d", len(rep.Differences), maxReplayExamples)
	}
	if !rep.Truncated {
		t.Error("the report does not say the example list was cut short")
	}
	// The count must still be complete, which is the whole point of separating them.
	if rep.Changed != 200 {
		t.Errorf("changed = %d, want 200: the count must not be bounded with the examples", rep.Changed)
	}
	if got := rep.FieldCounts["PID-8"]; got != 200 {
		t.Errorf("PID-8 count = %d, want 200", got)
	}
}

func TestReplayHonoursIgnoredPaths(t *testing.T) {
	// Without this every message differs on its timestamp and the report is useless.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase+`transformations:
  - set:
      path: PID-8
      value: F
`)

	rep, err := Replay(context.Background(), current, candidate, replayMessages(5),
		shadow.DiffOptions{Ignore: map[string]bool{"PID-8": true}}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 0 {
		t.Errorf("changed = %d, want 0 with PID-8 ignored: %+v", rep.Changed, rep.Differences)
	}
}

func TestReplayCannotDeliverAnything(t *testing.T) {
	// The safety property that makes it acceptable to run a candidate against real
	// production traffic. Both versions are built as shadows, so no sender exists for
	// either - it is a fact about what is in memory, not a promise about a function.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase)

	live, err := newShadowChannel(current, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if len(live.dests) != 0 {
		t.Fatalf("a replay channel has %d senders; it must have none", len(live.dests))
	}

	if _, err := Replay(context.Background(), current, candidate, replayMessages(2),
		shadow.DiffOptions{}, quiet()); err != nil {
		t.Fatal(err)
	}
}

func TestReplayStopsWhenTheCallerGivesUp(t *testing.T) {
	// A replay over fifty thousand messages is long enough that somebody navigates away, and
	// finishing the work after they have gone is waste.
	current := replayChannel(t, replayBase)
	candidate := replayChannel(t, replayBase)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rep, err := Replay(ctx, current, candidate, replayMessages(1000), shadow.DiffOptions{}, quiet())
	if err == nil {
		t.Fatal("a cancelled replay ran to completion")
	}
	if rep == nil {
		t.Fatal("no partial report was returned")
	}
	if rep.Examined == 1000 {
		t.Error("every message was examined despite cancellation")
	}
}
