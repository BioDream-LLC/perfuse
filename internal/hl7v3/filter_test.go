package hl7v3

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

const filterSample = `<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.72" extension="MSG00001"/>
  <interactionId extension="PRPA_IN201306UV02"/>
  <controlActProcess><subject><registrationEvent><subject1>
    <patient classCode="PAT">
      <id root="2.16.840.1.113883.3.72.5.9.1" extension="PIX1234"/>
      <id root="2.16.840.1.113883.4.1" extension="999887777"/>
      <patientPerson>
        <name><given>Rosalind</given><family>Okonkwo-Hale</family></name>
        <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/>
        <birthTime value="19551014"/>
      </patientPerson>
    </patient>
  </subject1></registrationEvent></subject></controlActProcess>
</PRPA_IN201306UV02>`

func filterTree(t *testing.T) *xtree.Node {
	t.Helper()

	root, err := xtree.Parse([]byte(filterSample))
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func matches(t *testing.T, src string, root *xtree.Node) bool {
	t.Helper()

	f, err := ParseFilter(src)
	if err != nil {
		t.Fatalf("ParseFilter(%q): %v", src, err)
	}
	ok, err := f.Match(root)
	if err != nil {
		t.Fatalf("evaluating %q: %v", src, err)
	}

	return ok
}

func TestTheFilterUsesTheSameSyntaxAsTheV2One(t *testing.T) {
	root := filterTree(t)

	for _, tc := range []struct {
		filter string
		want   bool
	}{
		{`//administrativeGenderCode@code == "F"`, true},
		{`//administrativeGenderCode@code == "M"`, false},
		{`//administrativeGenderCode@code != "M"`, true},
		{`//family == "Okonkwo-Hale"`, true},
		{`//birthTime@value < "19600101"`, true},
		{`//birthTime@value > "19600101"`, false},
		{`//family exists`, true},
		{`//deceasedTime exists`, false},
		{`//deceasedTime empty`, true},
		{`//family empty`, false},
		{`//family matches "^Okonkwo"`, true},
		{`//family matches "^Smith"`, false},
		{`//administrativeGenderCode@code in ("F", "M")`, true},
		{`//administrativeGenderCode@code in ("X", "Y")`, false},
		{`//interactionId@extension == "PRPA_IN201306UV02" and //family exists`, true},
		{`//family == "Nobody" or //given == "Rosalind"`, true},
		{`not //family == "Nobody"`, true},
		{`(//family == "Nobody" or //given == "Rosalind") and //birthTime exists`, true},
	} {
		if got := matches(t, tc.filter, root); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.filter, got, tc.want)
		}
	}
}

// TestAnAbsentFieldDoesNotEqualEmptyString is the comparison that causes real incidents.
//
// If a path addressing nothing compared equal to "", or sorted below every value, then "the gender is not F" would match a message
// with no gender at all - and a channel would route patients on the basis of a field its sender never sends.
func TestAnAbsentFieldDoesNotEqualEmptyString(t *testing.T) {
	root := filterTree(t)

	for _, filter := range []string{
		`//deceasedTime@value == ""`,
		`//deceasedTime@value == "anything"`,
		`//deceasedTime@value < "99999999"`,
		`//deceasedTime@value > "0"`,
		`//deceasedTime@value matches ".*"`,
		`//deceasedTime@value in ("", "x")`,
	} {
		if matches(t, filter, root) {
			t.Errorf("%s matched, but the field is not in the message at all", filter)
		}
	}

	// Not-equal is the deliberate exception: whatever a message without a gender has, it is certainly not F. Somebody who
	// means "has a gender and it is not F" writes that as two clauses, which is what the language is for.
	if !matches(t, `//deceasedTime@value != "20200101"`, root) {
		t.Error(`an absent field should not be equal to a value`)
	}
	if matches(t, `//deceasedTime exists and //deceasedTime@value != "20200101"`, root) {
		t.Error(`the two-clause form should be false when the field is absent`)
	}
}

// TestNullFlavorCanBeFilteredOn covers the construct v2 has no equivalent for.
//
// A birth date the patient declined to give and a birth date nobody recorded are different clinical facts leading to different
// actions. Without this a channel has no way to express the difference.
func TestNullFlavorCanBeFilteredOn(t *testing.T) {
	flavoured, err := xtree.Parse([]byte(`<msg xmlns="urn:hl7-org:v3">
	  <patientPerson><birthTime nullFlavor="ASKU"/></patientPerson>
	</msg>`))
	if err != nil {
		t.Fatal(err)
	}
	absent, err := xtree.Parse([]byte(`<msg xmlns="urn:hl7-org:v3"><patientPerson/></msg>`))
	if err != nil {
		t.Fatal(err)
	}
	present := filterTree(t)

	// Only the null-flavoured one matches.
	if !matches(t, `//birthTime nullflavor "ASKU"`, flavoured) {
		t.Error("a nullFlavor of ASKU did not match")
	}
	if matches(t, `//birthTime nullflavor "ASKU"`, absent) {
		t.Error("a message with no birthTime at all matched a nullFlavor test")
	}
	if matches(t, `//birthTime nullflavor "ASKU"`, present) {
		t.Error("a message with a real birth date matched a nullFlavor test")
	}

	// A different flavour is a different answer. NAV means "try later" and MSK means "deliberately withheld", which lead
	// to different actions, so they must not be interchangeable.
	if matches(t, `//birthTime nullflavor "NAV"`, flavoured) {
		t.Error("ASKU matched a test for NAV")
	}

	// Case-insensitive, because the codes are defined in upper case and senders write them both ways.
	if !matches(t, `//birthTime nullflavor "asku"`, flavoured) {
		t.Error("a lower-case flavour did not match")
	}

	// The three states are distinguishable in combination, which is the whole point.
	if !matches(t, `//birthTime exists and //birthTime empty`, flavoured) {
		t.Error("present-but-no-value is not expressible")
	}
	if matches(t, `//birthTime exists and //birthTime empty`, absent) {
		t.Error("an absent element reported as present")
	}
	if matches(t, `//birthTime exists and //birthTime empty`, present) {
		t.Error("a real birth date reported as empty")
	}
}

// TestEmptyAsksAboutTheValueNotTheElementText is the trap v3 sets, pinned.
//
// A bare path reads element text, and in v3 the value is in an attribute - so the first version of "empty" reported every real birth
// date as empty. A filter written to skip patients with no date of birth would have skipped all of them.
func TestEmptyAsksAboutTheValueNotTheElementText(t *testing.T) {
	root := filterTree(t)

	// The sample has <birthTime value="19551014"/> and <administrativeGenderCode code="F"/>: no text, real values.
	if matches(t, `//birthTime empty`, root) {
		t.Error("a birth date carried in an attribute reported as empty")
	}
	if matches(t, `//administrativeGenderCode empty`, root) {
		t.Error("a gender code carried in an attribute reported as empty")
	}
	// Text-carrying elements still work the obvious way.
	if matches(t, `//family empty`, root) {
		t.Error("a family name with text reported as empty")
	}

	// A null-flavoured element has no value in any of those senses, which is the answer that makes empty mean what it says.
	flavoured, err := xtree.Parse([]byte(`<msg xmlns="urn:hl7-org:v3">
	  <patientPerson><birthTime nullFlavor="ASKU"/></patientPerson>
	</msg>`))
	if err != nil {
		t.Fatal(err)
	}
	if !matches(t, `//birthTime empty`, flavoured) {
		t.Error("a null-flavoured birth date did not report as empty")
	}

	// An explicitly named attribute is taken exactly as written, so both readings stay available.
	if !matches(t, `//birthTime@nullFlavor empty`, root) {
		t.Error("a real birth date has no nullFlavor attribute, so that path should be empty")
	}
	if matches(t, `//birthTime@value empty`, root) {
		t.Error("an explicit @value path reported empty for a value that is there")
	}
}

// TestAnyRepetitionMatching covers repeated fields, following the v2 filter.
func TestAnyRepetitionMatching(t *testing.T) {
	root := filterTree(t)

	// The patient has two identifiers. Either one matching is a match.
	if !matches(t, `//patient/id@extension == "PIX1234"`, root) {
		t.Error("the first identifier did not match")
	}
	if !matches(t, `//patient/id@extension == "999887777"`, root) {
		t.Error("the second identifier did not match")
	}
	if matches(t, `//patient/id@extension == "NOTHERE"`, root) {
		t.Error("a value not present matched")
	}

	// And an occurrence narrows it, which is how somebody says "the medical record number specifically".
	if !matches(t, `//patient/id(1)@extension == "PIX1234"`, root) {
		t.Error("selecting the first identifier failed")
	}
	if matches(t, `//patient/id(1)@extension == "999887777"`, root) {
		t.Error("an occurrence-narrowed path matched the other identifier")
	}
}

// TestAnEmptyFilterIsNoFilter pins the direction the mistake would go.
//
// A blank filter treated as "matches nothing" would mean a channel with an accidentally empty filter silently discarding every
// message, which is the failure nobody notices until somebody asks where a week of data went.
func TestAnEmptyFilterIsNoFilter(t *testing.T) {
	root := filterTree(t)

	for _, blank := range []string{"", "   ", "\n\t"} {
		f, err := ParseFilter(blank)
		if err != nil {
			t.Fatalf("ParseFilter(%q) errored: %v", blank, err)
		}
		if f != nil {
			t.Errorf("ParseFilter(%q) returned a filter", blank)
		}
		// A nil filter has to pass everything through its own method, so no call site needs a nil check - which is
		// where one gets forgotten.
		ok, err := f.Match(root)
		if err != nil || !ok {
			t.Errorf("a nil filter did not pass: %v %v", ok, err)
		}
	}
}

// TestABadFilterIsRefusedAtParseTime covers the load-time rule.
//
// A pattern that fails to compile must stop a channel loading rather than failing on every message once it is running.
func TestABadFilterIsRefusedAtParseTime(t *testing.T) {
	for _, bad := range []string{
		`//family ==`,
		`//family == unquoted`,
		`== "F"`,
		`//family matches "["`,
		`//family matches`,
		`//family in "F"`,
		`//family in ("F"`,
		`//family nullflavor`,
		`//family nullflavor ASKU`,
		`//family == "F" and`,
		`(//family == "F"`,
		`//hl7:family == "F"`,
		`//family == "F" rubbish`,
		`//family unknownkeyword "F"`,
	} {
		if _, err := ParseFilter(bad); err == nil {
			t.Errorf("ParseFilter(%q) was accepted", bad)
		}
	}
}

// TestTheFilterReportsWhichPathsItReads covers what the interface needs.
func TestTheFilterReportsWhichPathsItReads(t *testing.T) {
	f, err := ParseFilter(`//administrativeGenderCode@code == "F" and //birthTime exists or //family matches "^O"`)
	if err != nil {
		t.Fatal(err)
	}

	paths := f.Paths()
	if len(paths) != 3 {
		t.Fatalf("paths = %v, want three", paths)
	}

	// Deduplicated, so a filter naming one field twice does not report it twice in a channel summary.
	dup, err := ParseFilter(`//family == "A" or //family == "B"`)
	if err != nil {
		t.Fatal(err)
	}
	if got := dup.Paths(); len(got) != 1 {
		t.Errorf("a filter reading one field twice reported %v", got)
	}
}

// TestAFilterRoundTripsThroughItsText covers what a channel file stores.
func TestAFilterRoundTripsThroughItsText(t *testing.T) {
	const src = `//administrativeGenderCode@code == "F" and //birthTime exists`

	f, err := ParseFilter(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.String() != src {
		t.Errorf("String() = %q, want the original", f.String())
	}

	// And reparsing gives the same behaviour, or a saved channel would drift from what somebody typed.
	again, err := ParseFilter(f.String())
	if err != nil {
		t.Fatal(err)
	}
	root := filterTree(t)

	first, _ := f.Match(root)
	second, _ := again.Match(root)
	if first != second {
		t.Error("a filter behaved differently after a round trip")
	}
}

// TestBothSpellingsOfAPatternAreRefusedAtLoad replaces a test that was measuring the wrong thing.
//
// The original asserted that "false and <bad pattern>" did not error, relying on the right side never being evaluated. It passed, and
// what it actually demonstrated was that an uncompilable =~ pattern reached run time at all - which was the bug. matches compiled its
// pattern at parse time and =~ did not, so the identical mistake either stopped a channel loading or failed on every message of one
// already running, depending on which spelling somebody used.
//
// Both compile at parse time now, so this asserts the property that matters: a bad pattern never reaches a running channel.
func TestBothSpellingsOfAPatternAreRefusedAtLoad(t *testing.T) {
	for _, src := range []string{
		`//family matches "("`,
		`//family =~ "("`,
		`//family == "ok" and //given =~ "[unclosed"`,
		`//family == "ok" or //given matches "*bad"`,
	} {
		if _, err := ParseFilter(src); err == nil {
			t.Errorf("ParseFilter(%q) accepted an uncompilable pattern", src)
		}
	}

	// A good pattern still works in both spellings, and they agree.
	root := filterTree(t)
	if !matches(t, `//family =~ "^Okonkwo"`, root) {
		t.Error("=~ did not match")
	}
	if !matches(t, `//family matches "^Okonkwo"`, root) {
		t.Error("matches did not match")
	}
}

// TestShortCircuitingStillHappens covers the logic, having lost its original observable.
//
// With patterns compiled at load time there is no longer an evaluation error to guard against, so laziness is an efficiency property
// rather than a safety one. Asserted through results, which is what can be observed without instrumenting the evaluator.
func TestShortCircuitingStillHappens(t *testing.T) {
	root := filterTree(t)

	if matches(t, `//nothinghere exists and //family exists`, root) {
		t.Error("a false left side did not make the conjunction false")
	}
	if !matches(t, `//family exists or //nothinghere exists`, root) {
		t.Error("a true left side did not make the disjunction true")
	}
}

// TestAFilterOnAMissingMessageIsAnErrorNotAPass covers the nil case.
//
// Silently passing would mean an unparseable message going through a channel whose whole purpose is to exclude some of them.
func TestAFilterOnAMissingMessageIsAnErrorNotAPass(t *testing.T) {
	f, err := ParseFilter(`//family exists`)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.Match(nil); err == nil {
		t.Error("filtering nothing succeeded")
	}
}

// TestTimestampsCompareAsText documents why ordering is textual.
func TestTimestampsCompareAsText(t *testing.T) {
	root, err := xtree.Parse([]byte(`<msg xmlns="urn:hl7-org:v3">
	  <patientPerson><birthTime value="19551014"/></patientPerson>
	</msg>`))
	if err != nil {
		t.Fatal(err)
	}

	// Nearly every ordered value in v3 is a timestamp written YYYYMMDD or YYYYMMDDHHMMSS, where text order and time order
	// are the same. A numeric comparison would break the moment a value carried a timezone offset.
	if !matches(t, `//birthTime@value < "19600101"`, root) {
		t.Error("a 1955 birth date did not compare below 1960")
	}
	if !matches(t, `//birthTime@value >= "19550101"`, root) {
		t.Error("a 1955 birth date did not compare at or above the start of 1955")
	}
	if matches(t, `//birthTime@value > "20000101"`, root) {
		t.Error("a 1955 birth date compared above 2000")
	}
}

// TestKeywordsAreCaseInsensitive covers what people actually type.
func TestKeywordsAreCaseInsensitive(t *testing.T) {
	root := filterTree(t)

	for _, src := range []string{
		`//family EXISTS`,
		`//family Exists`,
		`//family == "Okonkwo-Hale" AND //birthTime exists`,
		`NOT //family == "Nobody"`,
	} {
		if !matches(t, src, root) {
			t.Errorf("%q did not match", src)
		}
	}

	// But a value is not. Codes are case-sensitive and "f" is not "F" - a filter that ignored that would match patients
	// it was not meant to.
	if matches(t, `//administrativeGenderCode@code == "f"`, root) {
		t.Error("a lower-case code matched an upper-case value")
	}
}

// TestAPathErrorInAFilterSaysWhichPath covers the message somebody sees.
func TestAPathErrorInAFilterSaysWhichPath(t *testing.T) {
	_, err := ParseFilter(`//patient/id(0)@extension == "x"`)
	if err == nil {
		t.Fatal("an occurrence of zero was accepted")
	}
	if !strings.Contains(err.Error(), "1") {
		t.Errorf("the error should explain that occurrences count from 1, got: %v", err)
	}
}
