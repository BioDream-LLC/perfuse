package script

import (
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/steps"
)

// fakeMessage is a stand-in format: a map of path to value.
//
// # Why a fake rather than a real X12 message
//
// This file tests the *binding* - what get, has and set do, and how a change is recorded. Using a real X12 interchange would
// test x12.Set as well, which has its own tests, and a failure would be ambiguous between the two.
//
// The properties that matter here are format-independent by construction, because the binding only ever sees a steps.Accessor.
// The real formats are exercised through the engine instead, where the question is whether the wiring reaches them.
type fakeMessage map[string]string

// fakeAccessor resolves paths against a fakeMessage, and can be told to refuse or neutralise a write.
type fakeAccessor struct {
	// refuse names a path whose write fails, standing in for an over-long ISA element or an ambiguous NCPDP path.
	refuse string

	// neutralise names a path whose write is silently discarded, standing in for a fixed-width element being re-padded.
	neutralise string

	// bad names a path that will not compile, standing in for a mistyped one.
	bad string
}

func (a fakeAccessor) Path(raw string) (steps.Path[fakeMessage], error) {
	if raw == a.bad {
		return steps.Path[fakeMessage]{}, errBadPath
	}

	return steps.Path[fakeMessage]{
		Canonical: strings.ToUpper(raw),
		Read: func(m fakeMessage) (string, bool) {
			v, ok := m[raw]

			return v, ok
		},
		Write: func(m fakeMessage, value string) (fakeMessage, error) {
			if raw == a.refuse {
				return m, errRefused
			}

			// Copied rather than edited, matching the real accessors: a failed step has to leave the original intact.
			next := make(fakeMessage, len(m)+1)
			for k, v := range m {
				next[k] = v
			}
			if raw != a.neutralise {
				next[raw] = value
			}

			return next, nil
		},
	}, nil
}

func (fakeAccessor) Condition(string) (expr.Expr[fakeMessage], error) { return nil, nil }
func (fakeAccessor) CheckStep(steps.Step) error                       { return nil }

var (
	errBadPath = &pathError{"CLM99 is not a path"}
	errRefused = &pathError{"the value is too long for a fixed width element"}
)

type pathError struct{ msg string }

func (e *pathError) Error() string { return e.msg }

func runPath(t *testing.T, src string, kind Kind, msg fakeMessage, a fakeAccessor) (fakeMessage, []string, Result, error) {
	t.Helper()

	return runPathIn(t, src, kind, msg, a, Lua)
}

// runPathIn is runPath in a named language.
//
// Separate rather than runPath gaining a parameter, because most tests here are about the binding's behaviour rather than about
// either language, and making every one of them name Lua would suggest the choice mattered to what they assert.
func runPathIn(t *testing.T, src string, kind Kind, msg fakeMessage, a fakeAccessor, language Language) (fakeMessage, []string, Result, error) {
	t.Helper()

	name := "path.lua"
	if language != Lua {
		name = "path.js"
	}

	e := New(Options{Timeout: 2 * time.Second})
	s, err := e.CompileIn(name, src, kind, language)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	return RunPathScript(e, s, &Context{}, msg, a)
}

func TestAPathScriptReadsAValue(t *testing.T) {
	_, _, res, err := runPath(t, `return msg.get("CLM02") == "500"`, Filter, fakeMessage{"CLM02": "500"}, fakeAccessor{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("a path script could not read a value")
	}
}

// TestAnAbsentPathReadsAsNilAndNotEmpty keeps the distinction every one of these formats makes.
//
// A blank field was sent deliberately; an absent one was never sent. For a claim that is the difference between "no diagnosis"
// and "the diagnosis segment is missing", and a script that cannot tell them apart cannot make the decision either.
func TestAnAbsentPathReadsAsNilAndNotEmpty(t *testing.T) {
	msg := fakeMessage{"CLM02": ""}

	_, _, res, err := runPath(t, `return msg.get("CLM02") == "" and msg.get("CLM03") == nil`, Filter, msg, fakeAccessor{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("absent and blank did not read differently")
	}
}

func TestHasDistinguishesPresenceFromValue(t *testing.T) {
	msg := fakeMessage{"CLM02": ""}

	_, _, res, err := runPath(t, `return msg.has("CLM02") and not msg.has("CLM09")`, Filter, msg, fakeAccessor{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("has did not report presence independently of value")
	}
}

func TestAPathScriptWritesAValue(t *testing.T) {
	out, changed, _, err := runPath(t, `msg.set("CLM02", "900")`, Transformer, fakeMessage{"CLM02": "500"}, fakeAccessor{})
	if err != nil {
		t.Fatal(err)
	}

	if out["CLM02"] != "900" {
		t.Errorf("the value is %q after the write", out["CLM02"])
	}
	if len(changed) != 1 || changed[0] != "CLM02" {
		t.Errorf("changed paths are %v, want one entry for CLM02", changed)
	}
}

// TestAWriteDoesNotModifyTheOriginal is why the accessors are functional.
//
// A script that fails partway through must leave the message that arrived intact, so a retry starts from it rather than from a
// half-transformed version.
func TestAWriteDoesNotModifyTheOriginal(t *testing.T) {
	original := fakeMessage{"CLM02": "500"}

	if _, _, _, err := runPath(t, `msg.set("CLM02", "900")`, Transformer, original, fakeAccessor{}); err != nil {
		t.Fatal(err)
	}

	if original["CLM02"] != "500" {
		t.Errorf("the original was modified: CLM02 is now %q", original["CLM02"])
	}
}

// TestANeutralisedWriteIsNotReportedAsAChange is the audit-trail property.
//
// Setting a fixed-width X12 element to a shorter string re-pads it, so the value that lands is not the value asked for. Counting
// that as a change makes the record overstate what the script did - the same failure the declarative engine's re-read guards,
// and the one whose test stopped being able to fail once a refusal removed its only scenario.
func TestANeutralisedWriteIsNotReportedAsAChange(t *testing.T) {
	msg := fakeMessage{"ISA06": "SUBMITTER      "}

	_, changed, _, err := runPath(t, `msg.set("ISA06", "SUBMITTER")`, Transformer,
		msg, fakeAccessor{neutralise: "ISA06"})
	if err != nil {
		t.Fatal(err)
	}

	if len(changed) != 0 {
		t.Errorf("a write that did not take effect was reported as changing %v", changed)
	}
}

// TestARefusedWriteStopsTheScript is the direction that matters.
//
// The format refused it - an over-long element, an ambiguous path, a column not in the header. Carrying on would leave a script
// believing it had changed something, and the message would reach the destination missing the field.
func TestARefusedWriteStopsTheScript(t *testing.T) {
	_, _, _, err := runPath(t, `
		msg.set("ISA06", "FAR TOO LONG FOR THIS ELEMENT")
		msg.set("CLM02", "900")
	`, Transformer, fakeMessage{}, fakeAccessor{refuse: "ISA06"})

	if err == nil {
		t.Fatal("a refused write did not stop the script")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("the error should carry the format's reason, got: %v", err)
	}
}

// TestAMistypedPathFailsRatherThanReadingNil attributes the mistake to the script.
//
// A script that carries on with nil produces a message missing a field, discovered at the receiver where it looks like their
// problem.
func TestAMistypedPathFailsRatherThanReadingNil(t *testing.T) {
	_, _, _, err := runPath(t, `return msg.get("CLM99") == nil`, Filter, fakeMessage{}, fakeAccessor{bad: "CLM99"})

	if err == nil {
		t.Fatal("a path that does not compile was accepted and read as absent")
	}
	if !strings.Contains(err.Error(), "CLM99") {
		t.Errorf("the error should name the path, got: %v", err)
	}
}

// TestThePathScriptKeepsTheSharedVerdictRules is the property that makes this one feature rather than two.
//
// The whole argument for a shared Result is that a filter means the same thing whatever it addresses. Lua truthiness must not
// leak in here any more than it does for a tree script.
func TestThePathScriptKeepsTheSharedVerdictRules(t *testing.T) {
	for _, src := range []string{`return 0`, `return ""`, `return "yes"`} {
		if _, _, _, err := runPath(t, src, Filter, fakeMessage{}, fakeAccessor{}); err == nil {
			t.Errorf("%s was accepted as a filter verdict on a path-addressed message, but is refused on a tree one", src)
		}
	}

	if _, _, _, err := runPath(t, `local x = 1`, Filter, fakeMessage{}, fakeAccessor{}); err == nil {
		t.Error("a filter returning nothing was accepted")
	}
}

// TestAPathScriptGetsTheSameSandbox is the other half of shared semantics.
//
// A second binding is a second chance to open a library by accident. If these formats got a laxer sandbox, the safe answer would
// depend on which data type a channel happened to carry.
func TestAPathScriptGetsTheSameSandbox(t *testing.T) {
	for _, name := range []string{"os", "io", "debug", "require", "load"} {
		_, _, res, err := runPath(t, `return `+name+` == nil`, Filter, fakeMessage{}, fakeAccessor{})
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}
		if !res.Accept {
			t.Errorf("%s is reachable from a path-addressed script but not from a tree one, so the sandbox depends on the data type", name)
		}
	}
}

func TestAPathScriptCanLog(t *testing.T) {
	_, _, res, err := runPath(t, `logger.info("looked at a claim"); return true`, Filter, fakeMessage{}, fakeAccessor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Logs) != 1 {
		t.Fatalf("got %d log line(s)", len(res.Logs))
	}
}

// TestBothLanguagesAnswerThePathQuestionsIdentically is the guardrail that makes two languages safe.
//
// # What replaced what
//
// JavaScript used to be refused here, and the reason given was that a goja binding would be a second implementation of the same
// vocabulary which would drift on the interesting questions - what a neutralised write reports, what an ambiguous path does. That
// was right about the risk. It was wrong that the risk was unavoidable: the answers lived inside the Lua closures, which is what
// made a second copy necessary.
//
// They are methods on PathMessage now. Both bindings call them, so neither can answer differently, and this table is what keeps that
// true - it runs the same question in both languages and fails if they disagree, rather than testing each language separately and
// leaving a reader to compare two lists of expectations by eye.
//
// The two spellings that differ are deliberate and are the only ones: an absent field is nil in Lua and null in JavaScript, and a
// path error raises in Lua and throws in JavaScript. Both are the local word for the same decision.
func TestBothLanguagesAnswerThePathQuestionsIdentically(t *testing.T) {
	for _, tc := range []struct {
		name string
		lua  string
		js   string

		msg      fakeMessage
		accessor fakeAccessor

		wantAccept  bool
		wantErr     bool
		wantChanges []string
	}{
		{
			name:       "a present field reads back",
			lua:        `return msg.get("A") == "1"`,
			js:         `return msg.get("A") === "1";`,
			msg:        fakeMessage{"A": "1"},
			wantAccept: true,
		},
		{
			// The one difference in spelling, and the reason it exists: absent and blank are different facts in every format
			// here, so a missing field cannot read back as "".
			name:       "an absent field is not a blank one",
			lua:        `return msg.get("Z") == nil`,
			js:         `return msg.get("Z") === null;`,
			msg:        fakeMessage{"A": "1"},
			wantAccept: true,
		},
		{
			name:       "has distinguishes present from absent",
			lua:        `return msg.has("A") and not msg.has("Z")`,
			js:         `return msg.has("A") && !msg.has("Z");`,
			msg:        fakeMessage{"A": "1"},
			wantAccept: true,
		},
		{
			name:        "a write is recorded",
			lua:         `msg.set("A", "2") return true`,
			js:          `msg.set("A", "2"); return true;`,
			msg:         fakeMessage{"A": "1"},
			wantAccept:  true,
			wantChanges: []string{"A"},
		},
		{
			// The question the refusal was most worried about. A fixed-width element set to a shorter string is re-padded, so
			// the value that lands is not the value asked for - and reporting a change that did not happen makes an audit
			// trail overstate.
			name:        "a neutralised write is not recorded",
			lua:         `msg.set("A", "2") return true`,
			js:          `msg.set("A", "2"); return true;`,
			msg:         fakeMessage{"A": "1"},
			accessor:    fakeAccessor{neutralise: "A"},
			wantAccept:  true,
			wantChanges: nil,
		},
		{
			// The other one. A mistyped path is a bug in the script, and carrying on with nothing produces a message missing
			// a field that is discovered at the receiver.
			name:     "a path that will not compile stops the script",
			lua:      `return msg.get("nope") ~= nil`,
			js:       `return msg.get("nope") !== null;`,
			msg:      fakeMessage{"A": "1"},
			accessor: fakeAccessor{bad: "nope"},
			wantErr:  true,
		},
		{
			name:     "a write the format refuses stops the script",
			lua:      `msg.set("A", "toolong") return true`,
			js:       `msg.set("A", "toolong"); return true;`,
			msg:      fakeMessage{"A": "1"},
			accessor: fakeAccessor{refuse: "A"},
			wantErr:  true,
		},
	} {
		for _, lang := range []struct {
			name     string
			language Language
			source   string
		}{
			{"lua", Lua, tc.lua},
			{"javascript", JavaScript, tc.js},
		} {
			t.Run(tc.name+"/"+lang.name, func(t *testing.T) {
				// A copy per language, because a write in the first would otherwise be visible to the second and the
				// second assertion would be made against a message the first had already changed.
				msg := fakeMessage{}
				for k, v := range tc.msg {
					msg[k] = v
				}

				_, changed, res, err := runPathIn(t, lang.source, Filter, msg, tc.accessor, lang.language)

				if tc.wantErr {
					if err == nil {
						t.Fatalf("expected the script to stop, but it ran and accepted=%v", res.Accept)
					}

					return
				}
				if err != nil {
					t.Fatalf("running: %v", err)
				}

				if res.Accept != tc.wantAccept {
					t.Errorf("accept was %v, want %v", res.Accept, tc.wantAccept)
				}
				if len(changed) != len(tc.wantChanges) {
					t.Errorf("changed %v, want %v", changed, tc.wantChanges)
				}
				for i := range tc.wantChanges {
					if i < len(changed) && changed[i] != tc.wantChanges[i] {
						t.Errorf("change %d was %q, want %q", i, changed[i], tc.wantChanges[i])
					}
				}
			})
		}
	}
}

// TestAScriptThatWritesNothingReportsNoChanges separates two facts an audit trail needs apart.
func TestAScriptThatWritesNothingReportsNoChanges(t *testing.T) {
	_, changed, _, err := runPath(t, `local x = msg.get("CLM02")`, Transformer, fakeMessage{"CLM02": "500"}, fakeAccessor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Errorf("a script that only read reported changes: %v", changed)
	}
}

func TestSeveralWritesAreRecordedInOrder(t *testing.T) {
	_, changed, _, err := runPath(t, `
		msg.set("CLM02", "900")
		msg.set("REF02", "NEW")
		msg.set("CLM02", "1000")
	`, Transformer, fakeMessage{}, fakeAccessor{})
	if err != nil {
		t.Fatal(err)
	}

	// Three writes, three entries, including the repeated path. Deduplicating would hide that a script set a field twice,
	// which is usually a mistake worth seeing in the record.
	want := []string{"CLM02", "REF02", "CLM02"}
	if len(changed) != len(want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	for i := range want {
		if changed[i] != want[i] {
			t.Errorf("changed[%d] = %q, want %q", i, changed[i], want[i])
		}
	}
}

// TestWASMIsRefusedOnAPathAddressedFormat records why the third language does not reach the path binding.
//
// # Why this test exists at all
//
// The dispatch in RunPathScript had a Lua arm and a default arm, and the default ran goja. When WebAssembly was added it fell into
// that default, so a module on an X12 channel would have been handed to a JavaScript engine and its bytes evaluated as source - a
// syntax error naming neither the language nor the problem.
//
// That is the same shape as several defects this codebase has produced: a switch whose last arm was written for the only case that
// existed at the time, quietly becoming the arm for every case added later. So the arms are named now, and this asserts the refusal
// rather than trusting that they stay named.
//
// The reason is real rather than temporary. A path binding is host functions - the script calls get and set and the host resolves
// the path - and a module is given the message on stdin precisely so it needs no agreement about calling conventions or string
// encoding. Giving it get and set would mean inventing that agreement, and then a third answer to what a neutralised write reports.
func TestWASMIsRefusedOnAPathAddressedFormat(t *testing.T) {
	// Not a real module: compilation is not what is being tested, and the refusal happens before the module would be run.
	module := string([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})

	e := New(Options{Timeout: 2 * time.Second})

	s, err := e.CompileIn("path.wasm", module, Filter, WASM)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	_, _, _, err = RunPathScript(e, s, &Context{}, fakeMessage{}, fakeAccessor{})
	if err == nil {
		t.Fatal("a WebAssembly script was accepted against a path-addressed message; it would have been run as JavaScript")
	}

	// The error has to say what to do instead, or somebody reads it as a bug rather than a boundary.
	for _, want := range []string{"lua", "javascript", "stdin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q so it is actionable, got: %v", want, err)
		}
	}
}
