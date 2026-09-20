package main

import (
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// TestTheServedVersionIsDerivedFromTheResourceShapes is the guard that would have caught the defect.
//
// Two independent facts had to agree and nothing made them: the -fhir-version default, and the release the resource structs are
// shaped to. The flag said R5 because R5 is newest. The structs are R4 because US Core is R4-based. Both defensible, together
// wrong - the capability statement advertised 5.0.0 over resources an R5 client cannot read, and the client's reward was an
// empty medication list rather than an error.
//
// The fix makes the default derived rather than duplicated, so the two cannot disagree. This test defends that property, because
// the tempting "small" change is to write the literal back in - and a literal is how they diverged the first time.
//
// Source-level rather than behavioural, deliberately. The flagset is built inside cmdServe and exercising it would mean either
// restructuring the command for the benefit of a test or asserting on parsed output that hides where the value came from. What
// needs defending here is not the value; it is that the value has one source.
func TestTheServedVersionIsDerivedFromTheResourceShapes(t *testing.T) {
	// Every flag that selects a FHIR release, across both commands. There were four, and I fixed one and believed I was
	// done - the web `serve` command. `fhir convert`, `fhir validate` and `fhir serve` each had their own hardcoded "R5",
	// which is the same one-list-not-three failure as the resource type registry and the queue null.
	files := []string{"serve.go", "fhir.go"}

	total := 0

	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)

		// A release name as a flag default is the defect. Derived from fhir.ResourceShapeVersion is the fix, and there is
		// no third acceptable form, so this looks for the wrong shape rather than counting the right one.
		for _, release := range []string{`"R4"`, `"R4B"`, `"R5"`, `"R6"`} {
			for _, decl := range []string{"fset.String(", "flag.String("} {
				// Crude but sufficient: a release literal appearing as the second argument of a string flag.
				needle := decl
				idx := 0
				for {
					at := strings.Index(body[idx:], needle)
					if at < 0 {
						break
					}
					at += idx
					end := strings.Index(body[at:], ")")
					if end < 0 {
						break
					}
					call := body[at : at+end]
					if strings.Contains(call, release) {
						t.Errorf("%s hardcodes %s as a flag default: %s\n"+
							"  Serving or producing a release the resource structs do not match makes the\n"+
							"  output lie. Derive it from fhir.ResourceShapeVersion instead.",
							name, release, strings.TrimSpace(call))
					}
					idx = at + len(needle)
				}
			}
		}

		total += strings.Count(body, "string(fhir.ResourceShapeVersion)")
	}

	// All four sites, so a future fifth flag that forgets is visible as a count that did not move.
	if total < 4 {
		t.Errorf("only %d flag default(s) derive from fhir.ResourceShapeVersion; there should be at least 4 "+
			"(serve, fhir convert, fhir validate, fhir serve)", total)
	}

	// And the constant still has to name a release that can actually be served.
	if _, err := fhir.ParseVersion(string(fhir.ResourceShapeVersion)); err != nil {
		t.Errorf("fhir.ResourceShapeVersion is %q, which is not a servable release: %v",
			fhir.ResourceShapeVersion, err)
	}
}

// TestTheVersionFlagStillOffersTheLatest makes sure the fix did not silently narrow what is supported.
//
// The defect was declaring a version the resources did not match, not supporting R5 at all. A deployment that asks for R5 should
// still get it, and the flag's help should still say so.
func TestTheVersionFlagStillOffersTheLatest(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	for _, want := range []string{"R4", "R4B", "R5"} {
		if !strings.Contains(body, want) {
			t.Errorf("serve.go no longer mentions %s, so the flag may have stopped offering it", want)
		}
	}

	if _, err := fhir.ParseVersion("R5"); err != nil {
		t.Errorf("R5 can no longer be selected: %v", err)
	}
}
