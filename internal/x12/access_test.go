package x12

import (
	"strings"
	"testing"
)

// A small but structurally real 837: ISA/GS/ST envelope, repeated NM1 segments, a composite, and no trailing elements
// on some segments. The repeated NM1 is the point - an 837 has one in every loop, so occurrence addressing is not an
// edge case here the way it is in HL7.
const claim = "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       " +
	"*260829*1200*^*00501*000000001*0*P*:~" +
	"GS*HC*SENDER*RECEIVER*20260829*1200*1*X*005010X222A1~" +
	"ST*837*0001*005010X222A1~" +
	"BHT*0019*00*0123*20260829*1200*CH~" +
	"NM1*41*2*SUBMITTER NAME*****46*123456789~" +
	"NM1*40*2*RECEIVER NAME*****46*987654321~" +
	"CLM*PATIENT001*500***11:B:1*Y*A*Y*Y~" +
	"REF*D9*CLAIM123~" +
	"SE*7*0001~" +
	"GE*1*1~" +
	"IEA*1*000000001~"

func parseClaim(t *testing.T) *Message {
	t.Helper()
	m, err := Parse([]byte(claim))
	if err != nil {
		t.Fatalf("the fixture does not parse: %v", err)
	}
	return m
}

func TestReadingAnElementInBothNotations(t *testing.T) {
	m := parseClaim(t)

	// The same element, written the two ways the two audiences write it. Both must resolve identically, or a channel
	// behaves differently depending on who configured it.
	for _, path := range []string{"CLM-1", "CLM01"} {
		got, ok := ReadString(m, path)
		if !ok {
			t.Fatalf("%s did not resolve", path)
		}
		if got != "PATIENT001" {
			t.Fatalf("%s = %q, want PATIENT001", path, got)
		}
	}
}

func TestReadingTheSecondOccurrenceOfARepeatedSegment(t *testing.T) {
	m := parseClaim(t)

	first, ok := ReadString(m, "NM1-3")
	if !ok || first != "SUBMITTER NAME" {
		t.Fatalf("NM1-3 = %q, %v; want SUBMITTER NAME", first, ok)
	}

	// Without occurrence addressing this returns the submitter, which is the wrong trading partner - the kind of wrong
	// answer that looks entirely plausible in a claim.
	second, ok := ReadString(m, "NM1(2)-3")
	if !ok || second != "RECEIVER NAME" {
		t.Fatalf("NM1(2)-3 = %q, %v; want RECEIVER NAME", second, ok)
	}
}

func TestReadingAComponentOfAComposite(t *testing.T) {
	m := parseClaim(t)

	// CLM05 is 11:B:1 - place of service, facility code qualifier, frequency code.
	for path, want := range map[string]string{
		"CLM-5":   "11:B:1",
		"CLM-5.1": "11",
		"CLM-5.2": "B",
		"CLM-5.3": "1",
	} {
		got, ok := ReadString(m, path)
		if !ok || got != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, ok, want)
		}
	}
}

func TestAnAbsentElementIsDistinguishedFromAnEmptyOne(t *testing.T) {
	m := parseClaim(t)

	// NM1-4 is present and empty: the sender left it blank deliberately.
	got, ok := ReadString(m, "NM1-4")
	if !ok {
		t.Fatal("NM1-4 is present and empty, so it must resolve")
	}
	if got != "" {
		t.Fatalf("NM1-4 = %q, want empty", got)
	}

	// REF has two elements, so element 9 was never sent at all. A caller that cannot tell these apart cannot implement
	// "only set this if the partner did not send it".
	if _, ok := ReadString(m, "REF-9"); ok {
		t.Fatal("REF-9 was never sent, so it must not resolve")
	}
}

func TestWritingAnElementLeavesEverythingElseByteForByteIdentical(t *testing.T) {
	m := parseClaim(t)

	out, err := SetString(m, "CLM-1", "PATIENT999")
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	got, _ := ReadString(out, "CLM-1")
	if got != "PATIENT999" {
		t.Fatalf("CLM-1 = %q after write", got)
	}

	// The strongest available check that nothing else moved: the only difference between the two interchanges is the
	// value that was asked for. A rebuild that dropped a trailing empty element or changed a separator would show here,
	// and would look harmless right up to the point a claims processor read the shifted values.
	before, after := string(m.Raw()), string(out.Raw())
	if strings.Replace(before, "PATIENT001", "PATIENT999", 1) != after {
		t.Fatalf("the rebuild changed more than the value asked for\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestWritingDoesNotChangeTheMessageItWasGiven(t *testing.T) {
	m := parseClaim(t)
	original := string(m.Raw())

	if _, err := SetString(m, "CLM-1", "CHANGED"); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	// A step that returns a new message and also mutates the old one means a pipeline that fails halfway leaves
	// something no configuration describes.
	if string(m.Raw()) != original {
		t.Fatal("Set modified the message it was given")
	}
}

func TestWritingPastTheLastElementPadsTheOnesBetween(t *testing.T) {
	m := parseClaim(t)

	// REF has two elements. Writing the fourth must create the third as empty, because X12 elements are positional and
	// an 837 omitting trailing elements is entirely normal.
	out, err := SetString(m, "REF-4", "EXTRA")
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	if got, ok := ReadString(out, "REF-4"); !ok || got != "EXTRA" {
		t.Fatalf("REF-4 = %q, %v", got, ok)
	}
	if got, ok := ReadString(out, "REF-3"); !ok || got != "" {
		t.Fatalf("REF-3 should exist and be empty, got %q %v", got, ok)
	}
	// And the values that were already there must not have moved.
	if got, _ := ReadString(out, "REF-1"); got != "D9" {
		t.Fatalf("REF-1 moved: %q", got)
	}
}

func TestWritingAComponentKeepsTheOthers(t *testing.T) {
	m := parseClaim(t)

	out, err := SetString(m, "CLM-5.2", "C")
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	if got, _ := ReadString(out, "CLM-5"); got != "11:C:1" {
		t.Fatalf("CLM-5 = %q, want 11:C:1", got)
	}
}

func TestWritingASegmentWholesaleIsRefusedWithTheReason(t *testing.T) {
	m := parseClaim(t)

	_, err := SetString(m, "CLM", "CLM*A*B~")
	if err == nil {
		t.Fatal("writing a whole segment should be refused")
	}
	// The message has to say what would go wrong, and name what to do instead. "Unsupported" sends somebody looking for
	// a version that supports it.
	if !strings.Contains(err.Error(), "shift every value") {
		t.Fatalf("the refusal should explain the consequence, got: %v", err)
	}
	if !strings.Contains(err.Error(), "CLM-1") {
		t.Fatalf("the refusal should name what to write instead, got: %v", err)
	}
}

func TestAValueCarryingTheElementSeparatorIsRefused(t *testing.T) {
	m := parseClaim(t)

	// This is the case that matters most in the whole file. A value containing * does not produce a wrong value - it
	// produces an extra element, silently shifting everything after it. The interchange still parses and still looks
	// ordinary.
	_, err := SetString(m, "CLM-1", "PATIENT*001")
	if err == nil {
		t.Fatal("a value containing the element separator must be refused")
	}
	if !strings.Contains(err.Error(), "wrong position") {
		t.Fatalf("the refusal should say what would happen, got: %v", err)
	}
}

func TestAValueCarryingTheSegmentTerminatorIsRefused(t *testing.T) {
	m := parseClaim(t)

	if _, err := SetString(m, "CLM-1", "PATIENT~001"); err == nil {
		t.Fatal("a value containing the segment terminator must be refused")
	}
}

func TestAValueCarryingTheComponentSeparatorIsRefusedOnlyWhenAddressingAComponent(t *testing.T) {
	m := parseClaim(t)

	// Addressing a component: a colon in the value would create a component the caller did not ask for.
	if _, err := SetString(m, "CLM-5.1", "11:X"); err == nil {
		t.Fatal("a component value containing the component separator must be refused")
	}

	// Addressing a whole element: the caller may legitimately be assembling a composite, so this is allowed.
	out, err := SetString(m, "CLM-5", "22:C:2")
	if err != nil {
		t.Fatalf("assembling a composite as a whole element should be allowed: %v", err)
	}
	if got, _ := ReadString(out, "CLM-5.3"); got != "2" {
		t.Fatalf("CLM-5.3 = %q, want 2", got)
	}
}

func TestWritingTheSecondOccurrenceLeavesTheFirstAlone(t *testing.T) {
	m := parseClaim(t)

	out, err := SetString(m, "NM1(2)-3", "NEW RECEIVER")
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	if got, _ := ReadString(out, "NM1-3"); got != "SUBMITTER NAME" {
		t.Fatalf("the first NM1 changed: %q", got)
	}
	if got, _ := ReadString(out, "NM1(2)-3"); got != "NEW RECEIVER" {
		t.Fatalf("NM1(2)-3 = %q", got)
	}
}

func TestAskingForAnOccurrenceThatIsNotThereSaysHowManyThereAre(t *testing.T) {
	m := parseClaim(t)

	_, err := SetString(m, "NM1(5)-3", "X")
	if err == nil {
		t.Fatal("the fifth NM1 does not exist, so this must fail")
	}
	// "not found" is not enough. Knowing there are two is what tells somebody their loop assumption is wrong.
	if !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("the error should say how many were found, got: %v", err)
	}
}

func TestClearingEmptiesTheElementWithoutRemovingIt(t *testing.T) {
	m := parseClaim(t)

	p, err := ParsePath("CLM-1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Clear(m, p)
	if err != nil {
		t.Fatalf("clear failed: %v", err)
	}

	// Present and empty, not absent. A trading partner reads those differently, so clearing must not become removing.
	got, ok := ReadString(out, "CLM-1")
	if !ok {
		t.Fatal("a cleared element must still be present")
	}
	if got != "" {
		t.Fatalf("CLM-1 = %q, want empty", got)
	}
}

func TestTheResultOfAWriteReparsesAndValidates(t *testing.T) {
	m := parseClaim(t)

	out, err := SetString(m, "CLM-1", "PATIENT999")
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	// A rebuild that produced something only this package could read would be useless. The envelope validator is the
	// independent check, since it counts segments and reads control numbers rather than trusting the parse.
	if v := out.Validate(); !v.OK() {
		t.Fatalf("the rebuilt interchange does not validate: %v", v.Err())
	}
	if out.SegmentCount() != m.SegmentCount() {
		t.Fatalf("segment count changed from %d to %d", m.SegmentCount(), out.SegmentCount())
	}
}

func TestSegmentsSeparatedByNewlinesKeepTheirNewlines(t *testing.T) {
	// Plenty of senders put a newline after each terminator for readability. If a rebuild dropped them, a diff between
	// what arrived and what was sent on would show every line as changed, which buries the one value that did change.
	pretty := strings.ReplaceAll(claim, "~", "~\n")
	m, err := Parse([]byte(pretty))
	if err != nil {
		t.Fatalf("the pretty-printed fixture does not parse: %v", err)
	}

	out, err := SetString(m, "CLM-1", "PATIENT999")
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	if !strings.Contains(string(out.Raw()), "~\n") {
		t.Fatal("the newlines after each terminator were lost")
	}
	if strings.Replace(string(m.Raw()), "PATIENT001", "PATIENT999", 1) != string(out.Raw()) {
		t.Fatalf("more than the value changed:\n%s", out.Raw())
	}
}
