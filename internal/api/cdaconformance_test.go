package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A document with no recordTarget and no author: two conformance failures that cause outright rejection.
//
// Deliberately not the well-formed sample from the cda package tests. What is being proved here is that the
// interface can reach the conformance checker at all, and a document that passes every check proves nothing about
// whether the report arrived.
const nonConformantCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1" displayName="Summarization of Episode Note"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Allergies</title>
      <text>No known allergies.</text>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`

// Conformance checking must be reachable from the interface.
//
// The checker was written, tested and had no caller. An endpoint or a package function with no caller is
// indistinguishable from a feature that does not exist: the code is correct, the tests pass, and nobody can use
// it. This test fails if the report stops being returned, which is the only way to keep that from recurring.
func TestConformanceIsReportedWhenInspectingADocument(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/inspect/document", map[string]any{"document": nonConformantCDA})
	if res.Code != http.StatusOK {
		t.Fatalf("inspecting a document returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Found      bool `json:"found"`
		Validation *struct {
			Profile  string `json:"profile"`
			Errors   int    `json:"errors"`
			Findings []struct {
				Severity string `json:"severity"`
				Message  string `json:"message"`
				Kind     string `json:"kind"`
			} `json:"findings"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("the response is not readable: %v", err)
	}

	if !out.Found {
		t.Fatal("the document was not recognised")
	}
	if out.Validation == nil {
		t.Fatal("no conformance report was returned, so the checker is still unreachable from the interface")
	}

	// The profile has to be named. A finding nobody can attribute to a guide is a finding nobody can argue with a
	// supplier about, and "your document is wrong" is not a conversation that goes anywhere.
	if !strings.Contains(out.Validation.Profile, "C-CDA") {
		t.Errorf("the report does not say which guide was checked, it says %q", out.Validation.Profile)
	}

	if out.Validation.Errors == 0 {
		t.Fatalf("a document with no recordTarget passed conformance; findings were %+v", out.Validation.Findings)
	}

	// Each finding must carry a rule name, otherwise it cannot be looked up or suppressed by name.
	for _, f := range out.Validation.Findings {
		if strings.TrimSpace(f.Message) == "" {
			t.Error("a finding has no message")
		}
	}
}

// A conformant document must not be reported as broken, or the report is noise and gets ignored.
func TestConformanceIsQuietOnADocumentThatWouldBeAccepted(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/inspect/document", map[string]any{"document": conformantCDA})
	if res.Code != http.StatusOK {
		t.Fatalf("inspecting a document returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Validation *struct {
			Errors   int `json:"errors"`
			Findings []struct {
				Severity string `json:"severity"`
				Message  string `json:"message"`
			} `json:"findings"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Validation == nil {
		t.Fatal("no conformance report was returned")
	}

	for _, f := range out.Validation.Findings {
		if f.Severity == "error" {
			t.Errorf("a document that should be accepted was reported as rejectable: %s", f.Message)
		}
	}
}

// A document carrying the header a receiver requires.
const conformantCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <realmCode code="US"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <id root="2.16.840.1.113883.19.5.99999.1" extension="DOC-1"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1" displayName="Summarization of Episode Note"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <confidentialityCode code="N" codeSystem="2.16.840.1.113883.5.25"/>
  <languageCode code="en-US"/>
  <recordTarget><patientRole>
    <id root="2.16.840.1.113883.19.5" extension="PT-1"/>
    <patient>
      <name><given>Ada</given><family>Lovelace</family></name>
      <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/>
      <birthTime value="19151210"/>
    </patient>
  </patientRole></recordTarget>
  <author>
    <time value="20260818120000-0500"/>
    <assignedAuthor>
      <id root="2.16.840.1.113883.4.6" extension="1234567890"/>
      <assignedPerson><name><given>Grace</given><family>Hopper</family></name></assignedPerson>
    </assignedAuthor>
  </author>
  <custodian><assignedCustodian><representedCustodianOrganization>
    <id root="2.16.840.1.113883.19.5"/>
    <name>Example Hospital</name>
  </representedCustodianOrganization></assignedCustodian></custodian>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Allergies</title>
      <text>No known allergies.</text>
    </section></component>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.1.1"/>
      <code code="10160-0" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Medications</title>
      <text>No medications.</text>
    </section></component>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.5.1"/>
      <code code="11450-4" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Problems</title>
      <text>No known problems.</text>
    </section></component>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.3.1"/>
      <code code="30954-2" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Results</title>
      <text>No results in this period.</text>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`
