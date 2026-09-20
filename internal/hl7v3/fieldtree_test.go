package hl7v3

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// TestEveryGeneratedPathResolvesBackToItsOwnValue is the property the picker lives or dies on.
//
// A picker exists so nobody has to write a path by hand. If it hands somebody a path that does not resolve, or resolves to a
// different field, it is worse than no picker at all - they will paste it into a channel, the channel will run, and the filter will
// match everything or nothing with no error anywhere.
//
// So: walk the whole tree, take every path it generated, resolve it against the same message, and require the value that comes back
// to be the value the picker displayed.
func TestEveryGeneratedPathResolvesBackToItsOwnValue(t *testing.T) {
	root := pathTree(t)

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var checked int

	var walk func(FieldNode)
	walk = func(n FieldNode) {
		if n.Value != "" {
			checked++
			p, err := ParsePath(n.Path)
			if err != nil {
				t.Errorf("the picker produced %q for %s, which does not parse: %v", n.Path, n.Name, err)
			} else if got, ok := p.Value(root); !ok || got != n.Value {
				t.Errorf("%s: the picker showed %q at path %q, but that path resolves to %q (found=%v)",
					n.Name, n.Value, n.Path, got, ok)
			}
		}

		for _, attr := range n.Attributes {
			checked++
			p, err := ParsePath(attr.Path)
			if err != nil {
				t.Errorf("the picker produced %q, which does not parse: %v", attr.Path, err)

				continue
			}
			got, ok := p.Value(root)
			if !ok || got != attr.Value {
				t.Errorf("%s@%s: the picker showed %q at path %q, but that path resolves to %q (found=%v)",
					n.Name, attr.Name, attr.Value, attr.Path, got, ok)
			}
		}

		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(tree)

	// A test that walked nothing would pass. The sample has two identifiers, two names, three given/family parts, an
	// address and several timestamps, so the real figure is well above this.
	if checked < 15 {
		t.Errorf("only %d paths were checked, which suggests the tree was not built", checked)
	}
}

// TestARepeatedFieldGetsDistinctWorkingPaths covers the case that would break a naive picker.
//
// Two identifiers on a patient are the medical record number and the national identifier. A picker that gave both the same path
// would hand somebody a channel that routes on whichever came first.
func TestARepeatedFieldGetsDistinctWorkingPaths(t *testing.T) {
	root := pathTree(t)

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Collect the extension attributes of the patient's identifiers.
	var extensions []FieldAttribute
	for _, n := range FlattenFieldTree(tree) {
		if n.Name != "id" {
			continue
		}
		for _, attr := range n.Attributes {
			if attr.Name == "extension" {
				extensions = append(extensions, attr)
			}
		}
	}

	// Exactly three elements are named id in the sample: the message identifier and the patient's two. queryId and
	// interactionId are different element names, which is worth stating because assuming otherwise is what made the
	// first version of this assertion wrong.
	if len(extensions) != 3 {
		t.Fatalf("expected the message identifier and the patient's two, got %d: %v", len(extensions), extensions)
	}

	// Every path has to be distinct, and every path has to resolve to the value it was shown with.
	seen := map[string]string{}
	for _, attr := range extensions {
		if prior, dup := seen[attr.Path]; dup {
			t.Errorf("path %q was given for both %q and %q", attr.Path, prior, attr.Value)
		}
		seen[attr.Path] = attr.Value

		p, err := ParsePath(attr.Path)
		if err != nil {
			t.Errorf("%q does not parse: %v", attr.Path, err)

			continue
		}
		if got, _ := p.Value(root); got != attr.Value {
			t.Errorf("%q resolves to %q, not the %q it was shown with", attr.Path, got, attr.Value)
		}
	}

	// And the two patient identifiers specifically must be reachable separately, since that is the case with clinical
	// consequences.
	var patientIDs []string
	for _, v := range seen {
		if v == "PIX1234" || v == "999887777" {
			patientIDs = append(patientIDs, v)
		}
	}
	if len(patientIDs) != 2 {
		t.Errorf("the two patient identifiers were not both individually addressable: %v", seen)
	}
}

// TestAUniqueNameGetsAShortPath covers readability.
//
// An anchored path to a birth date in a PDQ response is seven steps of envelope before anything clinical. If the picker always
// produced those, people would rewrite them by hand and make mistakes doing it.
func TestAUniqueNameGetsAShortPath(t *testing.T) {
	root := pathTree(t)

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	paths := map[string]string{}
	for _, n := range FlattenFieldTree(tree) {
		for _, attr := range n.Attributes {
			paths[n.Name+"@"+attr.Name] = attr.Path
		}
		if n.Value != "" {
			paths[n.Name] = n.Path
		}
	}

	// birthTime appears once in this message, so it should be addressable the short way.
	if got := paths["birthTime@value"]; got != "//birthTime@value" {
		t.Errorf("birthTime got the path %q, want the short form", got)
	}
	if got := paths["administrativeGenderCode@code"]; got != "//administrativeGenderCode@code" {
		t.Errorf("gender got the path %q, want the short form", got)
	}

	// id appears several times, so it must NOT get the short form - //id would match the message identifier.
	if got := paths["id@extension"]; strings.HasPrefix(got, "//id") {
		t.Errorf("a repeated element got the short path %q, which would match the wrong one", got)
	}
}

// TestAShortPathIsOnlyUsedWhenItCannotReachTheWrongElement states the actual safety condition.
//
// The first version of this test banned the short form for any repeated name, which was too strict and had to be relaxed - two
// given names are the only two given elements in the document, so //given(2) is exactly as precise as the anchored path and a great
// deal shorter.
//
// The real condition is narrower than "unique" and wider than "never repeated": a descendant search must not be able to reach an
// element of that name outside this sibling group. So the count in the whole document has to equal the number of siblings.
//
// The instructive case is id in a PDQ response. It appears three times, two of them siblings, because the message carries one of its
// own - so //id(1) would be the message identifier rather than the patient's, and it stays anchored.
func TestAShortPathIsOnlyUsedWhenItCannotReachTheWrongElement(t *testing.T) {
	root := pathTree(t)

	counts := map[string]int{}
	countNames(root, counts)

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var shortForms int

	var walk func(FieldNode)
	walk = func(n FieldNode) {
		if strings.HasPrefix(n.Path, "//") {
			shortForms++

			// Either the only one of its name, or the whole set of that name is this sibling group.
			siblings := n.SiblingCount
			if siblings == 0 {
				siblings = 1
			}
			if counts[n.Name] != siblings {
				t.Errorf("%s got the short path %q, but the name appears %d times in the document and "+
					"only %d of those are siblings - so a descendant search can reach the wrong one",
					n.Name, n.Path, counts[n.Name], siblings)
			}
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(tree)

	if shortForms == 0 {
		t.Error("no short paths were produced at all, so this test proves nothing")
	}

	// The patient identifiers specifically must not be short, since that is the case with clinical consequences.
	for _, n := range FlattenFieldTree(tree) {
		if n.Name == "id" && strings.HasPrefix(n.Path, "//") {
			t.Errorf("an identifier got the short path %q, and //id reaches the message identifier first", n.Path)
		}
	}
}

// TestTheShortFormIsUsedWhereItHelpsMost covers the readability win, so the rule cannot be quietly tightened back.
//
// Without this, somebody could make fieldPath always anchor - every path would still be correct, every other test would still pass,
// and the picker would produce hundred-character paths that people rewrite by hand and get wrong.
//
// Its own sample, deliberately. The larger PDQ sample contains a second name and a family name inside the query criteria, so given
// appears three times across two sibling groups and family appears three times - which means the short form correctly does not
// apply there. Reusing that sample would have made this test assert the opposite of what it is for.
func TestTheShortFormIsUsedWhereItHelpsMost(t *testing.T) {
	// One name with two given parts, and nothing else that shares those element names.
	const simple = `<PRPA_IN201301UV02 xmlns="urn:hl7-org:v3">
	  <controlActProcess><subject><registrationEvent><subject1><patient>
	    <patientPerson>
	      <name><given>Rosalind</given><given>Marie</given><family>Okonkwo-Hale</family></name>
	      <birthTime value="19551014"/>
	      <addr><city>Birmingham</city></addr>
	    </patientPerson>
	  </patient></subject1></registrationEvent></subject></controlActProcess>
	</PRPA_IN201301UV02>`

	root, err := xtree.Parse([]byte(simple))
	if err != nil {
		t.Fatal(err)
	}

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	paths := map[string]string{}
	for _, n := range FlattenFieldTree(tree) {
		if n.Value != "" {
			paths[n.Value] = n.Path
		}
		for _, attr := range n.Attributes {
			paths[attr.Value] = attr.Path
		}
	}

	for value, want := range map[string]string{
		// Two given names, both siblings and the only two in the document, so the occurrence is enough.
		"Rosalind": "//given(1)",
		"Marie":    "//given(2)",
		// Unique names need no occurrence at all.
		"Okonkwo-Hale": "//family",
		"Birmingham":   "//city",
		"19551014":     "//birthTime@value",
	} {
		if got := paths[value]; got != want {
			t.Errorf("%q got the path %q, want %q", value, got, want)
		}
	}

	// And every one of those still has to resolve to what it was shown with, or short and wrong is worse than long.
	for value, path := range paths {
		p, err := ParsePath(path)
		if err != nil {
			t.Errorf("%q does not parse: %v", path, err)

			continue
		}
		if got, _ := p.Value(root); got != value {
			t.Errorf("%q was shown for %q but resolves to %q", path, value, got)
		}
	}
}

// TestANullFlavorIsLiftedOutOfTheAttributes covers the presentation decision.
//
// In v3 nullFlavor is not one attribute among several. It is the difference between "nobody recorded this" and "the patient
// declined to say", and leaving it among root and codeSystem buries the thing that changes what an absent value means.
func TestANullFlavorIsLiftedOutOfTheAttributes(t *testing.T) {
	xmlText := `<msg xmlns="urn:hl7-org:v3">
	  <patientPerson>
	    <birthTime nullFlavor="ASKU"/>
	    <administrativeGenderCode nullFlavor="MSK" codeSystem="2.16.840.1.113883.5.1"/>
	  </patientPerson>
	</msg>`

	root, err := xtree.Parse([]byte(xmlText))
	if err != nil {
		t.Fatal(err)
	}

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var found int
	for _, n := range FlattenFieldTree(tree) {
		switch n.Name {
		case "birthTime":
			found++
			if n.NullFlavor != "ASKU" {
				t.Errorf("birthTime null flavour = %q, want ASKU", n.NullFlavor)
			}
			for _, attr := range n.Attributes {
				if attr.Name == "nullFlavor" {
					t.Error("nullFlavor is still in the ordinary attribute list as well")
				}
			}
		case "administrativeGenderCode":
			found++
			if n.NullFlavor != "MSK" {
				t.Errorf("gender null flavour = %q, want MSK", n.NullFlavor)
			}
			// Its other attributes must survive being separated from the flavour.
			var hasSystem bool
			for _, attr := range n.Attributes {
				if attr.Name == "codeSystem" {
					hasSystem = true
				}
			}
			if !hasSystem {
				t.Error("lifting nullFlavor out also dropped codeSystem")
			}
		}
	}
	if found != 2 {
		t.Errorf("found %d of the two null-flavoured elements", found)
	}
}

// TestOccurrenceIsReportedOnlyWhenThereIsAChoice covers what an interface displays.
func TestOccurrenceIsReportedOnlyWhenThereIsAChoice(t *testing.T) {
	root := pathTree(t)

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range FlattenFieldTree(tree) {
		switch {
		case n.SiblingCount == 0 && n.Occurrence != 0:
			// "1 of 0" or a lone field labelled "1 of 1" is noise that makes somebody look for the other one.
			t.Errorf("%s has no siblings but reports occurrence %d", n.Name, n.Occurrence)
		case n.SiblingCount > 0 && n.Occurrence == 0:
			t.Errorf("%s reports %d siblings but no occurrence", n.Name, n.SiblingCount)
		case n.SiblingCount > 0 && n.Occurrence > n.SiblingCount:
			t.Errorf("%s reports being %d of %d", n.Name, n.Occurrence, n.SiblingCount)
		}
	}

	// The patient's two identifiers specifically should be labelled 1 of 2 and 2 of 2.
	var labels []string
	for _, n := range FlattenFieldTree(tree) {
		if n.Name == "id" && n.SiblingCount == 2 {
			labels = append(labels, n.Path)
		}
	}
	if len(labels) != 2 {
		t.Errorf("the patient's two identifiers were not both labelled as a pair: %v", labels)
	}
}

// TestClinicalFieldsAreMarkedInteresting covers the promotion.
//
// A v3 message is mostly envelope. Somebody opening a picker is nearly always after a demographic or an identifier, and making them
// scroll past controlActProcess and subject1 to find a birth date is the difference between a useful tool and a toy.
func TestClinicalFieldsAreMarkedInteresting(t *testing.T) {
	root := pathTree(t)

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	marked := map[string]bool{}
	for _, n := range FlattenFieldTree(tree) {
		if n.Interesting {
			marked[n.Name] = true
		}
	}

	for _, want := range []string{"birthTime", "administrativeGenderCode", "given", "family", "city", "telecom"} {
		if !marked[want] {
			t.Errorf("%s was not marked interesting", want)
		}
	}

	// And the envelope is not promoted, or promotion means nothing.
	var walk func(FieldNode)
	walk = func(n FieldNode) {
		if n.Name == "controlActProcess" || n.Name == "subject1" || n.Name == "registrationEvent" {
			if n.Interesting {
				t.Errorf("%s was marked interesting, so the marking no longer distinguishes anything", n.Name)
			}
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(tree)
}

// TestFlatteningDropsPureStructure covers the search list.
func TestFlatteningDropsPureStructure(t *testing.T) {
	root := pathTree(t)

	tree, err := BuildFieldTree(root, FieldTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	flat := FlattenFieldTree(tree)
	if len(flat) == 0 {
		t.Fatal("flattening produced nothing")
	}

	for _, n := range flat {
		// Every entry has to be something a person can read a value from. A search result you cannot use is noise,
		// and the tree is already there for understanding the shape.
		if n.Value == "" && len(n.Attributes) == 0 && n.NullFlavor == "" {
			t.Errorf("%s has nothing to read but appears in the flat list", n.Name)
		}
		if len(n.Children) != 0 {
			t.Errorf("%s kept its children in the flat list", n.Name)
		}
	}

	// subject1 and registrationEvent are pure structure and must not be there.
	for _, n := range flat {
		if n.Name == "subject1" || n.Name == "registrationEvent" {
			t.Errorf("%s is pure structure and appears in the flat list", n.Name)
		}
	}
}

// TestTheTreeIsBoundedSoAMessageCannotExhaustTheBrowser covers the limits.
func TestTheTreeIsBoundedSoAMessageCannotExhaustTheBrowser(t *testing.T) {
	// A wide message: one parent with many children.
	var b strings.Builder
	b.WriteString(`<msg xmlns="urn:hl7-org:v3"><patientPerson>`)
	for i := 0; i < 500; i++ {
		b.WriteString(`<telecom value="tel:+1205555" use="HP"/>`)
	}
	b.WriteString(`</patientPerson></msg>`)

	root, err := xtree.Parse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}

	tree, err := BuildFieldTree(root, FieldTreeOptions{MaxNodes: 50})
	if err != nil {
		t.Fatal(err)
	}

	if got := len(FlattenFieldTree(tree)); got > 60 {
		t.Errorf("the node budget was not honoured: %d entries", got)
	}

	// And a deep message.
	deep := strings.Repeat(`<a>`, 200) + `<b value="x"/>` + strings.Repeat(`</a>`, 200)
	deepRoot, err := xtree.Parse([]byte(`<msg xmlns="urn:hl7-org:v3">` + deep + `</msg>`))
	if err != nil {
		// xtree refuses very deep documents of its own accord, which is also an acceptable answer.
		return
	}

	if _, err := BuildFieldTree(deepRoot, FieldTreeOptions{MaxDepth: 10}); err != nil {
		t.Errorf("a deep message produced an error rather than a truncated tree: %v", err)
	}
}

// TestNoMessageIsRefusedClearly covers the empty case.
func TestNoMessageIsRefusedClearly(t *testing.T) {
	if _, err := BuildFieldTree(nil, FieldTreeOptions{}); err == nil {
		t.Error("building a tree from nothing succeeded")
	}
}
