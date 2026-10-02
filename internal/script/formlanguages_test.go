package script

import (
	"os"
	"strings"
	"testing"
)

// Every language the engine accepts is offered in the form.
//
// # Why this test crosses into the web source
//
// WebAssembly was built, wired, reachable from a channel file, and absent from the form. The standing rule here is that everything
// is reachable from the GUI, so that was a product bug on its own - but the reachability was the smaller half.
//
// The larger half was destructive. The form's reader folded any language it did not recognise into javascript, so opening a working
// WebAssembly channel and pressing save rewrote language: wasm to language: javascript and left the module path sitting in the filter
// field. A path is not a script, so the channel then failed to load, and the action that broke it was an ordinary edit of an
// unrelated field.
//
// The existing guardrail did not catch it. builddriftpath_test.go walks settings by path, and scripts.language was expressible - it
// was the *value* that had no control. A path-level test cannot see a missing option in a dropdown.
//
// So this checks the values, from the side that owns them. Adding a language to KnownLanguages without adding it to the form fails
// here, which is the arrangement that makes forgetting hard rather than merely regrettable. It is deliberately crude: a string search
// of the source rather than a parse, because the alternative is a build step and the crude version fails for the right reason.
func TestEveryLanguageIsOfferedInTheForm(t *testing.T) {
	const form = "../../web/src/BuilderScripts.tsx"

	raw, err := os.ReadFile(form)
	if err != nil {
		t.Fatalf("reading the form's script section: %v", err)
	}

	source := string(raw)

	// A control, so a moved or renamed file fails as a missing list rather than passing silently on an empty search.
	start := strings.Index(source, "const LANGUAGES")
	if start < 0 {
		t.Fatalf("%s no longer declares LANGUAGES, so this test is checking nothing. Point it at wherever the language "+
			"options now live", form)
	}

	// Narrowed to the option list, and getting this right took two attempts that both passed while the property was broken.
	//
	// Searching the whole file passed with the option deleted, because the language name also appears in the draft's type union
	// and in the check that switches the fields to a path. Narrowing to the first bracket after the declaration passed too, because
	// the first bracket belongs to the type annotation's []. Both were guardrails that could not fail, which is worse than none:
	// they report the property holding while it is broken.
	//
	// So the start is the assignment rather than the declaration, which puts the type union behind us.
	open := strings.Index(source[start:], "= [")
	if open < 0 {
		t.Fatalf("the LANGUAGES declaration in %s is not an assigned list this test can read", form)
	}
	from := start + open

	end := strings.Index(source[from:], "]")
	if end < 0 {
		t.Fatalf("the LANGUAGES list in %s is not closed", form)
	}
	options := source[from : from+end]

	// The narrowing itself is checked, because the whole point is that it excludes the type union. If the union ever moves inside
	// the list this test goes back to being unfalsifiable, and it should say so rather than pass.
	if strings.Contains(options, "value:") == false {
		t.Fatalf("the extracted LANGUAGES list from %s does not look like options: %q", form, options)
	}

	for _, language := range KnownLanguages {
		// The value as it appears in the option, which is what reaches the channel file.
		if !strings.Contains(options, "'"+string(language)+"'") {
			t.Errorf("the engine accepts %q but the form does not offer it, so a channel using it can only be written by "+
				"hand - and the form's reader will fold it into the default on the next save, rewriting a working "+
				"channel. Add it to LANGUAGES in %s", language, form)
		}
	}
}

// TestTheFormReaderKnowsEveryLanguage guards the half that corrupts rather than the half that omits.
//
// A dropdown entry with no matching branch in the reader is worse than no dropdown entry at all: the form offers the language,
// accepts it, and quietly reverts it the next time somebody opens the channel. So the reader is checked too, and against the same
// list.
func TestTheFormReaderKnowsEveryLanguage(t *testing.T) {
	const reader = "../../web/src/wireToDraft.ts"

	raw, err := os.ReadFile(reader)
	if err != nil {
		t.Fatalf("reading the form's channel reader: %v", err)
	}

	source := string(raw)

	if !strings.Contains(source, "scriptLanguage") {
		t.Fatalf("%s no longer sets scriptLanguage, so this test is checking nothing", reader)
	}

	for _, language := range KnownLanguages {
		// JavaScript is the default and is reached by falling through rather than by name, which is correct: an unmarked
		// script is javascript for Mirth compatibility, and requiring the string here would force a redundant branch.
		if language == DefaultLanguage {
			continue
		}

		if !strings.Contains(source, "'"+string(language)+"'") {
			t.Errorf("the engine accepts %q but %s does not read it back, so opening such a channel in the form and "+
				"saving would rewrite the language to the default and leave the scripts behind", language, reader)
		}
	}
}
