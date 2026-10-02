package alerts

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeRhythm is a rhythm with a fixed answer, so the rule can be tested without a database.
type fakeRhythm struct {
	lost float64
	ok   bool
	says string
}

func (f fakeRhythm) Shortfall(time.Time, int64) (float64, bool) { return f.lost, f.ok }
func (f fakeRhythm) Describe(time.Time) string {
	if f.says == "" {
		return "labs normally carries about 200 messages at Tuesday 14:00"
	}
	return f.says
}

func rhythmReading(received float64, r RhythmSource) Reading {
	read := Reading{
		At:       time.Date(2026, 8, 25, 14, 30, 0, 0, time.UTC), // a Tuesday afternoon
		Channels: map[string]ChannelReading{"labs": {Received: received}},
	}
	if r != nil {
		read.Rhythms = map[string]RhythmSource{"labs": r}
	}
	return read
}

func TestAFeedFarBelowItsNormalFires(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{Kind: KindBelowRhythm, Threshold: 0.8}

	got := e.evaluateRule(rule, rhythmReading(10, fakeRhythm{lost: 0.95, ok: true}))
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
	// The summary has to say how much is missing, since that is what decides whether somebody acts tonight.
	if !strings.Contains(got[0].Summary, "95%") {
		t.Errorf("summary = %q, which does not say how much traffic is missing", got[0].Summary)
	}
	// And the detail has to explain that nothing errored, or the first thing somebody does is search for errors.
	if !strings.Contains(got[0].Detail, "no error") {
		t.Errorf("detail does not explain that a stopped feed raises no error: %q", got[0].Detail)
	}
	// The learned normal belongs in the alert. An alert that says traffic is low without saying low against what
	// cannot be judged at 2am.
	if !strings.Contains(got[0].Detail, "normally carries") {
		t.Errorf("detail does not state what normal is: %q", got[0].Detail)
	}
}

// The property that makes this usable in a clinic at all.
func TestAnHourWhereSilenceIsNormalDoesNotFire(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{Kind: KindBelowRhythm, Threshold: 0.8}

	// ok=false is how the rhythm says it cannot honestly judge this hour.
	got := e.evaluateRule(rule, rhythmReading(0, fakeRhythm{lost: 1, ok: false}))
	if len(got) != 0 {
		t.Fatalf("an hour with no usable normal fired anyway: %q", got[0].Summary)
	}
}

// A channel with no history must not alert for having no past.
func TestAChannelWithNoRhythmDoesNotFire(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{Kind: KindBelowRhythm, Threshold: 0.8}

	if got := e.evaluateRule(rule, rhythmReading(0, nil)); len(got) != 0 {
		t.Errorf("a channel with no learned rhythm fired: %q", got[0].Summary)
	}
}

func TestAModestDipDoesNotFire(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{Kind: KindBelowRhythm, Threshold: 0.8}

	// A third down is a quiet afternoon, not a stopped feed.
	if got := e.evaluateRule(rule, rhythmReading(140, fakeRhythm{lost: 0.3, ok: true})); len(got) != 0 {
		t.Errorf("a feed down a third fired, which is how an alert gets switched off: %q", got[0].Summary)
	}
}

// The threshold is a fraction, so it must default to something usable rather than to zero.
//
// Zero would mean any shortfall at all fires, so a feed one message below its median would page somebody. A rule kind
// whose natural default is not zero has to say so, because the zero value of a struct field is what an unset rule gets.
func TestTheDefaultThresholdIsNotZero(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{Kind: KindBelowRhythm} // no threshold set

	if got := e.evaluateRule(rule, rhythmReading(199, fakeRhythm{lost: 0.005, ok: true})); len(got) != 0 {
		t.Errorf("a rule with no threshold fired on a half-percent shortfall: %q", got[0].Summary)
	}
	if got := e.evaluateRule(rule, rhythmReading(5, fakeRhythm{lost: 0.97, ok: true})); len(got) != 1 {
		t.Errorf("a rule with no threshold did not fire on a 97%% shortfall; got %d alerts", len(got))
	}
}

// Two channels with different rhythms are judged separately, or one busy feed masks another that has stopped.
func TestChannelsAreJudgedAgainstTheirOwnRhythm(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{Kind: KindBelowRhythm, Threshold: 0.8}

	read := Reading{
		At: time.Date(2026, 8, 25, 14, 30, 0, 0, time.UTC),
		Channels: map[string]ChannelReading{
			"labs": {Received: 0},
			"adt":  {Received: 5000},
		},
		Rhythms: map[string]RhythmSource{
			"labs": fakeRhythm{lost: 1, ok: true, says: "labs normally carries about 200 messages at Tuesday 14:00"},
			"adt":  fakeRhythm{lost: 0, ok: true},
		},
	}

	got := e.evaluateRule(rule, read)
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1 (only labs has stopped)", len(got))
	}
	if got[0].Channel != "labs" {
		t.Errorf("alerted on %q, want labs", got[0].Channel)
	}
}

// A rule scoped to one channel must not judge another.
func TestARuleScopedToOneChannelIgnoresOthers(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{Kind: KindBelowRhythm, Threshold: 0.8, Channel: "adt"}

	if got := e.evaluateRule(rule, rhythmReading(0, fakeRhythm{lost: 1, ok: true})); len(got) != 0 {
		t.Errorf("a rule scoped to adt fired on labs: %q", got[0].Summary)
	}
}

// Every declared kind must have an implementation.
//
// The constants are read out of the source rather than from a list in this file, because a list here is a second place
// to forget. My first attempt at this test iterated the dispatch map's own keys and asserted they were in the dispatch
// map, which is true by construction - it would have passed with a kind declared and never wired, which is the exact
// defect it exists to catch.
func TestEveryDeclaredKindHasAnImplementation(t *testing.T) {
	src, err := os.ReadFile("alerts.go")
	if err != nil {
		t.Fatal(err)
	}

	// Matches lines like:  KindQueueDepth  Kind = "queue-depth"
	decl := regexp.MustCompile(`(?m)^\s*(Kind\w+)\s+Kind\s*=\s*"([^"]+)"`)
	found := decl.FindAllStringSubmatch(string(src), -1)
	if len(found) < 10 {
		t.Fatalf("only found %d kind declarations, so this test is not reading the source properly", len(found))
	}

	for _, m := range found {
		constName, value := m[1], Kind(m[2])
		if !Handles(value) {
			t.Errorf("%s (%q) is declared but has no entry in ruleFuncs, so a rule using it would load, "+
				"appear in the interface, and silently never fire", constName, value)
		}
	}

	// And nothing in the map that is not declared, which would be a kind nobody can configure.
	for _, k := range AllKinds() {
		if !regexp.MustCompile(`"` + regexp.QuoteMeta(string(k)) + `"`).Match(src) {
			t.Errorf("kind %q is wired up but not declared as a constant", k)
		}
	}
}

// Evaluating any kind against an empty reading must not panic.
//
// Alerts run on a timer against whatever the engine last reported, and a nil map early in startup is normal. A panic
// here takes down alerting altogether, which is the one component whose failure is silent by definition.
func TestNoKindPanicsOnAnEmptyReading(t *testing.T) {
	e := &Evaluator{}
	for _, k := range AllKinds() {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("kind %q panicked on an empty reading: %v", k, p)
				}
			}()
			_ = e.evaluateRule(Rule{Kind: k}, Reading{At: time.Now()})
		}()
	}
}
