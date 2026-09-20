package transform

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/hl7xml"
)

const adt = "MSH|^~\\&|SENDAPP|SITEA|RECV|RFAC|20260818120000||ADT^A01^ADT_A01|CTRL1|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN9^^^SITEA^MR~999^^^SSA^SS||  Doe  ^Jane^Q||19800101|f|||4 Elm Rd^^Vestavia^AL^35216\r" +
	"PV1|1|I|ICU^7^01^SITEA||||1234^Smith^Sam|||MED\r" +
	"OBX|1|NM|718-7^Hemoglobin^LN||13.5|g/dL|12.0-16.0|N|||F\r" +
	"OBX|2|NM|6690-2^Leukocytes^LN||14.2|10*3/uL|4.0-11.0|H|||F\r"

func applySteps(t *testing.T, steps []Step) (string, []Change) {
	t.Helper()

	root, err := hl7xml.FromRaw([]byte(adt))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(steps)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	changes, err := p.Apply(root)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	out, err := hl7xml.ToER7(root, hl7xml.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return string(out), changes
}

func TestSteps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		steps  []Step
		expect string
		absent string
	}{
		{
			name:   "set a field",
			steps:  []Step{{Set: &SetStep{Path: "PID-4.1", Value: "ALT"}}},
			expect: "|ALT|",
		},
		{
			name:   "set creates a field that was absent",
			steps:  []Step{{Set: &SetStep{Path: "PID-19.1", Value: "SSN"}}},
			expect: "SSN",
		},
		{
			name:   "copy between fields",
			steps:  []Step{{Copy: &CopyStep{From: "PV1-10.1", To: "PID-4.1"}}},
			expect: "|MED|",
		},
		{
			name:   "copy uses the default when the source is empty",
			steps:  []Step{{Copy: &CopyStep{From: "PID-6.1", To: "PID-4.1", Default: "NONE"}}},
			expect: "|NONE|",
		},
		{
			name:   "clear a field",
			steps:  []Step{{Clear: &ClearStep{Path: "PID-7.1"}}},
			absent: "19800101",
		},
		{
			name:   "remove a segment",
			steps:  []Step{{Remove: &RemoveStep{Path: "EVN"}}},
			absent: "EVN|",
		},
		{
			name: "map a code",
			steps: []Step{{Map: &MapStep{
				Path:  "PV1-10.1",
				Table: map[string]string{"MED": "Medicine", "SUR": "Surgery"},
			}}},
			expect: "Medicine",
		},
		{
			name: "an unmapped value is kept, not blanked",
			steps: []Step{{Map: &MapStep{
				Path:  "PV1-2.1",
				Table: map[string]string{"O": "Outpatient"},
			}}},
			expect: "|I|",
		},
		{
			name: "an unmapped value can take a default",
			steps: []Step{{Map: &MapStep{
				Path:    "PV1-2.1",
				Table:   map[string]string{"O": "Outpatient"},
				Default: "UNKNOWN",
			}}},
			expect: "UNKNOWN",
		},
		{
			name:   "pad an identifier",
			steps:  []Step{{Pad: &PadStep{Path: "PID-3(1).1", Width: 10}}},
			expect: "000000MRN9",
		},
		{
			name:   "padding does not invent a value for an empty field",
			steps:  []Step{{Pad: &PadStep{Path: "PID-6.1", Width: 8}}},
			absent: "00000000",
		},
		{
			name:   "reformat a date",
			steps:  []Step{{Date: &DateStep{Path: "PID-7.1", From: "yyyyMMdd", To: "MM/dd/yyyy"}}},
			expect: "01/01/1980",
		},
		{
			name:   "trim whitespace",
			steps:  []Step{{Trim: &TrimStep{Path: "PID-5.1"}}},
			expect: "|Doe^Jane^Q|",
		},
		{
			name:   "upper case a value",
			steps:  []Step{{Case: &CaseStep{Path: "PID-8.1", To: "upper"}}},
			expect: "|F|",
		},
		{
			name: "regular expression replacement",
			steps: []Step{{Replace: &ReplaceStep{
				Path:    "PID-7.1",
				Pattern: `(\d{4})(\d{2})(\d{2})`,
				With:    "$1-$2-$3",
				All:     true,
			}}},
			expect: "1980-01-01",
		},
		{
			name: "a condition that holds",
			steps: []Step{{
				When: `MSH-9.2 == "A01"`,
				Set:  &SetStep{Path: "PID-4.1", Value: "ADMIT"},
			}},
			expect: "ADMIT",
		},
		{
			name: "a condition that does not hold",
			steps: []Step{{
				When: `MSH-9.2 == "A28"`,
				Set:  &SetStep{Path: "PID-4.1", Value: "SHOULD-NOT-APPEAR"},
			}},
			absent: "SHOULD-NOT-APPEAR",
		},
		{
			name: "an exists condition",
			steps: []Step{{
				When: `PID-3.1 exists`,
				Set:  &SetStep{Path: "PID-4.1", Value: "HASMRN"},
			}},
			expect: "HASMRN",
		},
		{
			name: "an empty condition",
			steps: []Step{{
				When: `PID-6.1 empty`,
				Set:  &SetStep{Path: "PID-4.1", Value: "NOMOTHER"},
			}},
			expect: "NOMOTHER",
		},
		{
			name: "steps run in order and see each other",
			steps: []Step{
				{Set: &SetStep{Path: "PID-4.1", Value: "first"}},
				{Copy: &CopyStep{From: "PID-4.1", To: "PID-19.1"}},
				{Set: &SetStep{Path: "PID-4.1", Value: "second"}},
			},
			expect: "|second|",
		},
		{
			name:   "a path without a repetition changes every repetition",
			steps:  []Step{{Set: &SetStep{Path: "PID-3.4", Value: "NEWFAC"}}},
			expect: "MRN9^^^NEWFAC^MR~999^^^NEWFAC^SS",
		},
		{
			name:   "a segment occurrence targets just one",
			steps:  []Step{{Set: &SetStep{Path: "OBX(2)-8.1", Value: "HH"}}},
			expect: "4.0-11.0|HH",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := applySteps(t, tc.steps)
			if tc.expect != "" && !strings.Contains(out, tc.expect) {
				t.Errorf("output missing %q:\n%s", tc.expect, out)
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("output should not contain %q:\n%s", tc.absent, out)
			}
		})
	}
}

// TestChangesAreReported matters because the point of the declarative layer over
// scripting is that you can see what it did.
func TestChangesAreReported(t *testing.T) {
	_, changes := applySteps(t, []Step{
		{Description: "anonymise the name", Set: &SetStep{Path: "PID-5.1", Value: "REDACTED"}},
		{Set: &SetStep{Path: "PID-4.1", Value: "X"}},
	})

	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2: %+v", len(changes), changes)
	}
	if changes[0].Step != "anonymise the name" {
		t.Errorf("a described step should be reported by its description, got %q", changes[0].Step)
	}
	if changes[0].From != "  Doe  " || changes[0].To != "REDACTED" {
		t.Errorf("change should record both values, got %+v", changes[0])
	}
	if changes[1].Step != "set" {
		t.Errorf("an undescribed step falls back to its action name, got %q", changes[1].Step)
	}
}

// TestNoChangeIsNotReported keeps the diff honest: a step that made no difference
// should not appear as though it did.
func TestNoChangeIsNotReported(t *testing.T) {
	_, changes := applySteps(t, []Step{
		{Set: &SetStep{Path: "PV1-2.1", Value: "I"}}, // already I
	})
	if len(changes) != 0 {
		t.Errorf("a step that changed nothing should not be reported: %+v", changes)
	}
}

// TestUnmappedStrictFails checks that a field the receiver validates can be made
// to stop the message rather than pass through unmapped.
func TestUnmappedStrictFails(t *testing.T) {
	root, _ := hl7xml.FromRaw([]byte(adt))
	p, err := Compile([]Step{{Map: &MapStep{
		Path:   "PV1-2.1",
		Table:  map[string]string{"O": "Outpatient"},
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(root); err == nil {
		t.Error("a strict map should fail on an unmapped value")
	}
}

// TestDateFailureIsLoudByDefault pins the choice that a timestamp which did not
// convert stops the message, because one that silently passed through is accepted
// downstream and then misread.
func TestDateFailureIsLoudByDefault(t *testing.T) {
	root, _ := hl7xml.FromRaw([]byte(adt))
	p, err := Compile([]Step{{Date: &DateStep{Path: "PID-5.1", From: "yyyyMMdd", To: "MM/dd/yyyy"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(root); err == nil {
		t.Error("an unparseable date should fail by default")
	}

	for _, mode := range []string{"keep", "clear"} {
		root, _ := hl7xml.FromRaw([]byte(adt))
		p, err := Compile([]Step{{Date: &DateStep{
			Path: "PID-5.1", From: "yyyyMMdd", To: "MM/dd/yyyy", OnError: mode,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		changes, err := p.Apply(root)
		if err != nil {
			t.Errorf("on_error=%s should not fail: %v", mode, err)
		}
		if len(changes) == 0 || changes[0].Note == "" {
			t.Errorf("on_error=%s should record why: %+v", mode, changes)
		}
	}
}

// TestCompileRejectsBadSteps checks that everything which can be caught at load
// time is, rather than surfacing when a patient is admitted.
func TestCompileRejectsBadSteps(t *testing.T) {
	for _, tc := range []struct {
		name string
		step Step
		says string
	}{
		{"no action", Step{Description: "empty"}, "no action"},
		{
			"two actions",
			Step{Set: &SetStep{Path: "PID-4.1", Value: "a"}, Clear: &ClearStep{Path: "PID-5.1"}},
			"exactly one thing",
		},
		{"missing path", Step{Set: &SetStep{Value: "a"}}, "required"},
		{"bad path", Step{Set: &SetStep{Path: "NOTASEGMENT-1", Value: "a"}}, "segment name"},
		{"bad field number", Step{Set: &SetStep{Path: "PID-x", Value: "a"}}, "field number"},
		{"too deep", Step{Set: &SetStep{Path: "PID-5.1.2.3", Value: "a"}}, "below a subcomponent"},
		// A map with neither an inline table nor a shared reference. The message changed when shared tables
		// arrived, and it should: "empty" was accurate when a table was the only option.
		{"map with no table at all",
			Step{Map: &MapStep{Path: "PID-8.1", Table: map[string]string{}}}, "would do nothing"},
		{
			"strict map with a default",
			Step{Map: &MapStep{Path: "PID-8.1", Table: map[string]string{"F": "Female"}, Strict: true, Default: "X"}},
			"could never apply",
		},
		{"bad regex", Step{Replace: &ReplaceStep{Path: "PID-8.1", Pattern: "(unclosed", With: "x"}}, "not a valid expression"},
		{"zero pad width", Step{Pad: &PadStep{Path: "PID-3.1", Width: 0}}, "must be positive"},
		{"absurd pad width", Step{Pad: &PadStep{Path: "PID-3.1", Width: 9999}}, "implausible"},
		{"multi-character pad", Step{Pad: &PadStep{Path: "PID-3.1", Width: 5, With: "ab"}}, "single character"},
		{"date without patterns", Step{Date: &DateStep{Path: "PID-7.1", From: "yyyyMMdd"}}, "both from and to"},
		{"unsupported date letter", Step{Date: &DateStep{Path: "PID-7.1", From: "yyyy-ww", To: "yyyy"}}, "not supported"},
		{"bad on_error", Step{Date: &DateStep{Path: "PID-7.1", From: "yyyyMMdd", To: "yyyy", OnError: "shrug"}}, "expected fail, keep or clear"},
		{"bad case", Step{Case: &CaseStep{Path: "PID-8.1", To: "sideways"}}, "expected upper or lower"},
		{"bad condition", Step{When: "PID-8 ~~ x", Set: &SetStep{Path: "PID-4.1", Value: "a"}}, "expected one of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile([]Step{tc.step})
			if err == nil {
				t.Fatal("expected a compile error")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error should mention %q, got: %v", tc.says, err)
			}
		})
	}
}

func TestPathParsing(t *testing.T) {
	for _, tc := range []struct {
		in      string
		segment string
		occ     int
		field   int
		rep     int
		comp    int
		sub     int
	}{
		{"PID-5.1", "PID", 0, 5, 0, 1, 0},
		{"PID-3(2).1", "PID", 0, 3, 2, 1, 0},
		{"OBX(3)-5", "OBX", 3, 5, 0, 0, 0},
		{"MSH-9.2", "MSH", 0, 9, 0, 2, 0},
		{"PID-3.4.1", "PID", 0, 3, 0, 4, 1},
		{"EVN", "EVN", 0, 0, 0, 0, 0},
	} {
		t.Run(tc.in, func(t *testing.T) {
			p, err := ParsePath(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Segment != tc.segment || p.Occurrence != tc.occ || p.Field != tc.field ||
				p.Repetition != tc.rep || p.Component != tc.comp || p.Subcomponent != tc.sub {
				t.Errorf("got %+v", p)
			}
			if p.String() != tc.in {
				t.Errorf("String() = %q, want %q", p.String(), tc.in)
			}
		})
	}
}

// TestConditionMatchesAnyRepetition keeps the condition semantics aligned with
// the filter language, where a path naming no repetition tests all of them.
func TestConditionMatchesAnyRepetition(t *testing.T) {
	out, _ := applySteps(t, []Step{{
		When: `PID-3.5 == "SS"`, // true only of the second repetition
		Set:  &SetStep{Path: "PID-4.1", Value: "HASSSN"},
	}})
	if !strings.Contains(out, "HASSSN") {
		t.Errorf("a condition should hold when any repetition matches:\n%s", out)
	}

	out, _ = applySteps(t, []Step{{
		When: `PID-3.5 != "SS"`, // false, because one repetition is SS
		Set:  &SetStep{Path: "PID-4.1", Value: "NOSSN"},
	}})
	if strings.Contains(out, "NOSSN") {
		t.Errorf("!= should mean no repetition matches:\n%s", out)
	}
}
