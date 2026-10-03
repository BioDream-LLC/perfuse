package mirth

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/mirth/mirthlive"
)

// Perfuse's Mirth exporter against a running Mirth, which is the only test of it that means anything.
//
// There are nine tests of Export in export_test.go and every one of them is a round trip through this package: a Channel is written to
// XML and read back by our own parser, and the two are compared. That is agreement with ourselves. The doc comment on Export already
// admitted the doubt in writing - "Mirth may or may not accept depending on the plugin" - and nothing had ever checked.
//
// This repository has found the same shape twice already. A thousand SAML tests passed while no real identity provider could sign
// anybody in, because every test signed and verified with the same code. Five canonicalisation tests passed for the same reason.
//
// What makes the check possible is that Mirth answers a bad channel in a way that can be asserted on. It does not reject the POST. It
// accepts the document, discards what it did not understand, and stores a channel whose description has been replaced with "This
// channel is invalid. Verify all required extensions are loaded correctly" and whose destinations are gone. A test that only checked
// the status code would pass against a file Mirth had thrown away, which is how four hand-written fixtures got as far as they did.
//
// The round trip here is the strongest one available: a document Mirth itself wrote, read by our parser, written back out by our
// exporter, and returned to Mirth. Anything our exporter drops or renames shows up as an invalid channel.
//
// Every test runs once per engine that answers - Mirth 4.5.2, the Open Integration Engine and BridgeLink - because those are where
// a site leaving Mirth goes, and a channel that loads in one and not another is a migration that fails on the day. Start them with
// ./scripts/interop-up.sh. Skipped when none is reachable, so the suite still runs on a machine without them - and a skip is reported
// rather than silently passing.

// eachEngine runs f once per running engine, as a subtest named for it.
func eachEngine(t *testing.T, f func(t *testing.T, e mirthlive.Engine)) {
	t.Helper()
	for _, e := range mirthlive.Engines(t) {
		t.Run(e.Label(), func(t *testing.T) { f(t, e) })
	}
}

func truncate(s string, n int) string { return mirthlive.Truncate(s, n) }

// theInvalidChannelSentence is what an engine puts in the description of a channel it could not understand. Matching on it exactly is
// deliberate: an earlier test in this repository matched a phrase that both the pass and the fail contained, so it could not fail.
const theInvalidChannelSentence = mirthlive.InvalidChannelSentence

// exportFixture is Mirth's own document, through our parser and out through our exporter.
func exportFixture(t *testing.T, edit func(*Channel)) (*Channel, []byte) {
	t.Helper()
	ch, err := ParseChannelFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatalf("parsing the Mirth-authored fixture: %v", err)
	}
	if edit != nil {
		edit(ch)
	}
	var out bytes.Buffer
	if err := Export(&out, ch); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	return ch, out.Bytes()
}

func TestExportedChannelIsAcceptedByRealMirth(t *testing.T) {
	eachEngine(t, func(t *testing.T, e mirthlive.Engine) {
		ch, out := exportFixture(t, nil)
		stored := e.ImportAndReadBack(t, ch.ID, out)

		// The assertion that matters. The engine does not refuse a channel it cannot understand; it stores one with this sentence where
		// the description was, which is why checking the status code proves nothing.
		if strings.Contains(stored, theInvalidChannelSentence) {
			t.Errorf("%s stored our exported channel as invalid. What it kept:\n%s\n\nWhat we sent:\n%s",
				e.Label(), truncate(stored, 1500), truncate(string(out), 2500))
		}
	})
}

func TestExportedChannelKeepsItsDestinationsThroughRealMirth(t *testing.T) {
	eachEngine(t, func(t *testing.T, e mirthlive.Engine) {
		ch, out := exportFixture(t, nil)
		if len(ch.Destinations) == 0 {
			t.Fatal("the fixture has no destinations, so this test would pass without checking anything")
		}
		stored := e.ImportAndReadBack(t, ch.ID, out)

		// Destinations are the first thing discarded when a connector's properties are not what the engine expects, and losing them
		// silently is the worst outcome available: the channel still exists, still looks like a channel, and delivers nowhere.
		for _, d := range ch.Destinations {
			if d.Name != "" && !strings.Contains(stored, d.Name) {
				t.Errorf("destination %q did not survive the trip through %s. Stored:\n%s", d.Name, e.Label(), truncate(stored, 1500))
			}
		}
		// And the transports, because a destination that keeps its name while losing its transport is equally broken.
		if ch.Source.Transport != "" && !strings.Contains(stored, ch.Source.Transport) {
			t.Errorf("the source transport %q did not survive %s. Stored:\n%s", ch.Source.Transport, e.Label(), truncate(stored, 1500))
		}
	})
}

// TestExportedDescriptionSurvivesRealMirth is the control for the two tests above.
//
// Both of those assert on the absence of a sentence, and an absence assertion is only worth having if something in the same test proves
// the document arrived at all. A server that stored nothing, or a GET that returned an unrelated channel, would satisfy "does not
// contain invalid" perfectly.
func TestExportedDescriptionSurvivesRealMirth(t *testing.T) {
	eachEngine(t, func(t *testing.T, e mirthlive.Engine) {
		marker := fmt.Sprintf("perfuse export control %d", time.Now().UnixNano())
		ch, out := exportFixture(t, func(c *Channel) { c.Description = marker })
		stored := e.ImportAndReadBack(t, ch.ID, out)

		if !strings.Contains(stored, marker) {
			t.Errorf("the description we set did not come back from %s, so the absence assertions elsewhere prove nothing.\nStored:\n%s",
				e.Label(), truncate(stored, 1500))
		}
	})
}

// TestMirthAuthoredFixtureStillImports guards the premise of all of the above.
//
// If an engine stopped accepting Mirth's own document - a version change, a missing extension in the container - these tests would start
// failing and the exporter would be blamed. This one fails first and names the real reason.
func TestMirthAuthoredFixtureStillImports(t *testing.T) {
	doc, err := os.ReadFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	ch, err := ParseChannel(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}

	eachEngine(t, func(t *testing.T, e mirthlive.Engine) {
		stored := e.ImportAndReadBack(t, ch.ID, doc)
		if strings.Contains(stored, theInvalidChannelSentence) {
			t.Fatalf("%s will not accept the document Mirth wrote, so the exporter tests cannot mean anything. Regenerate the "+
				"fixture with ./scripts/mirth-author-channel.sh. Stored:\n%s", e.Label(), truncate(stored, 1200))
		}
	})
}
