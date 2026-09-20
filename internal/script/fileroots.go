package script

import (
	"fmt"
	"path/filepath"
	"strings"
)

// FileRoots confines FileUtil to a set of directories.
//
// This exists because "allow: [file]" used to mean the whole filesystem. Demonstrated rather than reasoned about: a
// channel created through the console read a file from one directory and wrote it to another, and the same script read
// Perfuse's own database - the file holding password hashes, the LDAP service account password and the OIDC client
// secret.
//
// The severity is worth stating precisely. Creating a channel needs the editor role; starting one needs admin. So an
// editor cannot complete the escalation alone - they plant it, and an administrator starting the channel executes it.
// That is a confused deputy: the admin performs a routine action and grants somebody else's script arbitrary file access
// as the server process.
//
// Confinement rather than removal, because reading a file from a script is a real need - Mirth sites do it constantly,
// usually to pick up a lookup table or write a reject file - and taking it away would push people to a worse workaround.
type FileRoots struct {
	// roots are absolute, symlink-resolved directory paths.
	roots []string
}

// NewFileRoots builds a confinement set from configured directories.
//
// Symlinks are resolved now rather than at each check, because a root that is itself a symlink would otherwise never
// match the resolved path of a file inside it, and the failure would look like the confinement being broken.
func NewFileRoots(dirs []string) (*FileRoots, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no directories were given")
	}

	out := &FileRoots{}

	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}

		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("%q could not be made absolute: %w", dir, err)
		}

		// Refused outright. A root of / or a filesystem root on Windows is confinement that confines nothing, and
		// writing it down would give a false sense that a limit exists.
		if abs == string(filepath.Separator) || abs == filepath.VolumeName(abs)+string(filepath.Separator) {
			return nil, fmt.Errorf("%q is the filesystem root, which is not a confinement", dir)
		}

		// Resolved if it exists. A root that does not exist yet is kept as-is: a channel may legitimately be
		// configured before the directory is created, and refusing would make deployment order matter.
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}

		out.roots = append(out.roots, abs)
	}

	if len(out.roots) == 0 {
		return nil, fmt.Errorf("no usable directories were given")
	}

	return out, nil
}

// Roots returns the configured roots, for a description or a log line.
func (f *FileRoots) Roots() []string {
	if f == nil {
		return nil
	}
	return append([]string(nil), f.roots...)
}

// Check returns the resolved path if it is inside a root, or an error naming the refusal.
//
// The returned path is what a caller should open. Opening the original instead would reintroduce the traversal this
// resolves, because the check and the open would be looking at two different things - the classic race in which a path is
// validated and then swapped for a symlink before it is used.
func (f *FileRoots) Check(path string) (string, error) {
	if f == nil || len(f.roots) == 0 {
		return "", fmt.Errorf("no file directories are configured for this channel, so FileUtil cannot be used; " +
			"set scripts.file_roots")
	}

	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("an empty path was given")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%q could not be made absolute: %w", path, err)
	}

	// Cleaned first, which collapses .. segments. Checking before cleaning would let /allowed/../etc/passwd pass a
	// prefix test while opening something else entirely.
	abs = filepath.Clean(abs)

	// Then symlinks, on the deepest existing ancestor. A file being created does not exist yet, so resolving the
	// whole path would fail and the check would have to be skipped - which is exactly when a write escapes.
	resolved := resolveDeepest(abs)

	for _, root := range f.roots {
		if withinRoot(resolved, root) {
			return abs, nil
		}
	}

	return "", fmt.Errorf("%q is outside the directories this channel may use (%s)",
		path, strings.Join(f.roots, ", "))
}

// withinRoot reports whether path is root or sits underneath it.
//
// The separator matters. A plain prefix test would let /var/perfusehack pass for a root of /var/perfuse, which is a
// one-character mistake that opens the confinement completely.
func withinRoot(path, root string) bool {
	if path == root {
		return true
	}

	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	return strings.HasPrefix(path, prefix)
}

// resolveDeepest resolves symlinks on the longest existing prefix of a path.
//
// A file about to be created does not exist, so EvalSymlinks on the whole path fails. Walking up to the deepest ancestor
// that does exist and resolving that catches the case that matters: a symlinked parent directory pointing out of the
// confinement, with a not-yet-existing filename on the end.
func resolveDeepest(path string) string {
	remainder := ""
	current := path

	for i := 0; i < 64; i++ {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			if remainder == "" {
				return resolved
			}
			return filepath.Join(resolved, remainder)
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the top without finding anything that exists.
			return path
		}

		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}

	return path
}
