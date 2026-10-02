package manual

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CollectPackageDocs reads every package's own account of itself.
//
// Walked rather than listed. A hand-maintained list of packages to document is a list that will be missing the one
// added last week, and the resulting gap looks like a package with no documentation rather than a list with an omission.
func CollectPackageDocs(root string) ([]PackageDoc, error) {
	var out []PackageDoc
	seen := map[string]bool{}

	for _, base := range []string{"internal", "hl7"} {
		dir := filepath.Join(root, base)
		if _, err := os.Stat(dir); err != nil {
			continue
		}

		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			// Test fixtures and generated output are not components.
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" {
				return fs.SkipDir
			}
			if seen[p] {
				return nil
			}
			seen[p] = true

			// The manual package documents the manual, which is not a component of the engine.
			if filepath.Base(p) == "manual" {
				return nil
			}

			pd, err := ExtractPackageDoc(p, ImportPathFor(root, p))
			if err != nil || pd == nil {
				// A directory with no package comment is skipped rather than failing the build. Some are single-file
				// helpers where a package comment would restate the filename.
				return nil
			}
			out = append(out, *pd)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// By path, not by name. Three package names occur twice - hl7, winservice and compliance - and sorting on a
	// non-unique key leaves their relative order to the sort's discretion, which is another way the same document ends
	// up differing between builds.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
