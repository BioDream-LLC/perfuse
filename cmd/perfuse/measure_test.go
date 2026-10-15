package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdMeasure(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data.ndjson")
	_ = os.WriteFile(data, []byte(`{"resourceType":"Patient","id":"m1","gender":"female","birthDate":"1980-01-01"}
{"resourceType":"Encounter","id":"e1","status":"finished","subject":{"reference":"Patient/m1"},"period":{"start":"2026-02-01T09:00:00Z"}}
`), 0o600)
	var out, errb bytes.Buffer
	if err := cmdMeasure([]string{"-examples"}, &out, &errb); err != nil || !strings.Contains(out.String(), "example:example-bp-control") {
		t.Fatalf("%v %s", err, out.String())
	}
	out.Reset()
	err := cmdMeasure([]string{"-measure", "example:example-seen-in-period", "-period-start", "2026-01-01", "-period-end", "2026-12-31",
		"-type", "subject-list", data}, &out, &errb)
	if err != nil || !strings.Contains(out.String(), `"MeasureReport"`) || !strings.Contains(out.String(), "Patient/m1") {
		t.Fatalf("%v %s", err, out.String())
	}
	if err := cmdMeasure([]string{"-measure", "example:nope", "-period-start", "2026-01-01", "-period-end", "2026-12-31", data}, &out, &errb); err == nil {
		t.Error("an unknown example")
	}
	if err := cmdMeasure([]string{"-measure", "example:example-seen-in-period", "-period-start", "2026-01-01", "-period-end", "2026-12-31",
		"-subject", "Patient/zz", data}, &out, &errb); err == nil {
		t.Error("an unknown patient")
	}
}
