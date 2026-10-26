package fhir

import (
	"strings"
	"testing"
)

// A document Bundle read as any resource keeps its entries, each as its own type. It used to fail on the first entry, so the
// FHIR server refused to store a document.
func TestABundleReadAsAResourceKeepsItsEntries(t *testing.T) {
	doc := []byte(`{"resourceType":"Bundle","id":"toc-1","type":"document","identifier":{"system":"urn:ietf:rfc:3986","value":"urn:uuid:1"},
	  "timestamp":"2026-10-26T12:00:00Z",
	  "entry":[{"fullUrl":"urn:uuid:c1","resource":{"resourceType":"Composition","id":"c1","status":"final",
	    "type":{"coding":[{"system":"http://loinc.org","code":"18761-7"}]},"date":"2026-10-26","title":"Transfer summary",
	    "author":[{"display":"Hospital"}]}},
	   {"fullUrl":"urn:uuid:p1","resource":{"resourceType":"Patient","id":"p1","identifier":[{"system":"urn:x","value":"1"}]}}]}`)
	r, err := UnmarshalResource(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, ok := r.(*Bundle)
	if !ok || len(b.Entry) != 2 {
		t.Fatalf("got %T with %d entries", r, len(b.Entry))
	}
	if _, ok := b.Entry[0].Resource.(*Composition); !ok {
		t.Errorf("first entry is a %T, want a Composition", b.Entry[0].Resource)
	}
	if b.Entry[1].Resource.ResourceTypeName() != "Patient" {
		t.Errorf("second entry is a %s", b.Entry[1].Resource.ResourceTypeName())
	}
}

// A stored document read back as R4 has R4 resources inside it, and keeps what the Bundle struct does not model.
func TestABundleWrittenAsR4ConvertsItsEntries(t *testing.T) {
	doc := []byte(`{"resourceType":"Bundle","id":"toc","type":"document","language":"en-US",
	  "signature":{"type":[{"system":"urn:iso-astm:E1762-95:2013","code":"1.2.840.10065.1.12.1.1"}],"when":"2026-10-26T12:00:00Z","who":{"display":"Dr A"}},
	  "entry":[{"fullUrl":"urn:uuid:e1","resource":{"resourceType":"Encounter","id":"e1","status":"finished",
	    "class":{"system":"http://terminology.hl7.org/CodeSystem/v3-ActCode","code":"HH"},
	    "participant":[{"individual":{"reference":"PractitionerRole/r1"}}]}}]}`)
	r, err := UnmarshalResource(doc)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(r, R4)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"class":{"code":"HH","system":"http://terminology.hl7.org/CodeSystem/v3-ActCode"}`,
		`"individual":{"reference":"PractitionerRole/r1"}`, `"signature":`, `"language":"en-US"`, `"status":"finished"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the R4 document lacks %s:\n%s", want, s)
		}
	}
}
