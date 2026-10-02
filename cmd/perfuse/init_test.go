package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// init is the command that decides whether somebody ever sees the rest of Perfuse, so the tests are about
// the first-run experience rather than about file plumbing.

func runInit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := cmdInit(args, &out, &out)
	return out.String(), err
}

func TestInitCreatesALayoutThatWorks(t *testing.T) {
	dir := t.TempDir()

	out, err := runInit(t, "-dir", dir, "-service", "none")
	if err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"channels", "data", "logs", "certs"} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			t.Errorf("%s was not created", sub)
		}
	}
	for _, file := range []string{"perfuse.env", "channels/example.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Errorf("%s was not created", file)
		}
	}
	if !strings.Contains(out, "Next:") {
		t.Error("the output does not tell somebody what to do next")
	}
}

// TestTheExampleChannelActuallyLoads is the test that earns this command.
//
// An example channel that does not load turns the first five minutes of using Perfuse into debugging
// Perfuse. My first version had "framed: true" on a file destination, where that key does not exist, and
// perfuse check refused the whole directory - so the very first thing a new user would have done is hit an
// error produced by the tool that was supposed to be setting them up.
func TestTheExampleChannelActuallyLoads(t *testing.T) {
	dir := t.TempDir()
	if _, err := runInit(t, "-dir", dir, "-service", "none"); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "channels", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if err := loadExampleForTest(t, body); err != nil {
		t.Fatalf("the example channel does not load, so a new user's first command fails: %v", err)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	// A hospital's channel files are the most valuable thing on the box, and running init twice in the
	// wrong directory must not replace a working configuration.
	dir := t.TempDir()
	if _, err := runInit(t, "-dir", dir, "-service", "none"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "channels", "example.yaml")
	if err := os.WriteFile(path, []byte("name: mine-not-yours\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runInit(t, "-dir", dir, "-service", "none")
	if err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "mine-not-yours") {
		t.Fatal("init overwrote an existing channel file")
	}
	// And it says which files it left alone. "3 files skipped" makes somebody wonder which, and whether
	// the one they cared about was among them.
	if !strings.Contains(out, "example.yaml") || !strings.Contains(out, "already there") {
		t.Errorf("the output does not name what it kept:\n%s", out)
	}
}

func TestForceReplaces(t *testing.T) {
	dir := t.TempDir()
	if _, err := runInit(t, "-dir", dir, "-service", "none"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "channels", "example.yaml")
	if err := os.WriteFile(path, []byte("name: mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runInit(t, "-dir", dir, "-service", "none", "-force"); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "name: mine") {
		t.Error("-force did not replace the file")
	}
}

func TestTheSessionKeyIsGeneratedAndDifferentEveryTime(t *testing.T) {
	// A shared default signing key means a session cookie minted on one Perfuse is accepted by another,
	// which is a full authentication bypass between installations.
	var keys []string

	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		if _, err := runInit(t, "-dir", dir, "-service", "none"); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(dir, "perfuse.env"))
		if err != nil {
			t.Fatal(err)
		}
		key := valueOf(string(body), "PERFUSE_SESSION_KEY")
		if len(key) < 32 {
			t.Fatalf("the session key is too short to be safe: %q", key)
		}
		keys = append(keys, key)
	}

	if keys[0] == keys[1] || keys[1] == keys[2] {
		t.Error("the session key is the same between installations")
	}
}

func TestTheEnvironmentFileIsNotWorldReadable(t *testing.T) {
	// It holds the session key and is where credentials are meant to live.
	dir := t.TempDir()
	if _, err := runInit(t, "-dir", dir, "-service", "none"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "perfuse.env"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("perfuse.env is mode %o; it holds the session key", mode)
	}
}

func TestSystemdUnitHasAStopTimeoutLongerThanTheDrain(t *testing.T) {
	// Perfuse reports not-ready for a drain period after SIGTERM and then finishes in-flight messages. If
	// the stop timeout is shorter, systemd kills it mid-message - which is a lost message, on a shutdown
	// that was supposed to be graceful.
	unit := systemdUnit("/opt/perfuse", "perfuse")

	if !strings.Contains(unit, "TimeoutStopSec=90") {
		t.Errorf("the unit has no generous stop timeout:\n%s", unit)
	}
	if !strings.Contains(unit, "KillSignal=SIGTERM") {
		t.Error("the unit does not send SIGTERM, so the drain never starts")
	}
}

func TestSystemdUnitDoesNotRunAsRoot(t *testing.T) {
	unit := systemdUnit("/opt/perfuse", "perfuse")

	if strings.Contains(unit, "User=root") {
		t.Error("the unit runs as root")
	}
	if !strings.Contains(unit, "NoNewPrivileges=true") {
		t.Error("the unit does not set NoNewPrivileges")
	}
	// It has to be able to write its own database and channel directory, and nothing else.
	if !strings.Contains(unit, "ReadWritePaths=/opt/perfuse") {
		t.Error("the unit does not grant write access to its own directory, so it cannot start")
	}
	if !strings.Contains(unit, "ProtectSystem=strict") {
		t.Error("the unit does not protect the rest of the filesystem")
	}
}

func TestSystemdUnitReadsSecretsFromAFileNotFlags(t *testing.T) {
	// A password on the command line appears in a process listing, where anybody on the box can read it.
	unit := systemdUnit("/opt/perfuse", "perfuse")

	if !strings.Contains(unit, "EnvironmentFile=/opt/perfuse/perfuse.env") {
		t.Errorf("the unit does not read an environment file:\n%s", unit)
	}
}

func TestTheLaunchdPlistWaitsForInFlightMessages(t *testing.T) {
	plist := launchdPlist("/opt/perfuse", "127.0.0.1:8443")

	if !strings.Contains(plist, "ExitTimeOut") {
		t.Error("the plist has no exit timeout, so launchd may kill it mid-message")
	}
	if !strings.Contains(plist, "<key>Label</key>") {
		t.Error("the plist has no label, so launchctl cannot load it")
	}
}

func TestTheWindowsScriptDoesNotNeedAThirdPartyWrapper(t *testing.T) {
	// Windows-heavy hospital IT is exactly the audience that cannot install an unsigned third-party
	// service wrapper. sc.exe is already there and already approved.
	script := windowsService(`C:\Perfuse`, "127.0.0.1:8443")

	if !strings.Contains(script, "sc.exe create") {
		t.Error("the script does not use sc.exe")
	}
	if !strings.Contains(script, "LocalService") {
		t.Error("the service would run with more privilege than it needs")
	}
	// A crash loop must not be fast enough to fill the disk with logs.
	if !strings.Contains(script, "sc.exe failure") {
		t.Error("the script sets no restart policy")
	}
}

func TestAnUnknownServiceKindIsRefusedWithTheOptions(t *testing.T) {
	dir := t.TempDir()
	_, err := runInit(t, "-dir", dir, "-service", "upstart")
	if err == nil {
		t.Fatal("an unknown service kind was accepted")
	}
	if !strings.Contains(err.Error(), "systemd") {
		t.Errorf("the error does not list the valid options: %v", err)
	}
}

func TestLocalhostBindingWarnsAboutTLSBeforeSomebodyChangesIt(t *testing.T) {
	// Somebody who wants it reachable will otherwise change the address without reading anything about
	// TLS, and the sign-in password crosses the network in the clear.
	dir := t.TempDir()
	out, err := runInit(t, "-dir", dir, "-service", "none")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "TLS") {
		t.Errorf("the output does not mention TLS:\n%s", out)
	}
}

func valueOf(body, key string) string {
	for _, line := range strings.Split(body, "\n") {
		if after, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// loadExampleForTest loads a channel through the real loader.
//
// Written as a helper so the test above reads as a statement about the example rather than about config
// package plumbing.
func loadExampleForTest(t *testing.T, body []byte) error {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "example.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := config.LoadFile(path)
	return err
}
