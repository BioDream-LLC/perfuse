package mirth

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
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
// Start it with ./scripts/interop-up.sh, or docker start mirth. Skipped when unreachable, so the suite still runs on a machine without
// it - and a skip is reported rather than silently passing.

const mirthBase = "https://127.0.0.1:8443"

// mirthClient talks to the container, whose certificate is self-signed.
func mirthClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

// mirthDo makes an authenticated request. Mirth requires X-Requested-With on write operations, for the same reason Perfuse requires
// X-Perfuse-Request: it stops a form on another site from posting here.
func mirthDo(t *testing.T, method, path string, body io.Reader) (int, string) {
	t.Helper()

	req, err := http.NewRequest(method, mirthBase+path, body)
	if err != nil {
		t.Fatalf("building the %s request: %v", method, err)
	}
	req.SetBasicAuth("admin", "admin")
	req.Header.Set("X-Requested-With", "perfuse-test")
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}

	res, err := mirthClient().Do(req)
	if err != nil {
		t.Skipf("no Mirth on %s (%v). Start it with ./scripts/interop-up.sh", mirthBase, err)
	}
	defer res.Body.Close()

	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return res.StatusCode, string(out)
}

// requireMirth skips unless the server answers, so an unreachable container is a skip and not a failure.
func requireMirth(t *testing.T) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, mirthBase+"/api/server/status", nil)
	if err != nil {
		t.Fatalf("building the probe: %v", err)
	}
	req.SetBasicAuth("admin", "admin")
	req.Header.Set("X-Requested-With", "perfuse-test")

	res, err := mirthClient().Do(req)
	if err != nil {
		t.Skipf("no Mirth on %s (%v). Start it with ./scripts/interop-up.sh", mirthBase, err)
	}
	defer res.Body.Close()
}

// theInvalidChannelSentence is what Mirth puts in the description of a channel it could not understand. Matching on it exactly is
// deliberate: an earlier test in this repository matched a phrase that both the pass and the fail contained, so it could not fail.
const theInvalidChannelSentence = "This channel is invalid"

// postAndReadBack imports a document and returns what Mirth stored, which is not necessarily what was sent.
func postAndReadBack(t *testing.T, id string, doc []byte) string {
	t.Helper()

	// Any earlier copy goes first, or the POST is a no-op against an existing id and the test passes on stale data.
	mirthDo(t, http.MethodDelete, "/api/channels/"+id, nil)

	code, body := mirthDo(t, http.MethodPost, "/api/channels", bytes.NewReader(doc))
	if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusCreated {
		t.Fatalf("Mirth refused the import outright with %d:\n%s", code, truncate(body, 600))
	}

	code, stored := mirthDo(t, http.MethodGet, "/api/channels/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("Mirth accepted the import and then had no channel %s: GET returned %d", id, code)
	}
	if strings.TrimSpace(stored) == "" {
		t.Fatalf("Mirth accepted the import and stored nothing for %s", id)
	}

	t.Cleanup(func() { mirthDo(t, http.MethodDelete, "/api/channels/"+id, nil) })

	return stored
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... truncated"
}

func TestExportedChannelIsAcceptedByRealMirth(t *testing.T) {
	requireMirth(t)

	// Mirth's own document, through our parser, out through our exporter, and back to Mirth.
	ch, err := ParseChannelFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatalf("parsing the Mirth-authored fixture: %v", err)
	}

	var out bytes.Buffer
	if err := Export(&out, ch); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	stored := postAndReadBack(t, ch.ID, out.Bytes())

	// The assertion that matters. Mirth does not refuse a channel it cannot understand; it stores one with this sentence where the
	// description was, which is why checking the status code proves nothing.
	if strings.Contains(stored, theInvalidChannelSentence) {
		t.Errorf("Mirth stored our exported channel as invalid. What it kept:\n%s\n\nWhat we sent:\n%s",
			truncate(stored, 1500), truncate(out.String(), 2500))
	}
}

func TestExportedChannelKeepsItsDestinationsThroughRealMirth(t *testing.T) {
	requireMirth(t)

	ch, err := ParseChannelFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatalf("parsing the Mirth-authored fixture: %v", err)
	}
	if len(ch.Destinations) == 0 {
		t.Fatal("the fixture has no destinations, so this test would pass without checking anything")
	}

	var out bytes.Buffer
	if err := Export(&out, ch); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	stored := postAndReadBack(t, ch.ID, out.Bytes())

	// Destinations are the first thing Mirth discards when a connector's properties are not what it expects, and losing them silently
	// is the worst outcome available: the channel still exists, still looks like a channel, and delivers nowhere.
	for _, d := range ch.Destinations {
		if d.Name == "" {
			continue
		}
		if !strings.Contains(stored, d.Name) {
			t.Errorf("destination %q did not survive the trip through Mirth. Stored:\n%s", d.Name, truncate(stored, 1500))
		}
	}

	// And the transports, because a destination that keeps its name while losing its transport is equally broken.
	if ch.Source.Transport != "" && !strings.Contains(stored, ch.Source.Transport) {
		t.Errorf("the source transport %q did not survive. Stored:\n%s", ch.Source.Transport, truncate(stored, 1500))
	}
}

// TestExportedDescriptionSurvives is the control for the two tests above.
//
// Both of those assert on the absence of a sentence, and an absence assertion is only worth having if something in the same test proves
// the document arrived at all. A server that stored nothing, or a GET that returned an unrelated channel, would satisfy "does not
// contain invalid" perfectly.
func TestExportedDescriptionSurvivesRealMirth(t *testing.T) {
	requireMirth(t)

	ch, err := ParseChannelFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatalf("parsing the Mirth-authored fixture: %v", err)
	}

	marker := fmt.Sprintf("perfuse export control %d", time.Now().UnixNano())
	ch.Description = marker

	var out bytes.Buffer
	if err := Export(&out, ch); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	stored := postAndReadBack(t, ch.ID, out.Bytes())

	if !strings.Contains(stored, marker) {
		t.Errorf("the description we set did not come back, so the absence assertions elsewhere prove nothing.\nStored:\n%s",
			truncate(stored, 1500))
	}
}

// TestMirthAuthoredFixtureStillImports guards the premise of all of the above.
//
// If Mirth stopped accepting its own document - a version change, a missing extension in the container - these tests would start
// failing and the exporter would be blamed. This one fails first and names the real reason.
func TestMirthAuthoredFixtureStillImports(t *testing.T) {
	requireMirth(t)

	doc, err := os.ReadFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	ch, err := ParseChannel(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}

	stored := postAndReadBack(t, ch.ID, doc)

	if strings.Contains(stored, theInvalidChannelSentence) {
		t.Fatalf("this Mirth will not accept the document Mirth wrote, so the exporter tests cannot mean anything. Regenerate the "+
			"fixture with ./scripts/mirth-author-channel.sh. Stored:\n%s", truncate(stored, 1200))
	}
}
