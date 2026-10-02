// Command perfusedoc builds the Perfuse reference manual.
//
// The HTML is written directly. The PDF is printed from that HTML by a browser, which is why this command does not
// produce it: see docs/manual/print.mjs and the docs target in the Makefile. Printing rather than typesetting means the
// PDF and the HTML cannot disagree about layout, and it needs no LaTeX installed.
//
// Usage:
//
//	perfusedoc -out docs/manual/perfuse-manual.html -version "v0.1"
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/biodream-llc/perfuse/internal/manual"
)

func main() {
	out := flag.String("out", "docs/manual/perfuse-manual.html", "where to write the manual")
	version := flag.String("version", "", "version string for the title page")
	root := flag.String("root", ".", "module root")
	flag.Parse()

	if err := run(*root, *out, *version); err != nil {
		fmt.Fprintln(os.Stderr, "perfusedoc:", err)
		os.Exit(1)
	}
}

func run(root, out, version string) error {
	prose, err := manual.LoadProse()
	if err != nil {
		return fmt.Errorf("loading written chapters: %w", err)
	}

	components, err := manual.Extract(filepath.Join(root, "internal", "config"))
	if err != nil {
		return fmt.Errorf("extracting configuration: %w", err)
	}

	packages, err := manual.CollectPackageDocs(root)
	if err != nil {
		return fmt.Errorf("collecting package documentation: %w", err)
	}

	doc, err := manual.Build(version, prose, components, packages)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(doc.HTML()), 0o644); err != nil {
		return err
	}

	chapters, sections, keys := doc.Size()
	fmt.Printf("%s: %d chapters, %d sections, %d documented keys\n", out, chapters, sections, keys)
	return nil
}
