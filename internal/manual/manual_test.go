package manual

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// buildForTest assembles the real manual, so the tests below check the document that ships.
func buildForTest(t *testing.T) (*Doc, []Component) {
	t.Helper()

	prose, err := LoadProse()
	if err != nil {
		t.Fatalf("loading chapters: %v", err)
	}
	components, err := Extract(filepath.Join("..", "config"))
	if err != nil {
		t.Fatalf("extracting config: %v", err)
	}
	// The real package documentation, not nil. Building a different document here than the one that ships would mean
	// the cross-reference test validated anchors that do not exist in the published manual - and it did, before this:
	// with nil packages the component chapter is absent, every later chapter shifts by one, and the index anchor the
	// prose linked to was reported broken when it was correct.
	packages, err := CollectPackageDocs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("collecting package docs: %v", err)
	}
	doc, err := Build("test", prose, components, packages)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	return doc, components
}

// The guardrail that stops the manual drifting away from the code.
//
// Without this the manual is a snapshot: somebody adds a configuration key, the manual does not mention it, and the
// manual's silence now means two different things - the option does not exist, and nobody updated the document. A
// reader cannot tell which, so the whole document stops being trustworthy at once rather than gradually.
func TestEveryConfigurationKeyIsDocumented(t *testing.T) {
	doc, components := buildForTest(t)

	html := doc.HTML()

	var missing []string
	for _, c := range components {
		if len(c.Entries) == 0 {
			continue
		}
		for _, e := range c.Entries {
			// The key must appear as a code span somewhere in the rendered manual.
			if !strings.Contains(html, "<code>"+e.Name+"</code>") {
				missing = append(missing, c.Name+"."+e.Name)
			}
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d configuration keys are absent from the manual:\n  %s\n\n"+
			"A key that exists and is undocumented is worse than one that is missing, because the manual's silence "+
			"reads as the option not existing. Either document it or explain in Build why it is excluded.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// Every key must carry an explanation, not merely appear.
//
// Appearing in a table with a type and a default is not documentation: it tells a reader the option exists and nothing
// about when to use it. This is the check that made the difference between 372 and 396 documented keys, and it found a
// real gap - credential and timeout fields on three file transports that had no comment at all.
func TestEveryConfigurationKeyHasAnExplanation(t *testing.T) {
	_, components := buildForTest(t)

	var bare []string
	for _, c := range components {
		for _, e := range c.Entries {
			if strings.TrimSpace(e.Summary) == "" {
				bare = append(bare, c.Name+"."+e.Name)
			}
		}
	}

	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d configuration keys have no summary:\n  %s\n\n"+
			"Add a doc comment to the field in internal/config. If the field is documented jointly with the one above "+
			"it, make sure that comment names this field - see inheritedDoc.",
			len(bare), strings.Join(bare, "\n  "))
	}
}

// linkRe finds the cross-references written by hand in the prose chapters.
var linkRe = regexp.MustCompile(`\]\(#([a-z0-9-]+)\)`)

// Cross-references are written by hand against numbered anchors, so reordering a chapter silently breaks them.
//
// This is the fragile part of the design and the reason it needs a test rather than care. The anchors embed the chapter
// number - s13-testing - because two sections can share a title and a title-only anchor would collide. That makes every
// hand-written link a hostage to the chapter order in Build, and a broken internal link in a 200-page manual is not
// something anybody will notice by reading.
func TestEveryCrossReferenceResolves(t *testing.T) {
	doc, _ := buildForTest(t)

	// Against the resolver the renderer itself uses, which accepts a title slug as well as a numbered anchor. Checking
	// only the numbered anchors would fail every link in the manual, since they are all written as slugs.
	anchors := doc.Anchors()

	prose, err := LoadProse()
	if err != nil {
		t.Fatal(err)
	}

	var broken []string
	names := make([]string, 0, len(prose))
	for name := range prose {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, m := range linkRe.FindAllStringSubmatch(prose[name], -1) {
			if _, ok := anchors[m[1]]; !ok {
				broken = append(broken, fmt.Sprintf("%s.md links to #%s, which does not exist", name, m[1]))
			}
		}
	}

	if len(broken) > 0 {
		t.Errorf("%d cross-references do not resolve:\n  %s\n\n"+
			"Anchors embed the chapter number, so reordering the chapter list in Build invalidates every link into the "+
			"chapters that moved. Fix the links or restore the order.",
			len(broken), strings.Join(broken, "\n  "))
	}
}

// Every chapter named in the reading order must exist, and every chapter file must be used.
//
// The second half is the one that bites: a chapter file that is written but not listed does not appear in the manual and
// produces no error anywhere. Somebody would only find out by looking for it.
func TestEveryChapterFileIsUsedAndEveryUsedChapterExists(t *testing.T) {
	prose, err := LoadProse()
	if err != nil {
		t.Fatal(err)
	}

	// Build fails on a listed chapter with no file, which covers one direction.
	doc, components := buildForTest(t)
	_ = components

	titles := map[string]bool{}
	for _, c := range doc.Chapters {
		titles[c.Title] = true
	}

	var orphaned []string
	for name, src := range prose {
		// The first heading is the chapter title.
		var title string
		for _, l := range strings.Split(src, "\n") {
			if strings.HasPrefix(l, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(l, "# "))
				break
			}
		}
		if title == "" {
			orphaned = append(orphaned, name+".md has no title line")
			continue
		}
		if !titles[title] {
			orphaned = append(orphaned, name+".md ("+title+") is not in the manual")
		}
	}

	if len(orphaned) > 0 {
		sort.Strings(orphaned)
		t.Errorf("%d chapter files are not part of the manual:\n  %s\n\n"+
			"Add the name to the written list in Build, or delete the file. A chapter that exists and is not included "+
			"appears nowhere and reports nothing.",
			len(orphaned), strings.Join(orphaned, "\n  "))
	}
}

// The rendered HTML must be a single self-contained file.
//
// The manual is meant to be mailable and openable from a memory stick with no network. A linked stylesheet or an
// external font would break that quietly - the page still renders, just as unstyled text, which looks like a broken
// document rather than a missing file.
//
// Checked by the rel of each link rather than by the presence of the string "<link ", which was the earlier test and
// was broader than this reasoning. A rel="canonical" is metadata that no browser fetches, and the manual needs one
// because it is published on the web as well as shipped in every release archive: without it the copy somebody
// unpacks into a web root competes with the published one. Anything that causes a fetch is still refused.
func TestTheManualIsSelfContained(t *testing.T) {
	doc, _ := buildForTest(t)
	html := doc.HTML()

	for _, forbidden := range []string{"<script", "src=\"http", "@import", "url(http"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("the manual contains %q, so it depends on something outside the file", forbidden)
		}
	}

	// Only rels that are never fetched are allowed. stylesheet, preload, prefetch, icon and the rest all cause a
	// request, which is the thing this test exists to prevent.
	allowed := map[string]bool{"canonical": true}
	for _, tag := range regexp.MustCompile(`<link [^>]*>`).FindAllString(html, -1) {
		rel := regexp.MustCompile(`rel="([^"]*)"`).FindStringSubmatch(tag)
		if rel == nil {
			t.Errorf("a link tag with no rel: %s", tag)
			continue
		}
		if !allowed[rel[1]] {
			t.Errorf("the manual has a link with rel=%q, which a browser will try to fetch: %s", rel[1], tag)
		}
	}

	if !strings.Contains(html, "<style>") {
		t.Error("the manual has no embedded stylesheet")
	}
}

// A note written in the prose must render as a note.
//
// The notes carry the things that bite - that set takes a block, that clear and remove differ, that NCPDP start markers
// are not separators. In a 200-page document an inline warning in body text is not seen at all, so if the styling
// silently stopped applying the warnings would still be present and would still not be read.
func TestNotesRenderAsNotes(t *testing.T) {
	doc, _ := buildForTest(t)
	html := doc.HTML()

	if !strings.Contains(html, `class="note"`) {
		t.Fatal("no notes rendered; the prose contains lines beginning with > that should have become notes")
	}

	// And the marker must not survive into the output as literal text.
	if strings.Contains(html, "<p>&gt; ") {
		t.Error("a note marker rendered as literal text, so a warning is displayed as a stray angle bracket")
	}
}

// Tables must render as tables, since the reference chapters are mostly tables.
func TestReferenceTablesRender(t *testing.T) {
	doc, _ := buildForTest(t)
	html := doc.HTML()

	if !strings.Contains(html, "<table>") || !strings.Contains(html, "<th>") {
		t.Fatal("no tables rendered; every reference chapter emits one per component")
	}
	// The separator row must not become a row of dashes.
	if strings.Contains(html, "<td>---</td>") {
		t.Error("a table separator row rendered as content")
	}
}

// An unmatched delimiter must not turn the rest of the manual into code.
func TestAnUnmatchedBacktickIsLeftAlone(t *testing.T) {
	got := renderProse("A stray ` backtick and then ordinary text.", nil)
	if strings.Contains(got, "<code>") {
		t.Errorf("an unmatched backtick opened a code span: %q", got)
	}
}

// A heading inside a code fence is content, not structure.
//
// Without this a YAML comment starting with ## would silently split a chapter in two, and the manual would gain a
// section named after a comment.
func TestAHeadingInsideACodeFenceDoesNotSplitTheChapter(t *testing.T) {
	ch, err := parseProseChapter("# Title\n\n## Real Section\n\n```yaml\n## not a heading\nkey: value\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Sections) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(ch.Sections), ch.Sections)
	}
	if !strings.Contains(ch.Sections[0].Body, "## not a heading") {
		t.Error("the fenced comment was consumed as structure rather than kept as content")
	}
}

// The generated files must be current.
//
// Checked rather than assumed because the manual is committed, and a committed artefact that is stale is worse than one
// that is absent: it will be read, and it will be wrong.
func TestTheCommittedManualIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "manual", "perfuse-manual.html")

	committed, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no committed manual to compare against: %v", err)
	}

	doc, _ := buildForTest(t)

	// The version string differs between a build and this test, so compare from after it rather than the whole file.
	//
	// Anchored on the contents heading, not on the nav. The title block carries the build hash and now sits inside the nav
	// so it stays on screen while scrolling, which put the one volatile string in the file back inside the compared region
	// and made this fail on every run regardless of whether the manual was current.
	bodyOf := func(s string) string {
		i := strings.Index(s, "<h2>Table of Contents</h2>")
		if i < 0 {
			return s
		}
		return s[i:]
	}

	if bodyOf(string(committed)) != bodyOf(doc.HTML()) {
		t.Error("docs/manual/perfuse-manual.html is out of date. Run: make docs")
	}
}

// Building the manual twice must produce the same bytes.
//
// Tested explicitly rather than relying on the staleness check to notice. A nondeterministic build makes that check fail
// roughly half the time, which presents as a flaky test and gets rerun until it passes rather than investigated. This
// one names the cause.
//
// It has been needed: package comments were read from a map, and three package names occur twice in this codebase, so
// two builds an hour apart genuinely differed.
func TestTheManualBuildsIdenticallyEveryTime(t *testing.T) {
	first, _ := buildForTest(t)
	second, _ := buildForTest(t)

	a, b := first.HTML(), second.HTML()
	if a == b {
		return
	}

	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			lo := i - 100
			if lo < 0 {
				lo = 0
			}
			t.Fatalf("two builds of the same source differ at byte %d\nfirst:  %q\nsecond: %q",
				i, a[lo:min(i+140, len(a))], b[lo:min(i+140, len(b))])
		}
	}
	t.Fatalf("two builds differ in length: %d and %d", len(a), len(b))
}
