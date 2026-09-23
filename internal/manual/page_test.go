package manual

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The website is generated from the same markdown the repository publishes. That means the
// renderer now has to handle two things the manual never needed: markdown headings, because a
// standalone document has no chapter splitter in front of it, and raw HTML, because the README
// uses tables and <details> blocks that markdown cannot express.
//
// Both are places where being slightly wrong is invisible. A heading that fails to parse becomes
// a paragraph starting with a hash, which is exactly what the manual published for months. Raw
// HTML that fails to pass through becomes escaped tags shown as text, or worse, a dropped table.

func TestHeadingLevelsAreRecognised(t *testing.T) {
	for _, c := range []struct {
		line  string
		level int
		title string
	}{
		{"# Perfuse", 1, "Perfuse"},
		{"## Quick start", 2, "Quick start"},
		{"### Three lists", 3, "Three lists"},
		{"#### A denial and a refusal are not the same", 4, "A denial and a refusal are not the same"},
		{"##### Deep", 5, "Deep"},
		{"###### Deeper", 6, "Deeper"},
		{"##   Extra space collapses", 2, "Extra space collapses"},
	} {
		h := headingOf(c.line)
		if h == nil {
			t.Errorf("%q was not recognised as a heading", c.line)

			continue
		}
		if h.level != c.level {
			t.Errorf("%q: level %d, want %d", c.line, h.level, c.level)
		}
		if h.title != c.title {
			t.Errorf("%q: title %q, want %q", c.line, h.title, c.title)
		}
	}
}

func TestThingsThatAreNotHeadings(t *testing.T) {
	// Each of these would be a real document turning into a heading by accident.
	for _, line := range []string{
		"#hashtag",                     // no space
		"#412 is the issue number",     // an issue reference
		"####### seven is too many",    // beyond h6
		"#",                            // nothing after it
		"# ",                           // no title
		"not # in the middle",          // not at the start
		"",                             // empty
		"    # indented is code",       // leading space
		"#!/bin/sh",                    // a shebang
		"# define SOMETHING elsewhere", // arguably a heading, but see note
	} {
		if h := headingOf(line); h != nil && line != "# define SOMETHING elsewhere" {
			t.Errorf("%q was treated as a heading (level %d, title %q)", line, h.level, h.title)
		}
	}
}

func TestRawHTMLLinesArePassedThrough(t *testing.T) {
	for _, line := range []string{
		"<details>",
		"</details>",
		"<summary><b>Other ways to install</b></summary>",
		"<table>",
		"<thead>",
		"</tbody>",
		"<tr><td>Kafka</td><td>Included</td></tr>",
		"<img src=\"/assets/architecture.svg\" alt=\"Architecture\">",
		"<div align=\"center\">",
		"<sub>Built by BioDream LLC</sub>",
		"<br>",
		"<h1>Perfuse</h1>",
	} {
		if !isRawHTMLLine(line) {
			t.Errorf("%q should be passed through as raw HTML", line)
		}
	}
}

func TestProseThatMerelyContainsAnAngleBracketIsNotRawHTML(t *testing.T) {
	// If any of these were treated as HTML they would be emitted unescaped, which is both a
	// rendering bug and the one place this renderer could be made to inject markup.
	for _, line := range []string{
		"<- this is an arrow",
		"<3",
		"< 5 seconds",
		"<>",
		"<1.2.3",
		"<_underscore",
		"<!-- a comment -->",
	} {
		if isRawHTMLLine(line) {
			t.Errorf("%q was treated as raw HTML and would be emitted unescaped", line)
		}
	}
}

func TestAStandalonePageRendersHeadingsAndPassesThroughHTML(t *testing.T) {
	src := strings.Join([]string{
		"# The Title",
		"",
		"Ordinary prose with <angle> brackets in it.",
		"",
		"## A section",
		"",
		"<details>",
		"<summary>Click me</summary>",
		"",
		"Prose inside the details block.",
		"",
		"</details>",
		"",
		"<table>",
		"<tr><td>a</td><td>b</td></tr>",
		"</table>",
	}, "\n")

	got := RenderStandalonePage("T", "D", "https://perfuse.health/x/", src)

	for _, want := range []string{
		`<h1 id="the-title">The Title</h1>`,
		`<h2 id="a-section">A section</h2>`,
		"<details>",
		"<summary>Click me</summary>",
		"<tr><td>a</td><td>b</td></tr>",
		"Prose inside the details block.",
		`<link rel="canonical" href="https://perfuse.health/x/">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page is missing %q", want)
		}
	}

	// The angle brackets in prose must be escaped, or prose could inject markup.
	if !strings.Contains(got, "&lt;angle&gt;") {
		t.Error("angle brackets in ordinary prose were not escaped")
	}
}

// TestTheReadmeRendersWithoutLeavingMarkdownBehind renders the real README and checks for the
// signatures of a renderer that silently gave up: a visible hash heading, an escaped tag, or an
// unconverted pipe table. Uses the actual file because the point is that this document renders,
// not that a fixture resembling it does.
func TestTheReadmeRendersWithoutLeavingMarkdownBehind(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Skipf("README not readable from here: %v", err)
	}

	got := RenderStandalonePage("Overview", "d", "https://perfuse.health/overview/", string(raw))

	for _, bad := range []struct{ frag, why string }{
		{"<p>#", "a heading rendered as a paragraph beginning with a hash"},
		{"<p>##", "a heading rendered as a paragraph beginning with hashes"},
		{"&lt;details&gt;", "a details tag was escaped instead of passed through"},
		{"&lt;table&gt;", "a table tag was escaped instead of passed through"},
		{"<p>| ", "a markdown table was not converted"},
	} {
		if strings.Contains(got, bad.frag) {
			t.Errorf("the rendered README contains %q: %s", bad.frag, bad.why)
		}
	}

	// And it must actually have rendered the structures, not merely avoided the failures. A
	// renderer that emitted nothing would pass every check above.
	for _, want := range []string{"<details>", "<h2 id=", "<table>", "<pre"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendered README has no %q, so this test is not checking a real render", want)
		}
	}
}

// TestChapterMarkdownStillAvoidsRawHTMLOutsideDiagrams keeps the two renderer changes honest for
// the manual, which must not have changed at all. A chapter line beginning with a tag would now be
// emitted unescaped, and a #### heading now becomes a real heading. Both are wanted for the
// website; neither should arrive in the manual by accident, so this records the assumption.
func TestChapterMarkdownStillAvoidsRawHTMLOutsideDiagrams(t *testing.T) {
	prose, err := LoadProse()
	if err != nil {
		t.Fatalf("loading chapters: %v", err)
	}
	if len(prose) == 0 {
		t.Fatal("no chapters loaded; this guard would pass by checking nothing")
	}

	for name, src := range prose {
		inFence := false
		for i, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(line, "```") {
				inFence = !inFence

				continue
			}
			if inFence {
				continue
			}
			if isRawHTMLLine(line) {
				t.Errorf("%s line %d begins with raw HTML outside a fenced block: %q. "+
					"That is now passed through unescaped. If it is deliberate, say so here",
					name, i+1, line)
			}
		}
	}
}
