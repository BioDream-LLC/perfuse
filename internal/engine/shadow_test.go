package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// writeChannelFile puts a channel on disk, which is how a shadow candidate is
// referenced.
func writeChannelFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const shadowTestMessage = "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|SH1|P|2.5.1\r" +
	"PID|1||MRN9^^^SITEA^MR||o'brien^siobhan||19910228|f\r"

// shadowChannelPair builds a live channel shadowing a candidate written to disk.
func shadowChannelPair(t *testing.T, candidateBody string, sh config.Shadow, capture *captureDest) *Channel {
	t.Helper()

	path := writeChannelFile(t, "candidate.yaml", candidateBody)
	sh.Channel = path

	cfg := &config.Channel{
		Name:   "live",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Shadow: &sh,
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
			Retry: config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.startShadow(config.LoadFile); err != nil {
		t.Fatalf("startShadow: %v", err)
	}
	t.Cleanup(func() { _ = ch.stopShadow() })
	return ch
}

func TestShadowReportsNoDifferenceForAnIdenticalCandidate(t *testing.T) {
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{}, capture)

	if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
		t.Fatal(err)
	}

	r := ch.ShadowReport()
	if r == nil {
		t.Fatal("no shadow report")
	}
	if r.Stats.Compared != 1 {
		t.Errorf("compared = %d", r.Stats.Compared)
	}
	if r.Stats.Differed != 0 {
		t.Errorf("differed = %d, want 0: %+v", r.Stats.Differed, r.Differences)
	}
	// The verdict has to be honest about what identical output does and does not
	// prove, because the whole point is somebody deciding whether to promote.
	if !strings.Contains(r.Verdict, "not proof") {
		t.Errorf("the verdict overstates the evidence: %q", r.Verdict)
	}
}

func TestShadowFindsATransformationDifference(t *testing.T) {
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
transformations:
  - case: {path: PID-5.1, to: upper}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{}, capture)

	if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
		t.Fatal(err)
	}

	r := ch.ShadowReport()
	if r.Stats.Differed != 1 {
		t.Fatalf("differed = %d, want 1", r.Stats.Differed)
	}
	if len(r.Differences) != 1 {
		t.Fatalf("differences = %d", len(r.Differences))
	}

	d := r.Differences[0]
	if d.Kind != "transform" {
		t.Errorf("kind = %q", d.Kind)
	}
	if d.ControlID != "SH1" {
		t.Errorf("control id = %q; a difference has to be traceable to a message",
			d.ControlID)
	}

	// Field level, not byte level. That is the difference between a usable report and
	// a character diff of a pipe-delimited string that nobody can read.
	found := false
	for _, f := range d.Fields {
		if f.Path == "PID-5.1" {
			found = true
			if f.Live != "o'brien" || f.Candidate != "O'BRIEN" {
				t.Errorf("PID-5.1 live=%q candidate=%q", f.Live, f.Candidate)
			}
		}
	}
	if !found {
		t.Errorf("PID-5.1 was not reported: %+v", d.Fields)
	}
}

func TestShadowCountsAFilterDisagreementSeparately(t *testing.T) {
	// The most consequential disagreement available: one version keeps a message the
	// other drops, which decides whether the receiving system hears about the patient
	// at all. A transformation difference changes a value.
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
filter: MSH-9.2 != "A01"
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{}, capture)

	if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
		t.Fatal(err)
	}

	r := ch.ShadowReport()
	if r.Stats.FilterDisagreed != 1 {
		t.Errorf("filterDisagreed = %d, want 1", r.Stats.FilterDisagreed)
	}
	if len(r.Differences) != 1 || r.Differences[0].Kind != "filter" {
		t.Fatalf("differences = %+v", r.Differences)
	}
	if !strings.Contains(r.Differences[0].Note, "hears about this patient") {
		t.Errorf("the note should say why a filter change is bigger: %q",
			r.Differences[0].Note)
	}
	// And the verdict has to lead with it rather than lumping it in with the rest.
	if !strings.Contains(r.Verdict, "disagree about whether to keep") {
		t.Errorf("verdict = %q", r.Verdict)
	}
}

func TestShadowNeverDelivers(t *testing.T) {
	// The property that makes this safe to run against live traffic. A candidate with
	// its own destination must not produce a single delivery.
	live := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
destinations:
  - name: elsewhere
    type: file
    dir: /tmp/perfuse-shadow-must-not-exist
`, config.Shadow{}, live)

	for i := 0; i < 5; i++ {
		if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
			t.Fatal(err)
		}
	}

	// The live destination got everything.
	if n := len(live.messages()); n != 5 {
		t.Errorf("the live destination received %d messages, want 5", n)
	}
	// And the candidate's directory was never created, because its senders were never
	// constructed.
	if _, err := os.Stat("/tmp/perfuse-shadow-must-not-exist"); err == nil {
		t.Fatal("the shadow channel wrote to its own destination")
	}
	if ch.ShadowReport().Stats.Compared != 5 {
		t.Errorf("compared = %d", ch.ShadowReport().Stats.Compared)
	}
}

func TestShadowChannelRefusesToBuildASender(t *testing.T) {
	// Belt and braces on the same property, at the level of the type. If anything ever
	// does try to build a sender for a shadow, it must be loud rather than quietly
	// sending a message somewhere.
	cfg := &config.Channel{
		Name:   "candidate",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP, Address: "127.0.0.1:1",
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	sh, err := newShadowChannel(cfg, quiet())
	if err != nil {
		t.Fatalf("newShadowChannel: %v", err)
	}
	defer sh.Close()

	// It can transform.
	res, err := sh.TransformOnly(context.Background(), []byte(shadowTestMessage))
	if err != nil {
		t.Fatalf("TransformOnly: %v", err)
	}
	if !res.Accepted {
		t.Error("the message should have been accepted")
	}

	// And it has no senders at all.
	if len(sh.dests) != 0 {
		t.Errorf("a shadow channel has %d destination(s); it should have none", len(sh.dests))
	}
}

func TestShadowDoesNotStopTheLiveChannelWhenTheCandidateFails(t *testing.T) {
	// A candidate is by definition not trusted yet. A failure in it must be recorded
	// and otherwise ignored, or shadow mode becomes a way to break production while
	// trying to avoid breaking production.
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
scripts:
  transformer: |
    throw new Error("the candidate is broken");
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{}, capture)

	ack, err := ch.handle(context.Background(), []byte(shadowTestMessage))
	if err != nil {
		t.Fatalf("the live channel failed because the candidate did: %v", err)
	}
	if !strings.Contains(string(ack), "AA") {
		t.Errorf("the live message should still be accepted: %s", ack)
	}
	if len(capture.messages()) != 1 {
		t.Error("the live message was not delivered")
	}

	r := ch.ShadowReport()
	if r.Stats.CandidateFailed != 1 {
		t.Errorf("candidateFailed = %d, want 1", r.Stats.CandidateFailed)
	}
	// The verdict has to be unambiguous about this one.
	if !strings.Contains(r.Verdict, "not ready") {
		t.Errorf("verdict = %q", r.Verdict)
	}
}

func TestShadowObservesFilteredMessagesToo(t *testing.T) {
	// The messages a filter change affects are exactly the ones a naive
	// implementation misses, because they return early on the live path.
	capture := &captureDest{}

	path := writeChannelFile(t, "candidate.yaml", `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	cfg := &config.Channel{
		Name:   "live",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		// The live channel filters this message out.
		Filter: `MSH-9.2 != "A01"`,
		Shadow: &config.Shadow{Channel: path},
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
			Retry: config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.startShadow(config.LoadFile); err != nil {
		t.Fatal(err)
	}
	defer ch.stopShadow()

	if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
		t.Fatal(err)
	}

	r := ch.ShadowReport()
	if r.Stats.Compared != 1 {
		t.Fatalf("a filtered message was not compared: %+v", r.Stats)
	}
	if r.Stats.FilterDisagreed != 1 {
		t.Errorf("the filter disagreement was not found: %+v", r.Stats)
	}
}

func TestShadowIgnoresTheConfiguredPaths(t *testing.T) {
	// Needed in practice: a channel that stamps a timestamp differs on every message
	// and would report 100% while telling you nothing.
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
transformations:
  - set: {path: MSH-7, value: "20991231235959"}
  - case: {path: PID-5.1, to: upper}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{Ignore: []string{"MSH-7"}}, capture)

	if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
		t.Fatal(err)
	}

	d := ch.ShadowReport().Differences[0]
	for _, f := range d.Fields {
		if strings.HasPrefix(f.Path, "MSH-7") {
			t.Errorf("an ignored path was reported: %+v", f)
		}
	}
	// The one that was not ignored still has to appear, or ignore would be hiding
	// everything.
	found := false
	for _, f := range d.Fields {
		if f.Path == "PID-5.1" {
			found = true
		}
	}
	if !found {
		t.Errorf("the real difference was not reported: %+v", d.Fields)
	}
}

func TestShadowCanCompareOnlyChosenPaths(t *testing.T) {
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
transformations:
  - case: {path: PID-5.1, to: upper}
  - case: {path: PID-8, to: upper}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{Compare: []string{"PID-8"}}, capture)

	if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
		t.Fatal(err)
	}

	d := ch.ShadowReport().Differences[0]
	if len(d.Fields) != 1 || d.Fields[0].Path != "PID-8" {
		t.Errorf("compare should have limited the report to PID-8: %+v", d.Fields)
	}
}

func TestShadowRefusesAnInvalidCandidate(t *testing.T) {
	// A shadow that tolerated an invalid candidate would report differences for a
	// version that could never be promoted, which is misleading rather than useless.
	capture := &captureDest{}

	path := writeChannelFile(t, "bad.yaml", `
name: candidate
source:
  type: mllp
  listen: not-an-address
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`)

	cfg := &config.Channel{
		Name:   "live",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Shadow: &config.Shadow{Channel: path},
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	err = ch.startShadow(config.LoadFile)
	if err == nil {
		t.Fatal("an invalid candidate should be refused")
	}
	if !strings.Contains(err.Error(), "could not be promoted") {
		t.Errorf("the error should say why it matters: %v", err)
	}
}

func TestShadowRefusesAMissingCandidate(t *testing.T) {
	capture := &captureDest{}
	cfg := &config.Channel{
		Name:   "live",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Shadow: &config.Shadow{Channel: "/nonexistent/candidate.yaml"},
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ch, _ := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())

	if err := ch.startShadow(config.LoadFile); err == nil {
		t.Fatal("a missing candidate file should be refused")
	}
}

func TestShadowConfigIsCheckedAtLoad(t *testing.T) {
	cases := []struct {
		name string
		sh   config.Shadow
		want string
	}{
		{"no channel", config.Shadow{}, "shadow.channel is required"},
		{"sample too high", config.Shadow{Channel: "c.yaml", Sample: 2}, "fraction between"},
		{"sample negative", config.Shadow{Channel: "c.yaml", Sample: -0.5}, "fraction between"},
		{"no differences kept", config.Shadow{
			Channel: "c.yaml", MaxDifferences: -1,
		}, "nothing to look at"},
		{
			// The result would be silently empty, and the report would say the two
			// versions agree.
			"path in both lists",
			config.Shadow{
				Channel: "c.yaml",
				Compare: []string{"PID-5.1"}, Ignore: []string{"PID-5.1"},
			},
			"would be excluded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sh.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestShadowAlwaysSaysItCannotDeliver(t *testing.T) {
	// Said every start, because it is the property somebody will want reassurance
	// about before pointing this at live traffic.
	warnings := (&config.Shadow{Channel: "c.yaml", Ignore: []string{"MSH-7"}}).Warnings()
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "cannot deliver anything") {
		t.Errorf("the warnings should state that a shadow cannot deliver: %v", warnings)
	}
}

func TestShadowBoundsHowManyDifferencesItKeeps(t *testing.T) {
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
transformations:
  - case: {path: PID-5.1, to: upper}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{MaxDifferences: 3}, capture)

	for i := 0; i < 10; i++ {
		if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
			t.Fatal(err)
		}
	}

	r := ch.ShadowReport()
	if len(r.Differences) != 3 {
		t.Errorf("kept %d differences, want 3", len(r.Differences))
	}
	if r.Stats.Differed != 10 {
		t.Errorf("differed = %d; the count must not be bounded even though the "+
			"stored examples are", r.Stats.Differed)
	}
}

func TestShadowSamplingSkipsSomeMessages(t *testing.T) {
	capture := &captureDest{}
	ch := shadowChannelPair(t, `
name: candidate
source:
  type: mllp
  listen: 127.0.0.1:0
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, config.Shadow{Sample: 0.1}, capture)

	for i := 0; i < 200; i++ {
		if _, err := ch.handle(context.Background(), []byte(shadowTestMessage)); err != nil {
			t.Fatal(err)
		}
	}

	r := ch.ShadowReport()
	if r.Stats.Compared+r.Stats.Skipped != 200 {
		t.Errorf("compared %d + skipped %d != 200", r.Stats.Compared, r.Stats.Skipped)
	}
	// Loose bounds, because it is a random sample and a flaky test is worse than a
	// weak one.
	if r.Stats.Compared == 0 || r.Stats.Compared > 80 {
		t.Errorf("compared %d of 200 at sample 0.1, which is not a 10%% sample",
			r.Stats.Compared)
	}
	// And the live channel delivered all of them regardless.
	if len(capture.messages()) != 200 {
		t.Errorf("the live channel delivered %d of 200", len(capture.messages()))
	}
}

func TestNoShadowMeansNoReport(t *testing.T) {
	capture := &captureDest{}
	cfg := &config.Channel{
		Name:   "plain",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ch, _ := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())

	if err := ch.startShadow(config.LoadFile); err != nil {
		t.Fatalf("a channel with no shadow should start cleanly: %v", err)
	}
	if ch.ShadowReport() != nil {
		t.Error("a channel with no shadow should have no report")
	}
}
