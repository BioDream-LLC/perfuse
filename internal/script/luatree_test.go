package script

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// treeFixture is a small document with a repeated element, because repetition is where write semantics get decided.
func treeFixture(t *testing.T) *xtree.Node {
	t.Helper()

	root, err := xtree.Parse([]byte(`<patient>
	  <name><family>OKONKWO</family><given>ADAEZE</given></name>
	  <id extension="MRN001" root="1.2.3"/>
	  <id extension="MRN002" root="4.5.6"/>
	  <birthTime value="19551014"/>
	</patient>`))
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}

	return root
}

// runLuaOn runs a Lua transformer against a tree and returns the tree, mutated in place.
func runLuaOn(t *testing.T, root *xtree.Node, source string) *xtree.Node {
	t.Helper()

	e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
	s, err := e.CompileIn("tree.lua", source, Transformer, Lua)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	if _, err := e.Run(s, &Context{Message: root}); err != nil {
		t.Fatalf("running: %v", err)
	}

	return root
}

func TestLuaCanReadTheTree(t *testing.T) {
	root := treeFixture(t)

	res, err := func() (Result, error) {
		e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
		s, cerr := e.CompileIn("read.lua", `
			local family = msg.child("name").child("family")
			return family.text() == "OKONKWO"
		`, Filter, Lua)
		if cerr != nil {
			return Result{}, cerr
		}

		return e.Run(s, &Context{Message: root})
	}()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("a Lua script could not read nested element text")
	}
}

// TestLuaWritesReachTheTree is the whole point of the change.
//
// Until now the binding was read-only, and said so. A transformer that could inspect a message and not change it is half a
// feature: the declarative steps cover the common edits, and a script exists precisely for the ones they do not.
func TestLuaWritesReachTheTree(t *testing.T) {
	root := runLuaOn(t, treeFixture(t), `
		msg.child("name").child("family").setText("ADEYEMI")
	`)

	got := root.First("name").First("family").Text
	if got != "ADEYEMI" {
		t.Errorf("the family name is %q after the script wrote it, so the write did not reach the tree Go holds", got)
	}
}

// TestReadingBackAWriteSeesTheNewValue is the bug the first version of the binding would have had.
//
// name and text were table *values*, snapshotted when the node was exposed. After setText the stale copy still held the old
// text, so a script that wrote a field and read it back got what it had replaced - with nothing erroring and the script looking
// correct. Making them functions is what fixes it, and this is the test that would have caught it.
func TestReadingBackAWriteSeesTheNewValue(t *testing.T) {
	root := treeFixture(t)

	e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
	s, err := e.CompileIn("readback.lua", `
		local family = msg.child("name").child("family")
		family.setText("NAKAMURA")
		return family.text() == "NAKAMURA"
	`, Filter, Lua)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.Run(s, &Context{Message: root})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("a script read back the value it had just written and saw the old one, which means the accessors are snapshots rather than views")
	}
}

func TestLuaCanWriteAndRemoveAttributes(t *testing.T) {
	root := runLuaOn(t, treeFixture(t), `
		msg.child("birthTime").setAttr("value", "19600101")
		msg.child("id").removeAttr("root")
	`)

	if got := root.First("birthTime").AttrValue("value"); got != "19600101" {
		t.Errorf("birthTime value is %q", got)
	}
	if _, ok := root.First("id").Attr("root"); ok {
		t.Error("the root attribute was not removed")
	}
}

// TestAnAbsentAttributeIsNilAndNotEmpty keeps the distinction the filter grammar needs.
//
// In XML an absent element and one carrying "" are different clinical facts. Returning "" for both is the mistake the v3 filter
// made for months, and a script that cannot tell them apart cannot make the decision either.
func TestAnAbsentAttributeIsNilAndNotEmpty(t *testing.T) {
	root := treeFixture(t)

	e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
	s, err := e.CompileIn("absent.lua", `
		return msg.child("birthTime").attr("nullFlavor") == nil
	`, Filter, Lua)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.Run(s, &Context{Message: root})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("an absent attribute did not read as nil, so a script cannot tell absent from empty")
	}
}

// TestChildIsOneBasedAndIndexable is the repetition case.
//
// A message repeats elements and "the second identifier" is something scripts need to say. One-based matches the filter paths
// and every conversation about a message.
func TestChildIsOneBasedAndIndexable(t *testing.T) {
	root := treeFixture(t)

	e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
	s, err := e.CompileIn("index.lua", `
		local first = msg.child("id", 1).attr("extension")
		local second = msg.child("id", 2).attr("extension")
		return first == "MRN001" and second == "MRN002"
	`, Filter, Lua)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.Run(s, &Context{Message: root})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("indexed children did not resolve in document order from one")
	}
}

func TestAZeroChildIndexIsRefused(t *testing.T) {
	e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
	s, err := e.CompileIn("zero.lua", `msg.child("id", 0)`, Transformer, Lua)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.Run(s, &Context{Message: treeFixture(t)}); err == nil {
		t.Fatal("index 0 was accepted, which means something different to this code than to whoever wrote it")
	}
}

// TestEnsureCreatesWhatChildWouldNotFind is why the two are separate functions.
//
// Reading a missing element should answer nil; writing to one should make it exist. One function doing both would silently
// create elements during what the author thought was a read.
func TestEnsureCreatesWhatChildWouldNotFind(t *testing.T) {
	root := runLuaOn(t, treeFixture(t), `
		if msg.child("telecom") == nil then
			msg.ensure("telecom").setAttr("value", "tel:555-0100")
		end
	`)

	tel := root.First("telecom")
	if tel == nil {
		t.Fatal("ensure did not create the element")
	}
	if got := tel.AttrValue("value"); got != "tel:555-0100" {
		t.Errorf("the created element carries %q", got)
	}
}

// TestEnsureCreatesTheGapRatherThanCompacting is the corruption case xtree.Ensure documents.
//
// A script assigning to the third repetition when one exists means the third. Compacting would put the value in the second,
// and in an identifier list that is a corruption nobody notices until a human reads it.
func TestEnsureCreatesTheGapRatherThanCompacting(t *testing.T) {
	root := runLuaOn(t, treeFixture(t), `
		msg.ensure("id", 4).setAttr("extension", "MRN004")
	`)

	ids := root.All("id")
	if len(ids) != 4 {
		t.Fatalf("got %d id elements, want 4 - the gap was compacted", len(ids))
	}
	if got := ids[3].AttrValue("extension"); got != "MRN004" {
		t.Errorf("the fourth id carries %q", got)
	}
	// The two that existed must be untouched and in place.
	if got := ids[0].AttrValue("extension"); got != "MRN001" {
		t.Errorf("the first id was disturbed: %q", got)
	}
	// The third is the created gap, and carries nothing rather than a copy of anything.
	if got := ids[2].AttrValue("extension"); got != "" {
		t.Errorf("the gap element carries %q, which means something was copied into it", got)
	}
}

func TestLuaCanAppendAndRemoveElements(t *testing.T) {
	root := runLuaOn(t, treeFixture(t), `
		msg.append("note", "added by a script")
		msg.remove("birthTime")
	`)

	note := root.First("note")
	if note == nil || note.Text != "added by a script" {
		t.Error("append did not add an element with its text")
	}
	if root.First("birthTime") != nil {
		t.Error("remove did not delete the element")
	}
}

func TestRemoveReportsHowManyItRemoved(t *testing.T) {
	root := treeFixture(t)

	e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
	s, err := e.CompileIn("count.lua", `return msg.remove("id") == 2`, Filter, Lua)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.Run(s, &Context{Message: root})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("remove did not report the number of elements it deleted")
	}
}

// TestChildrenCanBeFilteredByName covers the iteration case, which is how a script walks repetitions.
func TestChildrenCanBeFilteredByName(t *testing.T) {
	root := treeFixture(t)

	e := New(Options{Timeout: 2 * 1000 * 1000 * 1000})
	s, err := e.CompileIn("walk.lua", `
		local seen = 0
		for _, id in ipairs(msg.children("id")) do
			if id.attr("extension") ~= nil then seen = seen + 1 end
		end
		return seen == 2
	`, Filter, Lua)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.Run(s, &Context{Message: root})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("iterating children filtered by name did not find both identifiers")
	}
}

// TestTheTransformedTreeStillSerialises is the check that a script cannot produce something no parser will read.
//
// A script can set an element name with a space in it. Failing here makes it attributable; failing at the destination looks
// like the receiver's problem.
func TestTheTransformedTreeStillSerialises(t *testing.T) {
	root := runLuaOn(t, treeFixture(t), `
		msg.child("name").child("family").setText("O'BRIEN & SONS <test>")
	`)

	encoded := root.Marshal(2)
	back, err := xtree.Parse(encoded)
	if err != nil {
		t.Fatalf("the transformed tree does not parse: %v\n%s", err, encoded)
	}

	// The characters that would break XML have to come back as themselves, not as entities left unescaped or double-escaped.
	if got := back.First("name").First("family").Text; got != "O'BRIEN & SONS <test>" {
		t.Errorf("after a round trip the text is %q", got)
	}
	if strings.Contains(string(encoded), "& SONS") && !strings.Contains(string(encoded), "&amp;") {
		t.Error("the ampersand was written raw, which produces XML no parser will read")
	}
}
