package manual

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sharedSections are written once and kept in two places: the reference (docs/reference.md, read on GitHub) and the
// manual's chapters (the PDF and the copy the server serves). Copies drift: a fix made in one is missing from the other,
// and a reader of the manual follows advice the reference has already corrected.
var sharedSections = map[string]string{
	"Shared mapping tables":   "transformation.md",
	"AS2":                     "delivery.md",
	"OpenPGP":                 "delivery.md",
	"UN/EDIFACT":              "formats.md",
	"The patient index (MPI)": "formats.md",
}

// sharedRenamed are sections whose heading differs between the two (reference title, chapter title, chapter).
var sharedRenamed = [][3]string{
	{"Running two instances as one (a cluster)", "Running two instances", "operations.md"},
}

var headingLine = regexp.MustCompile(`^(#{2,6}) (.+)$`)

// section is the text under a heading, to the next heading of the same or a higher level, with every heading inside it
// written at one level relative to the first: the reference nests a section one level deeper than a chapter does.
func section(t *testing.T, path, title string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	level := 0
	var out []string
	for _, l := range lines {
		m := headingLine.FindStringSubmatch(l)
		if level == 0 {
			if m != nil && strings.TrimSpace(m[2]) == title {
				level = len(m[1])
			}
			continue
		}
		if m != nil {
			if len(m[1]) <= level {
				break
			}
			l = strings.Repeat("#", len(m[1])-level) + " " + m[2]
		}
		out = append(out, l)
	}
	if level == 0 {
		t.Errorf("%s has no section %q", path, title)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func TestSectionsInTheReferenceAndTheManualAgree(t *testing.T) {
	ref := filepath.Join("..", "..", "docs", "reference.md")
	type pair struct{ refTitle, chTitle, chapter string }
	var pairs []pair
	for title, chapter := range sharedSections {
		pairs = append(pairs, pair{title, title, chapter})
	}
	for _, r := range sharedRenamed {
		pairs = append(pairs, pair{r[0], r[1], r[2]})
	}
	for _, p := range pairs {
		title, chapter := p.refTitle, p.chapter
		a := section(t, ref, p.refTitle)
		b := section(t, filepath.Join("chapters", chapter), p.chTitle)
		if a == b {
			continue
		}
		al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
		i := 0
		for i < len(al) && i < len(bl) && al[i] == bl[i] {
			i++
		}
		at := func(l []string) string {
			if i < len(l) {
				return l[i]
			}
			return "(end)"
		}
		t.Errorf("%q differs between docs/reference.md and chapters/%s from line %d:\n  reference: %s\n  manual:    %s\n"+
			"Make the same change in both.", title, chapter, i+1, at(al), at(bl))
	}
}
