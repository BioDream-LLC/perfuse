package priorauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The request fixture is src/test/resources/bundle-prior-auth.json from the Da Vinci PAS reference implementation
// (github.com/HL7-DaVinci/prior-auth, Apache-2.0): an invented patient and practitioner.
func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/pas-ri-request.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAPASRequestIsSummarised(t *testing.T) {
	var bundle map[string]any
	if err := json.Unmarshal(fixture(t), &bundle); err != nil {
		t.Fatal(err)
	}
	req, err := Summarise(bundle)
	if err != nil {
		t.Fatal(err)
	}
	// The reference implementation's request is minimal: a patient and a provider, no insurer or items.
	if req.ID == "" || req.PatientID != "pat013" || req.ProviderID != "1122334455" {
		t.Errorf("summary %+v", req)
	}
	if _, err := Summarise(map[string]any{"resourceType": "Bundle", "entry": []any{
		map[string]any{"resource": map[string]any{"resourceType": "Claim", "use": "claim"}}}}); err == nil {
		t.Error("a Claim for payment was summarised as a prior authorization request")
	}
}

func TestSubmitReadsTheDecisionFromAPASResponse(t *testing.T) {
	var gotAuth, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fhir/Claim/$submit" || r.Method != http.MethodPost {
			http.Error(w, "wrong", 404)
			return
		}
		gotAuth, gotType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"ClaimResponse",
			"id":"cr1","outcome":"complete","preAuthRef":"AUTH-9","item":[{"itemSequence":1,"adjudication":[{"category":{"coding":[{"code":
			"submitted"}]},"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[
			{"url":"number","valueString":"AUTH-9"},{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode",
			"valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A1"}]}}]}]}]}]}}]}`))
	}))
	defer srv.Close()
	raw, decision, err := Submit(context.Background(), srv.Client(), srv.URL+"/fhir/", fixture(t), func(r *http.Request, _ []byte) error {
		r.Header.Set("Authorization", "Bearer t0k")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer t0k" || gotType != "application/fhir+json" || decision.Decision != DecisionApproved ||
		decision.PreAuthRef != "AUTH-9" || !strings.Contains(string(raw), "ClaimResponse") {
		t.Errorf("auth %q type %q decision %+v", gotAuth, gotType, decision)
	}
}

func TestSubmitSaysWhyAPayerRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome","issue":[{"severity":"error","diagnostics":"Coverage is missing"}]}`))
	}))
	defer srv.Close()
	if _, _, err := Submit(context.Background(), srv.Client(), srv.URL, fixture(t), nil); err == nil ||
		!strings.Contains(err.Error(), "Coverage is missing") {
		t.Errorf("err %v", err)
	}
}

// TestAgainstThePASReferenceImplementation submits the fixture to the HL7 Da Vinci PAS payer reference implementation
// (docker image hlseven/davinci-prior-auth) and reads its decision. PERFUSE_TEST_PAS_RI is its FHIR base, for example
// http://localhost:9015/fhir, and PERFUSE_TEST_PAS_RI_TOKEN the bearer token it accepts (the RI's own test token, in its
// source as ADMIN_TOKEN). The RI pends every request until it is released, so the decision expected is pended, with the
// payer's reference.
func TestAgainstThePASReferenceImplementation(t *testing.T) {
	base := os.Getenv("PERFUSE_TEST_PAS_RI")
	if base == "" {
		t.Skip("PERFUSE_TEST_PAS_RI is not set (the HL7 Da Vinci PAS payer reference implementation)")
	}
	token := os.Getenv("PERFUSE_TEST_PAS_RI_TOKEN")
	_, decision, err := Submit(context.Background(), http.DefaultClient, base, fixture(t), func(r *http.Request, _ []byte) error {
		r.Header.Set("Authorization", "Bearer "+token)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != DecisionPended || decision.PreAuthRef == "" {
		t.Errorf("the reference payer's decision: %+v", decision)
	}
}
