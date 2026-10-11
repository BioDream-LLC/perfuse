package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The report is for a site to send back from its live feed, so nothing from a message may be in it.
func TestTheFeedReportHoldsNothingFromTheMessages(t *testing.T) {
	dir := t.TempDir()
	msg := "MSH|^~\\&|ZEPHYRLAB|QUILLFIELD|RCV|RCV|20261001120000||ORU^R01|CTRLXYZZY|P|2.5.1\r" +
		"PID|1||MRN884213^^^QUILLHOSP^MR||Wrenfeather^Ottoline||19610203|F\r" +
		"OBR|1||ACC5521|24331-1^Lipid panel^LN\r" +
		"OBX|1|NM|2093-3^Cholesterol^LN||231|mg/dL|<200|H|||F\r" +
		"OBX|2|ST|ZZLOCAL7^Local thing^99ZZ||sixty-one sparrows||||||F\r"
	in := filepath.Join(dir, "in.hl7")
	if err := os.WriteFile(in, []byte(msg), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report.md")
	_ = cmdFHIRConvert([]string{"-quiet", "-report", report, in}, io.Discard, io.Discard)
	body, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, secret := range []string{"ZEPHYRLAB", "QUILLFIELD", "CTRLXYZZY", "MRN884213", "QUILLHOSP", "Wrenfeather", "Ottoline",
		"19610203", "ACC5521", "2093-3", "231", "ZZLOCAL7", "99ZZ", "sixty-one", "sparrows"} {
		if strings.Contains(text, secret) {
			t.Errorf("the report carries %q from the message:\n%s", secret, text)
		}
	}
	for _, want := range []string{"1 message(s): 1 converted", "| ORU^R01 | 1 | 1 |", "Observation: 2", "| OBX-3 |"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
}
