package manual

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The site deploy must watch every file it publishes.
//
// The workflow copies the manual into the published site, and its path filter listed
// only site/ and docs/assets/. So editing the manual never triggered a deploy: the
// published manual and the PDF served from it updated by accident, whenever something
// in site/ happened to change. Three new chapters sat in the repository while the live
// manual stayed a release behind, with nothing anywhere to indicate the two had
// diverged.
//
// This reads the workflow and checks the two lists against each other: every source
// path the build copies from must appear in the triggers. It is a text check rather
// than a YAML parse on purpose - it has to keep working without adding a dependency,
// and the failure it guards against is a missing line, which text can see.

func TestSiteWorkflowWatchesEverythingItPublishes(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/site.yml")
	if err != nil {
		t.Skipf("site workflow not readable from here: %v", err)
	}
	body := string(raw)

	// Everything the build copies out of the repository. Quoted and unquoted forms both, because
	// a path in quotes is still a path being copied, and a pattern that silently stopped matching
	// is how a guard comes to check nothing.
	cp := regexp.MustCompile(`cp (?:-R )?"?([A-Za-z0-9_./$-]+)"?`)
	matches := cp.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatal("found no cp commands in the site workflow. The pattern has stopped matching, " +
			"which would make this guard pass by checking nothing")
	}

	// The directories named in the push path filter.
	watched := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- '") {
			continue
		}
		p := strings.Trim(strings.TrimPrefix(line, "- "), "'")
		p = strings.TrimSuffix(p, "/**")
		watched[p] = true
	}
	if len(watched) == 0 {
		t.Fatal("found no path filters in the site workflow; this guard is checking nothing")
	}

	for _, m := range matches {
		src := m[1]
		// Only sources read out of the repository matter. The destination is _site.
		if strings.HasPrefix(src, "_site") || src == "." {
			continue
		}
		top := strings.TrimSuffix(strings.SplitN(src, "/", 2)[0], ".")
		if top == "" || top == "_site" {
			continue
		}

		// Accept a watch on the top-level directory or on any prefix of the path.
		ok := false
		for w := range watched {
			if strings.HasPrefix(src, w) || w == top {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("the site workflow copies %q into the published site but no push path "+
				"filter matches it, so changing that file will not deploy. Add it to the "+
				"paths list", src)
		}
	}
}
