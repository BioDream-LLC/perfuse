package crd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const order = `{"resourceType":"DeviceRequest","id":"oxygen-1","status":"draft","intent":"original-order",
"codeCodeableConcept":{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0424"}]},
"subject":{"reference":"Patient/p1"}}`

const coverage = `{"resourceType":"Bundle","type":"searchset","entry":[{"resource":{"resourceType":"Coverage","id":"cov1","status":"active"}}]}`

func request(t *testing.T, orders ...string) *Request {
	t.Helper()
	var entries []string
	for _, o := range orders {
		entries = append(entries, `{"resource":`+o+`}`)
	}
	return &Request{Hook: "order-sign", HookInstance: "h1", Context: map[string]json.RawMessage{
		"patientId": json.RawMessage(`"p1"`), "draftOrders": json.RawMessage(`{"resourceType":"Bundle","type":"collection","entry":[` +
			strings.Join(entries, ",") + `]}`)}, Prefetch: map[string]json.RawMessage{"coverage": json.RawMessage(coverage)}}
}

func TestTheExampleRulesLoadAndAnswerHomeOxygen(t *testing.T) {
	rules, err := LoadRules("../../examples/crd/rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rules.Evaluate(request(t, order), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.SystemActions) != 1 || len(resp.Cards) != 1 {
		t.Fatalf("%+v", resp)
	}
	card := resp.Cards[0]
	if card.Indicator != "warning" || !strings.Contains(card.Summary, "prior authorization required") ||
		!strings.Contains(card.Detail, "questionnaire https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen") {
		t.Errorf("card: %+v", card)
	}
	ext, _ := json.Marshal(resp.SystemActions[0].Resource["extension"])
	for _, want := range []string{ExtCoverageInformation, `"valueReference":{"reference":"Coverage/cov1"}`, `"valueCode":"covered"`,
		`"pa-needed","valueCode":"auth-needed"`, `"doc-needed","valueCode":"clinical"`, `"valueDate":"2026-10-03"`, "coverage-assertion-id"} {
		if !strings.Contains(string(ext), want) {
			t.Errorf("the extension lacks %s:\n%s", want, ext)
		}
	}
}

func TestAnUnknownCodeIsConditionalNeverAssumedCovered(t *testing.T) {
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	other := strings.Replace(order, "E0424", "Z9999", 1)
	resp, _ := rules.Evaluate(request(t, other), time.Now())
	ext, _ := json.Marshal(resp.SystemActions[0].Resource["extension"])
	if !strings.Contains(string(ext), `"covered","valueCode":"conditional"`) || !strings.Contains(string(ext), `"pa-needed","valueCode":"conditional"`) {
		t.Errorf("%s", ext)
	}
	if !strings.Contains(resp.Cards[0].Summary, "could not be determined") {
		t.Errorf("%+v", resp.Cards[0])
	}
}

func TestNoCoverageIsSaidRatherThanGuessed(t *testing.T) {
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	req := request(t, order)
	delete(req.Prefetch, "coverage")
	resp, _ := rules.Evaluate(req, time.Now())
	if len(resp.SystemActions) != 0 || len(resp.Cards) != 1 || !strings.Contains(resp.Cards[0].Summary, "No active coverage") {
		t.Errorf("%+v", resp)
	}
}

func TestRulesWithWrongCodesAreRefused(t *testing.T) {
	bad := &Rules{Payer: "P", Rules: []Rule{{Codes: []string{"1"}, Covered: "yes"}, {Codes: []string{"2"}, Covered: "covered",
		Documentation: []string{"clinical"}}}}
	err := bad.Validate()
	if err == nil || !strings.Contains(err.Error(), `covered "yes"`) || !strings.Contains(err.Error(), "names no questionnaire") {
		t.Errorf("%v", err)
	}
}

func TestAnUnmatchedOrderSaysWhyInformationIsNeeded(t *testing.T) {
	// crd-ci-q6: info-needed OTH must carry a reason. The HL7 validator rejected every answer for an unrecognised order without one.
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	resp, _ := rules.Evaluate(request(t, strings.Replace(order, "E0424", "Z9999", 1)), time.Now())
	ext, _ := json.Marshal(resp.SystemActions[0].Resource["extension"])
	if !strings.Contains(string(ext), `"info-needed","valueCode":"OTH"`) || !strings.Contains(string(ext), `"url":"reason","valueCodeableConcept":{"text":"No coverage rule matches`) {
		t.Errorf("%s", ext)
	}
}

func TestRulesBreakingCRDsInvariantsAreRefused(t *testing.T) {
	for name, rule := range map[string]Rule{
		"q1": {Codes: []string{"1"}, Covered: "covered", Questionnaire: "https://p.example/Questionnaire/q"},
		"q2": {Codes: []string{"1"}, Covered: "not-covered", PA: "no-auth"},
		"q5": {Codes: []string{"1"}, Covered: "covered", PA: "satisfied"},
		"q3": {Codes: []string{"1"}, Covered: "conditional"},
		"q8": {Codes: []string{"1"}, Covered: "covered", PA: "auth-needed", Documentation: []string{"clinical"}, Questionnaire: "https://p.example/Questionnaire/q"},
	} {
		r := &Rules{Payer: "P", Rules: []Rule{rule}}
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "crd-ci-"+name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAnAssertionIsRememberedForDTR(t *testing.T) {
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	resp, _ := rules.Evaluate(request(t, order), time.Now())
	var id string
	for _, e := range resp.SystemActions[0].Resource["extension"].([]any)[0].(map[string]any)["extension"].([]any) {
		if m := e.(map[string]any); m["url"] == "coverage-assertion-id" {
			id = m["valueString"].(string)
		}
	}
	if got := rules.QuestionnairesFor(id); len(got) != 1 || !strings.HasSuffix(got[0], "/Questionnaire/home-oxygen") {
		t.Errorf("assertion %s: %v", id, got)
	}
}
