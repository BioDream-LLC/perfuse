package fhirserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The adaptive home-oxygen questionnaire: 2 is asked only for a replacement, 3 only when the saturation is under 89.
const adaptiveQ = `{"resourceType":"Questionnaire","id":"o2a","url":"https://payer.example/Questionnaire/o2a","version":"1","status":"active",
	"extension":[{"url":"http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-questionnaireAdaptive","valueBoolean":true}],
	"item":[
	 {"linkId":"1","text":"Order reason","type":"choice","required":true,"answerOption":[
	   {"valueCoding":{"system":"http://example.org","code":"initial"}},{"valueCoding":{"system":"http://example.org","code":"replacement"}}]},
	 {"linkId":"2","text":"Why is it being replaced?","type":"string","required":true,
	   "enableWhen":[{"question":"1","operator":"=","answerCoding":{"system":"http://example.org","code":"replacement"}}]},
	 {"linkId":"sat","text":"Resting SpO2 (%)","type":"integer","required":true},
	 {"linkId":"3","text":"Date of the qualifying test","type":"date","required":true,
	   "enableWhen":[{"question":"sat","operator":"<","answerInteger":89}]}]}`

func nextQ(t *testing.T, h http.Handler, qr map[string]any) (map[string]any, int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"resourceType": "Parameters", "parameter": []any{map[string]any{"name": "questionnaire-response", "resource": qr}}})
	rec := payerDo(t, h, "POST", "/Questionnaire/$next-question", string(b), nil)
	if rec.Code != http.StatusOK {
		return nil, rec.Code, rec.Body.String()
	}
	var out struct {
		Parameter []struct {
			Name     string         `json:"name"`
			Resource map[string]any `json:"resource"`
		} `json:"parameter"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Parameter) != 1 || out.Parameter[0].Name != "return" {
		t.Fatalf("not DTR's output parameters: %s", rec.Body)
	}
	return out.Parameter[0].Resource, rec.Code, ""
}

func askedIDs(qr map[string]any) string {
	var ids []string
	for _, it := range listOf(containedQuestionnaire(qr)["item"]) {
		ids = append(ids, it.(map[string]any)["linkId"].(string))
	}
	return strings.Join(ids, ",")
}

func answer(qr map[string]any, linkID string, value map[string]any) {
	qr["item"] = append(listOf(qr["item"]), map[string]any{"linkId": linkID, "answer": []any{value}})
}

func TestAnAdaptiveQuestionnaireAsksOnlyTheQuestionsThatApply(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	if rec := payerDo(t, h, "PUT", "/Questionnaire/o2a", adaptiveQ, nil); rec.Code >= 300 {
		t.Fatal(rec.Body)
	}

	// The package hands out the shell: no questions, and a response with the shell contained.
	rec := payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[`+coverageParam+`,{"name":"questionnaire","valueCanonical":"https://payer.example/Questionnaire/o2a"}]}`, nil)
	bundles, _ := packageOf(t, rec.Body.String())
	if len(bundles) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := entriesOf(bundles[0])
	if q := got["Questionnaire"][0]; q["item"] != nil || !strings.Contains(toJSON(q["meta"]), "dtr-questionnaire-adapt-search") {
		t.Errorf("the packaged adaptive questionnaire should be an empty adapt-search shell: %v", q)
	}
	qr := got["QuestionnaireResponse"][0]
	if qr["questionnaire"] != "#o2a" || askedIDs(qr) != "" {
		t.Fatalf("the seeded response should point at its contained shell: %v", qr)
	}

	// Replacement, saturation 92: 1, 2, sat, and 3 is skipped.
	steps := []struct {
		answer string
		value  map[string]any
		asked  string
	}{
		{"", nil, "1"},
		{"1", map[string]any{"valueCoding": map[string]any{"system": "http://example.org", "code": "replacement"}}, "1,2"},
		{"2", map[string]any{"valueString": "worn out"}, "1,2,sat"},
		{"sat", map[string]any{"valueInteger": 92}, "1,2,sat"},
	}
	for _, st := range steps {
		if st.answer != "" {
			answer(qr, st.answer, st.value)
		}
		next, code, body := nextQ(t, h, qr)
		if code != http.StatusOK {
			t.Fatalf("after %s: %d %s", st.answer, code, body)
		}
		if askedIDs(next) != st.asked {
			t.Errorf("after answering %q, asked %q, want %q", st.answer, askedIDs(next), st.asked)
		}
		qr = next
	}
	if qr["status"] != "completed" {
		t.Errorf("nothing is left to ask, so the response is complete: %v", qr["status"])
	}
}

func TestTheNextQuestionWaitsForARequiredAnswer(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	payerDo(t, h, "PUT", "/Questionnaire/o2a", adaptiveQ, nil)
	shell := map[string]any{"resourceType": "Questionnaire", "id": "o2a", "url": "https://payer.example/Questionnaire/o2a", "version": "1", "status": "active"}
	qr := map[string]any{"resourceType": "QuestionnaireResponse", "status": "in-progress", "questionnaire": "#o2a", "contained": []any{shell}}
	qr, _, _ = nextQ(t, h, qr)
	if _, code, body := nextQ(t, h, qr); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Order reason") {
		t.Errorf("an unanswered required question: %d %s", code, body)
	}
	// Under 89, the qualifying-test date is asked.
	answer(qr, "1", map[string]any{"valueCoding": map[string]any{"system": "http://example.org", "code": "initial"}})
	qr, _, _ = nextQ(t, h, qr)
	answer(qr, "sat", map[string]any{"valueInteger": 85})
	if qr, _, _ = nextQ(t, h, qr); askedIDs(qr) != "1,sat,3" {
		t.Errorf("a low saturation should bring in 3: %s", askedIDs(qr))
	}
}

func TestAStandardOrUnknownQuestionnaireIsNotAnsweredAdaptively(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	payerDo(t, h, "PUT", "/Questionnaire/plain", `{"resourceType":"Questionnaire","id":"plain","url":"https://payer.example/Questionnaire/plain","status":"active"}`, nil)
	for url, want := range map[string]int{"https://payer.example/Questionnaire/plain": http.StatusUnprocessableEntity, "https://payer.example/Questionnaire/none": http.StatusNotFound} {
		qr := map[string]any{"resourceType": "QuestionnaireResponse", "status": "in-progress", "questionnaire": "#q",
			"contained": []any{map[string]any{"resourceType": "Questionnaire", "id": "q", "url": url, "status": "active"}}}
		if _, code, body := nextQ(t, h, qr); code != want {
			t.Errorf("%s: want %d, got %d %s", url, want, code, body)
		}
	}
	if _, code, _ := nextQ(t, h, map[string]any{"resourceType": "QuestionnaireResponse", "status": "in-progress"}); code != http.StatusBadRequest {
		t.Errorf("no contained questionnaire: %d", code)
	}
}
