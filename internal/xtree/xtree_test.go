package xtree

import (
	"strings"
	"testing"
)

// The tree is shared by the HL7 XML view, the declarative transformation layer
// and the E4X script runtime. All three mutate it, so the behaviour that matters
// here is what happens at the edges: appending a node that already has a parent,
// addressing a repetition that does not exist yet, and reading a document from
// outside the organisation.

func TestParseAndSerialiseRoundTrip(t *testing.T) {
	src := `<HL7Message><MSH><MSH.1>|</MSH.1><MSH.9><MSH.9.1>ADT</MSH.9.1>` +
		`<MSH.9.2>A01</MSH.9.2></MSH.9></MSH></HL7Message>`

	root, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if root.Name != "HL7Message" {
		t.Errorf("root = %q", root.Name)
	}

	got := string(root.Marshal(0))
	if got != src {
		t.Errorf("round trip changed the document:\n got %s\nwant %s", got, src)
	}
}

func TestFindIsBreadthFirst(t *testing.T) {
	// Two nodes share a name at different depths. Breadth-first has to return the
	// shallower one, because a script asking for a segment means the one in the
	// message, not one nested inside another element.
	root, err := Parse([]byte(
		`<root><wrapper><target>deep</target></wrapper><target>shallow</target></root>`))
	if err != nil {
		t.Fatal(err)
	}

	found := root.Find("target")
	if found == nil {
		t.Fatal("Find returned nothing")
	}
	if found.Text != "shallow" {
		t.Errorf("Find should return the shallower node, got %q", found.Text)
	}
}

func TestDescendantsFindsEveryDepth(t *testing.T) {
	root, err := Parse([]byte(
		`<root><a><OBX>1</OBX></a><OBX>2</OBX><b><c><OBX>3</OBX></c></b></root>`))
	if err != nil {
		t.Fatal(err)
	}

	got := root.Descendants("OBX")
	if len(got) != 3 {
		t.Fatalf("want 3 descendants, got %d", len(got))
	}
	// Order matters: a script iterating OBX segments expects document order,
	// because that is the order the results were reported in.
	var texts []string
	for _, n := range got {
		texts = append(texts, n.Text)
	}
	if strings.Join(texts, ",") != "1,2,3" {
		t.Errorf("descendants should come back in document order, got %v", texts)
	}
}

func TestDescendantsWildcardReturnsEverything(t *testing.T) {
	root, _ := Parse([]byte(`<root><a><b/></a><c/></root>`))
	if got := len(root.Descendants("*")); got != 3 {
		t.Errorf("the wildcard should match every descendant, got %d", got)
	}
}

func TestAppendCopiesANodeThatAlreadyHasAParent(t *testing.T) {
	// This is E4X semantics and it matters. A script appending an inbound segment
	// to the outbound message almost never means "and remove it from the input",
	// but a move would do exactly that and the loss would be silent.
	source, _ := Parse([]byte(`<in><OBX>result</OBX></in>`))
	target, _ := Parse([]byte(`<out/>`))

	obx := source.Find("OBX")
	target.Append(obx)

	if source.Find("OBX") == nil {
		t.Error("appending should not have removed the node from its original parent")
	}
	if target.Find("OBX") == nil {
		t.Fatal("the node was not appended")
	}
	// And it has to be a copy, not an alias, or editing one would edit both.
	target.Find("OBX").Text = "changed"
	if source.Find("OBX").Text != "result" {
		t.Error("the append shared state with the original instead of copying")
	}
}

func TestAppendTakesOwnershipOfAnOrphan(t *testing.T) {
	// A freshly built node has no parent, so there is nothing to protect and it
	// should be adopted rather than copied.
	target, _ := Parse([]byte(`<out/>`))
	orphan := &Node{Name: "ZAB", Text: "1"}
	target.Append(orphan)

	if orphan.Parent() != target {
		t.Error("an orphan should be adopted, not copied")
	}
}

func TestEnsureCreatesGapsRatherThanCompacting(t *testing.T) {
	// Asking for the third repetition when none exist has to produce three, with
	// the first two empty. Compacting to one would mean a script writing to
	// repetition 3 silently wrote to repetition 1, and the value would end up in
	// the wrong place with nothing to show it moved.
	root := &Node{Name: "PID"}
	third := root.Ensure("PID.3", 2)
	if third == nil {
		t.Fatal("Ensure returned nothing")
	}
	third.Text = "MRN3"

	all := root.Children
	if len(all) != 3 {
		t.Fatalf("want three repetitions, got %d", len(all))
	}
	if all[0].Text != "" || all[1].Text != "" {
		t.Error("the skipped repetitions should be empty")
	}
	if all[2].Text != "MRN3" {
		t.Errorf("the value landed in the wrong repetition: %q", all[2].Text)
	}
}

func TestEnsureReturnsTheExistingNodeWhenItIsThere(t *testing.T) {
	root := &Node{Name: "PID"}
	first := root.Ensure("PID.3", 0)
	first.Text = "MRN1"

	again := root.Ensure("PID.3", 0)
	if again != first {
		t.Error("Ensure should return the existing node, not add another")
	}
	if len(root.Children) != 1 {
		t.Errorf("Ensure added a duplicate; children = %d", len(root.Children))
	}
}

func TestCloneIsDeepAndUnparented(t *testing.T) {
	root, _ := Parse([]byte(`<PID><PID.3><PID.3.1>MRN1</PID.3.1></PID.3></PID>`))
	clone := root.Clone()

	if clone.Parent() != nil {
		t.Error("a clone should have no parent")
	}

	// Mutating the clone must not reach the original, at any depth.
	clone.Find("PID.3.1").Text = "MRN2"
	if root.Find("PID.3.1").Text != "MRN1" {
		t.Error("the clone shares nodes with the original")
	}

	clone.Attrs = append(clone.Attrs, Attr{Name: "added", Value: "x"})
	if len(root.Attrs) != 0 {
		t.Error("the clone shares its attribute slice with the original")
	}
}

func TestPathNavigatesToANode(t *testing.T) {
	root, _ := Parse([]byte(
		`<HL7Message><PID><PID.5><PID.5.1>Frost</PID.5.1></PID.5></PID></HL7Message>`))

	leaf := root.Path("PID/PID.5/PID.5.1")
	if leaf == nil {
		t.Fatal("Path found nothing")
	}
	if leaf.Text != "Frost" {
		t.Errorf("Path landed on the wrong node, text = %q", leaf.Text)
	}

	// A path that does not exist has to come back empty rather than inventing
	// nodes, because Path is also used to test whether a field is present.
	if got := root.Path("PID/PID.99"); got != nil {
		t.Error("Path should not create missing nodes")
	}
}

// TestIndicesAreZeroBased pins the convention explicitly, because it is not the
// one used a layer up: internal/hl7 numbers segment occurrences from 1, and the
// transformation path language written for users numbers repetitions from 1 too.
// Anyone moving between the two will get this wrong once, and a test that states
// it plainly is cheaper than the afternoon spent finding out.
func TestIndicesAreZeroBased(t *testing.T) {
	root, _ := Parse([]byte(`<PID><PID.3>first</PID.3><PID.3>second</PID.3></PID>`))

	if got := root.Child("PID.3", 0); got == nil || got.Text != "first" {
		t.Errorf("index 0 should be the first repetition, got %v", got)
	}
	if got := root.Child("PID.3", 1); got == nil || got.Text != "second" {
		t.Errorf("index 1 should be the second repetition, got %v", got)
	}
	if got := root.Child("PID.3", 2); got != nil {
		t.Error("an index past the end should return nothing, not the last node")
	}
}

func TestPathAddressesARepetition(t *testing.T) {
	root, _ := Parse([]byte(
		`<PID><PID.3><PID.3.1>A</PID.3.1></PID.3><PID.3><PID.3.1>B</PID.3.1></PID.3></PID>`))

	second := root.Path("PID.3[1]/PID.3.1")
	if second == nil {
		t.Fatal("the second repetition was not found")
	}
	if second.Text != "B" {
		t.Errorf("want the second repetition, got %q", second.Text)
	}
}

func TestParseRefusesDeepNesting(t *testing.T) {
	// A document from outside the organisation can nest arbitrarily deep, and a
	// recursive parser meets the stack before it meets the end of the file.
	deep := strings.Repeat("<a>", 500) + "x" + strings.Repeat("</a>", 500)
	if _, err := Parse([]byte(deep)); err == nil {
		t.Fatal("deeply nested input should be refused, not recursed into")
	}
}

func TestParseIsNotVulnerableToEntityExpansion(t *testing.T) {
	// The billion laughs attack. Only the HTML entities are resolved, so a
	// document defining its own entities cannot make the parser expand them into
	// gigabytes.
	bomb := `<?xml version="1.0"?>
<!DOCTYPE lolz [
 <!ENTITY lol "lol">
 <!ENTITY lol1 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
 <!ENTITY lol2 "&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;">
 <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
]>
<root>&lol3;</root>`

	root, err := Parse([]byte(bomb))
	if err != nil {
		// Refusing is a perfectly good outcome.
		return
	}
	// If it parsed, the entity must not have been expanded.
	if len(root.Text) > 1000 {
		t.Errorf("a custom entity was expanded to %d bytes", len(root.Text))
	}
}

func TestParseRejectsMalformedInput(t *testing.T) {
	for _, bad := range []string{
		"",
		"not xml at all",
		"<unclosed>",
		"<a></b>",
		"<a><b></a></b>",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) should have failed", bad)
		}
	}
}

func TestFragmentsPreserveMixedContent(t *testing.T) {
	// CDA narrative is mixed content: text with markup inside it. Flattening it
	// to a text field would run a table's cells together and make two documents
	// impossible to compare.
	root, err := Parse([]byte(`<text>Take <content>two</content> daily</text>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Fragments) == 0 {
		t.Fatal("mixed content produced no fragments")
	}

	var rebuilt strings.Builder
	for _, f := range root.Fragments {
		if f.Node != nil {
			rebuilt.WriteString(f.Node.Text)
		} else {
			rebuilt.WriteString(f.Text)
		}
	}
	if got := rebuilt.String(); got != "Take two daily" {
		t.Errorf("the fragments do not reconstruct the content, got %q", got)
	}
}

func TestNamespaceIsRetained(t *testing.T) {
	// A CDA is namespaced and a script that ignores the namespace would match
	// elements from an embedded foreign document.
	root, err := Parse([]byte(`<ClinicalDocument xmlns="urn:hl7-org:v3"><id/></ClinicalDocument>`))
	if err != nil {
		t.Fatal(err)
	}
	if root.Namespace != "urn:hl7-org:v3" {
		t.Errorf("namespace = %q", root.Namespace)
	}
}

func TestSetParentKeepsTheTreeConsistent(t *testing.T) {
	parent := &Node{Name: "PID"}
	child := &Node{Name: "PID.3"}
	child.SetParent(parent)

	if child.Parent() != parent {
		t.Error("SetParent did not take")
	}
}

func TestMarshalEscapesContentThatWouldBreakTheDocument(t *testing.T) {
	// HL7 field values contain ampersands routinely: it is the subcomponent
	// separator. Emitting one raw would produce a document that will not reparse.
	root := &Node{Name: "PID.5", Text: `Smith & Sons <test> "quoted"`}
	out := string(root.Marshal(0))

	if strings.Contains(out, "& ") {
		t.Errorf("the ampersand was not escaped: %s", out)
	}
	back, err := Parse([]byte(out))
	if err != nil {
		t.Fatalf("the output does not reparse: %v", err)
	}
	if back.Text != root.Text {
		t.Errorf("round trip changed the text: %q -> %q", root.Text, back.Text)
	}
}
