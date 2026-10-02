package codeset

import (
	"strings"
	"testing"
)

// The point of this package is that a mapping outlives the person who worked it out, so most of these tests are
// about provenance surviving and about refusing the configurations that lose information silently.

func sexTable() *Table {
	return &Table{
		Name:      "sex-to-lab",
		Describes: "the hospital's sex codes into the numeric codes the lab's interface expects",
		DecidedBy: "Dave, during the 2019 migration",
		DecidedOn: "2019-04-11",
		Source:    "the lab's interface specification, version 3",
		Entries: []Entry{
			{From: "M", To: "1"},
			{From: "F", To: "2"},
			{From: "U", To: "9", Why: "the lab has no 'unknown', and 9 is its 'not stated'"},
		},
	}
}

func TestALookupCarriesTheReasonTheEntryExists(t *testing.T) {
	// This is the whole feature. "U became 9" is a fact; "the lab has no unknown, and 9 is its not stated" is
	// the knowledge that walks out of the building when somebody retires.
	table := sexTable()

	got, entry, mapped := table.Lookup("U")
	if !mapped || got != "9" {
		t.Fatalf("lookup = %q, mapped = %v", got, mapped)
	}
	if !strings.Contains(entry.Why, "not stated") {
		t.Errorf("the reason did not come back with the value: %q", entry.Why)
	}
}

func TestAnUnmappedValueIsKeptRatherThanBlanked(t *testing.T) {
	// An unmapped local code preserved is recoverable; one silently blanked is not, and the receiver cannot tell
	// the difference between "not sent" and "we lost it".
	table := sexTable()

	got, _, mapped := table.Lookup("X")
	if mapped {
		t.Error("an unknown value reported as mapped")
	}
	if got != "X" {
		t.Errorf("unmapped value became %q, want it left alone", got)
	}
}

func TestADefaultAppliesWhenSet(t *testing.T) {
	table := sexTable()
	table.Default = "9"

	got, _, mapped := table.Lookup("X")
	if mapped {
		t.Error("a defaulted value reported as mapped")
	}
	if got != "9" {
		t.Errorf("got %q, want the default", got)
	}
}

func TestStrictReportsWhichTableAndWhatItIsFor(t *testing.T) {
	// "value not in table" tells somebody nothing when a channel uses six tables. The description is what makes
	// the error actionable.
	table := sexTable()
	table.Strict = true

	_, err := table.Strictly("X")
	if err == nil {
		t.Fatal("a strict table accepted an unmapped value")
	}
	if !strings.Contains(err.Error(), "sex-to-lab") {
		t.Errorf("the error does not name the table: %v", err)
	}
	if !strings.Contains(err.Error(), "lab's interface") {
		t.Errorf("the error does not say what the table is for: %v", err)
	}
}

func TestATableWithoutADescriptionIsRefused(t *testing.T) {
	// A table called "sex" does not say whether it maps into the hospital's codes or out of them, and getting
	// that backwards is a silent data error: the receiver accepts the message and the value means something
	// else.
	table := sexTable()
	table.Describes = ""

	errs := table.Validate()
	if len(errs) == 0 {
		t.Fatal("a table with no description was accepted")
	}
	if !strings.Contains(errs[0].Error(), "direction") {
		t.Errorf("the error does not explain why a description matters: %v", errs[0])
	}
}

func TestADuplicateFromValueIsRefused(t *testing.T) {
	// Which row wins depends on ordering nobody intended to specify.
	table := sexTable()
	table.Entries = append(table.Entries, Entry{From: "M", To: "3"})

	errs := table.Validate()
	if len(errs) == 0 {
		t.Fatal("a table mapping the same value twice was accepted")
	}
	if !strings.Contains(errs[0].Error(), "not obvious which") {
		t.Errorf("the error does not explain the problem: %v", errs[0])
	}
}

func TestStrictAndADefaultTogetherIsRefused(t *testing.T) {
	// A contradiction: strict says an unmapped value is an error, a default says it is not. Silently preferring
	// one would make the file lie about what it does.
	table := sexTable()
	table.Strict = true
	table.Default = "9"

	if errs := table.Validate(); len(errs) == 0 {
		t.Fatal("strict and a default together were accepted")
	}
}

func TestAnEmptyFromValueIsRefusedWithTheAlternative(t *testing.T) {
	// It would silently map every absent value, which is a different operation.
	table := sexTable()
	table.Entries = append(table.Entries, Entry{From: "", To: "9"})

	errs := table.Validate()
	if len(errs) == 0 {
		t.Fatal("an entry with no from value was accepted")
	}
	if !strings.Contains(errs[0].Error(), "set step") {
		t.Errorf("the error does not name the right tool for the job: %v", errs[0])
	}
}

func TestValuesReportsWhatCanLeave(t *testing.T) {
	// The two domain-tax features meeting: a mapping knows what it produces, so a contract can assert that
	// nothing else appears.
	table := sexTable()
	table.Default = "9"

	values := table.Values()
	want := []string{"1", "2", "9"}
	if len(values) != len(want) {
		t.Fatalf("values = %v, want %v", values, want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Errorf("values = %v, want %v (sorted)", values, want)
		}
	}
}

func TestDescribeIncludesProvenance(t *testing.T) {
	// A specification that says what a field becomes without saying why invites the receiving team to argue with
	// the mapping.
	text := sexTable().Describe()

	for _, want := range []string{"Dave", "2019-04-11", "interface specification", "not stated"} {
		if !strings.Contains(text, want) {
			t.Errorf("the description omits %q:\n%s", want, text)
		}
	}
}

func TestDescribeIsStableAndSorted(t *testing.T) {
	// This ends up in a document that gets diffed between versions.
	table := sexTable()

	first := table.Describe()
	for i := 0; i < 5; i++ {
		if table.Describe() != first {
			t.Fatal("the description changed between calls")
		}
	}
	// Sorted by the incoming value, so F comes before M.
	if strings.Index(first, "F ") > strings.Index(first, "M ") {
		t.Errorf("entries are not sorted:\n%s", first)
	}
}

func TestDescribeSaysWhatHappensToAnUnmappedValue(t *testing.T) {
	// The three cases behave completely differently and a reader cannot tell which applies from the entry list.
	plain := sexTable()
	if !strings.Contains(plain.Describe(), "passed through unchanged") {
		t.Error("a pass-through table does not say so")
	}

	defaulted := sexTable()
	defaulted.Default = "9"
	if !strings.Contains(defaulted.Describe(), `becomes "9"`) {
		t.Error("a defaulting table does not say so")
	}

	strict := sexTable()
	strict.Strict = true
	if !strings.Contains(strict.Describe(), "is an error") {
		t.Error("a strict table does not say so")
	}
}

func TestTwoTablesWithTheSameNameAreRefused(t *testing.T) {
	// A channel referring to that name would get whichever loaded first, which depends on file order.
	set := &Set{Tables: []Table{*sexTable(), *sexTable()}}

	errs := set.Validate()
	if len(errs) == 0 {
		t.Fatal("two tables with the same name were accepted")
	}
	if !strings.Contains(errs[0].Error(), "loaded first") {
		t.Errorf("the error does not explain the problem: %v", errs[0])
	}
}

func TestATableCanBeFoundByName(t *testing.T) {
	set := &Set{Tables: []Table{*sexTable()}}
	set.Compile()

	if _, ok := set.Table("sex-to-lab"); !ok {
		t.Error("a table could not be found by its name")
	}
	if _, ok := set.Table("nonexistent"); ok {
		t.Error("a table that does not exist was found")
	}
}

func TestANilSetLooksUpNothingRatherThanPanicking(t *testing.T) {
	// Reached whenever a channel has no tables at all, which is the common case.
	var set *Set
	if _, ok := set.Table("anything"); ok {
		t.Error("a nil set returned a table")
	}
	if names := set.Names(); names != nil {
		t.Errorf("names = %v, want nil", names)
	}
}
