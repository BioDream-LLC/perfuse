package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/script"
)

// Every enumerated config value must have a control in the form.
//
// # The blind spot this closes
//
// builddriftpath_test.go walks config paths and asserts each is expressible in the builder. It is the guard that keeps
// knownBuilderGaps at zero, and it cannot see this class of defect at all, because it asks whether a path is reachable and not
// whether its values are.
//
// Twice in one week that was the difference between a passing test and a broken product:
//
//   - scripts.language was expressible, and the form's language menu listed JavaScript and Lua. Choosing WebAssembly was
//     impossible, and worse, opening a working wasm channel and pressing save rewrote it to javascript while leaving the module
//     path in the filter field.
//   - filter was expressible, and the rule rows offered a closed list of HL7 v2 field paths with no way to type anything else.
//     So a filter could not be written at all on five of the eight formats, while the server accepted one.
//
// Both were found by a person operating the interface, months after the code that broke them. Neither was findable by any check
// that asks about paths.
//
// # Why this is a registry rather than four assertions
//
// The lesson the last eleven defects kept teaching is that a check written next to the first case that needed it will be missing
// from the second. So this covers every exported enumeration at once, and the last test in the file fails if a new one is
// declared and not listed here - which is the part that makes it a fix for the class rather than for four instances.
//
// # What counts as a control, and the weaker version I wrote first
//
// The value has to appear in the shape a control is written in: value: 'x' in an options array, or value="x" on an option
// element. Not merely somewhere in the file.
//
// My first version searched the whole file, and break-verification showed it could not fail. Deleting the WebAssembly option
// from the language menu left the test passing, because 'wasm' still appears in the type union
// value: 'javascript' | 'lua' | 'wasm' and in a draft.scriptLanguage === 'wasm' comparison. That is the same mistake I had
// already recorded from an earlier session and repeated here - which is the argument for breaking every guard rather than
// trusting one that passes.
//
// Restricting it to the control shape is what makes the check able to fail. It also means wireToDraft.ts is no longer an
// accepted site for scripts.language: that file reads a saved channel back into the draft, and its mentioning a value proves
// nothing about whether anyone can choose it. The historical bug was exactly that combination - wireToDraft knew about wasm,
// and no control offered it, so opening a working channel and saving rewrote the language to javascript.

// enumeratedValues is every closed set of config values a person can choose, and where the form has to offer it.
var enumeratedValues = []struct {
	// what names the setting, for a failure message somebody can act on.
	what string

	// values is the closed set the loader accepts.
	values []string

	// files are the web sources that may carry the control. Any one of them containing the value satisfies the check: a
	// setting is often offered in a component and carried in the draft model, and requiring both would be noise.
	files []string
}{
	{
		what:   "dataType",
		values: dataTypeStrings(),
		files:  []string{"BuilderSourceSection.tsx", "model.ts", "ChannelBuilder.tsx"},
	},
	{
		what:   "x12.envelope",
		values: envelopePolicyStrings(),
		files:  []string{"BuilderSourceSection.tsx", "model.ts", "ChannelBuilder.tsx"},
	},
	{
		what:   "x12.acknowledge",
		values: KnownAcknowledgements,
		files:  []string{"BuilderSourceSection.tsx", "model.ts", "ChannelBuilder.tsx"},
	},
	{
		what:   "scripts.language",
		values: languageStrings(),
		files:  []string{"BuilderScripts.tsx"},
	},
}

func dataTypeStrings() []string {
	out := make([]string, 0, len(KnownDataTypes))
	for _, dt := range KnownDataTypes {
		out = append(out, string(dt))
	}

	return out
}

func envelopePolicyStrings() []string {
	out := make([]string, 0, len(KnownEnvelopePolicies))
	for _, p := range KnownEnvelopePolicies {
		out = append(out, string(p))
	}

	return out
}

func languageStrings() []string {
	out := make([]string, 0, len(script.KnownLanguages))
	for _, l := range script.KnownLanguages {
		out = append(out, string(l))
	}

	return out
}

func TestEveryEnumeratedConfigValueHasAControl(t *testing.T) {
	for _, set := range enumeratedValues {
		t.Run(set.what, func(t *testing.T) {
			if len(set.values) == 0 {
				t.Fatal("no values were found, so this subtest proves nothing")
			}

			sources := map[string]string{}
			for _, name := range set.files {
				path := filepath.Join("..", "..", "web", "src", name)

				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("reading %s: %v", name, err)
				}
				sources[name] = string(body)
			}

			for _, value := range set.values {
				// The two shapes a control is written in here. An options array entry, and a bare option element.
				// Neither can be produced by a type annotation or a comparison, which is what made the first
				// version of this test unable to fail.
				control := regexp.MustCompile(`value(?::\s*|=)['"]` + regexp.QuoteMeta(value) + `['"]`)

				found := false
				for _, body := range sources {
					if control.MatchString(body) {
						found = true

						break
					}
				}

				if !found {
					t.Errorf("%s accepts %q and no form source offers it (looked in %s).\n"+
						"A value the server takes and the interface cannot express is a product bug here, not a "+
						"limitation - and the path-walking drift guard cannot see it, because the path is fine and "+
						"it is the value that has nowhere to come from",
						set.what, value, strings.Join(set.files, ", "))
				}
			}
		})
	}
}

// TestNoEnumerationEscapesTheRegistry is the part that makes the test above a fix for the class.
//
// A guard covering four settings is four assertions. What turns it into a rule is failing when a fifth arrives: otherwise the next
// enumeration gets added, gets no control, and nothing notices - which is exactly how the first two happened.
//
// It scans for exported enumeration variables by name. That convention is load-bearing, so the failure message says so.
func TestNoEnumerationEscapesTheRegistry(t *testing.T) {
	covered := map[string]bool{}
	for _, set := range enumeratedValues {
		covered[set.what] = true
	}

	// The declarations, by the naming convention the project already uses for them.
	declared := map[string][]string{}
	for _, dir := range []string{filepath.Join("..", "config"), filepath.Join("..", "script")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}

			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("reading %s: %v", e.Name(), err)
			}

			for _, m := range regexp.MustCompile(`(?m)^var (Known\w+) = \[\]`).FindAllStringSubmatch(string(body), -1) {
				declared[m[1]] = append(declared[m[1]], e.Name())
			}
		}
	}

	if len(declared) == 0 {
		t.Fatal("no Known* enumerations were found, so this test proves nothing. If the naming convention changed, this " +
			"test has to change with it - it is the only thing that notices a new enumeration with no control")
	}

	// Which registry entry accounts for which declaration. Mapped by hand because the names do not match the config paths:
	// KnownDataTypes governs dataType, KnownLanguages governs scripts.language.
	// Only the four that exist. I had pre-listed several plausible names with no setting, which would have
	// pre-authorised enumerations I had invented and never examined - the opposite of what this test is for. An
	// unrecognised name must fail and make somebody decide.
	accountedFor := map[string]string{
		"KnownDataTypes":        "dataType",
		"KnownEnvelopePolicies": "x12.envelope",
		"KnownAcknowledgements": "x12.acknowledge",
		"KnownLanguages":        "scripts.language",
	}

	for name, files := range declared {
		setting, known := accountedFor[name]
		if !known {
			t.Errorf("%s is an exported enumeration (in %s) that this registry does not know about.\n"+
				"Add it to enumeratedValues with the form files that must offer its values, or add it to accountedFor "+
				"with an empty setting and a comment saying why it needs no control.\n"+
				"This test exists because a check written for the settings that had already broken would be missing "+
				"from the next one", name, strings.Join(files, ", "))

			continue
		}

		if setting != "" && !covered[setting] {
			t.Errorf("%s maps to setting %q, which is not in enumeratedValues", name, setting)
		}
	}
}
