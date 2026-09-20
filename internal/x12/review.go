package x12

import (
	"fmt"
)

// The 278 health care services review: prior authorisation requests and the answers to them.
//
// # Why this is here when Perfuse already does prior authorisation
//
// The priorauth package implements the FHIR side, which is what CMS-0057-F requires payers to expose by 1 January 2027. This is the
// transaction real payers accept today, and the rule does not replace it. Da Vinci PAS is in practice a mapping onto this transaction:
// a payer receiving a FHIR request translates it to a 278 internally, because that is what their utilisation management system speaks.
//
// So an engine offering prior authorisation and reading only the FHIR half can talk to the systems that will exist and not to the ones
// that do. That is an odd place to stop.
//
// # The structure
//
// Hierarchical, like the 277, and for the same reason: the payer and the requesting practice are stated once rather than on every
// service.
//
//	ISA/GS/ST(278) — envelope
//	  BHT          — beginning of hierarchical transaction, whose BHT06 says request or response
//	  HL(20)       — the utilisation management organisation: who decides
//	    NM1
//	    HL(21)     — the requester: who is asking
//	      NM1
//	      HL(22)   — subscriber, and HL(23) a dependent when the patient is not the subscriber
//	        NM1
//	        DMG    — date of birth and sex, which payers match on
//	        HL(EV) — the event: one service being requested
//	          TRN  — trace number, how the answer is matched to the question
//	          UM   — what kind of review this is, and whether it is urgent
//	          DTP  — when the service is proposed or was provided
//	          HI   — diagnosis codes
//	          HCR  — the decision. Response only.
//	          HL(SS) — a service line under the event, with its own decision when they differ
//	SE/GE/IEA
//
// # The distinction this file exists to make
//
// A 278 response can say no in two entirely different ways, and confusing them wastes days.
//
// An **HCR** with action code A3 is a decision: the payer considered the request and will not certify the service. The next step is an
// appeal, or a different plan of care.
//
// An **AAA** is a refusal to consider it: the patient could not be found, the requester is not recognised, the plan does not cover this
// kind of review. Nothing was decided. The next step is to correct the request and send it again.
//
// Both arrive as "not approved" to anybody reading loosely, and the actions are opposite. A practice that appeals a rejected request is
// appealing something nobody ruled on, and a practice that resubmits a denial gets the same denial. So they are separate fields here,
// and Outcome names which happened.

// ReviewOutcome is what a 278 response amounts to.
type ReviewOutcome string

// The outcomes, kept as a small closed set so an interface can group and explain them.
const (
	// OutcomeCertified means the service is approved as asked.
	OutcomeCertified ReviewOutcome = "certified"

	// OutcomePartial means some of what was asked for is approved. The certified quantity is what matters and it is not what was
	// requested, which is why this is not folded into certified: a practice booking six sessions when three were approved discovers
	// it at the fourth claim.
	OutcomePartial ReviewOutcome = "partially-certified"

	// OutcomeModified means the payer approved something other than what was asked - a different level of care, a different setting.
	OutcomeModified ReviewOutcome = "modified"

	// OutcomeDenied means the payer considered it and will not certify. An appeal, not a resubmission.
	OutcomeDenied ReviewOutcome = "denied"

	// OutcomePending means no decision yet.
	OutcomePending ReviewOutcome = "pending"

	// OutcomeNotConsidered means the request was refused rather than decided - see the AAA discussion above. A resubmission, not an
	// appeal.
	OutcomeNotConsidered ReviewOutcome = "not-considered"

	// OutcomeRequest means this is a request, so there is no outcome to report.
	OutcomeRequest ReviewOutcome = "request"

	// OutcomeUnknown means a response carried an action code this package does not recognise.
	//
	// Reported rather than guessed at. An unrecognised code silently mapped to pending would be read as "wait", and waiting is the
	// wrong action for most of the codes it could actually be.
	OutcomeUnknown ReviewOutcome = "unknown"
)

// ServiceReview is one parsed 278 transaction set.
type ServiceReview struct {
	// ReferenceID is BHT03, the sender's identifier for the transaction.
	ReferenceID string

	// Date and Time are BHT04 and BHT05.
	Date string
	Time string

	// IsResponse reports whether this is an answer rather than a request, from BHT06.
	IsResponse bool

	// Reviewer is the organisation deciding, and Requester is who asked.
	Reviewer  Party
	Requester Party

	// Patient is the person the request is about, and PatientIsDependent reports whether they were named under a dependent loop
	// rather than as the subscriber. Payers match differently on the two, and a request that names a child as the subscriber is a
	// common and confusing rejection.
	Patient            Party
	PatientIsDependent bool

	// DateOfBirth and Sex come from DMG, which payers match on alongside the member number.
	DateOfBirth string
	Sex         string

	// Events is one entry per service being reviewed.
	Events []ReviewEvent
}

// ReviewEvent is one service under review, with its decision when there is one.
type ReviewEvent struct {
	// TraceNumber is TRN02: how a response is matched to the request that produced it.
	TraceNumber string

	// Category is UM01, what kind of review this is - health services, admission, specialty care.
	Category string

	// CertificationType is UM02: an initial request, a renewal, or a revision of one already decided.
	CertificationType string

	// ServiceType is UM03.
	ServiceType string

	// Urgent reports whether UM02 or the request category marks this as expedited. Kept as a field rather than left to a caller to
	// derive, because the answer decides whether a queue is worked today or this week.
	Urgent bool

	// ServiceDate is when the service is proposed, from DTP.
	ServiceDate string

	// Diagnoses are the codes from HI, in the order sent. The first is the principal one.
	Diagnoses []string

	// Outcome is what the response amounts to. OutcomeRequest on a request.
	Outcome ReviewOutcome

	// AuthorisationNumber is HCR02, the number the payer assigns to an approved review.
	//
	// The single field a practice needs from the whole transaction. Without it a certified service is not billable: the claim goes in
	// without an authorisation number and is denied for lack of one, which reads as a clinical denial to everybody downstream.
	AuthorisationNumber string

	// ActionCode is HCR01 as sent, kept alongside Outcome so an unrecognised code can be seen rather than only reported as unknown.
	ActionCode string

	// ReasonCode is HCR03, why the payer decided as it did.
	ReasonCode string

	// Rejections are AAA segments: reasons the request was not considered at all. Non-empty means nothing was decided.
	Rejections []ReviewRejection

	// Lines are service-level decisions, present when the payer answered per line.
	Lines []ReviewLine
}

// ReviewLine is one service line's decision.
type ReviewLine struct {
	// ProcedureCode is the composite from SV1, SV2 or SV3 depending on the kind of service.
	ProcedureCode string

	Outcome             ReviewOutcome
	ActionCode          string
	AuthorisationNumber string
	ReasonCode          string
}

// ReviewRejection is one reason a request was not considered.
type ReviewRejection struct {
	// Code is AAA03, the reject reason.
	Code string

	// FollowUpAction is AAA04, what the payer says to do about it - resubmit, do not resubmit, correct and resubmit. This is the
	// field that tells a practice whether the request is recoverable, and it is routinely ignored.
	FollowUpAction string
}

// Hierarchical level codes used by the 278.
const (
	levelReviewer  = "20"
	levelRequester = "21"
	levelEvent     = "EV"
	levelServices  = "SS"
)

// ParseServiceReview reads the first 278 transaction set in an interchange.
func (m *Message) ParseServiceReview() (*ServiceReview, error) {
	segs := m.transactionSetOfType("278")
	if segs == nil {
		return nil, fmt.Errorf("this interchange contains no 278 transaction set")
	}

	review := &ServiceReview{}

	var level string
	var current *ReviewEvent
	var currentLine *ReviewLine

	for _, s := range segs {
		switch s.ID {
		case "BHT":
			review.ReferenceID = s.Element(3).String()
			review.Date = s.Element(4).String()
			review.Time = s.Element(5).String()
			// BHT06 distinguishes the two directions. 11 is a response and 13 a request; anything else is treated as a request,
			// because a request is the safe reading - it has no decision to act on.
			review.IsResponse = s.Element(6).String() == "11"

		case "HL":
			level = s.Element(3).String()
			switch level {
			case levelReviewer:
				review.Reviewer, review.Requester, review.Patient = Party{}, Party{}, Party{}
			case levelRequester:
				review.Requester, review.Patient = Party{}, Party{}
			case levelSubscriber:
				review.Patient = Party{}
				review.PatientIsDependent = false
			case levelDependent:
				review.Patient = Party{}
				// Recorded rather than inferred from whether a name is present. A dependent loop with the subscriber's own details
				// repeated in it is common, and the distinction still matters to how the payer matches.
				review.PatientIsDependent = true
			case levelEvent:
				review.Events = append(review.Events, ReviewEvent{Outcome: OutcomeRequest})
				current = &review.Events[len(review.Events)-1]
				currentLine = nil
			case levelServices:
				if current != nil {
					current.Lines = append(current.Lines, ReviewLine{Outcome: OutcomeRequest})
					currentLine = &current.Lines[len(current.Lines)-1]
				}
			}

		case "NM1":
			party := Party{
				EntityType:  s.Element(2).String(),
				Name:        s.Element(3).String(),
				FirstName:   s.Element(4).String(),
				IDQualifier: s.Element(8).String(),
				IDCode:      s.Element(9).String(),
			}
			switch level {
			case levelReviewer:
				review.Reviewer = party
			case levelRequester:
				review.Requester = party
			case levelSubscriber, levelDependent:
				review.Patient = party
			}

		case "DMG":
			// DMG02 is the date and DMG03 the sex. DMG01 says what format the date is in, and is not reported: every guide in use
			// sends D8, and storing a format nobody varies invites a caller to branch on it.
			review.DateOfBirth = s.Element(2).String()
			review.Sex = s.Element(3).String()

		case "TRN":
			if current != nil {
				current.TraceNumber = s.Element(2).String()
			}

		case "UM":
			if current == nil {
				continue
			}
			current.Category = s.Element(1).String()
			current.CertificationType = s.Element(2).String()
			current.ServiceType = s.Element(3).String()
			current.Urgent = isUrgentReview(current.Category, s.Element(6).String())

		case "DTP":
			if current != nil && current.ServiceDate == "" {
				current.ServiceDate = s.Element(3).String()
			}

		case "HI":
			if current == nil {
				continue
			}
			// Every element of HI is a composite of qualifier and code, and there may be up to twelve. The code is the second
			// component; the qualifier says which code set, which is worth keeping out of the value so a caller comparing to an
			// ICD-10 code does not have to strip it.
			for i := 1; i <= s.ElementCount(); i++ {
				e := s.Element(i)
				if e.IsEmpty() {
					continue
				}
				if code := e.Component(2); code != "" {
					current.Diagnoses = append(current.Diagnoses, code)
				}
			}

		case "SV1", "SV2", "SV3":
			if currentLine != nil {
				currentLine.ProcedureCode = s.Element(1).String()
			}

		case "HCR":
			action := s.Element(1).String()
			outcome := outcomeForAction(action)
			switch {
			case currentLine != nil:
				currentLine.ActionCode = action
				currentLine.Outcome = outcome
				currentLine.AuthorisationNumber = s.Element(2).String()
				currentLine.ReasonCode = s.Element(3).String()
			case current != nil:
				current.ActionCode = action
				current.Outcome = outcome
				current.AuthorisationNumber = s.Element(2).String()
				current.ReasonCode = s.Element(3).String()
			}

		case "AAA":
			if current == nil {
				continue
			}
			current.Rejections = append(current.Rejections, ReviewRejection{
				Code:           s.Element(3).String(),
				FollowUpAction: s.Element(4).String(),
			})
			// A rejection overrides whatever the outcome was. Nothing was decided, and reporting this as pending would have a
			// practice waiting for an answer that is never coming.
			current.Outcome = OutcomeNotConsidered
		}
	}

	return review, nil
}

// IsServiceReview reports whether the interchange carries a 278.
func (m *Message) IsServiceReview() bool { return m.transactionSetOfType("278") != nil }

// outcomeForAction maps HCR01 to an outcome.
//
// The codes handled are the ones payers send. Anything else becomes OutcomeUnknown rather than being folded into pending, because
// pending reads as "wait" and waiting is the wrong action for most of what an unrecognised code could be.
func outcomeForAction(action string) ReviewOutcome {
	switch action {
	case "A1":
		return OutcomeCertified
	case "A2":
		return OutcomePartial
	case "A3":
		return OutcomeDenied
	case "A4":
		return OutcomePending
	case "A6":
		return OutcomeModified
	case "":
		// No HCR at all, which is what a request looks like.
		return OutcomeRequest
	default:
		return OutcomeUnknown
	}
}

// isUrgentReview reports whether a review is expedited.
//
// Two places say so and payers use both: the request category can name an expedited review, and UM06 carries a level-of-service code
// where 03 is urgent. Read from either, because a request marked urgent in the field this implementation ignored would sit in a routine
// queue - and the whole point of the field is that somebody is waiting on care.
func isUrgentReview(category, levelOfService string) bool {
	if levelOfService == "03" {
		return true
	}

	// AR is admission review, which is urgent by nature: the patient is at the door.
	return category == "AR"
}

// Approved reports whether the review certifies the service, in whole or in part.
//
// Partial counts as approved deliberately: something was certified and there is an authorisation number to bill against. A caller who
// needs to know the quantity was reduced should read Outcome, which is why both exist.
func (e ReviewEvent) Approved() bool {
	return e.Outcome == OutcomeCertified || e.Outcome == OutcomePartial || e.Outcome == OutcomeModified
}

// NeedsResubmission reports whether the request was refused rather than decided.
//
// The question that decides what a practice does next, and the one most easily got wrong: a refused request and a denial both look like
// "not approved", and the actions are opposite. Appealing something nobody ruled on achieves nothing, and resubmitting a denial gets
// the same denial.
func (e ReviewEvent) NeedsResubmission() bool { return len(e.Rejections) > 0 }

// Approved reports whether every event in the review was approved.
func (r *ServiceReview) Approved() bool {
	if len(r.Events) == 0 {
		return false
	}

	for _, e := range r.Events {
		if !e.Approved() {
			return false
		}
	}

	return true
}

// AuthorisationNumbers returns every authorisation number in the review, event and line level together.
//
// A convenience with a purpose: this is what gets copied onto a claim, and a caller hunting through two levels of structure to find them
// is a caller who will read one level and miss the other.
func (r *ServiceReview) AuthorisationNumbers() []string {
	var out []string

	seen := map[string]bool{}
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}

	for _, e := range r.Events {
		add(e.AuthorisationNumber)
		for _, l := range e.Lines {
			add(l.AuthorisationNumber)
		}
	}

	return out
}
