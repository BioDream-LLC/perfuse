package hl7v3

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// transformDoc is a small PDQ-shaped document with the nesting real v3 has.
//
// Hand-built rather than a fixture, because these tests are about writing and each one needs to know exactly what was there
// before. The shape matches what the read-side tests use, so a path that works in one file works in the other.
const transformDoc = `<?xml version="1.0"?>
<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.72.5.1" extension="MSG00001"/>
  <controlActProcess>
    <subject>
      <registrationEvent>
        <subject1>
          <patient>
            <id root="2.16.840.1.113883.3.72.5.9.1" extension="MRN12345"/>
            <id root="2.16.840.1.113883.4.1" extension="123456789"/>
            <statusCode code="active"/>
            <patientPerson>
              <name use="L">
                <given>Marie</given>
                <given>Louise</given>
                <family>  Dubois  </family>
              </name>
              <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/>
              <birthTime value="19551014"/>
            </patientPerson>
          </patient>
        </subject1>
      </registrationEvent>
    </subject>
  </controlActProcess>
</PRPA_IN201306UV02>`

func transformTree(t *testing.T) *xtree.Node {
	t.Helper()

	root, err := xtree.Parse([]byte(transformDoc))
	if err != nil {
		t.Fatal(err)
	}

	return root
}

// run compiles and applies a single step, failing the test on any error.
func run(t *testing.T, root *xtree.Node, st Step) {
	t.Helper()

	steps, err := CompileSteps([]Step{st}, nil)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	if err := steps.Apply(root); err != nil {
		t.Fatalf("applying: %v", err)
	}
}

// mustValue reads a path, failing the test when it is absent.
func mustValue(t *testing.T, root *xtree.Node, raw string) string {
	t.Helper()

	p, err := ParsePath(raw)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.ReadValue(root)
	if !ok {
		t.Fatalf("%s has no value", raw)
	}

	return v
}

// TestAValueGoesToAnAttributeRatherThanElementText is the single most important property here.
//
// In v2 a value is text at a position. In v3 it is almost always an attribute, so an implementation carried over from v2
// would write element text and produce a document that looks populated and reads as empty everywhere - and would do it
// silently, because the element would exist and parse.
func TestAValueGoesToAnAttributeRatherThanElementText(t *testing.T) {
	t.Run("a plain value goes to the value attribute", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{Path: "//birthTime", Value: "19600101"}})

		if got := mustValue(t, root, "//birthTime@value"); got != "19600101" {
			t.Errorf("birthTime@value is %q, want the value written", got)
		}
	})

	t.Run("a coded element gets a code attribute", func(t *testing.T) {
		// administrativeGenderCode carries its value in code, not value. Writing the wrong one produces an
		// element a receiver reads as having no code at all.
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{Path: "//administrativeGenderCode", Value: "M"}})

		if got := mustValue(t, root, "//administrativeGenderCode@code"); got != "M" {
			t.Errorf("administrativeGenderCode@code is %q, want M", got)
		}
		if _, present := findNode(t, root, "//administrativeGenderCode").Attr("value"); present {
			t.Error("a value attribute was written onto a coded element as well as the code")
		}
	})

	t.Run("an explicit attribute is honoured literally", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{Path: "//patient/id(1)@extension", Value: "MRN99999"}})

		if got := mustValue(t, root, "//patient/id(1)@extension"); got != "MRN99999" {
			t.Errorf("the first identifier is %q", got)
		}
		// And the second is untouched, because an occurrence means that occurrence.
		if got := mustValue(t, root, "//patient/id(2)@extension"); got != "123456789" {
			t.Errorf("the second identifier changed to %q", got)
		}
	})

	t.Run("an element holding a structure is refused", func(t *testing.T) {
		// patientPerson has children. Writing text into it would discard them, which is a far larger change
		// than the author asked for, so it is refused rather than performed.
		steps, err := CompileSteps([]Step{{Set: &V3SetStep{Path: "//patientPerson", Value: "x"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = steps.Apply(transformTree(t))
		if err == nil {
			t.Fatal("writing a value into an element with children was allowed")
		}
		if !strings.Contains(err.Error(), "structure") {
			t.Errorf("the refusal does not explain itself: %v", err)
		}
	})
}

// TestTheThreeRemovalsAreDifferentStatements is the reason clear, nullflavor and remove are separate steps.
//
// Emptying an element, stating why it is empty, and saying it does not apply are different clinical statements. A receiving
// system does different things with each: ASKU says the gap has been chased once already, an empty element says nothing at
// all, and an absent element says the question does not arise. Collapsing them means a downstream system chases a value
// nobody has, or fills a gap somebody masked deliberately.
func TestTheThreeRemovalsAreDifferentStatements(t *testing.T) {
	t.Run("clear empties the element and leaves it there", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Clear: &V3ClearStep{Path: "//birthTime"}})

		node := findNode(t, root, "//birthTime")
		if node == nil {
			t.Fatal("clear removed the element rather than emptying it")
		}
		if v, present := node.Attr("value"); present {
			t.Errorf("the value attribute survived as %q", v)
		}
		if nf, present := node.Attr("nullFlavor"); present {
			t.Errorf("clear invented a null flavour %q, which claims a reason nobody stated", nf)
		}
	})

	t.Run("nullflavor states a reason and removes the value", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{NullFlavor: &V3NullFlavorStep{Path: "//birthTime", Reason: "ASKU"}})

		node := findNode(t, root, "//birthTime")
		if node == nil {
			t.Fatal("the element was removed rather than null-flavoured")
		}
		if got := node.AttrValue("nullFlavor"); got != "ASKU" {
			t.Errorf("nullFlavor is %q, want ASKU", got)
		}
		if v, present := node.Attr("value"); present {
			t.Errorf("the value %q survived alongside a null flavour, which is a contradiction: the element "+
				"claims both to have a value and to have none", v)
		}
	})

	t.Run("remove deletes the element outright", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Remove: &V3RemoveStep{Path: "//birthTime"}})

		if node := findNode(t, root, "//birthTime"); node != nil {
			t.Error("the element is still present after remove")
		}
	})

	t.Run("writing a value removes an existing null flavour", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{NullFlavor: &V3NullFlavorStep{Path: "//birthTime", Reason: "ASKU"}})
		run(t, root, Step{Set: &V3SetStep{Path: "//birthTime@value", Value: "19600101"}})

		node := findNode(t, root, "//birthTime")
		if nf, present := node.Attr("nullFlavor"); present {
			t.Errorf("nullFlavor %q survived a value being written, so the element says both that it has a "+
				"value and that none is available", nf)
		}
		if got := node.AttrValue("value"); got != "19600101" {
			t.Errorf("value is %q", got)
		}
	})

	t.Run("setting a null-flavoured element through a bare path is refused", func(t *testing.T) {
		// Once the value is gone, so is the only evidence of where it lived. Guessing text would produce
		// <birthTime>19600101</birthTime>, which is not valid v3; guessing an attribute on a name part would
		// produce an element every reader treats as empty. Both are silent here and loud at the receiver.
		root := transformTree(t)
		run(t, root, Step{NullFlavor: &V3NullFlavorStep{Path: "//birthTime", Reason: "ASKU"}})

		steps, err := CompileSteps([]Step{{Set: &V3SetStep{Path: "//birthTime", Value: "19600101"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = steps.Apply(root)
		if err == nil {
			t.Fatal("a bare path wrote to a null-flavoured element, which means guessing where the value goes")
		}
		if !strings.Contains(err.Error(), "@value") {
			t.Errorf("the refusal does not say what to write instead: %v", err)
		}
	})

	t.Run("removing the whole document is refused", func(t *testing.T) {
		steps, err := CompileSteps([]Step{{Remove: &V3RemoveStep{Path: "/PRPA_IN201306UV02"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := steps.Apply(transformTree(t)); err == nil {
			t.Fatal("removing the document element was allowed, which leaves nothing to send")
		}
	})
}

// TestADescendingPathMayUpdateButNotCreate covers the ambiguity that makes writing harder than reading.
//
// // means "search at any depth", which is right for reading and meaningless for creating: there is no answer to where a
// missing element should go. Guessing would put clinical data somewhere plausible and wrong, so it is refused with a
// message saying what to do instead.
func TestADescendingPathMayUpdateButNotCreate(t *testing.T) {
	t.Run("updating something that exists is fine", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{Path: "//birthTime", Value: "19700101"}})

		if got := mustValue(t, root, "//birthTime@value"); got != "19700101" {
			t.Errorf("birthTime is %q", got)
		}
	})

	t.Run("creating through a descending path is refused with advice", func(t *testing.T) {
		steps, err := CompileSteps([]Step{{Set: &V3SetStep{Path: "//deceasedTime", Value: "20260101"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = steps.Apply(transformTree(t))
		if err == nil {
			t.Fatal("a descending path created an element, which means guessing where it belongs")
		}
		if !strings.Contains(err.Error(), "nowhere definite") {
			t.Errorf("the refusal does not explain itself: %v", err)
		}
	})

	t.Run("a descending path matching several elements is refused", func(t *testing.T) {
		// //given matches two. Writing to whichever was found first is how a transformation corrupts a
		// message it appeared to work on.
		steps, err := CompileSteps([]Step{{Set: &V3SetStep{Path: "//given", Value: "Anne"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = steps.Apply(transformTree(t))
		if err == nil {
			t.Fatal("an ambiguous path was written to anyway")
		}
		if !strings.Contains(err.Error(), "does not say which") {
			t.Errorf("the refusal does not explain itself: %v", err)
		}
	})

	t.Run("an occurrence makes it unambiguous", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{Path: "//given(2)", Value: "Anne"}})

		if got := mustValue(t, root, "//given(1)"); got != "Marie" {
			t.Errorf("the first given name changed to %q", got)
		}
	})
}

// TestAnAnchoredPathCreatesWhatItMustAndKeepsRepeatsTogether covers creation.
//
// Element order is significant in a schema-valid v3 document in a way it is not in v2, so where a created element goes
// matters. A repeat has to sit next to its namesakes rather than at the end of the parent, or the document parses and fails
// validation at the far end - which is the worst kind of failure, because it happens at somebody else's site.
func TestAnAnchoredPathCreatesWhatItMustAndKeepsRepeatsTogether(t *testing.T) {
	root := transformTree(t)

	// A third given name, where two exist and family follows them.
	run(t, root, Step{Set: &V3SetStep{
		Path:  "/PRPA_IN201306UV02/controlActProcess/subject/registrationEvent/subject1/patient/patientPerson/name/given(3)",
		Value: "Claire",
	}})

	name := findNode(t, root, "//patientPerson/name")
	if name == nil {
		t.Fatal("the name element is gone")
	}

	var order []string
	for _, c := range name.Children {
		order = append(order, c.Name)
	}

	// given, given, given, family. Appending would have produced given, given, family, given.
	want := []string{"given", "given", "given", "family"}
	if len(order) != len(want) {
		t.Fatalf("the name has children %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("the name has children %v, want %v - a created repeat was appended at the end rather "+
				"than placed with its namesakes, which breaks schema sequence order", order, want)
		}
	}

	if got := mustValue(t, root, "//patientPerson/name/given(3)"); got != "Claire" {
		t.Errorf("the third given name is %q", got)
	}
}

// TestCompileRefusesWhatCannotWork covers the load-time checks.
//
// Every one of these must fail at load rather than on the first message. The operator who deployed the channel is watching
// when it starts and will not be watching at three the following morning.
func TestCompileRefusesWhatCannotWork(t *testing.T) {
	cases := []struct {
		name string
		step Step
		want string
	}{
		{
			name: "no action",
			step: Step{Description: "does nothing"},
			want: "does nothing",
		},
		{
			name: "two actions",
			step: Step{
				Set:   &V3SetStep{Path: "//birthTime", Value: "1"},
				Clear: &V3ClearStep{Path: "//birthTime"},
			},
			want: "more than one action",
		},
		{
			name: "an unparseable path",
			step: Step{Set: &V3SetStep{Path: "//hl7:name", Value: "x"}},
			want: "",
		},
		{
			name: "an empty path",
			step: Step{Set: &V3SetStep{Path: "   ", Value: "x"}},
			want: "empty path",
		},
		{
			name: "a null flavour that is not one",
			step: Step{NullFlavor: &V3NullFlavorStep{Path: "//birthTime", Reason: "ASKUU"}},
			want: "not a null flavour",
		},
		{
			name: "an uncompilable replace pattern",
			step: Step{Replace: &V3ReplaceStep{Path: "//family", From: "([unclosed", To: "x"}},
			want: "not a valid regular expression",
		},
		{
			name: "an unknown on_missing",
			step: Step{Map: &V3MapStep{Path: "//family", Table: "t", OnMissing: "ignore"}},
			want: "keep, clear or fail",
		},
		{
			name: "a map with no table",
			step: Step{Map: &V3MapStep{Path: "//family"}},
			want: "needs a table",
		},
		{
			name: "an unknown case",
			step: Step{Case: &V3CaseStep{Path: "//family", To: "title"}},
			want: "upper or lower",
		},
		{
			name: "an invalid when condition",
			step: Step{
				When: "//birthTime ===",
				Set:  &V3SetStep{Path: "//birthTime", Value: "1"},
			},
			want: "when condition",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := CompileSteps([]Step{c.step}, nil)
			if err == nil {
				t.Fatal("this was accepted at load, so it would fail on a message instead")
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("the error does not explain the problem: %v", err)
			}
		})
	}
}

// TestTheWhenConditionSkipsRatherThanFails covers conditional steps.
func TestTheWhenConditionSkipsRatherThanFails(t *testing.T) {
	root := transformTree(t)

	run(t, root, Step{
		When: `//administrativeGenderCode@code == "M"`,
		Set:  &V3SetStep{Path: "//birthTime", Value: "19000101"},
	})

	// The patient is F, so the step must not have run.
	if got := mustValue(t, root, "//birthTime@value"); got != "19551014" {
		t.Errorf("birthTime is %q, so a step whose condition was false ran anyway", got)
	}

	run(t, root, Step{
		When: `//administrativeGenderCode@code == "F"`,
		Set:  &V3SetStep{Path: "//birthTime", Value: "19000101"},
	})
	if got := mustValue(t, root, "//birthTime@value"); got != "19000101" {
		t.Errorf("birthTime is %q, so a step whose condition was true did not run", got)
	}
}

// TestTrimAndCaseAndReplaceLeaveAbsentFieldsAlone covers the rewriting steps.
//
// The property worth having is the second one: trimming a field the message does not carry must not create it. Creating an
// element to hold an empty string turns "this sender does not send a middle name" into "this patient has no middle name",
// and those are different claims - one is about a feed and the other is about a person.
func TestTrimAndCaseAndReplaceLeaveAbsentFieldsAlone(t *testing.T) {
	t.Run("trim tidies a value that is there", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Trim: &V3TrimStep{Path: "//family"}})

		if got := mustValue(t, root, "//family"); got != "Dubois" {
			t.Errorf("family is %q, want it trimmed", got)
		}
	})

	t.Run("case changes a value that is there", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Case: &V3CaseStep{Path: "//given(1)", To: "upper"}})

		if got := mustValue(t, root, "//given(1)"); got != "MARIE" {
			t.Errorf("the first given name is %q", got)
		}
	})

	t.Run("replace rewrites a value that is there", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Replace: &V3ReplaceStep{Path: "//patient/id(1)@extension", From: "^MRN", To: "M"}})

		if got := mustValue(t, root, "//patient/id(1)@extension"); got != "M12345" {
			t.Errorf("the identifier is %q", got)
		}
	})

	t.Run("an absent field is left absent", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Trim: &V3TrimStep{Path: "//patientPerson/deceasedTime"}})

		if node := findNode(t, root, "//patientPerson/deceasedTime"); node != nil {
			t.Error("trimming an absent field created it, which turns a fact about the feed into a fact " +
				"about the patient")
		}
	})
}

// genderTables is a real codeset for the map step tests.
//
// A real one rather than a stub, because tables are bound at compile time now and the binding is part of what these tests
// cover: a step naming a table that does not exist must refuse to compile, and a stub satisfying an interface would not
// exercise that at all.
func genderTables(t *testing.T) *codeset.Set {
	t.Helper()

	set := &codeset.Set{Tables: []codeset.Table{{
		Name:      "gender",
		Describes: "administrative gender into the receiving system's vocabulary",
		Entries:   []codeset.Entry{{From: "F", To: "female"}},
	}}}
	set.Compile()

	return set
}

// TestTheMapStepHandlesAMissingTranslationDeliberately covers on_missing.
//
// A code that failed to translate and travelled on unchanged is the failure mode of every mapping table ever written: the
// receiving system gets a code from the sender's vocabulary and either rejects the message or, worse, recognises it as
// something else entirely.
func TestTheMapStepHandlesAMissingTranslationDeliberately(t *testing.T) {
	tables := genderTables(t)

	t.Run("a known value is translated", func(t *testing.T) {
		root := transformTree(t)
		steps, err := CompileSteps([]Step{{Map: &V3MapStep{Path: "//administrativeGenderCode", Table: "gender"}}}, tables)
		if err != nil {
			t.Fatal(err)
		}
		if err := steps.Apply(root); err != nil {
			t.Fatal(err)
		}
		if got := mustValue(t, root, "//administrativeGenderCode@code"); got != "female" {
			t.Errorf("the gender code is %q", got)
		}
	})

	t.Run("fail refuses the message", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{Path: "//administrativeGenderCode", Value: "X"}})

		steps, err := CompileSteps([]Step{{Map: &V3MapStep{
			Path: "//administrativeGenderCode", Table: "gender", OnMissing: "fail",
		}}}, tables)
		if err != nil {
			t.Fatal(err)
		}
		if err := steps.Apply(root); err == nil {
			t.Fatal("an untranslatable code was allowed through by a step set to fail")
		}
	})

	t.Run("clear empties it", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{Path: "//administrativeGenderCode", Value: "X"}})

		steps, err := CompileSteps([]Step{{Map: &V3MapStep{
			Path: "//administrativeGenderCode", Table: "gender", OnMissing: "clear",
		}}}, tables)
		if err != nil {
			t.Fatal(err)
		}
		if err := steps.Apply(root); err != nil {
			t.Fatal(err)
		}
		if v, present := findNode(t, root, "//administrativeGenderCode").Attr("code"); present {
			t.Errorf("the code %q survived a clear", v)
		}
	})

	t.Run("an absent value is not an error", func(t *testing.T) {
		// A step mapping a gender should not fail on a message that carries no gender. The null flavour is
		// how a sender says so, and it is not the mapping's business.
		root := transformTree(t)
		steps, err := CompileSteps([]Step{{Map: &V3MapStep{
			Path: "//patientPerson/religiousAffiliationCode", Table: "gender", OnMissing: "fail",
		}}}, tables)
		if err != nil {
			t.Fatal(err)
		}
		if err := steps.Apply(root); err != nil {
			t.Errorf("mapping an absent field failed: %v", err)
		}
	})
}

// TestCopyRefusesToCopyNothing covers the copy step.
//
// Copying from an absent path is refused rather than treated as copying an empty string, because writing empty would
// overwrite a good value at the destination with nothing - and the author's intent was to move a value that exists.
func TestCopyRefusesToCopyNothing(t *testing.T) {
	t.Run("a real copy works", func(t *testing.T) {
		root := transformTree(t)
		run(t, root, Step{Copy: &V3CopyStep{From: "//patient/id(1)@extension", To: "//patient/id(2)@extension"}})

		if got := mustValue(t, root, "//patient/id(2)@extension"); got != "MRN12345" {
			t.Errorf("the second identifier is %q", got)
		}
	})

	t.Run("copying from nothing is refused", func(t *testing.T) {
		steps, err := CompileSteps([]Step{{Copy: &V3CopyStep{
			From: "//patientPerson/deceasedTime", To: "//birthTime",
		}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = steps.Apply(transformTree(t))
		if err == nil {
			t.Fatal("copying from an absent path was allowed, which would overwrite the destination with " +
				"nothing")
		}
		if !strings.Contains(err.Error(), "no value to copy") {
			t.Errorf("the refusal does not explain itself: %v", err)
		}
	})
}

// TestStepsStopAtTheFirstFailure covers what happens when a step cannot run.
//
// A half-transformed clinical message delivered as though it were complete is worse than one that did not go at all.
func TestStepsStopAtTheFirstFailure(t *testing.T) {
	root := transformTree(t)

	steps, err := CompileSteps([]Step{
		{Description: "this one works", Set: &V3SetStep{Path: "//birthTime", Value: "19700101"}},
		{Description: "this one cannot", Copy: &V3CopyStep{From: "//nothingHere", To: "//family"}},
		{Description: "this one must not run", Set: &V3SetStep{Path: "//given(1)", Value: "Should not happen"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = steps.Apply(root)
	if err == nil {
		t.Fatal("a failing step did not stop the sequence")
	}
	if !strings.Contains(err.Error(), "this one cannot") {
		t.Errorf("the error does not say which step failed: %v", err)
	}

	if got := mustValue(t, root, "//given(1)"); got != "Marie" {
		t.Errorf("the first given name is %q, so a step after the failure ran anyway", got)
	}
}

// findNode resolves a path to a single node for a test, or nil.
func findNode(t *testing.T, root *xtree.Node, raw string) *xtree.Node {
	t.Helper()

	p, err := ParsePath(raw)
	if err != nil {
		t.Fatal(err)
	}
	nodes := p.Resolve(root)
	if len(nodes) == 0 {
		return nil
	}

	return nodes[0]
}

// TestAStepNamingATableThatDoesNotExistRefusesToCompile covers the compile-time table binding.
//
// The alternative - looking the table up per message - has two failure modes and the quiet one is much worse. Either every
// message fails on a channel that started cleanly, or the value travels on untranslated and the receiving system gets a
// code from the sender's vocabulary that it may recognise as something else entirely.
func TestAStepNamingATableThatDoesNotExistRefusesToCompile(t *testing.T) {
	tables := genderTables(t)

	_, err := CompileSteps([]Step{{Map: &V3MapStep{Path: "//administrativeGenderCode", Table: "sex"}}}, tables)
	if err == nil {
		t.Fatal("a step naming a table that does not exist compiled, so the channel would start and then " +
			"either fail every message or pass them through untranslated")
	}
	if !strings.Contains(err.Error(), "gender") {
		t.Errorf("the error does not name the tables that do exist, which is what somebody needs: %v", err)
	}
}

// TestACreatedElementGoesWhereTheSchemaExpectsIt covers element ordering.
//
// Element order is significant in a schema-valid v3 document, so a created element in the wrong position produces a document
// that parses here and is rejected at the receiver. That is the worst place for it to fail: the person who can see the error is
// not the person who made the change, and the two may not speak for a week.
func TestACreatedElementGoesWhereTheSchemaExpectsIt(t *testing.T) {
	childNames := func(t *testing.T, root *xtree.Node, path string) []string {
		t.Helper()
		node := findNode(t, root, path)
		if node == nil {
			t.Fatalf("%s is missing", path)
		}
		var out []string
		for _, c := range node.Children {
			out = append(out, c.Name)
		}

		return out
	}

	anchored := "/PRPA_IN201306UV02/controlActProcess/subject/registrationEvent/subject1/patient"

	t.Run("a birth date goes after the gender, not at the end", func(t *testing.T) {
		root := transformTree(t)
		// Remove the existing birthTime so it has to be created.
		run(t, root, Step{Remove: &V3RemoveStep{Path: "//birthTime"}})
		run(t, root, Step{Set: &V3SetStep{
			Path:  anchored + "/patientPerson/birthTime@value",
			Value: "19700101",
		}})

		got := childNames(t, root, "//patientPerson")
		want := []string{"name", "administrativeGenderCode", "birthTime"}
		if len(got) != len(want) {
			t.Fatalf("patientPerson has children %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("patientPerson has children %v, want %v - a created element is in the wrong "+
					"position, which produces a document that parses here and is rejected at the "+
					"receiver", got, want)
			}
		}
	})

	t.Run("a death indicator goes before a death date", func(t *testing.T) {
		// The one people get wrong, and it matters: a receiver reading them in document order sees the flag
		// before the date.
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{
			Path:  anchored + "/patientPerson/deceasedTime@value",
			Value: "20260101",
		}})
		run(t, root, Step{Set: &V3SetStep{
			Path:  anchored + "/patientPerson/deceasedInd@value",
			Value: "true",
		}})

		got := childNames(t, root, "//patientPerson")
		ind, when := -1, -1
		for i, n := range got {
			switch n {
			case "deceasedInd":
				ind = i
			case "deceasedTime":
				when = i
			}
		}
		if ind < 0 || when < 0 {
			t.Fatalf("both elements were not created: %v", got)
		}
		if ind > when {
			t.Errorf("deceasedInd comes after deceasedTime in %v, which is the wrong sequence order", got)
		}
	})

	t.Run("a name part is placed within the name", func(t *testing.T) {
		// prefix before given, which is the datatype's sequence and not alphabetical.
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{
			Path:  anchored + "/patientPerson/name/prefix",
			Value: "Dr",
		}})

		got := childNames(t, root, "//patientPerson/name")
		if got[0] != "prefix" {
			t.Errorf("the name has children %v, want prefix first", got)
		}
	})

	t.Run("an element this table does not know is appended", func(t *testing.T) {
		// The honest fallback, and it must not disturb a sequence that was already correct. Putting an unknown
		// element in the middle of a known sequence is worse than putting it at the end.
		root := transformTree(t)
		run(t, root, Step{Set: &V3SetStep{
			Path:  anchored + "/patientPerson/somethingNobodyModelled@value",
			Value: "x",
		}})

		got := childNames(t, root, "//patientPerson")
		if got[len(got)-1] != "somethingNobodyModelled" {
			t.Errorf("an unknown element was not appended: %v", got)
		}
		// And the known elements kept their order.
		if got[0] != "name" || got[1] != "administrativeGenderCode" || got[2] != "birthTime" {
			t.Errorf("adding an unknown element disturbed the existing sequence: %v", got)
		}
	})
}

// TestAnUnmodelledSiblingDoesNotMisplaceACreatedElement covers a case no test reached.
//
// A document containing an element this table does not know, with a created element having to be placed around it. Written after a
// plant on the unranked-child handling did not fire, which showed the guard for it could not change any outcome - ranks are
// non-negative, so an unranked child never satisfies the comparison. The guard was removed and this pins the behaviour it claimed
// to protect, so the claim is now checked rather than asserted.
func TestAnUnmodelledSiblingDoesNotMisplaceACreatedElement(t *testing.T) {
	root := transformTree(t)
	anchored := "/PRPA_IN201306UV02/controlActProcess/subject/registrationEvent/subject1/patient"

	// An element the order table has never heard of, sitting among ones it knows.
	run(t, root, Step{Set: &V3SetStep{Path: anchored + "/patientPerson/vendorExtension@value", Value: "x"}})

	// Now create an address, which the table ranks after birthTime.
	run(t, root, Step{Set: &V3SetStep{Path: anchored + "/patientPerson/addr/city", Value: "Lyon"}})

	node := findNode(t, root, "//patientPerson")
	var got []string
	for _, c := range node.Children {
		got = append(got, c.Name)
	}

	// The known elements keep their sequence, which is the property that matters. Where the unmodelled one sits
	// relative to them is not something this package can know, and it is not asserted.
	rank := map[string]int{"name": 0, "administrativeGenderCode": 1, "birthTime": 2, "addr": 3}
	last := -1
	for _, n := range got {
		r, known := rank[n]
		if !known {
			continue
		}
		if r < last {
			t.Fatalf("the known elements are out of sequence in %v: an unmodelled sibling misplaced a "+
				"created element", got)
		}
		last = r
	}

	if findNode(t, root, "//patientPerson/addr/city") == nil {
		t.Error("the address was not created")
	}
}
