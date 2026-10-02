package manual

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Links in the README and the docs have two audiences reading the same characters: somebody on
// github.com and somebody on perfuse.health. A link can work for one and fail for the other, and
// the failure is silent because the link still resolves - it just resolves to the wrong kind of
// thing.
//
// The case that prompted this: the README linked to docs/manual/perfuse-manual.html, which is the
// rendered manual. GitHub does not render HTML files in a repository, it shows the source. So the
// most prominent link on the project's front page - "Manual" - presented a reader with a wall of
// markup instead of the manual, while the same link on the website worked perfectly.

var mdLink = regexp.MustCompile(`\]\(([^)]+)\)`)

// docsWithLinks returns the documents whose links a reader on GitHub will click.
func docsWithLinks(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, p := range []string{"README.md", "CONTRIBUTING.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			continue
		}
		out[p] = string(b)
	}

	entries, err := os.ReadDir(filepath.Join("..", "..", "docs"))
	if err != nil {
		t.Skipf("docs not readable from here: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", e.Name()))
		if err != nil {
			continue
		}
		out["docs/"+e.Name()] = string(b)
	}

	if len(out) == 0 {
		t.Fatal("found no documents to check; this guard would pass by checking nothing")
	}

	return out
}

func TestNoDocumentLinksToRepositoryHTML(t *testing.T) {
	for name, body := range docsWithLinks(t) {
		for _, m := range mdLink.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
				continue
			}
			if strings.HasPrefix(target, "#") {
				continue
			}

			clean := target
			if i := strings.IndexAny(clean, "#?"); i >= 0 {
				clean = clean[:i]
			}
			if !strings.HasSuffix(clean, ".html") && !strings.HasSuffix(clean, ".htm") {
				continue
			}

			t.Errorf("%s links to %q, a repository HTML file. GitHub does not render those, it "+
				"shows the source, so a reader gets markup instead of the document. Link to the "+
				"published copy on the website instead", name, target)
		}
	}
}

// TestRepositoryRelativeLinksExist catches the other half of the same problem: a link that is
// correct in shape and points at nothing. On GitHub such a link produces a 404 page, which reads
// as a broken project rather than a broken link.
func TestRepositoryRelativeLinksExist(t *testing.T) {
	root := filepath.Join("..", "..")

	for name, body := range docsWithLinks(t) {
		base := filepath.Dir(filepath.Join(root, name))

		for _, m := range mdLink.FindAllStringSubmatch(body, -1) {
			target := m[1]
			switch {
			case strings.HasPrefix(target, "http://"),
				strings.HasPrefix(target, "https://"),
				strings.HasPrefix(target, "#"),
				strings.HasPrefix(target, "mailto:"):
				continue
			}

			clean := target
			if i := strings.IndexAny(clean, "#?"); i >= 0 {
				clean = clean[:i]
			}
			if clean == "" {
				continue
			}

			if _, err := os.Stat(filepath.Join(base, clean)); err != nil {
				t.Errorf("%s links to %q, which does not exist in the repository", name, target)
			}
		}
	}
}
