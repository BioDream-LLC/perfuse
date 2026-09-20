package manual

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every diagram in the manual is well-formed XML, and says what it is to somebody who cannot see it.
//
// Why this test exists. The renderer emits the contents of an ```svg fence verbatim - it is the only place in the whole renderer that does not
// escape what it was given. That is safe, because these chapters are files in this repository written by people who can already change the
// program, so no boundary is being crossed. But it does mean a malformed diagram becomes broken markup in the published manual rather than an
// error, and broken markup in the middle of a chapter can swallow the rest of the page.
//
// The accessibility half is not decoration. A diagram is the one kind of content in a manual that is completely unavailable to a reader using a
// screen reader unless somebody writes down what it shows. An unlabelled diagram is a blank space in the middle of an explanation.

// svgFences returns every svg code fence in the chapters, with where it came from.
func svgFences(t *testing.T) map[string][]string {
	t.Helper()

	out := map[string][]string{}

	entries, err := os.ReadDir("chapters")
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		raw, err := os.ReadFile(filepath.Join("chapters", e.Name()))
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(string(raw), "\n")
		for i := 0; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "```svg" {
				continue
			}

			var body []string
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
				body = append(body, lines[i])
			}
			out[e.Name()] = append(out[e.Name()], strings.Join(body, "\n"))
		}
	}

	return out
}

func TestEveryDiagramIsWellFormed(t *testing.T) {
	fences := svgFences(t)
	if len(fences) == 0 {
		t.Skip("no diagrams yet")
	}

	for chapter, diagrams := range fences {
		for n, diagram := range diagrams {
			decoder := xml.NewDecoder(strings.NewReader(diagram))
			for {
				_, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("%s diagram %d is not well-formed XML, so it would become broken markup in the manual: %v", chapter, n+1, err)

					break
				}
			}
		}
	}
}

func TestEveryDiagramDescribesItself(t *testing.T) {
	fences := svgFences(t)
	if len(fences) == 0 {
		t.Skip("no diagrams yet")
	}

	for chapter, diagrams := range fences {
		for n, diagram := range diagrams {
			// A viewBox and no fixed width is what makes one drawing work in a browser at any window size and on a printed page, rather than
			// needing a second copy at another size.
			if !strings.Contains(diagram, "viewBox") {
				t.Errorf("%s diagram %d has no viewBox, so it cannot scale to the page it is printed on", chapter, n+1)
			}

			// The label a screen reader reads out. Without it the diagram is a blank space in the middle of an explanation.
			hasLabel := strings.Contains(diagram, "aria-label") || strings.Contains(diagram, "<title")
			if !hasLabel {
				t.Errorf("%s diagram %d has no aria-label or title, so it is unavailable to anybody who cannot see it", chapter, n+1)
			}

			if !strings.Contains(diagram, `role="img"`) {
				t.Errorf("%s diagram %d is missing role=\"img\", so its label may not be announced", chapter, n+1)
			}

			// A label has to say what the diagram shows, not merely that it is a diagram. "Diagram" as a whole label is the same as no label.
			if i := strings.Index(diagram, `aria-label="`); i >= 0 {
				rest := diagram[i+len(`aria-label="`):]
				if j := strings.Index(rest, `"`); j > 0 {
					label := strings.Join(strings.Fields(rest[:j]), " ")
					if len(label) < 25 {
						t.Errorf("%s diagram %d has the label %q, which does not describe what it shows", chapter, n+1, label)
					}
				}
			}
		}
	}
}
