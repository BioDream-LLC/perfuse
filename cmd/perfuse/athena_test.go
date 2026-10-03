package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAthenaPrintsAProjectedTableForTheArchiveLayout(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"athena", "-bucket", "hosp-archive", "-destination", "archive"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CREATE EXTERNAL TABLE IF NOT EXISTS perfuse.hl7_archive",
		"PARTITIONED BY (dt string)",
		"LOCATION 's3://hosp-archive/archive/'",
		"'storage.location.template' = 's3://hosp-archive/archive/dt=${dt}/'",
		"patient_id", "message ",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}

func TestAthenaRefusesNamesThatWouldBreakTheStatement(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"athena", "-bucket", "b'; DROP", "-destination", "a"}, &out, &errOut); err == nil {
		t.Error("a quote in the bucket name was written into SQL")
	}
	if err := run([]string{"athena", "-bucket", "b", "-destination", "a", "-table", "Bad-Name"}, &out, &errOut); err == nil {
		t.Error("an invalid table name was accepted")
	}
}
