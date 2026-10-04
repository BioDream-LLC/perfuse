package publichealth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

// The fixtures were checked with the HL7 validator 6.10.4 against hl7.fhir.us.ecr#2.1.2, with tx.fhir.org for terminology: 0
// errors for each eICR and for the eCR message wrapping one. These tests hold the shape that made them valid.

var testFacility = Facility{Name: "Springfield General Hospital", NPI: "1234567893", Phone: "+1-217-555-0100", Line: "100 Main St",
	City: "Springfield", State: "IL", PostalCode: "62701"}

func eicrFrom(t *testing.T, fixture string) *EICR {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	m, err := hl7.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	e, err := FromV2(m, nil, v2fhir.Options{}, EICROptions{Now: time.Date(2026, 10, 4, 15, 0, 0, 0, time.UTC), Facility: testFacility})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func entries(b map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, e := range b["entry"].([]any) {
		em := e.(map[string]any)
		out[em["fullUrl"].(string)] = em["resource"].(map[string]any)
	}
	return out
}

func TestADiagnosisTriggersAnEICRThatResolvesWithinItself(t *testing.T) {
	e := eicrFrom(t, "adt-covid.hl7")
	if len(e.Triggers) != 1 || e.Triggers[0].Code != "840539006" || !strings.HasPrefix(e.Triggers[0].Resource, "Condition/") {
		t.Fatalf("triggers %+v", e.Triggers)
	}
	b := e.Bundle
	if b["type"] != "document" || b["timestamp"] == nil || b["identifier"] == nil {
		t.Errorf("bundle header: %v %v %v", b["type"], b["timestamp"], b["identifier"])
	}
	all := entries(b)
	// Every reference in the document points at an entry in it, and every fullUrl is a real UUID.
	raw, _ := json.Marshal(b)
	for _, part := range strings.Split(string(raw), `"reference":"`)[1:] {
		ref := part[:strings.IndexByte(part, '"')]
		if _, ok := all[ref]; !ok {
			t.Errorf("reference %s does not resolve in the document", ref)
		}
	}
	for u := range all {
		if !strings.HasPrefix(u, "urn:uuid:") || len(u) != len("urn:uuid:")+36 {
			t.Errorf("fullUrl %s", u)
		}
	}
	first := b["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	if first["resourceType"] != "Composition" {
		t.Fatalf("first entry is %v", first["resourceType"])
	}
	var codes []string
	for _, s := range first["section"].([]any) {
		codes = append(codes, s.(map[string]any)["code"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"].(string))
	}
	// The seven sections eicr-composition requires.
	for _, want := range []string{"29299-5", "10154-3", "10164-2", "11450-4", "29549-3", "30954-2", "29762-2"} {
		if !strings.Contains(strings.Join(codes, " "), want) {
			t.Errorf("no section %s in %v", want, codes)
		}
	}
	if !strings.Contains(string(raw), "Fever and cough") {
		t.Error("PV2-3's admit reason is not the reason for visit")
	}
	// The trigger flag sits on the Encounter's diagnosis for the COVID-19 Condition, and carries no display: the sender's
	// text is not LOINC's or SNOMED's, and the validator rejects it as one.
	for _, r := range all {
		if r["resourceType"] != "Encounter" {
			continue
		}
		d, _ := json.Marshal(r["diagnosis"])
		if !strings.Contains(string(d), "eicr-trigger-code-flag-extension") || !strings.Contains(string(d), `"code":"840539006","system":"http://snomed.info/sct"}`) {
			t.Errorf("diagnosis %s", d)
		}
		ind, _ := json.Marshal(r["participant"])
		if !strings.Contains(string(ind), "urn:uuid:") {
			t.Errorf("participant %s", ind)
		}
	}
}

func TestAResultTriggersAnEICRFlaggedOnTheResultsEntry(t *testing.T) {
	e := eicrFrom(t, "oru-sarscov2.hl7")
	if len(e.Triggers) != 1 || e.Triggers[0].Code != "94500-6" {
		t.Fatalf("triggers %+v", e.Triggers)
	}
	raw, _ := json.Marshal(e.Bundle["entry"].([]any)[0])
	if !strings.Contains(string(raw), `"triggerCode","valueCoding":{"code":"94500-6","system":"http://loinc.org"}`) {
		t.Errorf("results entry not flagged: %s", raw)
	}
}

func TestPractitionersBecomeRolesWithTheirNPI(t *testing.T) {
	e := eicrFrom(t, "adt-covid.hl7")
	for _, r := range entries(e.Bundle) {
		if r["resourceType"] == "PractitionerRole" {
			ids, _ := json.Marshal(r["identifier"])
			if !strings.Contains(string(ids), "http://hl7.org/fhir/sid/us-npi") {
				t.Errorf("role identifier %s: XCN.9 NPI&2.16.840.1.113883.4.6&ISO is the NPI", ids)
			}
			return
		}
	}
	t.Error("no PractitionerRole")
}

func TestNoVisitNumberStillIdentifiesTheEncounter(t *testing.T) {
	e := eicrFrom(t, "oru-no-visit-number.hl7")
	for u, r := range entries(e.Bundle) {
		if r["resourceType"] == "Encounter" {
			id := r["identifier"].([]any)[0].(map[string]any)
			if id["system"] != "urn:ietf:rfc:3986" || id["value"] != u {
				t.Errorf("identifier %v for %s", id, u)
			}
		}
	}
	if !strings.Contains(strings.Join(e.Notes, "\n"), "visit number (PV1-19)") {
		t.Errorf("notes %v", e.Notes)
	}
}

func TestAMessageWithNothingReportableGetsNoReport(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "adt-covid.hl7"))
	raw = []byte(strings.ReplaceAll(string(raw), "840539006^COVID-19", "195967001^Asthma"))
	m, _ := hl7.Parse(raw)
	e, err := FromV2(m, nil, v2fhir.Options{}, EICROptions{Facility: testFacility})
	if err == nil || !strings.Contains(err.Error(), "nothing reportable") || e == nil || len(e.Triggers) != 0 || e.Bundle != nil {
		t.Errorf("%v %+v", err, e)
	}
}

func TestTriggersLoadFromAValueSet(t *testing.T) {
	vs := `{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"ValueSet","url":"http://cts.nlm.nih.gov/fhir/ValueSet/2.16.840.1.113762.1.4.1146.6",
	  "version":"20260901","title":"Asthma (test)","expansion":{"contains":[{"system":"http://snomed.info/sct","code":"195967001","display":"Asthma (disorder)"}]}}}]}`
	path := filepath.Join(t.TempDir(), "rctc.json")
	_ = os.WriteFile(path, []byte(vs), 0o600)
	ts, err := LoadTriggers(path)
	if err != nil || ts.Len() != 1 {
		t.Fatalf("%v %v", err, ts)
	}
	raw, _ := os.ReadFile(filepath.Join("testdata", "adt-covid.hl7"))
	raw = []byte(strings.ReplaceAll(string(raw), "840539006^COVID-19", "195967001^Asthma"))
	m, _ := hl7.Parse(raw)
	e, err := FromV2(m, ts, v2fhir.Options{}, EICROptions{Facility: testFacility})
	if err != nil {
		t.Fatal(err)
	}
	flag, _ := json.Marshal(e.Bundle)
	for _, want := range []string{`"valueOid":"urn:oid:2.16.840.1.113762.1.4.1146.6"`, `"valueString":"20260901"`, `"display":"Asthma (disorder)"`} {
		if !strings.Contains(string(flag), want) {
			t.Errorf("flag lacks %s", want)
		}
	}
	if strings.Contains(strings.Join(e.Notes, " "), "built-in") {
		t.Error("a loaded RCTC is not the built-in sample")
	}
}

func TestTheReportIsWrappedInAnECRMessage(t *testing.T) {
	e := eicrFrom(t, "adt-covid.hl7")
	msg, err := ReportingBundle(e, ReportingOptions{Destination: "https://ph.test/fhir", Source: "https://ehr.test/fhir", Event: EventFor("A08", e.Triggers)})
	if err != nil {
		t.Fatal(err)
	}
	all := entries(msg)
	es := msg["entry"].([]any)
	header := es[0].(map[string]any)["resource"].(map[string]any)
	focus := header["focus"].([]any)[0].(map[string]any)["reference"].(string)
	sender := header["sender"].(map[string]any)["reference"].(string)
	if msg["type"] != "message" || header["resourceType"] != "MessageHeader" || all[focus]["resourceType"] != "Bundle" || all[sender]["resourceType"] != "Organization" {
		t.Errorf("message %v header %v", msg["type"], header)
	}
	if r := header["reason"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"]; r != "encounter-change" {
		t.Errorf("reason %v", r)
	}
	if _, err := ReportingBundle(e, ReportingOptions{Destination: "https://ph.test/fhir"}); err == nil {
		t.Error("no source endpoint: the agency has nowhere to send the response")
	}
}

// TestHAPIStoresTheEICR posts each eICR to a real HAPI FHIR server as a document Bundle. Skipped when HAPI is not running:
// docker run -d --name hapi -p 8090:8080 hapiproject/hapi:latest
func TestHAPIStoresTheEICR(t *testing.T) {
	const base = "http://localhost:8090/fhir"
	c := &http.Client{Timeout: 30 * time.Second}
	if res, err := c.Get(base + "/metadata"); err != nil {
		t.Skip("HAPI FHIR is not running")
	} else {
		_ = res.Body.Close()
	}
	for _, f := range []string{"adt-covid.hl7", "oru-sarscov2.hl7", "oru-no-visit-number.hl7"} {
		body, _ := json.Marshal(eicrFrom(t, f).Bundle)
		res, err := c.Post(base+"/Bundle", "application/fhir+json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Errorf("%s: HAPI answered %d: %s", f, res.StatusCode, out)
		}
	}
}
