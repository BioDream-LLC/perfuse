package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/measure"
)

const measurePatients = `{"resourceType":"Patient","id":"m1","gender":"female","birthDate":"1980-01-01"}
{"resourceType":"Patient","id":"m2","gender":"male","birthDate":"1975-01-01"}
{"resourceType":"Encounter","id":"e1","status":"finished","subject":{"reference":"Patient/m1"},"period":{"start":"2026-02-01T09:00:00Z"}}
{"resourceType":"Encounter","id":"e2","status":"finished","subject":{"reference":"Patient/m2"},"period":{"start":"2025-02-01T09:00:00Z"}}`

func exampleMeasure(t *testing.T, id string) json.RawMessage {
	for _, e := range measure.Examples() {
		if e.ID == id {
			return e.Measure
		}
	}
	t.Fatalf("no example %s", id)
	return nil
}

func TestMeasures(t *testing.T) {
	h := newHarness(t)
	cohort := exampleMeasure(t, "example-seen-in-period")

	// Without a FHIR store: examples listed, pasted resources evaluated, the store refused.
	rr := h.do("viewer", "GET", "/api/measures", nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"fhir":false`) || !strings.Contains(rr.Body.String(), "example-bp-control") {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	rr = h.do("viewer", "POST", "/api/measures/evaluate", map[string]any{"measure": cohort, "periodStart": "2026-01-01",
		"periodEnd": "2026-12-31", "resources": measurePatients, "reportType": "subject-list"})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Patient/m1") || strings.Contains(rr.Body.String(), `"reference":"Patient/m2"`) {
		t.Fatalf("pasted: %d %s", rr.Code, rr.Body)
	}
	if rr := h.do("viewer", "POST", "/api/measures/evaluate", map[string]any{"measure": cohort, "periodStart": "2026-01-01", "periodEnd": "2026-12-31"}); rr.Code != 409 {
		t.Errorf("no store: %d", rr.Code)
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
	fsrv.MeasureReporter = "Test clinic"
	h.server.Measures = fsrv
	h.handler = h.server.Handler()
	for _, line := range strings.Split(measurePatients, "\n") {
		res, err := fhir.UnmarshalResource([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Put(t.Context(), res); err != nil {
			t.Fatal(err)
		}
	}

	if rr := h.do("viewer", "PUT", "/api/measures", map[string]any{"measure": cohort}); rr.Code != 403 {
		t.Errorf("a viewer saved a measure: %d", rr.Code)
	}
	cql := `{"resourceType":"Measure","id":"cql","group":[{"population":[{"code":{"coding":[{"code":"initial-population"}]},
	 "criteria":{"language":"text/cql-identifier","expression":"Initial Population"}}]}]}`
	if rr := h.do("editor", "PUT", "/api/measures", map[string]any{"measure": json.RawMessage(cql)}); rr.Code != 422 || !strings.Contains(rr.Body.String(), "CQL") {
		t.Errorf("a CQL measure: %d %s", rr.Code, rr.Body)
	}
	if rr := h.do("editor", "PUT", "/api/measures", map[string]any{"measure": cohort}); rr.Code != 200 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	rr = h.do("viewer", "GET", "/api/measures", nil)
	if !strings.Contains(rr.Body.String(), `"fhir":true`) || !strings.Contains(rr.Body.String(), `"scoring":"cohort"`) {
		t.Errorf("list: %s", rr.Body)
	}
	if rr := h.do("viewer", "GET", "/api/measures/example-seen-in-period", nil); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"group"`) {
		t.Errorf("get: %d %s", rr.Code, rr.Body)
	}
	rr = h.do("viewer", "POST", "/api/measures/evaluate", map[string]any{"id": "example-seen-in-period", "periodStart": "2026-01-01", "periodEnd": "2026-12-31"})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"count":1`) || !strings.Contains(rr.Body.String(), "Test clinic") {
		t.Errorf("store: %d %s", rr.Code, rr.Body)
	}
	if rr := h.do("viewer", "POST", "/api/measures/evaluate", map[string]any{"id": "nope", "periodStart": "2026-01-01", "periodEnd": "2026-12-31"}); rr.Code != 404 {
		t.Errorf("unknown measure: %d", rr.Code)
	}
	if rr := h.do("viewer", "POST", "/api/measures/evaluate", map[string]any{"id": "example-seen-in-period", "periodStart": "2026-12-31", "periodEnd": "2026-01-01"}); rr.Code != 400 {
		t.Errorf("backwards period: %d", rr.Code)
	}
	if rr := h.do("editor", "DELETE", "/api/measures/example-seen-in-period", nil); rr.Code != 200 {
		t.Errorf("delete: %d %s", rr.Code, rr.Body)
	}
	if rr := h.do("editor", "DELETE", "/api/measures/example-seen-in-period", nil); rr.Code != 404 {
		t.Errorf("delete twice: %d", rr.Code)
	}
}
