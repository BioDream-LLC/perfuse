package delimited

import (
	"strings"
	"testing"
)

// sample is a small file with a header, used throughout. Two rows so the multi-record case is real rather than hypothetical.
const sample = "PatientID,Surname,Ward\nP001,Okonkwo,ICU\nP002,Nakamura,\n"

func parseSample(t *testing.T) Message {
	t.Helper()
	records, err := Parse([]byte(sample), Settings{HasHeader: true}.Resolved())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}

	return Message(records)
}

func TestPathsParseInBothForms(t *testing.T) {
	byName, err := ParsePath("PatientID")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if byName.Column != "PatientID" || byName.Position != 0 {
		t.Errorf("by name resolved to %+v", byName)
	}

	byPosition, err := ParsePath("#3")
	if err != nil {
		t.Fatalf("by position: %v", err)
	}
	if byPosition.Position != 3 || byPosition.Column != "" {
		t.Errorf("by position resolved to %+v", byPosition)
	}
}

// TestABareNumberIsRefusedRatherThanGuessed is the reason the hash exists.
//
// A file exported with years as column headers has a column legitimately named 2024. Treating a bare number as a position
// would read column 2024 - or, worse, some other column entirely - without complaining.
func TestABareNumberIsRefusedRatherThanGuessed(t *testing.T) {
	_, err := ParsePath("2024")
	if err == nil {
		t.Fatal("a bare number was accepted, so a column named 2024 and the 2024th column are the same path")
	}
	if !strings.Contains(err.Error(), "#2024") {
		t.Errorf("the error should name the positional form as the fix, got: %v", err)
	}
}

func TestPositionsStartAtOne(t *testing.T) {
	if _, err := ParsePath("#0"); err == nil {
		t.Fatal("#0 was accepted, which means something different to this code than to whoever wrote it")
	}
}

// TestAnAbsentColumnIsNotABlankOne is the distinction exists depends on.
func TestAnAbsentColumnIsNotABlankOne(t *testing.T) {
	m := parseSample(t)

	// Ward is blank in the second row: present, no content. Two values.
	if got := Read(m, Path{Column: "Ward"}); len(got) != 2 {
		t.Errorf("Ward read %d value(s), want 2 - a blank field is present", len(got))
	}

	// Diagnosis is not a column at all. No values.
	if got := Read(m, Path{Column: "Diagnosis"}); len(got) != 0 {
		t.Errorf("Diagnosis read %d value(s), want 0 - it is not in the header", len(got))
	}
}

func TestReadingIsCaseInsensitive(t *testing.T) {
	m := parseSample(t)
	got := Read(m, Path{Column: "patientid"})
	if len(got) != 2 || got[0] != "P001" {
		t.Errorf("case-insensitive read got %v", got)
	}
}

// TestReadOneReportsManyAsAbsent keeps a step from reading one row and writing another.
func TestReadOneReportsManyAsAbsent(t *testing.T) {
	m := parseSample(t)

	if _, ok := ReadOne(m, Path{Column: "PatientID"}); ok {
		t.Error("ReadOne succeeded across two records, so a step would read one row's value")
	}

	if v, ok := ReadOne(m[:1], Path{Column: "PatientID"}); !ok || v != "P001" {
		t.Errorf("ReadOne on a single record got %q, %v", v, ok)
	}
}

// TestWritingAcrossRecordsIsRefused is the destructive case.
//
// Four hundred rows, one value, and every patient identifier the same afterwards. The refusal names the count and the fix.
func TestWritingAcrossRecordsIsRefused(t *testing.T) {
	m := parseSample(t)

	_, err := Set(m, Path{Column: "Ward"}, "HDU")
	if err == nil {
		t.Fatal("a write to two records succeeded, overwriting two rows with one value")
	}
	if !strings.Contains(err.Error(), "2 records") {
		t.Errorf("the refusal should name how many rows would be overwritten, got: %v", err)
	}
	if !strings.Contains(err.Error(), "split") {
		t.Errorf("the refusal should name the fix, got: %v", err)
	}
}

func TestWritingToOneRecordWorksByNameAndPosition(t *testing.T) {
	m := parseSample(t)

	out, err := Set(m[:1], Path{Column: "Ward"}, "HDU")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if v, _ := ReadOne(out, Path{Column: "Ward"}); v != "HDU" {
		t.Errorf("by name wrote %q", v)
	}

	out, err = Set(m[:1], Path{Position: 2}, "Adeyemi")
	if err != nil {
		t.Fatalf("by position: %v", err)
	}
	if v, _ := ReadOne(out, Path{Position: 2}); v != "Adeyemi" {
		t.Errorf("by position wrote %q", v)
	}
}

// TestAFailedWriteLeavesTheOriginalAlone is why clone exists.
func TestAFailedWriteLeavesTheOriginalAlone(t *testing.T) {
	m := parseSample(t)

	if _, err := Set(m[:1], Path{Column: "Diagnosis"}, "anything"); err == nil {
		t.Fatal("writing an unknown column succeeded")
	}

	if v, _ := ReadOne(m[:1], Path{Column: "Surname"}); v != "Okonkwo" {
		t.Errorf("the original was modified by a failed write: Surname is %q", v)
	}
}

// TestASuccessfulWriteDoesNotModifyTheInput is the other half of clone, and the one easier to get wrong.
func TestASuccessfulWriteDoesNotModifyTheInput(t *testing.T) {
	m := parseSample(t)

	if _, err := Set(m[:1], Path{Column: "Ward"}, "HDU"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if v, _ := ReadOne(m[:1], Path{Column: "Ward"}); v != "ICU" {
		t.Errorf("the input was mutated: Ward is now %q, want ICU", v)
	}
}

func TestAnUnknownColumnNamesTheColumnsThatExist(t *testing.T) {
	m := parseSample(t)

	_, err := Set(m[:1], Path{Column: "Diagnosis"}, "x")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"PatientID", "Surname", "Ward"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should list the real columns, %q missing: %v", want, err)
		}
	}
}

// TestAHeaderlessFileSaysToUsePositions gives the specific fix rather than "no such column".
func TestAHeaderlessFileSaysToUsePositions(t *testing.T) {
	records, err := Parse([]byte("P001,Okonkwo\n"), Settings{}.Resolved())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	_, err = Set(Message(records), Path{Column: "PatientID"}, "P999")
	if err == nil {
		t.Fatal("a named write to a headerless file succeeded")
	}
	if !strings.Contains(err.Error(), "#n") {
		t.Errorf("the error should point at positional addressing, got: %v", err)
	}
}

// TestCanonicalMakesTwoSpellingsOfAFieldEqual is what stops a copy to itself being accepted.
func TestCanonicalMakesTwoSpellingsOfAFieldEqual(t *testing.T) {
	lower, err := ParsePath("patientid")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := ParsePath("PatientID")
	if err != nil {
		t.Fatal(err)
	}

	if lower.Canonical() != upper.Canonical() {
		t.Errorf("%q and %q have different canonical forms, so copying one to the other looks like real work", lower, upper)
	}
}

func TestFilteringOnAColumn(t *testing.T) {
	m := parseSample(t)

	for _, tc := range []struct {
		src  string
		want bool
	}{
		{`Ward == "ICU"`, true},
		{`Ward == "HDU"`, false},
		{`PatientID =~ "^P0"`, true},
		{`Surname in ["Nakamura", "Adeyemi"]`, true},
		{`Diagnosis exists`, false},
		{`Ward exists`, true},
		{`Ward == "ICU" and PatientID == "P001"`, true},
		{`not Ward == "HDU"`, true},
		{`#3 == "ICU"`, true},
	} {
		f, err := ParseFilter(tc.src)
		if err != nil {
			t.Errorf("%s: parse: %v", tc.src, err)

			continue
		}
		got, err := f.Eval(m)
		if err != nil {
			t.Errorf("%s: eval: %v", tc.src, err)

			continue
		}
		if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.src, got, tc.want)
		}
	}
}

// TestAFilterAsksAboutAnyRow is the multi-record read the design allows on purpose.
//
// Reading across rows is allowed where writing is refused, because asking whether any row mentions a ward destroys nothing.
func TestAFilterAsksAboutAnyRow(t *testing.T) {
	m := parseSample(t)

	f, err := ParseFilter(`Surname == "Nakamura"`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Eval(m)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("a filter did not match a value in the second record, so only the first row is being read")
	}
}

// TestEmptyDistinguishesBlankFromAbsent is the same distinction exists depends on, from the filter's side.
func TestEmptyDistinguishesBlankFromAbsent(t *testing.T) {
	second := parseSample(t)[1:]

	f, err := ParseFilter(`Ward empty`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Eval(second)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("Ward is blank in the second row and empty did not say so")
	}
}
