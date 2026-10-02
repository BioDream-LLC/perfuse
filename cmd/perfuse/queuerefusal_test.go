package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A destination that asks for a durable queue must not be run without one.
//
// serve prepares the queue whether or not any channel wants it, and the comment
// there says why: so that enabling it cannot fail at the first delivery instead of
// at load. run had no queue at all and said nothing, so a destination configured to
// retry indefinitely made five in-memory attempts and dropped the message. The
// operator had asked, in the only way the configuration offers, for that never to
// happen.
//
// check had the matching gap: it printed the in-memory attempt count and nothing
// about the queue, so the summary for a queued destination was indistinguishable
// from one that loses everything after five tries.

const queuedChannel = `name: queued
source:
  type: mllp
  listen: 127.0.0.1:12987
destinations:
  - name: downstream
    type: tcp
    tcp:
      address: 127.0.0.1:12988
      framing: delimited
      delimiter: "\r"
    queue:
      enabled: true
      max_attempts: 0
`

const plainChannel = `name: plain
source:
  type: mllp
  listen: 127.0.0.1:12989
destinations:
  - name: downstream
    type: file
    dir: /tmp/perfuse-queue-test-out
`

func writeChannel(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "channel.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

func TestRunRefusesADurableQueueItCannotProvide(t *testing.T) {
	path := writeChannel(t, queuedChannel)

	channels, err := loadChannels([]string{path})
	if err != nil {
		t.Fatalf("loading the fixture: %v", err)
	}

	// Checked through the function rather than through cmdRun, which blocks once it
	// starts. Driving cmdRun here meant that removing the refusal made this test hang
	// until the ten-minute test timeout instead of failing, and a test that takes ten
	// minutes to report a regression will be deleted by whoever is waiting for it.
	// TestRunCallsTheQueueRefusal covers the wiring.
	err = refuseQueuesWithoutAStore(channels)
	if err == nil {
		t.Fatal("a channel with queue.enabled was accepted. run has no database, so the queue is " +
			"silently absent and the destination falls back to in-memory retries - data loss on " +
			"the one setting that exists to prevent it")
	}
	if !strings.Contains(err.Error(), "perfuse serve") {
		t.Errorf("the refusal does not say where the queue does work, which is the difference "+
			"between a rejection and an answer. Got: %v", err)
	}
	if !strings.Contains(err.Error(), "queued/downstream") {
		t.Errorf("the refusal does not name which destination is at fault, so an operator with "+
			"thirty channels cannot act on it. Got: %v", err)
	}
}

func TestRunCallsTheQueueRefusal(t *testing.T) {
	// The refusal being correct is useless if nothing calls it, and cmdRun cannot be
	// driven from a test without blocking. Read from source instead, the same way the
	// dependency and framing guards do.
	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("reading run.go: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "func cmdRun(")
	if start < 0 {
		t.Fatal("cmdRun not found in run.go; this guard has stopped checking anything")
	}
	rest := body[start+1:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		t.Fatal("could not find where cmdRun ends. Without a boundary this reads the rest of the " +
			"file and passes on the helper's own definition")
	}

	// The call, with its argument, rather than the bare name. The first version of this
	// guard searched for the name and found it in the helper's doc comment, which sits
	// just above the boundary - so deleting the call left the test passing on a comment
	// about the function it was meant to prove was called.
	if !strings.Contains(rest[:end], "refuseQueuesWithoutAStore(channels)") {
		t.Error("cmdRun does not call refuseQueuesWithoutAStore(channels), so a destination " +
			"asking for a durable queue will be run without one and lose messages silently")
	}
}

func TestRunStillStartsWithoutAQueue(t *testing.T) {
	// Without this, refusing every channel would pass the test above. The channel is
	// only loaded far enough to get past the queue check: cmdRun blocks once it starts,
	// so what is asserted is that it did not fail for this reason.
	path := writeChannel(t, plainChannel)

	channels, err := loadChannels([]string{path})
	if err != nil {
		t.Fatalf("loading a plain channel: %v", err)
	}
	if err := refuseQueuesWithoutAStore(channels); err != nil {
		t.Errorf("a channel with no queue was refused: %v", err)
	}
}

func TestCheckReportsADurableQueue(t *testing.T) {
	path := writeChannel(t, queuedChannel)

	var out, errOut bytes.Buffer
	if err := cmdCheck([]string{path}, &out, &errOut); err != nil {
		t.Fatalf("check rejected the fixture: %v (%s)", err, errOut.String())
	}

	got := out.String()
	if !strings.Contains(got, "queued") {
		t.Errorf("check says nothing about the durable queue, so its summary for a queued "+
			"destination is identical to one that drops the message after five attempts. "+
			"Output was:\n%s", got)
	}
	if !strings.Contains(got, "retries forever") {
		t.Errorf("max_attempts 0 means retry indefinitely and the summary does not say so:\n%s", got)
	}
	if !strings.Contains(got, "perfuse serve") {
		t.Errorf("the summary does not mention that the queue needs serve, which is the thing "+
			"that would have prevented the message loss:\n%s", got)
	}
}

func TestCheckSaysNothingAboutAQueueWhenThereIsNone(t *testing.T) {
	// The reverse guard. A line printed unconditionally would satisfy the test above
	// while telling an operator their unqueued destination is durable.
	path := writeChannel(t, plainChannel)

	var out, errOut bytes.Buffer
	if err := cmdCheck([]string{path}, &out, &errOut); err != nil {
		t.Fatalf("check rejected the plain fixture: %v (%s)", err, errOut.String())
	}
	if strings.Contains(out.String(), "queued") {
		t.Errorf("check reported a queue for a destination that has none:\n%s", out.String())
	}
}
