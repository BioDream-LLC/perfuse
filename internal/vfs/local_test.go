package vfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tempRoot(t *testing.T) *Local {
	t.Helper()

	// Resolved because macOS puts temporary directories under /var, which is a symlink to /private/var. Without this,
	// every containment check in these tests compares a resolved path against an unresolved root and fails for a
	// reason that has nothing to do with what is being tested.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	l, err := NewLocal(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func write(t *testing.T, l *Local, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(l.Root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A directory that does not exist must be reported, not created.
//
// Creating it would turn a typo into a channel that polls an empty directory forever and reports nothing wrong, which
// is the hardest kind of misconfiguration to find: everything is green and no messages arrive.
func TestAMissingDirectoryIsReportedRatherThanCreated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")

	_, err := NewLocal(missing, false)
	if err == nil {
		t.Fatal("a missing directory was accepted")
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("the directory was created; a typo would then poll an empty directory forever")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
}

// Pointing at a file rather than a directory must say what to do instead.
func TestPointingAtAFileSaysWhatToDoInstead(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "one.hl7")
	if err := os.WriteFile(f, []byte("MSH|"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewLocal(f, false)
	if err == nil {
		t.Fatal("a file was accepted as a directory")
	}
	// Naming the alternative matters more than naming the fault. Somebody who wanted one specific file needs to be
	// told about the pattern, or they will conclude the feature does not exist.
	if !strings.Contains(err.Error(), "pattern") {
		t.Errorf("the error does not explain how to read a single file: %v", err)
	}
}

// A path escaping the root must be refused.
//
// Without this, a channel configured from a web interface can read or delete anywhere the server process can reach, and
// the person configuring it need not have intended anything: "../processed" is a reasonable thing to type.
func TestAPathEscapingTheRootIsRefused(t *testing.T) {
	l := tempRoot(t)
	ctx := context.Background()

	outside := filepath.Join(filepath.Dir(l.Root), "outside.hl7")
	if err := os.WriteFile(outside, []byte("MSH|secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, attempt := range []string{
		"../outside.hl7",
		"sub/../../outside.hl7",
		outside,
	} {
		if _, err := l.Open(ctx, attempt); err == nil {
			t.Errorf("%q was read from outside the root", attempt)
		}
		if err := l.Remove(ctx, attempt); err == nil {
			t.Errorf("%q was deleted from outside the root", attempt)
		}
	}

	// And the file outside is still there, which is the assertion that matters: a refusal that had already deleted the
	// file would be no refusal at all.
	if _, err := os.Stat(outside); err != nil {
		t.Error("the file outside the root was deleted despite the refusal")
	}
}

// A root whose name is a prefix of another directory must not grant access to it.
//
// The reason containment is checked component-wise rather than with a string prefix: /data/inbox-old begins with
// /data/inbox.
func TestASiblingDirectoryWithASharedPrefixIsNotInsideTheRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	inbox := filepath.Join(base, "inbox")
	inboxOld := filepath.Join(base, "inbox-old")
	for _, d := range []string{inbox, inboxOld} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(inboxOld, "old.hl7"), []byte("MSH|"), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := NewLocal(inbox, false)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := l.Open(context.Background(), filepath.Join(inboxOld, "old.hl7")); err == nil {
		t.Error("a directory sharing a name prefix with the root was treated as inside it")
	}
}

// A symlink out of the tree must be refused unless it was asked for.
//
// This is the version of the escape a textual check misses: the path is entirely inside the root and the target is not.
func TestASymlinkOutOfTheTreeIsRefusedByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows that a test should not assume")
	}

	l := tempRoot(t)
	ctx := context.Background()

	outside := filepath.Join(filepath.Dir(l.Root), "target.hl7")
	if err := os.WriteFile(outside, []byte("MSH|secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(l.Root, "link.hl7")); err != nil {
		t.Fatal(err)
	}

	if _, err := l.Open(ctx, "link.hl7"); err == nil {
		t.Error("a symlink pointing out of the root was followed by default")
	} else if !strings.Contains(err.Error(), "follow_symlinks") {
		// Named so somebody with a legitimate symlink farm can find the setting rather than concluding it is
		// impossible.
		t.Errorf("the refusal does not name the setting that permits it: %v", err)
	}

	// And permitted when asked for, because a symlink farm is a real arrangement in some shops.
	l.FollowSymlinks = true
	rc, err := l.Open(ctx, "link.hl7")
	if err != nil {
		t.Fatalf("follow_symlinks did not permit it: %v", err)
	}
	defer rc.Close()

	body, _ := io.ReadAll(rc)
	if string(body) != "MSH|secret" {
		t.Errorf("read %q", body)
	}
}

// Listing must report a symlinked file's real size, not the length of the link target string.
//
// A symlink's own size is the number of characters in its target path, which for a real message file is a small number.
// A poller that believed it would treat every linked file as suspiciously small, and one that reported zero would wait
// on it forever.
func TestListingReportsTheRealSizeOfASymlinkedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows that a test should not assume")
	}

	l := tempRoot(t)
	body := strings.Repeat("MSH|^~\\&|A|B|C|D|20260827120000||ADT^A01|1|P|2.5.1\r", 40)
	write(t, l, "real.hl7", body)

	if err := os.Symlink(filepath.Join(l.Root, "real.hl7"), filepath.Join(l.Root, "linked.hl7")); err != nil {
		t.Fatal(err)
	}

	entries, err := l.List(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}

	var linked *Entry
	for i := range entries {
		if entries[i].Name == "linked.hl7" {
			linked = &entries[i]
		}
	}
	if linked == nil {
		t.Fatal("the symlink was not listed at all")
	}
	if linked.Size != int64(len(body)) {
		t.Errorf("the symlink reports size %d; the file it points at is %d bytes",
			linked.Size, len(body))
	}
}

// A broken symlink must be skipped, not waited on.
//
// Reported as a directory so the poller ignores it. Reported as a zero-byte file it would sit in the waiting count
// forever, and a permanently non-zero "files waiting" number is how a real backlog becomes invisible.
func TestABrokenSymlinkIsSkippedRatherThanWaitedOn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows that a test should not assume")
	}

	l := tempRoot(t)
	if err := os.Symlink(filepath.Join(l.Root, "gone.hl7"), filepath.Join(l.Root, "broken.hl7")); err != nil {
		t.Fatal(err)
	}

	entries, err := l.List(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "broken.hl7" && !e.IsDir {
			t.Error("a broken symlink was listed as a file, so the poller would wait on it forever")
		}
	}
}

// Renaming, creating directories and deleting must work inside the root.
func TestTheOrdinaryOperationsWorkInsideTheRoot(t *testing.T) {
	l := tempRoot(t)
	ctx := context.Background()

	write(t, l, "in.hl7", "MSH|one")

	if err := l.MkdirAll(ctx, "archive"); err != nil {
		t.Fatal(err)
	}
	if err := l.Rename(ctx, "in.hl7", l.Join("archive", "in.hl7")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.Root, "archive", "in.hl7")); err != nil {
		t.Errorf("the file was not moved: %v", err)
	}

	if err := l.Remove(ctx, l.Join("archive", "in.hl7")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.Root, "archive", "in.hl7")); !os.IsNotExist(err) {
		t.Error("the file was not deleted")
	}
}

// A cancelled context must stop the operation.
//
// A poll that cannot be abandoned holds up shutdown until a network timeout expires, which for a server being restarted
// during a maintenance window is the difference between seconds and minutes.
func TestACancelledContextStopsTheOperation(t *testing.T) {
	l := tempRoot(t)
	write(t, l, "in.hl7", "MSH|one")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := l.List(ctx, "."); !errors.Is(err, context.Canceled) {
		t.Errorf("List ignored a cancelled context: %v", err)
	}
	if _, err := l.Open(ctx, "in.hl7"); !errors.Is(err, context.Canceled) {
		t.Errorf("Open ignored a cancelled context: %v", err)
	}
	if err := l.Remove(ctx, "in.hl7"); !errors.Is(err, context.Canceled) {
		t.Errorf("Remove ignored a cancelled context: %v", err)
	}
}

// Local must satisfy FS. Asserted here so a change to the interface breaks the build rather than one backend.
var _ FS = (*Local)(nil)
