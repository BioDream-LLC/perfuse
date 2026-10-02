package api

import (
	"os"
	"path/filepath"
	"testing"
)

// A mapping table beside the channels must not be reported as a broken channel.
//
// This is the documented layout - a table lives in a file next to the channels so it travels with them into version control - and the
// loader read every .yaml in the directory and tried to parse each one as a channel. It was invisible while load failures were silent.
// The moment they were reported, an installation doing exactly the right thing produced a startup warning and a non-zero
// broken-channels gauge.
//
// A false alarm is worse than no alarm. It teaches an operator that the warning means nothing, and the next one will be real.
func TestACodesetFileBesideTheChannelsIsNotABrokenChannel(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "feed.yaml", `name: feed
source:
  type: mllp
  listen: 127.0.0.1:0
destinations:
  - name: archive
    type: file
    dir: `+dir+`
`)

	// A real codeset file. Its top level is tables:, which is not a channel and never will be.
	writeFile(t, dir, "codes.codeset.yaml", `tables:
  - name: sex
    describes: PID-8 into FHIR gender
    entries:
      - from: M
        to: male
`)

	repo, err := NewChannelRepo(dir)
	if err != nil {
		t.Fatalf("could not open the repository: %v", err)
	}

	valid, broken, err := repo.List()
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}

	if len(broken) != 0 {
		t.Errorf("a companion file was reported as a broken channel, which is a false alarm on the documented layout: %v",
			broken)
	}
	if len(valid) != 1 {
		t.Errorf("got %d channel(s), want 1", len(valid))
	}

	// And the .yml spelling, since both are accepted for channels.
	writeFile(t, dir, "more.codeset.yml", "tables: []\n")

	if _, broken, err = repo.List(); err != nil {
		t.Fatalf("listing failed: %v", err)
	} else if len(broken) != 0 {
		t.Errorf("the .yml spelling of a companion file was reported as broken: %v", broken)
	}
}

// A genuinely broken channel must still be reported.
//
// The exclusion above is by filename suffix on purpose. Deciding by whether a file parses as a channel would quietly reclassify a real
// broken channel as something else, which is the fault this whole area exists to catch.
func TestExcludingCompanionFilesDoesNotHideABrokenChannel(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "typo.yaml", `name: typo
source:
  type: mllp
  mllp:
    listen: 127.0.0.1:0
`)
	writeFile(t, dir, "codes.codeset.yaml", "tables: []\n")

	repo, err := NewChannelRepo(dir)
	if err != nil {
		t.Fatalf("could not open the repository: %v", err)
	}

	_, broken, err := repo.List()
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}

	if _, reported := broken["typo.yaml"]; !reported {
		t.Errorf("a broken channel was not reported: %v", broken)
	}
	if _, wrongly := broken["codes.codeset.yaml"]; wrongly {
		t.Error("the companion file was still reported")
	}
}

// writeFile puts a file in the channel directory.
func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("could not write %s: %v", name, err)
	}
}
