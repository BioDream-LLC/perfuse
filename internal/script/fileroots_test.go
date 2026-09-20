package script

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/hl7xml"
)

// This file guards the confinement of FileUtil.
//
// It exists because "allow: [file]" used to mean the entire filesystem, demonstrated against a running server: a channel
// created through the console read one file and wrote it to another, and the same script read Perfuse's own database -
// the file holding password hashes, the LDAP service account password and the OIDC client secret.
//
// Creating a channel needs the editor role; starting one needs admin. So the escalation is a confused deputy rather than
// a single-role break: an editor plants it, and an administrator performing the routine action of starting a channel
// executes somebody else's arbitrary file access as the server process.

func rootsFor(t *testing.T, dirs ...string) *FileRoots {
	t.Helper()
	r, err := NewFileRoots(dirs)
	if err != nil {
		t.Fatalf("NewFileRoots(%v): %v", dirs, err)
	}
	return r
}

// TestAPathInsideTheRootIsAllowed covers the ordinary case.
func TestAPathInsideTheRootIsAllowed(t *testing.T) {
	dir := t.TempDir()
	roots := rootsFor(t, dir)

	for _, path := range []string{
		filepath.Join(dir, "file.txt"),
		filepath.Join(dir, "nested", "deeper", "file.txt"),
		dir,
	} {
		if _, err := roots.Check(path); err != nil {
			t.Errorf("Check(%q) refused a path inside the root: %v", path, err)
		}
	}
}

// TestATraversalOutOfTheRootIsRefused is the case the confinement exists for.
func TestATraversalOutOfTheRootIsRefused(t *testing.T) {
	dir := t.TempDir()
	roots := rootsFor(t, dir)

	hostile := []string{
		"/etc/passwd",
		filepath.Join(dir, "..", "escaped.txt"),
		filepath.Join(dir, "..", "..", "etc", "passwd"),
		filepath.Join(dir, "a", "..", "..", "escaped.txt"),
		"/tmp",
	}

	for _, path := range hostile {
		if _, err := roots.Check(path); err == nil {
			t.Errorf("Check(%q) allowed a path outside the root", path)
		}
	}
}

// TestASiblingDirectoryWithASharedPrefixIsRefused covers a one-character mistake.
//
// A plain string prefix test would let /var/perfusehack pass for a root of /var/perfuse. That is the difference between
// a confinement and the appearance of one.
func TestASiblingDirectoryWithASharedPrefixIsRefused(t *testing.T) {
	parent := t.TempDir()

	allowed := filepath.Join(parent, "perfuse")
	sibling := filepath.Join(parent, "perfusehack")

	if err := os.MkdirAll(allowed, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o750); err != nil {
		t.Fatal(err)
	}

	roots := rootsFor(t, allowed)

	if _, err := roots.Check(filepath.Join(sibling, "stolen.txt")); err == nil {
		t.Error("a sibling directory sharing a prefix with the root was allowed")
	}
}

// TestASymlinkOutOfTheRootIsRefused covers the escape a path check alone misses.
//
// A directory inside the root can be a symlink pointing anywhere. Checking the path as written would pass; opening it
// would read whatever it points at.
func TestASymlinkOutOfTheRootIsRefused(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("sensitive"), 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	roots := rootsFor(t, dir)

	if _, err := roots.Check(filepath.Join(link, "secret.txt")); err == nil {
		t.Error("a symlinked directory inside the root allowed a read from outside it")
	}
}

// TestASymlinkedParentOfANewFileIsRefused covers a write rather than a read.
//
// A file being created does not exist yet, so resolving the whole path fails and a naive check would skip resolution
// entirely - which is exactly the moment a write escapes.
func TestASymlinkedParentOfANewFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()

	link := filepath.Join(dir, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	roots := rootsFor(t, dir)

	// The file does not exist. Its parent is a symlink out of the confinement.
	if _, err := roots.Check(filepath.Join(link, "new-file.txt")); err == nil {
		t.Error("a not-yet-existing file under a symlinked parent was allowed")
	}
}

// TestASymlinkedRootStillWorks covers the other direction.
//
// A root that is itself a symlink is ordinary - /var/perfuse pointing at a mounted volume. If roots were not resolved,
// nothing inside one would ever match and the failure would look like the confinement being broken.
func TestASymlinkedRootStillWorks(t *testing.T) {
	real := t.TempDir()
	parent := t.TempDir()
	link := filepath.Join(parent, "link-to-root")

	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	roots := rootsFor(t, link)

	if _, err := roots.Check(filepath.Join(link, "file.txt")); err != nil {
		t.Errorf("a file inside a symlinked root was refused: %v", err)
	}
	if _, err := roots.Check(filepath.Join(real, "file.txt")); err != nil {
		t.Errorf("a file inside the resolved root was refused: %v", err)
	}
}

// TestNoRootsMeansNoAccess covers the nil case.
//
// Nil has to mean refused, not unconfined. Nil meaning unconfined is precisely the behaviour being removed.
func TestNoRootsMeansNoAccess(t *testing.T) {
	var roots *FileRoots

	if _, err := roots.Check("/etc/passwd"); err == nil {
		t.Error("a nil confinement allowed a read")
	}

	empty := &FileRoots{}
	if _, err := empty.Check("/etc/passwd"); err == nil {
		t.Error("an empty confinement allowed a read")
	}
}

// TestTheFilesystemRootIsNotAConfinement covers a configuration that confines nothing.
func TestTheFilesystemRootIsNotAConfinement(t *testing.T) {
	if _, err := NewFileRoots([]string{"/"}); err == nil {
		t.Error("a root of / was accepted, which is a confinement that confines nothing")
	}
	if _, err := NewFileRoots([]string{}); err == nil {
		t.Error("an empty root list was accepted")
	}
	if _, err := NewFileRoots([]string{"  "}); err == nil {
		t.Error("a blank root was accepted")
	}
}

// TestSeveralRootsAllWork covers a channel with an inbound and an outbound directory.
func TestSeveralRootsAllWork(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	elsewhere := t.TempDir()

	roots := rootsFor(t, in, out)

	if _, err := roots.Check(filepath.Join(in, "a.txt")); err != nil {
		t.Errorf("the first root was refused: %v", err)
	}
	if _, err := roots.Check(filepath.Join(out, "b.txt")); err != nil {
		t.Errorf("the second root was refused: %v", err)
	}
	if _, err := roots.Check(filepath.Join(elsewhere, "c.txt")); err == nil {
		t.Error("a directory that is not a root was allowed")
	}
}

// TestTheRefusalNamesTheDirectories covers the message.
//
// Somebody hitting this is looking at a script that used to work. The refusal has to say what is permitted, or the only
// way forward is guessing.
func TestTheRefusalNamesTheDirectories(t *testing.T) {
	dir := t.TempDir()
	roots := rootsFor(t, dir)

	_, err := roots.Check("/etc/passwd")
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal does not name the permitted directory: %v", err)
	}
}

// TestFileUtilCannotReachOutsideTheRootFromAScript is the end-to-end version.
//
// The unit tests above cover Check; this one goes through the script engine, which is what the demonstrated escalation
// actually used. A confinement that were correct but not wired in would pass every test above.
func TestFileUtilCannotReachOutsideTheRootFromAScript(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("SENSITIVE"), 0o600); err != nil {
		t.Fatal(err)
	}

	roots := rootsFor(t, allowed)
	e := New(Options{Timeout: 3 * time.Second, Permissions: []Permission{PermFile}, FileRoots: roots})

	root, err := hl7xml.FromRaw([]byte(adt))
	if err != nil {
		t.Fatal(err)
	}

	// Reading outside the root is refused.
	s, err := e.Compile("read", `FileUtil.read('`+secret+`');`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(s, &Context{Message: root, ChannelMap: NewSharedMap()}); err == nil {
		t.Error("a script read a file outside the confinement")
	}

	// Writing outside it is refused too.
	target := filepath.Join(outside, "written.txt")
	root2, _ := hl7xml.FromRaw([]byte(adt))
	s2, err := e.Compile("write", `FileUtil.write('`+target+`', false, 'x');`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(s2, &Context{Message: root2, ChannelMap: NewSharedMap()}); err == nil {
		t.Error("a script wrote a file outside the confinement")
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("the file outside the confinement was created")
	}

	// And inside it still works, so this is a confinement rather than a removal.
	inside := filepath.Join(allowed, "ok.txt")
	root3, _ := hl7xml.FromRaw([]byte(adt))
	s3, err := e.Compile("ok", `FileUtil.write('`+inside+`', false, 'written');`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(s3, &Context{Message: root3, ChannelMap: NewSharedMap()}); err != nil {
		t.Errorf("a script could not write inside its own directory: %v", err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("the permitted write did not happen: %v", err)
	}
}
