package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The form's script availability must equal the loader's.
//
// # Why this test exists
//
// BuilderScripts.tsx decides whether to offer a filter or transformer script field, and its comment said it mirrored
// scriptSlotsRun. It did not. The predicate was dataType === 'hl7' || dataType === 'hl7v3', so the field was disabled on X12,
// NCPDP, delimited and SCRIPT - four formats that run both slots, three through the generic path stage and SCRIPT through the
// same tree stage v3 uses.
//
// The explanation underneath made it worse: it said a filter script needs the message as a tree, which only HL7 has. That is
// not true, and it is the kind of untrue that stops somebody looking. They would read it, believe the product cannot do this,
// and write the filter into a hand-edited file or give up.
//
// A control that refuses a working feature is the same defect as one that accepts a broken one, and both pass every test that
// only asks whether the section renders. So the two lists are compared here, in the package that owns the truth.
//
// # Why it reads the file rather than generating it
//
// Generating the predicate from Go would be the stronger answer and a worse trade: it puts a build step between somebody
// editing a form and seeing it change. Reading the file keeps the source honest about being handwritten while making drift
// fail here rather than in front of a user.
func TestTheFormOffersScriptSlotsExactlyWhereTheLoaderRunsThem(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "BuilderScripts.tsx")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the form: %v", err)
	}
	source := string(raw)

	// The predicate is a chain of comparisons against draft.dataType. Extracting the names it lists is enough: the question
	// is which formats it admits, not how the expression is spelled.
	const anchor = "const runsMessageScripts ="
	start := strings.Index(source, anchor)
	if start < 0 {
		t.Fatalf("%s no longer contains %q. If the predicate was renamed, update this test - it is the only thing keeping "+
			"the form's answer equal to the loader's", path, anchor)
	}

	end := strings.Index(source[start:], "\n\n")
	if end < 0 {
		t.Fatal("could not find the end of the predicate")
	}
	block := source[start : start+end]

	inForm := map[string]bool{}
	for _, m := range regexp.MustCompile(`draft\.dataType === '([a-z0-9]+)'`).FindAllStringSubmatch(block, -1) {
		inForm[m[1]] = true
	}

	if len(inForm) == 0 {
		t.Fatal("no data types were extracted from the predicate, so this test would pass no matter what the form said")
	}

	// The loader's answer, for the two slots the predicate governs.
	inLoader := map[string]bool{}
	for dt, slots := range scriptSlotsRun {
		if slots[slotFilter] && slots[slotTransformer] {
			inLoader[string(dt)] = true
		}
	}

	for dt := range inLoader {
		if !inForm[dt] {
			t.Errorf("the loader runs filter and transformer scripts on %q but the form does not offer them. A field "+
				"disabled with a reason that is not true is worse than a missing one: somebody reads it and stops "+
				"looking", dt)
		}
	}

	for dt := range inForm {
		if !inLoader[dt] {
			t.Errorf("the form offers filter and transformer scripts on %q but the loader does not run them, so a script "+
				"typed there would be accepted and never execute", dt)
		}
	}
}
