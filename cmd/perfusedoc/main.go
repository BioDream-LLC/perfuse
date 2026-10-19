// Command perfusedoc builds the Perfuse reference manual.
//
// The HTML is written directly. The PDF is printed from that HTML by a browser, which is why this command does not
// produce it: see docs/manual/print.mjs and the docs target in the Makefile. Printing rather than typesetting means the
// PDF and the HTML cannot disagree about layout, and it needs no LaTeX installed.
//
// Usage:
//
//	perfusedoc -out docs/manual/perfuse-manual.html -version "v0.1"
//	perfusedoc -units docs/manual/i18n/units.json             the English units a translation is made of
//	perfusedoc -lang de -out dist/perfuse-manual.de.html      a translation, from docs/manual/i18n/de.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/manual"
)

func main() {
	out := flag.String("out", "docs/manual/perfuse-manual.html", "where to write the manual")
	version := flag.String("version", "", "version string for the title page")
	root := flag.String("root", ".", "module root")
	units := flag.String("units", "", "write the manual's translatable units (JSON) here, and nothing else")
	lang := flag.String("lang", "", "write the translation into this language, from -translations/<lang>.json")
	translations := flag.String("translations", "docs/manual/i18n", "directory of the translations")
	flag.Parse()

	if err := run(*root, *out, *version, *units, *lang, *translations); err != nil {
		fmt.Fprintln(os.Stderr, "perfusedoc:", err)
		os.Exit(1)
	}
}

func run(root, out, version, units, lang, translations string) error {
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

	// The other languages the manual is published in, linked under its title. The links are to the website, since a server's own
	// /manual serves the English one only.
	names := map[string]string{"en": "English", "de": "Deutsch", "fr": "Français", "nl": "Nederlands", "es": "Español", "it": "Italiano",
		"pt": "Português", "pl": "Polski", "sv": "Svenska", "da": "Dansk", "nb": "Norsk bokmål", "fi": "Suomi"}
	var langs []manual.LanguageLink
	if files, err := filepath.Glob(filepath.Join(translations, "*.json")); err == nil {
		for _, f := range files {
			code := strings.TrimSuffix(filepath.Base(f), ".json")
			if name, ok := names[code]; ok && code != lang {
				langs = append(langs, manual.LanguageLink{Code: code, Name: name, Href: "https://perfuse.health/manual/" + code + "/"})
			}
		}
	}
	if lang != "" && len(langs) > 0 {
		langs = append([]manual.LanguageLink{{Code: "en", Name: "English", Href: "https://perfuse.health/manual/"}}, langs...)
	}

	if units != "" {
		b, _ := json.MarshalIndent(doc.Units(), "", " ")
		if err := os.MkdirAll(filepath.Dir(units), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(units, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s: %d units\n", units, len(doc.Units()))
		return nil
	}
	if lang != "" {
		b, err := os.ReadFile(filepath.Join(translations, lang+".json"))
		if err != nil {
			return fmt.Errorf("the %s translation: %w", lang, err)
		}
		var tr map[string]string
		if err := json.Unmarshal(b, &tr); err != nil {
			return fmt.Errorf("the %s translation: %w", lang, err)
		}
		var missing int
		doc, missing = doc.Translated(lang, tr)
		doc.Canonical += lang + "/"
		fmt.Printf("%s: %d units left in English\n", lang, missing)
	}
	doc.Languages = langs

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
