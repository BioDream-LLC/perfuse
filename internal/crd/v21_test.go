package crd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/davinci"
)

// CRD 2.1.0's coverage-information: no indeterminate (conditional with info-needed OTH), reasons other than gold-card and
// detail-code as text, details without a category and coded in CRD's temp system, contact as ContactPoints.
func TestCoverageInformationIsWrittenInTheClientsCRDRelease(t *testing.T) {
	r := &Rules{Payer: "Plan"}
	rule := Rule{Covered: "covered", PA: "auth-needed", Documentation: []string{"clinical"}, Questionnaire: "https://p.example/Q/1",
		Details: []Detail{{Code: "allowed-quantity", Value: "2"}}, Contact: &Contact{Name: "UM", Phone: "+1-217-555-0199", URL: "https://p.example/um"}}
	ext := r.coverageInformation(rule, true, "Coverage/c", time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	old, _ := json.Marshal(forVersion(ext, davinci.V21))
	s := string(old)
	for _, want := range []string{`"valueContactPoint":{"system":"phone","value":"+1-217-555-0199"}`,
		`"code":"allowed-quantity","system":"http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp"`} {
		if !strings.Contains(s, want) {
			t.Errorf("2.1.0 lacks %s: %s", want, s)
		}
	}
	for _, gone := range []string{"valueContactDetail", `"category"`} {
		if strings.Contains(s, gone) {
			t.Errorf("2.1.0 carries %s: %s", gone, s)
		}
	}
	unchanged, _ := json.Marshal(forVersion(ext, davinci.V22))
	orig, _ := json.Marshal(ext)
	if string(unchanged) != string(orig) {
		t.Errorf("a 2.2 answer is unchanged")
	}

	technical, _ := json.Marshal(forVersion(r.coverageExplained("Coverage/c", "indeterminate", "technical", "The EHR did not answer.", time.Now()), davinci.V21))
	s = string(technical)
	if strings.Contains(s, "indeterminate") || !strings.Contains(s, `"url":"info-needed","valueCode":"OTH"`) ||
		!strings.Contains(s, `"valueCodeableConcept":{"text":"The EHR did not answer."}`) || strings.Contains(s, `"code":"technical"`) {
		t.Errorf("an indeterminate answer for 2.1.0: %s", s)
	}
}

// The release asked for wins over the client's registration, which wins over the server's setting.
func TestTheCRDReleaseComesFromTheRequestFirst(t *testing.T) {
	r := &Rules{DaVinciVersion: davinci.V21}
	req := &Request{Extension: map[string]json.RawMessage{"davinci-crd.requestedVersion": json.RawMessage(`"2.2"`)}, ClientVersion: "2.1"}
	if got := r.version(req); got != davinci.V22 {
		t.Errorf("asked for 2.2: %s", got)
	}
	if got := r.version(&Request{ClientVersion: "2.2"}); got != davinci.V22 {
		t.Errorf("registered as 2.2: %s", got)
	}
	if got := r.version(&Request{}); got != davinci.V21 {
		t.Errorf("the server's setting: %s", got)
	}
}
