package x12

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/codeset"
)

func compile(t *testing.T, steps []Step, tables *codeset.Set) *Steps {
	t.Helper()
	s, err := CompileSteps(steps, tables)
	if err != nil {
		t.Fatalf("the steps do not compile: %v", err)
	}
	return s
}

func apply(t *testing.T, steps []Step, tables *codeset.Set) (*Message, []Change) {
	t.Helper()
	out, changes, err := compile(t, steps, tables).Apply(parseClaim(t))
	if err != nil {
		t.Fatalf("applying the steps failed: %v", err)
	}
	return out, changes
}

func TestSetWritesAValueAndReportsTheChange(t *testing.T) {
	out, changes := apply(t, []Step{{
		Description: "anonymise the patient control number",
		Set:         &SetStep{Path: "CLM01", Value: "REDACTED"},
	}}, nil)

	if got, _ := ReadString(out, "CLM-1"); got != "REDACTED" {
		t.Fatalf("CLM-1 = %q", got)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %+v", changes)
	}
	// The description travels with the change, because a trace that says CLM-1 changed is far less useful than one that
	// says why somebody decided it should.
	if changes[0].Description != "anonymise the patient control number" {
		t.Fatalf("the description was lost: %+v", changes[0])
	}
	if changes[0].From != "PATIENT001" || changes[0].To != "REDACTED" {
		t.Fatalf("the change does not record both values: %+v", changes[0])
	}
}

func TestWritingTheValueAlreadyThereIsNotReportedAsAChange(t *testing.T) {
	// Recorded in docs/queue.md from the HL7 path: a no-op counted as a change told somebody their step had worked when
	// it did nothing. Same defect, same refusal, this time before it shipped.
	_, changes := apply(t, []Step{{
		Set: &SetStep{Path: "CLM01", Value: "PATIENT001"},
	}}, nil)

	if len(changes) != 0 {
		t.Fatalf("writing the existing value changed nothing, so it must not be reported: %+v", changes)
	}
}

func TestStepsSeeTheChangesOfEarlierSteps(t *testing.T) {
	// The order a reader of the file expects. If each step saw the original, the same steps in one channel would behave
	// differently from the same steps split across two.
	out, changes := apply(t, []Step{
		{Set: &SetStep{Path: "CLM01", Value: "FIRST"}},
		{Replace: &ReplaceStep{Path: "CLM01", Pattern: "^FIRST$", With: "SECOND"}},
	}, nil)

	if got, _ := ReadString(out, "CLM-1"); got != "SECOND" {
		t.Fatalf("CLM-1 = %q, want SECOND", got)
	}
	if len(changes) != 2 {
		t.Fatalf("expected two changes, got %+v", changes)
	}
}

func TestCopyMovesAValueBetweenPaths(t *testing.T) {
	out, _ := apply(t, []Step{{
		Copy: &CopyStep{From: "NM1(2)-3", To: "REF-2"},
	}}, nil)

	if got, _ := ReadString(out, "REF-2"); got != "RECEIVER NAME" {
		t.Fatalf("REF-2 = %q", got)
	}
	// The source must be untouched. A copy that moves rather than copies is a different operation.
	if got, _ := ReadString(out, "NM1(2)-3"); got != "RECEIVER NAME" {
		t.Fatalf("the source changed: %q", got)
	}
}

func TestCopyFromAnAbsentElementChangesNothingRatherThanFailing(t *testing.T) {
	// An element the partner did not send is ordinary. Failing would turn every optional field into a required one.
	out, changes := apply(t, []Step{{
		Copy: &CopyStep{From: "REF-9", To: "CLM01"},
	}}, nil)

	if got, _ := ReadString(out, "CLM-1"); got != "PATIENT001" {
		t.Fatalf("the target should be untouched, got %q", got)
	}
	if len(changes) != 0 {
		t.Fatalf("nothing was copied, so nothing should be reported: %+v", changes)
	}
}

func TestClearEmptiesWithoutRemoving(t *testing.T) {
	out, _ := apply(t, []Step{{Clear: &ClearStep{Path: "CLM01"}}}, nil)

	got, ok := ReadString(out, "CLM-1")
	if !ok {
		t.Fatal("a cleared element must remain present; absent and blank mean different things in X12")
	}
	if got != "" {
		t.Fatalf("CLM-1 = %q, want empty", got)
	}
}

func TestTrimRemovesSurroundingWhitespace(t *testing.T) {
	// Trim exists as its own step because X12 pads constantly, and a named step is readable where a pattern is not.
	out, changes := apply(t, []Step{
		{Set: &SetStep{Path: "REF02", Value: "  CLAIM123  "}},
		{Trim: &TrimStep{Path: "REF02"}},
	}, nil)

	if got, _ := ReadString(out, "REF-2"); got != "CLAIM123" {
		t.Fatalf("REF-2 = %q, want CLAIM123", got)
	}
	if len(changes) != 2 {
		t.Fatalf("expected two changes, got %+v", changes)
	}
}

func TestTrimmingAnISAElementIsRefusedBecauseItCouldNeverWork(t *testing.T) {
	// The finding that produced the ISA width handling. ISA is the one segment whose element lengths are semantic, so a
	// trim there is padded straight back and can never have an effect. Left as a no-op it would be invisible.
	_, err := CompileSteps([]Step{{Trim: &TrimStep{Path: "ISA06"}}}, nil)
	if err == nil {
		t.Fatal("trimming a fixed-width ISA element should be refused")
	}
	if !strings.Contains(err.Error(), "fixed width") {
		t.Fatalf("the error should say why, got: %v", err)
	}
}

func TestShorteningAnISAElementKeepsTheSegmentIntact(t *testing.T) {
	// The bug this came from: shortening ISA06 shifted every byte after it, and the interchange came back reporting its
	// component delimiter as "C" - a letter out of RECEIVER. ISA16 is found by counting bytes, not by splitting.
	out, err := SetString(parseClaim(t), "ISA06", "NEWSUB")
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	if got, _ := ReadString(out, "ISA-6"); got != "NEWSUB         " {
		t.Fatalf("ISA-6 = %q, want the value padded to fifteen characters", got)
	}
	if d := out.Delimiters(); d.Component != ':' {
		t.Fatalf("the component delimiter became %q, so the ISA segment was shifted", string(d.Component))
	}
	if v := out.Validate(); !v.OK() {
		t.Fatalf("the interchange no longer validates: %v", v.Err())
	}
}

func TestAnISAValueTooLongToFitIsRefusedRatherThanTruncated(t *testing.T) {
	// Silently shortening a submitter identifier produces an interchange a partner accepts and attributes to somebody
	// else, which is worse than a channel that will not send it.
	_, err := SetString(parseClaim(t), "ISA06", "THIS IDENTIFIER IS FAR TOO LONG")
	if err == nil {
		t.Fatal("a value too long for a fixed-width ISA element should be refused")
	}
	if !strings.Contains(err.Error(), "shift every byte") {
		t.Fatalf("the error should say what would happen, got: %v", err)
	}
}

func TestCaseChangesTheValue(t *testing.T) {
	out, _ := apply(t, []Step{{Case: &CaseStep{Path: "NM1-3", To: "lower"}}}, nil)

	if got, _ := ReadString(out, "NM1-3"); got != "submitter name" {
		t.Fatalf("NM1-3 = %q", got)
	}
}

func TestMapTranslatesThroughATable(t *testing.T) {
	set := &codeset.Set{Tables: []codeset.Table{{
		Name:      "payers",
		Describes: "our submitter identifiers to the clearing house's",
		Entries: []codeset.Entry{
			{From: "123456789", To: "CH0001"},
		},
	}}}
	set.Compile()

	out, changes := apply(t, []Step{{
		Map: &MapStep{Path: "NM1-9", Table: "payers"},
	}}, set)

	if got, _ := ReadString(out, "NM1-9"); got != "CH0001" {
		t.Fatalf("NM1-9 = %q, want CH0001", got)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %+v", changes)
	}
}

func TestAnUnmappedValueIsKeptByDefaultAndCanBeMadeToFail(t *testing.T) {
	set := &codeset.Set{Tables: []codeset.Table{{
		Name:      "payers",
		Describes: "our submitter identifiers to the clearing house's",
		Entries:   []codeset.Entry{{From: "999", To: "CH9999"}},
	}}}
	set.Compile()

	// Kept by default. There is deliberately no option that blanks it: a claim with an empty payer identifier is
	// rejected downstream in a way that points at the wrong system.
	out, changes := apply(t, []Step{{Map: &MapStep{Path: "NM1-9", Table: "payers"}}}, set)
	if got, _ := ReadString(out, "NM1-9"); got != "123456789" {
		t.Fatalf("an unmapped value should be left alone, got %q", got)
	}
	if len(changes) != 0 {
		t.Fatalf("nothing changed, so nothing should be reported: %+v", changes)
	}

	// And the site that would rather stop than send an unrecognised code can say so.
	steps := compile(t, []Step{{
		Map: &MapStep{Path: "NM1-9", Table: "payers", OnMissing: "fail"},
	}}, set)
	if _, _, err := steps.Apply(parseClaim(t)); err == nil {
		t.Fatal("on_missing: fail should stop the message")
	} else if !strings.Contains(err.Error(), "not in table") {
		t.Fatalf("the error should name the problem, got: %v", err)
	}
}

func TestTwoActionsInOneStepIsRefused(t *testing.T) {
	// Their order is undefined, and a configuration whose meaning depends on what the reader assumes is worse than one
	// that will not load.
	_, err := CompileSteps([]Step{{
		Set:   &SetStep{Path: "CLM01", Value: "A"},
		Clear: &ClearStep{Path: "CLM02"},
	}}, nil)

	if err == nil {
		t.Fatal("two actions in one step should be refused")
	}
	if !strings.Contains(err.Error(), "order is undefined") {
		t.Fatalf("the error should say why, got: %v", err)
	}
}

func TestAStepWithNoActionIsRefusedAndListsTheOptions(t *testing.T) {
	_, err := CompileSteps([]Step{{Description: "does nothing"}}, nil)
	if err == nil {
		t.Fatal("a step with no action should be refused")
	}
	// Listing the options turns an error into documentation at the moment somebody needs it.
	for _, want := range []string{"set", "copy", "clear", "map", "replace", "trim", "case"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error should list %q as an option, got: %v", want, err)
		}
	}
}

func TestABadPathIsRefusedAtLoadAndNamesTheKey(t *testing.T) {
	// At load, not on the first message: the operator who deployed the channel is watching now and will not be at three
	// the following morning.
	_, err := CompileSteps([]Step{{Set: &SetStep{Path: "not a path!", Value: "x"}}}, nil)
	if err == nil {
		t.Fatal("a bad path should be refused at load")
	}
	if !strings.Contains(err.Error(), "set.path") {
		t.Fatalf("the error should name the key, got: %v", err)
	}
}

func TestAPathAddressingAWholeSegmentIsRefusedAtLoad(t *testing.T) {
	_, err := CompileSteps([]Step{{Set: &SetStep{Path: "CLM", Value: "x"}}}, nil)
	if err == nil {
		t.Fatal("a whole-segment path should be refused")
	}
	if !strings.Contains(err.Error(), "CLM-1") {
		t.Fatalf("the error should name what to write instead, got: %v", err)
	}
}

func TestABadPatternIsRefusedAtLoad(t *testing.T) {
	// The mistake recorded in the v3 filter: one form compiled its pattern at load and the other did not, so one failed
	// to start and the other failed on every message.
	_, err := CompileSteps([]Step{{
		Replace: &ReplaceStep{Path: "CLM01", Pattern: "([unclosed", With: "x"},
	}}, nil)

	if err == nil {
		t.Fatal("a pattern that does not compile should be refused at load")
	}
	if !strings.Contains(err.Error(), "replace.pattern") {
		t.Fatalf("the error should name the key, got: %v", err)
	}
}

func TestCopyingAPathToItselfIsRefused(t *testing.T) {
	// It cannot change anything, so it is almost certainly a typo in one of the two paths.
	_, err := CompileSteps([]Step{{Copy: &CopyStep{From: "CLM01", To: "CLM-1"}}}, nil)
	if err == nil {
		t.Fatal("copying a path to itself should be refused")
	}
}

func TestAnUnknownTableIsRefusedAtLoad(t *testing.T) {
	set := &codeset.Set{Tables: []codeset.Table{{Name: "payers", Describes: "x"}}}
	set.Compile()

	_, err := CompileSteps([]Step{{Map: &MapStep{Path: "NM1-9", Table: "typo"}}}, set)
	if err == nil {
		t.Fatal("an unknown table should be refused at load")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("the error should name the table asked for, got: %v", err)
	}
}

func TestAStepWithAConditionOnlyRunsWhenItHolds(t *testing.T) {
	// The condition that holds.
	out, changes := apply(t, []Step{{
		When: `CLM01 == "PATIENT001"`,
		Set:  &SetStep{Path: "CLM02", Value: "0"},
	}}, nil)
	if got, _ := ReadString(out, "CLM-2"); got != "0" {
		t.Fatalf("the step should have run: CLM-2 = %q", got)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %+v", changes)
	}

	// And the one that does not. The step must leave the interchange alone and report nothing, because a skipped step
	// that reported a change would be indistinguishable from one that ran.
	out, changes = apply(t, []Step{{
		When: `CLM01 == "SOMEONE ELSE"`,
		Set:  &SetStep{Path: "CLM02", Value: "0"},
	}}, nil)
	if got, _ := ReadString(out, "CLM-2"); got != "500" {
		t.Fatalf("the step should have been skipped: CLM-2 = %q", got)
	}
	if len(changes) != 0 {
		t.Fatalf("a skipped step must report nothing, got %+v", changes)
	}
}

func TestConditionsUseTheSameGrammarAsAnHL7Filter(t *testing.T) {
	// The point of making internal/expr generic. Somebody who can filter an HL7 feed can write these, and each of these
	// forms is the same keyword they already use.
	for _, when := range []string{
		`CLM01 exists`,
		`CLM-1 == "PATIENT001"`,
		`NM1(2)-3 =~ "(?i)receiver"`,
		`REF-1 in ["D9", "EA"]`,
		`not CLM01 empty`,
		`CLM01 exists and REF-1 == "D9"`,
		`CLM02 > 100`,
		`REF-9 empty`,
	} {
		steps, err := CompileSteps([]Step{{When: when, Set: &SetStep{Path: "CLM02", Value: "1"}}}, nil)
		if err != nil {
			t.Fatalf("%s: did not compile: %v", when, err)
		}
		out, _, err := steps.Apply(parseClaim(t))
		if err != nil {
			t.Fatalf("%s: did not evaluate: %v", when, err)
		}
		if got, _ := ReadString(out, "CLM-2"); got != "1" {
			t.Errorf("%s: expected the condition to hold, CLM-2 = %q", when, got)
		}
	}
}

func TestAConditionThatDoesNotParseRefusesTheChannelAtLoad(t *testing.T) {
	_, err := CompileSteps([]Step{{
		When: `CLM01 ===== "x"`,
		Set:  &SetStep{Path: "CLM02", Value: "0"},
	}}, nil)
	if err == nil {
		t.Fatal("a condition that does not parse should be refused at load")
	}
	if !strings.Contains(err.Error(), "when") {
		t.Fatalf("the error should name the key, got: %v", err)
	}
}

func TestAConditionNamingABadPathIsRefusedAtLoad(t *testing.T) {
	// The path is compiled by the resolver at parse time, so a path X12 cannot address fails here rather than on the
	// first claim.
	_, err := CompileSteps([]Step{{
		When: `"not a path!" == "x"`,
		Set:  &SetStep{Path: "CLM02", Value: "0"},
	}}, nil)
	if err == nil {
		t.Fatal("a condition with an unaddressable path should be refused at load")
	}
}

func TestApplyDoesNotModifyTheMessageItWasGiven(t *testing.T) {
	m := parseClaim(t)
	original := string(m.Raw())

	steps := compile(t, []Step{{Set: &SetStep{Path: "CLM01", Value: "CHANGED"}}}, nil)
	if _, _, err := steps.Apply(m); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	if string(m.Raw()) != original {
		t.Fatal("Apply modified the message it was given")
	}
}

func TestTheResultOfASequenceStillValidates(t *testing.T) {
	out, _ := apply(t, []Step{
		{Set: &SetStep{Path: "CLM01", Value: "REDACTED"}},
		{Trim: &TrimStep{Path: "REF02"}},
		{Case: &CaseStep{Path: "NM1-3", To: "upper"}},
	}, nil)

	// The independent check: the envelope validator counts segments and reads control numbers rather than trusting the
	// parse. It caught a bad fixture during the accessor work, so it has earned being run here too.
	if v := out.Validate(); !v.OK() {
		t.Fatalf("the transformed interchange does not validate: %v", v.Err())
	}
}

func TestNoStepsChangesNothing(t *testing.T) {
	m := parseClaim(t)
	out, changes, err := compile(t, nil, nil).Apply(m)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
	if string(out.Raw()) != string(m.Raw()) {
		t.Fatal("an empty sequence should return the message unchanged")
	}
}

// TestANeutralisedWriteReportsNoChange covers the reason the engine re-reads a path after writing it.
//
// ISA is the only X12 segment whose element lengths are semantic, so a write to it is padded back to the standard width.
// That means a write can be neutralised: the value requested differs from what was there, and the value that lands does
// not. Comparing the request against the previous value would report a change that did not happen, and a trace showing
// changes that did not happen is worse than no trace.
//
// This was originally found through a trim on an ISA element. That case is now refused at load, which removed the only
// scenario exercising the protection - so the protection stopped being tested without anybody changing it. Found by
// deleting the re-read and watching every test still pass.
func TestANeutralisedWriteReportsNoChange(t *testing.T) {
	// ISA06 is the sender identifier, fixed at 15 characters and space padded. The fixture carries "SUBMITTER" in it.
	before, ok := ReadString(parseClaim(t), "ISA-6")
	if !ok {
		t.Fatal("the fixture should carry ISA06")
	}
	if len(before) != 15 {
		t.Fatalf("ISA06 should be padded to 15 characters, got %d (%q)", len(before), before)
	}

	// Writing the trimmed form asks for something different from what is there, and lands as exactly what is there.
	out, changes := apply(t, []Step{{
		Description: "rewrite the sender to the value it already holds",
		Set:         &SetStep{Path: "ISA-6", Value: strings.TrimSpace(before)},
	}}, nil)

	after, _ := ReadString(out, "ISA-6")
	if after != before {
		t.Fatalf("the padding should have been restored: %q -> %q", before, after)
	}
	if len(changes) != 0 {
		t.Errorf("a write that changed nothing reported %d change(s): %+v", len(changes), changes)
	}
}
