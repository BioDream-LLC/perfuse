package fhirserver

// Forwarding PAS requests to the payer's utilization management system as X12 278.
//
// Most payers decide prior authorizations in a UM system that speaks X12 278, not FHIR. PAS was designed for that: the FHIR
// Claim converts to a 278 request, the UM system answers in 278, and the answer converts back to the PAS ClaimResponse. With
// PAS.UM set, this server is that intermediary: every $submit is sent to the UM system as one 278 request, each item a patient
// event, and the UM system's per-event answer becomes the item's review action, with the UM system's authorization number.
//
// A UM system often pends first and decides later. Its later 278 response is posted to Claim/$decide-278: each event is matched
// to its request by the trace number sent, and decided exactly as a reviewer's $decide is, so the provider's PAS subscription
// hears about it.
//
// When the UM system cannot be reached, or answers with something that is not a 278 response, every item is pended for a
// reviewer, and the provider is told only that: the reason goes to the log, not to the provider.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/x12"
)

// PASUM is the payer's utilization management system, reached over X12 278.
type PASUM struct {
	// Send delivers a 278 request and returns the UM system's 278 response.
	Send func(ctx context.Context, request []byte) ([]byte, error)
	// SenderID and ReceiverID go in the ISA and GS envelope; Production marks it P rather than T.
	SenderID, ReceiverID string
	Production           bool
	// PayerID is the UMO's payer ID when the request's insurer Organization carries no identifier.
	PayerID string
	// Log records why a forward failed. Nil discards it.
	Log *slog.Logger
}

const (
	cptSystem   = "http://www.ama-assn.org/go/cpt"
	icd10System = "http://hl7.org/fhir/sid/icd-10-cm"
	x12Service  = "https://codesystem.x12.org/005010/1365"
)

func isHCPCS(system string) bool {
	s := strings.TrimPrefix(strings.TrimPrefix(system, "https://"), "http://")
	return s == "www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets" || s == "terminology.hl7.org/CodeSystem/HCPCS"
}

// toServiceReview converts a PAS request to a 278 request. traces maps each 278 trace number to its item sequence.
func (um *PASUM) toServiceReview(req *pasRequest, id string, now time.Time) (x12.ServiceReviewRequest, map[string]string) {
	resource := func(field string) map[string]any {
		return asMapAny(req.Resolve(str(asMapAny(req.Claim[field])["reference"]))["resource"])
	}
	payer, provider, patient := resource("insurer"), resource("provider"), resource("patient")
	out := x12.ServiceReviewRequest{
		Envelope:  x12.Envelope{SenderID: um.SenderID, ReceiverID: um.ReceiverID, Production: um.Production, Now: now},
		Reference: strings.ToUpper(id[:8]),
	}
	out.Payer = x12.Person{LastName: strings.ToUpper(str(payer["name"])), ID: firstIdentifier(payer, "")}
	if out.Payer.ID == "" {
		out.Payer.ID = um.PayerID
	}
	out.Provider = personFrom(provider)
	out.Provider.ID = firstIdentifier(provider, "http://hl7.org/fhir/sid/us-npi")
	out.Subscriber = personFrom(patient)
	out.Subscriber.DOB = strings.ReplaceAll(str(patient["birthDate"]), "-", "")
	out.Subscriber.Gender = map[string]string{"male": "M", "female": "F"}[str(patient["gender"])]
	if out.Subscriber.Gender == "" && out.Subscriber.DOB != "" {
		out.Subscriber.Gender = "U"
	}
	// The member ID: the Coverage's subscriber ID is the number on the card, which is what a UM system matches on.
	for _, ins := range asSliceAny(req.Claim["insurance"]) {
		cov := asMapAny(req.Resolve(str(asMapAny(asMapAny(ins)["coverage"])["reference"]))["resource"])
		if v := str(cov["subscriberId"]); v != "" && out.Subscriber.ID == "" {
			out.Subscriber.ID = v
		}
		if out.Subscriber.ID == "" {
			out.Subscriber.ID = firstIdentifier(cov, "")
		}
	}
	if out.Subscriber.ID == "" {
		out.Subscriber.ID = firstIdentifier(patient, "")
	}

	diagnoses := map[string]string{}
	var order []string
	for _, d := range asSliceAny(req.Claim["diagnosis"]) {
		dm := asMapAny(d)
		for _, c := range asSliceAny(asMapAny(dm["diagnosisCodeableConcept"])["coding"]) {
			if cm := asMapAny(c); str(cm["system"]) == icd10System && str(cm["code"]) != "" {
				seq := fmt.Sprint(dm["sequence"])
				diagnoses[seq] = str(cm["code"])
				order = append(order, seq)
				break
			}
		}
	}
	urgent := false
	for _, c := range asSliceAny(asMapAny(req.Claim["priority"])["coding"]) {
		urgent = urgent || str(asMapAny(c)["code"]) == "stat"
	}
	// UM06 as PAS names it (extension-levelOfServiceCode, X12 1338); Claim.priority stat stands in only when it is absent.
	level := pasExtCode(req.Claim, "extension-levelOfServiceCode", "E", "U", "03")
	// An admission review's dates, from the PAS supportingInfo categories for them.
	var admitted, discharged string
	for _, si := range asSliceAny(req.Claim["supportingInfo"]) {
		sm := asMapAny(si)
		for _, c := range asSliceAny(asMapAny(sm["category"])["coding"]) {
			start, end := str(asMapAny(sm["timingPeriod"])["start"]), str(asMapAny(sm["timingPeriod"])["end"])
			if start == "" {
				start = str(sm["timingDate"])
			}
			switch str(asMapAny(c)["code"]) {
			case "admissionDates":
				admitted = ccyymmdd(start)
				if discharged == "" {
					discharged = ccyymmdd(end)
				}
			case "dischargeDates":
				discharged = ccyymmdd(start)
			}
		}
	}
	claimProvider := req.careTeamProvider(req.Claim, nil)

	traces := map[string]string{}
	for _, it := range asSliceAny(req.Claim["item"]) {
		item := asMapAny(it)
		seq := fmt.Sprint(item["sequence"])
		e := x12.ReviewRequestEvent{Trace: strings.ToUpper(id[:8]) + "-" + seq, Urgent: urgent, LevelOfService: level}
		// UM01 and UM02 as PAS names them (extension-serviceItemRequestType, extension-certificationType). A code the 278
		// request cannot carry (IN is a notification's) leaves the defaults, HS and I.
		e.Category = pasExtCode(item, "extension-serviceItemRequestType", "AR", "HS", "SC")
		e.CertificationType = pasExtCode(item, "extension-certificationType", "1", "2", "3", "4", "I", "R", "S")
		// UM04: the place of service.
		for _, c := range asSliceAny(asMapAny(item["locationCodeableConcept"])["coding"]) {
			if cm := asMapAny(c); strings.Contains(str(cm["system"]), "place-of-service") && e.Location == "" {
				e.Location = str(cm["code"])
			}
		}
		for _, c := range asSliceAny(asMapAny(req.Claim["type"])["coding"]) {
			e.Institutional = e.Institutional || str(asMapAny(c)["code"]) == "institutional"
		}
		if e.Institutional {
			for _, ext := range asSliceAny(item["extension"]) {
				em := asMapAny(ext)
				if str(em["url"]) == pasBase+"extension-revenueCode" {
					for _, c := range asSliceAny(asMapAny(em["valueCodeableConcept"])["coding"]) {
						e.RevenueCode = str(asMapAny(c)["code"])
					}
				}
			}
		}
		if e.Category == "AR" {
			e.AdmissionDate, e.DischargeDate = admitted, discharged
		}
		e.Provider = req.careTeamProvider(req.Claim, asSliceAny(item["careTeamSequence"]))
		if e.Provider.ID == "" {
			e.Provider = claimProvider
		}
		for _, cc := range []map[string]any{asMapAny(item["category"]), asMapAny(item["productOrService"])} {
			for _, c := range asSliceAny(cc["coding"]) {
				cm := asMapAny(c)
				switch system := str(cm["system"]); {
				case system == x12Service && e.ServiceType == "":
					e.ServiceType = str(cm["code"])
				case (system == cptSystem || isHCPCS(system)) && e.Procedure == "":
					e.Procedure = str(cm["code"])
				}
			}
		}
		if e.ServiceType == "" && e.Procedure == "" {
			// Nothing a 278 can carry; the item is answered as an error or pended, never invented.
			continue
		}
		if e.Procedure != "" && e.ServiceType == "" {
			// UM03 is required with UM01 HS; 1 is medical care, the general code, when only a procedure was named.
			e.ServiceType = "1"
		}
		if cancels(req.Claim) || cancels(item) {
			e.CertificationType = "3"
		}
		if e.Provider.ID == out.Provider.ID {
			// The requester doing the service themselves is said once, in 2010B.
			e.Provider = x12.Person{}
		}
		if q, ok := asMapAny(item["quantity"])["value"].(float64); ok {
			e.Quantity = q
		}
		if d := str(item["servicedDate"]); d != "" {
			e.ServiceDate = strings.ReplaceAll(d[:min(10, len(d))], "-", "")
		} else if d := str(asMapAny(item["servicedPeriod"])["start"]); d != "" {
			e.ServiceDate = strings.ReplaceAll(d[:min(10, len(d))], "-", "")
		}
		pointers := asSliceAny(item["diagnosisSequence"])
		if len(pointers) == 0 {
			for _, seq := range order {
				e.Diagnoses = append(e.Diagnoses, diagnoses[seq])
			}
		}
		for _, p := range pointers {
			if code := diagnoses[fmt.Sprint(p)]; code != "" {
				e.Diagnoses = append(e.Diagnoses, code)
			}
		}
		out.Events = append(out.Events, e)
		traces[e.Trace] = seq
	}
	return out, traces
}

// pasExtCode is the code of a PAS extension (by the last part of its URL) on res, when it is one of allowed.
func pasExtCode(res map[string]any, name string, allowed ...string) string {
	for _, e := range asSliceAny(res["extension"]) {
		em := asMapAny(e)
		if str(em["url"]) != pasBase+name {
			continue
		}
		for _, c := range asSliceAny(asMapAny(em["valueCodeableConcept"])["coding"]) {
			code := str(asMapAny(c)["code"])
			for _, a := range allowed {
				if code == a {
					return code
				}
			}
		}
	}
	return ""
}

// ccyymmdd is a FHIR date or dateTime as X12 writes a date; empty when it has no full date.
func ccyymmdd(d string) string {
	if len(d) < 10 {
		return ""
	}
	return strings.ReplaceAll(d[:10], "-", "")
}

// careTeamProvider is who will do the service, from the Claim's care team: the members an item names by careTeamSequence, or,
// with none named, a member PAS scopes to the whole claim (extension-careTeamClaimScope). Only one with an NPI is returned: a
// 278 names a provider by NPI, and a name alone would be matched to nobody.
func (req *pasRequest) careTeamProvider(claim map[string]any, sequences []any) x12.Person {
	for _, ct := range asSliceAny(claim["careTeam"]) {
		cm := asMapAny(ct)
		if len(sequences) > 0 {
			named := false
			for _, s := range sequences {
				named = named || fmt.Sprint(s) == fmt.Sprint(cm["sequence"])
			}
			if !named {
				continue
			}
		} else {
			scoped := false
			for _, e := range asSliceAny(cm["extension"]) {
				em := asMapAny(e)
				scoped = scoped || (str(em["url"]) == pasBase+"extension-careTeamClaimScope" && em["valueBoolean"] == true)
			}
			if !scoped {
				continue
			}
		}
		res := asMapAny(req.Resolve(str(asMapAny(cm["provider"])["reference"]))["resource"])
		if str(res["resourceType"]) == "PractitionerRole" {
			if org := asMapAny(req.Resolve(str(asMapAny(res["organization"])["reference"]))["resource"]); len(org) > 0 &&
				asMapAny(res["practitioner"])["reference"] == nil {
				res = org
			} else {
				res = asMapAny(req.Resolve(str(asMapAny(res["practitioner"])["reference"]))["resource"])
			}
		}
		p := personFrom(res)
		p.ID = firstIdentifier(res, "http://hl7.org/fhir/sid/us-npi")
		if p.ID != "" && p.LastName != "" {
			return p
		}
	}
	return x12.Person{}
}

func firstIdentifier(res map[string]any, system string) string {
	for _, id := range asSliceAny(res["identifier"]) {
		im := asMapAny(id)
		if (system == "" || str(im["system"]) == system) && str(im["value"]) != "" {
			return str(im["value"])
		}
	}
	return ""
}

func personFrom(res map[string]any) x12.Person {
	if n := str(res["name"]); n != "" {
		return x12.Person{LastName: strings.ToUpper(n)}
	}
	for _, n := range asSliceAny(res["name"]) {
		nm := asMapAny(n)
		p := x12.Person{LastName: strings.ToUpper(str(nm["family"]))}
		if given := asSliceAny(nm["given"]); len(given) > 0 {
			p.FirstName = strings.ToUpper(str(given[0]))
		}
		if p.LastName != "" {
			return p
		}
	}
	return x12.Person{}
}

// askUM sends the request to the UM system and records its answers on req, for decideItem. Every outcome is an answer: when
// the UM system is unreachable each item is pended, with a note that says so and no more.
func (s *Server) askUM(ctx context.Context, req *pasRequest, id string, now time.Time) {
	um := s.PAS.UM
	review, traces := um.toServiceReview(req, id, now)
	req.um, req.umTraces = map[string]itemDecision{}, map[string]string{}
	for trace, seq := range traces {
		req.umTraces[seq] = trace
	}
	unreachable := func(why string, err error) {
		if um.Log != nil {
			um.Log.Warn("PAS: the utilization management system did not answer; the request is pended", "claimResponse", id,
				"why", why, "error", err)
		}
		for _, seq := range traces {
			req.um[seq] = itemDecision{code: pasPended.code, display: pasPended.display,
				why: "Pended for a reviewer: the utilization management system did not answer."}
		}
	}
	if len(review.Events) == 0 {
		return
	}
	request, err := x12.BuildServiceReviewRequest(review)
	if err != nil {
		// What is missing from the request (an NPI, a member ID) is the provider's to fix, so this one is said.
		for _, seq := range traces {
			req.um[seq] = itemDecision{code: pasPended.code, display: pasPended.display,
				why: "Pended for a reviewer: the request could not be sent to utilization management (" +
					strings.TrimPrefix(err.Error(), "a 278 request needs ") + " missing)."}
		}
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := um.Send(cctx, request)
	if err != nil {
		unreachable("send", err)
		return
	}
	msg, err := x12.Parse(raw)
	if err != nil {
		unreachable("not X12", err)
		return
	}
	answer, err := msg.ParseServiceReview()
	if err != nil || !answer.IsResponse {
		unreachable("not a 278 response", err)
		return
	}
	for _, ev := range answer.Events {
		seq, ok := traces[ev.TraceNumber]
		if !ok {
			continue
		}
		d := umDecision(ev)
		req.um[seq] = d
		if req.umAuth == "" && certifies(d) && d.number != "" {
			req.umAuth = d.number
		}
	}
}

// umDecision is a 278 event's answer as a PAS review action. A line-level answer, when there is one, is the more specific.
func umDecision(ev x12.ReviewEvent) itemDecision {
	outcome, number, reason := ev.Outcome, ev.AuthorisationNumber, ev.ReasonCode
	for _, l := range ev.Lines {
		if l.Outcome != x12.OutcomeRequest && l.Outcome != "" {
			outcome, reason = l.Outcome, l.ReasonCode
			if l.AuthorisationNumber != "" {
				number = l.AuthorisationNumber
			}
			break
		}
	}
	if len(ev.Rejections) > 0 {
		outcome = x12.OutcomeNotConsidered
	}
	because := ""
	if reason != "" {
		because = " (X12 reason " + reason + ")"
	}
	var d itemDecision
	switch outcome {
	case x12.OutcomeCertified:
		d, d.why = pasApproved, "Certified by utilization management."
	case x12.OutcomePartial:
		d, d.why = pasPartial, "Certified in part by utilization management"+because+"."
	case x12.OutcomeModified:
		d, d.why = pasModified, "Certified as modified by utilization management"+because+"."
	case x12.OutcomeDenied:
		d, d.why = pasDenied, "Not certified by utilization management"+because+"."
	case x12.OutcomePending:
		d, d.why = pasPended, "Pended by utilization management"+because+"."
	case x12.OutcomeCancelled:
		d, d.why = pasCancelled, "Cancelled by utilization management"+because+"."
	case x12.OutcomeNotConsidered:
		var codes []string
		for _, r := range ev.Rejections {
			codes = append(codes, r.Code+" (follow-up "+r.FollowUpAction+")")
		}
		d, d.why = pasPended, "Utilization management did not consider this request: reject reason "+strings.Join(codes, ", ")+
			". It is pended for a reviewer."
	default:
		d, d.why = pasPended, "Utilization management answered with action code "+ev.ActionCode+
			", which is not one this server knows; it is pended for a reviewer."
	}
	if certifies(d) {
		d.number = number
	}
	return d
}

// handlePASDecide278 applies a UM system's later 278 response to the requests its events were sent for.
func (s *Server) handlePASDecide278(w http.ResponseWriter, r *http.Request) {
	if s.pasOff(w, r) {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body could not be read")
		return
	}
	msg, err := x12.Parse(raw)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body is not X12: "+err.Error())
		return
	}
	answer, err := msg.ParseServiceReview()
	if err != nil || !answer.IsResponse {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body is not a 278 response (BHT06 11)")
		return
	}
	var results []any
	result := func(trace, outcome, detail string) {
		parts := []any{map[string]any{"name": "trace", "valueString": trace}, map[string]any{"name": "result", "valueCode": outcome}}
		if detail != "" {
			parts = append(parts, map[string]any{"name": "detail", "valueString": detail})
		}
		results = append(results, map[string]any{"name": "event", "part": parts})
	}
	for _, ev := range answer.Events {
		var id string
		var seq int
		err := s.Store.db.QueryRowContext(r.Context(), `SELECT id, seq FROM pas_um_traces WHERE trace = ?`, ev.TraceNumber).Scan(&id, &seq)
		if err != nil {
			result(ev.TraceNumber, "unknown", "no prior authorization request was sent with this trace number")
			continue
		}
		d := umDecision(ev)
		if d.code == pasPended.code {
			result(ev.TraceNumber, "pending", d.why)
			continue
		}
		if _, err := s.decidePAS(r.Context(), id, d, map[int]bool{seq: true}, ""); err != nil {
			if errors.Is(err, ErrNotPended) {
				result(ev.TraceNumber, "already-decided", "the item was not pended")
				continue
			}
			s.internalError(w, r, err)
			return
		}
		result(ev.TraceNumber, "decided", d.display+" on ClaimResponse/"+id+", item "+fmt.Sprint(seq))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resourceType": "Parameters", "parameter": results})
}
