package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
)

const pasRequest = `{"resourceType":"Bundle","type":"collection","entry":[
 {"fullUrl":"http://p.example/fhir/Claim/c1","resource":{"resourceType":"Claim","id":"c1","status":"active","use":"preauthorization",
  "identifier":[{"system":"http://p.example/trace","value":"TR-1"}],
  "type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"professional"}]},
  "patient":{"reference":"Patient/m"},"insurer":{"reference":"Organization/plan"},"provider":{"reference":"Organization/clinic"},
  "created":"2026-10-05","priority":{"coding":[{"code":"normal"}]},
  "item":[{"sequence":1,"productOrService":{"coding":[{"system":"https://codesystem.x12.org/005010/1365","code":"2"}]},"quantity":{"value":10}}]}},
 {"fullUrl":"http://p.example/fhir/Patient/m","resource":{"resourceType":"Patient","id":"m","name":[{"family":"Member","given":["Pat"]}],
  "identifier":[{"system":"http://plan.example/member","value":"M1"}]}},
 {"fullUrl":"http://p.example/fhir/Organization/plan","resource":{"resourceType":"Organization","id":"plan","name":"Plan"}},
 {"fullUrl":"http://p.example/fhir/Organization/clinic","resource":{"resourceType":"Organization","id":"clinic","name":"Clinic",
  "identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"1234567893"}]}}]}`

func TestTheReviewerQueueShowsAndDecidesPendedRequests(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("viewer", http.MethodGet, "/api/pas/cases", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("with PAS off the queue says so: %d", rec.Code)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	fs, err := fhirserver.NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	fsrv := fhirserver.NewServer(fs, "http://example.test/fhir", slog.New(slog.NewTextHandler(io.Discard, nil)))
	fsrv.Auth = fhirserver.OpenAuth{}
	fsrv.PAS = &fhirserver.PAS{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/Claim/$submit", bytes.NewBufferString(pasRequest))
	req.Header.Set("Content-Type", "application/fhir+json")
	fsrv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}
	h.server.PAS = fsrv
	h.handler = h.server.Handler()

	rec = h.do("viewer", http.MethodGet, "/api/pas/cases", nil)
	var list struct{ Cases []fhirserver.PASCase }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Cases) != 1 || list.Cases[0].Member != "Pat Member" || list.Cases[0].Items[0].Code != "A4" {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body)
	}
	id := list.Cases[0].ID
	if rec := h.do("viewer", http.MethodGet, "/api/pas/cases/"+id, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"request"`) {
		t.Fatalf("case: %d", rec.Code)
	}
	if rec := h.do("viewer", http.MethodPost, "/api/pas/cases/"+id+"/decide", map[string]any{"decision": "approve"}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer decided: %d", rec.Code)
	}
	if rec := h.do("editor", http.MethodPost, "/api/pas/cases/"+id+"/decide", map[string]any{"decision": "modify"}); rec.Code != http.StatusBadRequest {
		t.Errorf("modify with nothing modified: %d", rec.Code)
	}
	rec = h.do("editor", http.MethodPost, "/api/pas/cases/"+id+"/decide", map[string]any{"decision": "modify", "quantity": 4, "reason": "Four first."})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"A6"`) {
		t.Fatalf("decide: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("editor", http.MethodPost, "/api/pas/cases/"+id+"/decide", map[string]any{"decision": "approve"}); rec.Code != http.StatusConflict {
		t.Errorf("deciding twice: %d", rec.Code)
	}
	rec = h.do("viewer", http.MethodGet, "/api/pas/cases", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Cases) != 0 {
		t.Errorf("the decided request is still in the queue")
	}
	if rec = h.do("viewer", http.MethodGet, "/api/pas/cases?all=1", nil); !strings.Contains(rec.Body.String(), id) {
		t.Errorf("all=1 lists decided requests too")
	}
}
