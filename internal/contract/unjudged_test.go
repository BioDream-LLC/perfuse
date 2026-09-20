package contract

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/profile"
)

// reportWithField builds a profile report containing one field, for testing what an expectation does with it.
func reportWithField(f profile.Field) *profile.Report {
	return &profile.Report{
		Messages: 200,
		Segments: []profile.Segment{{
			ID: "PID", Messages: 200, Rate: 1, Fields: []profile.Field{f},
		}},
	}
}

// TestAnExpectationWithNothingToCheckAgainstIsNotAPass is the important one.
//
// A "one of" expectation reads the vocabulary a profile recorded. A profile records values only where the field is a code set by
// definition, because that is the line between reporting the shape of a feed and retaining patient data. So an author can write a
// perfectly valid expectation against a path for which no values exist.
//
// That case used to return no violation, which is the same value as "this held". The expectation was counted among those checked, the
// contract read as green, and the check had never run once. Somebody believed they had a control on the sender's vocabulary and had
// nothing at all.
func TestAnExpectationWithNothingToCheckAgainstIsNotAPass(t *testing.T) {
	rep := reportWithField(profile.Field{
		Path: "PID-5", Present: 200, FillRate: 1, Distinct: 50,
		// No Codes, which is what a field with no code table produces.
	})

	res := Check(&Contract{Expectations: []Expectation{
		{Path: "PID-5", Rule: OneOf, Values: []string{"only-this"}},
	}}, rep)

	if res.Checked != 0 {
		t.Errorf("an expectation that could not be evaluated was counted among %d checked, so the contract "+
			"reports having proved something it did not", res.Checked)
	}
	if len(res.Unjudged) != 1 {
		t.Fatalf("the expectation was not reported as unjudged: %+v", res.Unjudged)
	}
	// The reason has to point at the likely cause, which is that the path is not a coded field - otherwise the author
	// goes looking for a fault in the profiler.
	if !strings.Contains(res.Unjudged[0], "code set") {
		t.Errorf("the reason does not say why no values are recorded: %q", res.Unjudged[0])
	}
	if res.Holds() {
		t.Error("a contract whose expectation could not be evaluated reports itself as holding, which turns " +
			"an absence of evidence into a pass")
	}
	if !strings.Contains(res.Summary(), "could not be evaluated") {
		t.Errorf("the summary does not mention it, and the summary is what somebody reads: %q", res.Summary())
	}
}

// TestAnExpectationWithVocabularyIsStillJudged guards the other direction.
//
// The check above must not swallow the case it was carved out of: a field that does have recorded values is judged normally, and an
// unexpected value is still a violation rather than becoming unjudgeable.
func TestAnExpectationWithVocabularyIsStillJudged(t *testing.T) {
	rep := reportWithField(profile.Field{
		Path: "PID-8", Present: 200, FillRate: 1, Distinct: 3,
		Codes: []profile.CodeCount{
			{Code: "M", Count: 100},
			{Code: "F", Count: 90},
			{Code: "X", Count: 10},
		},
	})

	res := Check(&Contract{Expectations: []Expectation{
		{Path: "PID-8", Rule: OneOf, Values: []string{"M", "F"}},
	}}, rep)

	if len(res.Unjudged) != 0 {
		t.Fatalf("a field with recorded values was reported as unjudgeable: %v", res.Unjudged)
	}
	if res.Checked != 1 {
		t.Errorf("checked %d expectations, want 1", res.Checked)
	}
	if len(res.Violations) != 1 {
		t.Fatalf("the unexpected value X did not produce a violation: %+v", res.Violations)
	}
	if res.Holds() {
		t.Error("a contract with a violation reports itself as holding")
	}
}

// TestAnAbsentFieldDoesNotBecomeUnjudgeable keeps the existing behaviour that avoids two findings for one cause.
//
// A path that is not present at all cannot have a wrong value, and reporting it here would double up with the "populated" expectation
// almost certainly sitting beside it. That is a deliberate silence and it must not turn into an unjudged entry, which would make every
// contract on an optional field read as unevaluable.
func TestAnAbsentFieldDoesNotBecomeUnjudgeable(t *testing.T) {
	rep := reportWithField(profile.Field{Path: "PID-8", Present: 200, FillRate: 1})

	res := Check(&Contract{Expectations: []Expectation{
		{Path: "PID-11", Rule: OneOf, Values: []string{"M"}},
	}}, rep)

	if len(res.Unjudged) != 0 {
		t.Errorf("an absent path was reported as unjudgeable, which would make every contract on an "+
			"optional field read as unevaluable: %v", res.Unjudged)
	}
	if !res.Holds() {
		t.Errorf("a contract whose only expectation concerns an absent path does not hold: %s", res.Summary())
	}
}
