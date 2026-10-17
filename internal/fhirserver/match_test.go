package fhirserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeMatcher struct{}

func (fakeMatcher) Match(_ context.Context, p json.RawMessage, _ int) ([]PatientMatch, error) {
	if !strings.Contains(string(p), "Garcia") {
		return nil, nil
	}
	return []PatientMatch{
		{Patient: map[string]any{"resourceType": "Patient", "id": "E1"}, Score: 0.98, Grade: "certain"},
		{Patient: map[string]any{"resourceType": "Patient", "id": "E2"}, Score: 0.71, Grade: "probable"},
	}, nil
}

func TestPatientMatch(t *testing.T) {
	s, _ := newTestServer(t)
	post := func(body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/Patient/$match", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/fhir+json")
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	params := `{"resourceType":"Parameters","parameter":[{"name":"resource","resource":{"resourceType":"Patient","name":[{"family":"Garcia"}]}}%s]}`
	if code, _ := post(strings.Replace(params, "%s", "", 1)); code != http.StatusNotFound {
		t.Errorf("without an index: %d", code)
	}
	s.Match = fakeMatcher{}
	code, out := post(strings.Replace(params, "%s", "", 1))
	if code != http.StatusOK || out["total"].(float64) != 2 {
		t.Fatalf("%d %v", code, out)
	}
	e := out["entry"].([]any)[0].(map[string]any)["search"].(map[string]any)
	if e["mode"] != "match" || e["score"].(float64) != 0.98 || !strings.Contains(mustJSON(e), `"valueCode":"certain"`) {
		t.Errorf("search %v", e)
	}
	_, out = post(strings.Replace(params, "%s", `,{"name":"onlyCertainMatches","valueBoolean":true}`, 1))
	if out["total"].(float64) != 1 {
		t.Errorf("only certain: %v", out)
	}
	if code, _ := post(`{"resourceType":"Parameters","parameter":[]}`); code != http.StatusBadRequest {
		t.Errorf("no resource: %d", code)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
