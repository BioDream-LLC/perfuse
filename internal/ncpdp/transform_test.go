package ncpdp

import (
	"strings"
	"testing"
)

func compileNCPDP(t *testing.T, in []Step) *Steps {
	t.Helper()
	s, err := CompileSteps(in, nil)
	if err != nil {
		t.Fatalf("compiling the steps failed: %v", err)
	}
	return s
}

func applyNCPDP(t *testing.T, in []Step) (*Message, []Change) {
	t.Helper()
	out, changes, err := compileNCPDP(t, in).Apply(claimTransmission(t))
	if err != nil {
		t.Fatalf("applying the steps failed: %v", err)
	}
	return out, changes
}

func TestSetWritesASegmentField(t *testing.T) {
	out, changes := applyNCPDP(t, []Step{{
		Description: "redact the cardholder",
		Set:         &SetStep{Path: "04-C2", Value: "REDACTED"},
	}})

	got := Read(*out, mustPath(t, "04-C2"))
	if len(got) != 1 || got[0] != "REDACTED" {
		t.Fatalf("04-C2 = %v, want [REDACTED]", got)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %+v", changes)
	}
	if changes[0].From != "MEMBER12345" || changes[0].To != "REDACTED" {
		t.Errorf("the change should record both values, got %+v", changes[0])
	}
	if changes[0].Description != "redact the cardholder" {
		t.Errorf("the change should carry the step's description, got %q", changes[0].Description)
	}
}

func TestSetWritesAHeaderField(t *testing.T) {
	out, changes := applyNCPDP(t, []Step{{
		Set: &SetStep{Path: "A4", Value: "PLANB99999"},
	}})

	if out.Header.ProcessorControlNumber != "PLANB99999" {
		t.Fatalf("the processor control number is %q", out.Header.ProcessorControlNumber)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %+v", changes)
	}
}

func TestTheReceiverIsNeverModified(t *testing.T) {
	original := claimTransmission(t)
	before := Read(*original, mustPath(t, "04-C2"))

	steps := compileNCPDP(t, []Step{{Set: &SetStep{Path: "04-C2", Value: "CHANGED"}}})
	if _, _, err := steps.Apply(original); err != nil {
		t.Fatal(err)
	}

	after := Read(*original, mustPath(t, "04-C2"))
	if len(after) != 1 || after[0] != before[0] {
		t.Fatalf("the original was modified: %v -> %v", before, after)
	}
}

func TestWritingAnAmbiguousPathIsRefused(t *testing.T) {
	// D7 addresses the product code of both claims. Writing it has no single meaning: writing both would change a drug
	// nobody asked about and writing the first would silently leave the second.
	_, _, err := compileNCPDP(t, []Step{{Set: &SetStep{Path: "D7", Value: "X"}}}).Apply(claimTransmission(t))
	if err == nil {
		t.Fatal("writing an ambiguous path should be refused")
	}
	if !strings.Contains(err.Error(), "no single meaning") {
		t.Errorf("the error should say why, got: %v", err)
	}
	// And it should name the form that would work.
	if !strings.Contains(err.Error(), "(1)") {
		t.Errorf("the error should name the qualified form, got: %v", err)
	}
}

func TestAnAmbiguousPathReadsAsAbsentSoAStepSkipsIt(t *testing.T) {
	// A trim on an ambiguous path must not pick one of the fields. It reads as absent, so the step does nothing, which is
	// the only safe answer that is not an error.
	_, changes := applyNCPDP(t, []Step{{Trim: &TrimStep{Path: "D7"}}})
	if len(changes) != 0 {
		t.Errorf("a step on an ambiguous path reported %d change(s): %+v", len(changes), changes)
	}
}

func TestAQualifiedPathWritesOneOccurrence(t *testing.T) {
	out, changes := applyNCPDP(t, []Step{{
		Set: &SetStep{Path: "07(2)-D7", Value: "99999999999"},
	}})

	all := Read(*out, mustPath(t, "D7"))
	if len(all) != 2 {
		t.Fatalf("expected two product codes, got %v", all)
	}
	if all[0] != "00093721410" {
		t.Errorf("the first claim should be untouched, got %q", all[0])
	}
	if all[1] != "99999999999" {
		t.Errorf("the second claim should be rewritten, got %q", all[1])
	}
	if len(changes) != 1 {
		t.Errorf("expected one change, got %+v", changes)
	}
}

func TestAStepWritingAnOverLongHeaderFieldIsRefused(t *testing.T) {
	// Truncating a fixed-width header field produces a well-formed transmission carrying the wrong value, which the
	// switch has no way to know was not meant.
	_, _, err := compileNCPDP(t, []Step{{
		Set: &SetStep{Path: "A1", Value: "1234567890"},
	}}).Apply(claimTransmission(t))
	if err == nil {
		t.Fatal("an over-long header field should be refused")
	}
	if !strings.Contains(err.Error(), "fixed width") {
		t.Errorf("the error should explain why, got: %v", err)
	}
}

func TestTheTransactionCountCannotBeWritten(t *testing.T) {
	// A9 is derived from the transmission when it is written, so a step changing it would describe a transmission that
	// does not exist. Refused at load, not at the write.
	_, err := CompileSteps([]Step{{Set: &SetStep{Path: "A9", Value: "4"}}}, nil)
	if err == nil {
		t.Fatal("writing the transaction count should be refused")
	}
	if !strings.Contains(err.Error(), "transaction count") {
		t.Errorf("the error should name the field, got: %v", err)
	}
}

func TestTheVersionCannotBeWritten(t *testing.T) {
	_, err := CompileSteps([]Step{{Set: &SetStep{Path: "A2", Value: "E1"}}}, nil)
	if err == nil {
		t.Fatal("rewriting the version should be refused")
	}
	if !strings.Contains(err.Error(), "without converting") {
		t.Errorf("the error should explain why, got: %v", err)
	}
}

func TestAConditionalStepOnlyRunsWhenItHolds(t *testing.T) {
	// The shared grammar, over NCPDP paths.
	out, changes := applyNCPDP(t, []Step{{
		When: `A3 == "B1"`,
		Set:  &SetStep{Path: "04-C2", Value: "BILLED"},
	}})
	if got := Read(*out, mustPath(t, "04-C2")); got[0] != "BILLED" {
		t.Fatalf("the step should have run, C2 = %q", got[0])
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %+v", changes)
	}

	out, changes = applyNCPDP(t, []Step{{
		When: `A3 == "B2"`,
		Set:  &SetStep{Path: "04-C2", Value: "REVERSED"},
	}})
	if got := Read(*out, mustPath(t, "04-C2")); got[0] != "MEMBER12345" {
		t.Fatalf("the step should have been skipped, C2 = %q", got[0])
	}
	if len(changes) != 0 {
		t.Errorf("a skipped step reported %+v", changes)
	}
}

func TestSettingAFieldTheTransmissionDoesNotCarryAddsIt(t *testing.T) {
	// The ordinary reason to write a field at all is that a partner omitted it.
	out, changes := applyNCPDP(t, []Step{{
		Set: &SetStep{Path: "07-D3", Value: "1"},
	}})
	got := Read(*out, mustPath(t, "07(1)-D3"))
	if len(got) != 1 || got[0] != "1" {
		t.Fatalf("07-D3 = %v, want [1]", got)
	}
	if len(changes) != 1 {
		t.Errorf("expected one change, got %+v", changes)
	}
}

func TestAddingAFieldWithNoSegmentIsRefused(t *testing.T) {
	// There is nowhere to put it, and guessing a segment would put a field in the wrong one.
	_, _, err := compileNCPDP(t, []Step{{
		Set: &SetStep{Path: "ZZ", Value: "1"},
	}}).Apply(claimTransmission(t))
	if err == nil {
		t.Fatal("adding a field with no segment should be refused")
	}
	if !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("the error should say why, got: %v", err)
	}
}

func TestTheStepsRoundTripThroughBuild(t *testing.T) {
	// The point of the whole exercise: a transformed transmission has to be writable back onto the wire, and the header
	// count has to match what is actually there.
	out, _ := applyNCPDP(t, []Step{{Set: &SetStep{Path: "04-C2", Value: "REDACTED"}}})

	out.SetHeaderCount()
	raw, err := Build(*out)
	if err != nil {
		t.Fatalf("the transformed transmission would not build: %v", err)
	}

	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("the built transmission would not parse: %v", err)
	}
	if got := Read(back, mustPath(t, "04-C2")); len(got) != 1 || got[0] != "REDACTED" {
		t.Errorf("after a round trip 04-C2 = %v", got)
	}
	if err := back.CheckHeaderCount(); err != nil {
		t.Errorf("the header count disagrees with the transmission: %v", err)
	}
}

func mustPath(t *testing.T, raw string) Path {
	t.Helper()
	p, err := ParsePath(raw)
	if err != nil {
		t.Fatalf("ParsePath(%q): %v", raw, err)
	}
	return p
}
