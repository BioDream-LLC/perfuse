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
	"github.com/biodream-llc/perfuse/internal/crd"
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

// groupAuth is a caller limited to some Groups, as a provider's API token is.
type groupAuth struct{ groups []string }

func (g groupAuth) Authenticate(*http.Request) (*Caller, error) {
	return &Caller{Name: "token:provider", AllScopes: true, Groups: g.groups}, nil
}
func (groupAuth) Describe() string { return "test" }

func TestGroupLimitedTokenReachesOnlyItsGroup(t *testing.T) {
	srv, _ := payerFixture(t)
	other := `{"resourceType":"Group","id":"other","type":"person","actual":true,"member":[{"entity":{"reference":"Patient/pt-MBR123456"}}]}`
	if rec := payerDo(t, srv.Handler(), "PUT", "/Group/other", other, nil); rec.Code >= 300 {
		t.Fatal(rec.Body)
	}
	srv.Auth = groupAuth{groups: []string{"attributed"}}
	h := srv.Handler()
	async := map[string]string{"Prefer": "respond-async"}

	if rec := payerDo(t, h, "GET", "/Group/attributed/$davinci-data-export", "", async); rec.Code != http.StatusAccepted {
		t.Fatalf("its own Group: %d %s", rec.Code, rec.Body)
	}
	if rec := payerDo(t, h, "GET", "/Group/other/$davinci-data-export", "", async); rec.Code != http.StatusNotFound {
		t.Fatalf("another provider's Group was exported: %d %s", rec.Code, rec.Body)
	}
	if rec := payerDo(t, h, "GET", "/Group/attributed", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("reading its own Group: %d", rec.Code)
	}
	for _, path := range []string{"/Group/other", "/Patient", "/ExplanationOfBenefit?patient=Patient/pt-MBR123456", "/Patient/pt-MBR123456"} {
		if rec := payerDo(t, h, "GET", path, "", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s answered %d for a Group-limited token", path, rec.Code)
		}
	}
	if rec := payerDo(t, h, "POST", "/Patient/$member-match", matchBody, nil); rec.Code != http.StatusForbidden {
		t.Errorf("member-match answered %d for a provider's token", rec.Code)
	}
}

func TestCRDResolvesTheMemberAgainstThePayersRecords(t *testing.T) {
	srv, _ := payerFixture(t)
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	member := map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "Sampleson", "given": []any{"Bravo"}}},
		"birthDate": "1980-02-15", "gender": "female"}
	stranger := map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "Nobody"}}, "birthDate": "1990-01-01"}
	for _, tc := range []struct {
		name     string
		coverage map[string]any
		patient  map[string]any
		want     crd.Membership
	}{
		{"the member and coverage", map[string]any{"subscriberId": "MBR123456"}, member, crd.MemberActive},
		{"a member with a coverage that is not theirs", map[string]any{"subscriberId": "NOPE"}, member, crd.CoverageNotFound},
		{"somebody who is not a member", map[string]any{"subscriberId": "NOPE"}, stranger, crd.NoMemberFound},
	} {
		got, why, err := srv.ResolveMember(t.Context(), tc.coverage, tc.patient, now)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q (%s) %v", tc.name, got, why, err)
		}
	}
}

func TestAnApprovedAuthorizationSatisfiesCRD(t *testing.T) {
	srv, h := payerFixture(t)
	cr := `{"resourceType":"ClaimResponse","id":"pa1","status":"active","type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"professional"}]},
	  "use":"preauthorization","patient":{"reference":"Patient/pt-MBR123456"},"created":"2026-02-01","insurer":{"display":"Example Health Plan"},
	  "outcome":"complete","preAuthRef":"AUTH-778","preAuthPeriod":{"start":"2026-02-01","end":"2026-08-01"},
	  "addItem":[{"productOrService":{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0424"}]}}]}`
	if rec := payerDo(t, h, "PUT", "/ClaimResponse/pa1", cr, nil); rec.Code >= 300 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	member := map[string]any{"name": []any{map[string]any{"family": "Sampleson"}}, "birthDate": "1980-02-15"}
	cov := map[string]any{"subscriberId": "MBR123456"}
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if id, err := srv.SatisfiedAuthorization(t.Context(), cov, member, []string{"E0424"}, now); err != nil || id != "AUTH-778" {
		t.Errorf("approved: %q %v", id, err)
	}
	if id, _ := srv.SatisfiedAuthorization(t.Context(), cov, member, []string{"E0431"}, now); id != "" {
		t.Errorf("another service: %q", id)
	}
	if id, _ := srv.SatisfiedAuthorization(t.Context(), cov, member, []string{"E0424"}, now.AddDate(1, 0, 0)); id != "" {
		t.Errorf("after the authorization period: %q", id)
	}
}
