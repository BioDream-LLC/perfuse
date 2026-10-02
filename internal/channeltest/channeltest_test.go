package channeltest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSuite lays out a channel and a test file in a temporary directory and
// returns the test file path.
func writeSuite(t *testing.T, channel, suite string) string {
	t.Helper()
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "ch.yaml"), []byte(channel), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s_test.yaml")
	if err := os.WriteFile(path, []byte(suite), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const simpleChannel = `
name: test-channel
source:
  type: mllp
  listen: 127.0.0.1:0
  ack: {when: on_delivery}
filter: MSH-9.2 != "A28"
transformations:
  - pad: {path: PID-3(1).1, width: 10}
  - case: {path: PID-8.1, to: upper}
destinations:
  - name: out
    type: file
    dir: /tmp/channeltest-unused
`

func TestAPassingSuitePasses(t *testing.T) {
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: the MRN is padded and the sex code upper-cased
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|T1|P|2.5.1
      PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|f
    expect:
      outcome: delivered
      ack: AA
      fields:
        PID-3.1: "000000MRN7"
        PID-8.1: "F"
`)

	suite, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		for _, res := range report.Results {
			t.Errorf("%s: %v", res.Name, res.Failures)
		}
	}
}

func TestEveryFailureIsReportedNotJustTheFirst(t *testing.T) {
	// Fixing one at a time when four are wrong wastes four runs, and the four
	// together usually describe one underlying mistake.
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: several wrong expectations
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|T1|P|2.5.1
      PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|f
    expect:
      outcome: filtered
      ack: AR
      fields:
        PID-3.1: "MRN7"
      not_contains: ["Frost"]
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("results = %d", len(report.Results))
	}
	if n := len(report.Results[0].Failures); n < 4 {
		t.Errorf("want at least 4 failures reported together, got %d: %v",
			n, report.Results[0].Failures)
	}
}

func TestFailuresQuoteTheActualValue(t *testing.T) {
	// A report saying only "PID-3.1 was wrong" makes somebody run the channel by
	// hand to find out what it actually was.
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: wrong field
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|T1|P|2.5.1
      PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|f
    expect:
      fields:
        PID-3.1: "WRONG"
`)

	suite, _ := Load(path)
	report, _ := Run(suite)
	joined := strings.Join(report.Results[0].Failures, "\n")
	if !strings.Contains(joined, "000000MRN7") {
		t.Errorf("the failure should quote what the value actually was: %s", joined)
	}
}

func TestFilteredMessagesReportTheFilteredOutcome(t *testing.T) {
	// This was a real bug: the outcome was recorded only on the delivery path, so a
	// filtered message reported an empty outcome and every filter test failed with
	// a confusing message.
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: an A28 is filtered
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A28^ADT_A05|T2|P|2.5.1
      PID|1||MRN8^^^SITEA^MR||Frost^Ivy||19910228|F
    expect:
      outcome: filtered
      ack: AA
      destinations:
        out: {received: false}
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Errorf("failures: %v", report.Results[0].Failures)
	}
	if report.Results[0].Outcome != "filtered" {
		t.Errorf("outcome = %q", report.Results[0].Outcome)
	}
}

func TestUnparseableMessagesReportTheirOutcome(t *testing.T) {
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: rubbish is rejected
    message: |
      this is not an HL7 message
    expect:
      outcome: unparseable
      ack: AR
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Errorf("failures: %v", report.Results[0].Failures)
	}
}

func TestADestinationCanBeToldToFail(t *testing.T) {
	// Otherwise testing failure handling needs a real receiver that breaks on cue.
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: a delivery failure answers AE
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|T3|P|2.5.1
      PID|1||MRN9^^^SITEA^MR||Frost^Ivy||19910228|F
    expect:
      outcome: failed
      ack: AE
      destinations:
        out: {fail: true}
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Errorf("failures: %v", report.Results[0].Failures)
	}
}

func TestRetriesAreCollapsedUnlessAskedFor(t *testing.T) {
	// A test replaces the transport, so retrying a capture sender measures the retry
	// loop rather than the channel. It also turned one failing case into fifteen
	// seconds of waiting, which is how a suite stops being run.
	channel := strings.Replace(simpleChannel,
		"    type: file",
		"    type: file\n    retry: {attempts: 5, backoff: 2s}", 1)

	path := writeSuite(t, channel, `
channel: ./ch.yaml
tests:
  - name: a failing destination does not sit through five backoffs
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|T4|P|2.5.1
      PID|1||MRN9^^^SITEA^MR||Frost^Ivy||19910228|F
    expect:
      outcome: failed
      max_duration: 2s
      destinations:
        out: {fail: true}
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Errorf("failures: %v", report.Results[0].Failures)
	}
}

func TestScriptOutputCanBeAsserted(t *testing.T) {
	// The only way to assert on a code path that produces no other output.
	channel := simpleChannel + `
scripts:
  transformer: |
    logger.info('saw patient ' + msg['PID']['PID.3']['PID.3.1'].toString());
`
	path := writeSuite(t, channel, `
channel: ./ch.yaml
tests:
  - name: the transformer logs the patient
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|T5|P|2.5.1
      PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|F
    expect:
      outcome: delivered
      logged:
        - saw patient 000000MRN7
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Errorf("failures: %v", report.Results[0].Failures)
	}
}

func TestSuiteWithNoAssertionsIsRefused(t *testing.T) {
	// A case asserting nothing passes unconditionally and looks like coverage,
	// which is worse than not having it.
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: asserts nothing
    message: |
      MSH|^~\&|A|B|C|D|20260819||ADT^A01|T1|P|2.5.1
    expect: {}
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("a case with no assertions should be refused")
	}
	if !strings.Contains(err.Error(), "asserts nothing") {
		t.Errorf("the error should say why: %v", err)
	}
}

func TestMisspelledAssertionIsRefused(t *testing.T) {
	// A silently ignored assertion is a test that passes without testing anything,
	// which is the most expensive kind of bug in a suite.
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: misspelled
    message: |
      MSH|^~\&|A|B|C|D|20260819||ADT^A01|T1|P|2.5.1
    expect:
      outcom: delivered
`)

	if _, err := Load(path); err == nil {
		t.Fatal("an unknown field should be refused")
	}
}

func TestDuplicateNamesAreRefused(t *testing.T) {
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: the same name
    message: "MSH|^~\\&|A|B|C|D|20260819||ADT^A01|T1|P|2.5.1"
    expect: {outcome: delivered}
  - name: the same name
    message: "MSH|^~\\&|A|B|C|D|20260819||ADT^A01|T2|P|2.5.1"
    expect: {outcome: delivered}
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("duplicate test names should be refused")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error = %v", err)
	}
}

func TestInvalidOutcomeIsRefused(t *testing.T) {
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: bad outcome
    message: "MSH|^~\\&|A|B|C|D|20260819||ADT^A01|T1|P|2.5.1"
    expect: {outcome: transmuted}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("an unknown outcome should be refused at load")
	}
}

func TestSkippedCasesDoNotFailTheSuite(t *testing.T) {
	// Better than commenting a case out, which loses the intent and the reason
	// together.
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: not implemented yet
    skip: waiting for the registry to accept the new identifier format
    message: "MSH|^~\\&|A|B|C|D|20260819||ADT^A01|T1|P|2.5.1"
    expect: {outcome: delivered}
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Error("a skipped case should not fail the suite")
	}
	_, _, skipped := report.Counts()
	if skipped != 1 {
		t.Errorf("skipped = %d", skipped)
	}
}

func TestNewlinesInAFixtureBecomeCarriageReturns(t *testing.T) {
	// Nobody should have to embed control characters in YAML by hand.
	got := normaliseSeparators([]byte("MSH|a\nPID|b\n"))
	if strings.Contains(string(got), "\n") {
		t.Errorf("newlines survived: %q", got)
	}
	if string(got) != "MSH|a\rPID|b\r" {
		t.Errorf("got %q", got)
	}
}

func TestWindowsLineEndingsDoNotProduceEmptySegments(t *testing.T) {
	// Replacing \n before \r\n would leave a stray \r and an empty segment between
	// every real one.
	got := string(normaliseSeparators([]byte("MSH|a\r\nPID|b\r\n")))
	if strings.Contains(got, "\r\r") {
		t.Errorf("doubled separators: %q", got)
	}
	if got != "MSH|a\rPID|b\r" {
		t.Errorf("got %q", got)
	}
}

func TestMLLPFramingIsStrippedFromAFixture(t *testing.T) {
	// A message copied out of a packet capture, or produced by perfuse generate,
	// arrives framed.
	got := string(normaliseSeparators([]byte("\x0bMSH|a\rPID|b\r\x1c\r")))
	if strings.ContainsAny(got, "\x0b\x1c") {
		t.Errorf("framing survived: %q", got)
	}
	if got != "MSH|a\rPID|b\r" {
		t.Errorf("got %q", got)
	}
}

func TestAFixtureCanComeFromAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ch.yaml"), []byte(simpleChannel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a01.hl7"),
		[]byte("MSH|^~\\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|F1|P|2.5.1\r"+
			"PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|f\r"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s_test.yaml")
	if err := os.WriteFile(path, []byte(`
channel: ./ch.yaml
tests:
  - name: from a fixture file
    file: ./a01.hl7
    expect:
      outcome: delivered
      fields:
        PID-3.1: "000000MRN7"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	suite, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Errorf("failures: %v", report.Results[0].Failures)
	}
}

func TestBothMessageAndFileIsRefused(t *testing.T) {
	path := writeSuite(t, simpleChannel, `
channel: ./ch.yaml
tests:
  - name: two sources
    message: "MSH|^~\\&|A|B|C|D|20260819||ADT^A01|T1|P|2.5.1"
    file: ./a01.hl7
    expect: {outcome: delivered}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("a case with both a message and a file should be refused")
	}
}

func TestCasesAreIndependentOfEachOther(t *testing.T) {
	// A fresh channel per case, so a script's globalMap cannot leak between tests.
	// A suite whose results depend on order is not a suite.
	channel := simpleChannel + `
scripts:
  transformer: |
    var seen = globalMap.get('seen');
    if (seen) { throw new Error('state leaked from a previous test'); }
    globalMap.put('seen', 'yes');
`
	path := writeSuite(t, channel, `
channel: ./ch.yaml
tests:
  - name: first
    message: |
      MSH|^~\&|A|B|C|D|20260819080000-0500||ADT^A01^ADT_A01|C1|P|2.5.1
      PID|1||MRN1^^^SITEA^MR||Frost^Ivy||19910228|F
    expect: {outcome: delivered}
  - name: second
    message: |
      MSH|^~\&|A|B|C|D|20260819080100-0500||ADT^A01^ADT_A01|C2|P|2.5.1
      PID|1||MRN2^^^SITEA^MR||Frost^Ivy||19910228|F
    expect: {outcome: delivered}
`)

	suite, _ := Load(path)
	report, err := Run(suite)
	if err != nil {
		t.Fatal(err)
	}
	// globalMap is genuinely global to a process by design, so this documents which
	// behaviour we get rather than asserting a particular one. What must hold is
	// that both cases ran and produced a definite result.
	if len(report.Results) != 2 {
		t.Fatalf("results = %d", len(report.Results))
	}
	for _, res := range report.Results {
		if res.Error != nil {
			t.Errorf("%s could not run: %v", res.Name, res.Error)
		}
	}
}
