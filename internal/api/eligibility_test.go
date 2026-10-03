package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/x12"
)

func TestEligibilityBuildsA270AndReadsA271WithItsCOREFindings(t *testing.T) {
	h := newHarness(t)
	rec := h.do("viewer", http.MethodPost, "/api/x12/eligibility/build", x12.EligibilityRequest{
		Envelope:   x12.Envelope{SenderID: "CLINIC01", ReceiverID: "PAYER01"},
		Payer:      x12.Person{LastName: "Springfield Health Plan", ID: "SHP01"},
		Provider:   x12.Person{LastName: "Riverside Clinic", ID: "1234567893"},
		Subscriber: x12.Person{LastName: "DOE", FirstName: "JANE", ID: "MBR123456"},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ST*270*") || !strings.Contains(rec.Body.String(), `"envelopeProblems":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do("viewer", http.MethodPost, "/api/x12/eligibility/build", x12.EligibilityRequest{}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "member ID") {
		t.Errorf("an empty request: %d %s", rec.Code, rec.Body.String())
	}

	raw, _ := os.ReadFile("../x12/testdata/271-eligibility.x12")
	rec = h.do("viewer", http.MethodPost, "/api/x12/eligibility/read", x12Body{X12: string(raw)})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"status":"active"`) || !strings.Contains(body, "Co-payment in network: $25.") ||
		!strings.Contains(body, `"core":[`) || strings.Contains(body, `"met":false`) {
		t.Errorf("%d %s", rec.Code, body)
	}
}

func TestClaimStatusAndEnrollmentAreOffered(t *testing.T) {
	h := newHarness(t)
	rec := h.do("viewer", http.MethodPost, "/api/x12/claimstatus/build", x12.ClaimStatusRequest{
		Envelope: x12.Envelope{SenderID: "C", ReceiverID: "P"}, Payer: x12.Person{LastName: "Plan", ID: "P1"},
		Provider: x12.Person{LastName: "Clinic", ID: "1234567893"}, Subscriber: x12.Person{LastName: "DOE", ID: "M1"},
		PatientAccount: "PCN1", ServiceFrom: "20260915",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ST*276*") {
		t.Errorf("276: %d %s", rec.Code, rec.Body.String())
	}
	raw, _ := os.ReadFile("../x12/testdata/834-enrollment.x12")
	rec = h.do("viewer", http.MethodPost, "/api/x12/enrollment/read", x12Body{X12: string(raw)})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Acme Manufacturing") || !strings.Contains(rec.Body.String(), `"action":"addition"`) {
		t.Errorf("834: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do("viewer", http.MethodPost, "/api/x12/enrollment/read", x12Body{X12: "not x12"}); rec.Code != http.StatusBadRequest {
		t.Errorf("garbage: %d", rec.Code)
	}
}
