package parity

import (
	"fmt"
	"strings"
	"testing"
)

// fakeEngine applies a substitution, standing in for a channel.
type fakeEngine struct {
	replace map[string]string
	failOn  string
	err     error
}

func (f fakeEngine) Transform(raw []byte) ([]byte, error) {
	s := string(raw)
	if f.failOn != "" && strings.Contains(s, f.failOn) {
		return nil, f.err
	}
	for from, to := range f.replace {
		s = strings.ReplaceAll(s, from, to)
	}
	return []byte(s), nil
}

func msg(control, sex string) []byte {
	return []byte(fmt.Sprintf(
		"MSH|^~\\&|SEND|FAC|RECV|RFAC|20260101120000||ADT^A08|%s|P|2.5\r"+
			"PID|1||MRN%s^^^FAC^MR||SURNAME^GIVEN||19800101|%s\r", control, control, sex))
}

// pairsWhere builds n pairs where the old engine produced exactly what we will produce.
func identicalPairs(n int) []Pair {
	var out []Pair
	for i := range n {
		m := msg(fmt.Sprintf("C%04d", i), "M")
		out = append(out, Pair{Input: m, Expected: m, Reference: fmt.Sprintf("mirth-%d", i)})
	}
	return out
}

// The outcome somebody needs before moving a live feed.
func TestIdenticalOutputIsReportedAsIdenticalAndQuotably(t *testing.T) {
	r, err := Compare(fakeEngine{}, identicalPairs(500))
	if err != nil {
		t.Fatal(err)
	}

	if r.Identical != 500 || r.Differing != 0 || r.Failed != 0 {
		t.Fatalf("identical=%d differing=%d failed=%d, want 500/0/0", r.Identical, r.Differing, r.Failed)
	}
	// The verdict has to be usable in a change request without editing.
	if !strings.Contains(r.Verdict, "500") || !strings.Contains(r.Verdict, "identical") {
		t.Errorf("verdict = %q", r.Verdict)
	}
}

// The finding that makes a migration decidable: one systematic difference, not four thousand problems.
//
// A run reporting four thousand differences has told somebody nothing they can act on. The same run reporting that PID-8
// differs in every message because the old engine wrote Male where we write M is one decision.
func TestASystematicDifferenceIsReportedAsOneFindingWithACount(t *testing.T) {
	// The old engine expanded the sex code; we do not.
	var pairs []Pair
	for i := range 4000 {
		in := msg(fmt.Sprintf("C%04d", i), "M")
		expected := strings.Replace(string(in), "|M\r", "|Male\r", 1)
		pairs = append(pairs, Pair{Input: in, Expected: []byte(expected), Reference: fmt.Sprintf("mirth-%d", i)})
	}

	r, err := Compare(fakeEngine{}, pairs)
	if err != nil {
		t.Fatal(err)
	}

	if r.Differing != 4000 {
		t.Fatalf("differing = %d, want 4000", r.Differing)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(r.Findings), r.Findings)
	}

	f := r.Findings[0]
	if f.Messages != 4000 {
		t.Errorf("the finding covers %d messages, want 4000", f.Messages)
	}
	if !f.Systematic {
		t.Error("a difference that is the same every time was not reported as systematic, which is what makes it " +
			"one decision rather than four thousand problems")
	}
	if f.DistinctPairs != 1 {
		t.Errorf("distinct pairs = %d, want 1", f.DistinctPairs)
	}
	if len(f.Examples) == 0 || f.Examples[0].Expected != "Male" || f.Examples[0].Got != "M" {
		t.Errorf("the example does not show the substitution: %+v", f.Examples)
	}
	// And a reference back into the system somebody still has open.
	if f.Examples[0].Reference == "" {
		t.Error("the example has no reference, so there is no message to go and look at")
	}

	if !strings.Contains(r.Verdict, "one decision") {
		t.Errorf("the verdict does not say the differences are systematic: %q", r.Verdict)
	}
}

// Content differing is a different and much worse problem, and must not read the same as a mapping difference.
func TestContentDifferingIsDistinguishedFromASystematicMapping(t *testing.T) {
	var pairs []Pair
	for i := range 50 {
		in := msg(fmt.Sprintf("C%04d", i), "M")
		// A different surname every time: not a mapping, the field's content.
		expected := strings.Replace(string(in), "SURNAME", fmt.Sprintf("NAME%d", i), 1)
		pairs = append(pairs, Pair{Input: in, Expected: []byte(expected)})
	}

	r, err := Compare(fakeEngine{}, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) == 0 {
		t.Fatal("no findings")
	}
	f := r.Findings[0]
	if f.Systematic {
		t.Error("fifty different values were reported as a systematic difference")
	}
	if f.DistinctPairs < 50 {
		t.Errorf("distinct pairs = %d, want 50; the count is what says this is content rather than a mapping",
			f.DistinctPairs)
	}
	// Examples are capped, or the report is a wall of text.
	if len(f.Examples) > maxExamples {
		t.Errorf("%d examples kept, want at most %d", len(f.Examples), maxExamples)
	}
}

// Findings must be ordered by impact, not by map iteration.
func TestFindingsComeInOrderOfImpact(t *testing.T) {
	var pairs []Pair
	for i := range 100 {
		in := msg(fmt.Sprintf("C%04d", i), "M")
		s := string(in)
		// Every message differs at PID-8.
		s = strings.Replace(s, "|M\r", "|Male\r", 1)
		// Only ten differ at MSH-3.
		if i < 10 {
			s = strings.Replace(s, "|SEND|", "|SENDER|", 1)
		}
		pairs = append(pairs, Pair{Input: in, Expected: []byte(s)})
	}

	r, err := Compare(fakeEngine{}, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) < 2 {
		t.Fatalf("got %d findings, want at least 2", len(r.Findings))
	}
	if r.Findings[0].Messages < r.Findings[1].Messages {
		t.Errorf("findings are not ordered by impact: %d then %d",
			r.Findings[0].Messages, r.Findings[1].Messages)
	}
}

// A message the channel cannot process is a different problem from one it processes wrongly.
//
// The first is usually a configuration gap the importer could not fill; the second is a mapping decision. Counting them
// together would have somebody chase the wrong one.
func TestAFailureIsCountedSeparatelyFromADifference(t *testing.T) {
	pairs := identicalPairs(100)
	// Ten will fail.
	for i := range 10 {
		pairs[i].Input = []byte("MSH|^~\\&|POISON|" + fmt.Sprint(i))
		pairs[i].Reference = fmt.Sprintf("mirth-bad-%d", i)
	}

	r, err := Compare(fakeEngine{failOn: "POISON", err: fmt.Errorf("no destination accepted the message")}, pairs)
	if err != nil {
		t.Fatal(err)
	}

	if r.Failed != 10 {
		t.Errorf("failed = %d, want 10", r.Failed)
	}
	if r.Differing != 0 {
		t.Errorf("differing = %d; a message that could not be processed is not a difference in output", r.Differing)
	}
	if len(r.FailureReasons) != 1 {
		t.Fatalf("got %d failure reasons, want 1 grouped: %+v", len(r.FailureReasons), r.FailureReasons)
	}
	if r.FailureReasons[0].Messages != 10 {
		t.Errorf("the reason covers %d messages, want 10", r.FailureReasons[0].Messages)
	}
	if r.FailureReasons[0].Reference == "" {
		t.Error("the failure reason names no message, so there is nothing to go and look at")
	}
	if !strings.Contains(r.Verdict, "could not be processed") {
		t.Errorf("the verdict does not mention the failures: %q", r.Verdict)
	}
}

// Everything failing must be called out as a configuration problem, not reported as a parity result.
func TestEverythingFailingSaysToLookAtTheConfigurationFirst(t *testing.T) {
	r, err := Compare(fakeEngine{failOn: "MSH", err: fmt.Errorf("channel is not configured")}, identicalPairs(20))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Verdict, "configuration gap") {
		t.Errorf("the verdict reads as a parity result rather than a setup problem: %q", r.Verdict)
	}
}

// A cosmetic difference must not be counted as identical, nor as a real difference.
//
// Trailing empty fields carry no information, so treating them as failures would bury real findings under thousands of
// cosmetic ones. But they are not identical either, and a receiver doing strict parsing may disagree - so the number
// gets its own line.
func TestACosmeticDifferenceIsReportedSeparately(t *testing.T) {
	var pairs []Pair
	for i := range 30 {
		in := msg(fmt.Sprintf("C%04d", i), "M")
		// Trailing empty fields on PID, which change nothing.
		expected := strings.Replace(string(in), "|19800101|M\r", "|19800101|M|||\r", 1)
		pairs = append(pairs, Pair{Input: in, Expected: []byte(expected)})
	}

	r, err := Compare(fakeEngine{}, pairs)
	if err != nil {
		t.Fatal(err)
	}

	if r.Identical != 0 {
		t.Errorf("identical = %d; the bytes were not identical", r.Identical)
	}
	if r.Equivalent != 30 {
		t.Errorf("equivalent = %d, want 30 (differing = %d)", r.Equivalent, r.Differing)
	}
	if r.Differing != 0 {
		t.Errorf("differing = %d; trailing empty fields carry no information", r.Differing)
	}
	// Reported, not silently folded into identical.
	if !strings.Contains(r.Verdict, "no information") {
		t.Errorf("the verdict does not explain the cosmetic differences: %q", r.Verdict)
	}
}

// No pairs is refused with something that says how to get them.
//
// This is the honest constraint at the centre of the feature: it cannot reach into the old engine, and a tool that
// claimed to verify a migration without ever seeing the old engine's output would be verifying nothing.
func TestNoPairsIsRefusedWithHowToGetThem(t *testing.T) {
	_, err := Compare(fakeEngine{}, nil)
	if err == nil {
		t.Fatal("comparing nothing was accepted")
	}
	for _, want := range []string{"message browser", "verifying nothing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal omits %q: %v", want, err)
		}
	}
}

// The verdict must not round to something flattering.
//
// 49,993 of 50,000 is a different statement from "over 99%", and the seven are the whole point.
func TestTheVerdictReportsExactCountsRatherThanAPercentage(t *testing.T) {
	pairs := identicalPairs(50000)
	for i := range 7 {
		s := strings.Replace(string(pairs[i].Expected), "|M\r", "|F\r", 1)
		pairs[i].Expected = []byte(s)
	}

	r, err := Compare(fakeEngine{}, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Verdict, "49993") {
		t.Errorf("the verdict does not give the exact count: %q", r.Verdict)
	}
	if strings.Contains(r.Verdict, "99.9") || strings.Contains(r.Verdict, "%") {
		t.Errorf("the verdict rounds to a percentage, which hides the seven: %q", r.Verdict)
	}
}
