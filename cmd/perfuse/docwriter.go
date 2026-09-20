package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/spec"
)

// Writing the interface inventory to a file on a schedule.
//
// The document generator has existed for a while and could only be reached through an authenticated API, one channel at a
// time, by somebody who already knew the channel's name. That is a feature that never leaves the building.
//
// A file changes that in two ways worth having. It can be committed, so the inventory diffs when somebody changes an
// interface and the diff is reviewable by people who will never log in here. And it is readable by somebody who has the
// machine but not the application - which is the situation during an incident, and exactly when nobody wants to be told to
// start the service first.

// channelSource is what the writer reads. An interface so it can be tested without a directory of files.
type channelSource interface {
	// Load returns the channels that loaded and, keyed by file name, the ones that did not with the reason.
	//
	// Both, rather than an error for the second. A directory with one bad file must still produce a document
	// describing the other thirty-nine and naming the one it could not - which is the difference between this and
	// the loader the engine uses, where a site that thinks it runs forty interfaces must not silently run
	// thirty-nine.
	Load() ([]*config.Channel, map[string]string, error)
}

// dirSource reads channels from a directory.
//
// Read fresh on every write rather than held from startup, so the document reflects the channels as they are now. Somebody
// who edits a channel through the interface and then looks at the inventory should see their change; a cached list would
// make the file quietly describe yesterday.
//
// Deliberately not config.LoadDir, which refuses the whole directory if any one file is invalid. That is right for starting
// an engine - a site that thinks it is running forty interfaces must not silently run thirty-nine - and wrong for an
// inventory, where one broken file would mean no document at all. Found by running it: a single unparseable channel
// produced nothing but a warning in the log.
type dirSource struct{ dir string }

func (d dirSource) Load() ([]*config.Channel, map[string]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, nil, err
	}

	var out []*config.Channel
	broken := map[string]string{}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml":
		default:
			continue
		}

		path := filepath.Join(d.dir, e.Name())
		c, err := config.LoadFile(path)
		if err != nil {
			// A contract sidecar is a valid file that is not a channel, and lands here too. Recorded with
			// its reason rather than filtered out by name: a document that silently omits a file cannot be
			// checked for completeness, which is the only reason to read it.
			broken[e.Name()] = err.Error()

			continue
		}
		out = append(out, c)
	}

	return out, broken, nil
}

// docWriter renders the inventory to a path.
type docWriter struct {
	path   string
	source channelSource
	log    *slog.Logger
	now    func() time.Time
}

// write renders and replaces the file.
//
// Written to a temporary file and renamed, the same way settings are saved. A reader that opens this file while it is being
// rewritten should see the old version or the new one, never half of each - and half a document is worse than a stale one,
// because a stale document is wrong in a way somebody can detect from the date at the top.
func (w *docWriter) write() error {
	channels, broken, err := w.source.Load()
	if err != nil {
		return fmt.Errorf("reading the channels: %w", err)
	}

	now := time.Now
	if w.now != nil {
		now = w.now
	}

	body := spec.Bundle(channels, broken, now())

	if dir := filepath.Dir(w.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("making %s: %w", dir, err)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".inventory-*")
	if err != nil {
		return err
	}
	name := tmp.Name()

	// Permissions before content, so the file is never briefly readable by more people than it should be.
	//
	// 0644 rather than the 0600 settings use, and the difference is deliberate: this document contains no
	// credentials by construction, and its whole purpose is being read by somebody who is not running the process.
	// Making it unreadable would defeat the feature.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(name)

		return err
	}

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(name)

		return err
	}

	// Synced before the rename, or a crash can leave a correctly named file with nothing in it.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)

		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)

		return err
	}

	if err := os.Rename(name, w.path); err != nil {
		os.Remove(name)

		return err
	}

	w.log.Debug("wrote the interface inventory", "path", w.path, "channels", len(channels))

	return nil
}

// start writes the document now and then on a schedule, returning a stop function.
//
// Written immediately rather than only after the first interval. A document that appears an hour after the server starts is
// a document somebody looks for, does not find, and stops expecting.
func (w *docWriter) start(every time.Duration) func() {
	if err := w.write(); err != nil {
		// A failure here does not stop the server. The inventory is documentation: refusing to serve HL7 because
		// a Markdown file could not be written would be a self-inflicted outage.
		w.log.Warn("could not write the interface inventory", "path", w.path, "err", err)
	}

	if every <= 0 {
		every = 24 * time.Hour
	}

	ticker := time.NewTicker(every)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				if err := w.write(); err != nil {
					w.log.Warn("could not write the interface inventory", "err", err)
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		ticker.Stop()
		close(done)
	}
}
