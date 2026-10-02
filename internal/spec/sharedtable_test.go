package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A shared mapping table must be described by its real contents.
//
// A step can hold its table inline or name a shared one from a companion file. Only the inline form was ever read, so a channel using a
// shared table documented an empty translation - zero rows under "Code translations", the heading a receiving vendor relies on most.
//
// As a table that was quiet enough to miss for a long time. As a sentence it reads "holds 0 entries; a value not listed is passed through
// unchanged", which is confident, specific and false about the part of an interface most likely to be wrong in the first place. A vendor
// acting on it would conclude no translation happens at all.
func TestSharedTableIsDescribedByItsContents(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "codes.codeset.yaml"), []byte(`tables:
  - name: sex
    describes: administrative sex as this hospital sends it
    entries:
      - from: M
        to: MALE
      - from: F
        to: FEMALE
      - from: A
        to: OTHER
    default: U
`), 0o600); err != nil {
		t.Fatal(err)
	}

	channel := filepath.Join(dir, "feed.yaml")
	if err := os.WriteFile(channel, []byte(`name: shared
source:
  type: mllp
  listen: 127.0.0.1:6671
tables:
  - codes.codeset.yaml
transformations:
  - map:
      path: PID-8
      use: sex
destinations:
  - name: archive
    type: file
    dir: /tmp/out
`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := config.LoadFile(channel)
	if err != nil {
		t.Fatalf("the channel did not load: %v", err)
	}

	doc := Build(c)

	if len(doc.Mappings) != 1 {
		t.Fatalf("got %d mappings, want 1", len(doc.Mappings))
	}

	m := doc.Mappings[0]
	if len(m.From) != 3 {
		t.Errorf("the shared table reports %d entries, want 3; a table described as empty tells a vendor no translation happens", len(m.From))
	}

	// The rows must be the real ones, not merely the right count.
	joined := strings.Join(m.From, ",") + " -> " + strings.Join(m.To, ",")
	for _, want := range []string{"M", "F", "A", "MALE", "FEMALE", "OTHER"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the shared table is missing %q: %s", want, joined)
		}
	}

	// The shared table's own default governs, not the step's, which is empty here.
	if !strings.Contains(m.Unmatched, "U") {
		t.Errorf("the shared table's default is not described: %q", m.Unmatched)
	}

	// And the narration must say the same thing, since that is the form somebody reads.
	narration := strings.Join(doc.Narration, " ")
	if strings.Contains(narration, "0 entries") {
		t.Errorf("the narration claims the shared table is empty: %s", narration)
	}
	if !strings.Contains(narration, "3 entries") {
		t.Errorf("the narration does not report the shared table's size: %s", narration)
	}
}

// A channel naming a table that does not exist must be refused at load.
//
// I wrote this expecting to test the specification's fallback wording and found the loader had already made the case unreachable: it
// refuses the channel outright. That is the stronger guarantee and the right place for it - a feature that cannot work is refused at load
// rather than skipped at run time, so nothing downstream ever has to describe a mapping it cannot see.
//
// The fallback wording in mappingOf stays as a backstop, because Build can be handed a config that was assembled rather than loaded, and
// "I could not read the table" must never render as "the table is empty".
func TestAChannelNamingAMissingTableIsRefused(t *testing.T) {
	_, err := config.Load(strings.NewReader(`name: missing
source:
  type: mllp
  listen: 127.0.0.1:6672
transformations:
  - map:
      path: PID-8
      use: nowhere
destinations:
  - name: archive
    type: file
    dir: /tmp/out
`), "(test)")
	if err == nil {
		t.Fatal("a channel naming a table that does not exist was accepted; nothing downstream can describe that mapping honestly")
	}

	// The error has to name the table, or somebody has to guess which of several steps is wrong.
	if !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("the refusal does not name the missing table: %v", err)
	}
}
