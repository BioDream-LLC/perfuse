package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// A bill of materials, because the security team is a decision maker.
//
// This is not a compliance box-tick. An integration engine gets installed by whoever runs the hospital's
// middleware, but it gets *blocked* by whoever reviews it, and that person's question is always the same:
// what is in it, and what do I have to patch. A Java engine answers that with a few hundred jars and a
// steady stream of advisories. Perfuse answers it with a list that fits on one screen.
//
// So the list is worth printing, and worth printing in a format a scanner already reads.
//
// # Read from the binary, not from go.mod
//
// debug.ReadBuildInfo reports what was actually linked into this binary, including versions resolved
// through the module graph. go.mod says what was asked for. Those differ, and the reviewer cares about
// the first one - a bill of materials that describes a build other than the one in front of them is
// worse than none, because it will be trusted.

const cycloneDXVersion = "1.5"

type sbomDocument struct {
	BOMFormat   string          `json:"bomFormat"`
	SpecVersion string          `json:"specVersion"`
	Version     int             `json:"version"`
	Metadata    sbomMetadata    `json:"metadata"`
	Components  []sbomComponent `json:"components"`
}

type sbomMetadata struct {
	Timestamp string        `json:"timestamp"`
	Tools     []sbomTool    `json:"tools"`
	Component sbomComponent `json:"component"`

	// Properties carry the two facts a reviewer actually wants and no standard field holds: that the
	// binary is statically linked with no C dependencies, and that no machine-learning model is
	// embedded. Both close a line of questioning in one sentence.
	Properties []sbomProperty `json:"properties,omitempty"`
}

type sbomTool struct {
	Vendor  string `json:"vendor"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type sbomComponent struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`

	// PURL is the package URL, which is what a scanner matches against an advisory database. Without it
	// the document is a list of names a human has to look up.
	PURL string `json:"purl,omitempty"`

	// Scope distinguishes what Perfuse calls directly from what those dependencies pulled in. A
	// reviewer reads the direct list and skims the rest, and flattening the two makes eight
	// dependencies look like twenty-eight.
	Scope string `json:"scope,omitempty"`

	// Description records that this component replaced another module.
	//
	// The replacement is what is in the binary, so naming it is right - but naming it silently is not. A reviewer
	// checking "do you ship the vulnerable version of X" against a document that lists only Y will conclude X is
	// absent, and the version they were asking about is the one that was replaced away. So the substitution is
	// stated rather than left as a difference between this document and go.mod.
	Description string `json:"description,omitempty"`
}

type sbomProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func cmdSBOM(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sbom", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		asJSON = fs.Bool("json", false, "emit CycloneDX JSON instead of prose")
		asSPDX = fs.Bool("spdx", false, "emit SPDX 2.3 tag-value instead of prose")
		direct = fs.Bool("direct", false, "list only dependencies Perfuse calls itself")
	)

	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage of sbom:
  perfuse sbom [flags]

Lists everything linked into this binary. Read from the build itself rather than
from go.mod, so it describes the binary in front of you and not what someone
asked for.

  perfuse sbom                 # readable list, direct dependencies marked
  perfuse sbom -direct         # only what Perfuse calls itself
  perfuse sbom -json           # CycloneDX 1.5, for a scanner
  perfuse sbom -spdx           # SPDX 2.3 tag-value, which some procurement asks for

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		// Only happens in an unusual build. Saying so beats printing an empty list that reads like a
		// binary with no dependencies at all.
		return fmt.Errorf("this binary carries no build information, so its dependencies cannot be read")
	}

	doc := buildSBOM(info)

	// Both formats at once is refused rather than one silently winning.
	//
	// A script that passes both has a bug in it, and picking one hides the bug until somebody notices the wrong
	// document went to a customer.
	if *asJSON && *asSPDX {
		return fmt.Errorf("-json and -spdx are different documents; ask for one")
	}

	if *asSPDX {
		// -direct is deliberately honoured here too. An SPDX document listing only the direct dependencies is a
		// smaller claim, not a wrong one, and refusing the combination would be arbitrary.
		_, err := io.WriteString(stdout, spdxTagValue(doc, *direct))

		return err
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}

	writeSBOMProse(stdout, doc, *direct)
	return nil
}

func buildSBOM(info *debug.BuildInfo) sbomDocument {
	directNames := directDependencies()

	doc := sbomDocument{
		BOMFormat:   "CycloneDX",
		SpecVersion: cycloneDXVersion,
		Version:     1,
		Metadata: sbomMetadata{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Tools: []sbomTool{{
				Vendor:  "Perfuse",
				Name:    "perfuse sbom",
				Version: buildVersion(),
			}},
			Component: sbomComponent{
				Type:    "application",
				Name:    "perfuse",
				Version: buildVersion(),
				PURL:    "pkg:golang/github.com/biodream-llc/perfuse@" + buildVersion(),
			},
			Properties: []sbomProperty{
				{Name: "perfuse:go", Value: runtime.Version()},
				{Name: "perfuse:cgo", Value: "disabled"},
				// Stated explicitly because it is the shortest possible answer to a question every
				// hospital security review now asks.
				{Name: "perfuse:embeddedModel", Value: "none"},
			},
		},
	}

	for _, dep := range info.Deps {
		if dep == nil || dep.Path == "" {
			continue
		}

		// A replaced module is reported as what was actually built, not as what it replaced. The
		// replacement is the code in the binary, and that is what a scanner has to match.
		mod := dep
		if mod.Replace != nil {
			mod = mod.Replace
		}

		scope := "optional"
		if directNames[dep.Path] {
			scope = "required"
		}

		component := sbomComponent{
			Type:    "library",
			Name:    mod.Path,
			Version: mod.Version,
			PURL:    "pkg:golang/" + mod.Path + "@" + mod.Version,
			Scope:   scope,
		}
		if dep.Replace != nil {
			component.Description = "replaces " + dep.Path + "@" + dep.Version
		}

		doc.Components = append(doc.Components, component)
	}

	// The browser bundle's dependencies.
	//
	// These were absent, and their absence made the document wrong rather than merely short. The binary embeds
	// internal/web/dist, so this code ships inside it and runs in an operator's browser - a reviewer matching the binary
	// against advisories would have found React and CodeMirror nowhere in the bill of materials for a product that serves
	// them. Every other check in this file passed while a third of the shipped code was undescribed.
	//
	// Runtime dependencies only. A test runner or a bundler is used to produce the artefact and is not in it, and listing
	// them would make the document answer a different question from the one a reviewer is asking.
	//
	// Versions are written down because there is no npm equivalent of debug.ReadBuildInfo: the Go modules above report what
	// was actually linked, and this cannot. A test compares both name and version against web/package.json, so a bump has
	// to be a deliberate edit here rather than something that drifts quietly.
	for _, dep := range webDependencies() {
		doc.Components = append(doc.Components, sbomComponent{
			Type:    "library",
			Name:    dep.name,
			Version: dep.version,
			PURL:    "pkg:npm/" + dep.name + "@" + dep.version,
			Scope:   "required",
		})
	}

	sort.SliceStable(doc.Components, func(i, j int) bool {
		a, b := doc.Components[i], doc.Components[j]
		// Direct dependencies first: a reviewer reads that list and skims the rest, and burying the
		// eight things Perfuse actually chose among twenty-eight resolved modules hides the point.
		if a.Scope != b.Scope {
			return a.Scope == "required"
		}
		return a.Name < b.Name
	})

	return doc
}

// webDependency is one npm package that ends up inside the embedded bundle.
type webDependency struct {
	name    string
	version string
}

// webDependencies is what the browser bundle is built from.
//
// Runtime only, and pinned exactly. Kept here rather than read from package.json because the SBOM is produced by the
// compiled binary, which does not carry the repository - and because a written list can say why each entry is present,
// which a parsed one cannot. formscriptslots_test.go and the SBOM test keep it equal to package.json.
func webDependencies() []webDependency {
	return []webDependency{
		// The editor. Six packages rather than one because CodeMirror 6 is deliberately modular: the language modes, the
		// linter and the completion engine are separate so a consumer takes only what it uses.
		//
		// legacy-modes carries the Lua mode. Highlighting Lua as JavaScript was actively misleading - it reads a comment
		// as a decrement - and this is the official package rather than a third-party grammar.
		{"@codemirror/autocomplete", "6.18.4"},
		{"@codemirror/lang-javascript", "6.2.2"},
		{"@codemirror/language", "6.10.8"},
		{"@codemirror/legacy-modes", "6.5.1"},
		{"@codemirror/lint", "6.8.4"},
		{"@codemirror/state", "6.5.0"},
		{"@codemirror/theme-one-dark", "6.1.2"},
		{"@codemirror/view", "6.36.1"},
		{"codemirror", "6.0.1"},

		// The interface itself.
		{"react", "18.3.1"},
		{"react-dom", "18.3.1"},

		// State, for the parts of the interface that several panels read at once.
		{"@reduxjs/toolkit", "2.5.0"},
		{"react-redux", "9.2.0"},
	}
}

// directDependencies is the set Perfuse calls itself.
//
// Written down rather than derived, because build information does not distinguish direct from indirect
// and the distinction is the whole argument. It is checked by a test against go.mod, so it cannot drift
// silently - which is the only reason a hand-maintained list is acceptable here.
func directDependencies() map[string]bool {
	return map[string]bool{
		// The WebAssembly runtime, for scripts a site brings compiled rather than writes in one of ours.
		//
		// wazero rather than wasmtime or wasmer because it is pure Go with no cgo: the binary stays a single static file that
		// cross-compiles to Windows and arm64 the way the rest of this does, and a hospital deploying it does not acquire a
		// C toolchain dependency. That is worth more here than the last few percent of execution speed, because the workload
		// is one transformation per message rather than a compute kernel.
		//
		// It also has no host imports unless given them, which is the property the feature rests on: a module reaches nothing
		// that was not handed to it, so the sandbox is the default rather than a configuration.
		"github.com/tetratelabs/wazero": true,

		// SMB2 and SMB3 for Windows file shares. Deliberately not SMB1, which is how several hospital ransomware
		// outbreaks spread. The maintained fork rather than the original, which has tagged releases but has not been
		// touched in years.
		"github.com/cloudsoda/go-smb2": true,

		"github.com/dop251/goja":          true,
		"github.com/yuin/gopher-lua":      true,
		"github.com/go-sql-driver/mysql":  true,
		"github.com/lib/pq":               true,
		"github.com/microsoft/go-mssqldb": true,
		"github.com/pkg/sftp":             true,
		"golang.org/x/crypto":             true,

		// Promoted from indirect when the SMB client arrived. Listed as direct because go.mod now says so, and a bill of
		// materials that described it as something a dependency pulled in would be wrong about who chose it.
		"golang.org/x/sys": true,

		// Serial ports, for equipment that will never get an Ethernet port: a blood gas analyser with a working sensor
		// and a 25-pin socket, a bedside monitor, a scale in a dialysis unit. A tagged release, unlike the SMB client.
		"go.bug.st/serial": true,

		"gopkg.in/yaml.v3":   true,
		"modernc.org/sqlite": true,
	}
}

func writeSBOMProse(w io.Writer, doc sbomDocument, directOnly bool) {
	var required, optional []sbomComponent
	for _, c := range doc.Components {
		if c.Scope == "required" {
			required = append(required, c)
		} else {
			optional = append(optional, c)
		}
	}

	fmt.Fprintf(w, "perfuse %s, built with %s\n", doc.Metadata.Component.Version, goVersionOf(doc))
	fmt.Fprintf(w, "statically linked, no C dependencies, no embedded model\n\n")

	fmt.Fprintf(w, "%d dependenc%s Perfuse calls directly:\n", len(required), plural2(len(required)))
	for _, c := range required {
		fmt.Fprintf(w, "  %-40s %s\n", c.Name, c.Version)
	}

	if directOnly {
		fmt.Fprintf(w, "\n%d further module(s) were pulled in by those. Run without -direct to list them.\n",
			len(optional))
		return
	}

	fmt.Fprintf(w, "\n%d module%s pulled in by those:\n", len(optional), plural3(len(optional)))
	for _, c := range optional {
		fmt.Fprintf(w, "  %-40s %s\n", c.Name, c.Version)
	}

	fmt.Fprintf(w, "\n%d total.\n", len(doc.Components))
}

func goVersionOf(doc sbomDocument) string {
	for _, p := range doc.Metadata.Properties {
		if p.Name == "perfuse:go" {
			return p.Value
		}
	}
	return runtime.Version()
}

func plural2(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func plural3(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sbomPURLSafe reports whether a module path can appear in a package URL unescaped.
//
// Kept as a named check rather than inlined because a malformed purl makes a scanner skip the component
// silently, which is worse than a document it rejects outright.
func sbomPURLSafe(path string) bool {
	return !strings.ContainsAny(path, " \t\"'<>#?")
}

// spdxTagValue renders the SPDX 2.3 tag-value form.
//
// A second format rather than only CycloneDX because procurement processes ask for SPDX by name, and a supplier who can produce only the other
// one ends up filling in a spreadsheet by hand. Both describe the same build; neither is derived from the other, so both come from buildSBOM.
func spdxTagValue(doc sbomDocument, directOnly bool) string {
	var b strings.Builder

	b.WriteString("SPDXVersion: SPDX-2.3\n")
	b.WriteString("DataLicense: CC0-1.0\n")
	b.WriteString("SPDXID: SPDXRef-DOCUMENT\n")
	fmt.Fprintf(&b, "DocumentName: perfuse-%s\n", doc.Metadata.Component.Version)
	// Derived from the content rather than random, so building the same binary twice produces the same document. A random
	// namespace makes every SBOM differ from the last one, which defeats the main thing anybody does with a series of
	// them: diffing to see what changed.
	fmt.Fprintf(&b, "DocumentNamespace: https://github.com/biodream-llc/perfuse/sbom/%s\n", sbomFingerprint(doc))
	fmt.Fprintf(&b, "Creator: Tool: perfuse-sbom-%s\n", doc.Metadata.Component.Version)
	fmt.Fprintf(&b, "Created: %s\n", doc.Metadata.Timestamp)
	b.WriteString("\n")

	fmt.Fprintf(&b, "PackageName: %s\n", doc.Metadata.Component.Name)
	b.WriteString("SPDXID: SPDXRef-Package-perfuse\n")
	fmt.Fprintf(&b, "PackageVersion: %s\n", doc.Metadata.Component.Version)
	b.WriteString("PackageDownloadLocation: https://github.com/biodream-llc/perfuse\n")
	// False, and stated rather than omitted. FilesAnalyzed: true asserts that every file in the package was examined and
	// that a verification code covers them, which is not what this does - it lists modules.
	b.WriteString("FilesAnalyzed: false\n")
	b.WriteString("\n")

	n := 0
	for _, c := range doc.Components {
		if directOnly && c.Scope != "required" {
			continue
		}
		n++

		id := fmt.Sprintf("SPDXRef-Package-%d", n)

		fmt.Fprintf(&b, "PackageName: %s\n", c.Name)
		fmt.Fprintf(&b, "SPDXID: %s\n", id)
		fmt.Fprintf(&b, "PackageVersion: %s\n", c.Version)
		fmt.Fprintf(&b, "PackageDownloadLocation: https://proxy.golang.org/%s/@v/%s.zip\n", c.Name, c.Version)
		b.WriteString("FilesAnalyzed: false\n")
		if c.PURL != "" {
			fmt.Fprintf(&b, "ExternalRef: PACKAGE-MANAGER purl %s\n", c.PURL)
		}
		if c.Description != "" {
			fmt.Fprintf(&b, "PackageComment: %s\n", c.Description)
		}
		fmt.Fprintf(&b, "Relationship: SPDXRef-Package-perfuse DEPENDS_ON %s\n", id)
		b.WriteString("\n")
	}

	return b.String()
}

// sbomFingerprint derives a stable identifier from the components.
//
// The timestamp is deliberately excluded. Including it would make two documents describing the same binary carry different identifiers, which
// is the opposite of what an identifier is for here.
func sbomFingerprint(doc sbomDocument) string {
	h := sha256.New()

	fmt.Fprintf(h, "%s\n%s\n", doc.Metadata.Component.Name, doc.Metadata.Component.Version)
	for _, c := range doc.Components {
		fmt.Fprintf(h, "%s\n%s\n%s\n", c.Name, c.Version, c.Description)
	}

	return hex.EncodeToString(h.Sum(nil))[:32]
}
