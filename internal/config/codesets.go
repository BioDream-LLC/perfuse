package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"gopkg.in/yaml.v3"
)

// Where a channel finds its shared mapping tables.
//
// A list of files rather than a single one, and relative to the channel, for the same reasons as script
// libraries: tables divide by subject, and a file beside the channels travels with them into version control and
// into a container image.

// loadCodeSets reads the tables a channel references.
func loadCodeSets(files []string, dir string) (*codeset.Set, error) {
	if len(files) == 0 {
		return nil, nil
	}

	set := &codeset.Set{}

	for _, name := range files {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}

		body, err := os.ReadFile(path)
		if err != nil {
			// Refused rather than skipped, like a script library. A missing table means every mapping that
			// referenced it fails to resolve, and the resulting error names a table rather than a file - which
			// sends somebody looking in the wrong place.
			return nil, fmt.Errorf("tables: %w", err)
		}

		var loaded codeset.Set
		dec := yaml.NewDecoder(strings.NewReader(string(body)))
		// Unknown keys refused, as everywhere. A misspelled key in a mapping table would silently drop a row,
		// and a dropped row is a value that passes through unmapped - which looks like valid data at the far
		// end.
		dec.KnownFields(true)
		if err := dec.Decode(&loaded); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		set.Tables = append(set.Tables, loaded.Tables...)
	}

	if errs := set.Validate(); len(errs) > 0 {
		var lines []string
		for _, e := range errs {
			lines = append(lines, e.Error())
		}
		return nil, fmt.Errorf("the mapping tables are not valid:\n  %s", strings.Join(lines, "\n  "))
	}

	set.Compile()
	return set, nil
}

// TableFileFor reports which of this channel's table files defines the named table.
//
// The loaded Set merges every file, so the origin has to be recovered by reading each one separately. Worth the
// work: telling somebody a table lives in codes.codeset.yaml is the difference between a warning they can act on and
// one they have to go and investigate.
//
// Returns the base name, or "" if the table is not found in any of them.
func (c *Channel) TableFileFor(name string) string {
	dir := filepath.Dir(c.path)
	for _, file := range c.Tables {
		set, err := loadCodeSets([]string{file}, dir)
		if err != nil || set == nil {
			continue
		}
		if _, found := set.Table(name); found {
			return filepath.Base(file)
		}
	}
	return ""
}

// CodeSets returns the shared mapping tables this channel loaded, or nil.
//
// Exported so a specification can print them. A document that says a field is mapped without saying how, or by
// whom, invites the receiving team to argue with the mapping - and the answer to that argument is usually
// "because you asked us to, in 2019".
func (c *Channel) CodeSets() *codeset.Set { return c.tables }
