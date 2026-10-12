package fhirserver

import (
	"net/http"
	"strings"
	"testing"
)

// TestPlanNetDirectorySearches covers the Da Vinci Plan-Net 1.1.0 provider directory searches, which answered 400 before:
// the parameters Plan-Net defines (some on extensions), and the includes its CapabilityStatement lists.
func TestPlanNetDirectorySearches(t *testing.T) {
	srv, h := newTestServer(t)
	const pn = "http://hl7.org/fhir/us/davinci-pdex-plan-net/StructureDefinition/"
	for _, r := range []string{
		`{"resourceType": "Organization", "id": "net", "name": "Acme Network", "type": [{"coding": [{"code": "ntwk"}]}],
		  "extension": [{"url": "` + pn + `location-reference", "valueReference": {"reference": "Location/area"}}]}`,
		`{"resourceType": "Location", "id": "area", "name": "Springfield County"}`,
		`{"resourceType": "Practitioner", "id": "dr", "name": [{"family": "Brown", "given": ["Dana"]}]}`,
		`{"resourceType": "PractitionerRole", "id": "role", "practitioner": {"reference": "Practitioner/dr"},
		  "location": [{"reference": "Location/area"}], "code": [{"coding": [{"code": "doctor"}]}],
		  "extension": [{"url": "` + pn + `network-reference", "valueReference": {"reference": "Organization/net"}}]}`,
		`{"resourceType": "OrganizationAffiliation", "id": "aff", "network": [{"reference": "Organization/net"}],
		  "participatingOrganization": {"reference": "Organization/net"}}`,
		`{"resourceType": "InsurancePlan", "id": "plan", "ownedBy": {"reference": "Organization/net"},
		  "coverageArea": [{"reference": "Location/area"}], "plan": [{"type": {"coding": [{"code": "gold"}]}}]}`,
	} {
		put(t, srv.Store, r)
	}

	for _, c := range []struct{ query, want string }{
		{"/PractitionerRole?network=Organization/net", `"id": "role"`},
		{"/PractitionerRole?role=doctor", `"id": "role"`},
		{"/OrganizationAffiliation?network=net", `"id": "aff"`},
		{"/OrganizationAffiliation?participating-organization=Organization/net", `"id": "aff"`},
		{"/Organization?coverage-area=Location/area", `"id": "net"`},
		{"/Organization?type=ntwk", `"id": "net"`},
		{"/InsurancePlan?plan-type=gold", `"id": "plan"`},
		{"/InsurancePlan?owned-by=net&_include=InsurancePlan:coverage-area", `"id": "area"`},
		{"/Practitioner?_id=dr&_revinclude=PractitionerRole:practitioner", `"id": "role"`},
		{"/Location?_id=area&_revinclude=InsurancePlan:coverage-area", `"id": "plan"`},
	} {
		rec := do(t, h, http.MethodGet, c.query, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d, want %s in %.200s", c.query, rec.Code, c.want, rec.Body.String())
		}
	}
}

// TestAnExtensionPathIndexes covers a path into an extension whose URL has dots, which every URL has: the path was split
// inside the URL and matched nothing, so Condition's asserted-date never indexed.
func TestAnExtensionPathIndexes(t *testing.T) {
	srv, h := newTestServer(t)
	put(t, srv.Store, `{"resourceType": "Condition", "id": "c1", "subject": {"reference": "Patient/p1"},
		"extension": [{"url": "http://hl7.org/fhir/StructureDefinition/condition-assertedDate", "valueDateTime": "2026-03-04"}]}`)
	if rec := do(t, h, http.MethodGet, "/Condition?patient=p1&asserted-date=2026-03-04", nil); !strings.Contains(rec.Body.String(), `"id": "c1"`) {
		t.Errorf("asserted-date found nothing: %d", rec.Code)
	}
}
