package main

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

// The bill of materials is an argument, so the tests are about whether the argument is true.

// TestTheDirectDependencyListMatchesGoMod is the reason a hand-maintained list is acceptable.
//
// Build information does not distinguish direct from indirect, and that distinction is the whole point -
// eight things Perfuse chose reads very differently from twenty-five resolved modules. So the list is
// written down, and this makes it impossible for it to drift silently. A dependency added without
// updating the list fails here.
func TestTheDirectDependencyListMatchesGoMod(t *testing.T) {
	body, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}

	fromMod := directFromGoMod(string(body))
	if len(fromMod) == 0 {
		t.Fatal("no direct requirements were found in go.mod, so this test proves nothing")
	}

	listed := directDependencies()

	for path := range fromMod {
		if !listed[path] {
			t.Errorf("%s is a direct dependency in go.mod but is missing from directDependencies();\n"+
				"add it, or the bill of materials will describe it as something a dependency pulled in",
				path)
		}
	}

	for path := range listed {
		if !fromMod[path] {
			t.Errorf("%s is listed as a direct dependency but go.mod no longer requires it directly;\n"+
				"remove it, or the bill of materials overstates what Perfuse chose", path)
		}
	}
}

// directFromGoMod reads the first require block, which is where direct requirements live.
//
// Indirect requirements carry an "// indirect" comment, so they are excluded by that rather than by
// guessing which block they are in - a go.mod can be organised either way and tidying it should not
// break this test.
func directFromGoMod(text string) map[string]bool {
	out := map[string]bool{}
	line := regexp.MustCompile(`^\s*([a-z0-9][^\s]*\.[^\s]*)\s+v\S+(\s*//\s*indirect)?\s*$`)

	for _, l := range strings.Split(text, "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if strings.Contains(m[2], "indirect") {
			continue
		}
		out[m[1]] = true
	}
	return out
}

func TestTheSBOMIsValidCycloneDX(t *testing.T) {
	var buf bytes.Buffer
	if err := cmdSBOM([]string{"-json"}, &buf, &buf); err != nil {
		t.Fatal(err)
	}

	var doc sbomDocument
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("the output is not valid JSON: %v", err)
	}

	if doc.BOMFormat != "CycloneDX" {
		t.Errorf("bomFormat = %q", doc.BOMFormat)
	}
	if doc.SpecVersion != cycloneDXVersion {
		t.Errorf("specVersion = %q, want %s", doc.SpecVersion, cycloneDXVersion)
	}
	if len(doc.Components) == 0 {
		t.Fatal("no components, so the document says this binary has no dependencies")
	}
}

func TestEveryComponentHasAPackageURL(t *testing.T) {
	// A missing or malformed purl makes a scanner skip the component silently, which is worse than a
	// document it rejects outright - the reviewer gets a clean report on something that was not checked.
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in the test binary")
	}

	doc := buildSBOM(info)

	webNames := map[string]bool{}
	for _, dep := range webDependencies() {
		webNames[dep.name] = true
	}

	for _, c := range doc.Components {
		if c.PURL == "" {
			t.Errorf("%s has no package URL", c.Name)
			continue
		}
		// Two ecosystems now. The binary embeds the browser bundle, so the document describes npm packages as well as
		// Go modules - and the purl has to name the right one, because a scanner uses that prefix to choose which
		// advisory database to search. A golang purl on a React version would match nothing and report clean.
		//
		// Checked against the list rather than by guessing from the name: a package called react could in principle be
		// a Go module, and this test should fail if the two ever disagree rather than infer its way past it.
		wantPrefix := "pkg:golang/"
		if webNames[c.Name] {
			wantPrefix = "pkg:npm/"
		}
		if !strings.HasPrefix(c.PURL, wantPrefix) {
			t.Errorf("%s has purl %q, which does not name its ecosystem (%s)", c.Name, c.PURL, wantPrefix)
		}
		if !strings.Contains(c.PURL, "@") {
			t.Errorf("%s has a purl with no version, so no advisory can match it: %q", c.Name, c.PURL)
		}
		if !sbomPURLSafe(c.Name) {
			t.Errorf("%s contains characters that need escaping in a purl", c.Name)
		}
	}
}

func TestDirectDependenciesSortFirst(t *testing.T) {
	// A reviewer reads the direct list and skims the rest. Burying the eight things Perfuse chose among
	// twenty-five resolved modules hides the point of publishing this at all.
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in the test binary")
	}

	doc := buildSBOM(info)

	seenOptional := false
	for _, c := range doc.Components {
		if c.Scope == "optional" {
			seenOptional = true
			continue
		}
		if seenOptional {
			t.Fatalf("%s is direct but sorts after an indirect module", c.Name)
		}
	}
}

func TestTheProseSaysTheThingsAReviewerAsks(t *testing.T) {
	// Static linking, no CGO and no embedded model each close a line of questioning in one sentence.
	// They are stated rather than left for somebody to work out.
	var buf bytes.Buffer
	if err := cmdSBOM(nil, &buf, &buf); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{"statically linked", "no C dependencies", "no embedded model"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}
}

func TestDirectOnlyStillSaysHowManyWereOmitted(t *testing.T) {
	// A shorter list that does not say what it left out invites the suspicion that it is hiding
	// something, which is the opposite of the point.
	var buf bytes.Buffer
	if err := cmdSBOM([]string{"-direct"}, &buf, &buf); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "further module") {
		t.Errorf("the direct listing does not say how many were omitted:\n%s", out)
	}
}

func TestTheEmbeddedModelPropertyIsNone(t *testing.T) {
	// Recorded as a machine-readable fact, not only prose, because it is the kind of thing a procurement
	// questionnaire asks for and a person then has to answer by hand.
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in the test binary")
	}

	doc := buildSBOM(info)
	for _, p := range doc.Metadata.Properties {
		if p.Name == "perfuse:embeddedModel" {
			if p.Value != "none" {
				t.Errorf("embeddedModel = %q, want none", p.Value)
			}
			return
		}
	}
	t.Error("the document does not record whether a model is embedded")
}

// TestTheSPDXDocumentIsStableAcrossRuns covers why the namespace is derived rather than random.
//
// A random identifier makes every bill of materials differ from the last one, which defeats the main thing anybody does with a series of them:
// diffing to see what changed between two releases.
func TestTheSPDXDocumentIsStableAcrossRuns(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in the test binary")
	}

	doc := buildSBOM(info)

	first := sbomFingerprint(doc)
	second := sbomFingerprint(doc)

	if first != second {
		t.Errorf("the fingerprint changed between two calls: %s then %s", first, second)
	}

	// And it changes when the components do, or it is not a fingerprint of anything.
	changed := buildSBOM(info)
	changed.Components = append(changed.Components, sbomComponent{
		Name: "example.com/new", Version: "v1.0.0",
	})
	if sbomFingerprint(changed) == first {
		t.Error("adding a component did not change the fingerprint")
	}
}

// TestTheSPDXDocumentCarriesTheRequiredHeaders covers the fields a parser refuses without.
func TestTheSPDXDocumentCarriesTheRequiredHeaders(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in the test binary")
	}

	body := spdxTagValue(buildSBOM(info), false)

	for _, want := range []string{
		"SPDXVersion: SPDX-2.3",
		"DataLicense: CC0-1.0",
		"SPDXID: SPDXRef-DOCUMENT",
		"DocumentName: perfuse-",
		"DocumentNamespace: ",
		"Creator: Tool: ",
		"Created: ",
		"Relationship: SPDXRef-Package-perfuse DEPENDS_ON ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the document is missing %q", want)
		}
	}

	// FilesAnalyzed must be false and must be said. True asserts that every file was examined and covered by a
	// verification code, which is not what this document does - it lists modules.
	if !strings.Contains(body, "FilesAnalyzed: false") {
		t.Error("the document does not state FilesAnalyzed: false, which would overstate what it examined")
	}
	if strings.Contains(body, "FilesAnalyzed: true") {
		t.Error("the document claims files were analysed")
	}
}

// TestTheSPDXDocumentCanBeNarrowedToDirectDependencies keeps -direct meaningful in both formats.
func TestTheSPDXDocumentCanBeNarrowedToDirectDependencies(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in the test binary")
	}

	doc := buildSBOM(info)

	all := strings.Count(spdxTagValue(doc, false), "Relationship:")
	directOnly := strings.Count(spdxTagValue(doc, true), "Relationship:")

	if directOnly == 0 {
		t.Fatal("the direct-only document lists no dependencies at all")
	}
	if directOnly >= all {
		t.Errorf("-direct listed %d of %d packages, so it narrowed nothing", directOnly, all)
	}
}

// TestAReplacedModuleSaysSo covers the most dangerous kind of error this document can contain.
//
// A replaced module is reported as what was built, which is right. Reporting it silently is not: a reviewer asking "do you ship the vulnerable
// version of X" against a document naming only Y concludes X is absent - and X is exactly the version that was replaced away.
func TestAReplacedModuleSaysSo(t *testing.T) {
	info := &debug.BuildInfo{
		GoVersion: "go1.26.6",
		Main:      debug.Module{Path: "github.com/biodream-llc/perfuse", Version: "test"},
		Deps: []*debug.Module{{
			Path:    "example.com/original",
			Version: "v1.0.0",
			Replace: &debug.Module{Path: "example.com/fork", Version: "v1.0.1"},
		}},
	}

	doc := buildSBOM(info)
	// The browser bundle's components are always present, so this counts the ones built from the build information it was
	// given. Asserting on the total would make this test fail every time a front-end dependency changes, which has nothing
	// to do with whether a replaced module is reported correctly.
	var goComponents []sbomComponent
	for _, c := range doc.Components {
		if strings.HasPrefix(c.PURL, "pkg:golang/") {
			goComponents = append(goComponents, c)
		}
	}

	if len(goComponents) != 1 {
		t.Fatalf("built %d Go components, want 1", len(goComponents))
	}

	c := goComponents[0]
	if c.Name != "example.com/fork" || c.Version != "v1.0.1" {
		t.Errorf("the component names %s@%s rather than what was built", c.Name, c.Version)
	}
	if !strings.Contains(c.Description, "example.com/original@v1.0.0") {
		t.Errorf("the substitution is not recorded, so the replaced version is invisible: %q", c.Description)
	}
}
