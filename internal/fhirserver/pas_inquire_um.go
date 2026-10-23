package fhirserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/x12"
)

// $inquire through the payer's utilization management system.
//
// A provider's $inquire must find a prior authorization however it was asked for: by fax, through the payer's portal, by phone,
// or in a 278 someone else sent. PAS's $inquire "follows the X12 278 Inquiry rules", and one of its authors confirmed on
// chat.fhir.org that it can return authorizations submitted in other ways, built from what the payer holds. This server only
// knows the requests that came through it; the UM system knows them all. So, with a UM link that answers in real time, an
// inquiry is also sent there as a 278 inquiry (005010X215), and every authorization it answers with that this server does not
// already hold comes back as one more Claim Inquiry Response Bundle.
//
// What is never done: a UM answer is never written into this server's own records (an inquiry reads, it does not decide), and
// no Claim is made up for an authorization nobody submitted in FHIR. Its ClaimResponse.request carries a data-absent-reason
// instead, as PAS's profile allows.

// pasInquiryWindow is how far either side of today an inquiry with no dates searches: a year back for what was decided, a
// year ahead for what was certified for later.
const pasInquiryWindow = 365 * 24 * time.Hour

const umTraceSystem = "https://perfuse.health/fhir/sid/x12-278-trace"

// toServiceReviewInquiry is a PAS inquiry as a 278 inquiry.
func (um *PASUM) toServiceReviewInquiry(req *pasRequest, id string, now time.Time) x12.ServiceReviewInquiry {
	base, _ := um.toServiceReview(req, id, now)
	q := x12.ServiceReviewInquiry{Envelope: base.Envelope, Payer: base.Payer, Provider: base.Provider, Subscriber: base.Subscriber,
		Reference: base.Reference, Trace: strings.ToUpper(id[:8]) + "-Q"}
	// The number the payer gave, when the provider has it: the Claim's insurance.preAuthRef.
	for _, ins := range asSliceAny(req.Claim["insurance"]) {
		for _, ref := range asSliceAny(asMapAny(ins)["preAuthRef"]) {
			if q.AuthorizationNumber == "" {
				q.AuthorizationNumber = str(ref)
			}
		}
	}
	for _, it := range asSliceAny(req.Claim["item"]) {
		if c := pasExtCode(asMapAny(it), "extension-serviceItemRequestType", "AR", "HS", "SC"); c != "" && q.Category == "" {
			q.Category = c
		}
	}
	// The dates: the Claim's billablePeriod, else the items' service dates, else a year either side of today.
	from, to := ccyymmdd(str(asMapAny(req.Claim["billablePeriod"])["start"])), ccyymmdd(str(asMapAny(req.Claim["billablePeriod"])["end"]))
	if from == "" {
		for _, it := range asSliceAny(req.Claim["item"]) {
			item := asMapAny(it)
			for _, d := range []string{str(item["servicedDate"]), str(asMapAny(item["servicedPeriod"])["start"]),
				str(asMapAny(item["servicedPeriod"])["end"])} {
				if d = ccyymmdd(d); d != "" {
					if from == "" || d < from {
						from = d
					}
					if d > to {
						to = d
					}
				}
			}
		}
	}
	if from == "" && q.AuthorizationNumber == "" {
		from, to = now.Add(-pasInquiryWindow).Format("20060102"), now.Add(pasInquiryWindow).Format("20060102")
	}
	q.From, q.To = from, to
	return q
}

// inquireUM asks the UM system, and returns a Claim Inquiry Response Bundle for each authorization it holds that is not
// among found (this server's own answers). Whatever goes wrong is logged and recorded on the link's status; the provider still
// gets what this server holds.
func (s *Server) inquireUM(ctx context.Context, req *pasRequest, found []map[string]any, now time.Time) []map[string]any {
	um := s.PAS.UM
	if um == nil || um.Send == nil || !um.Inquire {
		return nil
	}
	id := newUUID()
	warn := func(msg string, args ...any) {
		if um.Log != nil {
			um.Log.Warn("PAS $inquire: "+msg, args...)
		}
	}
	inquiry := um.toServiceReviewInquiry(req, id, now)
	control, err := s.nextUMControl(ctx, id, "278 inquiry", now)
	if err != nil {
		warn("no control number", "error", err)
		return nil
	}
	inquiry.Envelope.ControlNumber = control
	raw, err := x12.BuildServiceReviewInquiry(inquiry)
	if err != nil {
		s.markUM(ctx, control, "not sent", err.Error(), now)
		warn("the inquiry was not sent to utilization management", "why", err)
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	answer, err := um.Send(cctx, raw)
	switch {
	case errors.Is(err, ErrUMAnswersLater):
		// A batch link has no answer to give a provider waiting now.
		s.markUM(ctx, control, "sent", "inquiry delivered; a batch answer cannot answer $inquire", now)
		return nil
	case err != nil:
		s.markUM(ctx, control, "unreachable", err.Error(), now)
		warn("utilization management did not answer the inquiry", "error", err)
		return nil
	}
	msg, err := x12.Parse(answer)
	if err != nil {
		s.markUM(ctx, control, "unreadable", "the answer is not X12: "+err.Error(), now)
		return nil
	}
	if msg.IsAcknowledgement() {
		status, detail := "acknowledged", "inquiry acknowledged with no answer"
		if ack, err := msg.ReadAcknowledgement(); err == nil {
			detail = ack.Summary()
			if ack.Rejected() {
				status = "rejected"
			}
		}
		s.markUM(ctx, control, status, detail, now)
		warn("utilization management answered the inquiry with an acknowledgement", "detail", detail)
		return nil
	}
	review, err := msg.ParseServiceReview()
	if err != nil || !review.IsResponse {
		s.markUM(ctx, control, "unreadable", "the answer is not a 278 response", now)
		return nil
	}

	known := s.knownAuthorizations(ctx, found)
	var out []map[string]any
	for _, ev := range review.Events {
		if len(ev.Rejections) > 0 || ev.Outcome == x12.OutcomeRequest {
			// AAA: nothing found (AA), or the inquiry could not be matched; nothing to return for it.
			continue
		}
		if s.alreadyHeld(ctx, ev, known) {
			continue
		}
		out = append(out, umInquiryBundle(req, ev, now))
	}
	s.markUM(ctx, control, "answered", fmt.Sprintf("inquiry: %d authorizations held there, %d not submitted through this server",
		len(review.Events), len(out)), now)
	return out
}

// knownAuthorizations are the authorization numbers in this server's own answers.
func (s *Server) knownAuthorizations(_ context.Context, found []map[string]any) map[string]bool {
	known := map[string]bool{}
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			if ref := str(v["preAuthRef"]); ref != "" {
				known[ref] = true
			}
			if str(v["url"]) == "number" && str(v["valueString"]) != "" {
				known[str(v["valueString"])] = true
			}
			for _, c := range v {
				walk(c)
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	for _, b := range found {
		walk(b)
	}
	return known
}

// alreadyHeld says whether a UM answer is about a request this server sent there: by a trace number it sent, or by an
// authorization number already in its own answers.
func (s *Server) alreadyHeld(ctx context.Context, ev x12.ReviewEvent, known map[string]bool) bool {
	for _, n := range []string{ev.AuthorisationNumber, ev.References["BB"]} {
		if n != "" && known[n] {
			return true
		}
	}
	for _, l := range ev.Lines {
		if l.AuthorisationNumber != "" && known[l.AuthorisationNumber] {
			return true
		}
	}
	for _, trace := range ev.TraceNumbers {
		var n int
		if err := s.Store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pas_um_traces WHERE trace = ?`, trace).Scan(&n); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// fhirDate is CCYYMMDD as a FHIR date; empty when it is not one.
func fhirDate(d string) string {
	if len(d) != 8 {
		return ""
	}
	return d[:4] + "-" + d[4:6] + "-" + d[6:]
}

func datePeriod(start, end string) map[string]any {
	p := map[string]any{}
	if s := fhirDate(start); s != "" {
		p["start"] = s
	}
	if e := fhirDate(end); e != "" {
		p["end"] = e
	} else if s := fhirDate(start); s != "" {
		p["end"] = s
	}
	if len(p) == 0 {
		return nil
	}
	return p
}

// procedureCoding is an SV1/SV2 procedure as a coding: HC is CPT, or HCPCS when the code starts with a letter.
func procedureCoding(qualifier, code string) map[string]any {
	if code == "" || qualifier != "HC" {
		return nil
	}
	system := cptSystem
	if c := code[0]; c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
		system = "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"
	}
	return map[string]any{"system": system, "code": code}
}

// umInquiryBundle is one authorization the UM system holds, as a Claim Inquiry Response Bundle.
func umInquiryBundle(req *pasRequest, ev x12.ReviewEvent, now time.Time) map[string]any {
	claim := req.Claim
	id := newUUID()
	c := &pasContext{req: req, id: id}
	ref := func(field string) map[string]any {
		return map[string]any{"reference": str(asMapAny(claim[field])["reference"])}
	}
	trace := ev.TraceNumber
	if len(ev.TraceNumbers) > 0 {
		// The payer's own trace comes first in its answer; the one after it repeats the inquiry's.
		trace = ev.TraceNumbers[0]
	}
	if trace == "" {
		trace = ev.AuthorisationNumber
	}
	if trace == "" {
		trace = strings.ToUpper(id[:8])
	}
	c.note("Found in the payer's utilization management system. It was not submitted through this server, so its request " +
		"is not shown; what was asked for is as the utilization management system reports it.")

	type line struct {
		d          itemDecision
		coding     map[string]any
		qty        string
		start, end string
	}
	var lines []line
	eventDecision := umDecision(ev)
	for _, l := range ev.Lines {
		one := ev
		one.Lines = []x12.ReviewLine{l}
		d := umDecision(one)
		if l.Outcome == x12.OutcomeRequest || l.Outcome == "" {
			d = eventDecision
		}
		lines = append(lines, line{d: d, coding: procedureCoding(l.ProcedureQualifier, l.Procedure), qty: l.Quantity,
			start: l.ServiceDate, end: l.ServiceEndDate})
	}
	if len(lines) == 0 {
		lines = append(lines, line{d: eventDecision, start: ev.ServiceDate, end: ev.ServiceEndDate})
	}
	number := ev.AuthorisationNumber
	var items []any
	approved := false
	for i, l := range lines {
		item := map[string]any{"itemSequence": i + 1, "adjudication": []any{reviewAction(l.d, number)}}
		var ext []any
		what := "the event"
		if l.coding != nil {
			what = fmt.Sprintf("%s %s", map[bool]string{true: "CPT", false: "HCPCS"}[l.coding["system"] == cptSystem], l.coding["code"])
		}
		if certifies(l.d) {
			approved = true
			if l.coding != nil {
				detail := []any{map[string]any{"url": "productOrServiceCode", "valueCodeableConcept": map[string]any{"coding": []any{l.coding}}}}
				if q, err := strconv.ParseFloat(l.qty, 64); err == nil && q > 0 {
					detail = append(detail, map[string]any{"url": "quantity", "valueQuantity": map[string]any{"value": q}})
				}
				ext = append(ext, map[string]any{"url": pasBase + "extension-itemAuthorizedDetail", "extension": detail})
			}
			if p := datePeriod(l.start, l.end); p != nil {
				ext = append(ext, map[string]any{"url": pasBase + "extension-itemPreAuthPeriod", "valuePeriod": p})
			}
		}
		if nt := ev.References["NT"]; nt != "" {
			ext = append(ext, map[string]any{"url": pasBase + "extension-administrationReferenceNumber", "valueString": nt})
		}
		if len(ext) > 0 {
			item["extension"] = ext
		}
		item["noteNumber"] = []any{c.note(fmt.Sprintf("Item %d (%s): %s", i+1, what, l.d.why))}
		items = append(items, item)
	}

	cr := map[string]any{
		"resourceType": "ClaimResponse",
		"id":           id,
		"meta":         map[string]any{"profile": []any{pasBase + "profile-claiminquiryresponse|" + pasVersion}},
		"identifier":   []any{map[string]any{"system": umTraceSystem, "value": trace}},
		"status":       "active",
		"type":         claim["type"],
		"use":          "preauthorization",
		"patient":      ref("patient"),
		"created":      now.Format(time.RFC3339),
		"insurer":      ref("insurer"),
		"requestor":    ref("provider"),
		// No FHIR Claim was ever submitted for this authorization; none is made up.
		"request": map[string]any{"extension": []any{map[string]any{
			"url": "http://hl7.org/fhir/StructureDefinition/data-absent-reason", "valueCode": "unsupported"}},
			"display": "Not submitted in FHIR: held by the payer's utilization management system"},
		"outcome":     "complete",
		"item":        items,
		"processNote": c.notes,
	}
	if approved && number != "" {
		cr["preAuthRef"] = number
		if p := datePeriod(ev.ServiceDate, ev.ServiceEndDate); p != nil {
			cr["preAuthPeriod"] = p
		}
	}
	if ins := asSliceAny(claim["insurance"]); len(ins) > 0 {
		var out []any
		for _, i := range ins {
			im := asMapAny(i)
			out = append(out, map[string]any{"sequence": im["sequence"], "focal": im["focal"], "coverage": im["coverage"]})
		}
		cr["insurance"] = out
	}
	return pasBundle(req, cr, "profile-pas-inquiry-response-bundle", now)
}
