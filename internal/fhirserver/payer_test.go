package fhirserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms0057"
)

// payerFixture is a server with the payer operations and bulk export on, holding a member's CARIN claim loaded through the
// transaction endpoint - the way a payer's pipeline would load one.
func payerFixture(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	srv, h := newTestServer(t)
	srv.Payer = &PayerAPIs{RequireConsent: true, Now: func() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) }}
	mgr, err := NewExportManager(t.Context(), srv.Store)
	if err != nil {
		t.Fatal(err)
	}
	srv.Export = mgr
	h = srv.Handler()

	c, _ := os.ReadFile("../cms0057/testdata/837p.x12")
	r, _ := os.ReadFile("../cms0057/testdata/835p.x12")
	res, _, err := cms0057.ConvertClaims(c, r, cms0057.CARINOptions{IdentifierSystem: "https://fhir.springfield-health-plan.org/identifier", NetworkStatus: "innetwork"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res[0].Bundle)
	if rec := payerDo(t, h, "POST", "/", string(raw), nil); rec.Code != http.StatusOK {
		t.Fatalf("loading the CARIN bundle: %d %s", rec.Code, rec.Body)
	}

	group := `{"resourceType":"Group","id":"attributed","type":"person","actual":true,
	  "code":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pdex/CodeSystem/PdexMemberAttributionCS","code":"pdexprovidergroup"}]},
	  "member":[{"entity":{"reference":"Patient/pt-MBR123456"}}]}`
	if rec := payerDo(t, h, "PUT", "/Group/attributed", group, nil); rec.Code >= 300 {
		t.Fatalf("storing the Group: %d %s", rec.Code, rec.Body)
	}

	return srv, h
}

func payerDo(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/fhir+json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestCARINClaimIsServedWhole(t *testing.T) {
	_, h := payerFixture(t)
	rec := payerDo(t, h, "GET", "/ExplanationOfBenefit?patient=Patient/pt-MBR123456", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{"EHPCLAIM20250001", `"item"`, "99213", "memberliability", "clmrecvddate"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the stored claim lost %s", want)
		}
	}
	if rec := payerDo(t, h, "GET", "/ExplanationOfBenefit?identifier=EHPCLAIM20250001", "", nil); !strings.Contains(strings.ReplaceAll(rec.Body.String(), " ", ""), `"total":1`) {
		t.Errorf("search by the CARIN unique claim id found nothing: %s", rec.Body)
	}
}

const matchBody = `{"resourceType":"Parameters","parameter":[
 {"name":"MemberPatient","resource":{"resourceType":"Patient","name":[{"family":"Sampleson","given":["Bravo"]}],"birthDate":"1980-02-15","gender":"female"}},
 {"name":"CoverageToMatch","resource":{"resourceType":"Coverage","status":"active","subscriberId":"MBR123456","beneficiary":{"reference":"Patient/x"},"payor":[{"display":"Example Health Plan"}]}},
 {"name":"Consent","resource":{"resourceType":"Consent","status":"active","provision":{"type":"permit","period":{"start":"2026-01-01","end":"2027-01-01"}}}}]}`

func TestMemberMatchOverHTTP(t *testing.T) {
	_, h := payerFixture(t)
	rec := payerDo(t, h, "POST", "/Patient/$member-match", matchBody, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "MBR123456") || !strings.Contains(rec.Body.String(), "Patient/pt-MBR123456") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	wrong := strings.Replace(matchBody, "1980-02-15", "1981-02-15", 1)
	if rec := payerDo(t, h, "POST", "/Patient/$member-match", wrong, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong birth date matched: %d %s", rec.Code, rec.Body)
	}

	noConsent := strings.Replace(matchBody, `"status":"active","provision"`, `"status":"inactive","provision"`, 1)
	if rec := payerDo(t, h, "POST", "/Patient/$member-match", noConsent, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an inactive consent was accepted: %d %s", rec.Code, rec.Body)
	}
}

func TestPayerOperationsAreOffUnlessEnabled(t *testing.T) {
	_, h := newTestServer(t)
	if rec := payerDo(t, h, "POST", "/Patient/$member-match", matchBody, nil); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "-fhir-payer-apis") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func groupExportFile(t *testing.T, srv *Server, h http.Handler, path string) string {
	t.Helper()
	rec := payerDo(t, h, "GET", path, "", map[string]string{"Prefer": "respond-async"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	loc := rec.Header().Get("Content-Location")
	id := strings.TrimSuffix(strings.TrimPrefix(loc[strings.Index(loc, "/_export/"):], "/_export/"), "/status")
	job := waitForExport(t, srv.Export, id)
	if job.Status != ExportComplete {
		t.Fatalf("export: %s %s", job.Status, job.Error)
	}
	body, err := srv.Export.File(t.Context(), id, "ExplanationOfBenefit")
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestProviderAccessExportLeavesOutCostSharing(t *testing.T) {
	srv, h := payerFixture(t)
	body := groupExportFile(t, srv, h, "/Group/attributed/$davinci-data-export?exportType=hl7.fhir.us.davinci-pdex%23provider-download")
	if !strings.Contains(body, "99213") {
		t.Fatalf("the claim's services are missing: %s", body)
	}
	for _, gone := range []string{"memberliability", "paidtoprovider", `"payment"`} {
		if strings.Contains(body, gone) {
			t.Errorf("%s reached the provider", gone)
		}
	}
}

func TestProviderAccessRespectsOptOut(t *testing.T) {
	_, h := payerFixture(t)
	consent := `{"resourceType":"Consent","id":"optout","status":"active","patient":{"reference":"Patient/pt-MBR123456"},
	 "category":[{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pdex/CodeSystem/pdex-consent-api-purpose","code":"provider-access"}]}],
	 "provision":{"type":"deny","period":{"start":"2025-01-01"}}}`
	if rec := payerDo(t, h, "PUT", "/Consent/optout", consent, nil); rec.Code >= 300 {
		t.Fatalf("storing the opt-out: %d %s", rec.Code, rec.Body)
	}
	rec := payerDo(t, h, "GET", "/Group/attributed/$davinci-data-export", "", map[string]string{"Prefer": "respond-async"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "1 opted out") {
		t.Fatalf("an opted-out member was exported: %d %s", rec.Code, rec.Body)
	}
}

func TestGroupExportRefusesANonMember(t *testing.T) {
	_, h := payerFixture(t)
	rec := payerDo(t, h, "GET", "/Group/attributed/$davinci-data-export?patient=Patient/someone-else", "", map[string]string{"Prefer": "respond-async"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a patient outside the Group was accepted: %d %s", rec.Code, rec.Body)
	}
}
