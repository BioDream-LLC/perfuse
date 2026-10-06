package fhirserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const vsBase = "https://payer.example/ValueSet/"

func loadValueSets(t *testing.T, h http.Handler, sets map[string]string) {
	t.Helper()
	for id, compose := range sets {
		body := `{"resourceType":"ValueSet","id":"` + id + `","url":"` + vsBase + id + `","status":"active",` + compose + `}`
		if rec := payerDo(t, h, "PUT", "/ValueSet/"+id, body, nil); rec.Code >= 300 {
			t.Fatalf("PUT %s: %d %s", id, rec.Code, rec.Body)
		}
	}
}

func expansionCodes(t *testing.T, body string) (codes []string, total float64) {
	t.Helper()
	var vs map[string]any
	if err := json.Unmarshal([]byte(body), &vs); err != nil {
		t.Fatal(err)
	}
	exp, _ := vs["expansion"].(map[string]any)
	if exp == nil || exp["timestamp"] == nil {
		t.Fatalf("no expansion with a timestamp (FHIR requires one): %s", body)
	}
	for _, c := range listOf(exp["contains"]) {
		codes = append(codes, c.(map[string]any)["code"].(string))
	}
	total, _ = exp["total"].(float64)
	return codes, total
}

const snomed = `"system":"http://snomed.info/sct"`

func TestAStoredValueSetExpandsItsListedIncludedAndExcludedConcepts(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	loadValueSets(t, h, map[string]string{
		"oxygen": `"compose":{"include":[{` + snomed + `,"concept":[{"code":"426160001","display":"Oxygen concentrator"},
			{"code":"706172005","display":"Portable oxygen cylinder"}]}]}`,
		"dme": `"compose":{"include":[{"valueSet":["` + vsBase + `oxygen"]},{` + snomed + `,"concept":[{"code":"58938008","display":"Wheelchair"}]}],
			"exclude":[{` + snomed + `,"concept":[{"code":"706172005"}]}]}`,
		"expanded": `"expansion":{"timestamp":"2026-01-01T00:00:00Z","contains":[{"abstract":true,"display":"group",
			"contains":[{` + snomed + `,"code":"1","display":"one"}]}]}`,
	})

	rec := payerDo(t, h, "GET", "/ValueSet/$expand?url="+vsBase+"dme", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	codes, total := expansionCodes(t, rec.Body.String())
	if strings.Join(codes, ",") != "426160001,58938008" || total != 2 {
		t.Errorf("include by value set, add a concept, exclude one: got %v (total %v)", codes, total)
	}

	rec = payerDo(t, h, "GET", "/ValueSet/$expand?url="+vsBase+"oxygen&filter=portable", "", nil)
	if codes, _ := expansionCodes(t, rec.Body.String()); strings.Join(codes, ",") != "706172005" {
		t.Errorf("filter on the display: %v", codes)
	}
	rec = payerDo(t, h, "GET", "/ValueSet/$expand?url="+vsBase+"oxygen&count=1", "", nil)
	if codes, total := expansionCodes(t, rec.Body.String()); len(codes) != 1 || total != 2 {
		t.Errorf("count bounds the list, not the total: %v %v", codes, total)
	}
	rec = payerDo(t, h, "GET", "/ValueSet/$expand?url="+vsBase+"expanded", "", nil)
	if codes, _ := expansionCodes(t, rec.Body.String()); strings.Join(codes, ",") != "1" {
		t.Errorf("an existing expansion, abstract groupers left out: %v", codes)
	}
}

func TestAValueSetNeedingAWholeCodeSystemIsRefusedNotTruncated(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	loadValueSets(t, h, map[string]string{
		"filtered": `"compose":{"include":[{"system":"http://loinc.org","filter":[{"property":"CLASS","op":"=","value":"CHEM"}]}]}`,
		"all":      `"compose":{"include":[{"system":"http://loinc.org"}]}`,
		"loop":     `"compose":{"include":[{"valueSet":["` + vsBase + `loop"]}]}`,
		"dangling": `"compose":{"include":[{"valueSet":["` + vsBase + `nowhere"]}]}`,
	})
	for id, want := range map[string]string{
		"filtered": "filters http://loinc.org", "all": "every code in http://loinc.org",
		"loop": "cycle", "dangling": "not loaded",
	} {
		rec := payerDo(t, h, "GET", "/ValueSet/$expand?url="+vsBase+id, "", nil)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: want 422 saying %q, got %d %s", id, want, rec.Code, rec.Body)
		}
	}
	if rec := payerDo(t, h, "GET", "/ValueSet/$expand?url="+vsBase+"absent", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown value set: %d", rec.Code)
	}
}

func TestACodeIsValidatedAgainstAStoredValueSet(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	loadValueSets(t, h, map[string]string{"oxygen": `"compose":{"include":[{` + snomed + `,"concept":[{"code":"426160001","display":"Oxygen concentrator"}]}]}`})
	for q, want := range map[string]string{
		"&code=426160001": `"valueBoolean": true`,
		"&code=426160001&system=http://snomed.info/sct": `"valueBoolean": true`,
		"&code=426160001&system=http://loinc.org":       `"valueBoolean": false`,
		"&code=1": `"valueBoolean": false`,
	} {
		rec := payerDo(t, h, "GET", "/ValueSet/$validate-code?url="+vsBase+"oxygen"+q, "", nil)
		if rec.Code != http.StatusOK || !strings.Contains(strings.ReplaceAll(rec.Body.String(), " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("%s: want %s, got %d %s", q, want, rec.Code, rec.Body)
		}
	}
}

func TestAValueSetCannotBeWrittenIntoTheTableNamespace(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	body := `{"resourceType":"ValueSet","id":"x","url":"` + ConceptMapBaseURL + `sex:source","status":"active"}`
	rec := payerDo(t, h, "PUT", "/ValueSet/x", body, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "codeset.yaml") {
		t.Errorf("a write into the table namespace: %d %s", rec.Code, rec.Body)
	}
	tx := `{"resourceType":"Bundle","type":"transaction","entry":[{"resource":` + body + `,"request":{"method":"PUT","url":"ValueSet/x"}}]}`
	if rec := payerDo(t, h, "POST", "/", tx, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("the same in a transaction: %d %s", rec.Code, rec.Body)
	}
	if rec := payerDo(t, h, "GET", "/ValueSet?url="+vsBase+"none", "", nil); rec.Code != http.StatusOK {
		t.Errorf("searching stored value sets: %d %s", rec.Code, rec.Body)
	}
}

func TestExpandReadsAParametersBody(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	loadValueSets(t, h, map[string]string{"oxygen": `"compose":{"include":[{` + snomed + `,"concept":[{"code":"426160001"},{"code":"706172005"}]}]}`})
	rec := payerDo(t, h, "POST", "/ValueSet/$expand",
		`{"resourceType":"Parameters","parameter":[{"name":"url","valueUri":"`+vsBase+`oxygen"},{"name":"count","valueInteger":1}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if codes, total := expansionCodes(t, rec.Body.String()); len(codes) != 1 || total != 2 {
		t.Errorf("%v %v", codes, total)
	}
}
