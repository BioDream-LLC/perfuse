package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every format block is refused on every data type but its own.
//
// # Why this is a loop over the whole grid
//
// The refusals used to live in the switch on data type, one arm at a time, and that shape fails in a particular way: a new block has
// to be added to every arm, and the arm somebody forgets accepts it in silence. Two arms never carried any block refusals at all,
// because hl7v3 returned early and x12 fell through to the envelope checks.
//
// A probe over the whole grid found eight wrong acceptances: an ncpdp block on hl7, dicom, raw, delimited and x12; a delimited block
// on dicom and x12; an hl7v3 block on x12. Each of those channels loaded and reported no problem, and each would have done nothing
// with the block.
//
// So the guard is the grid rather than a case per pair. A block added to formatBlocks without a fixture here fails on the first line
// of the test, and a block added to the config without a row in formatBlocks fails the drift test in settings_test.go - which is the
// pairing that makes forgetting hard.

// blockFixtures is the smallest loadable form of each format block.
//
// Minimal on purpose. A block with more in it can be refused for a reason other than the data type, which would make this pass while
// testing nothing - the failure that a mismatched block is accepted looks exactly like a fixture that was refused for its contents.
func blockFixtures() map[string]string {
	return map[string]string{
		"hl7v3":     "hl7v3:\n  acknowledge: false\n",
		"x12":       "x12:\n  envelope: ignore\n",
		"delimited": "delimited:\n  columns: [a, b]\n",
		"ncpdp":     "ncpdp:\n  transformations:\n    - set: {path: A3, value: B1}\n",
		"script":    "script:\n  transformations:\n    - clear: {path: \"//DrugDescription\"}\n",
		"dicom":     "dicom:\n  transformations:\n    - deidentify: {}\n",
	}
}

// TestEveryFormatBlockIsRefusedOnEveryOtherType walks the grid.
func TestEveryFormatBlockIsRefusedOnEveryOtherType(t *testing.T) {
	fixtures := blockFixtures()

	// Every block in the table needs a fixture, or the grid has a hole that looks like a pass.
	for _, b := range formatBlocks {
		if _, ok := fixtures[b.name]; !ok {
			t.Fatalf("formatBlocks has a %q row with no fixture in this test, so nothing checks where it is refused",
				b.name)
		}
	}

	for _, b := range formatBlocks {
		for _, dt := range KnownDataTypes {
			if dt == b.owner {
				continue
			}

			t.Run(b.name+"-on-"+string(dt), func(t *testing.T) {
				body := "name: grid\ndataType: " + string(dt) + "\n" + fixtures[b.name] + `source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: file
    dir: /tmp/grid
`

				_, err := Load(strings.NewReader(body), filepath.Join(t.TempDir(), "c.yaml"))
				if err == nil {
					t.Fatalf("a %s block was accepted on a %s channel. The block is read by code that never runs on "+
						"this format, so the channel would load, report success, and do none of what it asked",
						b.name, dt)
				}

				// The refusal has to name the block, or somebody with several blocks set cannot tell which one is wrong.
				if !strings.Contains(err.Error(), b.name) {
					t.Errorf("the refusal does not name the %s block, so it is not actionable: %v", b.name, err)
				}
			})
		}
	}
}

// TestEveryFormatBlockIsAcceptedOnItsOwnType is the control.
//
// Without it the test above passes if the loader refuses every block everywhere, which would be a config that cannot express
// anything. This is the half that says the fixtures are otherwise valid.
func TestEveryFormatBlockIsAcceptedOnItsOwnType(t *testing.T) {
	fixtures := blockFixtures()

	for _, b := range formatBlocks {
		t.Run(b.name, func(t *testing.T) {
			body := "name: own\ndataType: " + string(b.owner) + "\n" + fixtures[b.name] + `source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: file
    dir: /tmp/grid
`

			if _, err := Load(strings.NewReader(body), filepath.Join(t.TempDir(), "c.yaml")); err != nil {
				t.Fatalf("a %s block was refused on its own data type, so the grid test above proves nothing: %v",
					b.name, err)
			}
		})
	}
}

// TestEveryFormatSpecificBlockHasARowInTheTable is the drift guard.
//
// The table is only exhaustive if every block is in it. A block added to Channel without a row would be accepted on all eight types,
// which is the defect this whole file exists to close - so the list is checked against the yaml keys the loader knows.
func TestEveryFormatSpecificBlockHasARowInTheTable(t *testing.T) {
	// The format-specific blocks, by their yaml key. Kept here rather than derived, because deriving it from the struct would
	// have to guess which blocks are format-specific and which are cross-cutting like attachments or scripts - and a wrong
	// guess in either direction makes this test useless.
	want := []string{"hl7v3", "x12", "delimited", "ncpdp", "script", "dicom"}

	have := map[string]bool{}
	for _, b := range formatBlocks {
		have[b.name] = true
	}

	for _, name := range want {
		if !have[name] {
			t.Errorf("the %s block has no row in formatBlocks, so it is accepted on every data type", name)
		}
	}

	if len(formatBlocks) != len(want) {
		t.Errorf("formatBlocks has %d rows and %d blocks are expected; if a format block was added, add it to want and "+
			"give it a fixture in blockFixtures", len(formatBlocks), len(want))
	}
}
