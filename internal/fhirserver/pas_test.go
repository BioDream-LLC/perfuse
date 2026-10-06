package fhirserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

const pasTestBase = "http://provider.example/fhir"

// pasRequestJSON is a PAS Request Bundle: a Claim from organization 8189991234 for member 12345678901, one item per code, all
// of them in X12's service type list. certType 3 on an item cancels it.
func pasRequestJSON(trace string, items ...string) string {
	var list []any
	for i, code := range items {
		code, qty, _ := strings.Cut(code, "*")
		system := "https://codesystem.x12.org/005010/1365"
		if code == "absent" {
			system, code = "http://terminology.hl7.org/CodeSystem/data-absent-reason", "not-applicable"
		}
		item := map[string]any{"sequence": i + 1, "productOrService": map[string]any{"coding": []any{map[string]any{
			"system": system, "code": strings.TrimPrefix(code, "cancel:")}}},
			"extension": []any{map[string]any{"url": pasBase + "extension-itemTraceNumber",
				"valueIdentifier": map[string]any{"system": "http://provider.example/trace", "value": fmt.Sprintf("%s-%d", trace, i+1)}}}}
		if qty != "" {
			item["quantity"] = map[string]any{"value": json.Number(qty)}
		}
		if strings.HasPrefix(code, "cancel:") {
			item["extension"] = append(item["extension"].([]any), map[string]any{"url": pasBase + "extension-certificationType",
				"valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": "https://codesystem.x12.org/005010/1322", "code": "3"}}}})
		}
		list = append(list, item)
	}
	b := map[string]any{"resourceType": "Bundle", "type": "collection", "timestamp": "2026-10-05T10:00:00Z",
		"identifier": map[string]any{"system": "http://provider.example/tx", "value": trace},
		"entry": []any{
			map[string]any{"fullUrl": pasTestBase + "/Claim/" + trace, "resource": map[string]any{
				"resourceType": "Claim", "id": trace, "status": "active", "use": "preauthorization",
				"identifier": []any{map[string]any{"system": "http://provider.example/PATIENT_EVENT_TRACE_NUMBER", "value": trace}},
				"type":       map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/claim-type", "code": "professional"}}},
				"patient":    map[string]any{"reference": "Patient/member"}, "insurer": map[string]any{"reference": "Organization/plan"},
				"provider": map[string]any{"reference": "Organization/clinic"}, "created": "2026-10-05",
				"priority":  map[string]any{"coding": []any{map[string]any{"code": "normal"}}},
				"insurance": []any{map[string]any{"sequence": 1, "focal": true, "coverage": map[string]any{"reference": "Coverage/cov"}}},
				"item":      list}},
			map[string]any{"fullUrl": pasTestBase + "/Patient/member", "resource": map[string]any{"resourceType": "Patient", "id": "member",
				"identifier": []any{map[string]any{"system": "http://plan.example/member", "value": "12345678901"}}}},
			map[string]any{"fullUrl": pasTestBase + "/Organization/plan", "resource": map[string]any{"resourceType": "Organization", "id": "plan", "name": "Plan"}},
			map[string]any{"fullUrl": pasTestBase + "/Organization/clinic", "resource": map[string]any{"resourceType": "Organization", "id": "clinic",
				"identifier": []any{map[string]any{"system": "http://hl7.org/fhir/sid/us-npi", "value": "8189991234"}}}},
			map[string]any{"fullUrl": pasTestBase + "/Coverage/cov", "resource": map[string]any{"resourceType": "Coverage", "id": "cov",
				"status": "active", "beneficiary": map[string]any{"reference": "Patient/member"}, "payor": []any{map[string]any{"reference": "Organization/plan"}}}},
		}}
	raw, _ := json.Marshal(b)
	return string(raw)
}

// pasRules approves consultations (3), denies dialysis (76) and leaves everything else to a reviewer.
func pasRules(codes ...map[string]any) PASAnswer {
	for _, cc := range codes {
		for _, c := range asSliceAny(cc["coding"]) {
			switch str(asMapAny(c)["code"]) {
			case "3":
				return PASAnswer{Decision: "approve", Why: "Consultations are approved on request."}
			case "76":
				return PASAnswer{Decision: "deny", Why: "Dialysis is not covered by this plan."}
			case "PT":
				return PASAnswer{Decision: "approve", Why: "Physical therapy.", AllowedQuantity: 6}
			case "62":
				return PASAnswer{Decision: "approve", Why: "An X-ray comes before an MRI.",
					Alternative: map[string]any{"system": "https://codesystem.x12.org/005010/1365", "code": "4"}}
			case "2":
				return PASAnswer{Decision: "pend", Why: "Surgery needs the plan of care.", Attachments: []string{"18776-5"},
					Questionnaire: "https://payer.example/Questionnaire/surgery"}
			}
		}
	}
	return PASAnswer{Decision: "pend"}
}

func newPASFixture(t *testing.T) *subFixture {
	t.Helper()
	f := newSubFixture(t, true)
	f.subs.opts.PAS = true
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewServer(f.store, "http://example.test/fhir", log)
	srv.Auth = OpenAuth{}
	srv.PAS = &PAS{Decide: pasRules, Now: func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }}
	f.h = srv.Handler()
	return f
}

func pasPost(t *testing.T, h http.Handler, path, body string) (int, map[string]any) {
	t.Helper()
	rec := payerDo(t, h, "POST", path, body, nil)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func claimResponseOf(t *testing.T, bundle map[string]any) map[string]any {
	t.Helper()
	for _, e := range asSliceAny(bundle["entry"]) {
		if res := asMapAny(asMapAny(e)["resource"]); res["resourceType"] == "ClaimResponse" {
			return res
		}
	}
	t.Fatalf("no ClaimResponse in %v", bundle)
	return nil
}

// actionCodes lists each item's review action code, in item order.
func actionCodes(cr map[string]any) string {
	var out []string
	for _, it := range asSliceAny(cr["item"]) {
		for _, a := range asSliceAny(asMapAny(it)["adjudication"]) {
			for _, e := range asSliceAny(asMapAny(a)["extension"]) {
				for _, x := range asSliceAny(asMapAny(e)["extension"]) {
					if str(asMapAny(x)["url"]) == pasBase+"extension-reviewActionCode" {
						out = append(out, str(asMapAny(asSliceAny(asMapAny(asMapAny(x)["valueCodeableConcept"])["coding"])[0])["code"]))
					}
				}
			}
		}
	}
	return strings.Join(out, ",")
}

func TestSubmitDecidesEachItemByTheRules(t *testing.T) {
	f := newPASFixture(t)
	code, bundle := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("T1", "3", "76", "2", "cancel:3"))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, bundle)
	}
	if bundle["type"] != "collection" || bundle["identifier"] == nil || bundle["timestamp"] == nil {
		t.Errorf("not a PAS Response Bundle: %v", bundle)
	}
	cr := claimResponseOf(t, bundle)
	if got := actionCodes(cr); got != "A1,A3,A4,C" {
		t.Errorf("decisions %s, want approve, deny, pend, cancelled", got)
	}
	if cr["outcome"] != "complete" || cr["preAuthRef"] == nil || str(asMapAny(cr["request"])["reference"]) != pasTestBase+"/Claim/T1" {
		t.Errorf("ClaimResponse: %v", cr)
	}
	// The ClaimResponse's references resolve inside the response, and the request Claim is not repeated in it.
	urls := map[string]bool{}
	for _, e := range asSliceAny(bundle["entry"]) {
		urls[str(asMapAny(e)["fullUrl"])] = true
	}
	for _, field := range []string{"patient", "insurer", "requestor"} {
		if ref := str(asMapAny(cr[field])["reference"]); !urls[ref] {
			t.Errorf("%s %q is not in the response", field, ref)
		}
	}
	if urls[pasTestBase+"/Claim/T1"] {
		t.Error("the request Claim should not be in the response")
	}
	if !strings.Contains(toJSON(cr["processNote"]), "Dialysis is not covered") {
		t.Errorf("a denial should say why: %v", cr["processNote"])
	}
}

func TestARequestPASCannotAnswerIsRefusedWithAnOutcome(t *testing.T) {
	f := newPASFixture(t)
	for name, body := range map[string]string{
		"empty":     `{"resourceType":"Bundle","type":"collection","entry":[]}`,
		"not claim": `{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"urn:uuid:1","resource":{"resourceType":"Patient"}}]}`,
		"no items":  strings.Replace(pasRequestJSON("T2", "3"), `"item":[{`, `"itemx":[{`, 1),
	} {
		code, out := pasPost(t, f.h, "/Claim/$submit", body)
		if code != http.StatusBadRequest || out["resourceType"] != "OperationOutcome" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}

func TestInquiryFindsTheLatestAnswerForThatProviderAndMember(t *testing.T) {
	f := newPASFixture(t)
	pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("T3", "2"))
	code, out := pasPost(t, f.h, "/Claim/$inquire", pasRequestJSON("T3"))
	if code != http.StatusOK || out["resourceType"] != "Parameters" {
		t.Fatalf("%d %v", code, out)
	}
	params := asSliceAny(out["parameter"])
	if len(params) != 1 || str(asMapAny(params[0])["name"]) != "return" {
		t.Fatalf("one return Bundle expected: %v", out)
	}
	b := asMapAny(asMapAny(params[0])["resource"])
	if !strings.Contains(toJSON(b["meta"]), "profile-pas-inquiry-response-bundle") || actionCodes(claimResponseOf(t, b)) != "A4" {
		t.Errorf("inquiry response: %v", b)
	}
	// Another provider asking about the same member and trace number learns nothing.
	other := strings.Replace(pasRequestJSON("T3"), "8189991234", "9999999999", 1)
	if _, out := pasPost(t, f.h, "/Claim/$inquire", other); len(asSliceAny(out["parameter"])) != 0 {
		t.Errorf("another provider's inquiry should find nothing: %v", out)
	}
}

func TestAReviewersDecisionIsDeliveredToTheSubscribedProvider(t *testing.T) {
	f := newPASFixture(t)
	rcv := newReceiver(t)
	sub := map[string]any{"resourceType": "Subscription", "status": "requested", "reason": "PAS", "criteria": pasTopicURL,
		"_criteria": map[string]any{"extension": []any{map[string]any{"url": extFilterCriteria, "valueString": "org-identifier=8189991234"}}},
		"channel": map[string]any{"type": "rest-hook", "endpoint": rcv.srv.URL, "payload": "application/json",
			"_payload": map[string]any{"extension": []any{map[string]any{"url": extPayloadContent, "valueCode": "full-resource"}}}}}
	raw, _ := json.Marshal(sub)
	f.put(t, "/Subscription/pas", string(raw))
	f.deliver(2)
	if f.status(t, "pas") != "active" {
		t.Fatalf("the PAS subscription should be active after its handshake: %s", f.status(t, "pas"))
	}

	_, submitted := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("T4", "2", "3"))
	id := str(claimResponseOf(t, submitted)["id"])
	f.deliver(2)
	if n := len(rcv.received()); n != 1 {
		t.Fatalf("the synchronous answer is not an event; only the handshake should have been sent, got %d", n)
	}

	code, decided := pasPost(t, f.h, "/Claim/$decide", `{"resourceType":"Parameters","parameter":[
		{"name":"claimResponse","valueString":"`+id+`"},{"name":"decision","valueCode":"approve"},
		{"name":"reason","valueString":"Reviewed by the medical director."}]}`)
	if code != http.StatusOK || actionCodes(claimResponseOf(t, decided)) != "A1,A1" {
		t.Fatalf("%d %v", code, decided)
	}
	f.deliver(2)
	got := rcv.received()
	if len(got) != 2 {
		t.Fatalf("one event notification expected after the decision, got %d", len(got)-1)
	}
	note := got[1]
	focus := ""
	for _, part := range asSliceAny(statusParams(t, note)["notification-event"]) {
		if str(asMapAny(part)["name"]) == "focus" {
			focus = str(asMapAny(asMapAny(part)["valueReference"])["reference"])
		}
	}
	entries := asSliceAny(note["entry"])
	if len(entries) != 2 || str(asMapAny(entries[1])["fullUrl"]) != focus {
		t.Fatalf("the focus should be the response Bundle, carried in full: %v", note)
	}
	inner := asMapAny(asMapAny(entries[1])["resource"])
	if strings.Contains(toJSON(inner), "pended for a reviewer") {
		t.Errorf("a decided response should not still say it is pended: %v", claimResponseOf(t, inner)["processNote"])
	}
	if inner["type"] != "collection" || actionCodes(claimResponseOf(t, inner)) != "A1,A1" {
		t.Errorf("the notification should carry the decided response: %v", inner)
	}

	// Deciding again is a conflict: nothing is pended any more.
	if code, _ := pasPost(t, f.h, "/Claim/$decide", `{"resourceType":"Parameters","parameter":[
		{"name":"claimResponse","valueString":"`+id+`"},{"name":"decision","valueCode":"deny"}]}`); code != http.StatusConflict {
		t.Errorf("a second decision should be 409, got %d", code)
	}
	// And an inquiry now reads the decision.
	_, out := pasPost(t, f.h, "/Claim/$inquire", pasRequestJSON("T4"))
	if actionCodes(claimResponseOf(t, asMapAny(asMapAny(asSliceAny(out["parameter"])[0])["resource"]))) != "A1,A1" {
		t.Errorf("an inquiry after the decision should read it: %v", out)
	}
}

func TestAnotherProvidersSubscriptionHearsNothing(t *testing.T) {
	f := newPASFixture(t)
	rcv := newReceiver(t)
	sub := strings.Replace(subscriptionJSON(rcv.srv.URL, "org-identifier=1111111111", "full-resource"), TopicEncounter, pasTopicURL, 1)
	f.put(t, "/Subscription/other", sub)
	f.deliver(2)
	_, submitted := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("T5", "2"))
	id := str(claimResponseOf(t, submitted)["id"])
	pasPost(t, f.h, "/Claim/$decide", `{"resourceType":"Parameters","parameter":[{"name":"claimResponse","valueString":"`+id+`"},{"name":"decision","valueCode":"deny"}]}`)
	f.deliver(2)
	if n := len(rcv.received()); n != 1 {
		t.Errorf("only the handshake should reach another provider's subscription, got %d messages", n)
	}
}

func TestThePASTopicIsOfferedOnlyWithPAS(t *testing.T) {
	f := newSubFixture(t, true)
	sub := strings.Replace(subscriptionJSON("http://127.0.0.1:1/hook", "", "id-only"), TopicEncounter, pasTopicURL, 1)
	rec := payerDo(t, f.h, "PUT", "/Subscription/x", sub, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("without PAS the topic should be refused: %d %s", rec.Code, rec.Body)
	}
	_ = context.Background()
}

func entriesOfType(bundle map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, e := range asSliceAny(bundle["entry"]) {
		if res := asMapAny(asMapAny(e)["resource"]); res["resourceType"] == kind {
			out = append(out, res)
		}
	}
	return out
}

func TestTheRulesCanCertifyLessOrSomethingElse(t *testing.T) {
	f := newPASFixture(t)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("T6", "PT*12", "62", "absent"))
	cr := claimResponseOf(t, b)
	if got := actionCodes(cr); got != "A6,A6" {
		t.Fatalf("decisions %s, want modified twice and no decision for the line with no service", got)
	}
	items := asSliceAny(cr["item"])
	if !strings.Contains(toJSON(items[0]), "extension-itemAuthorizedDetail") || !strings.Contains(toJSON(items[0]), `"value":6`) {
		t.Errorf("a quantity limit should say what was authorized: %v", items[0])
	}
	added := asSliceAny(cr["addItem"])
	if len(added) != 1 || !strings.Contains(toJSON(added[0]), `"code":"4"`) || actionCodes(map[string]any{"item": added}) != "A1" {
		t.Errorf("the alternative should be an approved added item: %v", added)
	}
	errs := asSliceAny(cr["error"])
	if cr["outcome"] != "partial" || len(errs) != 1 || !strings.Contains(toJSON(errs[0]), `"code":"AG"`) ||
		!strings.Contains(toJSON(errs[0]), "Claim.item[2].productOrService") {
		t.Errorf("a line naming no service is an error, not a decision: %v %v", cr["outcome"], errs)
	}
	// The payer's own reference for each line, never the provider's.
	if !strings.Contains(toJSON(items[0]), "ADM-") {
		t.Errorf("each line should carry the payer's administration reference number: %v", items[0])
	}
}

func TestAPendedRequestSaysWhatItNeeds(t *testing.T) {
	f := newPASFixture(t)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("T7", "2"))
	cr := claimResponseOf(t, b)
	tasks, comms := entriesOfType(b, "Task"), entriesOfType(b, "CommunicationRequest")
	if len(tasks) != 1 || len(comms) != 1 || len(asSliceAny(cr["communicationRequest"])) != 1 {
		t.Fatalf("a pended request needing documents should ask for them: %d tasks, %d requests", len(tasks), len(comms))
	}
	task := toJSON(tasks[0])
	for _, want := range []string{"payer-url", "attachments-needed", "18776-5", "questionnaire-context", "https://payer.example/Questionnaire/surgery", "priorAuthorization"} {
		if !strings.Contains(task, want) {
			t.Errorf("the Task should carry %s: %s", want, task)
		}
	}
	// Its references resolve inside the response.
	urls := map[string]bool{}
	for _, e := range asSliceAny(b["entry"]) {
		urls[str(asMapAny(e)["fullUrl"])] = true
	}
	if ref := str(asMapAny(tasks[0]["for"])["reference"]); !urls[ref] {
		t.Errorf("Task.for %q is not in the response", ref)
	}
	_, decided := pasPost(t, f.h, "/Claim/$decide", `{"resourceType":"Parameters","parameter":[{"name":"claimResponse","valueString":"`+
		str(cr["id"])+`"},{"name":"decision","valueCode":"approve"},{"name":"reviewer","valueString":"1234567893"}]}`)
	if entriesOfType(decided, "Task")[0]["status"] != "completed" {
		t.Errorf("an answered request for documents is completed")
	}
	if rv := toJSON(claimResponseOf(t, decided)["extension"]); !strings.Contains(rv, `"valueBoolean":true`) || !strings.Contains(rv, "1234567893") {
		t.Errorf("a reviewer's decision should say a person made it: %s", rv)
	}
}
