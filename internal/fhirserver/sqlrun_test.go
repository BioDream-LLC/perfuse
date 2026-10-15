package fhirserver

import (
	"encoding/json"
	"strings"
	"testing"
)

const sqlView = `{"resourceType":"ViewDefinition","name":"obs","resource":"Observation","select":[{"column":[
 {"name":"id","path":"getResourceKey()"},{"name":"patient","path":"subject.getReferenceKey(Patient)"},
 {"name":"code","path":"code.coding.code.first()"},{"name":"value","path":"value.ofType(Quantity).value","type":"decimal"}]}]}`

func runParams(view string, extra ...string) string {
	parts := []string{`{"name":"subjectResource","resource":` + view + `}`}
	parts = append(parts, extra...)
	return `{"resourceType":"Parameters","parameter":[` + strings.Join(parts, ",") + `]}`
}

func TestSQLRunOverStoredResources(t *testing.T) {
	srv, h := newTestServer(t)
	for _, p := range []string{"pt1", "pt2"} {
		if rr := do(t, h, "PUT", "/Patient/"+p, `{"resourceType":"Patient","id":"`+p+`"}`); rr.Code >= 300 {
			t.Fatalf("%d %s", rr.Code, rr.Body)
		}
	}
	obs := func(id, pt string, v float64) string {
		b, _ := json.Marshal(map[string]any{"resourceType": "Observation", "id": id, "status": "final",
			"code":    map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "29463-7"}}},
			"subject": map[string]any{"reference": "Patient/" + pt}, "valueQuantity": map[string]any{"value": v, "unit": "kg"}})
		return string(b)
	}
	for _, o := range [][3]any{{"o1", "pt1", 61.5}, {"o2", "pt1", 62.0}, {"o3", "pt2", 80.25}} {
		if rr := do(t, h, "PUT", "/Observation/"+o[0].(string), obs(o[0].(string), o[1].(string), o[2].(float64))); rr.Code >= 300 {
			t.Fatalf("%d %s", rr.Code, rr.Body)
		}
	}
	rr := do(t, h, "POST", "/$sql-run", runParams(sqlView, `{"name":"_format","valueCode":"csv"}`))
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "text/csv" {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if got := rr.Body.String(); got != "id,patient,code,value\no1,pt1,29463-7,61.5\no2,pt1,29463-7,62\no3,pt2,29463-7,80.25\n" {
		t.Errorf("csv:\n%s", got)
	}
	rr = do(t, h, "POST", "/$sql-run", runParams(sqlView, `{"name":"patient","valueReference":{"reference":"Patient/pt2"}}`))
	if rr.Code != 200 || strings.Count(rr.Body.String(), "\n") != 1 || !strings.Contains(rr.Body.String(), `"o3"`) {
		t.Errorf("patient filter: %d %s", rr.Code, rr.Body)
	}
	rr = do(t, h, "POST", "/$sql-run", runParams(sqlView, `{"name":"_limit","valueInteger":2}`, `{"name":"_format","valueCode":"json"}`))
	var arr []map[string]any
	if json.Unmarshal(rr.Body.Bytes(), &arr); len(arr) != 2 {
		t.Errorf("_limit: %s", rr.Body)
	}
	// Resources sent with the request, a Bundle unwrapped, instead of the server's.
	rr = do(t, h, "POST", "/$sql-run", runParams(sqlView,
		`{"name":"resource","resource":{"resourceType":"Bundle","type":"collection","entry":[{"resource":`+obs("x1", "pt9", 1)+`}]}}`))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"x1"`) || strings.Contains(rr.Body.String(), `"o1"`) {
		t.Errorf("inline resources: %d %s", rr.Code, rr.Body)
	}
	// Parquet.
	rr = do(t, h, "POST", "/$sql-run", runParams(sqlView, `{"name":"_format","valueCode":"parquet"}`))
	if rr.Code != 200 || !strings.HasPrefix(rr.Body.String(), "PAR1") {
		t.Errorf("parquet: %d", rr.Code)
	}

	// Refusals.
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"resourceType":"Parameters","parameter":[]}`, 400},
		{runParams(sqlView, `{"name":"subjectCanonical","valueCanonical":"http://x/vd"}`), 404},
		{runParams(`{"resourceType":"ViewDefinition","resource":"Patient","select":[{"column":[{"name":"x","path":"@@"}]}]}`), 422},
		{runParams(`{"resourceType":"Library","id":"q"}`), 422},
		{runParams(sqlView, `{"name":"_format","valueCode":"xlsx"}`), 400},
		{runParams(sqlView, `{"name":"parameters","resource":{"resourceType":"Parameters"}}`), 400},
		{`not json`, 400},
	} {
		if rr := do(t, h, "POST", "/$sql-run", c.body); rr.Code != c.code {
			t.Errorf("%s: %d, want %d: %s", c.body[:min(len(c.body), 80)], rr.Code, c.code, rr.Body)
		}
	}

	// A token bound to pt1 sees pt1's rows only, and may not read a type its scopes do not allow.
	srv.Auth = fixedCaller{Caller{Name: "app", Scopes: []string{"patient/Observation.rs"}, Patient: "pt1"}}
	h = srv.Handler()
	rr = do(t, h, "POST", "/$sql-run", runParams(sqlView))
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "pt2") || strings.Count(rr.Body.String(), "\n") != 2 {
		t.Errorf("patient token: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, h, "POST", "/$sql-run", runParams(sqlView, `{"name":"patient","valueReference":{"reference":"Patient/pt2"}}`)); rr.Code != 403 {
		t.Errorf("another patient: %d", rr.Code)
	}
	pv := strings.Replace(sqlView, `"resource":"Observation"`, `"resource":"Patient"`, 1)
	if rr := do(t, h, "POST", "/$sql-run", runParams(pv)); rr.Code != 403 {
		t.Errorf("a type outside the scopes: %d %s", rr.Code, rr.Body)
	}
}
