package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validChannel = `
name: adt-inbound
description: Receives ADT from the hospital and forwards it to the registry.

source:
  type: mllp
  listen: ":6661"
  ack:
    when: on_delivery
    application: PERFUSE
    facility: MAIN

filter: MSH-9.1 == "ADT" and MSH-9.2 != "A28"

destinations:
  - name: registry
    type: mllp
    address: registry.example.invalid:6661
    timeout: 10s
    retry:
      attempts: 3
      backoff: 2s
      max_backoff: 30s

  - name: archive
    type: file
    dir: ./archive
    filter: MSH-9.2 in ["A01", "A04"]
`

func load(t *testing.T, body string) (*Channel, error) {
	t.Helper()
	return Load(strings.NewReader(body), "test.yaml")
}

func mustLoad(t *testing.T, body string) *Channel {
	t.Helper()
	c, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestLoadValidChannel(t *testing.T) {
	c := mustLoad(t, validChannel)

	if c.Name != "adt-inbound" {
		t.Errorf("Name = %q", c.Name)
	}
	if !c.IsEnabled() {
		t.Error("a channel with no enabled field should be enabled")
	}
	if c.Source.Type != SourceMLLP {
		t.Errorf("source.type = %q", c.Source.Type)
	}
	if c.Source.Listen != ":6661" {
		t.Errorf("source.listen = %q", c.Source.Listen)
	}
	if c.Source.AckWhen() != AckOnDelivery {
		t.Errorf("ack.when = %q", c.Source.AckWhen())
	}
	if len(c.Destinations) != 2 {
		t.Fatalf("got %d destinations, want 2", len(c.Destinations))
	}

	reg := c.Destinations[0]
	if reg.Type != DestinationMLLP || reg.Address != "registry.example.invalid:6661" {
		t.Errorf("destination 0 = %+v", reg)
	}
	if reg.Timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", reg.Timeout)
	}
	if reg.Retry.Attempts != 3 || reg.Retry.Backoff != 2*time.Second {
		t.Errorf("retry = %+v", reg.Retry)
	}
}

func TestFilterIsCompiledAtLoad(t *testing.T) {
	c := mustLoad(t, validChannel)

	// A filter must be compiled when the file loads, not when the first message
	// arrives. Finding out at 2am that a filter does not parse is not a plan.
	if c.FilterExpr() == nil {
		t.Fatal("channel filter was not compiled")
	}
	if c.Destinations[1].FilterExpr() == nil {
		t.Fatal("destination filter was not compiled")
	}

	// And the channel can state its own data dependencies.
	got := strings.Join(c.Paths(), " ")
	if want := "MSH-9.1 MSH-9.2"; got != want {
		t.Errorf("Paths() = %q, want %q", got, want)
	}
}

func TestBadFilterFailsAtLoad(t *testing.T) {
	_, err := load(t, `
name: broken
source:
  type: mllp
  listen: ":6661"
filter: MSH-9.1 ===== "ADT"
destinations:
  - name: out
    type: file
    dir: ./out
`)
	if err == nil {
		t.Fatal("a channel with an unparseable filter loaded successfully")
	}
	if !strings.Contains(err.Error(), "filter") {
		t.Errorf("error does not mention the filter: %v", err)
	}
}

func TestUnknownFieldIsAnError(t *testing.T) {
	// A misspelled key that is silently ignored produces a channel that looks
	// configured and is not. For a filter or a retry policy that is the worst
	// possible outcome.
	_, err := load(t, `
name: typo
source:
  type: mllp
  listen: ":6661"
  idel_timeout: 30s
destinations:
  - name: out
    type: file
    dir: ./out
`)
	if err == nil {
		t.Fatal("a misspelled field was accepted")
	}
	if !strings.Contains(err.Error(), "idel_timeout") {
		t.Errorf("error does not name the offending field: %v", err)
	}
}

func TestMissingRequiredFields(t *testing.T) {
	cases := map[string]string{
		"no name": `
source:
  type: mllp
  listen: ":6661"
destinations:
  - name: out
    type: file
    dir: ./out
`,
		"no source type": `
name: x
source:
  listen: ":6661"
destinations:
  - name: out
    type: file
    dir: ./out
`,
		"no listen": `
name: x
source:
  type: mllp
destinations:
  - name: out
    type: file
    dir: ./out
`,
		"no destinations": `
name: x
source:
  type: mllp
  listen: ":6661"
`,
		"destination without a name": `
name: x
source:
  type: mllp
  listen: ":6661"
destinations:
  - type: file
    dir: ./out
`,
		"destination without a type": `
name: x
source:
  type: mllp
  listen: ":6661"
destinations:
  - name: out
    dir: ./out
`,
		"file destination without dir": `
name: x
source:
  type: mllp
  listen: ":6661"
destinations:
  - name: out
    type: file
`,
		"mllp destination without address": `
name: x
source:
  type: mllp
  listen: ":6661"
destinations:
  - name: out
    type: mllp
`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if c, err := load(t, body); err == nil {
				t.Errorf("loaded successfully: %+v", c)
			}
		})
	}
}

func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	// Someone fixing a file should see everything wrong with it, not one problem
	// per run.
	_, err := load(t, `
name: ""
source:
  type: carrier-pigeon
  listen: "not-an-address"
destinations: []
`)
	if err == nil {
		t.Fatal("expected an error")
	}

	msg := err.Error()
	for _, want := range []string{"name is required", "carrier-pigeon", "destination"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
	if n := strings.Count(msg, "\n  - "); n < 3 {
		t.Errorf("only %d problems reported, want at least 3:\n%s", n, msg)
	}
}

func TestWrongFieldForDestinationType(t *testing.T) {
	// A file destination with an address, or an mllp destination with a
	// directory, means somebody changed the type and forgot to finish.
	for name, body := range map[string]string{
		"file with address": `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: out
    type: file
    dir: ./out
    address: somewhere:1234
`,
		"mllp with dir": `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: out
    type: mllp
    address: somewhere:1234
    dir: ./out
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, body); err == nil {
				t.Error("loaded successfully; the field does not apply to this type")
			}
		})
	}
}

func TestDuplicateDestinationNames(t *testing.T) {
	_, err := load(t, `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: out
    type: file
    dir: ./a
  - name: out
    type: file
    dir: ./b
`)
	if err == nil {
		t.Fatal("duplicate destination names were accepted")
	}
}

func TestEnabledFlags(t *testing.T) {
	c := mustLoad(t, `
name: x
enabled: false
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: file
    dir: ./a
  - name: b
    type: file
    dir: ./b
    enabled: false
`)

	if c.IsEnabled() {
		t.Error("channel should be disabled")
	}
	if got := len(c.EnabledDestinations()); got != 1 {
		t.Errorf("got %d enabled destinations, want 1", got)
	}

	// A disabled channel is still validated, so a file that is temporarily off
	// is still checked by CI.
	if c.Source.Type != SourceMLLP {
		t.Error("a disabled channel should still be parsed")
	}
}

func TestEnabledChannelWithNoEnabledDestinations(t *testing.T) {
	// This would start, acknowledge every message and discard the lot.
	_, err := load(t, `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: file
    dir: ./a
    enabled: false
`)
	if err == nil {
		t.Fatal("an enabled channel with no enabled destination was accepted")
	}
}

func TestInvalidAckWhen(t *testing.T) {
	_, err := load(t, `
name: x
source:
  type: mllp
  listen: ":6661"
  ack:
    when: eventually
destinations:
  - name: a
    type: file
    dir: ./a
`)
	if err == nil {
		t.Fatal("an invalid ack.when was accepted")
	}
}

func TestAckWhenDefaultsToOnDelivery(t *testing.T) {
	// Promising delivery and then losing the message is worse than being slow,
	// so the safe option is the default.
	c := mustLoad(t, `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: file
    dir: ./a
`)
	if got := c.Source.AckWhen(); got != AckOnDelivery {
		t.Errorf("AckWhen() = %q, want %q", got, AckOnDelivery)
	}
}

func TestResolvedDefaults(t *testing.T) {
	c := mustLoad(t, `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: file
    dir: ./a
`)
	d := c.Destinations[0].Resolved()

	if d.Timeout != DefaultDestTimeout {
		t.Errorf("Timeout = %v, want %v", d.Timeout, DefaultDestTimeout)
	}
	if d.Retry.Attempts != DefaultRetryAttempts {
		t.Errorf("Attempts = %d, want %d", d.Retry.Attempts, DefaultRetryAttempts)
	}
	if d.Retry.Backoff != DefaultRetryBackoff {
		t.Errorf("Backoff = %v, want %v", d.Retry.Backoff, DefaultRetryBackoff)
	}
	if d.Retry.MaxBackoff != DefaultRetryMaxBackoff {
		t.Errorf("MaxBackoff = %v, want %v", d.Retry.MaxBackoff, DefaultRetryMaxBackoff)
	}
}

func TestBackoffLargerThanMax(t *testing.T) {
	_, err := load(t, `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: file
    dir: ./a
    retry:
      backoff: 1m
      max_backoff: 10s
`)
	if err == nil {
		t.Fatal("a backoff larger than max_backoff was accepted")
	}
}

func TestAddressValidation(t *testing.T) {
	bad := []string{"nohost", "host:notaport", ":99999", "host:", "]bad[:1234"}
	for _, addr := range bad {
		body := `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: mllp
    address: "` + addr + `"
`
		if _, err := load(t, body); err == nil {
			t.Errorf("address %q was accepted", addr)
		}
	}

	good := []string{"host:6661", "hospital.example.com:6661", "10.0.0.5:6661", "[::1]:6661"}
	for _, addr := range good {
		body := `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: mllp
    address: "` + addr + `"
`
		if _, err := load(t, body); err != nil {
			t.Errorf("address %q was rejected: %v", addr, err)
		}
	}
}

func TestHostnamesAreNotResolvedAtLoad(t *testing.T) {
	// A channel file must not fail to load because DNS is briefly down. That is
	// not the same thing as the file being wrong.
	if _, err := load(t, `
name: x
source: {type: mllp, listen: ":6661"}
destinations:
  - name: a
    type: mllp
    address: this-host-does-not-exist.invalid:6661
`); err != nil {
		t.Errorf("an unresolvable but well-formed hostname was rejected: %v", err)
	}
}

func TestMultipleDocumentsRejected(t *testing.T) {
	_, err := load(t, validChannel+"\n---\n"+validChannel)
	if err == nil {
		t.Fatal("a file with two documents was accepted")
	}
	if !strings.Contains(err.Error(), "one channel per file") {
		t.Errorf("error is unclear: %v", err)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()

	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("a.yaml", strings.Replace(validChannel, "adt-inbound", "channel-a", 1))
	write("nested/b.yml", strings.Replace(
		strings.Replace(validChannel, "adt-inbound", "channel-b", 1), ":6661\"", ":6662\"", 1))
	write("notes.txt", "ignored")

	channels, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("got %d channels, want 2", len(channels))
	}
	if channels[0].Name != "channel-a" || channels[1].Name != "channel-b" {
		t.Errorf("names = %q, %q", channels[0].Name, channels[1].Name)
	}
	if channels[0].Path() == "" {
		t.Error("Path() is empty; errors need to name the file")
	}
}

func TestLoadDirRejectsDuplicateChannelNames(t *testing.T) {
	// Two channels with the same name make an incident impossible to diagnose,
	// because logs and metrics cannot tell them apart.
	dir := t.TempDir()
	for _, name := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(validChannel), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := LoadDir(dir); err == nil {
		t.Fatal("duplicate channel names across files were accepted")
	}
}

func TestLoadDirEmpty(t *testing.T) {
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Error("an empty directory was accepted")
	}
}

func TestErrorNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("name: x\nsource:\n  type: mllp\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("error does not name the file: %v", err)
	}
}
