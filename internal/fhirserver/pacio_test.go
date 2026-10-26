package fhirserver

import (
	"net/http"
	"strings"
	"testing"
)

// The Patient searches PACIO's Transitions of Care kit makes, which answered 400: active, telecom (with and without its kind)
// and address. And a document Bundle, the transfer summary itself, stored and read back with its entries.
func TestPatientSearchesAndDocumentsForTransfers(t *testing.T) {
	_, h := newTestServer(t)
	p := `{"resourceType":"Patient","id":"betsy","active":true,"identifier":[{"system":"urn:oid:2.16.840.1.113883.4.3.25","value":"1"}],
	  "name":[{"family":"Smith-Johnson","given":["Betsy"]}],"telecom":[{"system":"phone","value":"555-0100"}],
	  "address":[{"city":"Boston","state":"MA","postalCode":"02134"}]}`
	if rec := do(t, h, http.MethodPut, "/Patient/betsy", p); rec.Code >= 300 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	for query, want := range map[string]int{
		"active=true": 1, "active=false": 0, "telecom=555-0100": 1, "telecom=phone|555-0100": 1, "telecom=email|555-0100": 0,
		"address-city=boston": 1, "address-state=MA": 1, "address-postalcode=02134": 1, "address=Bost": 1, "address=Chicago": 0,
	} {
		rec := do(t, h, http.MethodGet, "/Patient?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", query, rec.Code, rec.Body)
			continue
		}
		if got := len(bundleEntries(t, rec)); got != want {
			t.Errorf("%s: %d found, want %d", query, got, want)
		}
	}

	doc := `{"resourceType":"Bundle","id":"toc-1","type":"document","identifier":{"system":"urn:ietf:rfc:3986","value":"urn:uuid:1"},
	  "timestamp":"2026-10-26T12:00:00Z","entry":[
	  {"fullUrl":"urn:uuid:c1","resource":{"resourceType":"Composition","id":"c1","status":"final",
	    "type":{"coding":[{"system":"http://loinc.org","code":"18761-7"}]},"date":"2026-10-26","title":"Transfer summary",
	    "author":[{"display":"Hospital"}],"subject":{"reference":"urn:uuid:p1"}}},
	  {"fullUrl":"urn:uuid:p1","resource":{"resourceType":"Patient","id":"p1","identifier":[{"system":"urn:x","value":"1"}]}}]}`
	if rec := do(t, h, http.MethodPut, "/Bundle/toc-1", doc); rec.Code >= 300 {
		t.Fatalf("PUT document: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodGet, "/Bundle/toc-1", nil)
	if rec.Code != http.StatusOK || len(bundleEntries(t, rec)) != 2 {
		t.Fatalf("read document back: %d %s", rec.Code, rec.Body)
	}
	for query, want := range map[string]int{"type=document": 1, "composition=c1": 1, "identifier=urn:uuid:1": 1, "type=collection": 0} {
		rec := do(t, h, http.MethodGet, "/Bundle?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("Bundle?%s: %d %s", query, rec.Code, rec.Body)
		} else if got := len(bundleEntries(t, rec)); got != want {
			t.Errorf("Bundle?%s: %d found, want %d", query, got, want)
		}
	}
	// A transaction PUT is kept as a record and not applied: its Patient is not created.
	tx := `{"resourceType":"Bundle","id":"tx","type":"transaction","entry":[{"fullUrl":"urn:uuid:p9",
	  "resource":{"resourceType":"Patient","id":"p9","identifier":[{"system":"urn:x","value":"9"}]},"request":{"method":"PUT","url":"Patient/p9"}}]}`
	if rec := do(t, h, http.MethodPut, "/Bundle/tx", tx); rec.Code >= 300 {
		t.Errorf("a transaction Bundle was not kept: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodGet, "/Patient/p9", nil); rec.Code != http.StatusNotFound {
		t.Errorf("storing a transaction applied it: Patient/p9 answered %d", rec.Code)
	}
	// A search result is the server's answer, not a record.
	if rec := do(t, h, http.MethodPut, "/Bundle/s", `{"resourceType":"Bundle","id":"s","type":"searchset"}`); rec.Code < 400 {
		t.Errorf("a searchset Bundle was stored: %d", rec.Code)
	}
	// Lists by what they are and whose they are, as PACIO's medication profile searches them.
	list := `{"resourceType":"List","id":"meds","status":"current","mode":"snapshot","subject":{"reference":"Patient/betsy"},
	  "code":{"coding":[{"system":"http://loinc.org","code":"104202-7"}]}}`
	if rec := do(t, h, http.MethodPut, "/List/meds", list); rec.Code >= 300 {
		t.Fatalf("PUT List: %d %s", rec.Code, rec.Body)
	}
	for query, want := range map[string]int{"code=104202-7&patient=betsy": 1, "code=http://loinc.org|104202-7": 1, "code=1-1": 0} {
		rec := do(t, h, http.MethodGet, "/List?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("List?%s: %d %s", query, rec.Code, rec.Body)
		} else if got := len(bundleEntries(t, rec)); got != want {
			t.Errorf("List?%s: %d found, want %d", query, got, want)
		}
	}
}

// A guide the operator declares appears in the capability statement: its CapabilityStatement in instantiates, a profile in
// its type's supportedProfile, beside US Core's.
func TestDeclaredGuidesAppearInTheCapabilityStatement(t *testing.T) {
	srv, h := newTestServer(t)
	srv.Declares = []string{"http://hl7.org/fhir/us/pacio-toc/CapabilityStatement/toc",
		"Composition=http://hl7.org/fhir/us/pacio-toc/StructureDefinition/TOC-Composition"}
	rec := do(t, h, http.MethodGet, "/metadata", nil)
	body := rec.Body.String()
	for _, want := range []string{`"http://hl7.org/fhir/us/pacio-toc/CapabilityStatement/toc"`,
		`"http://hl7.org/fhir/us/core/CapabilityStatement/us-core-server"`,
		`"http://hl7.org/fhir/us/pacio-toc/StructureDefinition/TOC-Composition"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the capability statement lacks %s", want)
		}
	}
}

// PACIO's advance-directive kit finds a patient's documents by who keeps them.
func TestDocumentsSearchedByCustodian(t *testing.T) {
	_, h := newTestServer(t)
	doc := `{"resourceType":"DocumentReference","id":"adi-1","status":"current","subject":{"reference":"Patient/betsy"},
	  "custodian":{"reference":"Organization/good-health"},"content":[{"attachment":{"contentType":"application/pdf","url":"Binary/x"}}]}`
	if rec := do(t, h, http.MethodPut, "/DocumentReference/adi-1", doc); rec.Code >= 300 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	for query, want := range map[string]int{"custodian=Organization/good-health": 1, "custodian=good-health&patient=betsy": 1, "custodian=other": 0} {
		rec := do(t, h, http.MethodGet, "/DocumentReference?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", query, rec.Code, rec.Body)
		} else if got := len(bundleEntries(t, rec)); got != want {
			t.Errorf("%s: %d found, want %d", query, got, want)
		}
	}
}
