package measure

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func example(t *testing.T, id string) *Measure {
	t.Helper()
	for _, e := range Examples() {
		if e.ID == id {
			var m map[string]any
			d := json.NewDecoder(bytes.NewReader(e.Measure))
			d.UseNumber()
			_ = d.Decode(&m)
			c, err := Compile(m)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
	}
	t.Fatalf("no example %s", id)
	return nil
}

func patient(id, gender, birth string) map[string]any {
	return map[string]any{"resourceType": "Patient", "id": id, "gender": gender, "birthDate": birth}
}

func condition(code, system string) map[string]any {
	return map[string]any{"resourceType": "Condition", "code": map[string]any{"coding": []any{map[string]any{"system": system, "code": code}}},
		"clinicalStatus": map[string]any{"coding": []any{map[string]any{"code": "active"}}}}
}

func bp(when string, sys, dia int) map[string]any {
	comp := func(code string, v int) map[string]any {
		return map[string]any{"code": map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": code}}},
			"valueQuantity": map[string]any{"value": json.Number(itoa(v)), "code": "mm[Hg]"}}
	}
	return map[string]any{"resourceType": "Observation", "status": "final", "effectiveDateTime": when,
		"code":      map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "85354-9"}}},
		"component": []any{comp("8480-6", sys), comp("8462-4", dia)}}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

const htn, sct = "38341003", "http://snomed.info/sct"

func TestBloodPressureControl(t *testing.T) {
	m := example(t, "example-bp-control")
	p, err := ParsePeriod("2026-01-01", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEvaluator(m, p)
	subjects := []Subject{
		// Controlled: in IP, denominator and numerator.
		{patient("p1", "female", "1960-05-01"), []map[string]any{condition(htn, sct), bp("2026-03-01T09:00:00Z", 128, 82)}},
		// Not controlled: one reading high.
		{patient("p2", "male", "1955-02-02"), []map[string]any{condition("I10", "http://hl7.org/fhir/sid/icd-10-cm"),
			bp("2026-03-01T09:00:00Z", 128, 82), bp("2026-06-01T09:00:00Z", 150, 95)}},
		// No reading in the period: in the denominator, not the numerator.
		{patient("p3", "male", "1970-01-01"), []map[string]any{condition(htn, sct), bp("2025-06-01T09:00:00Z", 120, 80)}},
		// Excluded (end-stage renal disease).
		{patient("p4", "female", "1965-01-01"), []map[string]any{condition(htn, sct), condition("46177005", sct), bp("2026-03-01T09:00:00Z", 120, 80)}},
		// Too young; no hypertension.
		{patient("p5", "female", "2010-01-01"), []map[string]any{condition(htn, sct)}},
		{patient("p6", "male", "1960-01-01"), nil},
	}
	for _, s := range subjects {
		if err := e.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	rep := e.Report(Options{Type: SubjectList, Now: time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)})
	b, _ := json.Marshal(rep)
	got := decode(t, string(b))
	g := got["group"].([]any)[0].(map[string]any)
	count := map[string]string{}
	for _, p := range g["population"].([]any) {
		pm := p.(map[string]any)
		code := pm["code"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"].(string)
		count[code] = pm["count"].(json.Number).String()
	}
	if count[InitialPopulation] != "4" || count[Denominator] != "4" || count[DenominatorExclusion] != "1" || count[Numerator] != "1" {
		t.Fatalf("counts %v", count)
	}
	// 1 / (4 - 1)
	if s := g["measureScore"].(map[string]any)["value"].(json.Number).String(); s != "0.333333" {
		t.Errorf("score %s", s)
	}
	if rep["type"] != "subject-list" || len(rep["contained"].([]any)) == 0 {
		t.Errorf("subject list: %v", rep["type"])
	}
	strata := g["stratifier"].([]any)[0].(map[string]any)["stratum"].([]any)
	if len(strata) != 2 {
		t.Errorf("strata: %v", strata)
	}
}

func TestCohortAndIndividual(t *testing.T) {
	m := example(t, "example-seen-in-period")
	p, _ := ParsePeriod("2026-01-01", "2026-03-31")
	e := NewEvaluator(m, p)
	enc := map[string]any{"resourceType": "Encounter", "status": "finished", "period": map[string]any{"start": "2026-02-10T08:00:00Z"}}
	_ = e.Add(Subject{patient("p1", "female", "1980-01-01"), []map[string]any{enc}})
	rep := e.Report(Options{Type: Individual, Subject: "Patient/p1"})
	if rep["type"] != "individual" || rep["subject"].(map[string]any)["reference"] != "Patient/p1" {
		t.Fatalf("%v", rep)
	}
	g := rep["group"].([]any)[0].(map[string]any)
	if _, scored := g["measureScore"]; scored {
		t.Error("a cohort has no score")
	}
	if g["population"].([]any)[0].(map[string]any)["count"] != 1 {
		t.Errorf("%v", g["population"])
	}
}

func TestRefusals(t *testing.T) {
	cql := `{"resourceType":"Measure","id":"m","scoring":{"coding":[{"code":"proportion"}]},"group":[{"population":[
	 {"code":{"coding":[{"code":"initial-population"}]},"criteria":{"language":"text/cql-identifier","expression":"Initial Population"}}]}]}`
	if _, err := Compile(decode(t, cql)); err == nil || !strings.Contains(err.Error(), "CQL") {
		t.Errorf("cql: %v", err)
	}
	cv := strings.Replace(cql, `"proportion"`, `"continuous-variable"`, 1)
	cv = strings.Replace(cv, `text/cql-identifier","expression":"Initial Population"`, `text/fhirpath","expression":"true"`, 1)
	if _, err := Compile(decode(t, cv)); err == nil {
		t.Error("continuous variable")
	}
	noNum := strings.Replace(cql, `text/cql-identifier","expression":"Initial Population"`, `text/fhirpath","expression":"true"`, 1)
	if _, err := Compile(decode(t, noNum)); err == nil || !strings.Contains(err.Error(), "numerator") {
		t.Errorf("proportion without numerator: %v", err)
	}
	if _, err := ParsePeriod("2026-12-31", "2026-01-01"); err == nil {
		t.Error("backwards period")
	}
}

// PERFUSE_MEASURE_OUT=DIR writes a summary, an individual and a subject-list report, for the HL7 validator.
func TestWriteReportsForTheValidator(t *testing.T) {
	dir := os.Getenv("PERFUSE_MEASURE_OUT")
	if dir == "" {
		t.Skip("set PERFUSE_MEASURE_OUT")
	}
	m := example(t, "example-bp-control")
	p, _ := ParsePeriod("2026-01-01", "2026-12-31")
	e := NewEvaluator(m, p)
	_ = e.Add(Subject{patient("p1", "female", "1960-05-01"), []map[string]any{condition(htn, sct), bp("2026-03-01T09:00:00Z", 128, 82)}})
	_ = e.Add(Subject{patient("p2", "male", "1955-02-02"), []map[string]any{condition(htn, sct), bp("2026-03-01T09:00:00Z", 150, 95)}})
	for _, rt := range []ReportType{Summary, Individual, SubjectList} {
		rep := e.Report(Options{Type: rt, Subject: "Patient/p1"})
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "report-"+string(rt)+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, x := range Examples() {
		_ = os.WriteFile(filepath.Join(dir, "measure-"+x.ID+".json"), x.Measure, 0o644)
	}
}
