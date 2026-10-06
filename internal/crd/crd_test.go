package crd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if len(resp.SystemActions) != 0 || len(resp.Cards) != 1 || !strings.Contains(resp.Cards[0].Summary, "No coverage was sent") {
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

func TestTurningCoverageInfoOffReturnsNothing(t *testing.T) {
	// dev-5: setting a response type's option to false means no cards of that type. Coverage information is all this service
	// returns, so the answer is empty.
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	req := request(t, order)
	req.Extension = map[string]json.RawMessage{"davinci-crd.configuration": json.RawMessage(`{"coverage-info":false}`)}
	resp, err := rules.Evaluate(req, time.Now())
	if err != nil || len(resp.Cards) != 0 || len(resp.SystemActions) != 0 {
		t.Errorf("%v %+v", err, resp)
	}
}

func TestCoverageThatIsNotInForceIsNotCovered(t *testing.T) {
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	req := request(t, order)
	req.Prefetch["coverage"] = json.RawMessage(`{"resourceType":"Coverage","id":"c1","status":"active","period":{"end":"2020-12-31"}}`)
	resp, _ := rules.Evaluate(req, time.Now())
	ext, _ := json.Marshal(resp.SystemActions[0].Resource["extension"])
	if !strings.Contains(string(ext), `"valueCode":"not-covered"`) || !strings.Contains(string(ext), `"code":"no-active-coverage"`) {
		t.Errorf("%s", ext)
	}
}

func TestAMemberThePayerDoesNotKnowIsNotFound(t *testing.T) {
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	rules.Check = func(_ context.Context, cov, _ map[string]any, _ time.Time) (Membership, string, error) {
		return NoMemberFound, "No member of Springfield Health Plan matches this patient.", nil
	}
	resp, _ := rules.Evaluate(request(t, order), time.Now())
	ext, _ := json.Marshal(resp.SystemActions[0].Resource["extension"])
	if !strings.Contains(string(ext), `"code":"no-member-found"`) || !strings.Contains(string(ext), "No member of") {
		t.Errorf("%s", ext)
	}
}

func TestAnEHRServerThatFailsIsATechnicalProblem(t *testing.T) {
	// CRD's technical reason: the coverage could not be read, so the answer is indeterminate, with what went wrong.
	ehr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer ehr.Close()
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	req := request(t, order)
	delete(req.Prefetch, "coverage")
	req.FHIRServer = ehr.URL
	resp, _ := rules.Evaluate(req, time.Now())
	if len(resp.SystemActions) != 1 {
		t.Fatalf("%+v", resp)
	}
	ext, _ := json.Marshal(resp.SystemActions[0].Resource["extension"])
	if !strings.Contains(string(ext), `"valueCode":"indeterminate"`) || !strings.Contains(string(ext), `"code":"technical"`) || !strings.Contains(string(ext), "answered 500") {
		t.Errorf("%s", ext)
	}
}

func TestTheUpdatedOrderKeepsTheEHRsKeyOrder(t *testing.T) {
	ext := map[string]any{"url": ExtCoverageInformation}
	got := string(withExtension(json.RawMessage(`{"resourceType":"DeviceRequest","status":"draft","id":"o","extension":[{"url":"x"},{"url":"`+ExtCoverageInformation+`","old":1}],"subject":{"reference":"Patient/p","display":"P"}}`), ext))
	want := `{"resourceType":"DeviceRequest","status":"draft","id":"o","extension":[{"url":"x"},{"url":"` + ExtCoverageInformation + `"}],"subject":{"reference":"Patient/p","display":"P"}}`
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestAnApprovedAuthorizationMakesTheRequirementSatisfied(t *testing.T) {
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	rules.Authorized = func(_ context.Context, _, _ map[string]any, codes []string, _ time.Time) (string, error) {
		return "AUTH-778", nil
	}
	resp, _ := rules.Evaluate(request(t, order), time.Now())
	ext, _ := json.Marshal(resp.SystemActions[0].Resource["extension"])
	if !strings.Contains(string(ext), `"pa-needed","valueCode":"satisfied"`) || !strings.Contains(string(ext), `"satisfied-pa-id","valueString":"AUTH-778"`) ||
		!strings.Contains(resp.Cards[0].Summary, "already approved") {
		t.Errorf("%s %s", ext, resp.Cards[0].Summary)
	}
}

// CDS Hooks limits a card summary to under 140 characters, and the summary carries the payer's own rule description.
func TestACardSummaryStaysUnder140Characters(t *testing.T) {
	rules, _ := LoadRules("../../examples/crd/rules.yaml")
	long := strings.Repeat("Home oxygen concentrator with portable cylinders ", 4)
	for i := range rules.Rules {
		rules.Rules[i].Description = long
	}
	resp, err := rules.Evaluate(request(t, order), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := resp.Cards[0]
	if n := len([]rune(c.Summary)); n >= 140 {
		t.Errorf("summary is %d characters: %q", n, c.Summary)
	}
	if !strings.HasPrefix(c.Detail, strings.TrimSpace(long)) || !strings.Contains(c.Detail, "prior authorization required") {
		t.Errorf("the full summary did not move to the detail: %q", c.Detail)
	}
}

func TestPADecisionFollowsTheRules(t *testing.T) {
	r := &Rules{Payer: "Plan", Rules: []Rule{
		{Codes: []string{"3"}, Covered: "covered", PA: "auth-needed", PADecision: "approve", Description: "Consultation"},
		{Codes: []string{"76"}, Covered: "not-covered", Description: "Dialysis"},
		{Codes: []string{"BT"}, Covered: "covered", PA: "no-auth", Description: "Gynecological"},
		{Codes: []string{"2"}, Covered: "covered", PA: "auth-needed", Description: "Surgical"},
	}}
	cc := func(code string) map[string]any {
		return map[string]any{"coding": []any{map[string]any{"system": "https://codesystem.x12.org/005010/1365", "code": code}}}
	}
	for code, want := range map[string]string{"3": "approve", "76": "deny", "BT": "approve", "2": "pend", "99": "pend"} {
		if got := r.PADecision(cc(code)).Decision; got != want {
			t.Errorf("%s: %s, want %s", code, got, want)
		}
	}
	if a := r.PADecision(cc("99"), cc("76")); a.Decision != "deny" || a.Why != "Dialysis" {
		t.Errorf("the requested order's code should count too: %+v", a)
	}
	r.Rules[3].Details = []Detail{{Code: "allowed-quantity", Value: "10"}}
	r.Rules[3].PAAttachments = []string{"18776-5"}
	r.Rules[3].Questionnaire = "https://payer.example/Questionnaire/surgery"
	if a := r.PADecision(cc("2")); a.AllowedQuantity != 10 || a.Attachments[0] != "18776-5" || a.Questionnaire == "" {
		t.Errorf("limits and documentation should come with the answer: %+v", a)
	}
	r.Rules[0].PADecision = "maybe"
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "pa_decision") {
		t.Errorf("an unknown pa_decision should be refused: %v", err)
	}
}
