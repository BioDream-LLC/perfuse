package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The file source: Mirth's File Reader, and the connector a site tries first.
//
// These tests assert on messages arriving and files moving, never on a poller existing. The failure this connector is
// written against is a half-written file read early, which produces a *valid-looking* message, so every test here has to
// establish what actually came out.

const oneMessage = "MSH|^~\\&|LAB|SITEA|PERFUSE|SITEB|20260827120000||ORU^R01|MSG1|P|2.5.1\r" +
	"PID|1||MRN1^^^SITEA^MR||Doe^Jane\r" +
	"OBX|1|NM|718-7^Hemoglobin^LN||13.5|g/dL\r"

// fileHarness builds a running engine whose one channel reads a directory.
type fileHarness struct {
	t    *testing.T
	dir  string
	e    *Engine
	sink *recordingSender
}

func newFileHarness(t *testing.T, overrides map[string]string, preexisting ...map[string]string) *fileHarness {
	t.Helper()

	// Resolved because macOS puts temporary directories under /var, a symlink to /private/var. Without this the
	// source's containment check compares a resolved path against an unresolved root and refuses everything.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Merged rather than appended, so a test overriding a setting replaces it instead of declaring the key twice -
	// which YAML rejects outright.
	settings := map[string]string{
		"root":          dir,
		"pattern":       `"*.hl7"`,
		"poll_interval": "1s",
		"stable_for":    "0s",
	}
	for k, v := range overrides {
		settings[k] = v
	}

	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// A raw source needs a raw channel, and the two are validated against each other at load. Set here so the harness
	// produces a configuration that would actually be accepted.
	dataType := ""
	if settings["raw"] == "true" {
		dataType = "dataType: raw\n"
	}

	var block strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&block, "    %s: %s\n", k, settings[k])
	}

	yaml := fmt.Sprintf(`
name: folder
%ssource:
  type: file
  file:
%s
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, dataType, block.String())

	// Files written before the engine starts, for tests where a partial first poll would change the answer.
	//
	// The ordering guarantee is within one poll. If the first poll lands after only some of the files exist, those
	// settle a poll earlier and are read on their own - correct behaviour, and it makes an ordering assertion across the
	// whole set meaningless.
	for _, set := range preexisting {
		for name, body := range set {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	cfg, err := config.Load(strings.NewReader(yaml), "folder.yaml")
	if err != nil {
		t.Fatalf("loading the config: %v", err)
	}

	sink := &recordingSender{name: "out"}
	e, err := New([]*config.Channel{cfg}, func(config.Destination) (Sender, error) { return sink, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Stop(ctx)
	})

	return &fileHarness{t: t, dir: dir, e: e, sink: sink}
}

func (h *fileHarness) write(name, body string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, name), []byte(body), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// waitForMessages waits until at least n messages have been delivered.
func (h *fileHarness) waitForMessages(n int) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.sink.all()) >= n {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("only %d of %d messages arrived", len(h.sink.all()), n)
}

// waitForGone waits until a file is no longer in the source directory.
func (h *fileHarness) waitForGone(name string) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(h.dir, name)); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("%s is still in the source directory", name)
}

// A file in the directory must become a message, and the file must be archived.
//
// Both halves matter. Delivering without archiving means the same message every poll forever; archiving without
// delivering means a file that looks processed and was not.
func TestAFileBecomesAMessageAndIsArchived(t *testing.T) {
	h := newFileHarness(t, nil)

	h.write("one.hl7", oneMessage)
	h.waitForMessages(1)
	h.waitForGone("one.hl7")

	got := h.sink.all()
	if len(got) != 1 {
		t.Fatalf("%d messages delivered, want 1", len(got))
	}
	if !strings.Contains(string(got[0]), "Hemoglobin") {
		t.Errorf("the message that arrived is not the one in the file: %q", got[0])
	}

	// Archived to the default directory, not deleted. A first configuration that deletes destroys somebody's files
	// while they are still working out whether the channel is right.
	if _, err := os.Stat(filepath.Join(h.dir, "processed", "one.hl7")); err != nil {
		t.Errorf("the file was not archived to processed/: %v", err)
	}
}

// A file holding several messages must deliver all of them.
func TestAFileHoldingSeveralMessagesDeliversEachOne(t *testing.T) {
	h := newFileHarness(t, nil)

	var b strings.Builder
	for i := 1; i <= 3; i++ {
		b.WriteString(strings.Replace(oneMessage, "MSG1", fmt.Sprintf("MSG%d", i), 1))
	}
	h.write("batch.hl7", b.String())

	h.waitForMessages(3)
	if got := len(h.sink.all()); got != 3 {
		t.Fatalf("%d messages delivered, want 3", got)
	}
}

// A non-HL7 file must be deliverable whole when raw is set.
//
// The setting that makes this connector useful for anything other than HL7. Without it a CSV batch, an X12 claim file or
// a PDF produces nothing and the error says the file is not HL7 - true, but not the point, since it was never meant to
// be.
func TestARawFileIsDeliveredWholeRatherThanSplit(t *testing.T) {
	h := newFileHarness(t, map[string]string{"raw": "true", "pattern": `"*.csv"`})

	csv := "mrn,name,result\nMRN1,Doe Jane,13.5\nMRN2,Roe John,9.1\n"
	h.write("results.csv", csv)

	h.waitForMessages(1)
	got := h.sink.all()
	if len(got) != 1 {
		t.Fatalf("%d messages delivered, want 1 whole file", len(got))
	}
	if string(got[0]) != csv {
		t.Errorf("the file was altered on the way through:\ngot  %q\nwant %q", got[0], csv)
	}
}

// A file not matching the pattern must be left completely alone.
//
// Not read, and not archived either. A source that tidied away files it was not configured to read would move a
// neighbouring channel's input out from under it.
func TestAFileNotMatchingThePatternIsLeftAlone(t *testing.T) {
	h := newFileHarness(t, nil)

	h.write("ignore.txt", oneMessage)
	h.write("read.hl7", oneMessage)

	h.waitForMessages(1)
	h.waitForGone("read.hl7")

	if _, err := os.Stat(filepath.Join(h.dir, "ignore.txt")); err != nil {
		t.Errorf("a file outside the pattern was moved or deleted: %v", err)
	}
	if got := len(h.sink.all()); got != 1 {
		t.Errorf("%d messages delivered; a file outside the pattern was read", got)
	}
}

// A file being written must not be read until it has stopped changing.
//
// The failure this connector exists to prevent. A half-written HL7 message very often still parses - the MSH is intact
// and the segments that arrived are well formed - so it is accepted, acknowledged, delivered, and nothing ever says the
// rest was missing.
func TestAFileStillBeingWrittenIsNotReadUntilItSettles(t *testing.T) {
	h := newFileHarness(t, map[string]string{"stable_for": "3s"})

	path := filepath.Join(h.dir, "slow.hl7")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	// The first half only. This is a complete, parseable HL7 message on its own, which is the whole problem.
	if _, err := f.WriteString("MSH|^~\\&|LAB|SITEA|PERFUSE|SITEB|20260827120000||ORU^R01|MSG1|P|2.5.1\r"); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}

	// Long enough for several polls at 1s. If the settle rule were not working, the half message would be delivered
	// during this window.
	time.Sleep(2500 * time.Millisecond)

	if got := h.sink.all(); len(got) != 0 {
		t.Fatalf("a half-written file was read after %d bytes: %q", len(got[0]), got[0])
	}

	if _, err := f.WriteString("PID|1||MRN1^^^SITEA^MR||Doe^Jane\rOBX|1|NM|718-7^Hemoglobin^LN||13.5|g/dL\r"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	h.waitForMessages(1)

	// And the message that arrived is the whole one, which is the assertion that matters. Waiting and then delivering
	// half would be no better than delivering half immediately.
	if got := string(h.sink.all()[0]); !strings.Contains(got, "Hemoglobin") {
		t.Errorf("the message delivered after settling is still incomplete: %q", got)
	}
}

// A file whose name marks it as partial must never be read.
func TestAFileMarkedPartialIsNeverRead(t *testing.T) {
	h := newFileHarness(t, map[string]string{"pattern": `"*"`})

	h.write("half.hl7.part", oneMessage)
	h.write("done.hl7", oneMessage)

	h.waitForMessages(1)
	time.Sleep(1500 * time.Millisecond)

	if got := len(h.sink.all()); got != 1 {
		t.Errorf("%d messages delivered; a file marked partial was read", got)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "half.hl7.part")); err != nil {
		t.Errorf("the partial file was moved: %v", err)
	}
}

// A file too large to read must be quarantined, not skipped.
//
// Skipped, it is listed on every poll for the rest of the process's life and the log says the same thing every second,
// which is how a real problem becomes background noise.
func TestAnOversizeFileIsQuarantinedRatherThanSkippedForever(t *testing.T) {
	h := newFileHarness(t, map[string]string{"max_file_size": "100"})

	h.write("big.hl7", strings.Repeat(oneMessage, 10))
	h.waitForGone("big.hl7")

	if _, err := os.Stat(filepath.Join(h.dir, "errors", "big.hl7")); err != nil {
		t.Errorf("the oversize file was not moved to errors/: %v", err)
	}
	if got := len(h.sink.all()); got != 0 {
		t.Errorf("%d messages were delivered from a file that was never read", got)
	}
}

// A file that cannot be parsed must go to the error directory, separately from the archive.
//
// A directory holding nothing but failures is one somebody can watch. Mixed into the archive, the only way to find a
// failure is to read every file.
func TestAnUnreadableFileGoesToTheErrorDirectoryNotTheArchive(t *testing.T) {
	h := newFileHarness(t, nil)

	h.write("junk.hl7", "this is not an HL7 message at all\n")
	h.waitForGone("junk.hl7")

	if _, err := os.Stat(filepath.Join(h.dir, "errors", "junk.hl7")); err != nil {
		t.Errorf("the unreadable file did not reach errors/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "processed", "junk.hl7")); err == nil {
		t.Error("an unreadable file was archived as though it had been processed")
	}
}

// Files must be read in a defined order.
//
// An ADT stream where an A08 update arrives before the A01 admission produces patients that do not exist yet. Directory
// listing order is arbitrary on most filesystems, so unsorted the order changes between polls for no visible reason.
func TestFilesAreReadInNameOrder(t *testing.T) {
	// The guarantee is ordering *within* a poll, not across polls, and it cannot be anything else: a file arriving after
	// a batch has been read is read next time, whatever its name.
	//
	// My first two attempts at this test got that wrong. Both wrote the files after starting the channel, so whether the
	// first poll saw one file or three decided the answer - a file seen alone settles a poll earlier and is read on its
	// own. It failed as a race under load and the fault was in the test, not the ordering.
	// All three exist before the poller does, so the first poll sees the whole set and the second reads it as one batch.
	files := map[string]string{}
	for _, n := range []int{3, 1, 2} {
		files[fmt.Sprintf("%02d.hl7", n)] = strings.Replace(oneMessage, "MSG1", fmt.Sprintf("SEQ%d", n), 1)
	}
	h := newFileHarness(t, nil, files)

	h.waitForMessages(3)

	var order []string
	for _, m := range h.sink.all() {
		for _, n := range []string{"SEQ1", "SEQ2", "SEQ3"} {
			if strings.Contains(string(m), n) {
				order = append(order, n)
			}
		}
	}
	want := []string{"SEQ1", "SEQ2", "SEQ3"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("read in order %v, want %v", order, want)
	}
}

// An archive that is the source directory must be refused at load.
//
// Otherwise the channel collects its own archive on the next poll and delivers every message a second time, then a
// third, forever.
func TestAnArchiveInsideTheSourceDirectoryIsRefused(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: loop
source:
  type: file
  file:
    root: /tmp
    dir: inbox
    after_read: move
    move_to: inbox
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "loop.yaml")

	if err == nil {
		t.Fatal("a channel that archives into its own source directory was accepted")
	}
	if !strings.Contains(err.Error(), "forever") {
		t.Errorf("the refusal does not explain the consequence: %v", err)
	}
}

// Framed and raw together must be refused, because they contradict each other.
func TestFramedAndRawTogetherAreRefused(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: both
source:
  type: file
  file:
    root: /tmp
    framed: true
    raw: true
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "both.yaml")

	if err == nil {
		t.Fatal("framed and raw were accepted together")
	}
	if !strings.Contains(err.Error(), "contradict") {
		t.Errorf("the refusal does not say why they conflict: %v", err)
	}
}
