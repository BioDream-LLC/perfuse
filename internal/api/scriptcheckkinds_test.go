package api

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The workbench must offer every slot a script can occupy.
//
// # What was wrong
//
// Seven kinds exist. Both compilers have handled all seven for as long as they have existed, and the endpoint passed the kind
// straight through to them. But its parser named two, so the other five came back as "the script kind is not recognised" - and the
// form, which could only send what the endpoint took, offered two buttons.
//
// The effect was quiet and specific. Somebody moving off Mirth has preprocessors, postprocessors and deploy hooks to move as well as
// transformers, and the one place built to tell them whether a script survives could only answer for two of the seven. It did not
// look broken; it looked like a tool for transformers.
//
// # Why checking as the wrong kind is not a workaround
//
// The kind decides the wrapper, so it is not a label. A writer compiled as a transformer has its return value discarded, which is
// exactly how a destination that refused a message once reported success. A lifecycle hook has no message in scope, and a
// preprocessor returns text where a transformer mutates a tree. Checking a preprocessor as a transformer would have reported that it
// compiles - true, and about a script nobody was going to run.
//
// # The drift half
//
// The endpoint's list and the form's selector are separate declarations that have to agree, which is the shape of defect that has
// cost this project the most: a value the server accepts and the interface cannot express. The path-walking builder guard cannot see
// it, because there is no config path involved at all.

// TestTheWorkbenchOffersEveryKindTheEndpointAccepts compares the two declarations in both directions.
func TestTheWorkbenchOffersEveryKindTheEndpointAccepts(t *testing.T) {
	const form = "ScriptLab.tsx"

	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", form))
	if err != nil {
		t.Fatalf("reading %s: %v", form, err)
	}

	// The KINDS table, read by its kind: entries. Matching the whole block rather than the file keeps a kind named in prose
	// from counting as a control, which is the mistake that made an earlier guard in this repository unable to fail.
	source := string(body)

	start := strings.Index(source, "const KINDS")
	if start < 0 {
		t.Fatalf("no KINDS table found in %s. If the selector was renamed, this test has to follow it - it is the only "+
			"thing that notices the form offering fewer slots than the engine runs", form)
	}

	end := strings.Index(source[start:], "\n]")
	if end < 0 {
		t.Fatalf("the KINDS table in %s does not end where expected", form)
	}
	table := source[start : start+end]

	offered := map[string]bool{}
	for _, m := range regexp.MustCompile(`kind:\s*'(\w+)'`).FindAllStringSubmatch(table, -1) {
		offered[m[1]] = true
	}

	if len(offered) == 0 {
		t.Fatal("no kinds were extracted from the KINDS table, so this test proves nothing")
	}

	accepted := map[string]bool{}
	for _, k := range scriptCheckKinds {
		accepted[k] = true
	}

	for k := range accepted {
		if !offered[k] {
			t.Errorf("the endpoint accepts %q and the workbench does not offer it.\n"+
				"That is a slot somebody can write a script for and cannot check, which is the whole purpose of the "+
				"pane", k)
		}
	}

	for k := range offered {
		if !accepted[k] {
			t.Errorf("the workbench offers %q and the endpoint refuses it, so pressing it produces an error", k)
		}
	}
}

// TestEveryScriptKindCompilesInBothLanguages is the reachability half: the list agreeing with the form proves nothing if the
// endpoint cannot actually compile them.
//
// Each kind is sent a script that should compile and one that should not. The failing case is the control that matters - without it
// a handler that returned OK without compiling anything would pass every subtest, which is the defect this project keeps finding.
func TestEveryScriptKindCompilesInBothLanguages(t *testing.T) {
	h := newHarness(t)

	for _, language := range []string{"javascript", "lua"} {
		// Syntactically broken in both languages, so the same pair works across the matrix.
		broken := map[string]string{
			"javascript": "return (",
			"lua":        "return (",
		}[language]

		valid := map[string]string{
			"javascript": "return 1",
			"lua":        "return 1",
		}[language]

		for _, kind := range scriptCheckKinds {
			t.Run(language+"/"+kind, func(t *testing.T) {
				code, out := checkScript(t, h, "editor", scriptCheckRequest{
					Kind: kind, Source: valid, Language: language,
				})
				if code != http.StatusOK {
					t.Fatalf("a valid script in the %s slot was refused with status %d", kind, code)
				}

				if !out.OK {
					t.Errorf("a valid script did not compile as a %s: %v", kind, out.Error)
				}

				// The kind is echoed so the pane can say which compiler answered. A response that named a
				// different kind would mean the request had been quietly reinterpreted.
				if out.Kind != kind {
					t.Errorf("sent kind %q and the response says %q", kind, out.Kind)
				}

				_, bad := checkScript(t, h, "editor", scriptCheckRequest{
					Kind: kind, Source: broken, Language: language,
				})
				if bad.OK {
					t.Errorf("a syntactically broken script compiled as a %s, so this slot is not being "+
						"compiled at all", kind)
				}
			})
		}
	}
}

// TestAnUnknownScriptKindSaysWhatIsAvailable keeps the error useful now that there are seven.
func TestAnUnknownScriptKindSaysWhatIsAvailable(t *testing.T) {
	h := newHarness(t)

	res := h.do("editor", http.MethodPost, "/api/scripts/check", scriptCheckRequest{
		Kind: "destination", Source: "return 1",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind returned status %d, want 400", res.Code)
	}

	// Naming them matters because the list is no longer short enough to guess.
	for _, kind := range scriptCheckKinds {
		if !strings.Contains(res.Body.String(), kind) {
			t.Errorf("the error for an unknown kind does not mention %q, so it does not say what to use instead", kind)
		}
	}
}
