// Package pas278 maps between Da Vinci prior authorisation and the X12 278 transaction.
//
// # Why this exists
//
// CMS-0057-F requires payers to expose a FHIR prior authorisation API by 1 January 2027. It does not replace the X12 278, and it was
// never going to: a payer's utilisation management system decides authorisations and speaks 278. So a payer meeting the rule is
// translating FHIR to 278 and back, and a provider sending 278 today will be sending FHIR tomorrow to the same payers who still accept
// both.
//
// That translation is the thing worth owning. An engine that holds both models and cannot convert between them leaves the hardest part
// of the work - and the part where the mistakes are expensive - to whoever is integrating it.
//
// # The rules this package follows, stated rather than implied
//
// Written down for the same reason v2fhir writes its own down: mapping is where interoperability projects fail, and the failures are
// silent.
//
//   - **A decision is never upgraded.** Partial certification does not become approval, and a request the payer refused to consider does
//     not become a denial. Both of those collapses are available in the obvious mapping and both are wrong in the direction that costs
//     somebody weeks: an appeal filed against a decision nobody made, or six sessions booked against three approved.
//   - **Nothing is invented to fill a required field.** Where the target format demands something the source does not carry, the mapping
//     reports it as a note rather than supplying a plausible default. A fabricated authorisation number is worse than an absent one.
//   - **Every judgement is reported.** Notes come back with the result, so a person can read the handful that needed a decision rather
//     than the whole output.
//   - **Round trips are tested, not assumed.** A mapping that loses a field in one direction is found by converting back and comparing,
//     which is the only way to see a field nobody thought to assert on.
package pas278

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/x12"
)

// Decision is the outcome of a prior authorisation, with the states the 278 actually distinguishes.
//
// # Why this type exists rather than reusing the one in priorauth
//
// priorauth.PriorAuthResponse.Decision has three states: approved, denied, pended. The 278 has seven, and three of the differences
// matter to what somebody does next:
//
//   - Partial certification is approval of less than was asked for. Collapsing it into approved means the quantity reduction is
//     invisible, and it surfaces as a denied claim at the fourth session.
//   - A modified authorisation is approval of something else - a different level of care or setting. Also invisible when collapsed.
//   - A refusal to consider is not a denial. The answer is a corrected resubmission, not an appeal.
//
// So the bridge keeps the distinctions and the caller decides what to lose. Mapping into the narrower type is available through
// Narrow, which is explicit about what it discards.
type Decision string

// The decisions, matching the 278's own vocabulary.
const (
	Certified     Decision = "certified"
	PartiallyOK   Decision = "partially-certified"
	Modified      Decision = "modified"
	Denied        Decision = "denied"
	Pending       Decision = "pending"
	NotConsidered Decision = "not-considered"
	Unknown       Decision = "unknown"
)

// Authorisation is one prior authorisation decision, in a form both sides can express.
type Authorisation struct {
	// TraceNumber ties a decision to the request that produced it. Present in both formats, and the only reliable join between them.
	TraceNumber string

	// Decision is what the payer said.
	Decision Decision

	// Number is the authorisation identifier, which is what a claim needs.
	Number string

	// ReasonCode is the payer's code for why, as sent. Not translated to text: the code lists differ per payer and a wrong expansion
	// reads as authoritative.
	ReasonCode string

	// FollowUpAction is what the payer says to do about a request it would not consider. Empty otherwise.
	FollowUpAction string

	// Patient, Requester and Reviewer, as far as each format carries them.
	PatientName    string
	PatientID      string
	RequesterName  string
	RequesterID    string
	ReviewerName   string
	ReviewerID     string
	ServiceCode    string
	ServiceDate    string
	DiagnosisCodes []string
	Urgent         bool
}

// Result carries a conversion and the judgements it needed.
type Result struct {
	// Authorisations is one entry per event in the 278, or per item in the FHIR response.
	Authorisations []Authorisation

	// Notes records every place the mapping had to decide something, or could not. Read these: they are the difference between a
	// conversion that worked and one that appeared to.
	Notes []string
}

func (r *Result) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// FromReview converts a parsed X12 278 into authorisations.
//
// Works on requests as well as responses: a request has no decision, and the caller usually wants the patient, service and diagnosis
// out of it anyway - which is what a payer translating an incoming 278 into a FHIR Claim needs.
func FromReview(review *x12.ServiceReview) *Result {
	out := &Result{}

	if review == nil {
		out.note("there was no review to convert")

		return out
	}

	if len(review.Events) == 0 {
		// Reported rather than returned as an empty success. A 278 with no event loop is malformed in a way the envelope check does
		// not catch, and an empty result looks like a transaction that asked for nothing.
		out.note("the 278 carries no event loop, so there is nothing to authorise")
	}

	for i, e := range review.Events {
		a := Authorisation{
			TraceNumber:    e.TraceNumber,
			Decision:       decisionFor(e.Outcome),
			Number:         e.AuthorisationNumber,
			ReasonCode:     e.ReasonCode,
			PatientName:    joinName(review.Patient),
			PatientID:      review.Patient.IDCode,
			RequesterName:  review.Requester.Name,
			RequesterID:    review.Requester.IDCode,
			ReviewerName:   review.Reviewer.Name,
			ReviewerID:     review.Reviewer.IDCode,
			ServiceDate:    e.ServiceDate,
			DiagnosisCodes: e.Diagnoses,
			Urgent:         e.Urgent,
		}

		// A decision carried only at line level.
		//
		// Legitimate and not rare: a payer answering per service can leave the event loop without an HCR of its own. Reading only the
		// event level then reports every one of those as pending, which is a silent wrong answer in the worst direction - a practice
		// waits for a decision it has already been given, and an approval expires unused.
		//
		// Derived rather than assumed: the event takes the least favourable line decision, because an event is not approved if any
		// part of it was refused, and the authorisation number comes from whichever line has one. Both are judgements, so both are
		// reported.
		if e.Outcome == x12.OutcomeRequest && len(e.Lines) > 0 {
			derived, number := decisionFromLines(e.Lines)
			if derived != Unknown {
				a.Decision = derived
				if a.Number == "" {
					a.Number = number
				}
				out.note("event %d carries no decision of its own and its service lines do; the event is reported as %q, "+
					"the least favourable of them", i+1, derived)
			}
		}

		if len(e.Lines) > 0 {
			a.ServiceCode = e.Lines[0].ProcedureCode
			if len(e.Lines) > 1 {
				// Reported rather than silently dropped. A FHIR item carries one product or service, so several lines under one
				// event need several items - and a converter that used only the first would authorise one procedure out of three
				// with no indication that it had.
				out.note("event %d has %d service lines and only the first is carried on the authorisation; the rest are in the review",
					i+1, len(e.Lines))
			}
		}

		if e.NeedsResubmission() && len(e.Rejections) > 0 {
			a.FollowUpAction = e.Rejections[0].FollowUpAction
			// Said in words, because this is the case most likely to be mishandled downstream and the note is where somebody will
			// read it.
			out.note("event %d was not considered rather than denied: correct the request and send it again rather than appealing",
				i+1)
		}

		if a.Decision == Unknown {
			out.note("event %d carries action code %q, which this mapping does not recognise; it is reported as unknown rather than "+
				"assumed to be pending", i+1, e.ActionCode)
		}

		if (a.Decision == Certified || a.Decision == PartiallyOK || a.Decision == Modified) && a.Number == "" {
			// The failure this note exists to prevent: a certified service with no authorisation number produces a claim that is
			// denied for lacking one, and the denial reads as clinical.
			out.note("event %d is certified and carries no authorisation number, so a claim against it will be denied for "+
				"lacking one", i+1)
		}

		out.Authorisations = append(out.Authorisations, a)
	}

	return out
}

// ToClaimResponse renders authorisations as a Da Vinci PAS ClaimResponse.
//
// A map rather than a typed resource, matching priorauth.ToFHIR: the PAS profile constrains ClaimResponse in ways the general FHIR
// structs in this repository do not model, and a typed resource that silently dropped a profile-required extension would be worse than
// an explicit map somebody can read against the implementation guide.
func ToClaimResponse(result *Result) map[string]any {
	items := make([]any, 0, len(result.Authorisations))

	for i, a := range result.Authorisations {
		adjudication := []any{
			map[string]any{
				"category": map[string]any{
					"coding": []any{map[string]any{
						"system": "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes",
						"code":   "reviewAction",
					}},
				},
				"reason": map[string]any{
					"coding": []any{map[string]any{
						"system": "https://codesystem.x12.org/005010/306",
						// The X12 action code, carried as sent. PAS defines this exact mapping, so translating it into a FHIR
						// vocabulary of our own choosing would lose the only thing a payer's system can match on.
						"code": actionCodeFor(a.Decision),
					}},
				},
			},
		}

		if a.ReasonCode != "" {
			adjudication = append(adjudication, map[string]any{
				"category": map[string]any{
					"coding": []any{map[string]any{
						"system": "http://terminology.hl7.org/CodeSystem/adjudication",
						"code":   "denialreason",
					}},
				},
				"reason": map[string]any{
					"coding": []any{map[string]any{
						"system": "https://codesystem.x12.org/005010/1034",
						"code":   a.ReasonCode,
					}},
				},
			})
		}

		item := map[string]any{
			"itemSequence": i + 1,
			"adjudication": adjudication,
		}
		if a.Number != "" {
			item["extension"] = []any{map[string]any{
				"url":         "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-authorizationNumber",
				"valueString": a.Number,
			}}
		}

		items = append(items, item)
	}

	out := map[string]any{
		"resourceType": "ClaimResponse",
		"status":       "active",
		"type": map[string]any{
			"coding": []any{map[string]any{
				"system": "http://terminology.hl7.org/CodeSystem/claim-type",
				"code":   "professional",
			}},
		},
		"use":     "preauthorization",
		"outcome": fhirOutcomeFor(result),
		"item":    items,
	}

	if len(result.Notes) > 0 {
		// Carried into the resource rather than only returned to the caller. A payer's system reading this response is the audience
		// for "this was not considered rather than denied", and a note that only exists in a Go struct never reaches them.
		processNote := make([]any, 0, len(result.Notes))
		for i, n := range result.Notes {
			processNote = append(processNote, map[string]any{
				"number": i + 1,
				"type":   map[string]any{"coding": []any{map[string]any{"code": "print"}}},
				"text":   n,
			})
		}
		out["processNote"] = processNote
	}

	return out
}

// Narrow reduces a decision to the three states priorauth uses, saying what was lost.
//
// Explicit rather than implicit. The narrower vocabulary is what the existing FHIR client works in, and collapsing into it is sometimes
// the right thing - but it must be a choice somebody made, with the loss visible, rather than something that happens on the way through
// a converter.
func Narrow(d Decision) (decision string, lost string) {
	switch d {
	case Certified:
		return "approved", ""
	case PartiallyOK:
		return "approved", "the reduction in quantity: less was certified than was asked for"
	case Modified:
		return "approved", "the modification: the payer certified something other than what was requested"
	case Denied:
		return "denied", ""
	case NotConsidered:
		// Mapped to denied because there is nothing better in three states, and the loss is named loudly because the correct action
		// is the opposite of a denial's.
		return "denied", "that the request was never considered: the answer is a corrected resubmission, not an appeal"
	case Pending:
		return "pended", ""
	default:
		return "pended", fmt.Sprintf("an unrecognised decision %q, reported as pended because there is no state for unknown", d)
	}
}

// decisionFromLines derives an event decision from its service lines.
//
// The least favourable wins. An event whose lines are half certified and half denied is not an approval: something was refused, and a
// caller told "certified" would bill for it. Reporting the worst is the reading that cannot cause a claim to be submitted for a service
// nobody authorised.
func decisionFromLines(lines []x12.ReviewLine) (Decision, string) {
	// Ordered worst to best, which is the whole mechanism: the first one found wins.
	order := []Decision{NotConsidered, Unknown, Denied, Pending, Modified, PartiallyOK, Certified}
	rank := map[Decision]int{}
	for i, d := range order {
		rank[d] = i
	}

	worst := Decision("")
	number := ""

	for _, l := range lines {
		d := decisionFor(l.Outcome)
		if d == Pending && l.ActionCode == "" {
			// A line with no HCR at all says nothing, rather than saying pending. Counting it would make every event with one
			// undecided line look undecided.
			continue
		}
		if worst == "" || rank[d] < rank[worst] {
			worst = d
		}
		if number == "" {
			number = l.AuthorisationNumber
		}
	}

	if worst == "" {
		return Unknown, ""
	}

	return worst, number
}

func decisionFor(o x12.ReviewOutcome) Decision {
	switch o {
	case x12.OutcomeCertified:
		return Certified
	case x12.OutcomePartial:
		return PartiallyOK
	case x12.OutcomeModified:
		return Modified
	case x12.OutcomeDenied:
		return Denied
	case x12.OutcomePending:
		return Pending
	case x12.OutcomeNotConsidered:
		return NotConsidered
	case x12.OutcomeRequest:
		// A request has no decision. Reported as pending rather than unknown, because pending is what a request's state is once the
		// payer has it.
		return Pending
	default:
		return Unknown
	}
}

// actionCodeFor is the inverse, for rendering back into the X12 vocabulary PAS reuses.
func actionCodeFor(d Decision) string {
	switch d {
	case Certified:
		return "A1"
	case PartiallyOK:
		return "A2"
	case Denied:
		return "A3"
	case Pending:
		return "A4"
	case Modified:
		return "A6"
	case NotConsidered:
		// There is no action code for this, because in X12 it is not an action - it is the absence of one, carried in AAA. Rendering
		// it as A3 would assert a decision the payer did not make, so the field is left empty and the note carries the truth.
		return ""
	default:
		return ""
	}
}

// fhirOutcomeFor reports the ClaimResponse outcome for the set.
//
// FHIR offers queued, complete, error and partial. A mixed set is "partial", which is the one case an implementation is likely to get
// wrong by reporting the first item's state for all of them.
func fhirOutcomeFor(result *Result) string {
	if len(result.Authorisations) == 0 {
		return "error"
	}

	states := map[Decision]bool{}
	for _, a := range result.Authorisations {
		states[a.Decision] = true
	}

	switch {
	case len(states) > 1:
		return "partial"
	case states[Pending]:
		return "queued"
	case states[NotConsidered], states[Unknown]:
		return "error"
	default:
		return "complete"
	}
}

func joinName(p x12.Party) string {
	if p.FirstName == "" {
		return p.Name
	}

	return p.FirstName + " " + p.Name
}

// SummariseDecisions returns a stable, readable count of decisions, for a log line or an interface.
func SummariseDecisions(result *Result) string {
	counts := map[Decision]int{}
	for _, a := range result.Authorisations {
		counts[a.Decision]++
	}

	kinds := make([]string, 0, len(counts))
	for d := range counts {
		kinds = append(kinds, string(d))
	}
	// Sorted, because Go maps range randomly and this is user-visible - a standing invariant in this project, and one that gets
	// rediscovered every time somebody diffs two runs.
	sort.Strings(kinds)

	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[Decision(k)], k))
	}

	return strings.Join(parts, ", ")
}
