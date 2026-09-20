package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Guards against the ways this project's own queue has stopped being true.
//
// Nobody runs a queue, so nothing fails when it becomes wrong - and it became wrong repeatedly. Three distinct shapes turned up, all
// in one day, and all of them mechanical enough to check:
//
//   - A struck-through title beside an open box. Nine entries in August, three more on 17 September. The text said done and the box
//     said open, so a count of remaining work was wrong in whichever direction the reader trusted.
//   - Work recorded only in prose under a heading. A scan for open boxes did not see it, so it was invisible to the only method
//     anybody uses to ask what is left.
//   - An item that existed twice, once open with struck-through text and once closed, for six days.
//
// The first is exactly checkable and is checked below. The others are judgement calls that a test cannot make, so what is enforced is
// the weaker, still useful thing: a section that describes remaining work has to contain a box for it.

// queuePath finds docs/queue.md from wherever the test runs.
func queuePath(t *testing.T) string {
	t.Helper()

	// Walk up until the file is found, so this works from the package directory and from the repository root.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for range 5 {
		candidate := filepath.Join(dir, "queue.md")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		candidate = filepath.Join(dir, "docs", "queue.md")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		dir = filepath.Dir(dir)
	}

	t.Fatal("could not find docs/queue.md")

	return ""
}

func readQueue(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile(queuePath(t))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(data), "\n")

	// A positive control. A guard that silently read an empty file would report agreement about nothing, which is the failure this
	// whole file exists to prevent.
	if len(lines) < 100 {
		t.Fatalf("the queue is %d lines, which is too few to be the real file", len(lines))
	}

	return lines
}

var (
	openBox   = regexp.MustCompile(`^\s*- \[ \]`)
	closedBox = regexp.MustCompile(`^\s*- \[x\]`)
	// Struck through: ~~something~~ anywhere on the line.
	struckThrough = regexp.MustCompile(`~~[^~]+~~`)
)

func TestNoOpenItemHasAStruckThroughTitle(t *testing.T) {
	// The exact shape that misled a reader twelve times.
	//
	// Striking a title through is how somebody marks it done while leaving the text for context. Leaving the box open then makes the
	// entry say two contradictory things, and which one a reader believes decides whether they think there is work left. Three
	// entries were in this state on 17 September and had been for weeks.
	for i, line := range readQueue(t) {
		if openBox.MatchString(line) && struckThrough.MatchString(line) {
			t.Errorf("docs/queue.md:%d has an open box and a struck-through title, so it says both done and not done:\n  %s",
				i+1, strings.TrimSpace(line))
		}
	}
}

func TestEverySectionThatDescribesRemainingWorkHasABoxForIt(t *testing.T) {
	// The weaker check for the second shape, because a test cannot tell prose describing future work from prose describing a past
	// decision.
	//
	// What it can tell is whether a section says something is left and offers nothing to tick. SAML's verification lived exactly
	// there: a heading reading "supported, apart from verification", a paragraph beginning "What is left", and no box anywhere in the
	// section. A reader scanning for open boxes concluded SAML was finished.
	lines := readQueue(t)

	// Phrases that assert work remains. Deliberately few and specific: a broad list would match a sentence explaining why something
	// was not done, and a guard that cries wolf gets an exception list, and then the exception list is the thing that rots.
	claims := []string{
		"what is left",
		"what remains absent",
		"still to do",
		"remains to be done",
	}

	// The front matter before the first heading is skipped. The file's own subtitle is "What is left to do in Perfuse", which the
	// first version of this guard reported as an unticked claim - a guard whose first finding is the document describing itself.
	section := ""
	sectionStart := 1
	sectionHasBox := false
	claimLine := 0
	claimText := ""

	report := func() {
		if section == "" {
			return
		}

		if claimLine > 0 && !sectionHasBox {
			t.Errorf("docs/queue.md:%d says work remains and its section %q (from line %d) has no box to tick, "+
				"so nothing scanning for open items will find it:\n  %s",
				claimLine, section, sectionStart, claimText)
		}
	}

	for i, line := range lines {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			report()

			section = strings.TrimSpace(strings.TrimLeft(line, "# "))
			sectionStart = i + 1
			sectionHasBox = false
			claimLine = 0
			claimText = ""

			continue
		}

		if openBox.MatchString(line) || closedBox.MatchString(line) {
			sectionHasBox = true
		}

		if claimLine == 0 {
			lower := strings.ToLower(line)
			for _, c := range claims {
				if strings.Contains(lower, c) {
					claimLine = i + 1
					claimText = strings.TrimSpace(line)

					break
				}
			}
		}
	}

	report()
}

func TestTheQueueHasNoDuplicatedItemTitles(t *testing.T) {
	// The third shape: one item present twice, once open with struck-through text and once closed, for six days. The queue's own
	// account of that says it is what let a reader be told to delete a package that had been deliberately rebuilt.
	//
	// Compared on the bold title only, because the body text legitimately repeats phrases and the title is what somebody scans.
	titleOf := regexp.MustCompile(`^\s*- \[[ x]\] \*\*(.+?)\*\*`)

	seen := map[string]int{}

	for i, line := range readQueue(t) {
		m := titleOf.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		// The strike marks and any parenthetical date are removed, so the same item written once open and once closed collides
		// rather than passing as two entries - which is precisely how it survived.
		title := strings.ToLower(strings.NewReplacer("~~", "", "*", "").Replace(m[1]))
		if paren := strings.Index(title, " ("); paren > 0 {
			title = title[:paren]
		}

		title = strings.TrimSpace(strings.TrimSuffix(title, "."))
		if title == "" {
			continue
		}

		if first, ok := seen[title]; ok {
			t.Errorf("docs/queue.md has %q at both line %d and line %d; one item written twice is how a closed decision "+
				"goes on reading as open work", title, first, i+1)

			continue
		}

		seen[title] = i + 1
	}

	// Positive control: if the title pattern stopped matching, every check above would pass while reading nothing.
	if len(seen) < 20 {
		t.Errorf("only %d item titles were found, so the pattern is not matching the file's real shape", len(seen))
	}
}
