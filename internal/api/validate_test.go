package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
)

func validateYAML(t *testing.T, h *harness, role, yaml string) (int, validateResponse) {
	t.Helper()
	res := h.do(role, http.MethodPost, "/api/channels/validate", validatePayload{YAML: yaml})

	var out validateResponse
	if res.Body.Len() > 0 {
		_ = json.Unmarshal(res.Body.Bytes(), &out)
	}
	return res.Code, out
}

const goodChannel = `name: validate-me
source:
  type: mllp
  listen: 127.0.0.1:16801
destinations:
  - name: archive
    type: file
    dir: /tmp/validate-me
`

func TestAValidChannelValidates(t *testing.T) {
	h := newHarness(t)

	code, out := validateYAML(t, h, "editor", goodChannel)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !out.OK {
		t.Fatalf("not ok: %+v", out.Problems)
	}
	if out.Summary == nil {
		t.Fatal("no summary was returned")
	}
	if out.Summary.Name != "validate-me" {
		t.Errorf("name = %q", out.Summary.Name)
	}
	if !strings.Contains(out.Summary.Receives, "16801") {
		t.Errorf("receives = %q, expected the listen address", out.Summary.Receives)
	}
	if len(out.Summary.Sends) != 1 || !strings.Contains(out.Summary.Sends[0], "/tmp/validate-me") {
		t.Errorf("sends = %v", out.Summary.Sends)
	}
}

func TestValidatingDoesNotCreateAChannel(t *testing.T) {
	// The whole reason this endpoint exists. If validating created the channel, the
	// builder could not check its work without committing to it.
	h := newHarness(t)

	before := h.do("viewer", http.MethodGet, "/api/channels", nil)
	var listBefore struct {
		Channels []struct{ Name string } `json:"channels"`
	}
	if err := json.Unmarshal(before.Body.Bytes(), &listBefore); err != nil {
		t.Fatal(err)
	}

	if _, out := validateYAML(t, h, "editor", goodChannel); !out.OK {
		t.Fatalf("the fixture did not validate: %+v", out.Problems)
	}

	after := h.do("viewer", http.MethodGet, "/api/channels", nil)
	var listAfter struct {
		Channels []struct{ Name string } `json:"channels"`
	}
	if err := json.Unmarshal(after.Body.Bytes(), &listAfter); err != nil {
		t.Fatal(err)
	}

	if len(listAfter.Channels) != len(listBefore.Channels) {
		t.Fatalf("channel count went from %d to %d: validating wrote something",
			len(listBefore.Channels), len(listAfter.Channels))
	}
}

func TestAnUnknownKeyIsReportedNotIgnored(t *testing.T) {
	// The design invariant, checked through the endpoint the builder uses. A typo that
	// validates clean is how a channel silently does not do what its file says.
	h := newHarness(t)

	_, out := validateYAML(t, h, "editor", strings.Replace(goodChannel,
		"destinations:", "wibble: 3\ndestinations:", 1))
	if out.OK {
		t.Fatal("an unknown key validated clean")
	}
	joined := strings.ToLower(problemText(out))
	if !strings.Contains(joined, "wibble") {
		t.Errorf("the problem did not name the offending key: %q", joined)
	}
}

func TestAProblemCarriesItsLineWhereYAMLKnowsIt(t *testing.T) {
	// A marker in the gutter beats a red box under the form, and the line number is
	// buried in the message text until something pulls it out.
	h := newHarness(t)

	// An unterminated quote, which yaml cannot recover from and so reports with a line.
	// My first attempt at this used bad indentation, which yaml quietly folded into a
	// multi-line scalar and parsed without complaint - a reminder that YAML accepts a lot
	// more than it looks like it should.
	_, out := validateYAML(t, h, "editor", "name: \"unterminated\nsource:\n  type: mllp\n")
	if out.OK {
		t.Fatal("malformed YAML validated clean")
	}

	found := false
	for _, p := range out.Problems {
		if p.Line > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("no problem carried a line number: %+v", out.Problems)
	}
}

func TestAnEmptyDocumentIsNotAnError(t *testing.T) {
	// The builder asks about an empty form before anything is typed. A red banner at
	// that moment teaches people to ignore red banners.
	h := newHarness(t)

	code, out := validateYAML(t, h, "editor", "   \n")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out.OK {
		t.Error("an empty document reported as valid")
	}
	if len(out.Problems) != 1 {
		t.Fatalf("problems = %+v", out.Problems)
	}
	if out.Summary != nil {
		t.Error("an empty document produced a summary")
	}
}

func TestAViewerCannotValidate(t *testing.T) {
	// It compiles arbitrary filter expressions and scripts, so it is gated like creating
	// a channel rather than like reading one.
	h := newHarness(t)
	res := h.do("viewer", http.MethodPost, "/api/channels/validate", validatePayload{YAML: goodChannel})
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}

func TestTheSummarySaysWhatTheSenderIsPromised(t *testing.T) {
	// The most consequential setting in a channel, and the one most often wrong.
	h := newHarness(t)

	_, onDelivery := validateYAML(t, h, "editor", goodChannel)
	if !strings.Contains(onDelivery.Summary.Acknowledges, "every destination") {
		t.Errorf("default ack described as %q", onDelivery.Summary.Acknowledges)
	}

	receipt := strings.Replace(goodChannel,
		"  listen: 127.0.0.1:16801", "  listen: 127.0.0.1:16801\n  ack:\n    when: on_receipt", 1)
	_, out := validateYAML(t, h, "editor", receipt)
	if !out.OK {
		t.Fatalf("on_receipt did not validate: %+v", out.Problems)
	}
	if !strings.Contains(out.Summary.Acknowledges, "can still be lost") {
		t.Errorf("on_receipt described as %q, without the warning", out.Summary.Acknowledges)
	}
}

func TestAChannelWithNoDestinationsIsRefused(t *testing.T) {
	// I had written this expecting a warning, and the validator refuses it outright - a
	// channel that accepts messages and sends them nowhere is a mistake often enough that
	// it has to be asked for explicitly rather than mentioned in passing. Asserting the
	// real behaviour, and the warning branch that assumed otherwise is gone.
	h := newHarness(t)

	yaml := "name: nowhere\nsource:\n  type: mllp\n  listen: 127.0.0.1:16802\ndestinations: []\n"
	_, out := validateYAML(t, h, "editor", yaml)
	if out.OK {
		t.Fatal("a channel with no destinations validated clean")
	}
	if !strings.Contains(problemText(out), "at least one destination") {
		t.Errorf("problems = %+v", out.Problems)
	}
}

func TestTheSummaryNeverCarriesADSN(t *testing.T) {
	// A description that leaks a password into a screenshot is worse than a vague
	// description.
	h := newHarness(t)

	yaml := `name: db-in
source:
  type: database
  database:
    driver: postgres
    dsn: postgres://someone:sup3rsecret@db.example.org/records
    query: SELECT id, payload FROM inbound
    column: payload
    key_column: id
destinations:
  - name: archive
    type: file
    dir: /tmp/db-in
`
	_, out := validateYAML(t, h, "editor", yaml)
	if !out.OK {
		t.Fatalf("problems: %+v", out.Problems)
	}

	body, err := json.Marshal(out.Summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "sup3rsecret") {
		t.Fatal("the summary leaked the database password")
	}
	if !strings.Contains(out.Summary.Receives, "postgres") {
		t.Errorf("receives = %q, expected the driver named", out.Summary.Receives)
	}
}

func TestAScriptedChannelSaysSo(t *testing.T) {
	// A channel whose behaviour cannot be established by reading its configuration
	// should say that in the place somebody reads its configuration.
	h := newHarness(t)

	yaml := goodChannel + "scripts:\n  transformer: |\n    msg['PID']['PID.8']['PID.8.1'] = 'F';\n"
	_, out := validateYAML(t, h, "editor", yaml)
	if !out.OK {
		t.Fatalf("problems: %+v", out.Problems)
	}
	if !warns(out.Summary.Warnings, "JavaScript") {
		t.Errorf("warnings = %v", out.Summary.Warnings)
	}
}

func TestYamlLine(t *testing.T) {
	cases := map[string]int{
		"yaml: line 7: did not find expected key": 7,
		"  line 12: field wibble not found":       12,
		"no number here":                          0,
		"line ":                                   0,
		"line abc":                                0,
	}
	for in, want := range cases {
		if got := yamlLine(in); got != want {
			t.Errorf("yamlLine(%q) = %d, want %d", in, got, want)
		}
	}
}

func problemText(out validateResponse) string {
	parts := make([]string, 0, len(out.Problems))
	for _, p := range out.Problems {
		parts = append(parts, p.Message)
	}
	return strings.Join(parts, " | ")
}

func warns(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestTheSummaryNamesEmailRecipientsButNotBlindOnes(t *testing.T) {
	// Who receives a notification is what somebody reviewing a channel needs to check, so
	// To and Cc are named. Bcc recipients are counted instead: in this setting that list is
	// often who is being told about a patient, and a description that leaks it into a
	// screenshot is worse than a vague one.
	h := newHarness(t)

	yaml := `name: notify
source:
  type: mllp
  listen: 127.0.0.1:17301
destinations:
  - name: tell-the-lab
    type: smtp
    smtp:
      host: mail.example.org
      from: perfuse@example.org
      to: [lab@example.org]
      bcc: [secret-oversight@example.org]
      body: a message arrived
`
	_, out := validateYAML(t, h, "editor", yaml)
	if !out.OK {
		t.Fatalf("problems: %+v", out.Problems)
	}

	sends := strings.Join(out.Summary.Sends, "\n")
	if !strings.Contains(sends, "lab@example.org") {
		t.Errorf("the visible recipient was not named: %q", sends)
	}
	if strings.Contains(sends, "secret-oversight@example.org") {
		t.Errorf("a blind recipient was disclosed in the summary: %q", sends)
	}
	if !strings.Contains(sends, "1 other recipient") {
		t.Errorf("the hidden recipient was not accounted for at all: %q", sends)
	}
	if !strings.Contains(sends, "summary only") {
		t.Errorf("the summary did not say whether the message itself is sent: %q", sends)
	}
}

// TestAV3ChannelsTransformationsAppearInTheSummary covers a gap found by running perfuse check.
//
// The summary is built from the v2 transformation list, so a v3 channel's steps - which live under hl7v3 - were missing
// entirely. Two consequences, and the second is worse than the first: the interface showed nothing where three steps
// existed, and the "messages are passed through unchanged" warning fired on a channel that was masking a birth date.
func TestAV3ChannelsTransformationsAppearInTheSummary(t *testing.T) {
	acknowledge := true
	c := &config.Channel{
		Name:     "pdq",
		DataType: config.DataHL7v3,
		HL7v3: &config.HL7v3Options{
			Acknowledge:  &acknowledge,
			SenderDevice: "PERFUSE",
			SenderOID:    "2.16.840.1.113883.3.999",
			Transformations: []hl7v3.Step{
				{
					Description: "mask the birth date",
					NullFlavor:  &hl7v3.V3NullFlavorStep{Path: "//birthTime", Reason: "MSK"},
				},
			},
		},
		Source: config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
		},
		Destinations: []config.Destination{
			{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the channel is not valid: %v", err)
	}

	got := describeChannel(c)

	if len(got.Steps) != 1 {
		t.Fatalf("the summary lists %d steps, want 1 - a v3 channel's transformations are invisible in the "+
			"interface", len(got.Steps))
	}
	if !strings.Contains(got.Steps[0], "MSK") {
		t.Errorf("the step description does not name the reason: %q", got.Steps[0])
	}
	// The distinction between the three removals has to survive into the description, because "clears" and
	// "records that no value is available" read the same in a summary and are not the same statement.
	if !strings.Contains(got.Steps[0], "withheld") {
		t.Errorf("the description does not explain what MSK means: %q", got.Steps[0])
	}

	for _, w := range got.Warnings {
		if strings.Contains(w, "passed through unchanged") {
			t.Errorf("the summary claims messages pass through unchanged on a channel that masks a birth "+
				"date: %q", w)
		}
	}
}
