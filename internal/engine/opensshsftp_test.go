package engine

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// SFTP against OpenSSH, which is what sites actually run.
//
// The existing SFTP tests stand up a server from the same Go library the client uses, which checks that the library agrees with itself.
// OpenSSH's sftp-server is a different implementation by different people, and it is the one on the far end of nearly every real feed.
//
// The difference matters in specific places: how a rename over an existing file behaves, whether a directory has to exist before a
// write, what happens to a partial file when a connection drops. A file-based feed that half-works is worse than one that fails,
// because a receiving system reads the half.
//
// Skipped when the server is absent, like the rest of the interop set.

const sftpAddr = "127.0.0.1:2222"

func requireOpenSSHSFTP(t *testing.T) string {
	t.Helper()

	conn, err := net.DialTimeout("tcp", sftpAddr, 2*time.Second)
	if err != nil {
		t.Skip("no SFTP server on " + sftpAddr + ": see scripts/interop-up.sh")
	}

	_ = conn.Close()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot find the home directory")
	}

	// The container's upload directory is bind-mounted here, so a test can see what actually landed rather than asking the server.
	shared := filepath.Join(home, "sftp-test", "upload")
	if _, err := os.Stat(shared); err != nil {
		t.Skip("the shared upload directory is missing: see scripts/interop-up.sh")
	}

	return shared
}

func TestAFileWrittenOverSFTPLandsOnAnOpenSSHServer(t *testing.T) {
	// The assertion is the file on disk, read through the bind mount. A write that the library reported as successful and that left
	// nothing on the far end is the failure worth catching, and it is invisible from the client's side.
	shared := requireOpenSSHSFTP(t)

	sender, err := NewSFTPSender(config.Destination{
		Name: "partner",
		Type: config.DestinationSFTP,
		SFTP: &config.SFTPDestination{
			Host:     sftpAddr,
			User:     "perfuse",
			Password: "perfuse",
			Dir:      "upload",
			// A container generates a fresh host key each start, so pinning one would make this test fail for the wrong reason. A real
			// deployment must not do this, which is why the setting says so in its name.
			InsecureSkipHostKeyCheck: true,
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("building the SFTP destination: %v", err)
	}

	defer func() { _ = sender.Close() }()

	message := "MSH|^~\\&|PERFUSE|TEST|PARTNER|TEST|20260917210000||ADT^A01|SFTP1|P|2.5\r" +
		"PID|1||MRN00412^^^HOSP^MR||Hopper^Grace^B||19061209|F\r"

	before := countFiles(t, shared)

	if err := sender.Send(context.Background(), []byte(message)); err != nil {
		t.Fatalf("OpenSSH refused the write: %v", err)
	}

	// Allow a moment: the file is written through a bind mount into a virtual machine, and expecting it instantly would be a race
	// rather than an assertion.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countFiles(t, shared) > before {
			// And the contents, because a file of the right name and the wrong length is the shape of a framing mistake.
			assertOneFileContains(t, shared, "MRN00412")

			return
		}

		time.Sleep(250 * time.Millisecond)
	}

	t.Errorf("the write reported success and the directory still holds %d files", before)
}

func TestSFTPRefusesAWrongPassword(t *testing.T) {
	// The negative control. Without it, a server with authentication disabled would make the test above pass while proving that a
	// socket exists.
	_ = requireOpenSSHSFTP(t)

	sender, err := NewSFTPSender(config.Destination{
		Name: "partner",
		Type: config.DestinationSFTP,
		SFTP: &config.SFTPDestination{
			Host:                     sftpAddr,
			User:                     "perfuse",
			Password:                 "not-the-password",
			Dir:                      "upload",
			InsecureSkipHostKeyCheck: true,
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		// Refusing at construction is fine; what matters is that nothing reports a delivery.
		return
	}

	defer func() { _ = sender.Close() }()

	if err := sender.Send(context.Background(), []byte("MSH|^~\\&|PERFUSE|TEST|||||ADT^A01|2|P|2.5\r")); err == nil {
		t.Fatal("OpenSSH accepted a wrong password, so the test above proves nothing about authentication")
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	n := 0

	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}

	return n
}

func assertOneFileContains(t *testing.T, dir, want string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}

		if strings.Contains(string(data), want) {
			return
		}
	}

	t.Errorf("a file arrived and none of them contains %q, so the contents did not survive the transfer", want)
}
