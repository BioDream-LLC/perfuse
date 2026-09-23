package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// An image whose alt text does not match the image is a silent accessibility defect: a sighted
// reader sees one thing, a screen reader announces another, and nothing anywhere reports the
// disagreement. The capability chips are the case that prompted this - the first version of the
// chip row described them from memory, so cda.svg reads "Documents" and was announced as "C-CDA
// and CDA", and liveinterface.svg reads "Live changes" and was announced as "Live interface".
//
// The chip is an SVG with its label as text inside it, so the correct alt text is not a matter of
// opinion; it can be read out of the file.

var (
	chipRef   = regexp.MustCompile(`<img src="assets/chips/([a-z0-9]+)\.svg"[^>]*alt="([^"]*)"`)
	chipLabel = regexp.MustCompile(`>([^<>]{2,24})</text>`)
)

func TestChipAltTextMatchesTheChip(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "site", "index.html"))
	if err != nil {
		t.Skipf("landing page not readable from here: %v", err)
	}

	refs := chipRef.FindAllStringSubmatch(string(page), -1)
	if len(refs) == 0 {
		t.Fatal("found no chip images on the landing page. Either the chips were removed or the " +
			"pattern stopped matching, and the second would make this guard pass by checking nothing")
	}

	for _, r := range refs {
		name, alt := r[1], r[2]

		body, err := os.ReadFile(filepath.Join("..", "..", "docs", "assets", "chips", name+".svg"))
		if err != nil {
			t.Errorf("the page references chips/%s.svg, which is not in docs/assets/chips", name)

			continue
		}

		m := chipLabel.FindStringSubmatch(string(body))
		if m == nil {
			t.Errorf("chips/%s.svg has no text element, so its label cannot be checked", name)

			continue
		}

		if want := strings.TrimSpace(m[1]); alt != want {
			t.Errorf("chips/%s.svg reads %q but its alt text says %q. A screen reader should "+
				"announce what the image says", name, want, alt)
		}
	}
}

// TestEveryChipIsUsed keeps the page and the asset directory in step. A chip nobody shows is a
// capability the project drew an icon for and then left off its own front page, which is how the
// fleet view came to be built, working and documented nowhere.
func TestEveryChipIsUsed(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "site", "index.html"))
	if err != nil {
		t.Skipf("landing page not readable from here: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join("..", "..", "docs", "assets", "chips"))
	if err != nil {
		t.Skipf("chips not readable from here: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no chips found; this guard would pass by checking nothing")
	}

	body := string(page)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".svg") {
			continue
		}
		if !strings.Contains(body, "assets/chips/"+e.Name()) {
			t.Errorf("docs/assets/chips/%s exists but the landing page does not show it", e.Name())
		}
	}
}

// TestScreenshotsReferencedOnThePageExist is the same check the build performs, kept as a test so
// it fails in a normal test run rather than only when somebody builds the site. The screenshots
// were absent from the published site for a day because the build dropped them silently.
func TestScreenshotsReferencedOnThePageExist(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "site", "index.html"))
	if err != nil {
		t.Skipf("landing page not readable from here: %v", err)
	}

	shot := regexp.MustCompile(`src="(assets/screens/[a-z0-9/]+\.(?:png|webp))"`)
	refs := shot.FindAllStringSubmatch(string(page), -1)
	if len(refs) == 0 {
		t.Fatal("the landing page shows no screenshots of the product it is selling")
	}

	for _, r := range refs {
		rel := strings.TrimPrefix(r[1], "assets/")
		if _, err := os.Stat(filepath.Join("..", "..", "docs", "assets", rel)); err != nil {
			t.Errorf("the page references %s, which is not in docs/assets", r[1])
		}
	}
}

// TestWebScreenshotsAreCurrent catches the drift that keeping two copies of an image invites.
//
// The site serves WebP because a 3000px PNG is 460 KB and the same picture at the size it is
// displayed is 40 KB; all seven come to 341 KB instead of 3.3 MB. The README cannot use them,
// because GitHub documents PNG, GIF, JPEG and SVG as its image types and does not list WebP. So
// there are two copies, and the failure mode is a screenshot updated in one place: the repository
// shows the new console and the website shows last month's, with nothing reporting the difference.
//
// Git does not preserve modification times, so freshness cannot be judged from the filesystem. The
// generator records the SHA256 of each source PNG instead, and this compares them.
func TestWebScreenshotsAreCurrent(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "assets", "screens")

	manifest, err := os.ReadFile(filepath.Join(dir, "web", "sources.sha256"))
	if err != nil {
		t.Fatalf("no source manifest for the web screenshots: %v. Run: make screens", err)
	}

	recorded := map[string]string{}
	for _, line := range strings.Split(string(manifest), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		recorded[f[1]] = f[0]
	}
	if len(recorded) == 0 {
		t.Fatal("the manifest records no hashes, so this guard would pass by checking nothing. Run: make screens")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("screens not readable from here: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Errorf("reading %s: %v", e.Name(), err)

			continue
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(body))

		want, ok := recorded[e.Name()]
		if !ok {
			t.Errorf("%s has no web-sized copy. Run: make screens", e.Name())

			continue
		}
		if sum != want {
			t.Errorf("%s has changed since its web-sized copy was made, so the website is showing "+
				"an out-of-date picture of the product. Run: make screens", e.Name())
		}

		webp := strings.TrimSuffix(e.Name(), ".png") + ".webp"
		if _, err := os.Stat(filepath.Join(dir, "web", webp)); err != nil {
			t.Errorf("%s is recorded in the manifest but %s is missing. Run: make screens", e.Name(), webp)
		}
	}
}
