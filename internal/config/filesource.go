package config

import (
	"fmt"
	"strings"
)

// FileSource reads files from a directory this server can see.
//
// # The connector a site tries first
//
// An analyser writes results to a folder. A billing system drops a batch overnight. Radiology exports reports to a
// directory somebody mounted years ago. None of it involves a protocol, and an integration engine that cannot read a
// folder fails its evaluation on the first afternoon.
type FileSource struct {
	// Root bounds every path this source touches. Required.
	//
	// Separate from Dir so that move_to and error_dir can be relative and still be contained. Given only Dir, an
	// error_dir of "../failed" would be outside anything the source had been granted, and refusing it would be
	// arbitrary because there would be nothing to refuse it against.
	Root string `yaml:"root"`

	// FollowSymlinks permits reading a file that links outside Root.
	FollowSymlinks bool `yaml:"follow_symlinks,omitempty"`

	FilePoll `yaml:",inline"`
}

// ApplyDefaults fills in what was not set.
func (f *FileSource) ApplyDefaults() { f.applyFilePollDefaults() }

// Validate checks the settings.
//
// Defaults are applied here rather than by the caller, matching the other sources. Worth noting that this means a
// FileSource constructed in code and never validated has a zero poll interval, which a ticker panics on. Every path
// that reaches the engine goes through Validate, and the file source refuses to start without a poll interval for that
// reason.
func (f *FileSource) Validate() error {
	f.ApplyDefaults()

	if strings.TrimSpace(f.Root) == "" {
		return fmt.Errorf("a file source needs root: the directory it is allowed to read. Every other path is " +
			"relative to it, and anything resolving outside it is refused, so that a channel configured from the " +
			"web interface cannot read or delete arbitrary parts of the server")
	}
	return f.validateFilePoll("file source")
}

// Warnings reports settings that are legal but usually mistakes.
func (f *FileSource) Warnings() []string { return f.FilePollWarnings() }
