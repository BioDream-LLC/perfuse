package ncpdp

import (
	"strings"
	"testing"
)

func TestAnApprovalReportsWhatThePatientOwes(t *testing.T) {
	wire := header("B1") +
		"\x1e21\x1cANA\x1cF3AUTH12345" +
		"\x1e22\x1cD2RX1234" +
		"\x1e23\x1cF51200\x1cF93300"

	rs := mustResponses(t, wire)
	if len(rs) != 1 {
		t.Fatalf("got %d responses, want 1", len(rs))
	}
	r := rs[0]

	if !r.Status.Paid() {
		t.Errorf("status %q does not report as paid", r.Status)
	}
	if len(r.Rejects) != 0 {
		t.Errorf("an approval carries rejects: %v", r.Rejects)
	}
	if r.AuthorizationNumber != "AUTH12345" {
		t.Errorf("authorisation number = %q; a reversal has to quote it", r.AuthorizationNumber)
	}
	if r.PatientPayAmount != "1200" {
		t.Errorf("patient pay = %q, want 1200", r.PatientPayAmount)
	}
	if r.TotalAmountPaid != "3300" {
		t.Errorf("total paid = %q, want 3300", r.TotalAmountPaid)
	}
	if r.PrescriptionRefNumber != "RX1234" {
		t.Errorf("prescription number = %q, want RX1234", r.PrescriptionRefNumber)
	}
}

// Money stays as sent. Parsing a fixed-width amount into a float here would put rounding into figures
// that have to reconcile to the cent.
func TestAmountsAreNotTurnedIntoNumbers(t *testing.T) {
	r := mustResponses(t, header("B1")+"\x1e21\x1cANA"+"\x1e23\x1cF50000000000")[0]
	if r.PatientPayAmount != "0000000000" {
		t.Errorf("patient pay = %q; leading zeroes were removed, which means the value was parsed and reformatted",
			r.PatientPayAmount)
	}
}

// A capture is not an approval. Nothing has agreed to pay, and a pharmacy treating it as payment has
// dispensed against a claim that may still be rejected.
func TestACaptureDoesNotReportAsPaid(t *testing.T) {
	r := mustResponses(t, header("B1")+"\x1e21\x1cANC")[0]
	if r.Status != StatusCaptured {
		t.Fatalf("status = %q, want C", r.Status)
	}
	if r.Status.Paid() {
		t.Error("a captured claim reports as paid, so a pharmacy would dispense against nothing")
	}
	if r.Status.Rejected() {
		t.Error("a captured claim reports as rejected, which would have it resubmitted")
	}
	if !strings.Contains(r.Summary(), "later processing") {
		t.Errorf("the summary does not say a capture is not adjudicated yet: %q", r.Summary())
	}
}

// A duplicate is not a rejection to fix. The answer is to go and find the earlier claim, and repeatedly
// resubmitting is how a pharmacy comes to believe a claim failed when it was paid the first time.
func TestADuplicateSendsSomebodyToFindTheEarlierClaim(t *testing.T) {
	for _, code := range []string{"D", "Q", "S"} {
		r := mustResponses(t, header("B1")+"\x1e21\x1cAN"+code)[0]
		if !r.Status.Duplicate() {
			t.Errorf("status %q does not report as a duplicate", code)
		}
		if r.Status.Paid() {
			t.Errorf("status %q reports as paid", code)
		}
	}

	// And reject code 83 says the same thing, so it must send the reader the same way.
	r := LookupReject("83")
	if r.Action != ActionFindEarlierClaim {
		t.Errorf("reject 83 action = %q, want %q", r.Action, ActionFindEarlierClaim)
	}
}

// Every reject code says whose problem it is.
//
// The code alone does not, and that is the only thing the person at the counter needs. Refill Too Soon and
// Missing Cardholder ID are both rejections and they belong to different people.
func TestEachRejectCodeSaysWhoHasToAct(t *testing.T) {
	for _, tc := range []struct {
		code string
		want Action
	}{
		{"07", ActionFixAndResubmit},
		{"75", ActionPriorAuth},
		{"70", ActionNotCovered},
		{"79", ActionTooSoon},
		{"88", ActionClinicalReview},
		{"52", ActionCheckEnrolment},
		{"83", ActionFindEarlierClaim},
	} {
		got := LookupReject(tc.code)
		if !got.Known {
			t.Errorf("reject %s is not recognised", tc.code)
		}
		if got.Action != tc.want {
			t.Errorf("reject %s action = %q, want %q", tc.code, got.Action, tc.want)
		}
		if got.Text == "" {
			t.Errorf("reject %s has no description", tc.code)
		}
	}
}

// An unrecognised code must look unrecognised. Defaulting it to "fix and resubmit" sends staff hunting
// for a typo that is not there.
func TestAnUnrecognisedRejectCodeSaysSoRatherThanGuessing(t *testing.T) {
	got := LookupReject("ZZ")
	if got.Known {
		t.Error("an invented code reports as known")
	}
	if got.Action != ActionUnknown {
		t.Errorf("action = %q, want %q", got.Action, ActionUnknown)
	}
	// The text must still be specific enough to look up on a payer sheet.
	if !strings.Contains(got.Text, "ZZ") {
		t.Errorf("the description does not contain the code itself: %q", got.Text)
	}
}

// A rejection with no reason is one nobody can act on.
func TestARejectionWithNoCodeIsReported(t *testing.T) {
	_, err := readResponses(header("B1") + "\x1e21\x1cANR")
	if err == nil {
		t.Fatal("a rejection carrying no reject code was accepted")
	}
	if !strings.Contains(err.Error(), "act on") {
		t.Errorf("the error does not say the problem is that nothing can be done: %v", err)
	}
}

// A response with no status segment has not said what happened, and approved is the dangerous default.
func TestAResponseWithNoStatusSegmentIsRefused(t *testing.T) {
	_, err := readResponses(header("B1") + "\x1e22\x1cD2RX1")
	if err == nil {
		t.Fatal("a response with no status segment was accepted")
	}
}

// An undefined status is refused rather than read as a rejection. A status this server cannot read is not
// evidence of what the processor decided.
func TestAnUndefinedStatusIsRefusedRatherThanAssumedRejected(t *testing.T) {
	_, err := readResponses(header("B1") + "\x1e21\x1cANZ")
	if err == nil {
		t.Fatal("status Z was accepted")
	}
	if !strings.Contains(err.Error(), "not evidence") {
		t.Errorf("the error does not explain why guessing is wrong: %v", err)
	}
}

// Four claims get four answers, and they can differ. One verdict for the transmission would have a
// pharmacy resubmit three claims that were paid.
func TestEachClaimInATransmissionGetsItsOwnAnswer(t *testing.T) {
	wire := header("B1") +
		"\x1e21\x1cANA\x1cF3A1" + "\x1e22\x1cD2RX1" +
		"\x1d\x1e21\x1cANA\x1cF3A2" + "\x1e22\x1cD2RX2" +
		"\x1d\x1e21\x1cANR\x1cFB79" + "\x1e22\x1cD2RX3" +
		"\x1d\x1e21\x1cANA\x1cF3A4" + "\x1e22\x1cD2RX4"

	rs := mustResponses(t, wire)
	if len(rs) != 4 {
		t.Fatalf("got %d responses, want 4", len(rs))
	}

	paid := 0
	for _, r := range rs {
		if r.Status.Paid() {
			paid++
		}
	}
	if paid != 3 {
		t.Errorf("got %d paid, want 3", paid)
	}
	if !rs[2].Status.Rejected() {
		t.Error("the third claim should be rejected")
	}
	if len(rs[2].Rejects) != 1 || rs[2].Rejects[0].Action != ActionTooSoon {
		t.Errorf("the third claim's reject is %+v, want refill too soon", rs[2].Rejects)
	}
	// And the approved ones must keep their own authorisation numbers, or a reversal quotes the wrong one.
	if rs[0].AuthorizationNumber != "A1" || rs[3].AuthorizationNumber != "A4" {
		t.Errorf("authorisation numbers crossed over: %q and %q", rs[0].AuthorizationNumber, rs[3].AuthorizationNumber)
	}
}

func TestMultipleRejectCodesAreAllKept(t *testing.T) {
	r := mustResponses(t, header("B1")+"\x1e21\x1cANR\x1cFB07\x1cFB09\x1cFB25")[0]
	if len(r.Rejects) != 3 {
		t.Fatalf("got %d rejects, want 3; a processor sends up to five and dropping any hides work still to do", len(r.Rejects))
	}
	summary := r.Summary()
	for _, want := range []string{"cardholder", "date of birth", "prescriber"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary does not mention %q: %q", want, summary)
		}
	}
}

// A transmission the processor never accepted has adjudicated nothing, and that has to be distinguishable
// from a rejection of the claim.
func TestATransmissionLevelRefusalIsNotAClaimRejection(t *testing.T) {
	r := mustResponses(t, header("B1")+"\x1e21\x1cF1R\x1cANR\x1cFB01")[0]
	if r.TransmissionAccepted {
		t.Error("a transmission rejected at the header reports as accepted")
	}
	if !strings.Contains(r.Summary(), "nothing was adjudicated") {
		t.Errorf("the summary reads as a claim decision rather than a transmission refusal: %q", r.Summary())
	}
}

func TestFreeTextFromTheProcessorIsKept(t *testing.T) {
	r := mustResponses(t, header("B1")+"\x1e21\x1cANR\x1cFB76\x1cFQCALL PLAN 800 555 0100")[0]
	if len(r.Messages) == 0 {
		t.Fatal("processor free text was discarded, and it is often the only actionable thing in the response")
	}
	if !strings.Contains(r.Messages[0], "800 555 0100") {
		t.Errorf("message = %q", r.Messages[0])
	}
}

func readResponses(wire string) ([]Response, error) {
	m, err := Parse([]byte(wire))
	if err != nil {
		return nil, err
	}
	return ReadResponse(m)
}

func mustResponses(t *testing.T, wire string) []Response {
	t.Helper()
	rs, err := readResponses(wire)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	return rs
}
