package x12

import (
	"strings"
	"testing"
)

func TestParsePathCanonicalForms(t *testing.T) {
	cases := []struct {
		in   string
		want Path
	}{
		{"CLM", Path{Segment: "CLM", SegmentOccurs: 1}},
		{"CLM-1", Path{Segment: "CLM", SegmentOccurs: 1, Element: 1}},
		{"CLM-5.1", Path{Segment: "CLM", SegmentOccurs: 1, Element: 5, Component: 1}},
		{"NM1(2)-3", Path{Segment: "NM1", SegmentOccurs: 2, Element: 3}},
		{"REF-2(3)", Path{Segment: "REF", SegmentOccurs: 1, Element: 2, Repeat: 3}},
		{"NM1(2)-3(4).2", Path{Segment: "NM1", SegmentOccurs: 2, Element: 3, Repeat: 4, Component: 2}},
		{"N3", Path{Segment: "N3", SegmentOccurs: 1}},
		{"L11", Path{Segment: "L11", SegmentOccurs: 1}},
		{"CLM-12", Path{Segment: "CLM", SegmentOccurs: 1, Element: 12}},
		{"  CLM-1  ", Path{Segment: "CLM", SegmentOccurs: 1, Element: 1}},
	}
	for _, c := range cases {
		got, err := ParsePath(c.in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePath(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParsePathNativeGuideNotation(t *testing.T) {
	// The notation every 837 implementation guide uses. Accepting it matters because
	// the people configuring claims interfaces read those guides all day; making them
	// translate CLM01 into CLM-1 by hand is how transposition errors get in, and a
	// claim routed on CLM02 instead of CLM01 is a real outage.
	cases := []struct {
		in      string
		segment string
		element int
	}{
		{"CLM01", "CLM", 1},
		{"NM103", "NM1", 3},
		{"SV101", "SV1", 1},
		{"CLM05", "CLM", 5},
		{"BHT06", "BHT", 6},
		{"N301", "N3", 1},
		{"L1101", "L11", 1},
		{"ISA13", "ISA", 13},
		{"CLM12", "CLM", 12},
	}
	for _, c := range cases {
		got, err := ParsePath(c.in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", c.in, err)
			continue
		}
		if got.Segment != c.segment || got.Element != c.element {
			t.Errorf("ParsePath(%q) = %s element %d, want %s element %d",
				c.in, got.Segment, got.Element, c.segment, c.element)
		}
	}
}

func TestNativeNotationDoesNotEatRealSegmentIdentifiers(t *testing.T) {
	// The case that looks dangerous. "L11" is a real segment. Stripping two digits
	// leaves "L", too short to be a segment, so it must read as the whole L11 segment
	// and not as L1 element 1.
	for _, in := range []string{"L11", "N3", "K3", "G62", "HL", "SE"} {
		p, err := ParsePath(in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", in, err)
			continue
		}
		if p.Segment != in {
			t.Errorf("ParsePath(%q) read the segment as %q", in, p.Segment)
		}
		if p.Element != 0 {
			t.Errorf("ParsePath(%q) invented element %d", in, p.Element)
		}
	}
}

func TestNativeNotationIsNotAppliedToExplicitPaths(t *testing.T) {
	// Once a path carries an explicit element or occurrence, the identifier is the
	// identifier. Inferring on top of that would make "L11-1" ambiguous.
	p, err := ParsePath("L11-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Segment != "L11" || p.Element != 1 {
		t.Errorf("ParsePath(\"L11-1\") = %+v", p)
	}

	p, err = ParsePath("NM1(2)")
	if err != nil {
		t.Fatal(err)
	}
	if p.Segment != "NM1" || p.Element != 0 || p.SegmentOccurs != 2 {
		t.Errorf("ParsePath(\"NM1(2)\") = %+v", p)
	}
}

func TestParsePathRejectsBadInput(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"-1", "does not start with a segment identifier"},
		{"C", "two or three characters"},
		{"CLAIM", "two or three characters"},
		{"CLM-", "number"},
		{"CLM-0", "start at 1"},
		{"CLM-1.", "number"},
		{"CLM-1.0", "start at 1"},
		{"CLM(0)-1", "starts at 1"},
		{"CLM(2", "missing"},
		{"CLM()", "empty"},
		{"CLM(x)", "not a number"},
		{"CLM-1(0)", "starts at 1"},
		{"CLM-1(x)", "not a number"},
		{"CLM.1", "expected"},
		{"CLM-1.2.3", "no subcomponents"},
		{"CLM-1!", "expected"},
	}
	for _, c := range cases {
		_, err := ParsePath(c.in)
		if err == nil {
			t.Errorf("ParsePath(%q) was accepted", c.in)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParsePath(%q) = %v, want it to mention %q", c.in, err, c.want)
		}
	}
}

func TestAnHL7SubcomponentGetsAUsefulError(t *testing.T) {
	// Somebody coming from HL7 will write this. Saying why it cannot work is more use
	// than "unexpected input", because the reason is a real difference between the
	// formats rather than a typo.
	_, err := ParsePath("CLM-5.1.2")
	if err == nil {
		t.Fatal("a subcomponent path was accepted")
	}
	if !strings.Contains(err.Error(), "HL7") {
		t.Errorf("the error should name the difference, got: %v", err)
	}
}

func TestPathStringIsCanonicalAndRoundTrips(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"CLM", "CLM"},
		{"CLM-1", "CLM-1"},
		{"CLM01", "CLM-1"},
		{"NM103", "NM1-3"},
		{"NM1(2)-3", "NM1(2)-3"},
		{"NM1(1)-3", "NM1-3"},
		{"REF-2(3)", "REF-2(3)"},
		{"REF-2(1)", "REF-2(1)"},
		{"CLM-5.1", "CLM-5.1"},
		{"NM1(2)-3(4).2", "NM1(2)-3(4).2"},
	}
	for _, c := range cases {
		p, err := ParsePath(c.in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", c.in, err)
			continue
		}
		if got := p.String(); got != c.want {
			t.Errorf("ParsePath(%q).String() = %q, want %q", c.in, got, c.want)
		}
		// The canonical form must parse back to the same path, or a path shown in the
		// interface cannot be pasted back into a channel file.
		again, err := ParsePath(p.String())
		if err != nil {
			t.Errorf("the canonical form %q does not parse: %v", p.String(), err)
			continue
		}
		if again != p {
			t.Errorf("round trip of %q gave %+v, want %+v", c.in, again, p)
		}
	}
}

func TestGetResolvesValues(t *testing.T) {
	m, err := Parse([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want string
	}{
		{"CLM01", "PATIENT-ACCT-1"},
		{"CLM-1", "PATIENT-ACCT-1"},
		{"CLM02", "500"},
		{"CLM05", "11:B:1"},
		{"CLM-5.1", "11"},
		{"CLM-5.2", "B"},
		{"CLM-5.3", "1"},
		{"ST01", "837"},
		{"ST02", "0001"},
		{"BHT06", "CH"},
		{"NM101", "41"},
		{"NM103", "SUBMITTER NAME"},
		{"GS08", "005010X222A1"},
		{"SE01", "6"},
		{"IEA02", "000000001"},
	}
	for _, c := range cases {
		got, err := m.Get(c.path)
		if err != nil {
			t.Errorf("Get(%q): %v", c.path, err)
			continue
		}
		if got != c.want {
			t.Errorf("Get(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestGetWholeSegment(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	got, err := m.Get("ST")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ST*837*0001*005010X222A1" {
		t.Errorf("Get(\"ST\") = %q", got)
	}
}

func TestAbsenceResolvesToEmptyNotAnError(t *testing.T) {
	// Absence is ordinary in X12: trailing elements are routinely omitted and whole
	// segments are situational. Treating it as a fault would make every caller write
	// the same check, and the check would be wrong - an empty element and a missing
	// one mean the same thing to a receiver.
	m, _ := Parse([]byte(claim837))

	for _, path := range []string{"ZZZ", "ZZZ-1", "CLM-99", "CLM-99.4", "NM1(9)-3", "CLM-5.9"} {
		got, err := m.Get(path)
		if err != nil {
			t.Errorf("Get(%q) returned an error: %v", path, err)
		}
		if got != "" {
			t.Errorf("Get(%q) = %q, want empty", path, got)
		}
	}
}

func TestGetPropagatesPathErrors(t *testing.T) {
	// A malformed path is a configuration fault, unlike a missing value, so it must
	// surface rather than resolve to empty.
	m, _ := Parse([]byte(claim837))
	if _, err := m.Get("CLM-0"); err == nil {
		t.Error("a malformed path resolved silently")
	}
}

func TestSegmentOccurrenceSelects(t *testing.T) {
	// NM1 appears in every loop of an 837, so this is the common case rather than a
	// rarity.
	multi := strings.Replace(claim837,
		"NM1*41*2*SUBMITTER NAME*****46*SUBMITTERID~",
		"NM1*41*2*SUBMITTER NAME*****46*SUBMITTERID~NM1*40*2*RECEIVER NAME*****46*RECEIVERID~", 1)
	multi = strings.Replace(multi, "SE*6*0001~", "SE*7*0001~", 1)

	m, err := Parse([]byte(multi))
	if err != nil {
		t.Fatal(err)
	}
	if v := m.Validate(); !v.OK() {
		t.Fatalf("fixture is not valid: %v", v.Problems)
	}

	if got, _ := m.Get("NM103"); got != "SUBMITTER NAME" {
		t.Errorf("NM103 = %q, want the first occurrence", got)
	}
	if got, _ := m.Get("NM1(2)-3"); got != "RECEIVER NAME" {
		t.Errorf("NM1(2)-3 = %q", got)
	}
	if got, _ := m.Get("NM1(1)-1"); got != "41" {
		t.Errorf("NM1(1)-1 = %q", got)
	}
}

func TestARepeatedElementReturnsEverythingWhenNoRepetitionIsNamed(t *testing.T) {
	// Same rule as HL7 here: asking for the element without naming a repetition should
	// show you that it repeats, not quietly hand back the first value and hide the
	// rest. Hiding it is how a second identifier gets dropped without anybody noticing.
	m, _ := Parse([]byte(claim837))
	seg := newSegment([]byte("REF*XX*A^B^C"), m.Delimiters())

	whole := Element{raw: seg.elements[1], delim: m.Delimiters()}
	if got := whole.String(); got != "A^B^C" {
		t.Errorf("the whole element = %q, want every repetition", got)
	}
}

func TestRepetitionSelectionThroughAPath(t *testing.T) {
	withRepeats := strings.Replace(claim837, "CLM*PATIENT-ACCT-1*500*", "CLM*A^B^C*500*", 1)
	m, err := Parse([]byte(withRepeats))
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := m.Get("CLM-1"); got != "A^B^C" {
		t.Errorf("CLM-1 = %q, want every repetition", got)
	}
	if got, _ := m.Get("CLM-1(2)"); got != "B" {
		t.Errorf("CLM-1(2) = %q, want B", got)
	}
	if got, _ := m.Get("CLM-1(4)"); got != "" {
		t.Errorf("CLM-1(4) = %q, want empty", got)
	}
}

func TestARepetitionIsSelectedBeforeAComponent(t *testing.T) {
	// A repeated composite element repeats whole composites, so the repetition has to
	// be chosen first. The other order reads components out of the wrong repetition,
	// which produces plausible values that are simply the wrong ones.
	withRepeats := strings.Replace(claim837, "CLM*PATIENT-ACCT-1*500*[**11:B:1*", "CLM*X*500*[**11:B:1^22:C:2*", 1)
	m, err := Parse([]byte(withRepeats))
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := m.Get("CLM-5(1).1"); got != "11" {
		t.Errorf("CLM-5(1).1 = %q, want 11", got)
	}
	if got, _ := m.Get("CLM-5(2).1"); got != "22" {
		t.Errorf("CLM-5(2).1 = %q, want 22", got)
	}
	if got, _ := m.Get("CLM-5(2).3"); got != "2" {
		t.Errorf("CLM-5(2).3 = %q, want 2", got)
	}
}

func TestExistsDistinguishesEmptyFromAbsent(t *testing.T) {
	// A filter needs to tell "this element is empty" from "this segment is not here at
	// all", and a string cannot carry both.
	m, _ := Parse([]byte(claim837))

	present, _ := ParsePath("CLM-1")
	if !m.Exists(present) {
		t.Error("a populated element reported absent")
	}

	empty, _ := ParsePath("HL-2")
	if m.Exists(empty) {
		t.Error("an empty element reported present")
	}

	missingSeg, _ := ParsePath("ZZZ-1")
	if m.Exists(missingSeg) {
		t.Error("an element of a missing segment reported present")
	}

	wholeSeg, _ := ParsePath("CLM")
	if !m.Exists(wholeSeg) {
		t.Error("a present segment reported absent")
	}

	wholeMissing, _ := ParsePath("ZZZ")
	if m.Exists(wholeMissing) {
		t.Error("a missing segment reported present")
	}
}

func TestValueAtToleratesAZeroOccurrence(t *testing.T) {
	// A Path built in code rather than parsed can carry a zero, and defaulting to the
	// first occurrence is better than returning nothing for a path that names a real
	// segment.
	m, _ := Parse([]byte(claim837))
	if got := m.ValueAt(Path{Segment: "CLM", Element: 1}); got != "PATIENT-ACCT-1" {
		t.Errorf("ValueAt with occurrence 0 = %q", got)
	}
	if !m.Exists(Path{Segment: "CLM", Element: 1}) {
		t.Error("Exists with occurrence 0 reported absent")
	}
}
