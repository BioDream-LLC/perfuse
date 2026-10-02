package api

import (
	"net/http"
	"strings"
	"testing"
)

// The script workbench compiles Lua as well as JavaScript.
//
// # Why this was a gap worth closing
//
// The endpoint existed to answer a migration question - paste a Mirth transformer and learn whether it survives - so it was
// JavaScript only, and that was right for the question it was built for. But the value is not the E4X translation, it is finding out
// before deploying. Mirth makes you deploy to find out, and that is the thing this replaces.
//
// A person writing Lua had nowhere to do that. Their script compiled for the first time when the channel loaded, which is the exact
// behaviour the workbench exists to avoid, and the form offered Lua as a language with no way to check one.
//
// WebAssembly is refused here on purpose. A module is compiled output rather than source, so there is nothing to paste, and a channel
// naming a module already has it compiled and refused at load - so for modules the gap is already closed. Refused explicitly rather
// than left to the unrecognised arm, so somebody who sends it learns the reason is the paste box and not a typo.

// TestTheWorkbenchCompilesLua is the reachability half, with the controls that make it mean something.
func TestTheWorkbenchCompilesLua(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		name     string
		language string
		source   string
		wantOK   bool
	}{
		{
			name:     "valid lua",
			language: "lua",
			source:   `return msg.child("PID").child("PID.3").child("PID.3.1").text() ~= ""`,
			wantOK:   true,
		},
		{
			// The half that proves a compiler ran at all. Without it the endpoint could return ok for anything.
			name:     "lua with a syntax error",
			language: "lua",
			source:   `if then end`,
			wantOK:   false,
		},
		{
			// A control against the wrong compiler answering. This is valid JavaScript and not valid Lua, so an ok here
			// would mean the request was compiled as JavaScript despite asking for Lua - which is the failure that a
			// language field added to a handler without dispatching on it would produce.
			name:     "javascript sent as lua is refused",
			language: "lua",
			source:   `var x = {a: 1}; return x.a === 1;`,
			wantOK:   false,
		},
		{
			name:     "valid javascript still works",
			language: "javascript",
			source:   `return msg['PID']['PID.3']['PID.3.1'].toString() != '';`,
			wantOK:   true,
		},
		{
			// An absent language means javascript, the same default as everywhere else, so existing clients are unaffected.
			name:     "an absent language is javascript",
			language: "",
			source:   `return true;`,
			wantOK:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := checkScript(t, h, "editor", scriptCheckRequest{
				Kind:     "filter",
				Source:   tc.source,
				Language: tc.language,
			})
			if code != http.StatusOK {
				t.Fatalf("status = %d", code)
			}

			if out.OK != tc.wantOK {
				detail := ""
				if out.Error != nil {
					detail = out.Error.Message
				}
				t.Fatalf("ok was %v, want %v (%s)", out.OK, tc.wantOK, detail)
			}

			// The response says which compiler answered, so a client cannot silently receive the other one's diagnostics.
			want := tc.language
			if want == "" {
				want = "javascript"
			}
			if out.Language != want {
				t.Errorf("the response says language %q, want %q", out.Language, want)
			}
		})
	}
}

// TestTheE4XTranslatorDoesNotRunOnLua keeps the panes honest.
//
// E4X is a JavaScript extension. Running the translator over Lua would report rewrites of a language it cannot parse, and the pane's
// whole job is to show what happened to the script - so a spurious rewrite there is worse than showing nothing.
func TestTheE4XTranslatorDoesNotRunOnLua(t *testing.T) {
	h := newHarness(t)

	// Contains the characters E4X cares about, so a translator that ran would have something to say about it.
	lua := `local t = msg.children("OBX")
for i = 1, #t do
  logger.info(t[i].child("OBX.5").text())
end
return true`

	code, out := checkScript(t, h, "editor", scriptCheckRequest{Kind: "transformer", Source: lua, Language: "lua"})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !out.OK {
		t.Fatalf("valid lua was refused: %+v", out.Error)
	}

	if out.Rewritten {
		t.Error("the E4X translator claims it rewrote a Lua script")
	}
	if out.Translated != "" {
		t.Errorf("a translated form was returned for Lua: %q", out.Translated)
	}
	if len(out.Notes) != 0 {
		t.Errorf("translation notes were returned for Lua: %+v", out.Notes)
	}
}

// TestTheWorkbenchRefusesWebAssemblyWithAReason records the boundary.
func TestTheWorkbenchRefusesWebAssemblyWithAReason(t *testing.T) {
	h := newHarness(t)

	res := h.do("editor", http.MethodPost, "/api/scripts/check",
		scriptCheckRequest{Kind: "filter", Source: "anything", Language: "wasm"})

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status was %d, want 400: a module is compiled output and cannot be pasted as source", res.Code)
	}
	if !strings.Contains(strings.ToLower(res.Body.String()), "language") {
		t.Errorf("the refusal should name the language as the problem, got: %s", res.Body.String())
	}
}
