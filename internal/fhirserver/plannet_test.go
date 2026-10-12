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

// TestAChainMatchingSeveralFindsEach covers a chained search whose inner condition matches more than one resource: the
// matches were ANDed, so the search found only what referred to all of them, which is nothing.
func TestAChainMatchingSeveralFindsEach(t *testing.T) {
	srv, h := newTestServer(t)
	for _, r := range []string{
		`{"resourceType": "Practitioner", "id": "s1", "name": [{"family": "Smith"}]}`,
		`{"resourceType": "Practitioner", "id": "s2", "name": [{"family": "Smith"}]}`,
		`{"resourceType": "PractitionerRole", "id": "r1", "practitioner": {"reference": "Practitioner/s1"}}`,
		`{"resourceType": "PractitionerRole", "id": "r2", "practitioner": {"reference": "Practitioner/s2"}}`,
	} {
		put(t, srv.Store, r)
	}
	rec := do(t, h, http.MethodGet, "/PractitionerRole?practitioner.name=Smith", nil)
	for _, id := range []string{"r1", "r2"} {
		if !strings.Contains(rec.Body.String(), `"id": "`+id+`"`) {
			t.Errorf("practitioner.name=Smith did not find %s: %d", id, rec.Code)
		}
	}
}

// TestReverseChaining covers _has, which Plan-Net uses to find, say, the practitioners who work at a place. It was refused.
func TestReverseChaining(t *testing.T) {
	srv, h := newTestServer(t)
	const pn = "http://hl7.org/fhir/us/davinci-pdex-plan-net/StructureDefinition/"
	for _, r := range []string{
		`{"resourceType": "Practitioner", "id": "here", "name": [{"family": "Here"}]}`,
		`{"resourceType": "Practitioner", "id": "there", "name": [{"family": "There"}]}`,
		`{"resourceType": "Practitioner", "id": "also", "name": [{"family": "Also"}]}`,
		`{"resourceType": "Location", "id": "clinic"}`,
		`{"resourceType": "Organization", "id": "net"}`,
		`{"resourceType": "PractitionerRole", "id": "r1", "practitioner": {"reference": "Practitioner/here"}, "location": [{"reference": "Location/clinic"}],
		  "extension": [{"url": "` + pn + `network-reference", "valueReference": {"reference": "Organization/net"}}]}`,
		`{"resourceType": "PractitionerRole", "id": "r2", "practitioner": {"reference": "Practitioner/also"}, "location": [{"reference": "Location/clinic"}]}`,
		`{"resourceType": "PractitionerRole", "id": "r3", "practitioner": {"reference": "Practitioner/there"}}`,
		`{"resourceType": "InsurancePlan", "id": "plan", "ownedBy": {"reference": "Organization/net"}, "coverageArea": [{"reference": "Location/clinic"}]}`,
	} {
		put(t, srv.Store, r)
	}
	for _, c := range []struct {
		query   string
		want    []string
		notWant string
	}{
		{"/Practitioner?_has:PractitionerRole:practitioner:location=Location/clinic", []string{"here", "also"}, "there"},
		{"/Organization?_has:PractitionerRole:network:location=Location/clinic", []string{"net"}, ""},
		{"/Location?_has:InsurancePlan:coverage-area:owned-by=Organization/net", []string{"clinic"}, ""},
		{"/Practitioner?_has:PractitionerRole:practitioner:location=Location/nowhere", nil, "here"},
	} {
		rec := do(t, h, http.MethodGet, c.query, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %.200s", c.query, rec.Code, body)
			continue
		}
		for _, id := range c.want {
			if !strings.Contains(body, `"id": "`+id+`"`) {
				t.Errorf("%s did not find %s", c.query, id)
			}
		}
		if c.notWant != "" && strings.Contains(body, `"id": "`+c.notWant+`"`) {
			t.Errorf("%s found %s", c.query, c.notWant)
		}
	}
	if rec := do(t, h, http.MethodGet, "/Practitioner?_has:Observation:subject:code=x", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a _has through a reference that cannot point here: %d, want 400", rec.Code)
	}
}

// TestIDSearchSemantics covers _id=a,b (any of, which looked for an id spelled "a,b") and _id=a&_id=b (both, which found
// either).
func TestIDSearchSemantics(t *testing.T) {
	srv, h := newTestServer(t)
	put(t, srv.Store, `{"resourceType": "Practitioner", "id": "a"}`)
	put(t, srv.Store, `{"resourceType": "Practitioner", "id": "b"}`)
	if body := do(t, h, http.MethodGet, "/Practitioner?_id=a,b", nil).Body.String(); !strings.Contains(body, `"id": "a"`) || !strings.Contains(body, `"id": "b"`) {
		t.Error("_id=a,b did not find both")
	}
	if body := do(t, h, http.MethodGet, "/Practitioner?_id=a&_id=b", nil).Body.String(); strings.Contains(body, `"id": "a"`) || strings.Contains(body, `"id": "b"`) {
		t.Error("_id=a&_id=b found something; no resource has both ids")
	}
}
