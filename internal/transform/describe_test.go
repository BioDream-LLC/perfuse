package transform

import (
	"strings"
	"testing"
)

func TestDescribeEveryStepKind(t *testing.T) {
	// Every action must produce a sentence. A step kind added without a description
	// renders as the "does nothing" fallback, which reads like a broken channel rather
	// than like a missing case in this file.
	cases := []struct {
		name string
		step Step
		want []string
	}{
		{
			name: "set",
			step: Step{Set: &SetStep{Path: "PID-8", Value: "F"}},
			want: []string{"set", "PID-8", `"F"`},
		},
		{
			name: "set to empty",
			step: Step{Set: &SetStep{Path: "PID-8"}},
			want: []string{"empty value"},
		},
		{
			name: "copy",
			step: Step{Copy: &CopyStep{From: "PID-3", To: "PID-4"}},
			want: []string{"copy", "PID-3", "PID-4"},
		},
		{
			name: "copy with default",
			step: Step{Copy: &CopyStep{From: "PID-3", To: "PID-4", Default: "UNKNOWN"}},
			want: []string{"when it is empty", `"UNKNOWN"`},
		},
		{
			name: "clear",
			step: Step{Clear: &ClearStep{Path: "PID-19"}},
			want: []string{"clear", "PID-19"},
		},
		{
			name: "remove",
			step: Step{Remove: &RemoveStep{Path: "ZZZ-1"}},
			want: []string{"remove", "entirely"},
		},
		{
			name: "replace one",
			step: Step{Replace: &ReplaceStep{Path: "PID-3", Pattern: "^0+", With: ""}},
			want: []string{"the first match", "^0+"},
		},
		{
			name: "replace all",
			step: Step{Replace: &ReplaceStep{Path: "PID-3", Pattern: "-", With: "", All: true}},
			want: []string{"every match"},
		},
		{
			name: "pad left",
			step: Step{Pad: &PadStep{Path: "PID-3", Width: 10, With: "0"}},
			want: []string{"the left", "10 characters", "leaving anything longer alone"},
		},
		{
			name: "pad right truncating",
			step: Step{Pad: &PadStep{Path: "PID-3", Width: 4, Right: true, Truncate: true}},
			want: []string{"the right", "cutting anything longer"},
		},
		{
			name: "date",
			step: Step{Date: &DateStep{Path: "PID-7", From: "20060102", To: "2006-01-02"}},
			want: []string{"reformat the date", "PID-7"},
		},
		{
			name: "trim",
			step: Step{Trim: &TrimStep{Path: "PID-5"}},
			want: []string{"trim whitespace"},
		},
		{
			name: "case",
			step: Step{Case: &CaseStep{Path: "PID-5", To: "upper"}},
			want: []string{"upper case"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.step.Describe()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("Describe() = %q, missing %q", got, want)
				}
			}
			if strings.Contains(got, "does nothing") {
				t.Errorf("Describe() fell through to the fallback: %q", got)
			}
		})
	}
}

func TestDescribeKeepsTheAuthorsOwnWords(t *testing.T) {
	// Somebody who took the trouble to explain why a step exists knows something the
	// fields cannot express. Both are shown: the description says why, the action says
	// what, and checking one against the other is the point.
	step := Step{
		Description: "strip the leading zeroes Epic adds",
		Replace:     &ReplaceStep{Path: "PID-3.1", Pattern: "^0+", With: ""},
	}

	got := step.Describe()
	if !strings.Contains(got, "strip the leading zeroes Epic adds") {
		t.Errorf("the author's description was dropped: %q", got)
	}
	if !strings.Contains(got, "^0+") {
		t.Errorf("the action was dropped: %q", got)
	}
}

func TestDescribeReportsItsCondition(t *testing.T) {
	step := Step{
		When: "PID-3 exists",
		Set:  &SetStep{Path: "PID-8", Value: "F"},
	}
	got := step.Describe()
	if !strings.Contains(got, "when PID-3 exists") {
		t.Errorf("the condition was dropped: %q", got)
	}
}

func TestDescribeAMapTableIsStable(t *testing.T) {
	// A Go map ranges randomly. Without sorting, the same channel would describe itself
	// differently on each request and anybody diffing two descriptions would see changes
	// that are not there.
	step := Step{Map: &MapStep{
		Path:  "PID-8",
		Table: map[string]string{"1": "M", "2": "F", "3": "O", "4": "U", "9": "A"},
	}}

	first := step.Describe()
	for i := 0; i < 50; i++ {
		if got := step.Describe(); got != first {
			t.Fatalf("description changed between calls:\n%q\n%q", first, got)
		}
	}

	if !strings.Contains(first, "5 values") {
		t.Errorf("the table size was not reported: %q", first)
	}
	if !strings.Contains(first, "and 2 more") {
		t.Errorf("the tail was not summarised: %q", first)
	}
	// Sorted, so the first three shown are 1, 2 and 3.
	if !strings.Contains(first, "1 to M") || !strings.Contains(first, "3 to O") {
		t.Errorf("examples were not in sorted order: %q", first)
	}
}

func TestDescribeAMapSaysWhatHappensToAnUnknownValue(t *testing.T) {
	// The three cases behave differently at run time, and which one is in force is
	// exactly what somebody reviewing a mapping needs to know.
	base := MapStep{Path: "PID-8", Table: map[string]string{"1": "M"}}

	loose := Step{Map: &base}
	if !strings.Contains(loose.Describe(), "left as it is") {
		t.Errorf("loose: %q", loose.Describe())
	}

	withDefault := base
	withDefault.Default = "U"
	if got := (Step{Map: &withDefault}).Describe(); !strings.Contains(got, `becomes "U"`) {
		t.Errorf("default: %q", got)
	}

	strict := base
	strict.Strict = true
	if got := (Step{Map: &strict}).Describe(); !strings.Contains(got, "is an error") {
		t.Errorf("strict: %q", got)
	}
}

func TestDescribeASingularTable(t *testing.T) {
	step := Step{Map: &MapStep{Path: "PID-8", Table: map[string]string{"1": "M"}}}
	if got := step.Describe(); !strings.Contains(got, "1 value (") {
		t.Errorf("a one-entry table was described as %q", got)
	}
}

func TestDescribeAnEmptyStepDoesNotReturnNothing(t *testing.T) {
	// Unreachable through a validated pipeline, but a blank line in a list of steps looks
	// like a rendering fault and sends somebody looking in the wrong place.
	if got := (Step{}).Describe(); got == "" {
		t.Fatal("an empty step described itself as the empty string")
	}
}
