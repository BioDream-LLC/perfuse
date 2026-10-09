package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func elrChannel(t *testing.T, dest string, withConfig bool) (*Channel, error) {
	t.Helper()
	dir := t.TempDir()
	if withConfig {
		raw, err := os.ReadFile("../../examples/elr/elr.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "elr.yaml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src := "name: lab-to-state\nsource:\n  type: mllp\n  listen: \":0\"\ndestinations:\n" + dest
	path := filepath.Join(dir, "lab-to-state.yaml")
	return Load(strings.NewReader(src), path)
}

// The ELR file is named from the channel file's directory and read when the channel loads.
func TestAnELRDestinationReadsItsFileBesideTheChannel(t *testing.T) {
	c, err := elrChannel(t, "  - name: state\n    type: mllp\n    address: elr.state.test:6661\n    elr: {config: elr.yaml}\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Destinations[0].ELR.Config; !filepath.IsAbs(got) || filepath.Base(got) != "elr.yaml" {
		t.Errorf("config path %q was not resolved against the channel file", got)
	}
}

func TestAnELRDestinationIsRefusedWhereItCouldNotWork(t *testing.T) {
	for name, tc := range map[string]struct {
		dest, want string
		withConfig bool
	}{
		"no config":    {"  - name: state\n    type: mllp\n    address: elr.state.test:6661\n    elr: {}\n", "elr.config is required", true},
		"missing file": {"  - name: state\n    type: mllp\n    address: elr.state.test:6661\n    elr: {config: elr.yaml}\n", "elr.config", false},
		"converting destination": {"  - name: state\n    type: fhir\n    fhir: {url: \"https://ph.test/fhir\"}\n    elr: {config: elr.yaml}\n",
			"delivers the HL7 v2 message itself", true},
	} {
		_, err := elrChannel(t, tc.dest, tc.withConfig)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// The ELR file beside its channel is that channel's companion, not a channel file that will not load; a stray file that is
// neither still stops the directory.
func TestADirectoryWithAnELRChannelLoads(t *testing.T) {
	cs, err := LoadDir("../../examples/elr")
	if err != nil {
		t.Fatalf("examples/elr: %v", err)
	}
	if len(cs) != 1 || cs[0].Name != "lab-to-state" {
		t.Fatalf("loaded %d channels, want lab-to-state alone", len(cs))
	}
	dir := t.TempDir()
	for _, f := range []string{"elr.yaml", "lab-to-state.yaml"} {
		raw, err := os.ReadFile(filepath.Join("../../examples/elr", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stray.yaml"), []byte("sending_facility: {id: x}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "stray.yaml") {
		t.Errorf("a stray file that is no channel's companion loaded: %v", err)
	}
}
