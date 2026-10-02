package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A channel file that will not load must be visible, not merely absent.
//
// One sat in the test harness's own channel directory for a day. The server refused it, which was right, and then said nothing: no
// log line, no metric, and a status payload counting only the files that loaded. The dashboard therefore reported "1/1, all running"
// while a feed did not exist. Every number was true and the conclusion was false.
//
// This is the failure mode that matters most in a hospital, because a channel that never loaded produces no traffic and no errors.
// It produces nothing, which is indistinguishable from a quiet night.
func TestABrokenChannelFileIsReportedInStatus(t *testing.T) {
	dir := t.TempDir()

	// One that loads.
	write(t, dir, "good.yaml", `name: good
source:
  type: mllp
  listen: 127.0.0.1:0
destinations:
  - name: archive
    type: file
    dir: `+dir+`
`)

	// One with a plausible mistake: the listen address nested under a transport key, which is not the schema. This is the exact
	// error that was sitting in the harness, and it is the kind a person makes from memory rather than from the documentation.
	write(t, dir, "typo.yaml", `name: typo
source:
  type: mllp
  mllp:
    listen: 127.0.0.1:0
destinations:
  - name: archive
    type: file
    dir: `+dir+`
`)

	repo, err := NewChannelRepo(dir)
	if err != nil {
		t.Fatalf("could not open the repository: %v", err)
	}

	// The repository must separate the two rather than dropping the broken one.
	valid, broken, err := repo.List()
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}
	if len(valid) != 1 {
		t.Errorf("got %d valid channel(s), want 1", len(valid))
	}
	if len(broken) != 1 {
		t.Fatalf("got %d broken file(s), want 1 - the invalid file was dropped silently", len(broken))
	}

	// The reason has to name something a person can act on. "invalid" alone sends somebody reading the whole file.
	reason := broken["typo.yaml"]
	if reason == "" {
		t.Fatal("the broken file has no reason attached")
	}
	for _, want := range []string{"mllp", "line"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the reason %q does not mention %q, so it does not say where to look", reason, want)
		}
	}

	// And the status payload must carry the count, because that is what the dashboard reads.
	body := statusBody(NewRuntime(repo, nil, nil))

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("status does not marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("status does not round trip: %v", err)
	}

	got, ok := out["channelsBroken"]
	if !ok {
		t.Fatal("the status payload does not report channelsBroken, so the dashboard cannot know a file failed to load " +
			"and will say \"all running\"")
	}
	if n, isNum := got.(float64); !isNum || int(n) != 1 {
		t.Errorf("channelsBroken = %v, want 1", got)
	}

	// The count of loadable channels must not include the broken one - otherwise the dashboard shows a channel that does not exist.
	if total, _ := out["channelsTotal"].(float64); int(total) != 1 {
		t.Errorf("channelsTotal = %v, want 1: a file that will not load is not a channel", out["channelsTotal"])
	}
}

// write puts a file in the channel directory.
func write(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("could not write %s: %v", name, err)
	}
}
