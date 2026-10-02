package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// TestAPreprocessorRunsOnEveryFormatThatClaimsOne is the behavioural half of the scriptSlotsRun table.
//
// # Why it asserts on delivered bytes
//
// The config table says which formats run a preprocessor. A test that only checked the channel loaded would pass against a
// handler that never invokes one - which is exactly the defect this session found three times: shadow mode on five formats,
// scripts on six, and a v3 loader comment describing a feature that had no caller.
//
// So each case gives the preprocessor a substitution that changes the message into something recognisable, then checks what
// reached the destination. If the script did not run, the marker is absent and the test fails for the right reason.
func TestAPreprocessorRunsOnEveryFormatThatClaimsOne(t *testing.T) {
	// One interchange, one transmission, one CSV, one blob. Each carries a token the preprocessor rewrites.
	for _, tc := range []struct {
		name     string
		dataType string
		extra    string
		message  string
		// from and to are what the preprocessor substitutes.
		from, to string
	}{
		{
			name:     "x12",
			dataType: "x12",
			extra:    "",
			message: "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       *260830*1200*^*00501*000000001*0*P*:~" +
				"GS*HC*SENDER*RECEIVER*20260830*1200*1*X*005010X222A1~ST*837*0001*005010X222A1~" +
				"BHT*0019*00*0123*20260830*1200*CH~CLM*PATIENT001*500***11:B:1*Y*A*Y*Y~" +
				"REF*D9*CLAIM123~SE*5*0001~GE*1*1~IEA*1*000000001~",
			from: "CLAIM123", to: "REPAIRED",
		},
		{
			name:     "raw",
			dataType: "raw",
			message:  "anything at all, ORIGINAL",
			from:     "ORIGINAL", to: "REPAIRED",
		},
		{
			name:     "delimited",
			dataType: "delimited",
			extra:    "delimited:\n  delimiter: \",\"\n  has_header: true\n  split: false\n",
			message:  "PatientID,Surname\nP001,ORIGINAL\n",
			from:     "ORIGINAL", to: "REPAIRED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSender{}
			sink.name = "out"

			yaml := "\nname: pre-" + tc.name + "\ndataType: " + tc.dataType + "\n" + tc.extra +
				"scripts:\n  preprocessor: |\n    return message.replace('" + tc.from + "', '" + tc.to + "');\n" + `source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: tcp
    tcp:
      address: 127.0.0.1:1
      framing: length
`

			cfg, err := config.Load(strings.NewReader(yaml), "pre.yaml")
			if err != nil {
				t.Fatalf("loading a %s channel with a preprocessor: %v", tc.dataType, err)
			}

			c := startChannelFor(t, cfg, sink)

			if _, err := c.handle(context.Background(), []byte(tc.message)); err != nil {
				t.Fatalf("handling: %v", err)
			}

			if sink.count() == 0 {
				t.Fatal("nothing was delivered, so the preprocessor cannot be judged")
			}

			delivered := sink.last()
			if strings.Contains(delivered, tc.from) {
				t.Errorf("the delivered message still contains %q, so the preprocessor did not run on a %s channel - the config accepted it and the handler ignored it", tc.from, tc.dataType)
			}
			if !strings.Contains(delivered, tc.to) {
				t.Errorf("the delivered message does not contain %q; got: %s", tc.to, delivered)
			}
		})
	}
}

// startChannelFor builds and starts an engine around one channel.
func startChannelFor(t *testing.T, cfg *config.Channel, sink *recordingSender) *Channel {
	t.Helper()

	e, err := New([]*config.Channel{cfg}, func(config.Destination) (Sender, error) { return sink, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })

	chans := e.Channels()
	if len(chans) != 1 {
		t.Fatalf("got %d channels, want 1", len(chans))
	}

	return chans[0]
}

// TestADicomChannelRefusesAPreprocessor is the deliberate exception.
//
// A DICOM object is binary with pixel data in it. A script editing it as text will corrupt an image, which is the same reason
// DICOM gets named transformation steps rather than a general path writer. Refusing is the honest answer; running it would be
// worse than either alternative because the damage would reach an archive.
func TestADicomChannelRefusesAPreprocessor(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: dicom-pre
dataType: dicom
scripts:
  preprocessor: |
    return message;
source:
  type: dicom
  dicom:
    listen: "127.0.0.1:0"
    ae_title: PERFUSE
destinations:
  - name: out
    type: file
    dir: /tmp/out
`), "dicom.yaml")

	if err == nil {
		t.Fatal("a DICOM channel accepted a preprocessor, and a script editing a binary object as text will corrupt an image")
	}
	if !strings.Contains(err.Error(), "preprocessor") {
		t.Errorf("the refusal should name the slot, got: %v", err)
	}
}

// TestAFilterScriptIsRefusedOnAFormatThatStillCannotRunOne checks the per-slot granularity.
//
// # Why this test moved formats twice
//
// It was written against X12, which then gained filter and transformer scripts through the path binding - so the test started
// failing because the thing it asserted was refused had become supported. That is the guardrail working: the limit was a decision
// to change, not something discovered afterwards.
//
// Then it moved again, off SCRIPT, for the same reason: a prescription is XML, so it got the tree binding the HL7 formats use
// rather than the path binding, and the work turned out to be a branch calling a stage that already existed.
//
// Raw is what is left, and it is a decision rather than a gap. There is no addressable structure at all - a filter there could
// only ask about the bytes, which the expression filter cannot do either, so there is nothing to bind to. DICOM is the other, for
// a different reason again: an object is binary with pixel data in it.
//
// If this test has to move a third time, the thing to check first is whether any format still belongs here. A test asserting a
// refusal that no longer exists anywhere is worse than no test, because it looks like coverage.
func TestAFilterScriptIsRefusedOnAFormatThatStillCannotRunOne(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: raw-filter-script
dataType: raw
scripts:
  filter: |
    return true;
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: http
    http:
      url: http://127.0.0.1:1/in
`), "rawfilter.yaml")

	if err == nil {
		t.Fatal("a raw channel accepted a filter script, which would validate and never run")
	}
	if !strings.Contains(err.Error(), "filter") {
		t.Errorf("the refusal should name the slot, got: %v", err)
	}
	// The message has to say what does work, or somebody deletes the whole block including a preprocessor that was fine.
	if !strings.Contains(err.Error(), "preprocessor") {
		t.Errorf("the refusal should name the slots that do run, got: %v", err)
	}
}

// TestAPreprocessorAndAFilterScriptTogetherNamesOnlyTheFilter is the reason for per-slot reporting.
//
// A channel with a working preprocessor and an unsupported filter should be told about the filter. Refusing wholesale would make
// somebody delete the preprocessor as well, which was doing something useful.
func TestAPreprocessorAndAFilterScriptTogetherNamesOnlyTheFilter(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: raw-both
dataType: raw
scripts:
  preprocessor: |
    return message;
  filter: |
    return true;
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: http
    http:
      url: http://127.0.0.1:1/in
`), "rawboth.yaml")

	if err == nil {
		t.Fatal("the filter script was accepted")
	}

	// Singular, naming the one slot that is wrong.
	if !strings.Contains(err.Error(), "a script is set in filter") {
		t.Errorf("the refusal should name only the filter as the problem, got: %v", err)
	}
}
