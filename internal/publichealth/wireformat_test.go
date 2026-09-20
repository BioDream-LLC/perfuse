package publichealth

import (
	"strings"
	"testing"
	"time"
)

// These two verify at the wire format, independently of the builders internals. An escape helper
// that exists but is never called looks correct in review and ships unescaped data, so the check
// has to read the bytes that would actually be sent.
func TestEscapingReachesTheWire(t *testing.T) {
	msg := ELRMessage{
		PatientID:        "P1",
		PatientName:      "Bad|Name^X~Y&Z",
		DOB:              "19800101",
		OrderingProvider: "Dr Who",
		PerformingLab:    "Lab A",
		CollectionDate:   time.Now(),
		Results: []LabResult{{
			Code: "1", CodeSystem: "LN", Display: "x", Value: "1", Status: "F",
		}},
	}
	out, err := BuildORU(msg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	if strings.Contains(s, "Bad|Name") {
		t.Errorf("UNESCAPED pipe reached the wire, every later field is shifted:\n%s", s)
	}
	if !strings.Contains(s, `\F\`) {
		t.Errorf("pipe was not escaped to \\F\\:\n%s", s)
	}
	if !strings.Contains(s, `\R\`) {
		t.Errorf("tilde was not escaped to \\R\\:\n%s", s)
	}
	if !strings.Contains(s, `\T\`) {
		t.Errorf("ampersand was not escaped to \\T\\:\n%s", s)
	}
	if strings.Contains(s, "\n") {
		t.Error("message contains a newline; HL7 v2 segments terminate with carriage return")
	}
}

// A refusal must never serialise as an administered dose.
func TestRefusalIsNotSerialisedAsADose(t *testing.T) {
	rec := ImmunizationRecord{
		PatientID:        "P1",
		AdminDate:        "20260101",
		VaccineCode:      "08",
		VaccineName:      "Hep B",
		CompletionStatus: "RE",
		RefusalReason:    "00",
		ActionCode:       "A",
	}
	out, err := BuildVXU(rec)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	var rxa string
	for _, seg := range strings.Split(s, "\r") {
		if strings.HasPrefix(seg, "RXA") {
			rxa = seg
		}
	}
	if rxa == "" {
		t.Fatalf("no RXA segment:\n%s", s)
	}
	f := strings.Split(rxa, "|")
	if len(f) < 22 {
		t.Fatalf("RXA has %d fields, need 22 to carry the action code: %s", len(f), rxa)
	}
	if f[20] != "RE" {
		t.Errorf("RXA-20 completion status = %q, want RE; a refusal is being reported as given", f[20])
	}
	if f[18] != "00" {
		t.Errorf("RXA-18 refusal reason = %q, want 00", f[18])
	}
	if f[21] != "A" {
		t.Errorf("RXA-21 action code = %q, want A", f[21])
	}
}
