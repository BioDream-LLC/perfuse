package fhirserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/measure"
)

func TestEvaluateMeasure(t *testing.T) {
	srv, h := newTestServer(t)
	var bp []byte
	for _, e := range measure.Examples() {
		if e.ID == "example-bp-control" {
			bp = e.Measure
		}
	}
	if rr := do(t, h, "PUT", "/Measure/example-bp-control", string(bp)); rr.Code >= 300 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	put := func(path, body string) {
		t.Helper()
		if rr := do(t, h, "PUT", path, body); rr.Code >= 300 {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body)
		}
	}
	reading := func(id, pt string, sys, dia int) string {
		b, _ := json.Marshal(map[string]any{"resourceType": "Observation", "id": id, "status": "final",
			"subject": map[string]any{"reference": "Patient/" + pt}, "effectiveDateTime": "2026-04-01T10:00:00Z",
			"code": map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "85354-9"}}},
			"component": []any{
				map[string]any{"code": map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8480-6"}}},
					"valueQuantity": map[string]any{"value": sys, "unit": "mmHg", "system": "http://unitsofmeasure.org", "code": "mm[Hg]"}},
				map[string]any{"code": map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8462-4"}}},
					"valueQuantity": map[string]any{"value": dia, "unit": "mmHg", "system": "http://unitsofmeasure.org", "code": "mm[Hg]"}}}})
		return string(b)
	}
	htn := func(id, pt string) string {
		return `{"resourceType":"Condition","id":"` + id + `","subject":{"reference":"Patient/` + pt + `"},
		 "clinicalStatus":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/condition-clinical","code":"active"}]},
		 "code":{"coding":[{"system":"http://snomed.info/sct","code":"38341003"}]}}`
	}
	put("/Patient/a", `{"resourceType":"Patient","id":"a","gender":"female","birthDate":"1960-01-01"}`)
	put("/Patient/b", `{"resourceType":"Patient","id":"b","gender":"male","birthDate":"1958-01-01"}`)
	put("/Patient/c", `{"resourceType":"Patient","id":"c","gender":"male","birthDate":"1990-01-01"}`)
	put("/Condition/ca", htn("ca", "a"))
	put("/Condition/cb", htn("cb", "b"))
	put("/Observation/oa", reading("oa", "a", 124, 80))
	put("/Observation/ob", reading("ob", "b", 152, 96))

	rr := do(t, h, "GET", "/Measure/example-bp-control/$evaluate-measure?periodStart=2026-01-01&periodEnd=2026-12-31", nil)
	if rr.Code != 200 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	body := rr.Body.String()
	for _, want := range []string{`"type":"summary"`, `"measure":"https://perfuse.health/fhir/Measure/example-bp-control|1.0.0"`, `"value":0.5`} {
		if !strings.Contains(strings.ReplaceAll(body, " ", ""), want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	// By canonical, as subject-list.
	rr = do(t, h, "GET", "/Measure/$evaluate-measure?measure=https://perfuse.health/fhir/Measure/example-bp-control&periodStart=2026-01-01&periodEnd=2026-12-31&reportType=subject-list", nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"List"`) || !strings.Contains(rr.Body.String(), "Patient/a") {
		t.Errorf("subject-list: %d %s", rr.Code, rr.Body)
	}
	// One patient, by POST.
	rr = do(t, h, "POST", "/Measure/example-bp-control/$evaluate-measure", `{"resourceType":"Parameters","parameter":[
	 {"name":"periodStart","valueDate":"2026-01-01"},{"name":"periodEnd","valueDate":"2026-12-31"},{"name":"subject","valueString":"Patient/b"}]}`)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"individual"`) || !strings.Contains(rr.Body.String(), "Patient/b") {
		t.Errorf("individual: %d %s", rr.Code, rr.Body)
	}
	// Refusals.
	for _, c := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/Measure/nope/$evaluate-measure?periodStart=2026-01-01&periodEnd=2026-12-31", "", 404},
		{"GET", "/Measure/example-bp-control/$evaluate-measure?periodStart=2026-01-01", "", 400},
		{"GET", "/Measure/example-bp-control/$evaluate-measure?periodStart=2026-01-01&periodEnd=2026-12-31&subject=Group/g1", "", 400},
		{"GET", "/Measure/example-bp-control/$evaluate-measure?periodStart=2026-01-01&periodEnd=2026-12-31&subject=Patient/zz", "", 404},
		{"POST", "/Measure/$evaluate-measure", `{"resourceType":"Parameters","parameter":[{"name":"periodStart","valueDate":"2026-01-01"},
		 {"name":"periodEnd","valueDate":"2026-12-31"},{"name":"measure","resource":{"resourceType":"Measure","id":"q","group":[{"population":[
		 {"code":{"coding":[{"code":"initial-population"}]},"criteria":{"language":"text/cql-identifier","expression":"IP"}}]}]}}]}`, 422},
	} {
		if rr := do(t, h, c.method, c.path, c.body); rr.Code != c.code {
			t.Errorf("%s %s: %d, want %d: %s", c.method, c.path, rr.Code, c.code, rr.Body)
		}
	}
	// A patient token sees its own patient only, as an individual report.
	srv.Auth = fixedCaller{Caller{Name: "app", Scopes: []string{"patient/*.rs"}, Patient: "a"}}
	h = srv.Handler()
	rr = do(t, h, "GET", "/Measure/example-bp-control/$evaluate-measure?periodStart=2026-01-01&periodEnd=2026-12-31", nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Patient/a") || strings.Contains(rr.Body.String(), "Patient/b") {
		t.Errorf("patient token: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "GET", "/Measure/example-bp-control/$evaluate-measure?periodStart=2026-01-01&periodEnd=2026-12-31&subject=Patient/b", nil); rr.Code != 403 {
		t.Errorf("another patient: %d", rr.Code)
	}
}
