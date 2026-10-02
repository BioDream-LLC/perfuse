package x12

import (
	"strings"
	"testing"
)

func validate(t *testing.T, raw string) Validation {
	t.Helper()
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m.Validate()
}

func TestAGoodInterchangeHasNoProblems(t *testing.T) {
	v := validate(t, claim837)
	if !v.OK() {
		t.Errorf("a valid interchange reported problems: %v", v.Problems)
	}
	if err := v.Err(); err != nil {
		t.Errorf("Err() = %v", err)
	}
	if v.TransactionSets != 1 || v.FunctionalGroups != 1 {
		t.Errorf("counts: sets=%d groups=%d, want 1 and 1", v.TransactionSets, v.FunctionalGroups)
	}
}

func TestATruncatedTransactionSetIsCaught(t *testing.T) {
	// The check that matters most. Half an 837 parses perfectly - it is simply missing
	// claims, and nothing about the remaining structure looks wrong. Without SE01 the
	// first sign of trouble is a payer reporting fewer claims than were sent, weeks
	// later, with no way to tell which ones vanished.
	truncated := strings.Replace(claim837, "NM1*41*2*SUBMITTER NAME*****46*SUBMITTERID~HL*1**20*1~", "", 1)

	v := validate(t, truncated)
	if v.OK() {
		t.Fatal("a transaction set missing two segments was accepted")
	}
	err := v.Err()
	if err == nil {
		t.Fatal("Err() returned nil for a truncated set")
	}
	for _, want := range []string{"SE01", "truncated"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

func TestExtraSegmentsAreCaughtToo(t *testing.T) {
	// A spliced file - two interchanges concatenated by a well-meaning script - shows
	// up as more segments than declared rather than fewer.
	spliced := strings.Replace(claim837, "CLM*", "REF*XX*EXTRA~CLM*", 1)

	v := validate(t, spliced)
	if v.OK() {
		t.Error("a transaction set with an extra segment was accepted")
	}
}

func TestAMissingIEAIsFatal(t *testing.T) {
	// The single most likely cause of a bad X12 file: the transfer stopped early.
	// Everything before it may be perfectly valid, which is exactly why it must not be
	// accepted.
	noTrailer := strings.Replace(claim837, "IEA*1*000000001~", "", 1)

	v := validate(t, noTrailer)
	if v.OK() {
		t.Fatal("an interchange with no IEA was accepted")
	}
	if !strings.Contains(v.Err().Error(), "stopped early") {
		t.Errorf("the error should name the likely cause, got: %v", v.Err())
	}
}

func TestAWrongGroupCountIsFatal(t *testing.T) {
	wrong := strings.Replace(claim837, "IEA*1*000000001~", "IEA*3*000000001~", 1)

	v := validate(t, wrong)
	if v.OK() {
		t.Fatal("IEA01 claiming three groups where there is one was accepted")
	}
	if !strings.Contains(v.Err().Error(), "missing or duplicated") {
		t.Errorf("unexpected message: %v", v.Err())
	}
}

func TestAWrongTransactionSetCountIsFatal(t *testing.T) {
	wrong := strings.Replace(claim837, "GE*1*1~", "GE*5*1~", 1)

	v := validate(t, wrong)
	if v.OK() {
		t.Fatal("GE01 claiming five transaction sets where there is one was accepted")
	}
	if !strings.Contains(v.Err().Error(), "missing") {
		t.Errorf("unexpected message: %v", v.Err())
	}
}

func TestAMismatchedControlNumberIsAWarningNotAnError(t *testing.T) {
	// Usually a generation bug rather than a truncation: the file is complete but
	// assembled wrongly. Being strict here would reject otherwise usable files from a
	// partner whose software has a bug they will not fix this quarter.
	mismatch := strings.Replace(claim837, "IEA*1*000000001~", "IEA*1*000000999~", 1)

	v := validate(t, mismatch)
	if !v.OK() {
		t.Errorf("a control number mismatch was treated as fatal: %v", v.Err())
	}
	warnings := v.Warnings()
	if len(warnings) == 0 {
		t.Fatal("a control number mismatch produced no warning either")
	}
	if !strings.Contains(strings.Join(warnings, " "), "control number") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestAMismatchedTransactionSetControlNumberWarns(t *testing.T) {
	mismatch := strings.Replace(claim837, "SE*6*0001~", "SE*6*0002~", 1)

	v := validate(t, mismatch)
	warnings := strings.Join(v.Warnings(), " ")
	if !strings.Contains(warnings, "0001") || !strings.Contains(warnings, "0002") {
		t.Errorf("both control numbers should be named, got: %v", v.Warnings())
	}
}

func TestUnbalancedGroupEnvelopesAreFatal(t *testing.T) {
	noGE := strings.Replace(claim837, "GE*1*1~", "", 1)

	v := validate(t, noGE)
	if v.OK() {
		t.Fatal("a functional group with no GE was accepted")
	}
	if !strings.Contains(v.Err().Error(), "needs both") {
		t.Errorf("unexpected message: %v", v.Err())
	}
}

func TestUnbalancedTransactionSetEnvelopesAreFatal(t *testing.T) {
	noSE := strings.Replace(claim837, "SE*6*0001~", "", 1)

	v := validate(t, noSE)
	if v.OK() {
		t.Fatal("a transaction set with no SE was accepted")
	}
}

func TestANonNumericCountIsNotTreatedAsZero(t *testing.T) {
	// Treating an unreadable count as zero turns it into a spurious mismatch, sending
	// somebody looking for missing segments that were never missing. The count is
	// skipped instead, and the control numbers still get checked.
	garbled := strings.Replace(claim837, "SE*6*0001~", "SE*ABC*0001~", 1)

	v := validate(t, garbled)
	joined := ""
	for _, p := range v.Problems {
		joined += p.Message + " "
	}
	if strings.Contains(joined, "0 are present") {
		t.Errorf("a non-numeric count was treated as zero: %v", v.Problems)
	}
}

func TestEveryProblemIsReportedNotJustTheFirst(t *testing.T) {
	// A trading partner given one problem at a time will send four more broken files,
	// and each round trip is a day.
	bad := strings.Replace(claim837, "SE*6*0001~", "SE*99*0001~", 1)
	bad = strings.Replace(bad, "GE*1*1~", "GE*7*1~", 1)
	bad = strings.Replace(bad, "IEA*1*000000001~", "IEA*4*000000001~", 1)

	v := validate(t, bad)
	err := v.Err()
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"SE01", "GE01", "IEA01"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

func TestMultipleTransactionSetsAreCountedIndividually(t *testing.T) {
	// Three sets in one group, each with its own segment count. A single aggregate
	// figure would let one short set hide behind another long one.
	multi := strings.Replace(claim837,
		"SE*6*0001~GE*1*1~",
		"SE*6*0001~ST*835*0002~REF*XX*A~SE*3*0002~ST*837*0003~REF*XX*B~SE*3*0003~GE*3*1~", 1)

	v := validate(t, multi)
	if !v.OK() {
		t.Errorf("three correctly counted sets reported problems: %v", v.Problems)
	}
	if v.TransactionSets != 3 {
		t.Errorf("TransactionSets = %d, want 3", v.TransactionSets)
	}
}

func TestAShortSetAmongGoodOnesIsStillFound(t *testing.T) {
	multi := strings.Replace(claim837,
		"SE*6*0001~GE*1*1~",
		"SE*6*0001~ST*835*0002~REF*XX*A~SE*9*0002~ST*837*0003~REF*XX*B~SE*3*0003~GE*3*1~", 1)

	v := validate(t, multi)
	if v.OK() {
		t.Fatal("a short set between two good ones was missed")
	}
	if !strings.Contains(v.Err().Error(), "0002") {
		t.Errorf("the error should identify which set, got: %v", v.Err())
	}
}

func TestTransactionSetsAreAttributedToTheirGroup(t *testing.T) {
	// A bare count of ST segments cannot tell which group each belongs to, so a file
	// with two groups would pass a naive check even with the sets in the wrong one.
	twoGroups := strings.Replace(claim837,
		"SE*6*0001~GE*1*1~IEA*1*000000001~",
		"SE*6*0001~GE*1*1~"+
			"GS*HC*SUBMITTERID*RECEIVERID*20260819*1253*2*X*005010X222A1~"+
			"ST*835*0002~REF*XX*A~SE*3*0002~GE*1*2~"+
			"IEA*2*000000001~", 1)

	v := validate(t, twoGroups)
	if !v.OK() {
		t.Errorf("two well-formed groups reported problems: %v", v.Problems)
	}
	if v.FunctionalGroups != 2 {
		t.Errorf("FunctionalGroups = %d, want 2", v.FunctionalGroups)
	}

	// Now move a set into the wrong group's count and confirm it is caught.
	wrong := strings.Replace(twoGroups, "GE*1*2~", "GE*2*2~", 1)
	if validate(t, wrong).OK() {
		t.Error("a group claiming two sets where it has one was accepted")
	}
}

func TestProblemStringDistinguishesErrorsFromWarnings(t *testing.T) {
	// The two call for different handling - reject to the sender, versus note and
	// carry on - so a reader has to be able to tell them apart at a glance.
	e := Problem{Segment: "SE", Message: "x", Fatal: true}
	w := Problem{Segment: "SE", Message: "x", Fatal: false}

	if !strings.HasPrefix(e.String(), "error:") {
		t.Errorf("fatal problem renders as %q", e.String())
	}
	if !strings.HasPrefix(w.String(), "warning:") {
		t.Errorf("non-fatal problem renders as %q", w.String())
	}
}

func TestWarningsAndErrorsAreSeparated(t *testing.T) {
	both := strings.Replace(claim837, "SE*6*0001~", "SE*99*0002~", 1)

	v := validate(t, both)
	if v.OK() {
		t.Fatal("expected a fatal problem")
	}
	if len(v.Warnings()) == 0 {
		t.Error("the control number mismatch was not reported as a warning")
	}
	if !strings.Contains(v.Err().Error(), "SE01") {
		t.Error("the fatal count mismatch is missing from Err()")
	}
	// A warning must not appear in Err(), or a caller rejecting on any error would
	// reject complete files over a cosmetic fault.
	if strings.Contains(v.Err().Error(), "control number") {
		t.Error("a warning leaked into Err()")
	}
}
