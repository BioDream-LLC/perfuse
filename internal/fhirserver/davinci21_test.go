package fhirserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/davinci"
)

// A DTR client that claims 2.1.0 profiles gets DTR 2.1.0's package: the parameter named PackageBundle, profiles at 2.1.0, and no
// QuestionnaireResponse (optional in 2.1.0, and its 2.1.0 profile wants what only a filled-in response has). The same call
// claiming nothing gets 2.2.0's.
func TestAQuestionnairePackageIsAnsweredInTheClientsDTRRelease(t *testing.T) {
	srv, _ := payerFixture(t)
	h := srv.Handler()
	q, _ := os.ReadFile("../../examples/crd/questionnaire-home-oxygen.json")
	if rec := payerDo(t, h, "PUT", "/Questionnaire/home-oxygen", string(q), nil); rec.Code >= 300 {
		t.Fatal(rec.Body)
	}
	call := func(profile string) map[string]any {
		meta := ""
		if profile != "" {
			meta = `"meta":{"profile":["http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/dtr-qpackage-input-parameters|` + profile + `"]},`
		}
		rec := payerDo(t, h, "POST", "/Questionnaire/$questionnaire-package", `{"resourceType":"Parameters",`+meta+`"parameter":[`+
			coverageParam+`,{"name":"order","resource":`+dtrOrder("https://www.springfield-health-plan.example/fhir/Questionnaire/home-oxygen|1.0.0")+`}]}`, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	old := call("2.1.0")
	p := asMapAny(asSliceAny(old["parameter"])[0])
	if p["name"] != "PackageBundle" {
		t.Errorf("DTR 2.1.0 names it PackageBundle: %v", p["name"])
	}
	b := asMapAny(p["resource"])
	if !strings.Contains(toJSON(b["meta"]), "DTR-QPackageBundle|2.1.0") || len(entriesOf(b)["QuestionnaireResponse"]) != 0 {
		t.Errorf("2.1.0 package: %s", toJSON(b))
	}
	now := call("")
	p = asMapAny(asSliceAny(now["parameter"])[0])
	b = asMapAny(p["resource"])
	if p["name"] != "packagebundle" || !strings.Contains(toJSON(b["meta"]), "|2.2.0") || len(entriesOf(b)["QuestionnaireResponse"]) != 1 {
		t.Errorf("2.2.0 package: %s", toJSON(p))
	}
}

// The release comes from the request first, then the SMART client's registration, then the server's setting.
func TestTheDaVinciReleaseIsTheRequestsThenTheClientsThenTheServers(t *testing.T) {
	s := &Server{DaVinciVersion: davinci.V21, ClientDaVinci: func(id string) string {
		if id == "old-ehr" {
			return "2.1"
		}
		return ""
	}}
	req := func(client string) *http.Request {
		r := httptest.NewRequest("POST", "/x", nil)
		if client != "" {
			r = r.WithContext(withCaller(context.Background(), &Caller{ClientID: client}))
		}
		return r
	}
	claim22 := map[string]any{"meta": map[string]any{"profile": []any{pasBase + "profile-pas-request-bundle|2.2.1"}}}
	if got := s.daVinciVersion(req("old-ehr"), claim22); got != davinci.V22 {
		t.Errorf("a request claiming 2.2.1 is answered in 2.2, whatever its client is registered as: %s", got)
	}
	if got := s.daVinciVersion(req("old-ehr"), nil); got != davinci.V21 {
		t.Errorf("a client registered as 2.1: %s", got)
	}
	s.DaVinciVersion = ""
	if got := s.daVinciVersion(req("new-ehr"), nil); got != davinci.Latest {
		t.Errorf("nothing said anywhere: %s", got)
	}
	if got := s.daVinciVersion(req("old-ehr"), nil); got != davinci.V21 {
		t.Errorf("the registration with no server setting: %s", got)
	}
}

// A PAS 2.1.0 client: its $submit answer carries 2.1.0's profiles and none of the extensions 2.1.0 does not define; a pended
// request's Task numbers its lines with paLineNumber and its CommunicationRequest on the payload; $inquire answers with one Bundle.
func TestAPASAnswerIsRewrittenForA21Client(t *testing.T) {
	f := newPASFixture(t)
	req21 := strings.Replace(pasRequestJSON("T21", "2"), `"resourceType":"Bundle"`,
		`"resourceType":"Bundle","meta":{"profile":["`+pasBase+`profile-pas-request-bundle|2.1.0"]}`, 1)
	_, b := pasPost(t, f.h, "/Claim/$submit", req21)
	text := toJSON(b)
	for _, gone := range []string{"claimResponseReviewer", "|2.2.1", "extension-TransmissionIdentifiers", "admissionDates"} {
		if strings.Contains(text, gone) {
			t.Errorf("a 2.1.0 answer carries %s", gone)
		}
	}
	if !strings.Contains(text, "profile-claimresponse|2.1.0") {
		t.Errorf("profiles not at 2.1.0: %s", text)
	}
	task := toJSON(entriesOfType(b, "Task"))
	if !strings.Contains(task, "extension-paLineNumber") || strings.Contains(task, "extension-serviceLineNumber") {
		t.Errorf("the Task's lines: %s", task)
	}
	for _, c := range entriesOfType(b, "CommunicationRequest") {
		if strings.Contains(toJSON(c["extension"]), "serviceLineNumber") || !strings.Contains(toJSON(c["payload"]), "serviceLineNumber") {
			t.Errorf("2.1.0 numbers a CommunicationRequest's line on its payload: %s", toJSON(c))
		}
	}
	inq21 := strings.Replace(pasRequestJSON("T21"), `"resourceType":"Bundle"`,
		`"resourceType":"Bundle","meta":{"profile":["`+pasBase+`profile-pas-inquiry-request-bundle|2.1.0"]}`, 1)
	code, out := pasPost(t, f.h, "/Claim/$inquire", inq21)
	if code != http.StatusOK || out["resourceType"] != "Bundle" || !strings.Contains(toJSON(out["meta"]), "inquiry-response-bundle|2.1.0") {
		t.Errorf("a 2.1.0 inquiry is answered by one Bundle: %d %s", code, toJSON(out))
	}
	// The stored answer is 2.2.1's, so a 2.2 client inquiring sees its own release.
	code, out = pasPost(t, f.h, "/Claim/$inquire", pasRequestJSON("T21"))
	if code != http.StatusOK || out["resourceType"] != "Parameters" || !strings.Contains(toJSON(out), "|2.2.1") {
		t.Errorf("a 2.2 inquiry: %d %s", code, toJSON(out))
	}
}

// errorElement is a string in PAS 2.1.0 and a pair of sub-extensions in 2.2.1.
func TestAnErrorElementIsAStringIn21(t *testing.T) {
	cr := map[string]any{"resourceType": "ClaimResponse", "error": []any{map[string]any{"extension": []any{
		map[string]any{"url": pasBase + "extension-errorElement", "extension": []any{
			map[string]any{"url": "processNote", "valuePositiveInt": 1}, map[string]any{"url": "error", "valueString": "SV1-01"}}}}}}}
	got := toJSON(pasForVersion(cr, davinci.V21))
	if !strings.Contains(got, `"valueString":"SV1-01"`) || strings.Contains(got, "processNote") {
		t.Errorf("%s", got)
	}
	if toJSON(pasForVersion(cr, davinci.V22)) != toJSON(cr) {
		t.Errorf("a 2.2 answer is unchanged")
	}
}
