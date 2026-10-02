package x12xml

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/x12"
)

const claim837 = "ISA*00*          *00*          *ZZ*SUBMITTERID    *ZZ*RECEIVERID     *260819*1253*^*00501*000000001*0*P*:~" +
	"GS*HC*SUBMITTERID*RECEIVERID*20260819*1253*1*X*005010X222A1~" +
	"ST*837*0001*005010X222A1~" +
	"BHT*0019*00*244579*20260819*1253*CH~" +
	"NM1*41*2*SUBMITTER NAME*****46*SUBMITTERID~" +
	"HL*1**20*1~" +
	"CLM*PATIENT-ACCT-1*500***11:B:1*Y*A*Y*I~" +
	"SE*6*0001~" +
	"GE*1*1~" +
	"IEA*1*000000001~"

func TestFromRawBuildsTheExpectedShape(t *testing.T) {
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}

	if root.Name != Root {
		t.Errorf("root = %q, want %q", root.Name, Root)
	}
	if got := len(root.Children); got != 10 {
		t.Errorf("segments = %d, want 10", got)
	}

	// Elements are named in implementation-guide form, so a person reading an 837
	// guide and a person reading the tree see the same token.
	clm := root.First("CLM")
	if clm == nil {
		t.Fatal("no CLM segment")
	}
	if got := clm.First("CLM01").Value(); got != "PATIENT-ACCT-1" {
		t.Errorf("CLM01 = %q", got)
	}
	if got := clm.First("CLM02").Value(); got != "500" {
		t.Errorf("CLM02 = %q", got)
	}
}

func TestElementNamesAreTwoDigits(t *testing.T) {
	// Because that is how the guides write it. A single digit reads as a different
	// element to anybody checking against a guide, and the point of this naming is that
	// the two agree.
	if got := ElementName("CLM", 1); got != "CLM01" {
		t.Errorf("ElementName(CLM,1) = %q, want CLM01", got)
	}
	if got := ElementName("CLM", 9); got != "CLM09" {
		t.Errorf("ElementName(CLM,9) = %q, want CLM09", got)
	}
	if got := ElementName("CLM", 10); got != "CLM10" {
		t.Errorf("ElementName(CLM,10) = %q, want CLM10", got)
	}
	if got := ElementName("ISA", 13); got != "ISA13" {
		t.Errorf("ElementName(ISA,13) = %q, want ISA13", got)
	}
}

func TestCompositeElementsBecomeChildNodes(t *testing.T) {
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}

	// CLM05 is the composite place-of-service code, "11:B:1".
	clm05 := root.First("CLM").First("CLM05")
	if clm05 == nil {
		t.Fatal("no CLM05")
	}
	if len(clm05.Children) != 3 {
		t.Fatalf("CLM05 has %d components, want 3", len(clm05.Children))
	}
	for i, want := range []string{"11", "B", "1"} {
		child := clm05.Children[i]
		wantName := ElementName("CLM", 5) + "." + string(rune('1'+i))
		if child.Name != wantName {
			t.Errorf("component %d named %q, want %q", i+1, child.Name, wantName)
		}
		if child.Value() != want {
			t.Errorf("component %d = %q, want %q", i+1, child.Value(), want)
		}
	}
}

func TestANonCompositeElementIsALeaf(t *testing.T) {
	root, _ := FromRaw([]byte(claim837))
	clm01 := root.First("CLM").First("CLM01")
	if !clm01.Simple() {
		t.Error("a non-composite element gained children")
	}
}

func TestRepeatedElementsBecomeRepeatedNodes(t *testing.T) {
	// Same shape as HL7 repetitions, which is what makes indexing behave the same way
	// in scripts and in the filter language.
	withRepeats := strings.Replace(claim837, "CLM*PATIENT-ACCT-1*", "CLM*A^B^C*", 1)
	root, err := FromRaw([]byte(withRepeats))
	if err != nil {
		t.Fatal(err)
	}

	clm := root.First("CLM")
	if got := clm.Count("CLM01"); got != 3 {
		t.Fatalf("CLM01 occurrences = %d, want 3", got)
	}
	for i, want := range []string{"A", "B", "C"} {
		if got := clm.Child("CLM01", i).Value(); got != want {
			t.Errorf("CLM01[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestTheISASegmentKeepsItsPadding(t *testing.T) {
	// ISA is fixed width, so the padding is part of the format rather than part of the
	// value. An ISA rebuilt without it is no longer 106 bytes, and the next parser
	// along reads its delimiters from byte offsets that no longer line up.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}

	isa := root.First("ISA")
	if got := isa.First("ISA06").Value(); got != "SUBMITTERID    " {
		t.Errorf("ISA06 = %q, want the padding preserved", got)
	}
	if got := isa.First("ISA02").Value(); got != "          " {
		t.Errorf("ISA02 = %q, want ten spaces", got)
	}
}

func TestTheISASegmentIsNotComponentSplit(t *testing.T) {
	// ISA16 holds the component separator itself. Splitting on it would turn the
	// declaration of the delimiter into an empty element and lose it.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	isa := root.First("ISA")
	if got := isa.First("ISA16").Value(); got != ":" {
		t.Errorf("ISA16 = %q, want the separator itself", got)
	}
	if !isa.First("ISA16").Simple() {
		t.Error("ISA16 was component-split, which would lose the separator")
	}
}

func TestRoundTripIsByteExact(t *testing.T) {
	// The test that matters. A claims file that changes on the way through a
	// transformation it was not supposed to affect is a file the trading partner may
	// reject, and the difference is usually invisible to the eye.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != claim837 {
		t.Errorf("round trip changed the interchange\n got: %s\nwant: %s", out, claim837)
	}
}

func TestRoundTripPreservesUnusualDelimiters(t *testing.T) {
	// The delimiters are read from the tree's own ISA rather than assumed, because
	// re-encoding a partner's file in a different dialect is how a round trip through
	// an unrelated transformation starts getting files rejected.
	piped := strings.NewReplacer("*", "|", "~", "+", ":", ">").Replace(claim837)

	root, err := FromRaw([]byte(piped))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != piped {
		t.Errorf("round trip changed the dialect\n got: %s\nwant: %s", out, piped)
	}
}

func TestRoundTripSurvivesAnEditedValue(t *testing.T) {
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	root.First("CLM").First("CLM01").SetText("NEW-ACCT")

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "CLM*NEW-ACCT*500") {
		t.Errorf("the edit did not survive: %s", out)
	}
	// And the parser must accept the result, which is the real proof.
	if _, err := x12.Parse(out); err != nil {
		t.Errorf("the re-encoded interchange does not parse: %v", err)
	}
}

func TestElementsAreWrittenByNumberNotTreeOrder(t *testing.T) {
	// X12 is positional. An element written in the wrong slot is not a formatting
	// problem, it is a different value - so a transformation that appended an element
	// out of order must still produce a correct segment.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}

	bht := root.First("BHT")
	// Remove BHT02 and append it again, putting it last in tree order.
	old := bht.First("BHT02")
	value := old.Value()
	bht.Remove(old)
	bht.Append(leafNamed("BHT02", value))

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "BHT*0019*00*244579*") {
		t.Errorf("elements were written in tree order rather than by number: %s", out)
	}
}

func TestEmptyElementsArePreserved(t *testing.T) {
	// HL*1**20*1 has an empty HL02, and CLM has two empty slots. Dropping an empty
	// element shifts every element after it by one, which in a positional format
	// silently changes what each one means.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "HL*1**20*1~") {
		t.Errorf("the empty HL02 was not preserved: %s", out)
	}
	if !strings.Contains(string(out), "CLM*PATIENT-ACCT-1*500***11:B:1*") {
		t.Errorf("the empty CLM03 and CLM04 were not preserved: %s", out)
	}
}

func TestScratchNodesAreSkippedNotWritten(t *testing.T) {
	// A transformation can leave working nodes behind. Writing them into a claims file
	// would corrupt it, so anything that is not a plausible segment id is dropped.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	root.Append(leafNamed("scratch", "working value"))
	root.Append(leafNamed("TOOLONG", "x"))

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != claim837 {
		t.Errorf("a scratch node reached the output: %s", out)
	}
}

func TestARepeatedElementWithoutASeparatorIsRefused(t *testing.T) {
	// An interchange before version 00501 has no way to express a repeated element.
	// Joining on some substitute character produces a file the receiver reads as one
	// value with punctuation in it, so this is refused instead.
	old := strings.Replace(claim837, "*^*00501*", "*U*00401*", 1)
	root, err := FromRaw([]byte(old))
	if err != nil {
		t.Fatal(err)
	}

	clm := root.First("CLM")
	clm.Append(leafNamed("CLM01", "SECOND"))

	_, err = ToX12(root)
	if err == nil {
		t.Fatal("a repeated element was written into a 00401 interchange")
	}
	if !strings.Contains(err.Error(), "00501") {
		t.Errorf("the error should name the version rule, got: %v", err)
	}
}

func TestToX12RejectsAWrongRoot(t *testing.T) {
	// Catches a transformation that replaced the root by accident, which otherwise
	// produces a file with one segment in it.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	root.Name = "HL7Message"

	if _, err := ToX12(root); err == nil {
		t.Fatal("a tree with an HL7 root was encoded as X12")
	}
}

func TestNilInputsAreHandled(t *testing.T) {
	if _, err := FromMessage(nil); err == nil {
		t.Error("FromMessage(nil) did not fail")
	}
	if _, err := ToX12(nil); err == nil {
		t.Error("ToX12(nil) did not fail")
	}
}

func TestFromRawPropagatesParseErrors(t *testing.T) {
	if _, err := FromRaw([]byte("MSH|^~\\&|A")); err == nil {
		t.Error("an HL7 message was accepted as X12")
	}
}

func TestATreeBuiltFromNothingUsesTheCommonDelimiters(t *testing.T) {
	// A transformation building an interchange from scratch has no ISA to read, and the
	// near-universal delimiters are the only defensible default.
	root := newRoot()
	seg := newNode("REF")
	seg.Append(leafNamed("REF01", "XX"))
	seg.Append(leafNamed("REF02", "VALUE"))
	root.Append(seg)

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "REF*XX*VALUE~" {
		t.Errorf("out = %q", out)
	}
}

func TestATrailingEmptyComponentIsPreserved(t *testing.T) {
	// A partner who sends "11:" gets "11:" back. Truncating to "11" would mean an
	// unrelated transformation changed the bytes of a claims file - the two are
	// equivalent to a receiver, but only one of them round-trips.
	root := newRoot()
	seg := newNode("CLM")
	el := newNode("CLM01")
	el.Append(leafNamed("CLM01.1", "11"))
	el.Append(leafNamed("CLM01.2", ""))
	seg.Append(el)
	root.Append(seg)

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "CLM*11:~" {
		t.Errorf("out = %q, want CLM*11:~", out)
	}

	// And it survives a full round trip through the parser.
	again, err := FromRaw([]byte("ISA*00*          *00*          *ZZ*S              *ZZ*R              *260819*1253*^*00501*000000001*0*P*:~CLM*11:~IEA*1*000000001~"))
	if err != nil {
		t.Fatal(err)
	}
	back, err := ToX12(again)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back), "CLM*11:~") {
		t.Errorf("the trailing empty component was lost: %s", back)
	}
}

func TestAnElementNumberFixesItsPosition(t *testing.T) {
	// X12 is positional, so a tree carrying only CLM05 must still put it in slot five.
	// Writing it first would silently turn a place-of-service code into a patient
	// account number.
	root := newRoot()
	seg := newNode("CLM")
	seg.Append(leafNamed("CLM05", "11"))
	root.Append(seg)

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "CLM*****11~" {
		t.Errorf("out = %q, want CLM*****11~", out)
	}
}

func TestTheDelimitersTravelOnTheRoot(t *testing.T) {
	// Two of the four cannot be recovered any other way. HL7 gets this for free because
	// MSH-1 and MSH-2 hold the delimiters as content; X12 only does it halfway. ISA16
	// and ISA11 are values, but the element separator and segment terminator exist
	// purely between things and appear nowhere as a value, so parsing consumes them.
	piped := strings.NewReplacer("*", "|", "~", "+", ":", ">").Replace(claim837)

	root, err := FromRaw([]byte(piped))
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"elementSeparator":    "|",
		"segmentTerminator":   "+",
		"componentSeparator":  ">",
		"repetitionSeparator": "^",
	} {
		if got := root.AttrValue(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestISAContentIsUsedWhenTheAttributesAreGone(t *testing.T) {
	// A script that rebuilds the root loses the attributes. ISA16 and ISA11 are still
	// there in the content, so the component and repetition separators can be recovered
	// - which is better than defaulting all four.
	root, err := FromRaw([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"elementSeparator", "segmentTerminator", "componentSeparator", "repetitionSeparator"} {
		root.RemoveAttr(a)
	}

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	// The element separator and terminator fall back to the common ones, which happen
	// to be right here, and the composite is still joined on ISA16's colon.
	if !strings.Contains(string(out), "11:B:1") {
		t.Errorf("the component separator was not recovered from ISA16: %s", out)
	}
	if _, err := x12.Parse(out); err != nil {
		t.Errorf("the result does not parse: %v", err)
	}
}

func TestAnUnusualComponentSeparatorIsRecoveredFromISA16(t *testing.T) {
	// The half that X12 does describe itself. Worth having as a distinct fallback,
	// because it is the one a hand-assembled tree is most likely to get right.
	withCaret := strings.Replace(claim837, "*P*:~", "*P*>~", 1)
	withCaret = strings.Replace(withCaret, "11:B:1", "11>B>1", 1)

	root, err := FromRaw([]byte(withCaret))
	if err != nil {
		t.Fatal(err)
	}
	root.RemoveAttr("componentSeparator")

	out, err := ToX12(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "11>B>1") {
		t.Errorf("ISA16 was not consulted: %s", out)
	}
}
