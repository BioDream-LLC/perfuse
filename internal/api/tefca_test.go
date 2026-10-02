package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/tefca"
)

// TEFCA, reachable from the interface at last.
//
// The whole package had no caller anywhere: 346 lines of implementation and 552 of tests that nothing outside it
// imported. These tests exist so that cannot silently become true again.

func tefcaHarness(t *testing.T) (*harness, *tefca.PersistentAuditLog) {
	t.Helper()

	h := newHarness(t)

	audit, err := tefca.OpenAuditLog(filepath.Join(h.dir, "tefca-audit.jsonl"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close() })

	p, err := tefca.NewParticipant(tefca.TEFCAConfig{
		OrganizationName:  "Example Hospital",
		OrganizationOID:   "2.16.840.1.113883.19.5",
		QHINEndpoint:      "https://qhin.example/fhir",
		ParticipantType:   "provider",
		CertificatePath:   "/etc/perfuse/tefca.pem",
		KeyPath:           "/etc/perfuse/tefca.key",
		SupportedPurposes: []string{tefca.PurposeTreatment, tefca.PurposeIndividualAccess},
	}, &audit.AuditLog)
	if err != nil {
		t.Fatal(err)
	}

	h.server.TEFCA = p
	h.server.TEFCAAudit = audit
	return h, audit
}

// An instance that does not participate must say so, not fail.
//
// Most instances do not take part in TEFCA. Reporting the absence as an error would make every ordinary installation
// look misconfigured, and a section full of red on a server that is working correctly teaches people to ignore it.
func TestAnInstanceThatDoesNotParticipateSaysSoRatherThanFailing(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/tefca/status", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	var out struct {
		Configured  bool     `json:"configured"`
		Explanation string   `json:"explanation"`
		Purposes    []string `json:"purposes"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Configured {
		t.Error("an instance with no TEFCA configuration reported that it participates")
	}
	if out.Explanation == "" {
		t.Error("nothing explained what TEFCA is or what taking part would need")
	}
	// Never null, because the interface reads a length from it.
	if out.Purposes == nil {
		t.Error("purposes is null rather than an empty array")
	}
}

// A configured participant must report its identity, so somebody can check it against the agreement.
func TestAConfiguredParticipantReportsItsIdentity(t *testing.T) {
	h, _ := tefcaHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/tefca/status", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Configured      bool     `json:"configured"`
		Organisation    string   `json:"organisation"`
		OID             string   `json:"oid"`
		ParticipantType string   `json:"participantType"`
		Purposes        []string `json:"purposes"`
		AllPurposes     []string `json:"allPurposes"`
		Trail           *struct {
			Path     string `json:"path"`
			Writable bool   `json:"writable"`
		} `json:"trail"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if !out.Configured {
		t.Fatal("a configured participant reported that it does not participate")
	}
	if out.Organisation != "Example Hospital" || out.OID != "2.16.840.1.113883.19.5" {
		t.Errorf("the identity is wrong: %+v", out)
	}
	if len(out.Purposes) != 2 {
		t.Errorf("%d purposes reported, want 2", len(out.Purposes))
	}

	// Every purpose that exists, so the interface can show which are declared and which are not without hard-coding a
	// list that would drift from the package.
	if len(out.AllPurposes) != 5 {
		t.Errorf("%d purposes exist according to the response, want 5", len(out.AllPurposes))
	}

	// The state of the trail, because a participant whose audit file has stopped being written is out of compliance and
	// nothing else on the screen would say so.
	if out.Trail == nil {
		t.Fatal("nothing was reported about the audit trail")
	}
	if !out.Trail.Writable {
		t.Error("a writable trail was reported as not writable")
	}
	if out.Trail.Path == "" {
		t.Error("the trail does not say where it is written, so nobody can find it or back it up")
	}
}

// The audit trail must be readable through the interface, or nobody checks it.
func TestTheAuditTrailIsReadableThroughTheInterface(t *testing.T) {
	h, audit := tefcaHarness(t)

	audit.Record(tefca.TEFCAAudit{
		Direction: "outbound", ExchangeType: "query", Purpose: tefca.PurposeTreatment,
		PatientID: "P12345", RequestingOrg: "Example Hospital", Success: true,
	})
	audit.Record(tefca.TEFCAAudit{
		Direction: "outbound", ExchangeType: "delivery", Purpose: tefca.PurposeTreatment,
		RespondingOrg: "Other Hospital", RequestingOrg: "Example Hospital",
		Success: false, ErrorDetail: "the recipient did not accept it",
	})

	res := h.do("viewer", http.MethodGet, "/api/tefca/audit", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		Entries []struct {
			ExchangeType string `json:"exchangeType"`
			Purpose      string `json:"purpose"`
			PatientID    string `json:"patientId"`
			Success      bool   `json:"success"`
			ErrorDetail  string `json:"errorDetail"`
		} `json:"entries"`
		Total   int `json:"total"`
		Summary struct {
			Total          int            `json:"total"`
			Failed         int            `json:"failed"`
			ByType         map[string]any `json:"byType"`
			WithoutPurpose int            `json:"withoutPurpose"`
			WorstOrg       string         `json:"worstOrg"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if out.Total != 2 {
		t.Fatalf("%d entries returned, want 2", out.Total)
	}

	// Newest first, because somebody opening an audit view is nearly always asking what happened recently.
	if out.Entries[0].ExchangeType != "delivery" {
		t.Errorf("the newest entry is %q; the trail is not newest first", out.Entries[0].ExchangeType)
	}

	if out.Summary.Failed != 1 {
		t.Errorf("%d failures counted, want 1", out.Summary.Failed)
	}

	// The partner responsible for the failures is named, because a run of failures against one organisation is the
	// finding and it is invisible in a total.
	if out.Summary.WorstOrg != "Other Hospital" {
		t.Errorf("the failing partner is reported as %q", out.Summary.WorstOrg)
	}
}

// An exchange recorded with no purpose of use is a compliance gap and must be counted.
func TestExchangesWithNoPurposeAreCounted(t *testing.T) {
	h, audit := tefcaHarness(t)

	audit.Record(tefca.TEFCAAudit{
		Direction: "outbound", ExchangeType: "notification", RequestingOrg: "Example Hospital", Success: true,
	})

	res := h.do("viewer", http.MethodGet, "/api/tefca/audit", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d", res.Code)
	}

	var out struct {
		Summary struct {
			WithoutPurpose int `json:"withoutPurpose"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary.WithoutPurpose != 1 {
		t.Errorf("%d exchanges without a purpose counted, want 1", out.Summary.WithoutPurpose)
	}
}

// A backwards range must be refused rather than silently swapped.
//
// Swapping would return results for a range nobody asked for, and an audit answer to a question that was not asked is
// worse than a refusal.
func TestABackwardsAuditRangeIsRefused(t *testing.T) {
	h, _ := tefcaHarness(t)

	res := h.do("viewer", http.MethodGet,
		"/api/tefca/audit?from=2026-08-26T00:00:00Z&to=2026-08-01T00:00:00Z", nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
}

// An unreadable timestamp must be refused with an example.
func TestAnUnparseableAuditRangeIsRefusedWithAnExample(t *testing.T) {
	h, _ := tefcaHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/tefca/audit?from=last+tuesday", nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "2026") {
		t.Errorf("the refusal gives no example of the format: %s", res.Body.String())
	}
}

// Checking a purpose must distinguish unrecognised from undeclared.
//
// Two different problems with two different fixes. An unrecognised purpose is a spelling error or an invention and no
// configuration will make it work; a recognised one that is not declared is a gap this site can close.
func TestCheckingAPurposeDistinguishesUnrecognisedFromUndeclared(t *testing.T) {
	h, _ := tefcaHarness(t)

	cases := []struct {
		purpose        string
		wantRecognised bool
		wantDeclared   bool
	}{
		{tefca.PurposeTreatment, true, true},
		{tefca.PurposePayment, true, false},
		{"research", false, false},
	}

	for _, tc := range cases {
		res := h.do("viewer", http.MethodPost, "/api/tefca/purpose-check",
			map[string]any{"purpose": tc.purpose})
		if res.Code != http.StatusOK {
			t.Fatalf("checking %q returned %d: %s", tc.purpose, res.Code, res.Body.String())
		}

		var out struct {
			Recognised  bool   `json:"recognised"`
			Declared    bool   `json:"declared"`
			Acceptable  bool   `json:"acceptable"`
			Explanation string `json:"explanation"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}

		if out.Recognised != tc.wantRecognised {
			t.Errorf("%q recognised=%v, want %v", tc.purpose, out.Recognised, tc.wantRecognised)
		}
		if out.Declared != tc.wantDeclared {
			t.Errorf("%q declared=%v, want %v", tc.purpose, out.Declared, tc.wantDeclared)
		}
		if out.Acceptable != (tc.wantRecognised && tc.wantDeclared) {
			t.Errorf("%q acceptable=%v", tc.purpose, out.Acceptable)
		}
		if out.Explanation == "" {
			t.Errorf("%q got no explanation, so nobody knows what to do about it", tc.purpose)
		}
	}
}

// An empty purpose must be refused rather than answered.
func TestCheckingAnEmptyPurposeIsRefused(t *testing.T) {
	h, _ := tefcaHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/tefca/purpose-check", map[string]any{"purpose": "  "})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
}

// The audit trail must be readable by viewers.
//
// Knowing what was disclosed about a patient is not privileged information within an organisation, and an audit trail
// only one person can read is an audit trail nobody checks.
func TestTheAuditTrailIsReadableByViewers(t *testing.T) {
	h, _ := tefcaHarness(t)

	for _, role := range []string{"viewer", "editor", "admin"} {
		res := h.do(role, http.MethodGet, "/api/tefca/audit", nil)
		if res.Code != http.StatusOK {
			t.Errorf("%s could not read the audit trail (%d)", role, res.Code)
		}
	}
}

// Signing out must still be required. The trail names patients.
func TestTheAuditTrailRequiresASession(t *testing.T) {
	h, _ := tefcaHarness(t)

	res := h.do("", http.MethodGet, "/api/tefca/audit", nil)
	if res.Code == http.StatusOK {
		t.Error("the audit trail was readable with no session, and it names patients")
	}
}

// Slices must be arrays, never null.
func TestTefcaSlicesAreArraysWhenEmpty(t *testing.T) {
	h, _ := tefcaHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/tefca/audit", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d", res.Code)
	}
	if strings.Contains(res.Body.String(), `"entries":null`) {
		t.Error("entries is null rather than an empty array")
	}
}

// A configured participant must say that it cannot actually exchange.
//
// This is the assertion that makes the honesty reachable rather than only true. A participant with a valid configuration and no
// transport looks identical to a working one from this screen: the organisation, the OID, the endpoint and the declared purposes are
// all present and all correct. The difference is whether anything is ever sent, and an operator who believes exchange is running does
// not go looking for why no records arrive - while the audit trail, which is real, fills with failures nobody is watching.
//
// The field is asserted rather than the sentence, because the interface decides the wording and the server decides the fact.
func TestAConfiguredParticipantSaysItCannotYetExchange(t *testing.T) {
	h, _ := tefcaHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/tefca/status", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if body["configured"] != true {
		t.Fatalf("the harness participant did not report as configured, so this test is not reading the branch it means to: %v",
			body["configured"])
	}

	implemented, present := body["exchangeImplemented"]
	if !present {
		t.Fatal("the status does not say whether this build can exchange, so a configured participant with no transport is " +
			"indistinguishable from a working one")
	}
	if implemented != false {
		t.Errorf("exchangeImplemented = %v, want false - if a transport has been added, this test and the screen notice both "+
			"need removing rather than adjusting", implemented)
	}

	explanation, _ := body["exchangeExplanation"].(string)
	if !strings.Contains(explanation, "QHIN") {
		t.Errorf("the explanation does not name what is missing: %q", explanation)
	}
	if !strings.Contains(explanation, "audit trail") {
		t.Errorf("the explanation does not say what still works, which is what an operator needs in order to decide whether "+
			"to keep this configured: %q", explanation)
	}

	// And the other transport, which does exist now.
	//
	// Reported separately on purpose. One flag covering both would be wrong in whichever direction it was rounded: false would deny a
	// transport that is built and verified against a third-party reference server, and true would imply an exchange that has never
	// spoken to a real QHIN.
	facilitated, present := body["facilitatedFHIRImplemented"]
	if !present {
		t.Fatal("the status says nothing about Facilitated FHIR, so an operator cannot tell which transport this build has")
	}

	if facilitated != true {
		t.Errorf("facilitatedFHIRImplemented = %v, want true", facilitated)
	}

	// The claim has to be bounded. "Implemented" on its own would be read as working with our partners, and it is not - what has been
	// verified is that a live reference server reads the documents this build signs and refuses them on community membership.
	facilitatedWhy, _ := body["facilitatedFHIRExplanation"].(string)
	for _, want := range []string{"reference server", "QHIN", "onboarding"} {
		if !strings.Contains(facilitatedWhy, want) {
			t.Errorf("the Facilitated FHIR explanation does not mention %q, so it overstates what has been proved: %q",
				want, facilitatedWhy)
		}
	}
}
