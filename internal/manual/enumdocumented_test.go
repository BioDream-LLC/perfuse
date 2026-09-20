package manual

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/script"
)

// Every value somebody has to type must appear in the manual as a value.
//
// The configuration keys have been guarded for a while, and the form's controls are guarded separately, but the accepted
// *values* were not - and a key documented without its values leaves a reader knowing a setting exists and not what to put
// in it. Three real gaps were found the first time this ran:
//
//   - ta1 appeared nowhere at all, so one of the four X12 acknowledgements was undocumented.
//   - require, warn and ignore, the envelope policies, appeared only as ordinary English words.
//   - lua and wasm were described in prose as "Lua" and "WebAssembly" and never shown as the values they are written as.
//
// The last one is why this looks for the value inside a code element rather than anywhere in the text. Prose naming a
// language is not the same as telling somebody what to type, and a loose search passed happily while the manual failed to
// say that the value is spelled "wasm". The same reasoning applies to "raw" and "none", which occur constantly as ordinary
// words and would make a loose search meaningless.
func TestEveryEnumeratedValueIsDocumented(t *testing.T) {
	doc, _ := buildForTest(t)
	prose := strings.ToLower(doc.HTML())

	groups := map[string][]string{}

	for _, v := range config.KnownDataTypes {
		groups["data type"] = append(groups["data type"], string(v))
	}
	for _, v := range script.KnownLanguages {
		groups["script language"] = append(groups["script language"], string(v))
	}
	for _, v := range config.KnownEnvelopePolicies {
		groups["envelope policy"] = append(groups["envelope policy"], string(v))
	}
	groups["acknowledgement"] = append(groups["acknowledgement"], config.KnownAcknowledgements...)

	// A positive control. If an enumeration is renamed and this stops reading it, every value looks documented.
	total := 0
	for _, values := range groups {
		total += len(values)
	}
	if total < 15 {
		t.Fatalf("only found %d enumerated values, so this test is not reading the enumerations properly", total)
	}

	for group, values := range groups {
		for _, v := range values {
			if !strings.Contains(prose, "<code>"+strings.ToLower(v)+"</code>") {
				t.Errorf("%s %q is accepted by the configuration but never shown in the manual as a value somebody types",
					group, v)
			}
		}
	}
}
