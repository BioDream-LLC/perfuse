package manual

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The units a translation is made of are what the manual says now, and every language translates nearly all of them. A unit
// left untranslated is shown in English, so this allows a few (a long table a model would not keep the shape of), not a
// chapter: a new section fails here until web/scripts/i18n-manual.mjs has been run.
func TestTheTranslationsAreCurrent(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "manual", "i18n")
	b, err := os.ReadFile(filepath.Join(dir, "units.json"))
	if err != nil {
		t.Skipf("no translations: %v", err)
	}
	var committed []string
	if err := json.Unmarshal(b, &committed); err != nil {
		t.Fatal(err)
	}
	doc, _ := buildForTest(t)
	if !reflect.DeepEqual(committed, doc.Units()) {
		t.Fatal("docs/manual/i18n/units.json is out of date: run go run ./cmd/perfusedoc -units docs/manual/i18n/units.json, " +
			"then node web/scripts/i18n-manual.mjs")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	langs := 0
	for _, f := range files {
		if strings.HasSuffix(f, "units.json") {
			continue
		}
		langs++
		var tr map[string]string
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &tr); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		translated := 0
		for _, u := range committed {
			if strings.TrimSpace(tr[u]) != "" {
				translated++
			}
		}
		if share := float64(translated) / float64(len(committed)); share < 0.98 {
			t.Errorf("%s translates %.1f%% of the manual; run node web/scripts/i18n-manual.mjs %s", filepath.Base(f), 100*share,
				strings.TrimSuffix(filepath.Base(f), ".json"))
		}
		// Code spans are never translated: the manual's YAML keys and commands are the same in every language.
		for _, u := range committed {
			t2, ok := tr[u]
			if !ok {
				continue
			}
			if spans(u) != spans(t2) {
				t.Errorf("%s changes a code span in %q", filepath.Base(f), u[:min(len(u), 60)])
				break
			}
		}
	}
	if langs < 11 {
		t.Errorf("%d languages, want 11", langs)
	}
	// A translation keeps the English anchors, so a link into the manual works in every language.
	de, _ := doc.Translated("de", map[string]string{doc.Chapters[0].Title: "Erstes Kapitel"})
	if de.Anchors()[slugFor(doc.Chapters[0].Title)] != doc.Anchors()[slugFor(doc.Chapters[0].Title)] {
		t.Error("a translated title changed the chapter's anchor")
	}
	if !strings.Contains(de.HTML(), `<html lang="de">`) {
		t.Error("a translation does not say its language")
	}
}

var codeSpan = regexp.MustCompile("`[^`]+`")

// spans are a text's code spans, sorted, as the translation script compares them.
func spans(s string) string {
	out := codeSpan.FindAllString(s, -1)
	sort.Strings(out)
	return strings.Join(out, "\x00")
}
