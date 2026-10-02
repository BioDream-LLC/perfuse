package config

import (
	"strings"
	"testing"
)

func loadYAML(t *testing.T, body string) (*Channel, error) {
	t.Helper()
	return Load(strings.NewReader(body), "test.yaml")
}

const x12Base = `
name: claims-in
dataType: x12
source:
  type: http
  http:
    listen: 127.0.0.1:14501
    path: /claims
destinations:
  - name: archive
    type: file
    dir: /tmp/claims
`

func TestDataTypeDefaultsToHL7(t *testing.T) {
	// Every channel written before this existed is HL7. Making the field mandatory
	// would break all of them on upgrade.
	ch, err := loadYAML(t, `
name: adt-in
source:
  type: http
  http:
    listen: 127.0.0.1:14502
    path: /hl7
destinations:
  - name: archive
    type: file
    dir: /tmp/out
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := ch.Type(); got != DataHL7 {
		t.Errorf("Type() = %q, want hl7", got)
	}
}

func TestAnX12ChannelLoads(t *testing.T) {
	ch, err := loadYAML(t, x12Base)
	if err != nil {
		t.Fatal(err)
	}
	if got := ch.Type(); got != DataX12 {
		t.Errorf("Type() = %q, want x12", got)
	}
	// Strict by default: the counts exist to catch a truncated file.
	if got := ch.X12.Policy(); got != EnvelopeRequire {
		t.Errorf("Policy() = %q, want require", got)
	}
	if ch.X12.ShouldSplit() {
		t.Error("Split defaulted to on")
	}
}

func TestAnUnknownDataTypeIsRefused(t *testing.T) {
	_, err := loadYAML(t, strings.Replace(x12Base, "dataType: x12", "dataType: edifact", 1))
	if err == nil {
		t.Fatal("an unsupported data type was accepted")
	}
	// The message has to list what is available, or the reader has to go and find the
	// source to learn what to type.
	if !strings.Contains(err.Error(), "hl7") || !strings.Contains(err.Error(), "x12") {
		t.Errorf("the error should list the supported types, got: %v", err)
	}
}

func TestAnUnknownEnvelopePolicyIsRefused(t *testing.T) {
	_, err := loadYAML(t, x12Base+`x12:
  envelope: maybe
`)
	if err == nil {
		t.Fatal("an unsupported envelope policy was accepted")
	}
	if !strings.Contains(err.Error(), "require") {
		t.Errorf("the error should list the policies, got: %v", err)
	}
}

func TestTheEnvelopePolicyCanBeSet(t *testing.T) {
	for _, policy := range []EnvelopePolicy{EnvelopeRequire, EnvelopeWarn, EnvelopeIgnore} {
		ch, err := loadYAML(t, x12Base+"x12:\n  envelope: "+string(policy)+"\n")
		if err != nil {
			t.Errorf("policy %q: %v", policy, err)
			continue
		}
		if got := ch.X12.Policy(); got != policy {
			t.Errorf("Policy() = %q, want %q", got, policy)
		}
	}
}

func TestSplitCanBeTurnedOn(t *testing.T) {
	ch, err := loadYAML(t, x12Base+"x12:\n  split: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !ch.X12.ShouldSplit() {
		t.Error("split was not read")
	}
}

func TestX12OptionsOnAnHL7ChannelAreRefused(t *testing.T) {
	// Not ignored. A block that has no effect is a reader believing something that is
	// not happening.
	_, err := loadYAML(t, strings.Replace(x12Base, "dataType: x12", "dataType: hl7", 1)+"x12:\n  split: true\n")
	if err == nil {
		t.Fatal("an x12 block on an HL7 channel was accepted")
	}
	if !strings.Contains(err.Error(), "dataType") {
		t.Errorf("the error should point at the mismatch, got: %v", err)
	}
}

func TestAnX12ChannelCompilesItsFilter(t *testing.T) {
	// Filters work on X12 now, because the grammar in internal/expr is generic over the format and X12 supplies only how
	// a path becomes a value. Compiled at load, so a bad expression refuses the channel.
	c, err := loadYAML(t, x12Base+"filter: ISA13 exists and CLM01 != \"\"\n")
	if err != nil {
		t.Fatalf("a valid X12 filter was refused: %v", err)
	}
	if c.X12Filter() == nil {
		t.Fatal("the filter was accepted but not compiled")
	}
	// And the HL7 one must be left alone, or an X12 channel would carry a filter compiled against the wrong paths.
	if c.FilterExpr() != nil {
		t.Fatal("an X12 channel should not also compile an HL7 filter")
	}
}

func TestAnX12FilterThatDoesNotParseIsRefusedAtLoad(t *testing.T) {
	_, err := loadYAML(t, x12Base+"filter: ISA13 ===== 1\n")
	if err == nil {
		t.Fatal("a filter that does not parse was accepted")
	}
	if !strings.Contains(err.Error(), "filter") {
		t.Errorf("the error should name the key, got: %v", err)
	}
}

func TestTopLevelTransformationsOnAnX12ChannelAreRedirectedRatherThanJustRefused(t *testing.T) {
	// X12 can transform now, under x12.transformations, where the paths are read as X12. The top-level steps still
	// cannot apply, because they address HL7 fields - so the refusal names the block to move them to. An error that
	// only says no leaves somebody believing the feature is absent when it is one key away.
	_, err := loadYAML(t, x12Base+`transformations:
  - set:
      path: PID-8
      value: "F"
`)
	if err == nil {
		t.Fatal("top-level HL7 transformations on an X12 channel were accepted")
	}
	if !strings.Contains(err.Error(), "x12.transformations") {
		t.Errorf("the error should name where the steps belong: %v", err)
	}
}

func TestX12TransformationsCompileAtLoad(t *testing.T) {
	c, err := loadYAML(t, x12Base+`x12:
  transformations:
    - description: anonymise the patient control number
      set:
          path: CLM01
          value: REDACTED
`)
	if err != nil {
		t.Fatalf("valid X12 transformations were refused: %v", err)
	}
	// Compiled at load, not on first message. A channel with a bad step must refuse to start while the operator who
	// deployed it is still watching.
	if c.X12.Steps().Len() != 1 {
		t.Fatalf("the steps were not compiled: %d", c.X12.Steps().Len())
	}
}

func TestABadX12TransformationRefusesTheChannelAtLoad(t *testing.T) {
	_, err := loadYAML(t, x12Base+`x12:
  transformations:
    - set:
          path: "not a path!"
          value: X
`)
	if err == nil {
		t.Fatal("a bad X12 path was accepted")
	}
	if !strings.Contains(err.Error(), "x12.transformations") {
		t.Errorf("the error should name the block: %v", err)
	}
}

// TestATransformerScriptOnAnX12ChannelIsAccepted is the second replacement of this test in one session.
//
// It first asserted that every script on an X12 channel was refused. Then that a transformer specifically was, while a
// preprocessor was allowed - because the refusals had been all-or-nothing when a preprocessor needs no parsed message.
//
// Now the transformer runs too. It addresses the interchange by path, through the same steps.Accessor the declarative
// transformations use, so CLM01 means the same thing in a script as in a step. The reasoning that kept it out was that a filter
// or transformer needs the message as a tree; that turned out to be the wrong requirement rather than a hard one - a tree would
// have meant inventing an XML shape for a format with its own vocabulary, and losing the invariants x12.Set enforces.
//
// Both earlier versions were correct when written, and each failed the moment the limit it recorded was lifted. That is the
// guardrail working: the limit was a decision to change rather than something to discover afterwards.
func TestATransformerScriptOnAnX12ChannelIsAccepted(t *testing.T) {
	if _, err := loadYAML(t, x12Base+`scripts:
  language: lua
  transformer: |
    msg.set("REF02", "SCRIPTED")
`); err != nil {
		t.Fatalf("a transformer script on an X12 channel was refused: %v", err)
	}
}

// TestAPreprocessorOnAnX12ChannelIsAccepted is the other half, and the reason the coarse refusal was wrong.
func TestAPreprocessorOnAnX12ChannelIsAccepted(t *testing.T) {
	if _, err := loadYAML(t, x12Base+`scripts:
  preprocessor: |
    return message;
`); err != nil {
		t.Fatalf("a preprocessor on an X12 channel was refused, so an interchange needing repair before parsing cannot be repaired: %v", err)
	}
}

// TestAShadowOnAnX12ChannelIsAccepted replaces a test that asserted the opposite.
//
// It was correct when written: there was no X12-aware comparison, so a shadow would have compared an interchange with the HL7
// walker. shadow.DiffX12 now compares by segment and element using the same CLM01 paths the filter uses, and handleX12 observes
// the shadow.
//
// Kept as a test rather than deleted, because the interesting property is no longer the refusal - it is that this is *not*
// refused while dicom, delimited, ncpdp, script and raw still are. Those five accepted a shadow that never ran, which is what
// removing this refusal turned out to expose.
func TestAShadowOnAnX12ChannelIsAccepted(t *testing.T) {
	if _, err := loadYAML(t, x12Base+`shadow:
  channel: claims-candidate
`); err != nil {
		t.Fatalf("a shadow on an X12 channel was refused: %v", err)
	}
}

func TestAnMLLPSourceCannotCarryX12(t *testing.T) {
	// MLLP framing exists to carry HL7, and this codebase answers every MLLP message
	// with an HL7 acknowledgement. An X12 sender would receive an MSA segment it cannot
	// read and would most likely retry for ever.
	_, err := loadYAML(t, `
name: claims-in
dataType: x12
source:
  type: mllp
  listen: 127.0.0.1:14503
destinations:
  - name: archive
    type: file
    dir: /tmp/claims
`)
	if err == nil {
		t.Fatal("an MLLP X12 channel was accepted")
	}
	if !strings.Contains(err.Error(), "acknowledgement") {
		t.Errorf("the error should explain the acknowledgement problem, got: %v", err)
	}
}

func TestHL7OnlyDestinationsAreRefusedForX12(t *testing.T) {
	cases := []struct {
		dest string
		want string
	}{
		{"    type: fhir\n    fhir:\n      url: http://127.0.0.1:9/fhir\n", "cannot take X12"},
		{"    type: mllp\n    address: 127.0.0.1:9\n", "acknowledgement"},
	}
	for _, c := range cases {
		body := `
name: claims-in
dataType: x12
source:
  type: http
  http:
    listen: 127.0.0.1:14504
    path: /claims
destinations:
  - name: onward
` + c.dest
		_, err := loadYAML(t, body)
		if err == nil {
			t.Errorf("destination %q was accepted for X12", c.dest)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("destination %q: unexpected error: %v", c.dest, err)
		}
	}
}

func TestFileAndHTTPAndSFTPDestinationsAreFineForX12(t *testing.T) {
	// The ones a claims interface actually uses: archive it, post it to a clearinghouse,
	// drop it on an SFTP server.
	ch, err := loadYAML(t, `
name: claims-in
dataType: x12
source:
  type: http
  http:
    listen: 127.0.0.1:14505
    path: /claims
destinations:
  - name: archive
    type: file
    dir: /tmp/claims
  - name: clearinghouse
    type: http
    http:
      url: http://127.0.0.1:14506/claims
`)
	if err != nil {
		t.Fatalf("a reasonable X12 channel was refused: %v", err)
	}
	if len(ch.Destinations) != 2 {
		t.Errorf("destinations = %d, want 2", len(ch.Destinations))
	}
}

func TestOneMistakeProducesOneError(t *testing.T) {
	// One fault should report once. This used to be about an X12 filter reporting both unsupported and a compile
	// failure; X12 filters compile now, so the case that remains is a filter that genuinely does not parse.
	_, err := loadYAML(t, x12Base+"filter: ISA13 ===== 1\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := strings.Count(err.Error(), "\n  - "); n > 1 {
		t.Errorf("one mistake produced %d separate errors:\n%v", n, err)
	}
}

func TestAnHL7ChannelStillCompilesItsFilter(t *testing.T) {
	// The guard that skips compilation for X12 must not skip it for HL7, or every
	// filter in existence stops being validated at load time.
	_, err := loadYAML(t, `
name: adt-in
filter: "PID-3 exists and"
source:
  type: http
  http:
    listen: 127.0.0.1:14507
    path: /hl7
destinations:
  - name: archive
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a broken HL7 filter was accepted")
	}
	if !strings.Contains(err.Error(), "filter") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestEveryDataTypeEitherRunsScriptsOrRefusesThem has moved and become finer-grained.
//
// The successor is TestEveryDataTypeEitherRunsAScriptSlotOrRefusesIt in scriptslots_test.go, which asks the same question per
// script slot rather than per data type.
//
// # Why the coarse version had to go
//
// It was right about the danger and wrong about the granularity, and the wrongness mattered. It could only ask "does this format
// run scripts", so the honest answer for X12 was no and the refusal covered every slot. But a preprocessor needs no parsed
// message at all - it reads text and returns text, before parsing, which is the entire reason sites need one. So an interchange
// from a partner who prefixes a byte order mark could not be repaired, and the refusal that prevented it was itself carrying a
// comment admitting a preprocessor would be meaningful.
//
// The coarse test then locked that in: relaxing the refusal made this test fail, so the refusal looked load-bearing.
//
// This note is kept rather than deleted because the original recorded a proven silent failure - a raw channel whose only script
// was a bare throw accepted a message, delivered it, wrote the file, and never ran the script, while perfuse check called it
// valid. That evidence is why the successor exists.
