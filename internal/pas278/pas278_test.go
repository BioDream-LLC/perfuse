package pas278

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/x12"
)

// Tests for the prior authorisation bridge.
//
// The ones that matter are about what must not be lost. A converter that drops a field is found by a round trip; a converter that
// upgrades a decision is not, because the output is well-formed and plausible - so those cases are asserted individually and by name.

const reviewISA = "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PROVIDER       " +
	"*260916*1500*^*00501*000000404*0*P*:~"

// response builds a 278 response carrying one decision.
func response(action, authNumber, reasonCode string) string {
	hcr := "HCR*" + action + "*" + authNumber + "*" + reasonCode
	if action == "" {
		hcr = "AAA*N**15*C"
	}

	return reviewISA +
		"GS*HI*PAYER*PROVIDER*20260916*1500*404*X*005010X217~" +
		"ST*278*0001*005010X217~" +
		"BHT*0007*11*REQ-9*20260916*1500*11~" +
		"HL*1**20*1~" +
		"NM1*X3*2*ACME HEALTH PLAN*****PI*ACME01~" +
		"HL*2*1*21*1~" +
		"NM1*1P*2*RIVERSIDE ORTHOPAEDICS*****XX*1234567893~" +
		"HL*3*2*22*1~" +
		"NM1*IL*1*TURNER*ROSALIND****MI*MEM88771~" +
		"HL*4*3*EV*1~" +
		"TRN*2*AUTHREQ-4471~" +
		"UM*HS*I*4~" +
		"DTP*472*D8*20261001~" +
		"HI*BK:M1711~" +
		// The event's decision sits in the EV loop, before any service loop opens. Putting it after the SS loop - which the first
		// version of this fixture did - attaches it to the line instead, and every event then reads as having no decision. The
		// parser was right and the fixture was wrong, which is the dangerous way round.
		hcr + "~" +
		"HL*5*4*SS*0~" +
		"SV1*HC:29881*450.00*UN*1~" +
		"SE*15*0001~GE*1*404~IEA*1*000000404~"
}

func convert(t *testing.T, raw string) *Result {
	t.Helper()

	m, err := x12.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	review, err := m.ParseServiceReview()
	if err != nil {
		t.Fatalf("parse review: %v", err)
	}

	return FromReview(review)
}

func TestACertifiedReviewCarriesEverythingAClaimNeeds(t *testing.T) {
	result := convert(t, response("A1", "AUTH-99120", ""))

	if len(result.Authorisations) != 1 {
		t.Fatalf("got %d authorisations, want 1", len(result.Authorisations))
	}
	a := result.Authorisations[0]

	if a.Decision != Certified {
		t.Errorf("decision %q, want certified", a.Decision)
	}
	if a.Number != "AUTH-99120" {
		t.Errorf("authorisation number %q", a.Number)
	}
	if a.TraceNumber != "AUTHREQ-4471" {
		t.Errorf("trace number %q, which is the only reliable join back to the request", a.TraceNumber)
	}
	if a.PatientName != "ROSALIND TURNER" || a.PatientID != "MEM88771" {
		t.Errorf("patient %q / %q", a.PatientName, a.PatientID)
	}
	if a.RequesterName != "RIVERSIDE ORTHOPAEDICS" || a.ReviewerName != "ACME HEALTH PLAN" {
		t.Errorf("requester %q reviewer %q", a.RequesterName, a.ReviewerName)
	}
	if a.ServiceCode != "HC:29881" {
		t.Errorf("service code %q", a.ServiceCode)
	}
	if len(a.DiagnosisCodes) != 1 || a.DiagnosisCodes[0] != "M1711" {
		t.Errorf("diagnoses %v", a.DiagnosisCodes)
	}

	// A clean conversion should need no judgement, so an unexpected note here means something was decided quietly.
	if len(result.Notes) != 0 {
		t.Errorf("a straightforward certification produced notes: %v", result.Notes)
	}
}

func TestNoDecisionIsEverUpgraded(t *testing.T) {
	// The whole reason this package has its own vocabulary. Every one of these collapses is available in the obvious mapping, and each
	// is wrong in the direction that costs somebody weeks.
	for _, tc := range []struct {
		action string
		want   Decision
	}{
		{"A1", Certified},
		{"A2", PartiallyOK},
		{"A3", Denied},
		{"A4", Pending},
		{"A6", Modified},
	} {
		t.Run(tc.action, func(t *testing.T) {
			result := convert(t, response(tc.action, "AUTH-1", ""))

			if got := result.Authorisations[0].Decision; got != tc.want {
				t.Errorf("action %s became %q, want %q", tc.action, got, tc.want)
			}
			if tc.want == PartiallyOK && result.Authorisations[0].Decision == Certified {
				t.Error("partial certification was reported as full certification")
			}
		})
	}
}

func TestARefusalToConsiderIsNotConvertedIntoADenial(t *testing.T) {
	// The most expensive confusion in the transaction, carried across the bridge intact. A denial is appealed; a refusal is corrected
	// and resent. Getting this wrong sends a practice to appeal something nobody ruled on.
	result := convert(t, response("", "", ""))

	a := result.Authorisations[0]
	if a.Decision != NotConsidered {
		t.Fatalf("decision %q, want not-considered", a.Decision)
	}
	if a.Decision == Denied {
		t.Fatal("a refusal to consider was converted into a denial")
	}

	// The payer's own advice on whether it is recoverable, which implementations reading only the reason code drop.
	if a.FollowUpAction != "C" {
		t.Errorf("follow-up action %q, want C", a.FollowUpAction)
	}

	// And a note saying so in words, because this is the case most likely to be mishandled by whatever reads the output.
	if !containsAny(result.Notes, "corrected request", "send it again") {
		t.Errorf("nothing tells the reader to resubmit rather than appeal: %v", result.Notes)
	}
}

func TestACertifiedReviewWithNoAuthorisationNumberIsFlagged(t *testing.T) {
	// A certified service with no number produces a claim denied for lacking one, and that denial reads as clinical to everybody
	// downstream. Nothing else in the transaction says this is wrong, so the conversion has to.
	result := convert(t, response("A1", "", ""))

	if result.Authorisations[0].Decision != Certified {
		t.Fatalf("decision %q", result.Authorisations[0].Decision)
	}
	if !containsAny(result.Notes, "no authorisation number") {
		t.Errorf("a certification with no number was not flagged: %v", result.Notes)
	}
}

func TestAnUnrecognisedActionCodeIsNotTreatedAsPending(t *testing.T) {
	result := convert(t, response("ZZ", "AUTH-1", ""))

	if got := result.Authorisations[0].Decision; got != Unknown {
		t.Errorf("decision %q, want unknown", got)
	}
	if !containsAny(result.Notes, "does not recognise") {
		t.Errorf("an unrecognised code produced no note: %v", result.Notes)
	}
}

func TestNarrowingSaysWhatItLoses(t *testing.T) {
	// Narrowing into the three-state vocabulary is sometimes right. It must be a choice with the loss visible, rather than something
	// that happens quietly inside a converter.
	for _, tc := range []struct {
		in       Decision
		decision string
		lossy    bool
	}{
		{Certified, "approved", false},
		{PartiallyOK, "approved", true},
		{Modified, "approved", true},
		{Denied, "denied", false},
		{NotConsidered, "denied", true},
		{Pending, "pended", false},
		{Unknown, "pended", true},
	} {
		t.Run(string(tc.in), func(t *testing.T) {
			decision, lost := Narrow(tc.in)
			if decision != tc.decision {
				t.Errorf("narrowed to %q, want %q", decision, tc.decision)
			}
			if tc.lossy && lost == "" {
				t.Error("something was lost and nothing said what")
			}
			if !tc.lossy && lost != "" {
				t.Errorf("nothing should have been lost, and the loss reported was %q", lost)
			}
		})
	}

	// The one that matters most: narrowing a refusal produces "denied", and the loss has to name the opposite action.
	_, lost := Narrow(NotConsidered)
	if !strings.Contains(lost, "resubmission") {
		t.Errorf("narrowing a refusal to consider does not warn that the action is a resubmission: %q", lost)
	}
}

func TestTheClaimResponseCarriesTheAuthorisationNumberWhereAPayerLooksForIt(t *testing.T) {
	result := convert(t, response("A1", "AUTH-99120", ""))
	res := ToClaimResponse(result)

	// Marshalled and re-read, so the assertions run against what would actually go on the wire rather than against a Go map that
	// might not survive encoding.
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)

	if !strings.Contains(body, `"resourceType":"ClaimResponse"`) {
		t.Errorf("not a ClaimResponse: %s", body)
	}
	if !strings.Contains(body, `"use":"preauthorization"`) {
		t.Errorf("the response is not marked as a preauthorisation: %s", body)
	}
	if !strings.Contains(body, "extension-authorizationNumber") || !strings.Contains(body, "AUTH-99120") {
		t.Errorf("the authorisation number is not in the PAS extension a payer reads: %s", body)
	}

	// The X12 action code is carried rather than translated into a vocabulary of our own. PAS defines this exact reuse, and inventing
	// a different code system would lose the only value a payer's system can match on.
	if !strings.Contains(body, `"code":"A1"`) {
		t.Errorf("the X12 action code was not carried into the adjudication: %s", body)
	}
}

func TestTheClaimResponseOutcomeReflectsTheWholeSetRatherThanTheFirstItem(t *testing.T) {
	// A mixed set is "partial", and reporting the first item's state for all of them is the easy mistake. Built by hand rather than
	// from a transaction, because a 278 carrying two events with different decisions is awkward to write and the mapping is what is
	// under test.
	for _, tc := range []struct {
		name    string
		in      []Decision
		outcome string
	}{
		{"all certified", []Decision{Certified, Certified}, "complete"},
		{"mixed", []Decision{Certified, Denied}, "partial"},
		{"all pending", []Decision{Pending}, "queued"},
		{"not considered", []Decision{NotConsidered}, "error"},
		{"nothing at all", nil, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := &Result{}
			for _, d := range tc.in {
				result.Authorisations = append(result.Authorisations, Authorisation{Decision: d})
			}

			res := ToClaimResponse(result)
			if got := res["outcome"]; got != tc.outcome {
				t.Errorf("outcome %v, want %v", got, tc.outcome)
			}
		})
	}
}

func TestARefusalRendersNoActionCodeRatherThanADenialCode(t *testing.T) {
	// In X12 a refusal is not an action - it is the absence of one, carried in AAA. Rendering it as A3 would assert a decision the
	// payer never made, in a field a payer's system will read as authoritative.
	if got := actionCodeFor(NotConsidered); got != "" {
		t.Errorf("a refusal to consider rendered as action code %q", got)
	}

	result := convert(t, response("", "", ""))
	data, err := json.Marshal(ToClaimResponse(result))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"code":"A3"`) {
		t.Errorf("a refusal to consider was rendered as a denial: %s", data)
	}

	// And the reason has to travel, because the resource is what the payer's system reads and a note living only in a Go struct
	// reaches nobody.
	if !strings.Contains(string(data), "processNote") {
		t.Errorf("the notes did not reach the resource: %s", data)
	}
}

func TestTheNotesReachTheResource(t *testing.T) {
	result := convert(t, response("A1", "", ""))

	data, err := json.Marshal(ToClaimResponse(result))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "no authorisation number") {
		t.Errorf("the flag about a missing authorisation number did not reach the resource: %s", data)
	}
}

func TestSeveralServiceLinesUnderOneEventAreReportedRatherThanDropped(t *testing.T) {
	// A FHIR item carries one product or service, so several lines need several items. A converter using only the first would
	// authorise one procedure out of three with no indication that it had.
	raw := strings.Replace(response("A1", "AUTH-1", ""),
		"HL*5*4*SS*0~"+"SV1*HC:29881*450.00*UN*1~",
		"HL*5*4*SS*0~SV1*HC:29881*450.00*UN*1~HL*6*4*SS*0~SV1*HC:29882*300.00*UN*1~", 1)

	result := convert(t, raw)

	if !containsAny(result.Notes, "service lines and only the first") {
		t.Errorf("extra service lines were dropped without a note: %v", result.Notes)
	}
}

func TestConvertingNothingSaysSoRatherThanSucceeding(t *testing.T) {
	if result := FromReview(nil); len(result.Notes) == 0 {
		t.Error("converting a nil review reported no problem")
	}

	// A 278 with no event loop is malformed in a way the envelope checks do not catch, and an empty result looks like a transaction
	// that asked for nothing.
	empty := &x12.ServiceReview{}
	if result := FromReview(empty); len(result.Notes) == 0 {
		t.Error("converting a review with no events reported no problem")
	}
}

func TestTheSummaryIsStableAcrossRuns(t *testing.T) {
	// Go maps range randomly and this is user-visible, which is a standing invariant here. A summary that reorders between runs turns
	// every diff of two logs into noise.
	result := &Result{Authorisations: []Authorisation{
		{Decision: Certified}, {Decision: Denied}, {Decision: Certified}, {Decision: Pending},
	}}

	first := SummariseDecisions(result)
	for i := 0; i < 20; i++ {
		if got := SummariseDecisions(result); got != first {
			t.Fatalf("the summary changed between runs: %q then %q", first, got)
		}
	}

	if !strings.Contains(first, "2 certified") {
		t.Errorf("the summary does not count correctly: %q", first)
	}
}

func containsAny(notes []string, wants ...string) bool {
	for _, n := range notes {
		for _, w := range wants {
			if strings.Contains(n, w) {
				return true
			}
		}
	}

	return false
}

func TestADecisionCarriedOnlyAtLineLevelIsNotReportedAsPending(t *testing.T) {
	// A payer answering per service can leave the event loop without an HCR. Reading only the event level reports that as pending,
	// which is a silent wrong answer in the worst direction: a practice waits for a decision it already has, and the approval expires
	// unused.
	//
	// Built by removing the event's HCR from a response that has one, so the only difference from the passing case is where the
	// decision sits.
	raw := strings.Replace(response("A1", "AUTH-99120", ""), "HCR*A1*AUTH-99120*~", "", 1)
	raw = strings.Replace(raw, "SV1*HC:29881*450.00*UN*1~", "SV1*HC:29881*450.00*UN*1~HCR*A1*AUTH-LINE-7*~", 1)

	result := convert(t, raw)
	a := result.Authorisations[0]

	if a.Decision == Pending {
		t.Fatal("a decision carried at line level was reported as pending")
	}
	if a.Decision != Certified {
		t.Errorf("decision %q, want certified", a.Decision)
	}
	if a.Number != "AUTH-LINE-7" {
		t.Errorf("authorisation number %q, want the one from the line", a.Number)
	}

	// Derived rather than read, so it is reported as a judgement.
	if !containsAny(result.Notes, "carries no decision of its own") {
		t.Errorf("the derivation was not reported: %v", result.Notes)
	}
}

func TestAnEventWithAMixOfLineDecisionsTakesTheLeastFavourable(t *testing.T) {
	// An event whose lines are half certified and half denied is not an approval: something was refused, and a caller told "certified"
	// would bill for it. The worst reading is the one that cannot produce a claim for a service nobody authorised.
	raw := strings.Replace(response("A1", "AUTH-99120", ""), "HCR*A1*AUTH-99120*~", "", 1)
	raw = strings.Replace(raw,
		"SV1*HC:29881*450.00*UN*1~",
		"SV1*HC:29881*450.00*UN*1~HCR*A1*AUTH-OK*~HL*6*4*SS*0~SV1*HC:29882*300.00*UN*1~HCR*A3**0T~", 1)

	result := convert(t, raw)

	if got := result.Authorisations[0].Decision; got != Denied {
		t.Errorf("decision %q, want denied - one line was refused", got)
	}
}

func TestAnEventWhoseLinesSayNothingIsStillPending(t *testing.T) {
	// The other direction. A line with no HCR at all says nothing rather than saying pending, so an event with undecided lines must not
	// be reported as decided - and must not be reported as unknown either, which would read as a fault.
	raw := strings.Replace(response("A1", "AUTH-99120", ""), "HCR*A1*AUTH-99120*~", "", 1)

	result := convert(t, raw)

	if got := result.Authorisations[0].Decision; got != Pending {
		t.Errorf("decision %q, want pending for a review with no decision anywhere in it", got)
	}
}
