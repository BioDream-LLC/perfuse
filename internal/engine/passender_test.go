package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/priorauth"
)

func pasRequest(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "priorauth", "testdata", "pas-ri-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withTracker(t *testing.T) *priorauth.PriorAuthTracker {
	t.Helper()
	tr, err := priorauth.OpenTracker(filepath.Join(t.TempDir(), "prior-auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	old := PriorAuths
	PriorAuths = tr
	t.Cleanup(func() { PriorAuths = old })
	return tr
}

const deniedAnswer = `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"ClaimResponse","id":"x",
"outcome":"complete","preAuthRef":"REF-1","disposition":"Not medically necessary","item":[{"itemSequence":1,"adjudication":[{
"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[{"url":
"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode","valueCodeableConcept":{"coding":[{"code":"A3"}]}}]}]}]}]}}]}`

func TestADenialIsDeliveredRecordedAndPassedOn(t *testing.T) {
	tr := withTracker(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "no", 401)
			return
		}
		_, _ = w.Write([]byte(deniedAnswer))
	}))
	defer srv.Close()
	t.Setenv("PAS_TOKEN", "secret-token")
	router := newFakeRouter()
	s, err := NewPASSender(config.Destination{Name: "payer", Type: config.DestinationPAS,
		PAS: &config.PASDestination{Payer: srv.URL, BearerToken: "${PAS_TOKEN}", ResultsTo: "answers"}}, router)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := s.SendForResponse(context.Background(), pasRequest(t))
	if err != nil {
		t.Fatalf("a denial failed the delivery, so it would be submitted again: %v", err)
	}
	if !strings.Contains(string(reply), "REF-1") || len(router.sent["answers"]) != 1 {
		t.Errorf("reply %s, routed %d", reply, len(router.sent["answers"]))
	}
	got := tr.List("", 0)
	if len(got) != 1 || got[0].Response == nil || got[0].Response.Decision != priorauth.DecisionDenied ||
		got[0].Request.PatientID != "pat013" {
		t.Errorf("tracked %+v", got)
	}
}

func TestAPayerThatDidNotDecideFailsTheDelivery(t *testing.T) {
	tr := withTracker(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"resourceType":"OperationOutcome","issue":[{"diagnostics":"down for maintenance"}]}`, 503)
	}))
	defer srv.Close()
	s, _ := NewPASSender(config.Destination{Name: "payer", Type: config.DestinationPAS, PAS: &config.PASDestination{Payer: srv.URL}}, nil)
	if err := s.Send(context.Background(), pasRequest(t)); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Errorf("err %v", err)
	}
	if err := s.Send(context.Background(), []byte("MSH|^~\\&|")); err == nil {
		t.Error("an HL7 v2 message was submitted as a PAS request")
	}
	if n := len(tr.List("", 0)); n != 0 {
		t.Errorf("%d requests recorded as decided", n)
	}
}

// TestPASAgainstTheReferencePayer sends a request through the destination to the HL7 Da Vinci PAS payer reference
// implementation (see internal/priorauth's TestAgainstThePASReferenceImplementation for the variables).
func TestPASAgainstTheReferencePayer(t *testing.T) {
	base := os.Getenv("PERFUSE_TEST_PAS_RI")
	if base == "" {
		t.Skip("PERFUSE_TEST_PAS_RI is not set")
	}
	tr := withTracker(t)
	s, err := NewPASSender(config.Destination{Name: "ri", Type: config.DestinationPAS,
		PAS: &config.PASDestination{Payer: base, BearerToken: os.Getenv("PERFUSE_TEST_PAS_RI_TOKEN")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), pasRequest(t)); err != nil {
		t.Fatal(err)
	}
	got := tr.List("", 0)
	if len(got) != 1 || got[0].Response.Decision != priorauth.DecisionPended || got[0].Response.PreAuthRef == "" {
		t.Errorf("tracked %+v", got)
	}
}
