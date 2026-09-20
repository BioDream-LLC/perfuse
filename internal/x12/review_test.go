package x12

import (
	"strings"
	"testing"
)

// Tests for the 278 health care services review.
//
// The distinction between a denial and a refusal to consider is tested hardest, because it is the one that costs a practice days. Both
// arrive as "not approved" to anybody reading loosely, and the correct next actions are opposite: an appeal for one, a corrected
// resubmission for the other.

// um builds a UM segment from named positions.
//
// Written this way because counting asterisks by eye has now failed three times in this package: UM06 is the level-of-service code and
// a hand-written fixture put "03" in element 9, which read back as absent and looked like the parser ignoring the field. The code was
// right and the fixture was wrong, which is the more dangerous way round - it invites "fixing" working code.
func um(category, certification, serviceType, location, levelOfService string) string {
	e := make([]string, 7) // index 0 is the segment ID
	e[0] = "UM"
	e[1] = category
	e[2] = certification
	e[3] = serviceType
	e[4] = location
	e[6] = levelOfService

	return strings.Join(e, "*")
}

const reviewISA = "ISA*00*          *00*          *ZZ*PROVIDER       *ZZ*PAYER          " +
	"*260916*1400*^*00501*000000303*0*P*:~"

// reviewRequest is a provider asking for authorisation of an outpatient procedure.
var reviewRequest = reviewISA +
	"GS*HI*PROVIDER*PAYER*20260916*1400*303*X*005010X217~" +
	"ST*278*0001*005010X217~" +
	"BHT*0007*13*REQ-2026-0916-7*20260916*1400*13~" +
	"HL*1**20*1~" +
	"NM1*X3*2*ACME HEALTH PLAN*****PI*ACME01~" +
	"HL*2*1*21*1~" +
	"NM1*1P*2*RIVERSIDE ORTHOPAEDICS*****XX*1234567893~" +
	"HL*3*2*22*1~" +
	"NM1*IL*1*TURNER*ROSALIND****MI*MEM88771~" +
	"DMG*D8*19790214*F~" +
	"HL*4*3*EV*1~" +
	"TRN*1*AUTHREQ-4471~" +
	um("HS", "I", "4", "11:B", "") + "~" +
	"DTP*472*D8*20261001~" +
	"HI*BK:M1711*BF:M25561~" +
	"HL*5*4*SS*0~" +
	"SV1*HC:29881*450.00*UN*1~" +
	"SE*17*0001~GE*1*303~IEA*1*000000303~"

// reviewResponse is the payer certifying it, with an authorisation number.
var reviewResponse = reviewISA +
	"GS*HI*PAYER*PROVIDER*20260916*1500*304*X*005010X217~" +
	"ST*278*0001*005010X217~" +
	"BHT*0007*11*REQ-2026-0916-7*20260916*1500*11~" +
	"HL*1**20*1~" +
	"NM1*X3*2*ACME HEALTH PLAN*****PI*ACME01~" +
	"HL*2*1*21*1~" +
	"NM1*1P*2*RIVERSIDE ORTHOPAEDICS*****XX*1234567893~" +
	"HL*3*2*22*1~" +
	"NM1*IL*1*TURNER*ROSALIND****MI*MEM88771~" +
	"HL*4*3*EV*1~" +
	"TRN*2*AUTHREQ-4471~" +
	"UM*HS*I*4~" +
	"HCR*A1*AUTH-99120*~" +
	"SE*12*0001~GE*1*304~IEA*1*000000304~"

func TestARequestIsReadAsARequest(t *testing.T) {
	m, err := Parse([]byte(reviewRequest))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsServiceReview() {
		t.Fatal("a 278 interchange was not recognised as a service review")
	}

	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}

	if r.IsResponse {
		t.Error("a request was read as a response")
	}
	if r.Reviewer.Name != "ACME HEALTH PLAN" {
		t.Errorf("reviewer %q", r.Reviewer.Name)
	}
	if r.Requester.Name != "RIVERSIDE ORTHOPAEDICS" {
		t.Errorf("requester %q", r.Requester.Name)
	}
	if r.Patient.Name != "TURNER" || r.Patient.FirstName != "ROSALIND" {
		t.Errorf("patient %q %q", r.Patient.FirstName, r.Patient.Name)
	}
	if r.DateOfBirth != "19790214" || r.Sex != "F" {
		t.Errorf("date of birth %q sex %q, both of which payers match on", r.DateOfBirth, r.Sex)
	}

	if len(r.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(r.Events))
	}
	e := r.Events[0]

	if e.TraceNumber != "AUTHREQ-4471" {
		t.Errorf("trace number %q, which is how the answer gets matched to this", e.TraceNumber)
	}

	// A request has no decision, and must not be reported as pending: pending means the payer has it and has not ruled, which is a
	// different state a practice would wait on.
	if e.Outcome != OutcomeRequest {
		t.Errorf("outcome %q, want %q", e.Outcome, OutcomeRequest)
	}
	if e.Approved() {
		t.Error("a request was reported as approved")
	}

	// Diagnosis codes without their qualifiers, so a caller comparing to an ICD-10 code does not have to strip "BK:" first.
	if len(e.Diagnoses) != 2 || e.Diagnoses[0] != "M1711" || e.Diagnoses[1] != "M25561" {
		t.Errorf("diagnoses %v, want the codes without their qualifiers", e.Diagnoses)
	}

	if len(e.Lines) != 1 || e.Lines[0].ProcedureCode != "HC:29881" {
		t.Errorf("service lines %+v", e.Lines)
	}
}

func TestACertifiedResponseCarriesTheNumberAClaimNeeds(t *testing.T) {
	m, err := Parse([]byte(reviewResponse))
	if err != nil {
		t.Fatal(err)
	}

	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}

	if !r.IsResponse {
		t.Error("a response was read as a request")
	}
	if !r.Approved() {
		t.Error("a certified review was not reported as approved")
	}

	e := r.Events[0]
	if e.Outcome != OutcomeCertified {
		t.Errorf("outcome %q, want certified", e.Outcome)
	}

	// The single field the whole transaction exists to deliver. Without it a certified service is not billable: the claim goes in with
	// no authorisation number and is denied for lack of one, which reads downstream as a clinical denial.
	if e.AuthorisationNumber != "AUTH-99120" {
		t.Errorf("authorisation number %q, want AUTH-99120", e.AuthorisationNumber)
	}
	if got := r.AuthorisationNumbers(); len(got) != 1 || got[0] != "AUTH-99120" {
		t.Errorf("AuthorisationNumbers returned %v", got)
	}

	// And the trace number has to match the request, or a practice cannot tell which of forty outstanding requests this answers.
	if e.TraceNumber != "AUTHREQ-4471" {
		t.Errorf("trace number %q", e.TraceNumber)
	}
}

func TestEveryActionCodeIsMappedToSomethingActionable(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   ReviewOutcome
		// approved records whether there is something to bill against, which is a different question from whether the payer said yes
		// to exactly what was asked.
		approved bool
	}{
		{"A1", OutcomeCertified, true},
		{"A2", OutcomePartial, true},
		{"A3", OutcomeDenied, false},
		{"A4", OutcomePending, false},
		{"A6", OutcomeModified, true},
	} {
		t.Run(tc.action, func(t *testing.T) {
			raw := strings.Replace(reviewResponse, "HCR*A1*AUTH-99120*~", "HCR*"+tc.action+"*AUTH-99120*~", 1)

			m, err := Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			r, err := m.ParseServiceReview()
			if err != nil {
				t.Fatal(err)
			}

			if got := r.Events[0].Outcome; got != tc.want {
				t.Errorf("action %s became %q, want %q", tc.action, got, tc.want)
			}
			if got := r.Events[0].Approved(); got != tc.approved {
				t.Errorf("action %s reported approved=%v, want %v", tc.action, got, tc.approved)
			}
		})
	}
}

func TestAnUnrecognisedActionCodeIsReportedAsUnknownRatherThanPending(t *testing.T) {
	// Pending reads as "wait", and waiting is the wrong action for most of what an unrecognised code could be. So an unknown code says
	// so, and the code itself is kept so somebody can look it up rather than only being told it was not understood.
	raw := strings.Replace(reviewResponse, "HCR*A1*AUTH-99120*~", "HCR*ZZ*AUTH-99120*~", 1)

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}

	e := r.Events[0]
	if e.Outcome != OutcomeUnknown {
		t.Errorf("outcome %q, want unknown", e.Outcome)
	}
	if e.ActionCode != "ZZ" {
		t.Errorf("the action code was not kept: %q", e.ActionCode)
	}
	if e.Approved() {
		t.Error("an unrecognised action code was treated as approval")
	}
}

func TestADenialAndARefusalToConsiderAreDifferentThings(t *testing.T) {
	// The most expensive confusion available in this transaction, and the reason this file exists.
	//
	// A denial means the payer ruled and said no: the next step is an appeal. A refusal means nothing was ruled on - the patient could
	// not be found, the requester is not recognised - and the next step is to correct the request and send it again. Appealing
	// something nobody decided achieves nothing, and resubmitting a denial gets the same denial.
	denied := strings.Replace(reviewResponse, "HCR*A1*AUTH-99120*~", "HCR*A3**0T~", 1)
	refused := strings.Replace(reviewResponse, "HCR*A1*AUTH-99120*~", "AAA*N**15*C~", 1)

	t.Run("denied", func(t *testing.T) {
		m, err := Parse([]byte(denied))
		if err != nil {
			t.Fatal(err)
		}
		r, err := m.ParseServiceReview()
		if err != nil {
			t.Fatal(err)
		}

		e := r.Events[0]
		if e.Outcome != OutcomeDenied {
			t.Errorf("outcome %q, want denied", e.Outcome)
		}
		if e.NeedsResubmission() {
			t.Error("a denial was reported as needing resubmission, which would send a practice to redo a request the payer already ruled on")
		}
		if e.ReasonCode != "0T" {
			t.Errorf("reason code %q, which is the only thing saying why", e.ReasonCode)
		}
	})

	t.Run("not considered", func(t *testing.T) {
		m, err := Parse([]byte(refused))
		if err != nil {
			t.Fatal(err)
		}
		r, err := m.ParseServiceReview()
		if err != nil {
			t.Fatal(err)
		}

		e := r.Events[0]
		if e.Outcome != OutcomeNotConsidered {
			t.Errorf("outcome %q, want not-considered", e.Outcome)
		}
		if !e.NeedsResubmission() {
			t.Error("a refused request was not reported as needing resubmission, so it would be appealed instead of corrected")
		}
		if e.Outcome == OutcomeDenied {
			t.Error("a refusal to consider was reported as a denial")
		}

		if len(e.Rejections) != 1 {
			t.Fatalf("got %d rejections, want 1", len(e.Rejections))
		}
		// The follow-up action is what says whether the request is recoverable at all, and it is routinely ignored by
		// implementations that read only the reason code.
		if e.Rejections[0].Code != "15" || e.Rejections[0].FollowUpAction != "C" {
			t.Errorf("rejection %+v, want code 15 with follow-up action C", e.Rejections[0])
		}
	})
}

func TestAPartialCertificationIsNotReportedAsAPlainYes(t *testing.T) {
	// Partial is approved - there is a number to bill against - and it is not the same as certified, because the quantity approved is
	// not the quantity asked for. A practice booking six sessions against three approved finds out at the fourth claim.
	raw := strings.Replace(reviewResponse, "HCR*A1*AUTH-99120*~", "HCR*A2*AUTH-99121*~", 1)

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}

	e := r.Events[0]
	if e.Outcome == OutcomeCertified {
		t.Error("a partial certification was reported as fully certified")
	}
	if e.Outcome != OutcomePartial {
		t.Errorf("outcome %q, want partially-certified", e.Outcome)
	}
	if !e.Approved() {
		t.Error("a partial certification was reported as not approved, though there is an authorisation number to bill against")
	}
}

func TestALineLevelDecisionDoesNotOverwriteTheEventDecision(t *testing.T) {
	// A payer can certify the event and deny one line within it, and both have to survive. Collapsing them loses the denial, which is
	// the half a practice needs to act on.
	raw := strings.Replace(reviewResponse,
		"HCR*A1*AUTH-99120*~",
		"HCR*A1*AUTH-99120*~HL*5*4*SS*0~SV1*HC:29881*450.00*UN*1~HCR*A3**0T~", 1)

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}

	e := r.Events[0]
	if e.Outcome != OutcomeCertified {
		t.Errorf("the event outcome became %q after a line was denied", e.Outcome)
	}
	if e.AuthorisationNumber != "AUTH-99120" {
		t.Errorf("the event authorisation number became %q", e.AuthorisationNumber)
	}
	if len(e.Lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(e.Lines))
	}
	if e.Lines[0].Outcome != OutcomeDenied {
		t.Errorf("the line outcome is %q, want denied", e.Lines[0].Outcome)
	}
}

func TestADependentIsRecordedAsOne(t *testing.T) {
	// A request naming a child under the subscriber loop rather than the dependent loop is a common rejection, and one that reads as
	// "patient not found". Recording which loop the patient came from is how a channel can tell.
	raw := strings.Replace(reviewRequest, "HL*3*2*22*1~", "HL*3*2*23*1~", 1)

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}

	if !r.PatientIsDependent {
		t.Error("a patient named in the dependent loop was not recorded as a dependent")
	}
	if r.Patient.Name != "TURNER" {
		t.Errorf("the dependent's name was lost: %q", r.Patient.Name)
	}
}

func TestAnUrgentReviewIsMarkedUrgentFromEitherPlaceItIsSaid(t *testing.T) {
	// Two places carry this and payers use both. A request marked urgent in the field an implementation ignored sits in a routine
	// queue, and the entire purpose of the field is that somebody is waiting on care.
	for _, tc := range []struct {
		name string
		um   string
		want bool
	}{
		{"level of service says urgent", um("HS", "I", "4", "11:B", "03") + "~", true},
		{"admission review is urgent by nature", um("AR", "I", "4", "", "") + "~", true},
		{"an ordinary outpatient review is not", um("HS", "I", "4", "", "") + "~", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(reviewRequest, um("HS", "I", "4", "11:B", "")+"~", tc.um, 1)

			m, err := Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			r, err := m.ParseServiceReview()
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Events[0].Urgent; got != tc.want {
				t.Errorf("urgent=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestTheThreeTransactionsAreRecognisedSeparately(t *testing.T) {
	// A 275, a 277 and a 278 are all hierarchical interchanges read from the same shape. A dispatcher that answered yes to two of them
	// would hand a claims office the wrong parser with no error anywhere.
	for _, tc := range []struct {
		name       string
		raw        string
		attachment bool
		status     bool
		review     bool
	}{
		{"275", attachment275("content"), true, false, false},
		{"277", status277, false, true, false},
		{"278 request", reviewRequest, false, false, true},
		{"278 response", reviewResponse, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got := m.IsAttachment(); got != tc.attachment {
				t.Errorf("IsAttachment=%v, want %v", got, tc.attachment)
			}
			if got := m.IsStatusReport(); got != tc.status {
				t.Errorf("IsStatusReport=%v, want %v", got, tc.status)
			}
			if got := m.IsServiceReview(); got != tc.review {
				t.Errorf("IsServiceReview=%v, want %v", got, tc.review)
			}
		})
	}
}

func TestAskingForAReviewInAnInterchangeWithoutOneIsAnError(t *testing.T) {
	m, err := Parse([]byte(status277))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ParseServiceReview(); err == nil {
		t.Error("parsing a service review out of a 277 succeeded")
	}
}

func TestTheDocumentedAuthorisationFilterCompilesAndMatches(t *testing.T) {
	// The expression printed in the manual. A documented filter that does not compile will be copied into a channel, refused at load,
	// and teach somebody the feature is broken rather than that the example was.
	const documented = `HCR-1 == "A3"`

	f, err := ParseFilter(documented)
	if err != nil {
		t.Fatalf("the filter printed in the manual does not compile: %v", err)
	}

	denied, err := Parse([]byte(strings.Replace(reviewResponse, "HCR*A1*AUTH-99120*~", "HCR*A3**0T~", 1)))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := f.Eval(denied)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("the documented filter does not match a denied review")
	}

	certified, err := Parse([]byte(reviewResponse))
	if err != nil {
		t.Fatal(err)
	}
	ok, err = f.Eval(certified)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("the documented filter matches a certified review")
	}
}
