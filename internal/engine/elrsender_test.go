package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A lab result reaches a file destination with an elr block as an ELR 2.5.1 message holding only the reportable order, and a
// result with nothing reportable is not written at all.
func TestAnELRDestinationDeliversOnlyReportableResultsAsELR(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile("../../examples/elr/elr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "elr.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	src := "name: lab-to-state\nsource:\n  type: mllp\n  listen: \"127.0.0.1:0\"\ndestinations:\n" +
		"  - name: state\n    type: file\n    dir: " + out + "\n    elr: {config: elr.yaml}\n"
	cfg, err := config.Load(strings.NewReader(src), filepath.Join(dir, "lab-to-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()

	oru, err := os.ReadFile("../publichealth/testdata/oru-elr-source.hl7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.HandleForTest(context.Background(), oru); err != nil {
		t.Fatal(err)
	}
	cbcOnly := strings.Replace(strings.Replace(string(oru), "94500-6", "2345-7", -1), "260373001^Detected", "5.5", 1)
	cbcOnly = strings.Replace(cbcOnly, "LAB0043", "LAB0044", 1)
	if _, err := ch.HandleForTest(context.Background(), []byte(cbcOnly)); err != nil {
		t.Fatalf("a result with nothing reportable is not a failure: %v", err)
	}

	files, _ := os.ReadDir(out)
	if len(files) != 1 {
		t.Fatalf("%d files written, want 1 (the reportable result only)", len(files))
	}
	msg, _ := os.ReadFile(filepath.Join(out, files[0].Name()))
	s := string(msg)
	for _, want := range []string{"PHLabReport-NoAck^phLabResultsELRv251", "94500-6", "SFT|BioDream LLC"} {
		if !strings.Contains(s, want) {
			t.Errorf("the delivered message lacks %q", want)
		}
	}
	if strings.Contains(s, "6690-2") {
		t.Error("the CBC order, with nothing reportable, was sent to the state")
	}
}
