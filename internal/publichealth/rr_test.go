package publichealth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testAgency = Agency{Name: "Illinois Department of Public Health", Phone: "+1-217-555-0199", Email: "ecr@idph.example",
	Line: "535 W Jefferson St", City: "Springfield", State: "IL", PostalCode: "62761", Endpoint: "https://ph.test/fhir"}

func caseReportMessage(t *testing.T) ([]byte, *EICR) {
	t.Helper()
	e := eicrFrom(t, "adt-covid.hl7")
	msg, err := ReportingBundle(e, ReportingOptions{Destination: "https://ph.test/fhir", Source: "https://ehr.test/fhir", Event: EventFor("A08", e.Triggers)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msg)
	return raw, e
}

// The IG's own example RR reads as the IG describes it. Its timeframe is "24 H", as the example writes it.
func TestTheIGsExampleRRParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "rr-ig-example.json"))
	if err != nil {
		t.Fatal(err)
	}
	rr, err := ParseRR(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Status != "RRVS20" || rr.EICR != "urn:uuid:db734647-fc99-424c-a864-7e3cda82e703" || rr.Priority != "RRVS17" {
		t.Errorf("status %q eicr %q priority %q", rr.Status, rr.EICR, rr.Priority)
	}
	if len(rr.Conditions) != 1 || rr.Conditions[0].Code != "3928002" || !rr.Reportable() {
		t.Fatalf("conditions %+v", rr.Conditions)
	}
	d := rr.Conditions[0].Determinations
	if len(d) != 1 || d[0].Determination != "RRVS1" || d[0].Location != "RRVS5" || d[0].Timeframe != "24 H" || d[0].Agency == "" {
		t.Errorf("determination %+v", d)
	}
}

// A test agency answers a case report with an RR that names the eICR it answers, goes back to the eICR's source, and reads
// back as reportable with the trigger's condition.
func TestTheTestAgencyAnswersACaseReport(t *testing.T) {
	raw, e := caseReportMessage(t)
	reply, rr, err := BuildRR(raw, nil, testAgency, time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PERFUSE_RR_OUT") != "" {
		b, _ := json.MarshalIndent(reply, "", "  ")
		_ = os.WriteFile(os.Getenv("PERFUSE_RR_OUT"), b, 0o644)
	}
	eicrID := e.Bundle["identifier"].(map[string]any)["value"].(string)
	header := reply["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	if header["destination"].([]any)[0].(map[string]any)["endpoint"] != "https://ehr.test/fhir" {
		t.Errorf("reply goes to %v", header["destination"])
	}
	back, _ := json.Marshal(reply)
	parsed, err := ParseRR(back)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.EICR != eicrID || rr.EICR != eicrID || parsed.Status != "RRVS19" || !parsed.Reportable() {
		t.Fatalf("parsed %+v", parsed)
	}
	if len(parsed.Conditions) == 0 || parsed.Conditions[0].Determinations[0].Agency != testAgency.Name || parsed.Summary == "" {
		t.Errorf("conditions %+v summary %q", parsed.Conditions, parsed.Summary)
	}
	if ev, _ := MessageEvent(back); ev != EventReportabilityResponse {
		t.Errorf("event %q", ev)
	}
	if ev, _ := MessageEvent(raw); ev != EventCaseReport {
		t.Errorf("case report event %q", ev)
	}
}

func TestTheTestAgencyRefusesWhatIsNotACaseReport(t *testing.T) {
	raw, _ := caseReportMessage(t)
	reply, _, _ := BuildRR(raw, nil, testAgency, time.Time{})
	back, _ := json.Marshal(reply)
	if _, _, err := BuildRR(back, nil, testAgency, time.Time{}); err == nil {
		t.Error("an RR was answered as if it were a case report")
	}
	if _, _, err := BuildRR(raw, nil, Agency{Name: "x"}, time.Time{}); err == nil {
		t.Error("an agency with no address or phone answered")
	}
	if _, err := ParseRR(raw); err == nil {
		t.Error("a case report was read as an RR")
	}
}
