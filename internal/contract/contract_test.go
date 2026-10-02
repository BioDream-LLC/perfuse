package contract

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/profile"
)

// The whole risk with this feature is that it produces alerts nobody trusts. So most of these tests are about
// what it deliberately does not fire on, and about whether a violation says enough to act on.

func report(messages int, segs ...profile.Segment) *profile.Report {
	return &profile.Report{Messages: messages, Segments: segs}
}

func seg(id string, rate float64, maxPer int, fields ...profile.Field) profile.Segment {
	return profile.Segment{ID: id, Rate: rate, MaxPerMessage: maxPer, Standard: true, Fields: fields}
}

func field(path string, fill float64, repeats int, codes ...profile.CodeCount) profile.Field {
	distinct := len(codes)
	return profile.Field{Path: path, FillRate: fill, MaxRepeats: repeats, Codes: codes, Distinct: distinct}
}

func code(value string, count int) profile.CodeCount {
	return profile.CodeCount{Code: value, Count: count, Known: true}
}

func TestAFieldThatStoppedBeingSentIsAViolation(t *testing.T) {
	// The incident this feature exists for. The messages are still valid HL7, so nothing else notices.
	c := &Contract{
		MinMessages:  100,
		Expectations: []Expectation{{Path: "PID-3", Rule: Populated, MinRate: 0.99}},
	}

	result := Check(c, report(500, seg("PID", 1, 1, field("PID-3", 0.942, 1))))

	if result.Holds() {
		t.Fatal("a field populated in 94% of messages satisfied a 99% expectation")
	}
	if len(result.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(result.Violations))
	}

	// The numbers are the message: how bad, how confident, and whether it is worth waking somebody.
	says := result.Violations[0].Says
	for _, want := range []string{"94.2%", "500", "99.0%"} {
		if !strings.Contains(says, want) {
			t.Errorf("the violation does not state %s: %q", want, says)
		}
	}
}

func TestOneOddMessageDoesNotFire(t *testing.T) {
	// Real feeds contain test messages, manual entries and patients with no recorded sex. Alerting on each one
	// trains people to ignore the alerts, at which point the feature is worse than nothing.
	c := &Contract{
		MinMessages:  100,
		Expectations: []Expectation{{Path: "PID-8", Rule: Populated}},
	}

	// 9,999 of 10,000 populated.
	result := Check(c, report(10000, seg("PID", 1, 1, field("PID-8", 0.9999, 1))))

	if !result.Holds() {
		t.Errorf("one odd message in ten thousand fired the contract: %s", result.Summary())
	}
}

func TestTooFewMessagesIsNotJudgedRatherThanPassing(t *testing.T) {
	// Saying it holds would turn "we have no evidence" into "everything is fine", which is the failure mode of
	// every monitoring system that reports green when its input has stopped.
	c := &Contract{
		MinMessages:  100,
		Expectations: []Expectation{{Path: "PID-3", Rule: Populated}},
	}

	result := Check(c, report(4, seg("PID", 1, 1, field("PID-3", 0, 1))))

	if result.Judged {
		t.Error("four messages were judged against a 99% expectation")
	}
	if result.Holds() {
		t.Error("an unjudged result reported as holding, which reads as everything being fine")
	}
	if !strings.Contains(result.Note, "noise") {
		t.Errorf("the note does not explain why: %q", result.Note)
	}
}

func TestANewCodeIsAViolationAndIsNamed(t *testing.T) {
	// This is what breaks a strict downstream mapping, and the rejection at the far end rarely says which value
	// caused it.
	c := &Contract{
		MinMessages: 100,
		Expectations: []Expectation{{
			Path: "PID-8", Rule: OneOf, Values: []string{"M", "F", "U"}, MinRate: 0.99,
		}},
	}

	result := Check(c, report(1000, seg("PID", 1, 1,
		field("PID-8", 1, 1, code("M", 480), code("F", 490), code("X", 30)))))

	if result.Holds() {
		t.Fatal("an unexpected code satisfied the contract")
	}
	v := result.Violations[0]
	if len(v.Unexpected) != 1 || v.Unexpected[0] != "X" {
		t.Errorf("unexpected = %v, want [X]", v.Unexpected)
	}
	if !strings.Contains(v.Says, `"X"`) {
		t.Errorf("the violation does not name the offending code: %q", v.Says)
	}
	if !strings.Contains(v.Says, "reject") {
		t.Errorf("the violation does not say what will happen: %q", v.Says)
	}
}

func TestARareNewCodeWithinToleranceDoesNotFire(t *testing.T) {
	// A single message with an odd code is not an incident, and a contract that says otherwise gets deleted.
	c := &Contract{
		MinMessages: 100,
		Expectations: []Expectation{{
			Path: "PID-8", Rule: OneOf, Values: []string{"M", "F"}, MinRate: 0.99,
		}},
	}

	result := Check(c, report(1000, seg("PID", 1, 1,
		field("PID-8", 1, 1, code("M", 500), code("F", 498), code("X", 2)))))

	if !result.Holds() {
		t.Errorf("two odd codes in a thousand fired the contract: %s", result.Summary())
	}
}

func TestAOneOfExpectationOnAnAbsentFieldDoesNotDoubleUp(t *testing.T) {
	// A field that is not present cannot have a wrong value, and reporting it here would double up with the
	// Populated expectation that almost certainly sits beside it. Two findings for one cause is what makes a
	// report unreadable.
	c := &Contract{
		MinMessages: 100,
		Expectations: []Expectation{
			{Path: "PID-8", Rule: Populated, MinRate: 0.99},
			{Path: "PID-8", Rule: OneOf, Values: []string{"M", "F"}, MinRate: 0.99},
		},
	}

	result := Check(c, report(500, seg("PID", 1, 1)))

	if len(result.Violations) != 1 {
		t.Fatalf("violations = %d, want 1 - the missing field should be reported once:\n%+v",
			len(result.Violations), result.Violations)
	}
	if result.Violations[0].Expectation.Rule != Populated {
		t.Errorf("the reported violation is %q, want the populated one", result.Violations[0].Expectation.Rule)
	}
}

func TestAFieldThatStartedArrivingIsAViolation(t *testing.T) {
	// As much of a change as one that stops. A sender that begins populating a national identifier has changed
	// what the site is holding.
	c := &Contract{
		MinMessages:  100,
		Expectations: []Expectation{{Path: "PID-19", Rule: Absent, MinRate: 0.99}},
	}

	result := Check(c, report(500, seg("PID", 1, 1, field("PID-19", 0.85, 1))))

	if result.Holds() {
		t.Fatal("a field that started arriving satisfied an absent expectation")
	}
	if !strings.Contains(result.Violations[0].Says, "started sending") {
		t.Errorf("the violation does not say what happened: %q", result.Violations[0].Says)
	}
}

func TestRepeatsAreAMaximumNotARate(t *testing.T) {
	// One message with forty repetitions is the one that overflows a fixed-width downstream table. Averaging it
	// away would hide exactly the case worth catching.
	c := &Contract{
		MinMessages:  100,
		Expectations: []Expectation{{Path: "PID-3", Rule: MaxRepeats, Limit: 3}},
	}

	result := Check(c, report(10000, seg("PID", 1, 1, field("PID-3", 1, 40))))

	if result.Holds() {
		t.Fatal("a single message with forty repetitions was averaged away")
	}
	if !strings.Contains(result.Violations[0].Says, "overflow") {
		t.Errorf("the violation does not say what will happen: %q", result.Violations[0].Says)
	}
}

func TestViolationsAreOrderedWorstFirst(t *testing.T) {
	// Sorting by path reads like a data structure; sorting by severity reads like advice.
	c := &Contract{
		MinMessages: 100,
		Expectations: []Expectation{
			{Path: "AAA-1", Rule: Populated, MinRate: 0.99},
			{Path: "ZZZ-1", Rule: Populated, MinRate: 0.99},
		},
	}

	result := Check(c, report(500,
		seg("AAA", 1, 1, field("AAA-1", 0.97, 1)),
		seg("ZZZ", 1, 1, field("ZZZ-1", 0.10, 1)),
	))

	if len(result.Violations) != 2 {
		t.Fatalf("violations = %d, want 2", len(result.Violations))
	}
	if result.Violations[0].Expectation.Path != "ZZZ-1" {
		t.Errorf("the worse violation is not first: %s", result.Violations[0].Expectation.Path)
	}
}

func TestAPassingResultSaysWhatItProved(t *testing.T) {
	// "No violations" is not the same as "we checked twelve things". A summary that does not say how much was
	// checked cannot be told apart from one that checked nothing.
	c := &Contract{
		MinMessages: 100,
		Expectations: []Expectation{
			{Path: "PID-3", Rule: Populated},
			{Path: "PID-5", Rule: Populated},
		},
	}

	result := Check(c, report(500, seg("PID", 1, 1,
		field("PID-3", 1, 1), field("PID-5", 1, 1))))

	if !result.Holds() {
		t.Fatalf("a healthy feed failed: %+v", result.Violations)
	}
	if !strings.Contains(result.Summary(), "2 expectation") {
		t.Errorf("the summary does not say what was checked: %q", result.Summary())
	}
}

func TestAnEmptyValueListIsRefused(t *testing.T) {
	// Treated as "no values permitted" it would fail every message and read as a broken feed rather than a
	// broken contract.
	e := Expectation{Path: "PID-8", Rule: OneOf}
	if err := e.Validate(); err == nil {
		t.Fatal("a one-of expectation with no values was accepted")
	}
}

func TestDuplicateExpectationsAreRefused(t *testing.T) {
	// Two expectations that disagree, where the stricter one is silently doing all the work.
	c := &Contract{Expectations: []Expectation{
		{Path: "PID-3", Rule: Populated, MinRate: 0.99},
		{Path: "PID-3", Rule: Populated, MinRate: 0.50},
	}}

	errs := c.Validate()
	if len(errs) == 0 {
		t.Fatal("duplicate expectations were accepted")
	}
	if !strings.Contains(errs[0].Error(), "doing nothing") {
		t.Errorf("the error does not explain the problem: %v", errs[0])
	}
}

func TestAContractWithNoExpectationsIsRefused(t *testing.T) {
	if errs := (&Contract{}).Validate(); len(errs) == 0 {
		t.Fatal("a contract that checks nothing was accepted")
	}
}

// Promotion tests. The risk here is generating a contract so long nobody reads it, or one that asserts patient
// data.

func TestPromoteOnlyAssertsFieldsThatAreAlwaysThere(t *testing.T) {
	// A field populated 60% of the time is not a guarantee, it is a fact about the current case mix, and that
	// changes every winter.
	p := report(1000, seg("PID", 1, 1,
		field("PID-3", 1.0, 1),
		field("PID-8", 0.62, 1),
	))

	c := Promote(p, PromoteOptions{})

	var paths []string
	for _, e := range c.Expectations {
		if e.Rule == Populated {
			paths = append(paths, e.Path)
		}
	}
	if len(paths) != 1 || paths[0] != "PID-3" {
		t.Errorf("promoted populated expectations = %v, want only PID-3", paths)
	}
}

func TestPromoteLeavesHeadroomBelowWhatWasObserved(t *testing.T) {
	// An expectation generated at exactly the observed rate fires on the first message that is slightly worse,
	// which is the same day it was created.
	p := report(1000, seg("PID", 1, 1, field("PID-3", 1.0, 1)))

	c := Promote(p, PromoteOptions{})
	for _, e := range c.Expectations {
		if e.Rule == Populated && e.MinRate >= 1.0 {
			t.Errorf("%s was promoted at %.3f, with no headroom below the observed rate", e.Path, e.MinRate)
		}
	}
}

func TestPromoteDoesNotAssertAValueSetThatLooksLikePatientData(t *testing.T) {
	// The judgement that matters most in the package. Getting it wrong writes patient data into a configuration
	// file that goes into version control.
	p := report(1000, seg("PID", 1, 1,
		field("PID-5", 1.0, 1,
			code("FROST^IVY^ANNE", 3), code("HALE^JUNE^MARGARET", 2), code("OKONKWO^ADAEZE", 1)),
	))

	c := Promote(p, PromoteOptions{})
	for _, e := range c.Expectations {
		if e.Rule == OneOf {
			t.Errorf("a value set was promoted from what looks like a name field: %v", e.Values)
		}
	}
}

func TestPromoteDoesAssertARealCodeSet(t *testing.T) {
	p := report(1000, seg("PID", 1, 1,
		field("PID-8", 1.0, 1, code("M", 500), code("F", 480), code("U", 20)),
	))

	c := Promote(p, PromoteOptions{})

	found := false
	for _, e := range c.Expectations {
		if e.Rule == OneOf && e.Path == "PID-8" {
			found = true
			if len(e.Values) != 3 {
				t.Errorf("values = %v, want three", e.Values)
			}
			// Sorted, because a contract file is diffed and the profile's own ordering is by frequency, which
			// changes between runs on the same feed.
			if e.Values[0] != "F" || e.Values[1] != "M" || e.Values[2] != "U" {
				t.Errorf("values = %v, want them sorted", e.Values)
			}
		}
	}
	if !found {
		t.Error("a genuine code set was not promoted")
	}
}

func TestPromoteNeverGeneratesAbsentExpectations(t *testing.T) {
	// "This field is empty today" is almost never a requirement, and generating hundreds of them would bury the
	// handful that matter.
	p := report(1000, seg("PID", 1, 1, field("PID-3", 1.0, 1)))

	for _, e := range Promote(p, PromoteOptions{}).Expectations {
		if e.Rule == Absent {
			t.Errorf("an absent expectation was generated for %s", e.Path)
		}
	}
}

func TestPromoteGivesRepeatLimitsHeadroom(t *testing.T) {
	// The observed maximum is a sample, and the next message is allowed to be slightly bigger.
	p := report(1000, seg("PID", 1, 1, field("PID-3", 1.0, 2)))

	c := Promote(p, PromoteOptions{})
	found := false
	for _, e := range c.Expectations {
		if e.Rule == MaxRepeats && e.Path == "PID-3" {
			found = true
			if e.Limit <= 2 {
				t.Errorf("limit = %d, want headroom above the observed 2", e.Limit)
			}
		}
	}
	if !found {
		t.Error("no repeat limit was promoted for a repeating field")
	}
}

func TestEveryPromotedExpectationSaysItCameFromObservation(t *testing.T) {
	// A measured expectation and a decided one are different kinds of claim, and the difference matters when
	// somebody is deciding whether to relax one.
	p := report(1000, seg("PID", 1, 1,
		field("PID-3", 1.0, 2), field("PID-8", 1.0, 1, code("M", 500), code("F", 500))))

	c := Promote(p, PromoteOptions{Source: "last month's traffic"})

	if !strings.Contains(c.DerivedFrom, "last month") {
		t.Errorf("derivedFrom = %q", c.DerivedFrom)
	}
	for _, e := range c.Expectations {
		if e.Why == "" {
			t.Errorf("%s (%s) was promoted with no reason recorded", e.Path, e.Rule)
		}
		if !strings.Contains(e.Why, "observed") {
			t.Errorf("%s does not say it was measured rather than decided: %q", e.Path, e.Why)
		}
	}
}

func TestAPromotedContractValidates(t *testing.T) {
	// Generating something the package's own validator refuses would be an obvious own goal.
	p := report(1000,
		seg("MSH", 1, 1, field("MSH-9", 1.0, 1, code("ADT", 1000))),
		seg("PID", 1, 1, field("PID-3", 1.0, 2), field("PID-5", 1.0, 1)),
		seg("OBX", 0.4, 12, field("OBX-3", 1.0, 1)),
	)

	c := Promote(p, PromoteOptions{})
	if errs := c.Validate(); len(errs) > 0 {
		t.Fatalf("a promoted contract does not validate: %v", errs)
	}
}

func TestAPromotedContractHoldsAgainstTheProfileItCameFrom(t *testing.T) {
	// The property that makes promotion trustworthy: it must not generate a contract that immediately fails on
	// the very traffic it was measured from.
	p := report(1000,
		seg("MSH", 1, 1, field("MSH-9", 1.0, 1, code("ADT", 1000))),
		seg("PID", 1, 1, field("PID-3", 1.0, 2), field("PID-5", 1.0, 1),
			field("PID-8", 1.0, 1, code("M", 500), code("F", 500))),
	)

	c := Promote(p, PromoteOptions{})
	result := Check(c, p)

	if !result.Holds() {
		t.Fatalf("a promoted contract failed against its own profile: %s\n%+v",
			result.Summary(), result.Violations)
	}
}
