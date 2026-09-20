package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// The web dependencies must be described, and described accurately.
//
// # Why this test exists
//
// Nothing tracked them. The Go modules had a list checked against go.mod, four tests on the document's shape, and a check that
// every component carries a package URL. All of them passed while React, CodeMirror and the state library - the code the binary
// embeds and serves to an operator's browser - appeared nowhere.
//
// That made the document wrong rather than short. A reviewer matching a Perfuse binary against advisories would have concluded it
// contains no browser code at all, and a React advisory would not have matched. The one thing a bill of materials is for is being
// complete, and every check we had was about form.
//
// # Why the list is written down
//
// There is no npm equivalent of debug.ReadBuildInfo. The Go components report what was actually linked; these cannot, because the
// SBOM is produced by the compiled binary and the repository is not present at runtime. So the names and versions are recorded in
// Go, and this test is what stops them drifting from package.json - in both directions, because a dependency added to the bundle
// and not described here is the original defect returning.

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func readPackageJSON(t *testing.T) packageJSON {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("..", "..", "web", "package.json"))
	if err != nil {
		t.Fatalf("reading package.json: %v", err)
	}

	var out packageJSON
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("parsing package.json: %v", err)
	}

	if len(out.Dependencies) == 0 {
		t.Fatal("no runtime dependencies were found, so this test proves nothing")
	}
	return out
}

func TestTheWebDependencyListMatchesPackageJSON(t *testing.T) {
	pkg := readPackageJSON(t)

	listed := map[string]string{}
	for _, dep := range webDependencies() {
		if _, seen := listed[dep.name]; seen {
			t.Errorf("%s is listed twice", dep.name)
		}
		listed[dep.name] = dep.version
	}

	for name, version := range pkg.Dependencies {
		got, ok := listed[name]
		if !ok {
			t.Errorf("%s is a runtime dependency of the bundle but is missing from webDependencies();\n"+
				"it ships inside the binary, so a reviewer matching advisories would never see it", name)

			continue
		}

		// The version matters as much as the name. A purl naming the wrong version points a scanner at the wrong
		// advisories, which is worse than pointing it at nothing.
		if got != version {
			t.Errorf("%s is pinned at %s in package.json but the SBOM says %s", name, version, got)
		}
	}

	for name := range listed {
		if _, ok := pkg.Dependencies[name]; !ok {
			t.Errorf("%s is described in the SBOM but is no longer a runtime dependency of the bundle;\n"+
				"describing code that is not shipped is its own kind of wrong", name)
		}
	}
}

// TestTheSBOMDescribesTheBundleItEmbeds is the end-to-end half.
//
// The list above could be perfect and unused. This asserts the components actually reach the document, with npm package URLs, which
// is the form a scanner reads.
func TestTheSBOMDescribesTheBundleItEmbeds(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in the test binary")
	}

	doc := buildSBOM(info)

	byName := map[string]sbomComponent{}
	for _, c := range doc.Components {
		byName[c.Name] = c
	}

	// Spot-checked on the two a reviewer is most likely to search for, rather than the whole list, which the test above
	// already covers.
	for _, want := range []string{"react", "codemirror"} {
		c, ok := byName[want]
		if !ok {
			t.Errorf("the SBOM has no component for %q", want)

			continue
		}

		if c.PURL != "pkg:npm/"+want+"@"+c.Version {
			t.Errorf("%s carries purl %q, which is not the npm form a scanner matches on", want, c.PURL)
		}

		// Scope required, not optional: these are not something a dependency pulled in, they are what the interface is
		// built from, and the document sorts required components first so a reviewer sees them.
		if c.Scope != "required" {
			t.Errorf("%s has scope %q, want required", want, c.Scope)
		}
	}
}
