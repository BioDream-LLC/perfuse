package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func attachmentBody(doc string) map[string]any {
	return map[string]any{
		"senderId": "PROVIDER01", "receiverId": "PAYER01", "controlNumber": 77,
		"reference": "REF-1", "traceNumber": "ACN-1",
		"payer":       map[string]any{"name": "EXAMPLE PLAN", "id": "12345"},
		"provider":    map[string]any{"name": "EXAMPLE CLINIC", "id": "1234567893"},
		"patient":     map[string]any{"name": "DOE", "firstName": "JANE", "id": "M1"},
		"contentType": "application/pdf", "filename": "note.pdf",
		"documentBase64": base64.StdEncoding.EncodeToString([]byte(doc)),
	}
}

func TestAttachmentBuildRoundTripsThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	rec := h.do("viewer", "POST", "/api/x12/attachment/build", attachmentBody("%PDF-1.4 ~*:^ %%EOF"))
	if rec.Code != http.StatusOK {
		t.Fatalf("build returned %d: %s", rec.Code, rec.Body.String())
	}
	assertNoNulls(t, "POST /api/x12/attachment/build", rec.Body.Bytes())
	var body struct {
		X12       string `json:"x12"`
		RoundTrip bool   `json:"roundTrip"`
		Basis     string `json:"basis"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.RoundTrip || !strings.Contains(body.X12, "006020X314") {
		t.Errorf("roundTrip=%v x12=%.80q", body.RoundTrip, body.X12)
	}
	// The limits of the claim travel with the output, so the page cannot show a 275 without them.
	if !strings.Contains(body.Basis, "not validated against the X12 TR3") {
		t.Errorf("the response does not say what the 275 was built from: %q", body.Basis)
	}

	read := h.do("viewer", "POST", "/api/x12/attachment/read", map[string]any{"x12": body.X12})
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"filename":"note.pdf"`) {
		t.Errorf("reading the built 275 back: %d %s", read.Code, read.Body.String())
	}
}

func TestAttachmentBuildNamesWhatIsMissing(t *testing.T) {
	h := newHarness(t)
	rec := h.do("viewer", "POST", "/api/x12/attachment/build", map[string]any{"documentText": "x"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "payer identifier") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestPriorAuthResponseBecomesAPASClaimResponse(t *testing.T) {
	h := newHarness(t)
	x := "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PROVIDER       *260916*1500*^*00501*000000404*0*T*:~" +
		"GS*HI*PAYER*PROVIDER*20260916*1500*404*X*005010X217~ST*278*0001*005010X217~BHT*0007*11*REQ-9*20260916*1500*11~" +
		"HL*1**20*1~NM1*X3*2*ACME HEALTH PLAN*****PI*ACME01~HL*2*1*21*1~NM1*1P*2*RIVERSIDE ORTHOPAEDICS*****XX*1234567893~" +
		"HL*3*2*22*1~NM1*IL*1*TURNER*ROSALIND****MI*MEM88771~HL*4*3*EV*1~TRN*2*AUTHREQ-4471~UM*HS*I*4~DTP*472*D8*20261001~" +
		"HI*BK:M1711~HCR*A1*AUTH-1~HL*5*4*SS*0~SV1*HC:29881*450.00*UN*1~SE*15*0001~GE*1*404~IEA*1*000000404~"
	rec := h.do("viewer", "POST", "/api/priorauth/claimresponse", map[string]any{"x12": x})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	assertNoNulls(t, "POST /api/priorauth/claimresponse", rec.Body.Bytes())
	for _, want := range []string{"profile-claimresponse", `"preAuthRef":"AUTH-1"`, `"code":"A1"`, `"created":"2026-09-16"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("response lacks %s", want)
		}
	}
}

func TestSubscriptionsEndpointSaysWhenOff(t *testing.T) {
	h := newHarness(t)
	h.server.Runtime = NewRuntime(h.server.Channels, nil, nil)
	rec := h.do("viewer", "GET", "/api/fhir/subscriptions", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	assertNoNulls(t, "GET /api/fhir/subscriptions", rec.Body.Bytes())
}
