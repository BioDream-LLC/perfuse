package ncpdp

import (
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────────────────────
// Responses
// ──────────────────────────────────────────────────────────────────────────────

// Response field identifiers, for the fields this package reads by name.
const (
	// FieldHeaderResponseStatus is 501-F1: whether the transmission itself was accepted.
	FieldHeaderResponseStatus = "F1"
	// FieldTransactionResponseStatus is 112-AN: what happened to the claim.
	FieldTransactionResponseStatus = "AN"
	// FieldRejectCode is 511-FB: why it was rejected. Repeats, up to five.
	FieldRejectCode = "FB"
	// FieldRejectFieldOccurrence is 546-4F: which occurrence of a repeating field was at fault.
	FieldRejectFieldOccurrence = "4F"
	// FieldAdditionalMessage is 526-FQ: free text from the processor. Repeats.
	FieldAdditionalMessage = "FQ"
	// FieldAuthorizationNumber is 503-F3, returned on an approval.
	FieldAuthorizationNumber = "F3"
	// FieldPatientPayAmount is 505-F5, in cents with no decimal point.
	FieldPatientPayAmount = "F5"
	// FieldTotalAmountPaid is 509-F9, in cents with no decimal point.
	FieldTotalAmountPaid = "F9"
	// FieldIngredientCostPaid is 506-F6.
	FieldIngredientCostPaid = "F6"
	// FieldDispensingFeePaid is 507-F7.
	FieldDispensingFeePaid = "F7"
	// FieldPrescriptionRefNumber is 402-D2.
	FieldPrescriptionRefNumber = "D2"
)

// Status is what a processor did with a claim. Field 112-AN.
type Status string

const (
	// StatusApproved means it will be paid.
	StatusApproved Status = "A"
	// StatusBenefit means a benefit was reported without payment.
	StatusBenefit Status = "B"
	// StatusCaptured means it was accepted for later processing, not adjudicated now.
	//
	// Not the same as approved, and the difference matters: nothing has agreed to pay yet. A pharmacy
	// treating a capture as an approval has dispensed against a claim that may still be rejected.
	StatusCaptured Status = "C"
	// StatusDuplicateOfPaid means this claim was already paid.
	StatusDuplicateOfPaid Status = "D"
	// StatusPriorAuthDeferred means a prior authorisation decision is pending.
	StatusPriorAuthDeferred Status = "F"
	// StatusPaid means paid.
	StatusPaid Status = "P"
	// StatusDuplicateOfCaptured means this claim was already captured.
	StatusDuplicateOfCaptured Status = "Q"
	// StatusRejected means it will not be paid, and the reject codes say why.
	StatusRejected Status = "R"
	// StatusDuplicateOfApproved means this claim was already approved.
	StatusDuplicateOfApproved Status = "S"
)

// Paid reports whether money will move.
//
// Captured is deliberately excluded. It means accepted for later processing, and treating it as paid is
// how a pharmacy dispenses against a claim nothing has yet agreed to fund.
func (s Status) Paid() bool {
	return s == StatusApproved || s == StatusPaid
}

// Rejected reports whether the claim will not be paid as submitted.
func (s Status) Rejected() bool { return s == StatusRejected }

// Duplicate reports whether the processor is saying it has seen this claim already.
//
// Worth separating from a rejection: the answer is not to fix and resubmit, it is to look for the
// earlier one. Resubmitting a duplicate repeatedly is how a pharmacy ends up believing a claim failed
// when it was paid the first time.
func (s Status) Duplicate() bool {
	return s == StatusDuplicateOfPaid || s == StatusDuplicateOfCaptured || s == StatusDuplicateOfApproved
}

// StatusName describes a status in words, or "" if it is not one this knows.
func StatusName(s Status) string {
	switch s {
	case StatusApproved:
		return "approved"
	case StatusBenefit:
		return "benefit reported"
	case StatusCaptured:
		return "captured for later processing"
	case StatusDuplicateOfPaid:
		return "duplicate of a paid claim"
	case StatusPriorAuthDeferred:
		return "prior authorisation deferred"
	case StatusPaid:
		return "paid"
	case StatusDuplicateOfCaptured:
		return "duplicate of a captured claim"
	case StatusRejected:
		return "rejected"
	case StatusDuplicateOfApproved:
		return "duplicate of an approved claim"
	default:
		return ""
	}
}

// Action is what somebody should do about a rejection.
//
// Grouped because the reject code alone does not say who has to act, and that is the only thing the
// pharmacy staff member reading it needs. Refill Too Soon and Missing Cardholder ID are both rejections
// and they belong to different people.
type Action string

const (
	// ActionFixAndResubmit means the claim has a data problem the sender can correct.
	ActionFixAndResubmit Action = "fix and resubmit"
	// ActionPriorAuth means the plan needs authorisation before it will pay.
	ActionPriorAuth Action = "obtain prior authorisation"
	// ActionNotCovered means the plan does not cover this, for this patient, now.
	ActionNotCovered Action = "not covered by this plan"
	// ActionCheckEnrolment means the patient or pharmacy is not recognised by the plan.
	ActionCheckEnrolment Action = "check enrolment or credentials"
	// ActionTooSoon means the patient still has supply on hand.
	ActionTooSoon Action = "too soon to refill"
	// ActionFindEarlierClaim means the processor has already seen this claim.
	ActionFindEarlierClaim Action = "find the earlier claim"
	// ActionClinicalReview means a drug utilisation conflict needs a pharmacist's judgement.
	ActionClinicalReview Action = "pharmacist review required"
	// ActionUnknown means this package does not recognise the code.
	//
	// Named rather than left blank, so an unrecognised code is visibly unrecognised. A default of
	// "fix and resubmit" would send staff hunting for a typo that is not there.
	ActionUnknown Action = "unknown reject code"
)

// rejectCode describes one reject code.
type rejectCode struct {
	text   string
	action Action
}

// rejectCodes covers the codes seen most often in practice.
//
// Not exhaustive, deliberately and visibly. NCPDP defines several hundred and processors add their own,
// so a lookup that invented a description for anything it did not know would be worse than one that
// says it does not know. An unrecognised code returns its own digits and ActionUnknown.
var rejectCodes = map[string]rejectCode{
	// Missing or invalid, which the standard writes as M/I. All correctable by the sender.
	"01": {"missing or invalid BIN number", ActionFixAndResubmit},
	"02": {"missing or invalid version number", ActionFixAndResubmit},
	"03": {"missing or invalid transaction code", ActionFixAndResubmit},
	"04": {"missing or invalid processor control number", ActionFixAndResubmit},
	"05": {"missing or invalid pharmacy number", ActionFixAndResubmit},
	"06": {"missing or invalid group number", ActionFixAndResubmit},
	"07": {"missing or invalid cardholder ID", ActionFixAndResubmit},
	"08": {"missing or invalid person code", ActionFixAndResubmit},
	"09": {"missing or invalid date of birth", ActionFixAndResubmit},
	"10": {"missing or invalid patient gender code", ActionFixAndResubmit},
	"11": {"missing or invalid patient relationship code", ActionFixAndResubmit},
	"19": {"missing or invalid days supply", ActionFixAndResubmit},
	"21": {"missing or invalid product/service ID", ActionFixAndResubmit},
	"25": {"missing or invalid prescriber ID", ActionFixAndResubmit},

	// Not matched: the value is well formed but the plan does not recognise it.
	"50": {"pharmacy number not matched", ActionCheckEnrolment},
	"51": {"group ID not matched", ActionCheckEnrolment},
	"52": {"cardholder ID not matched", ActionCheckEnrolment},
	"53": {"person code not matched", ActionCheckEnrolment},
	"54": {"product/service ID not matched", ActionFixAndResubmit},
	"55": {"product package size not matched", ActionFixAndResubmit},
	"56": {"prescriber ID not matched", ActionCheckEnrolment},

	"40": {"pharmacy not contracted with this plan", ActionCheckEnrolment},
	"41": {"submit bill to another processor", ActionCheckEnrolment},

	// Coverage decisions. Nothing the pharmacy can correct.
	"60": {"not covered for this patient's age", ActionNotCovered},
	"61": {"not covered for this patient's gender", ActionNotCovered},
	"65": {"patient is not covered", ActionCheckEnrolment},
	"66": {"patient age exceeds the plan maximum", ActionNotCovered},
	"69": {"filled after coverage terminated", ActionNotCovered},
	"70": {"product or service not covered", ActionNotCovered},
	"71": {"prescriber is not covered", ActionCheckEnrolment},
	"MR": {"product not on formulary", ActionNotCovered},

	"75": {"prior authorisation required", ActionPriorAuth},
	"76": {"plan limitations exceeded", ActionNotCovered},
	"77": {"discontinued product/service ID", ActionFixAndResubmit},
	"78": {"cost exceeds the plan maximum", ActionNotCovered},

	// The one that generates the most counter conversations.
	"79": {"refill too soon", ActionTooSoon},

	"80": {"drug and diagnosis do not match", ActionClinicalReview},
	"81": {"claim too old", ActionNotCovered},
	"82": {"claim is post-dated", ActionFixAndResubmit},
	"83": {"duplicate of a paid or captured claim", ActionFindEarlierClaim},
	"84": {"claim has not been paid or captured", ActionFindEarlierClaim},
	"85": {"claim not processed", ActionFixAndResubmit},
	"88": {"drug utilisation review conflict", ActionClinicalReview},
	"AA": {"patient spenddown not met", ActionNotCovered},
	"NN": {"rejected at the switch or intermediary", ActionFixAndResubmit},
}

// Reject is one reason a claim was rejected.
type Reject struct {
	// Code is the reject code as sent.
	Code string
	// Text describes it, or repeats the code when this package does not recognise it.
	Text string
	// Action says who has to do something about it.
	Action Action
	// Known reports whether the code was recognised.
	//
	// Present so an unrecognised code is visibly unrecognised rather than looking like a description that
	// happens to be terse. Several hundred codes exist and processors add their own.
	Known bool
}

// LookupReject describes a reject code.
func LookupReject(code string) Reject {
	c := strings.ToUpper(strings.TrimSpace(code))
	if r, ok := rejectCodes[c]; ok {
		return Reject{Code: c, Text: r.text, Action: r.action, Known: true}
	}
	return Reject{
		Code: c,
		// The code itself rather than an empty string, so a log line still says something specific and a
		// reader can look it up on the payer sheet.
		Text:   fmt.Sprintf("reject code %s, which this server does not have a description for", c),
		Action: ActionUnknown,
	}
}

// Response is what a processor said, read out of a parsed transmission.
type Response struct {
	// TransmissionAccepted is the header response status, 501-F1. False means the processor did not accept
	// the transmission at all, in which case nothing was adjudicated.
	TransmissionAccepted bool
	// Status is what happened to the claim.
	Status Status
	// Rejects are the reasons, in the order sent. Empty on an approval.
	Rejects []Reject
	// Messages are free text from the processor.
	Messages []string
	// AuthorizationNumber identifies an approval, and is what a reversal must quote.
	AuthorizationNumber string
	// PatientPayAmount is what the patient owes, in cents. Empty if not sent.
	//
	// Cents as a string rather than a parsed number, because the field is a fixed-width signed amount with
	// no decimal point and turning it into a float here would introduce rounding into money that has to
	// reconcile exactly.
	PatientPayAmount string
	// TotalAmountPaid is what the plan will pay, in cents.
	TotalAmountPaid string
	// PrescriptionRefNumber ties the response back to the claim.
	PrescriptionRefNumber string
}

// ReadResponse extracts the response from a parsed transmission.
//
// Returns one Response per transaction, because a transmission carrying four claims gets four answers and
// they can differ - three approved and one rejected is ordinary. Collapsing them into a single verdict
// would report the whole transmission as failed and have a pharmacy resubmit three claims that were paid.
func ReadResponse(m Message) ([]Response, error) {
	// The header response status lives in the response message or status segment depending on the
	// processor, so it is looked for in both rather than assumed.
	accepted := true
	if v, ok := findField(m, FieldHeaderResponseStatus); ok {
		accepted = strings.EqualFold(v, "A")
	}

	if len(m.Transactions) == 0 {
		return nil, fmt.Errorf("response transmission has no transactions")
	}

	out := make([]Response, 0, len(m.Transactions))
	for _, t := range m.Transactions {
		r := Response{TransmissionAccepted: accepted}

		st, ok := t.Segment(SegResponseStatus)
		if !ok {
			// Reported rather than defaulted. A response with no status segment has not said what happened,
			// and guessing approved would have a pharmacy dispense on nothing at all.
			return nil, fmt.Errorf(
				"a transaction in the response has no response status segment (%s), so it does not say whether the "+
					"claim was approved or rejected", SegResponseStatus)
		}

		if v, ok := st.Get(FieldTransactionResponseStatus); ok {
			r.Status = Status(strings.ToUpper(strings.TrimSpace(v)))
		}
		if StatusName(r.Status) == "" {
			return nil, fmt.Errorf(
				"response status %q is not one the standard defines; it is refused rather than treated as a rejection, "+
					"because a status this server cannot read is not evidence of what the processor decided",
				string(r.Status))
		}

		for _, c := range st.All(FieldRejectCode) {
			r.Rejects = append(r.Rejects, LookupReject(c))
		}
		r.Messages = append(r.Messages, st.All(FieldAdditionalMessage)...)
		r.AuthorizationNumber, _ = st.Get(FieldAuthorizationNumber)

		if msg, ok := t.Segment(SegResponseMessage); ok {
			r.Messages = append(r.Messages, msg.All(FieldAdditionalMessage)...)
		}
		if pr, ok := t.Segment(SegResponsePricing); ok {
			r.PatientPayAmount, _ = pr.Get(FieldPatientPayAmount)
			r.TotalAmountPaid, _ = pr.Get(FieldTotalAmountPaid)
		}
		if cl, ok := t.Segment(SegResponseClaim); ok {
			r.PrescriptionRefNumber, _ = cl.Get(FieldPrescriptionRefNumber)
		}

		// A rejection with no reason is a rejection nobody can act on. Reported, because the alternative is
		// a pharmacy staff member being told the claim failed and having nothing to work from.
		if r.Status.Rejected() && len(r.Rejects) == 0 {
			return nil, fmt.Errorf("the claim was rejected and no reject code was sent, so there is nothing to act on")
		}
		out = append(out, r)
	}
	return out, nil
}

// Summary describes a response in one line, for a log or a message list.
func (r Response) Summary() string {
	if !r.TransmissionAccepted {
		return "the processor did not accept the transmission, so nothing was adjudicated"
	}
	name := StatusName(r.Status)
	if len(r.Rejects) == 0 {
		return name
	}
	parts := make([]string, 0, len(r.Rejects))
	for _, rj := range r.Rejects {
		parts = append(parts, fmt.Sprintf("%s (%s)", rj.Text, rj.Action))
	}
	return name + ": " + strings.Join(parts, "; ")
}

// findField looks for a field in any segment of any transaction.
func findField(m Message, id string) (string, bool) {
	for _, t := range m.Transactions {
		for _, s := range t.Segments {
			if v, ok := s.Get(id); ok {
				return v, true
			}
		}
	}
	return "", false
}
