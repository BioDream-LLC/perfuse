package engine

import (
	"context"
	"fmt"
	"path"

	"github.com/biodream-llc/perfuse/internal/vfs"
)

// startFileSource begins polling a local directory.
func (c *Channel) startFileSource() error {
	cfg := c.cfg.Source.File
	if cfg == nil {
		return fmt.Errorf("channel %q has a file source but no file block", c.cfg.Name)
	}

	for _, w := range cfg.Warnings() {
		c.log.Warn("file source", "channel", c.cfg.Name, "warning", w)
	}

	// Resolved now rather than on the first poll, so a directory that does not exist, or is a file, or is outside
	// anything this server should read, is reported while somebody is looking at the channel instead of thirty seconds
	// later in a log.
	root, err := vfs.NewLocal(cfg.Root, cfg.FollowSymlinks)
	if err != nil {
		return fmt.Errorf("channel %q cannot read %s: %w", c.cfg.Name, cfg.Root, err)
	}

	p := newFilePoller(c, "file", &cfg.FilePoll, func(context.Context) (vfs.FS, error) {
		// A local directory holds no connection, so the same object is handed out every poll. Close is a no-op on it,
		// which is why the poller closing what it is given is safe here.
		return nopCloser{root}, nil
	})

	c.files = p

	c.log.Info("reading a directory",
		"channel", c.cfg.Name, "root", root.Describe(), "dir", cfg.Dir, "pattern", cfg.Pattern,
		"every", cfg.PollInterval, "settles_for", cfg.StableFor, "after_read", cfg.AfterRead)

	p.start()
	return nil
}

// stopFileSource stops polling.
func (c *Channel) stopFileSource() error {
	if c.files == nil {
		return nil
	}
	c.files.close()
	c.files = nil
	return nil
}

// nopCloser keeps a long-lived filesystem alive across polls.
//
// The poller closes what it opens, which is right for every network transport. A local directory is not reopened, so it
// must survive being closed.
type nopCloser struct{ vfs.FS }

func (nopCloser) Close() error { return nil }

// pathMatch matches a glob against a name.
//
// Wrapped so every backend matches identically. path.Match rather than filepath.Match: filepath.Match uses the host's
// separator, so a pattern would behave differently on Windows for the same configuration, and these names never contain
// a separator anyway.
func pathMatch(pattern, name string) (bool, error) { return path.Match(pattern, name) }
