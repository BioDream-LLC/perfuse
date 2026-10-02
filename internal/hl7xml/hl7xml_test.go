package hl7xml

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

const sample = "MSH|^~\\&|SENDAPP|SITEA|RECV|RFAC|20260818120000||ADT^A01^ADT_A01|CTRL1|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN9^^^SITEA^MR~999^^^SSA^SS||Doe^Jane^Q^^Ms.||19800101|F\r" +
	"PV1|1|I|ICU^7^01^SITEA||||1234^Smith^Sam\r"

// TestMirthShape pins the element names and nesting that Mirth produces.
//
// This is the contract every ported script depends on, so it is asserted
// literally rather than through a helper. If this test changes, somebody's
// working script breaks.
func TestMirthShape(t *testing.T) {
	root, err := FromRaw([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}

	if root.Name != "HL7Message" {
		t.Errorf("root = %q, want HL7Message", root.Name)
	}

	// A field with one component still gets a numbered component element,
	// because scripts index straight to .1.
	if got := root.Path("PID/PID.1/PID.1.1").Value(); got != "1" {
		t.Errorf("PID.1.1 = %q, want 1", got)
	}
	if got := root.Path("PID/PID.5/PID.5.1").Value(); got != "Doe" {
		t.Errorf("PID.5.1 = %q, want Doe", got)
	}
	if got := root.Path("PID/PID.5/PID.5.2").Value(); got != "Jane" {
		t.Errorf("PID.5.2 = %q, want Jane", got)
	}

	// MSH keeps standard HL7 numbering: MSH.9 is the message type.
	if got := root.Path("MSH/MSH.9/MSH.9.2").Value(); got != "A01" {
		t.Errorf("MSH.9.2 = %q, want A01", got)
	}
	if got := root.Path("MSH/MSH.3/MSH.3.1").Value(); got != "SENDAPP" {
		t.Errorf("MSH.3.1 = %q, want SENDAPP", got)
	}

	// MSH.1 and MSH.2 are the delimiters, written as plain text with no
	// component wrapping, because splitting them on themselves is meaningless.
	if got := root.Path("MSH/MSH.1").Value(); got != "|" {
		t.Errorf("MSH.1 = %q, want |", got)
	}
	if got := root.Path("MSH/MSH.2").Value(); got != `^~\&` {
		t.Errorf("MSH.2 = %q, want ^~\\&", got)
	}
	if n := root.Path("MSH/MSH.1"); n != nil && len(n.Children) != 0 {
		t.Errorf("MSH.1 has %d children, want 0", len(n.Children))
	}

	// A repetition is a repeated element, which is what lets a script index it.
	pid := root.First("PID")
	if got := pid.Count("PID.3"); got != 2 {
		t.Fatalf("PID.3 repetitions = %d, want 2", got)
	}
	if got := pid.Child("PID.3", 0).First("PID.3.1").Value(); got != "MRN9" {
		t.Errorf("first PID.3.1 = %q, want MRN9", got)
	}
	if got := pid.Child("PID.3", 1).First("PID.3.5").Value(); got != "SS" {
		t.Errorf("second PID.3.5 = %q, want SS", got)
	}
}

// TestRoundTrip is the property that matters: converting to XML and back must
// not change the message. Anything else means a transformation that touches one
// field silently rewrites others.
func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"sample", sample},
		{"minimal", "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\r"},
		{"empty middle fields", "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rPID|1||MRN|||||F\r"},
		{"subcomponents", "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rPID|1||MRN^^^FAC&SUB&MORE^MR\r"},
		{"repetitions", "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rPID|1||A~B~C\r"},
		{"escaped delimiters", "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rNTE|1||Value with \\F\\ pipe and \\S\\ caret\r"},
		{"z segment", "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rZPD|1|custom^data\r"},
		{"numeric segment", "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rZ01|x\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := FromRaw([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			out, err := ToER7(root, DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.raw {
				t.Errorf("round trip changed the message:\n in: %q\nout: %q", tc.raw, string(out))
			}
		})
	}
}

// TestRoundTripCustomSeparators checks that a message using non-standard
// delimiters comes back with its own, not the defaults. A sender that uses ! as
// its field separator is unusual but legal, and rewriting its delimiters while
// leaving MSH-1 alone produces a message nothing can parse.
func TestRoundTripCustomSeparators(t *testing.T) {
	raw := "MSH!@~\\&!A!B!C!D!20260101!!ADT@A01!1!P!2.5.1\rPID!1!!MRN@@@FAC@MR\r"
	root, err := FromRaw([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := root.Path("MSH/MSH.9/MSH.9.2").Value(); got != "A01" {
		t.Errorf("MSH.9.2 = %q, want A01", got)
	}
	out, err := ToER7(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != raw {
		t.Errorf("round trip changed the message:\n in: %q\nout: %q", raw, string(out))
	}
}

// TestMutationThenEncode is the transformation path: change the tree, get valid
// HL7 out.
func TestMutationThenEncode(t *testing.T) {
	root, err := FromRaw([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}

	// Overwrite an existing value.
	root.Path("PID/PID.5/PID.5.1").SetText("Smith")

	// Create a field that was not there, several past the end.
	pid := root.First("PID")
	pid.Ensure("PID.11", 0).Ensure("PID.11.1", 0).SetText("1 Main St")

	out, err := ToER7(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	if !strings.Contains(got, "|Smith^Jane^Q^^Ms.|") {
		t.Errorf("family name was not replaced: %q", got)
	}
	// The gap between PID-8 and PID-11 must be preserved as empty fields, or
	// every field after it shifts and the message means something else.
	if !strings.Contains(got, "|F|||1 Main St\r") {
		t.Errorf("new field landed in the wrong position: %q", got)
	}

	// The result must still parse, and parse to the same values.
	reparsed, err := hl7.Parse(out)
	if err != nil {
		t.Fatalf("mutated message no longer parses: %v", err)
	}
	if got := reparsed.MustGet("PID-11.1"); got != "1 Main St" {
		t.Errorf("PID-11.1 = %q, want 1 Main St", got)
	}
	if got := reparsed.MustGet("PID-8"); got != "F" {
		t.Errorf("PID-8 = %q, want F; fields shifted", got)
	}
}

// TestEnsureCreatesGaps pins the decision that assigning to the third repetition
// creates three, rather than compacting. Compacting would put an identifier in
// the wrong slot, which nobody notices until a human reads the chart.
func TestEnsureCreatesGaps(t *testing.T) {
	root := xtree.New(Root)
	pid := root.Append(xtree.New("PID"))
	pid.Ensure("PID.3", 2).Ensure("PID.3.1", 0).SetText("third")

	if got := pid.Count("PID.3"); got != 3 {
		t.Errorf("PID.3 count = %d, want 3", got)
	}
	if got := pid.Child("PID.3", 2).First("PID.3.1").Value(); got != "third" {
		t.Errorf("third repetition = %q, want third", got)
	}
}

// TestAppendCopiesAttachedNode pins E4X's behaviour: appending a node that
// already has a parent copies it. A script moving a segment from the inbound to
// the outbound message almost never means to delete it from the input.
func TestAppendCopiesAttachedNode(t *testing.T) {
	src, err := FromRaw([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	dst := xtree.New(Root)
	pid := src.First("PID")
	dst.Append(pid)

	if src.First("PID") == nil {
		t.Error("appending to another tree removed the segment from the source")
	}
	if dst.First("PID") == pid {
		t.Error("the node was moved rather than copied")
	}
	if got := dst.Path("PID/PID.5/PID.5.1").Value(); got != "Doe" {
		t.Errorf("copy lost its content: %q", got)
	}
}

func TestStripEmptyTrailing(t *testing.T) {
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5.1\rPID|1|||||||\r"
	root, err := FromRaw([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.StripEmptyTrailing = false
	loose, err := ToER7(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loose), "PID|1|||||||") {
		t.Errorf("without stripping, trailing fields should remain: %q", string(loose))
	}

	opts.StripEmptyTrailing = true
	tight, err := ToER7(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tight), "PID|1\r") {
		t.Errorf("with stripping, trailing empties should go: %q", string(tight))
	}
}

func TestRejectsEmptyTree(t *testing.T) {
	if _, err := ToER7(xtree.New(Root), DefaultOptions()); err == nil {
		t.Error("a tree with no segments should not encode to a message")
	}
	if _, err := ToER7(nil, DefaultOptions()); err == nil {
		t.Error("a nil tree should be an error")
	}
}

// TestXMLTextRoundTrip checks the tree survives being serialised to XML and read
// back, which is what happens when a script hands a document to another step.
func TestXMLTextRoundTrip(t *testing.T) {
	root, err := FromRaw([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	data := root.Marshal(0)

	reread, err := xtree.Parse(data)
	if err != nil {
		t.Fatalf("could not reparse our own XML: %v\n%s", err, data)
	}
	out, err := ToER7(reread, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != sample {
		t.Errorf("XML round trip changed the message:\n in: %q\nout: %q", sample, string(out))
	}
}
