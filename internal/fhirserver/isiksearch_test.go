package fhirserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The searches gematik's ISiK suite makes that the server did not have.
func TestISiKSearches(t *testing.T) {
	_, h := newTestServer(t)
	put := func(path, body string) {
		t.Helper()
		if rec := do(t, h, http.MethodPut, path, body); rec.Code >= 300 {
			t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	put("/Patient/max", `{"resourceType":"Patient","id":"max","identifier":[{"system":"http://fhir.de/sid/gkv/kvid-10","value":"X485231029"}],
	  "name":[{"family":"Mustermann"}],"address":[{"city":"Berlin","country":"DE"}],
	  "meta":{"tag":[{"system":"http://fhir.de/CodeSystem/common-meta-tag-de","code":"external"}]}}`)
	put("/Encounter/e1", `{"resourceType":"Encounter","id":"e1","status":"in-progress","class":{"system":"http://terminology.hl7.org/CodeSystem/v3-ActCode","code":"IMP"},
	  "subject":{"reference":"Patient/max"},"period":{"start":"2026-01-06","end":"2026-01-10"},"serviceProvider":{"reference":"Organization/kh"},
	  "account":[{"reference":"Account/a1"}]}`)
	put("/Observation/bp", `{"resourceType":"Observation","id":"bp","status":"final","subject":{"reference":"Patient/max"},
	  "code":{"coding":[{"system":"http://loinc.org","code":"85354-9"}]},
	  "component":[{"code":{"coding":[{"system":"http://loinc.org","code":"8480-6"}]},"valueQuantity":{"value":107,"unit":"mm[Hg]"}}]}`)
	put("/Location/zimmer", `{"resourceType":"Location","id":"zimmer","position":{"latitude":52.5163,"longitude":13.3777},
	  "operationalStatus":{"system":"http://terminology.hl7.org/CodeSystem/v2-0116","code":"O"}}`)
	put("/ValueSet/vs", `{"resourceType":"ValueSet","id":"vs","status":"active","url":"http://example.org/vs","version":"1.0",
	  "useContext":[{"code":{"system":"http://terminology.hl7.org/CodeSystem/usage-context-type","code":"focus"},
	    "valueCodeableConcept":{"coding":[{"system":"http://hl7.org/fhir/resource-types","code":"Encounter"}]}}]}`)

	put("/Account/a1", `{"resourceType":"Account","id":"a1","status":"active","subject":[{"reference":"Patient/max"}],
	  "identifier":[{"system":"https://test.krankenhaus.de/fhir/sid/abrechnungsnummer","value":"0123456789"}]}`)
	put("/Coverage/c1", `{"resourceType":"Coverage","id":"c1","status":"active","beneficiary":{"reference":"Patient/max"},
	  "payor":[{"reference":"Patient/max"}]}`)
	put("/Condition/c1", `{"resourceType":"Condition","id":"c1","subject":{"reference":"Patient/max"},
	  "clinicalStatus":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/condition-clinical","code":"active"}]}}`)
	put("/Condition/c2", `{"resourceType":"Condition","id":"c2","subject":{"reference":"Patient/max"},
	  "clinicalStatus":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/condition-clinical","code":"inactive"}]}}`)

	for query, want := range map[string]int{
		"/Condition/?subject=Patient/max&clinical-status:not=http://terminology.hl7.org/CodeSystem/condition-clinical|inactive": 1,
		"/Condition?_id:not=c1": 1,
		"/Coverage/?patient.identifier=http://fhir.de/sid/gkv/kvid-10|X485231029":                          1,
		"/Coverage/?beneficiary.identifier=http://fhir.de/sid/gkv/kvid-10|X485231029":                      1,
		"/Encounter/?account:identifier=https://test.krankenhaus.de/fhir/sid/abrechnungsnummer|0123456789": 1,
		"/Encounter/?account:identifier=https://test.krankenhaus.de/fhir/sid/abrechnungsnummer|other":      0,
		"/Patient/?_count": 1,
		"/Encounter?status=http://hl7.org/fhir/encounter-status|in-progress":  1,
		"/Patient?_tag=http://fhir.de/CodeSystem/common-meta-tag-de|external": 1,
		"/Patient?_tag=other":          0,
		"/Patient/?address-country=DE": 1,
		"/Encounter?date-start=ge2026-01-05&end-date=le2026-01-14&patient=Patient/max":                        1,
		"/Encounter?date-start=ge2026-01-14":                                                                  0,
		"/Encounter?service-provider=Organization/kh&account=a1":                                              1,
		"/Observation?combo-code-value-quantity=8480-6$107":                                                   1,
		"/Observation?combo-code-value-quantity=http://loinc.org|8480-6$107|http://unitsofmeasure.org|mm[Hg]": 1,
		"/Observation?combo-code-value-quantity=8480-6$108":                                                   0,
		"/Observation?component-code=8480-6":                                                                  1,
		"/Location?near=52.52|13.405":                                                                         1,
		"/Location?near=48.137|11.575|10|km":                                                                  0,
		"/Location?operational-status=O":                                                                      1,
		"/ValueSet?context-type-value=focus$http://hl7.org/fhir/resource-types|Encounter":                     1,
		"/ValueSet?url=http://example.org/vs&version=1.0":                                                     1,
		"/Patient?_count=20":                                                                                  1,
	} {
		rec := do(t, h, http.MethodGet, query, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", query, rec.Code, rec.Body)
		} else if got := len(bundleEntries(t, rec)); got != want {
			t.Errorf("%s: %d found, want %d", query, got, want)
		}
	}
}

func TestBinaryReadInItsOwnFormat(t *testing.T) {
	_, h := newTestServer(t)
	if rec := do(t, h, http.MethodPut, "/Binary/b1", `{"resourceType":"Binary","id":"b1","contentType":"text/plain","data":"VGVzdA=="}`); rec.Code >= 300 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/Binary/b1", nil)
	req.Header.Set("Accept", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "Test" || rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("text/plain: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if rec := do(t, h, http.MethodGet, "/Binary/b1", nil); !strings.Contains(rec.Body.String(), `"resourceType"`) {
		t.Errorf("FHIR read: %s", rec.Body)
	}
}

func TestADocumentPostedToTheBase(t *testing.T) {
	_, h := newTestServer(t)
	do(t, h, http.MethodPut, "/Patient/max", `{"resourceType":"Patient","id":"max","identifier":[{"system":"http://fhir.de/sid/gkv/kvid-10","value":"X485231029"}]}`)
	doc := func(kvid, text string) string {
		return `{"resourceType":"Bundle","type":"document","identifier":{"system":"urn:ietf:rfc:3986","value":"urn:uuid:` + kvid + `"},
		  "timestamp":"2026-01-07T10:00:00+01:00","entry":[
		  {"fullUrl":"urn:uuid:c","resource":{"resourceType":"Composition","status":"final","type":{"text":"Brief"},"title":"Brief","date":"2026-01-07",
		    "author":[{"display":"Dr. A"}],"subject":{"reference":"urn:uuid:p"}` + text + `}},
		  {"fullUrl":"urn:uuid:p","resource":{"resourceType":"Patient","identifier":[{"system":"http://fhir.de/sid/gkv/kvid-10","value":"` + kvid + `"}]}}]}`
	}
	narrative := `,"text":{"status":"generated","div":"<div xmlns=\"http://www.w3.org/1999/xhtml\">Brief</div>"}`
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"known patient":   {doc("X485231029", narrative), http.StatusCreated},
		"unknown patient": {doc("X123456789", narrative), http.StatusUnprocessableEntity},
		"no narrative":    {doc("X485231029", ""), http.StatusUnprocessableEntity},
	} {
		if rec := do(t, h, http.MethodPost, "/", c.body); rec.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", name, rec.Code, c.want, rec.Body)
		}
	}
}
