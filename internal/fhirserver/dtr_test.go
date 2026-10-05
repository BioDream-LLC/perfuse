package fhirserver

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

const coverageParam = `{"name":"coverage","resource":{"resourceType":"Coverage","id":"cov1","status":"active",
"beneficiary":{"reference":"Patient/p"},"payor":[{"reference":"Organization/plan"}]}}`

func dtrOrder(questionnaire string) string {
	return `{"resourceType":"DeviceRequest","id":"o2","status":"draft","intent":"original-order","subject":{"reference":"Patient/p"},
	"codeCodeableConcept":{"text":"oxygen"},"extension":[{"url":"http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information",
	"extension":[{"url":"pa-needed","valueCode":"auth-needed"},{"url":"questionnaire","valueCanonical":"` + questionnaire + `"}]}]}`
}

func packageOf(t *testing.T, body string) (bundles []map[string]any, outcome map[string]any) {
	t.Helper()
	var out struct {
		ResourceType string `json:"resourceType"`
		Parameter    []struct {
			Name     string         `json:"name"`
			Resource map[string]any `json:"resource"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.ResourceType != "Parameters" {
		t.Fatalf("not a Parameters: %s", body)
	}
	for _, p := range out.Parameter {
		switch p.Name {
		case "packagebundle":
			bundles = append(bundles, p.Resource)
		case "outcome":
			outcome = p.Resource
		default:
			t.Errorf("parameter %q is not one DTR 2.2.0 defines (packagebundle, outcome)", p.Name)
		}
	}
	return bundles, outcome
}

func entriesOf(b map[string]any) map[string][]map[string]any {
	out := map[string][]map[string]any{}
	for _, e := range b["entry"].([]any) {
		r := e.(map[string]any)["resource"].(map[string]any)
		out[r["resourceType"].(string)] = append(out[r["resourceType"].(string)], r)
	}
	return out
}

func TestAQuestionnairePackageIsBuiltFromTheOrdersCoverageInformation(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	q, _ := os.ReadFile("../../examples/crd/questionnaire-home-oxygen.json")
	if rec := payerDo(t, h, "PUT", "/Questionnaire/home-oxygen", string(q), nil); rec.Code >= 300 {
		t.Fatal(rec.Body)
	}
	rec := payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[`+coverageParam+`,{"name":"order","resource":`+
			dtrOrder("https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen|1.0.0")+`}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	bundles, _ := packageOf(t, rec.Body.String())
	if len(bundles) != 1 {
		t.Fatalf("want one package bundle, got %d", len(bundles))
	}
	got := entriesOf(bundles[0])
	if len(got["Questionnaire"]) != 1 || got["Questionnaire"][0]["title"] != "Home oxygen therapy: documentation for prior authorization" {
		t.Errorf("questionnaire: %v", got["Questionnaire"])
	}
	// DTR 2.2.0 requires exactly one QuestionnaireResponse in each package.
	if len(got["QuestionnaireResponse"]) != 1 {
		t.Fatalf("want one QuestionnaireResponse, got %d", len(got["QuestionnaireResponse"]))
	}
	qr, _ := json.Marshal(got["QuestionnaireResponse"][0])
	for _, want := range []string{`"status":"in-progress"`, `"subject":{"reference":"Patient/p"}`,
		`"questionnaire":"https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen|1.0.0"`,
		`qr-coverage","valueReference":{"reference":"Coverage/cov1"}`, `"code":"withpa"`,
		`qr-context","valueReference":{"reference":"DeviceRequest/o2"}`} {
		if !strings.Contains(string(qr), want) {
			t.Errorf("the QuestionnaireResponse lacks %s:\n%s", want, qr)
		}
	}

	rec = payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[`+coverageParam+`,{"name":"questionnaire","valueCanonical":"https://example.org/Questionnaire/none"}]}`, nil)
	if _, oo := packageOf(t, rec.Body.String()); rec.Code != http.StatusOK || !strings.Contains(toJSON(oo), "not-found") {
		t.Errorf("an unknown questionnaire: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPackageCarriesLibraryDependenciesAndAnswerValueSets(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	for path, body := range map[string]string{
		"/Questionnaire/q": `{"resourceType":"Questionnaire","id":"q","url":"https://payer.example/Questionnaire/q","status":"active",
			"extension":[{"url":"http://hl7.org/fhir/StructureDefinition/cqf-library","valueCanonical":"https://payer.example/Library/Prepop"}],
			"item":[{"linkId":"g","type":"group","item":[{"linkId":"1","type":"choice","answerValueSet":"https://payer.example/ValueSet/devices"}]}]}`,
		"/Library/prepop": `{"resourceType":"Library","id":"prepop","url":"https://payer.example/Library/Prepop","status":"active",
			"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/library-type","code":"logic-library"}]},
			"relatedArtifact":[{"type":"depends-on","resource":"http://fhir.org/guides/cqf/common/Library/FHIRHelpers|4.0.1"}]}`,
		"/Library/fhirhelpers": `{"resourceType":"Library","id":"fhirhelpers","url":"http://fhir.org/guides/cqf/common/Library/FHIRHelpers","version":"4.0.1",
			"status":"active","type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/library-type","code":"logic-library"}]}}`,
	} {
		if rec := payerDo(t, h, "PUT", path, body, nil); rec.Code >= 300 {
			t.Fatal(path, rec.Body)
		}
	}
	rec := payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[`+coverageParam+`,{"name":"questionnaire","valueCanonical":"https://payer.example/Questionnaire/q"}]}`, nil)
	bundles, oo := packageOf(t, rec.Body.String())
	if len(bundles) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := entriesOf(bundles[0])
	if len(got["Library"]) != 2 {
		t.Errorf("the Library and the FHIRHelpers it depends on should both be packaged; got %d (%v)", len(got["Library"]), oo)
	}
	// A fullUrl may equal the canonical url only when that url ends in Type/id; .../Library/Prepop does not end in Library/prepop.
	for _, e := range bundles[0]["entry"].([]any) {
		em := e.(map[string]any)
		res := em["resource"].(map[string]any)
		if res["id"] == "prepop" && (em["fullUrl"] == res["url"] || !strings.HasSuffix(em["fullUrl"].(string), "Library/prepop")) {
			t.Errorf("the Library's fullUrl %v contradicts its id", em["fullUrl"])
		}
	}
	// One named by URL but not loaded is reported, so the app knows to expand it.
	if !strings.Contains(toJSON(oo), "https://payer.example/ValueSet/devices, which is not loaded") {
		t.Errorf("an answer value set the server does not hold is not reported: %v", oo)
	}

	// Once the payer loads it, it travels in the package.
	if rec := payerDo(t, h, "PUT", "/ValueSet/devices", `{"resourceType":"ValueSet","id":"devices","url":"https://payer.example/ValueSet/devices",
		"status":"active","compose":{"include":[{"system":"http://snomed.info/sct","concept":[{"code":"426160001","display":"Oxygen concentrator"}]}]}}`, nil); rec.Code >= 300 {
		t.Fatal(rec.Body)
	}
	rec = payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[`+coverageParam+`,{"name":"questionnaire","valueCanonical":"https://payer.example/Questionnaire/q"}]}`, nil)
	bundles, oo = packageOf(t, rec.Body.String())
	if len(bundles) != 1 || len(entriesOf(bundles[0])["ValueSet"]) != 1 {
		t.Errorf("a loaded answer value set is not packaged: %s", rec.Body)
	}
	if strings.Contains(toJSON(oo), "ValueSet/devices") {
		t.Errorf("a loaded value set is still reported missing: %v", oo)
	}
}

func TestAPackageCanBeAskedForByCRDsAssertionID(t *testing.T) {
	srv, _ := payerFixture(t)
	q, _ := os.ReadFile("../../examples/crd/questionnaire-home-oxygen.json")
	h := srv.Handler()
	payerDo(t, h, "PUT", "/Questionnaire/home-oxygen", string(q), nil)
	srv.DTRContext = func(id string) []string {
		if id == "assert-1" {
			return []string{"https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen"}
		}
		return nil
	}
	h = srv.Handler()
	rec := payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[`+coverageParam+`,{"name":"context","valueString":"assert-1"}]}`, nil)
	if bundles, _ := packageOf(t, rec.Body.String()); len(bundles) != 1 {
		t.Errorf("by context: %d %s", rec.Code, rec.Body)
	}
	// An unknown context still answers 200 with an explanation (oper-8), not an empty package or an error.
	rec = payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[`+coverageParam+`,{"name":"context","valueString":"nope"}]}`, nil)
	if _, oo := packageOf(t, rec.Body.String()); rec.Code != http.StatusOK || !strings.Contains(toJSON(oo), "not a coverage assertion") {
		t.Errorf("unknown context: %d %s", rec.Code, rec.Body)
	}
}

func TestQuestionnaireErrorsAreAcceptedAndPaired(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	oo := `{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"processing","diagnostics":"CQL failed"}]}`
	rec := payerDo(t, h, "POST", "/Questionnaire/$log-questionnaire-errors", `{"resourceType":"Parameters","parameter":[
		{"name":"questionnaire","valueCanonical":"https://payer.example/Questionnaire/q|1"},{"name":"operationOutcome","resource":`+oo+`}]}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "recorded 1 issue") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	rec = payerDo(t, h, "POST", "/Questionnaire/$log-questionnaire-errors", `{"resourceType":"Parameters","parameter":[
		{"name":"questionnaire","valueCanonical":"a"},{"name":"questionnaire","valueCanonical":"b"},{"name":"operationOutcome","resource":`+oo+`}]}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unequal counts cannot be paired: %d %s", rec.Code, rec.Body)
	}
	if rec := payerDo(t, h, "POST", "/Questionnaire/$next-question", `{"resourceType":"Parameters"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("$next-question with no questionnaire-response: %d", rec.Code)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
