package fhirserver

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/codeset"
)

// fakeTables is a TableSource for tests.
type fakeTables struct {
	tables map[string]*codeset.Table
}

func (f *fakeTables) Tables() map[string]*codeset.Table { return f.tables }

// sexTable is a table of the shape a real one has: a few entries, one of them with a recorded reason.
func sexTable() *codeset.Table {
	t := &codeset.Table{
		Name:      "sex",
		Describes: "HL7 v2 PID-8 administrative sex into FHIR gender",
		DecidedBy: "the 2019 migration",
		DecidedOn: "2019-04-02",
		Source:    "the interface specification, section 4",
		Entries: []codeset.Entry{
			{From: "M", To: "male"},
			{From: "F", To: "female"},
			{From: "O", To: "other"},
			{From: "A", To: "other", Why: "the lab sends A for ambiguous", Since: "2021-06-01"},
		},
		Default: "unknown",
	}
	t.Compile()

	return t
}

// strictTable refuses anything it does not know.
func strictTable() *codeset.Table {
	t := &codeset.Table{
		Name:      "department",
		Describes: "sending department codes into ours",
		Entries:   []codeset.Entry{{From: "ICU", To: "CRIT"}},
		Strict:    true,
	}
	t.Compile()

	return t
}

// The projection must carry the fields that would otherwise be unrecoverable.
func TestTableProjectsAsAConceptMap(t *testing.T) {
	cm := TableAsConceptMap(sexTable())

	if cm.ResourceTypeName() != "ConceptMap" {
		t.Errorf("resource type is %q", cm.ResourceTypeName())
	}
	if cm.ResourceID() != "sex" {
		t.Errorf("id is %q, want sex", cm.ResourceID())
	}
	if cm.Description == "" {
		t.Error("the table's description was dropped, so the map does not say what it is for")
	}

	// Provenance. These are the fields that settle an argument about a mapping years later, and nothing else records them.
	if cm.Publisher != "the 2019 migration" {
		t.Errorf("decided_by was not carried across: publisher = %q", cm.Publisher)
	}
	if cm.Date != "2019-04-02" {
		t.Errorf("decided_on was not carried across: date = %q", cm.Date)
	}
	if !strings.Contains(cm.Purpose, "interface specification") {
		t.Errorf("source was not carried across: purpose = %q", cm.Purpose)
	}

	if len(cm.Group) != 1 {
		t.Fatalf("got %d group(s), want 1", len(cm.Group))
	}
	group := cm.Group[0]

	if len(group.Element) != 4 {
		t.Fatalf("got %d element(s), want 4", len(group.Element))
	}

	// Order preserved, because that is the order somebody wrote and reviewed.
	if group.Element[0].Code != "M" {
		t.Errorf("first element is %q, want M: entry order was not preserved", group.Element[0].Code)
	}

	// The per-entry reason, which is the least recoverable thing in the table.
	var found string
	for _, e := range group.Element {
		if e.Code == "A" && len(e.Target) > 0 {
			found = e.Target[0].Comment
		}
	}
	if !strings.Contains(found, "ambiguous") {
		t.Errorf("the reason recorded against A was dropped: comment = %q", found)
	}
	if !strings.Contains(found, "2021-06-01") {
		t.Errorf("the date A was added was dropped: comment = %q", found)
	}

	// A default means unmapped codes are translated to it, and a client needs to know that.
	if group.Unmapped == nil || group.Unmapped.Mode != "fixed" || group.Unmapped.Code != "unknown" {
		t.Errorf("the table's default is not described in unmapped: %+v", group.Unmapped)
	}
}

// A strict table must be distinguishable from one that passes codes through.
//
// These are opposite behaviours - refuse the message, or send it on unchanged - and a client that cannot tell them apart does not know
// whether an unrecognised code is safe to send.
func TestUnmappedDescribesWhatActuallyHappens(t *testing.T) {
	strictMap := TableAsConceptMap(strictTable())
	if u := strictMap.Group[0].Unmapped; u != nil {
		t.Errorf("a strict table declared an unmapped rule, implying unknown codes are handled: %+v", u)
	}

	passThrough := &codeset.Table{Name: "pass", Describes: "x", Entries: []codeset.Entry{{From: "a", To: "b"}}}
	passThrough.Compile()

	if u := TableAsConceptMap(passThrough).Group[0].Unmapped; u == nil || u.Mode != "provided" {
		t.Errorf("a pass-through table does not say so: %+v", u)
	}
}

// Translation must come from the table itself, and must say what happens in every case.
func TestTranslate(t *testing.T) {
	sex := sexTable()

	t.Run("a mapped code", func(t *testing.T) {
		res := Translate(sex, "F")
		if !res.Matched || res.Code != "female" {
			t.Fatalf("F did not translate to female: %+v", res)
		}
	})

	t.Run("the recorded reason is in the message", func(t *testing.T) {
		res := Translate(sex, "A")
		if !strings.Contains(res.Message, "ambiguous") {
			t.Errorf("the reason for this entry was not reported: %q", res.Message)
		}
	})

	t.Run("an unmapped code falls to the default and says so", func(t *testing.T) {
		res := Translate(sex, "Z")
		if !res.Matched || res.Code != "unknown" {
			t.Fatalf("Z did not fall to the default: %+v", res)
		}
		if !strings.Contains(res.Message, "default") {
			t.Errorf("the message does not explain that a default was used: %q", res.Message)
		}
	})

	t.Run("a strict table refuses rather than defaulting", func(t *testing.T) {
		res := Translate(strictTable(), "WARD9")
		if res.Matched {
			t.Fatalf("a strict table translated a code it does not hold: %+v", res)
		}
		if !res.Refused {
			t.Error("the refusal was not marked, so a caller cannot tell this from an ordinary miss")
		}
		if !strings.Contains(res.Message, "refused") {
			t.Errorf("the message does not say the message would be refused: %q", res.Message)
		}
	})
}

// Parameters this operation does not implement must be refused, not ignored.
func TestTranslateRefusesWhatItCannotDo(t *testing.T) {
	tests := []struct {
		name   string
		params map[string][]string
		expect string
	}{
		{"reverse would give the mapping backwards", map[string][]string{"code": {"M"}, "url": {"urn:perfuse:codeset:sex"}, "reverse": {"true"}}, "refused rather than ignored"},
		{"target system is not honoured", map[string][]string{"code": {"M"}, "url": {"urn:perfuse:codeset:sex"}, "target": {"http://x"}}, "refused rather than ignored"},
		{"an invented parameter", map[string][]string{"code": {"M"}, "url": {"urn:perfuse:codeset:sex"}, "colour": {"blue"}}, "not a parameter"},
		{"no code at all", map[string][]string{"url": {"urn:perfuse:codeset:sex"}}, "code is required"},
		{"no map named", map[string][]string{"code": {"M"}}, "url is required"},
		{"two codes", map[string][]string{"code": {"M", "F"}, "url": {"urn:perfuse:codeset:sex"}}, "more than once"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTranslate("", tc.params); err == nil {
				t.Fatal("this was accepted, and a translation answered as though the parameter had not been sent " +
					"is a confident wrong answer")
			} else if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("the refusal does not mention %q: %v", tc.expect, err)
			}
		})
	}
}

// A table can be found by name, by projected id, or by canonical URL.
func TestFindConceptMapAcceptsEitherIdentifier(t *testing.T) {
	src := &fakeTables{tables: map[string]*codeset.Table{"sex": sexTable()}}

	for _, want := range []string{"sex", "urn:perfuse:codeset:sex"} {
		if _, err := FindConceptMap(src, want); err != nil {
			t.Errorf("%q was not found: %v", want, err)
		}
	}

	// And an unknown one must say what does exist, rather than only that this does not.
	_, err := FindConceptMap(src, "nonsense")
	if err == nil {
		t.Fatal("an unknown table was found")
	}
	if !strings.Contains(err.Error(), "sex") {
		t.Errorf("the error does not list the tables that do exist: %v", err)
	}
}

// A table name that is not a legal FHIR id must be made into one.
func TestConceptMapIDIsALegalFHIRId(t *testing.T) {
	cases := map[string]string{
		"sex":              "sex",
		"lab_result_codes": "lab-result-codes",
		"a/b c":            "a-b-c",
		"__":               "table",
	}

	for in, want := range cases {
		if got := ConceptMapID(in); got != want {
			t.Errorf("ConceptMapID(%q) = %q, want %q", in, got, want)
		}
	}

	// Length is bounded, because a FHIR id may not exceed 64 characters and an unreadable resource is worse than a truncated name.
	if got := ConceptMapID(strings.Repeat("a", 100)); len(got) != 64 {
		t.Errorf("a long table name produced a %d character id, which FHIR would reject", len(got))
	}
}

// The projected list must be sorted, since a client may cache and compare it.
func TestConceptMapsAreSorted(t *testing.T) {
	src := &fakeTables{tables: map[string]*codeset.Table{
		"sex":        sexTable(),
		"department": strictTable(),
	}}

	got := ConceptMaps(src)
	if len(got) != 2 {
		t.Fatalf("got %d maps, want 2", len(got))
	}
	if got[0].ResourceID() > got[1].ResourceID() {
		t.Errorf("not sorted: %q before %q", got[0].ResourceID(), got[1].ResourceID())
	}

	// And no tables at all is an empty list, not a panic.
	if out := ConceptMaps(&fakeTables{tables: map[string]*codeset.Table{}}); len(out) != 0 {
		t.Errorf("an installation with no tables produced %d map(s)", len(out))
	}
	if out := ConceptMaps(nil); out != nil {
		t.Errorf("a nil source produced %v", out)
	}
}
