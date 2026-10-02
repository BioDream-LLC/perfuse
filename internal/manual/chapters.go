package manual

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// chapterFS holds the written chapters.
//
// Embedded rather than read from disk so the manual can be produced by the binary wherever it runs, and so the running
// server can serve it. A manual that only exists as a file next to the source is unavailable to the person most likely
// to need it: somebody logged into the web interface at two in the morning wondering what a setting does.
//
//go:embed chapters/*.md
var chapterFS embed.FS

// LoadProse reads the written chapters, keyed by name without extension.
func LoadProse() (map[string]string, error) {
	entries, err := fs.ReadDir(chapterFS, "chapters")
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := chapterFS.ReadFile(path.Join("chapters", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		out[strings.TrimSuffix(e.Name(), ".md")] = string(b)
	}

	return out, nil
}
