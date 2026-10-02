package engine

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/delimited"
)

// loadDelimited builds a channel configuration from yaml, so that these tests exercise the same load path a real file takes.
// A hand-built config would skip the compilation step, which is where the filter and the steps are prepared and where a
// mistake would actually live.
func loadDelimited(t *testing.T, body string) *config.Channel {
	t.Helper()
	c, err := config.Load(strings.NewReader(body), "test.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	return c
}

const delimitedChannel = `
name: rows
dataType: delimited
delimited:
  delimiter: ","
  has_header: true
  filter: 'Ward == "ICU"'
  transformations:
    - set:
        path: Surname
        value: REDACTED
source:
  type: file
  file:
    root: /tmp
    dir: in
    raw: true
destinations:
  - name: out
    type: file
    dir: /tmp/out
`

// TestTheDelimitedFilterCompilesAgainstColumns is the load-time half.
//
// A filter that compiled but read nothing would pass this file and fail in production, so the assertion is that the expression
// answers correctly about a real record rather than that it is non-nil.
func TestTheDelimitedFilterCompilesAgainstColumns(t *testing.T) {
	c := loadDelimited(t, delimitedChannel)

	filter := c.Delimited.FilterExpr()
	if filter == nil {
		t.Fatal("the delimited filter did not compile, so every row would be delivered")
	}

	records, err := delimited.Parse([]byte("PatientID,Surname,Ward\nP001,Okonkwo,ICU\n"), delimited.Settings{HasHeader: true}.Resolved())
	if err != nil {
		t.Fatal(err)
	}

	match, err := filter.Eval(delimited.Message(records))
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !match {
		t.Error("the filter did not match an ICU row, so it compiled into something that reads nothing")
	}
}

func TestTheDelimitedStepsCompileAndApply(t *testing.T) {
	c := loadDelimited(t, delimitedChannel)

	steps := c.Delimited.Steps()
	if steps.Len() != 1 {
		t.Fatalf("got %d compiled step(s), want 1", steps.Len())
	}

	records, err := delimited.Parse([]byte("PatientID,Surname,Ward\nP001,Okonkwo,ICU\n"), delimited.Settings{HasHeader: true}.Resolved())
	if err != nil {
		t.Fatal(err)
	}

	out, changes, err := steps.Apply(delimited.Message(records))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("got %d change(s), want 1", len(changes))
	}
	if v, _ := delimited.ReadOne(out, delimited.Path{Column: "Surname"}); v != "REDACTED" {
		t.Errorf("Surname is %q after the step", v)
	}
}

// TestAnHL7FilterIsStillRefusedOnADelimitedChannel guards the reason the delimited pair exists at all.
//
// The channel-level filter compiles HL7 paths. If that refusal were ever relaxed, a delimited channel would compile
// "PID-8 == \"M\"" into an expression that finds no segments, match nothing, and drop every row - and the symptom would be a
// silent feed rather than an error.
func TestAnHL7FilterIsStillRefusedOnADelimitedChannel(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: rows
dataType: delimited
filter: 'PID-8 == "M"'
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: out
    type: file
    dir: /tmp/out
`), "test.yaml")
	if err == nil {
		t.Fatal("a channel-level HL7 filter was accepted on a delimited channel, so every row would be dropped silently")
	}
	if !strings.Contains(err.Error(), "delimited.filter") {
		t.Errorf("the refusal should name the setting to use instead, got: %v", err)
	}
}

// TestADelimitedChannelCanReadFromAFileSource is the regression for a defect that made the format unusable.
//
// filePoller.split finds message boundaries by looking for MSH, which is meaningless for a CSV, so a delimited channel needs
// raw on its file source - and the split function's own comment says exactly that, naming a CSV batch. A separate validator
// refused the combination, insisting dataType be raw as well.
//
// So both configurations failed: without raw the poller reported "no message could be found in it" on every file, and with raw
// the channel would not load. Delimited data arrives as a file drop more often than by any other route, and that route was
// closed.
func TestADelimitedChannelCanReadFromAFileSource(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: rows
dataType: delimited
delimited:
  delimiter: ","
  has_header: true
source:
  type: file
  file:
    root: /tmp
    dir: in
    raw: true
destinations:
  - name: out
    type: file
    dir: /tmp/out
`), "test.yaml")
	if err != nil {
		t.Fatalf("a delimited channel reading whole files was refused, which closes the commonest route for this format: %v", err)
	}
}

// TestAnHL7ChannelStillCannotSilentlyTakeWholeFiles is the other half, kept because relaxing the guard for every format would
// have removed a real protection.
//
// For v2 the guard is right: raw on the source means the file is delivered whole, so a batch of forty admissions arrives as one
// message, and only the first is parsed. That is a genuine misconfiguration and it stays refused.
func TestAnHL7ChannelStillCannotSilentlyTakeWholeFiles(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: adt
dataType: hl7
source:
  type: file
  file:
    root: /tmp
    dir: in
    raw: true
destinations:
  - name: out
    type: file
    dir: /tmp/out
`), "test.yaml")
	if err == nil {
		t.Fatal("an HL7 channel accepted raw file reading, so a batch of admissions would arrive as one message and only the first would be parsed")
	}
}
