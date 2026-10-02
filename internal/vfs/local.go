package vfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Local is a directory on a disk this server can see.
//
// # Why this is the connector most sites need first
//
// A very large amount of real integration is a folder. A laboratory analyser writes a result file to a share, a billing
// system drops a batch overnight, a radiology system exports reports to a directory somebody mounted years ago. None of
// it involves a protocol.
//
// # The root exists to make a mistake impossible
//
// Every path is resolved inside Root and a path that escapes it is refused. Without that, a pattern or an archive
// directory containing .. lets a channel configured through a web interface read or delete anywhere the server process
// can reach, and the person configuring it need not have intended anything: "../processed" is a reasonable thing to
// type. It is checked after resolving symlinks, because a symlink inside the directory pointing at /etc is the version
// of this that a string comparison misses.
type Local struct {
	// Root bounds every operation. Required.
	Root string

	// FollowSymlinks permits reading a file that is a symlink to somewhere outside Root.
	//
	// Off by default. A symlink farm is a legitimate arrangement in some shops, but the safe default for a directory
	// that arbitrary systems write into is to treat a link out of the tree as the attack it usually is.
	FollowSymlinks bool
}

// NewLocal prepares a local directory, resolving the root once.
//
// The root is resolved and checked at construction rather than on first poll, so a directory that does not exist or is
// a file is reported when somebody is looking at the channel instead of thirty seconds later in a log.
func NewLocal(root string, followSymlinks bool) (*Local, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("no directory was given to read from")
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("cannot work out where %s is: %w", root, err)
	}

	// Resolved so the containment check below compares like with like. A root that is itself a symlink is normal - it
	// is how mounted shares are usually arranged - so this is resolved rather than refused.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("the directory %s does not exist. It is not created automatically, because "+
				"creating it would turn a typo into a channel that polls an empty directory forever and reports "+
				"nothing wrong", abs)
		}
		return nil, fmt.Errorf("cannot resolve %s: %w", abs, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is a file, not a directory. A source reads every file in a directory that "+
			"matches its pattern; to read one specific file, point at its directory and set the pattern to its "+
			"name", resolved)
	}

	return &Local{Root: resolved, FollowSymlinks: followSymlinks}, nil
}

// resolve turns a caller's path into an absolute one inside Root, or refuses it.
func (l *Local) resolve(p string) (string, error) {
	full := p
	if !filepath.IsAbs(full) {
		full = filepath.Join(l.Root, p)
	}
	full = filepath.Clean(full)

	// Checked before resolving symlinks so that a path escaping by ".." is refused even when the target does not exist
	// - which is the case for an archive directory about to be created.
	if !l.within(full) {
		return "", fmt.Errorf("%s is outside %s, and a source only reads and writes inside its own directory",
			p, l.Root)
	}

	if l.FollowSymlinks {
		return full, nil
	}

	// And again after resolving, because a symlink inside the tree pointing out of it passes the check above. Only
	// applied when the target exists: a path being created cannot be resolved and has already been checked textually.
	if real, err := filepath.EvalSymlinks(full); err == nil && !l.within(real) {
		return "", fmt.Errorf("%s is a link to %s, which is outside %s. Set follow_symlinks if that is deliberate",
			p, real, l.Root)
	}

	return full, nil
}

// within reports whether p is Root or inside it.
//
// Compared component-wise rather than with strings.HasPrefix, which would accept /data/inbox-old for a root of
// /data/inbox.
func (l *Local) within(p string) bool {
	rel, err := filepath.Rel(l.Root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// List returns the entries in dir, which is relative to Root.
func (l *Local) List(ctx context.Context, dir string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	full, err := l.resolve(dir)
	if err != nil {
		return nil, err
	}

	items, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			// Vanished between listing and stat, which is entirely normal in a directory another process is writing
			// to. Skipped rather than failing the poll: the alternative abandons every other file in the directory
			// because one was moved.
			continue
		}

		e := Entry{Name: it.Name(), Size: info.Size(), ModTime: info.ModTime(), IsDir: info.IsDir()}

		// A symlink reports as neither file nor directory by mode, and its own size is the length of the target
		// string. Followed here so that a link to a real file is read as that file, and so a link to a directory is
		// correctly skipped rather than read as a tiny file.
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Stat(filepath.Join(full, it.Name())); err == nil {
				e.Size, e.ModTime, e.IsDir = target.Size(), target.ModTime(), target.IsDir()
			} else {
				// A broken link. Reported as a directory so the poller skips it, rather than as a zero-byte file it
				// would wait on forever.
				e.IsDir = true
			}
		}

		out = append(out, e)
	}
	return out, nil
}

// Open reads one file.
func (l *Local) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := l.resolve(p)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

// Remove deletes one file.
func (l *Local) Remove(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := l.resolve(p)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

// Rename moves a file.
//
// No copy-then-delete fallback. A rename across filesystems fails on every operating system, and a connector that
// quietly copied instead would leave the original to be collected again on the next poll: the same message delivered
// every thirty seconds until somebody noticed. Failing here means the file stays put and is reported, which is
// recoverable.
func (l *Local) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	src, err := l.resolve(from)
	if err != nil {
		return err
	}
	dst, err := l.resolve(to)
	if err != nil {
		return err
	}

	if err := os.Rename(src, dst); err != nil {
		if isCrossDevice(err) {
			return fmt.Errorf("%s and %s are on different filesystems, so the file cannot be moved between them. "+
				"Put the archive directory on the same filesystem as the directory being read: copying instead "+
				"would leave the original to be collected again on every poll: %w", from, to, err)
		}
		return err
	}
	return nil
}

// MkdirAll creates a directory and its parents.
func (l *Local) MkdirAll(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := l.resolve(dir)
	if err != nil {
		return err
	}
	// 0o750 rather than 0o755. These directories hold clinical messages, and the group is usually deliberate while
	// everybody else on the machine is not.
	return os.MkdirAll(full, 0o750)
}

// Join joins path elements.
func (l *Local) Join(elem ...string) string { return filepath.Join(elem...) }

// Describe names the directory.
func (l *Local) Describe() string { return l.Root }

// Close does nothing. A local directory holds no connection.
func (l *Local) Close() error { return nil }
