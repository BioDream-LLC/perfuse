package config

import (
	"fmt"
	"strings"
	"time"
)

// FilePoll is the part of a file-collecting source that has nothing to do with the transport.
//
// Shared by the local, FTP, SMB and WebDAV sources, and by SFTP. Every one of these settings exists because of a way
// files go wrong rather than a way transports differ, which is why they belong together: a site that has worked out the
// right stable_for for its analyser should not have to work it out again when the analyser moves to a share.
type FilePoll struct {
	// Dir is the directory to read, relative to the transport's root where it has one.
	Dir string `yaml:"dir"`

	// Pattern selects files by glob. Empty means every file.
	Pattern string `yaml:"pattern,omitempty"`

	// PollInterval is how often to look.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`

	// AfterRead is delete, move or leave.
	AfterRead string `yaml:"after_read,omitempty"`

	// MoveTo is where a successfully read file goes when AfterRead is move.
	MoveTo string `yaml:"move_to,omitempty"`

	// ErrorDir is where a file that could not be processed goes.
	//
	// Separate from MoveTo deliberately. A directory holding nothing but failures is one somebody can watch, and mixing
	// failures into the archive means the only way to find them is to read every file.
	ErrorDir string `yaml:"error_dir,omitempty"`

	// StableFor is how long a file's size and modification time must be unchanged before it is read.
	StableFor time.Duration `yaml:"stable_for,omitempty"`

	// MaxFileSize bounds one file.
	MaxFileSize int64 `yaml:"max_file_size,omitempty"`

	// Framed says the file uses MLLP framing rather than being a plain run of messages.
	Framed bool `yaml:"framed,omitempty"`

	// Raw treats the whole file as one message regardless of content.
	//
	// The setting that makes this connector useful for anything other than HL7. Without it a CSV batch, an X12 claim
	// file or a PDF is split on lines beginning MSH and produces nothing, and the error says the file is not HL7 - true
	// but not the point, since the file was never meant to be.
	Raw bool `yaml:"raw,omitempty"`

	// BatchSize bounds how many files one poll will read. Zero means no limit.
	//
	// Exists for the first poll after an outage. A directory holding forty thousand files that accumulated overnight
	// will otherwise be read in one pass, which holds the poll loop for as long as it takes and makes the channel look
	// hung. Reading a bounded number per poll keeps the channel responsive and the queue draining visibly.
	BatchSize int `yaml:"batch_size,omitempty"`

	// SortBy orders the files within a poll: name, modified or none.
	//
	// Defaults to name. Ordering matters more than it looks: an ADT stream where A08 updates arrive before the A01
	// admission produces patients that do not exist yet. Directory listing order is arbitrary on most filesystems, so
	// leaving it unsorted means the order changes between polls for no visible reason.
	SortBy string `yaml:"sort_by,omitempty"`
}

// Valid values for AfterRead.
const (
	AfterReadDelete = "delete"
	AfterReadMove   = "move"
	AfterReadLeave  = "leave"
)

// Valid values for SortBy.
const (
	SortByName     = "name"
	SortByModified = "modified"
	SortByNone     = "none"
)

// applyFilePollDefaults fills in what was not set.
func (f *FilePoll) applyFilePollDefaults() {
	if f.Pattern == "" {
		f.Pattern = "*"
	}
	if f.PollInterval == 0 {
		f.PollInterval = 30 * time.Second
	}
	if f.AfterRead == "" {
		// Move rather than delete. A first-time configuration that deletes is a first-time configuration that destroys
		// somebody's files while they are still working out whether the channel is right, and the file is the only
		// copy.
		f.AfterRead = AfterReadMove
	}
	if f.AfterRead == AfterReadMove && f.MoveTo == "" {
		f.MoveTo = "processed"
	}
	if f.ErrorDir == "" {
		f.ErrorDir = "errors"
	}
	if f.StableFor == 0 {
		f.StableFor = 5 * time.Second
	}
	if f.MaxFileSize == 0 {
		f.MaxFileSize = 64 << 20
	}
	if f.SortBy == "" {
		f.SortBy = SortByName
	}
	if f.Dir == "" {
		f.Dir = "."
	}
}

// validateFilePoll checks the transport-independent settings.
func (f *FilePoll) validateFilePoll(what string) error {
	switch f.AfterRead {
	case AfterReadDelete, AfterReadMove, AfterReadLeave:
	default:
		return fmt.Errorf("%s: after_read is %q; it must be delete, move or leave", what, f.AfterRead)
	}

	switch f.SortBy {
	case SortByName, SortByModified, SortByNone:
	default:
		return fmt.Errorf("%s: sort_by is %q; it must be name, modified or none", what, f.SortBy)
	}

	if f.PollInterval < time.Second {
		return fmt.Errorf("%s: poll_interval is %s, which is less than a second. A directory polled that often "+
			"spends more time being listed than read, and on a network transport it is indistinguishable from an "+
			"attack", what, f.PollInterval)
	}

	if f.StableFor < 0 {
		return fmt.Errorf("%s: stable_for cannot be negative", what)
	}
	if f.StableFor >= f.PollInterval*10 {
		return fmt.Errorf("%s: stable_for is %s but poll_interval is %s, so a file would wait at least %d polls "+
			"before being read. One of the two is probably in the wrong unit",
			what, f.StableFor, f.PollInterval, int(f.StableFor/f.PollInterval))
	}

	if f.MaxFileSize < 0 {
		return fmt.Errorf("%s: max_file_size cannot be negative", what)
	}
	if f.BatchSize < 0 {
		return fmt.Errorf("%s: batch_size cannot be negative", what)
	}

	if f.Framed && f.Raw {
		return fmt.Errorf("%s: framed and raw are both set, and they contradict each other. framed reads MLLP "+
			"framing to find message boundaries; raw says the whole file is one message and there are no "+
			"boundaries to find", what)
	}

	if f.AfterRead == AfterReadMove {
		if strings.TrimSpace(f.MoveTo) == "" {
			return fmt.Errorf("%s: after_read is move but move_to is empty, so there is nowhere to move to", what)
		}
		// The check that prevents an infinite loop. A channel whose archive is the directory it reads collects its own
		// archive on the next poll and delivers every message a second time, then a third.
		if samePath(f.MoveTo, f.Dir) {
			return fmt.Errorf("%s: move_to and dir are both %q, so a file would be moved to where it already is "+
				"and read again on the next poll, forever", what, f.Dir)
		}
	}

	if samePath(f.ErrorDir, f.Dir) {
		return fmt.Errorf("%s: error_dir and dir are both %q, so a file that failed would be read again on every "+
			"poll and fail again", what, f.Dir)
	}

	if f.AfterRead == AfterReadMove && samePath(f.MoveTo, f.ErrorDir) {
		return fmt.Errorf("%s: move_to and error_dir are both %q. A directory holding nothing but failures is one "+
			"somebody can watch; mixed in with the archive, the only way to find a failure is to read every file",
			what, f.MoveTo)
	}

	if f.AfterRead == AfterReadLeave {
		// Worth saying out loud rather than leaving somebody to find out. Nothing about the file changes, so the only
		// thing preventing every message being resent is a list held in memory.
		return nil
	}

	return nil
}

// FilePollWarnings reports settings that are legal but usually mistakes.
func (f *FilePoll) FilePollWarnings() []string {
	var out []string

	if f.AfterRead == AfterReadLeave {
		out = append(out, "after_read is leave, so nothing about a file changes once it has been read. The only "+
			"thing preventing every message being processed again is a list held in memory, which a restart "+
			"empties. Use it for a directory somebody else clears out, not as a way of keeping the files")
	}

	if f.StableFor > 0 && f.StableFor < time.Second {
		out = append(out, fmt.Sprintf("stable_for is %s. A file written by a slow producer can easily pause "+
			"longer than that between writes and be read half-finished, and half an HL7 message usually still "+
			"parses", f.StableFor))
	}

	if f.Pattern == "*" {
		out = append(out, "pattern is * so every file is read, including any temporary file a sending system is "+
			"still writing under its final name and anything unrelated that lands in the directory. A pattern "+
			"like *.hl7 is safer")
	}

	if f.BatchSize == 0 {
		out = append(out, "batch_size is unset, so one poll will read every file it finds. After an outage that "+
			"can be tens of thousands of files in a single pass, during which the channel looks hung")
	}

	return out
}

// samePath compares two directory settings the way a filesystem would.
func samePath(a, b string) bool {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.ReplaceAll(s, "\\", "/")
		s = strings.TrimSuffix(s, "/")
		if s == "" {
			s = "."
		}
		return s
	}
	return clean(a) == clean(b)
}
