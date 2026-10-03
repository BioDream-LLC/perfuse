package fhirserver

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestAQuestionnairePackageIsBuiltFromTheOrdersCoverageInformation(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	q, _ := os.ReadFile("../../examples/crd/questionnaire-home-oxygen.json")
	if rec := payerDo(t, h, "PUT", "/Questionnaire/home-oxygen", string(q), nil); rec.Code >= 300 {
		t.Fatal(rec.Body)
	}
	order := `{"resourceType":"DeviceRequest","status":"draft","intent":"original-order","subject":{"reference":"Patient/p"},
	"codeCodeableConcept":{"text":"oxygen"},"extension":[{"url":"http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information",
	"extension":[{"url":"questionnaire","valueCanonical":"https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen|1.0.0"}]}]}`
	rec := payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[{"name":"order","resource":`+order+`}]}`, nil)
	body := strings.ReplaceAll(rec.Body.String(), `": `, `":`)
	if rec.Code != http.StatusOK || !strings.Contains(body, `"name":"PackageBundle"`) || !strings.Contains(body, "DTR-QPackageBundle") ||
		!strings.Contains(body, "Home oxygen therapy: documentation") ||
		!strings.Contains(body, `"fullUrl":"https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen"`) ||
		!strings.Contains(body, "Oxygen saturation at rest") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	rec = payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package",
		`{"resourceType":"Parameters","parameter":[{"name":"questionnaire","valueCanonical":"https://example.org/Questionnaire/none"}]}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not-found") {
		t.Errorf("an unknown questionnaire: %d %s", rec.Code, rec.Body.String())
	}
}
