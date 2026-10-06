package publichealth

import (
	"strings"
	"testing"
)

// These two verify at the wire format, independently of the builders internals. An escape helper
// that exists but is never called looks correct in review and ships unescaped data, so the check
// has to read the bytes that would actually be sent.
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
