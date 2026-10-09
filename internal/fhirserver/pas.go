package fhirserver

// Da Vinci PAS 2.2.1, the payer's side: Claim/$submit answers a prior authorization request, Claim/$inquire reports where one
// stands, and the result of a request that was pended reaches the provider on the PAS subscription topic.
//
// # Who decides
//
// The payer's rules, the same file CRD answers coverage questions from (-crd-rules): a rule's pa_decision, or what its coverage
// answer implies - not covered is denied, no prior authorization needed is approved. Everything else is pended, because a
// decision nobody wrote down is a decision for a person: a reviewer answers it with Claim/$decide, and that answer is what the
// subscription delivers. Nothing here invents an approval.
//
// # What is kept
//
// Every request and every version of its response, in pas_requests and pas_responses, beside the FHIR store rather than in it:
// a PAS Bundle is a message, not a resource to search, and $inquire and the notification both need the response exactly as it
// was sent. A decision and its notification are written in one transaction, as a FHIR write and its notification are.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

const (
	pasBase      = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/"
	pasVersion   = "2.2.1"
	pasTopicURL  = "http://hl7.org/fhir/us/davinci-pas/SubscriptionTopic/PASSubscriptionTopic"
	pasTempCodes = "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes"
	x12Action    = "https://codesystem.x12.org/005010/306"

	// pasAuthDays is how long an approval holds when the rules do not say.
	pasAuthDays = 90
)

// PAS turns on the Da Vinci PAS payer operations. Nil leaves them off.
type PAS struct {
	// Decide answers one service without a reviewer. Nil pends everything, which is the honest default for a payer that has
	// written no rules.
	Decide func(codes ...map[string]any) PASAnswer
	// Now is the clock, for tests. Nil is time.Now.
	Now func() time.Time
	// UM, when set, sends every request to the payer's utilization management system as an X12 278 and answers with its
	// decisions; Decide is then not consulted.
	UM *PASUM
}

// PASAnswer is the payer's rule for one service.
type PASAnswer struct {
	// Decision is "approve", "deny" or "pend".
	Decision string
	// Why is said in the response's process note.
	Why string
	// AllowedQuantity, when above 0, certifies a request for more as modified, for this many.
	AllowedQuantity float64
	// Questionnaire and Attachments say what a pended request must be supported with: the DTR questionnaire to fill in and
	// the LOINC attachment codes of the documents to send. The response asks for them with a Task and a CommunicationRequest.
	Questionnaire string
	Attachments   []string
	// AttachmentModifiers are LOINC attachment modifier codes sent with each attachment asked for.
	AttachmentModifiers []string
	// Alternative is a service approved instead (a Coding): the item is answered modified and the alternative added.
	Alternative map[string]any
}

func (p *PAS) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// pasTopic is PAS's result-available: a pended request has its answer. Its notification is the PAS Response Bundle, filtered by
// the identifier of the provider organization that asked.
var pasTopic = Topic{
	URL: pasTopicURL, Title: "Prior authorization result available", ResourceType: "ClaimResponse",
	// orgIdentifier is the topic's name for it; org-identifier is how the IG's own example Subscription writes it.
	Filters:   []string{"org-identifier", "orgIdentifier"},
	published: true,
	match: func(tree map[string]any, filters url.Values) bool {
		have := map[string]bool{}
		for _, id := range bundleRequestorIdentifiers(tree) {
			have[id] = true
		}
		for _, values := range filters {
			ok := false
			for _, v := range values {
				ok = ok || have[strings.TrimSpace(v)]
			}
			if !ok {
				return false
			}
		}
		return true
	},
	payload: func(ctx context.Context, db *sql.DB, id string, version int) (json.RawMessage, error) {
		var raw string
		err := db.QueryRowContext(ctx, `SELECT response FROM pas_responses WHERE id = ? AND version = ?`, id, version).Scan(&raw)
		return json.RawMessage(raw), err
	},
}

func (s *Server) registerPAS(mux *http.ServeMux) {
	mux.HandleFunc("POST /Claim/$submit", s.handlePASSubmit)
	mux.HandleFunc("POST /Claim/$inquire", s.handlePASInquire)
	mux.HandleFunc("POST /Claim/$decide", s.handlePASDecide)
	mux.HandleFunc("POST /Claim/$decide-278", s.handlePASDecide278)
	mux.HandleFunc("POST /Claim/$submit-attachment", s.handleSubmitAttachment)
	mux.HandleFunc("POST /$submit-attachment", s.handleSubmitAttachment)
}

func (s *Server) pasReady(ctx context.Context) error {
	s.pasOnce.Do(func() {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS pas_requests (
				id TEXT PRIMARY KEY,
				trace TEXT NOT NULL,
				member TEXT NOT NULL,
				provider TEXT NOT NULL,
				claim_url TEXT NOT NULL,
				pended INTEGER NOT NULL,
				version INTEGER NOT NULL,
				created INTEGER NOT NULL,
				updated INTEGER NOT NULL,
				request TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS pas_tracking (
				tracking TEXT PRIMARY KEY,
				id TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS pas_attachments (
				id TEXT NOT NULL,
				tracking TEXT NOT NULL,
				resource_type TEXT NOT NULL,
				code TEXT NOT NULL,
				line_items TEXT NOT NULL,
				final INTEGER NOT NULL,
				received INTEGER NOT NULL,
				content TEXT NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS pas_attachments_id ON pas_attachments (id)`,
			`CREATE TABLE IF NOT EXISTS pas_um_traces (
				trace TEXT PRIMARY KEY,
				id TEXT NOT NULL,
				seq INTEGER NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS pas_responses (
				id TEXT NOT NULL,
				version INTEGER NOT NULL,
				response TEXT NOT NULL,
				PRIMARY KEY (id, version))`,
		} {
			if _, err := s.Store.db.ExecContext(ctx, stmt); err != nil {
				s.pasErr = fmt.Errorf("preparing the PAS tables: %w", err)
				return
			}
		}
	})
	return s.pasErr
}

func (s *Server) pasOff(w http.ResponseWriter, r *http.Request) bool {
	if s.PAS == nil {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			"Da Vinci PAS is not enabled on this server (serve -pas)")
		return true
	}
	if err := s.pasReady(r.Context()); err != nil {
		s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception", err.Error())
		return true
	}
	return false
}

// pasBody reads the request Bundle: the body itself, or the resource parameter of a Parameters body.
func (s *Server) pasBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	raw, ok := s.readBody(w, r)
	if !ok {
		return nil, false
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body is not JSON: "+err.Error())
		return nil, false
	}
	if body["resourceType"] == "Parameters" {
		for _, p := range asSliceAny(body["parameter"]) {
			pm := asMapAny(p)
			if pm["name"] == "resource" {
				body = asMapAny(pm["resource"])
			}
		}
	}
	return body, true
}

// pasRequest is a request Bundle, read: its Claim and how its references resolve.
type pasRequest struct {
	bundle   map[string]any
	claim    map[string]any
	claimURL string
	entries  []map[string]any
	// um holds the payer UM system's answers by item sequence, when the request was forwarded as an X12 278; umAuth is the
	// authorization number it assigned, and umTraces the 278 trace numbers sent, by item sequence.
	um       map[string]itemDecision
	umAuth   string
	umTraces map[string]string
}

// readPASRequest checks what this server needs to answer and says everything missing at once. It does not repeat the
// profile validation a client should have done; it refuses what could not be answered correctly.
func readPASRequest(b map[string]any, inquiry bool) (*pasRequest, []string) {
	var problems []string
	if b["resourceType"] != "Bundle" {
		return nil, []string{"the request must be a PAS Request Bundle, and this is not a Bundle"}
	}
	if b["type"] != "collection" {
		problems = append(problems, fmt.Sprintf("Bundle.type must be collection, not %q", str(b["type"])))
	}
	req := &pasRequest{bundle: b}
	for _, e := range asSliceAny(b["entry"]) {
		req.entries = append(req.entries, asMapAny(e))
	}
	if len(req.entries) == 0 {
		return nil, append(problems, "the Bundle has no entries; its first entry must be the Claim")
	}
	req.claim = asMapAny(req.entries[0]["resource"])
	req.claimURL = str(req.entries[0]["fullUrl"])
	if req.claim["resourceType"] != "Claim" {
		return nil, append(problems, "the Bundle's first entry must be the Claim")
	}
	if req.claimURL == "" {
		problems = append(problems, "the Claim's entry has no fullUrl")
	}
	if req.claim["use"] != "preauthorization" {
		problems = append(problems, fmt.Sprintf("Claim.use must be preauthorization, not %q", str(req.claim["use"])))
	}
	for _, field := range []string{"patient", "insurer", "provider"} {
		ref := str(asMapAny(req.claim[field])["reference"])
		if ref == "" {
			problems = append(problems, "Claim."+field+" is missing")
		} else if req.resolve(ref) == nil {
			problems = append(problems, fmt.Sprintf("Claim.%s %q is not in the Bundle", field, ref))
		}
	}
	if !inquiry && len(asSliceAny(req.claim["item"])) == 0 {
		problems = append(problems, "the Claim has no items: there is nothing to authorize")
	}
	return req, problems
}

// resolve finds the entry a reference names: by fullUrl, or relative to the Claim's base as FHIR resolves a relative reference
// inside a Bundle.
func (req *pasRequest) resolve(ref string) map[string]any {
	if ref == "" {
		return nil
	}
	full := ref
	if !strings.Contains(ref, ":") && strings.Count(ref, "/") == 1 {
		if base := fhirBaseOf(req.claimURL); base != "" {
			full = base + "/" + ref
		}
	}
	for _, e := range req.entries {
		u := str(e["fullUrl"])
		if u == ref || u == full {
			return e
		}
		res := asMapAny(e["resource"])
		if u == "" && ref == str(res["resourceType"])+"/"+str(res["id"]) {
			return e
		}
	}
	return nil
}

// fhirBaseOf is the base of an absolute resource URL: https://x/fhir/Claim/1 is https://x/fhir.
func fhirBaseOf(u string) string {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	parts := strings.Split(strings.TrimRight(u, "/"), "/")
	if len(parts) < 5 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], "/")
}

func (s *Server) refusePAS(w http.ResponseWriter, r *http.Request, problems []string) {
	issues := make([]any, 0, len(problems))
	for _, p := range problems {
		issues = append(issues, map[string]any{"severity": "error", "code": "invalid", "diagnostics": p})
	}
	s.writeJSON(w, http.StatusBadRequest, map[string]any{"resourceType": "OperationOutcome", "issue": issues})
}

func (s *Server) handlePASSubmit(w http.ResponseWriter, r *http.Request) {
	if s.pasOff(w, r) {
		return
	}
	body, ok := s.pasBody(w, r)
	if !ok {
		return
	}
	req, problems := readPASRequest(body, false)
	if len(problems) > 0 {
		s.refusePAS(w, r, problems)
		return
	}
	now := s.PAS.now().UTC()
	id := newUUID()
	if s.PAS.UM != nil && s.PAS.UM.Send != nil {
		s.askUM(r.Context(), req, id, now)
	}
	response, pended := s.pasRespond(req, id, now)
	if err := s.savePAS(r.Context(), req, id, response, pended, now); err != nil {
		s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, response)
}

// itemDecision is one item's answer.
type itemDecision struct {
	code, display, why string
	answer             PASAnswer
	// number is the authorization number when someone else assigned it (the payer's UM system, over X12); empty means this
	// server's own.
	number string
	// missing names what made the item impossible to decide, for an error rather than a decision.
	missing string
}

var (
	pasApproved  = itemDecision{code: "A1", display: "Certified in total"}
	pasDenied    = itemDecision{code: "A3", display: "Not Certified"}
	pasPended    = itemDecision{code: "A4", display: "Pending"}
	pasModified  = itemDecision{code: "A6", display: "Modified"}
	pasPartial   = itemDecision{code: "A2", display: "Certified - partial"}
	pasCancelled = itemDecision{code: "C", display: "Cancelled"}
)

// cancels says whether a Claim or one of its items carries certificationType 3, which is how PAS cancels.
func cancels(o map[string]any) bool {
	for _, e := range asSliceAny(o["extension"]) {
		em := asMapAny(e)
		if str(em["url"]) != pasBase+"extension-certificationType" {
			continue
		}
		for _, c := range asSliceAny(asMapAny(em["valueCodeableConcept"])["coding"]) {
			if str(asMapAny(c)["code"]) == "3" {
				return true
			}
		}
	}
	return false
}

// usableCode says whether a CodeableConcept names a service, rather than saying it has none (data-absent-reason).
func usableCode(cc map[string]any) bool {
	for _, c := range asSliceAny(cc["coding"]) {
		cm := asMapAny(c)
		if str(cm["code"]) != "" && str(cm["system"]) != "http://terminology.hl7.org/CodeSystem/data-absent-reason" {
			return true
		}
	}
	return false
}

func (s *Server) decideItem(req *pasRequest, item map[string]any) itemDecision {
	if cancels(req.claim) || cancels(item) {
		return pasCancelled
	}
	var codes []map[string]any
	if cc := asMapAny(item["productOrService"]); usableCode(cc) {
		codes = append(codes, cc)
	}
	for _, e := range asSliceAny(item["extension"]) {
		em := asMapAny(e)
		if str(em["url"]) != pasBase+"extension-requestedService" {
			continue
		}
		if order := req.resolve(str(asMapAny(em["valueReference"])["reference"])); order != nil {
			res := asMapAny(order["resource"])
			for _, f := range []string{"code", "codeCodeableConcept", "medicationCodeableConcept"} {
				if cc := asMapAny(res[f]); usableCode(cc) {
					codes = append(codes, cc)
				}
			}
		}
	}
	if len(codes) == 0 {
		// Nothing to decide about. X12 answers this with AAA, an error, not with a decision.
		return itemDecision{missing: "productOrService"}
	}
	if req.um != nil {
		// The payer's UM system decides, over X12; its rules are not this server's to second-guess.
		if d, ok := req.um[fmt.Sprint(item["sequence"])]; ok {
			return d
		}
		return itemDecision{code: pasPended.code, display: pasPended.display,
			why: "The utilization management system gave no answer for this service; it is pended for a reviewer."}
	}
	a := PASAnswer{Decision: "pend"}
	if s.PAS.Decide != nil {
		a = s.PAS.Decide(codes...)
	}
	var d itemDecision
	switch a.Decision {
	case "approve":
		d = pasApproved
		if a.Alternative != nil {
			d = pasModified
		} else if q, _ := asMapAny(item["quantity"])["value"].(float64); a.AllowedQuantity > 0 && q > a.AllowedQuantity {
			d = pasModified
		}
	case "deny":
		d = pasDenied
	default:
		d = pasPended
		if a.Why == "" {
			a.Why = "No rule decides this service; it is pended for a reviewer."
		}
	}
	d.why, d.answer = a.Why, a
	return d
}

// pasContext is what one response is built from.
type pasContext struct {
	req        *pasRequest
	id         string
	authNumber string
	today      string
	until      string
	notes      []any
}

func (c *pasContext) note(text string) int {
	n := nextNote(c.notes)
	c.notes = append(c.notes, map[string]any{"number": n, "type": "display", "text": text})
	return n
}

func reviewAction(d itemDecision, number string) map[string]any {
	ext := []any{map[string]any{"url": pasBase + "extension-reviewActionCode", "valueCodeableConcept": map[string]any{
		"coding": []any{map[string]any{"system": x12Action, "code": d.code, "display": d.display}}}}}
	if certifies(d) {
		if d.number != "" {
			number = d.number
		}
		ext = append(ext, map[string]any{"url": "number", "valueString": number})
	}
	return map[string]any{
		"extension": []any{map[string]any{"url": pasBase + "extension-reviewAction", "extension": ext}},
		"category": map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/adjudication", "code": "submitted"}}},
	}
}

// certifies says whether a decision approves something, in whole, in part or modified: what carries an authorization number.
func certifies(d itemDecision) bool {
	return d.code == pasApproved.code || d.code == pasModified.code || d.code == pasPartial.code
}

// careTeamProviders are the authorized providers an item (or, with sequences nil, the whole request) names through
// Claim.careTeam, as itemAuthorizedProvider extensions.
func (c *pasContext) careTeamProviders(sequences []any) []any {
	var out []any
	for _, ct := range asSliceAny(c.req.claim["careTeam"]) {
		ctm := asMapAny(ct)
		if sequences == nil {
			whole := false
			for _, e := range asSliceAny(ctm["extension"]) {
				if str(asMapAny(e)["url"]) == pasBase+"extension-careTeamClaimScope" && asMapAny(e)["valueBoolean"] == true {
					whole = true
				}
			}
			if !whole {
				continue
			}
		} else {
			named := false
			for _, seq := range sequences {
				named = named || fmt.Sprint(seq) == fmt.Sprint(ctm["sequence"])
			}
			if !named {
				continue
			}
		}
		ref := str(asMapAny(ctm["provider"])["reference"])
		if role := c.req.resolve(ref); role != nil && asMapAny(role["resource"])["resourceType"] == "PractitionerRole" {
			// The authorized provider is a Practitioner or an Organization; a role names both.
			rr := asMapAny(role["resource"])
			ref = str(asMapAny(rr["practitioner"])["reference"])
			if ref == "" {
				ref = str(asMapAny(rr["organization"])["reference"])
			}
		}
		if ref == "" {
			continue
		}
		parts := []any{map[string]any{"url": "provider", "valueReference": map[string]any{"reference": ref}}}
		if role := ctm["role"]; role != nil {
			parts = append(parts, map[string]any{"url": "role", "valueCodeableConcept": role})
		}
		if q := ctm["qualification"]; q != nil {
			parts = append(parts, map[string]any{"url": "qualification", "valueCodeableConcept": q})
		}
		out = append(out, map[string]any{"url": pasBase + "extension-itemAuthorizedProvider", "extension": parts})
	}
	return out
}

// encounterPeriod is the period of the encounter the Claim names, when it has one.
func (c *pasContext) encounterPeriod() map[string]any {
	for _, e := range asSliceAny(c.req.claim["extension"]) {
		em := asMapAny(e)
		if !strings.HasSuffix(str(em["url"]), "extension-Claim.encounter") {
			continue
		}
		if enc := c.req.resolve(str(asMapAny(em["valueReference"])["reference"])); enc != nil {
			return asMapAny(asMapAny(enc["resource"])["period"])
		}
	}
	return nil
}

// itemEchoes are what a 278 response repeats about a service line, and the payer's own reference for it.
func (c *pasContext) itemEchoes(item map[string]any, d itemDecision) []any {
	seq := fmt.Sprint(item["sequence"])
	ext := copyExtensions(item, pasBase+"extension-itemTraceNumber")
	// The number the provider gave as a previous authorization, under the name the response uses for it.
	ext = append(ext, copyExtensions(item, pasBase+"extension-authorizationNumber")...)
	// The utilization management organization's own reference for this line; a replaced request gets a new one.
	ext = append(ext, map[string]any{"url": pasBase + "extension-administrationReferenceNumber", "valueString": "ADM-" + strings.ToUpper(c.id[:8]) + "-" + seq})
	if v, ok := item["servicedDate"]; ok {
		ext = append(ext, map[string]any{"url": pasBase + "extension-itemRequestedServiceDate", "valueDateTime": v})
	} else if v, ok := item["servicedPeriod"]; ok {
		ext = append(ext, map[string]any{"url": pasBase + "extension-itemRequestedServiceDate", "valuePeriod": v})
	}
	ext = append(ext, map[string]any{"url": pasBase + "extension-itemPreAuthIssueDate", "valueDate": c.today})
	if certifies(d) {
		ext = append(ext, map[string]any{"url": pasBase + "extension-itemPreAuthPeriod", "valuePeriod": map[string]any{"start": c.today, "end": c.until}})
		ext = append(ext, c.careTeamProviders(asSliceAny(item["careTeamSequence"]))...)
		if p := c.encounterPeriod(); p != nil {
			if p["start"] != nil {
				ext = append(ext, map[string]any{"url": pasBase + "extension-admissionDates", "valuePeriod": p})
			}
			if end := str(p["end"]); end != "" {
				ext = append(ext, map[string]any{"url": pasBase + "extension-dischargeDate", "valueDate": end[:min(10, len(end))]})
			}
		}
	}
	// Not ClaimResponse.item.extension:communicatedDiagnosis: the extension's context does not allow ClaimResponse.item,
	// though the profile slices it there. The diagnosis goes on the CommunicationRequest that asks for documents.
	return ext
}

// addedItem is the alternative a payer approves instead of an item, carrying the item's own context.
func (c *pasContext) addedItem(item map[string]any, d itemDecision) map[string]any {
	var ext []any
	for _, e := range asSliceAny(item["extension"]) {
		switch strings.TrimPrefix(str(asMapAny(e)["url"]), pasBase) {
		case "extension-itemTraceNumber", "extension-authorizationNumber", "extension-epsdtIndicator",
			"extension-nursingHomeResidentialStatus", "extension-nursingHomeLevelOfCare", "extension-revenueUnitRateLimit",
			"extension-requestedService", "extension-serviceItemRequestType", "extension-certificationType":
			ext = append(ext, e)
		}
	}
	seq := fmt.Sprint(item["sequence"])
	ext = append(ext,
		map[string]any{"url": pasBase + "extension-administrationReferenceNumber", "valueString": "ADM-" + strings.ToUpper(c.id[:8]) + "-" + seq + "A"},
		map[string]any{"url": pasBase + "extension-itemPreAuthIssueDate", "valueDate": c.today},
		map[string]any{"url": pasBase + "extension-itemPreAuthPeriod", "valuePeriod": map[string]any{"start": c.today, "end": c.until}})
	if rev := item["revenue"]; rev != nil {
		ext = append(ext, map[string]any{"url": pasBase + "extension-revenueCode", "valueCodeableConcept": rev})
	}
	if p := c.encounterPeriod(); p != nil {
		ext = append(ext, map[string]any{"url": pasBase + "extension-admissionDates", "valuePeriod": p})
		if end := str(p["end"]); end != "" {
			ext = append(ext, map[string]any{"url": pasBase + "extension-dischargeDate", "valueDate": end[:min(10, len(end))]})
		}
	}
	add := map[string]any{
		"extension":        ext,
		"itemSequence":     []any{item["sequence"]},
		"productOrService": map[string]any{"coding": []any{d.answer.Alternative}},
		"adjudication":     []any{reviewAction(pasApproved, c.authNumber)},
	}
	for _, f := range []string{"modifier", "servicedDate", "servicedPeriod", "locationCodeableConcept", "locationAddress", "locationReference", "quantity", "unitPrice"} {
		if v, ok := item[f]; ok {
			add[f] = v
		}
	}
	var providers []any
	for _, ct := range asSliceAny(c.req.claim["careTeam"]) {
		ctm := asMapAny(ct)
		for _, seq := range asSliceAny(item["careTeamSequence"]) {
			if fmt.Sprint(seq) == fmt.Sprint(ctm["sequence"]) {
				// X12 98 SJ: the service provider, who gives the service approved instead.
				p := map[string]any{"reference": str(asMapAny(ctm["provider"])["reference"]),
					"extension": []any{map[string]any{"url": pasBase + "extension-authorizedProviderType",
						"valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": "https://codesystem.x12.org/005010/98", "code": "SJ"}}}}}}
				providers = append(providers, p)
			}
		}
	}
	if len(providers) > 0 {
		add["provider"] = providers
	}
	return add
}

// pasRespond builds the PAS Response Bundle. pended says whether any item waits for a reviewer.
func (s *Server) pasRespond(req *pasRequest, id string, now time.Time) (map[string]any, bool) {
	claim := req.claim
	c := &pasContext{req: req, id: id, authNumber: "PA-" + strings.ToUpper(id[:8]),
		today: now.Format("2006-01-02"), until: now.AddDate(0, 0, pasAuthDays).Format("2006-01-02")}
	if req.umAuth != "" {
		// The payer's UM system numbered it; the provider bills against that number, not one this server made up.
		c.authNumber = req.umAuth
	}

	var items, added, errs, docRequests []any
	var extra []map[string]any
	pended, approved := false, false
	decisions := map[string]bool{}
	for i, it := range asSliceAny(claim["item"]) {
		item := asMapAny(it)
		d := s.decideItem(req, item)
		out := map[string]any{"itemSequence": item["sequence"]}
		if d.missing != "" {
			n := c.note(fmt.Sprintf("Item %v names no service, so it cannot be decided; send it again with the service requested.", item["sequence"]))
			out["extension"] = copyExtensions(item, pasBase+"extension-itemTraceNumber")
			out["noteNumber"] = []any{n}
			out["adjudication"] = []any{map[string]any{"category": map[string]any{"coding": []any{map[string]any{
				"system": "http://terminology.hl7.org/CodeSystem/adjudication", "code": "submitted"}}}}}
			errs = append(errs, map[string]any{
				"extension": []any{
					// X12 889 C: please correct and resubmit.
					map[string]any{"url": pasBase + "extension-errorFollowupAction", "valueCodeableConcept": map[string]any{"coding": []any{
						map[string]any{"system": "https://codesystem.x12.org/005010/889", "code": "C"}}}},
					map[string]any{"url": pasBase + "extension-errorElement", "extension": []any{
						map[string]any{"url": "processNote", "valuePositiveInt": n},
						map[string]any{"url": "error", "valueString": "SV1-01"}}},
					map[string]any{"url": pasBase + "extension-errorPath", "valueString": fmt.Sprintf("Claim.item[%d].%s", i, d.missing)},
				},
				"itemSequence": item["sequence"],
				// X12 901 AG: invalid or missing procedure code.
				"code": map[string]any{"coding": []any{map[string]any{"system": "https://codesystem.x12.org/005010/901", "code": "AG"}}},
			})
			items = append(items, out)
			continue
		}
		decisions[d.code] = true
		out["extension"] = c.itemEchoes(item, d)
		if d.code == pasModified.code && d.answer.Alternative == nil {
			// Certified for fewer than asked: what was authorized.
			detail := []any{map[string]any{"url": "productOrServiceCode", "valueCodeableConcept": item["productOrService"]},
				map[string]any{"url": "quantity", "valueQuantity": withValue(asMapAny(item["quantity"]), d.answer.AllowedQuantity)}}
			if up := item["unitPrice"]; up != nil {
				detail = append(detail, map[string]any{"url": "unitPrice", "valueMoney": up})
			}
			out["extension"] = append(asSliceAny(out["extension"]), map[string]any{"url": pasBase + "extension-itemAuthorizedDetail", "extension": detail})
			d.why = strings.TrimSpace(d.why + fmt.Sprintf(" Certified for %v.", d.answer.AllowedQuantity))
		}
		if d.answer.Alternative != nil && d.code == pasModified.code {
			added = append(added, c.addedItem(item, d))
			d.why = strings.TrimSpace(d.why + " Approved as the added item instead.")
		}
		out["adjudication"] = []any{reviewAction(d, c.authNumber)}
		if d.why != "" {
			out["noteNumber"] = []any{c.note(d.why)}
		}
		approved = approved || certifies(d)
		if d.code == pasPended.code {
			pended = true
			if len(d.answer.Attachments) > 0 || d.answer.Questionnaire != "" {
				docRequests = append(docRequests, map[string]any{"item": item, "answer": d.answer})
			}
		}
		items = append(items, out)
	}

	ref := func(field string) map[string]any {
		e := req.resolve(str(asMapAny(claim[field])["reference"]))
		return map[string]any{"reference": str(e["fullUrl"])}
	}
	ext := []any{map[string]any{"url": pasBase + "extension-claimResponseReviewer",
		"extension": []any{map[string]any{"url": "wasHumanReviewedFlag", "valueBoolean": false}}}}
	if t := transmissionReply(claim); t != nil {
		ext = append([]any{t}, ext...)
	}
	cr := map[string]any{
		"resourceType": "ClaimResponse",
		"id":           id,
		"meta":         map[string]any{"profile": []any{pasBase + "profile-claimresponse|" + pasVersion}},
		"extension":    ext,
		"status":       "active",
		"type":         claim["type"],
		"use":          "preauthorization",
		"patient":      ref("patient"),
		"created":      now.Format(time.RFC3339),
		"insurer":      ref("insurer"),
		"requestor":    ref("provider"),
		"request":      map[string]any{"reference": req.claimURL},
		// Outcome is processing, not the decision: a pended request was processed and its answer is A4 (see pas278).
		"outcome": "complete",
		"item":    items,
	}
	if len(errs) > 0 {
		cr["outcome"] = "partial"
		cr["error"] = errs
	}
	if ids := asSliceAny(claim["identifier"]); len(ids) > 0 {
		cr["identifier"] = ids
	}
	if len(added) > 0 {
		cr["addItem"] = added
	}
	// Not sent: ClaimResponse.adjudication and ClaimResponse.extension:authorizedProvider, though the profile marks them
	// must-support. The reviewActionCode and itemAuthorizedProvider extensions they would need are not allowed there by their
	// own context in PAS 2.2.1, so the validator rejects the response that carries them.
	_ = decisions
	if approved {
		cr["preAuthRef"] = c.authNumber
		cr["preAuthPeriod"] = map[string]any{"start": c.today, "end": c.until}
	}
	if len(docRequests) > 0 {
		var crs []any
		task, comms := s.documentationRequests(c, docRequests)
		for _, comm := range comms {
			extra = append(extra, comm)
			crs = append(crs, map[string]any{"reference": "urn:uuid:" + str(comm["id"])})
		}
		cr["communicationRequest"] = crs
		extra = append(extra, task)
	}
	if len(c.notes) > 0 {
		cr["processNote"] = c.notes
	}
	if len(asSliceAny(claim["insurance"])) > 0 {
		var ins []any
		for _, i := range asSliceAny(claim["insurance"]) {
			im := asMapAny(i)
			cov := req.resolve(str(asMapAny(im["coverage"])["reference"]))
			ins = append(ins, map[string]any{"sequence": im["sequence"], "focal": im["focal"],
				"coverage": map[string]any{"reference": str(cov["fullUrl"])}})
		}
		cr["insurance"] = ins
	}
	b := pasBundle(req, cr, "profile-pas-response-bundle", now, extra...)
	return b, pended
}

// transmissionReply is the request's transmission identifiers as the reply carries them: sender and receiver swapped.
func transmissionReply(claim map[string]any) map[string]any {
	for _, e := range asSliceAny(claim["extension"]) {
		em := asMapAny(e)
		if str(em["url"]) != pasBase+"extension-TransmissionIdentifiers" {
			continue
		}
		swap := map[string]string{"applicationSenderCode": "applicationReceiverCode", "applicationReceiverCode": "applicationSenderCode",
			"interchangeSenderID": "interchangeReceiverID", "interchangeReceiverID": "interchangeSenderID"}
		var parts []any
		for _, p := range asSliceAny(em["extension"]) {
			pm := deepCopy(asMapAny(p))
			if to, ok := swap[str(pm["url"])]; ok {
				pm["url"] = to
			}
			parts = append(parts, pm)
		}
		return map[string]any{"url": em["url"], "extension": parts}
	}
	return nil
}

// documentationRequests asks for what pended items need: one PAS Task for the request (the payer's FHIR base, the
// attachments, the DTR questionnaires) and a CommunicationRequest per document.
func (s *Server) documentationRequests(c *pasContext, pending []any) (map[string]any, []map[string]any) {
	identifierRef := func(field string) map[string]any {
		e := c.req.resolve(str(asMapAny(c.req.claim[field])["reference"]))
		ids := asSliceAny(asMapAny(e["resource"])["identifier"])
		if len(ids) == 0 {
			return map[string]any{"display": str(asMapAny(e["resource"])["name"])}
		}
		id := asMapAny(ids[0])
		return map[string]any{"identifier": map[string]any{"system": id["system"], "value": id["value"]}}
	}
	inputs := []any{map[string]any{"type": map[string]any{"coding": []any{map[string]any{"system": pasTempCodes, "code": "payer-url"}}},
		"valueUrl": strings.TrimRight(s.BaseURL, "/")}}
	var comms []map[string]any
	code := "attachment-request-code"
	for _, p := range pending {
		pm := asMapAny(p)
		item := asMapAny(pm["item"])
		a := pm["answer"].(PASAnswer)
		line := map[string]any{"url": pasBase + "extension-serviceLineNumber", "valuePositiveInt": item["sequence"]}
		for _, loinc := range a.Attachments {
			var modifiers []any
			for _, m := range a.AttachmentModifiers {
				modifiers = append(modifiers, map[string]any{"url": pasBase + "extension-contentModifier",
					"valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": m}}}})
			}
			inputs = append(inputs, map[string]any{"extension": append([]any{line}, modifiers...),
				"type":                 map[string]any{"coding": []any{map[string]any{"system": pasTempCodes, "code": "attachments-needed"}}},
				"valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": loinc}}}})
			payload := map[string]any{"contentString": loinc}
			var pext []any
			for _, seq := range asSliceAny(item["diagnosisSequence"]) {
				for _, dg := range asSliceAny(c.req.claim["diagnosis"]) {
					if dm := asMapAny(dg); fmt.Sprint(dm["sequence"]) == fmt.Sprint(seq) && dm["diagnosisCodeableConcept"] != nil {
						pext = append(pext, map[string]any{"url": pasBase + "extension-communicatedDiagnosis", "valueCodeableConcept": dm["diagnosisCodeableConcept"]})
					}
				}
			}
			pext = append(pext, modifiers...)
			if len(pext) > 0 {
				payload["extension"] = pext
			}
			comm := map[string]any{
				"resourceType": "CommunicationRequest", "id": newUUID(),
				"meta":       map[string]any{"profile": []any{pasBase + "profile-communicationrequest|" + pasVersion}},
				"extension":  []any{line},
				"identifier": []any{map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + newUUID()}},
				"status":     "active",
				// X12 755 OZ: support data for claim; 756 EL: electronically only.
				"category":  []any{map[string]any{"coding": []any{map[string]any{"system": "https://codesystem.x12.org/005010/755", "code": "OZ"}}}},
				"medium":    []any{map[string]any{"coding": []any{map[string]any{"system": "https://codesystem.x12.org/005010/756", "code": "EL"}}}},
				"payload":   []any{payload},
				"requester": map[string]any{"reference": c.req.claim["insurer"].(map[string]any)["reference"]},
				"recipient": []any{map[string]any{"reference": c.req.claim["insurer"].(map[string]any)["reference"]}},
				"sender":    map[string]any{"reference": c.req.claim["provider"].(map[string]any)["reference"]},
				"subject":   map[string]any{"reference": c.req.claim["patient"].(map[string]any)["reference"]},
			}
			comms = append(comms, comm)
		}
		if a.Questionnaire != "" {
			code = "attachment-request-questionnaire"
			inputs = append(inputs, map[string]any{"extension": []any{line},
				"type":        map[string]any{"coding": []any{map[string]any{"system": pasTempCodes, "code": "questionnaire-context"}}},
				"valueString": a.Questionnaire})
		}
	}
	task := map[string]any{
		"resourceType": "Task", "id": newUUID(),
		"meta":            map[string]any{"profile": []any{pasBase + "profile-task|" + pasVersion}},
		"identifier":      []any{map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + c.id}},
		"status":          "requested",
		"statusReason":    map[string]any{"text": "The prior authorization request is pended until these are received."},
		"intent":          "order",
		"code":            map[string]any{"coding": []any{map[string]any{"system": pasTempCodes, "code": code}}},
		"for":             map[string]any{"reference": c.req.claim["patient"].(map[string]any)["reference"]},
		"requester":       identifierRef("insurer"),
		"owner":           identifierRef("provider"),
		"reasonCode":      map[string]any{"coding": []any{map[string]any{"system": pasTempCodes, "code": "priorAuthorization"}}},
		"reasonReference": map[string]any{"reference": c.req.claimURL},
		"restriction":     map[string]any{"period": map[string]any{"end": c.until}},
		"input":           inputs,
	}
	return task, comms
}

// pasBundle wraps a ClaimResponse with every request resource it reaches, references rewritten to their fullUrls.
func pasBundle(req *pasRequest, cr map[string]any, profile string, now time.Time, extra ...map[string]any) map[string]any {
	entries := []any{map[string]any{"fullUrl": "urn:uuid:" + str(cr["id"]), "resource": cr}}
	for _, x := range extra {
		entries = append(entries, map[string]any{"fullUrl": "urn:uuid:" + str(x["id"]), "resource": x})
	}
	seen := map[string]bool{}
	var walk func(node any, skipRequest bool)
	walk = func(node any, skipRequest bool) {
		switch v := node.(type) {
		case map[string]any:
			for k, child := range v {
				if k == "request" && skipRequest {
					continue
				}
				if k == "reference" {
					e := req.resolve(str(child))
					if e == nil {
						continue
					}
					u := str(e["fullUrl"])
					v[k] = u
					if !seen[u] && u != req.claimURL {
						seen[u] = true
						res := deepCopy(asMapAny(e["resource"]))
						entries = append(entries, map[string]any{"fullUrl": u, "resource": res})
						walk(res, false)
					}
					continue
				}
				walk(child, false)
			}
		case []any:
			for _, c := range v {
				walk(c, false)
			}
		}
	}
	walk(cr, true)
	for _, x := range extra {
		walk(x, false)
	}
	return map[string]any{
		"resourceType": "Bundle",
		"id":           str(cr["id"]),
		"meta":         map[string]any{"profile": []any{pasBase + profile + "|" + pasVersion}},
		"identifier":   map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + newUUID()},
		"type":         "collection",
		"timestamp":    now.Format(time.RFC3339),
		"entry":        entries,
	}
}

func deepCopy(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func copyExtensions(o map[string]any, urls ...string) []any {
	var out []any
	for _, e := range asSliceAny(o["extension"]) {
		for _, u := range urls {
			if str(asMapAny(e)["url"]) == u {
				out = append(out, e)
			}
		}
	}
	return out
}

// identifiersOf lists a resource's identifiers as "system|value" and as the bare value, which is how a subscription filter or
// an inquiry may name them.
func identifiersOf(res map[string]any) []string {
	var out []string
	for _, i := range asSliceAny(res["identifier"]) {
		im := asMapAny(i)
		if v := str(im["value"]); v != "" {
			out = append(out, v)
			if sys := str(im["system"]); sys != "" {
				out = append(out, sys+"|"+v)
			}
		}
	}
	return out
}

func bundleRequestorIdentifiers(bundle map[string]any) []string {
	var cr map[string]any
	byURL := map[string]map[string]any{}
	for _, e := range asSliceAny(bundle["entry"]) {
		em := asMapAny(e)
		res := asMapAny(em["resource"])
		byURL[str(em["fullUrl"])] = res
		if res["resourceType"] == "ClaimResponse" && cr == nil {
			cr = res
		}
	}
	if cr == nil {
		return nil
	}
	requestor := byURL[str(asMapAny(cr["requestor"])["reference"])]
	ids := identifiersOf(requestor)
	// A PractitionerRole asks on behalf of its organization; the topic filters by organization.
	if requestor["resourceType"] == "PractitionerRole" {
		ids = append(ids, identifiersOf(byURL[str(asMapAny(requestor["organization"])["reference"])])...)
	}
	return ids
}

func joinedIDs(res map[string]any) string {
	return " " + strings.Join(identifiersOf(res), " ") + " "
}

func (s *Server) savePAS(ctx context.Context, req *pasRequest, id string, response map[string]any, pended bool, now time.Time) error {
	reqRaw, err := json.Marshal(req.bundle)
	if err != nil {
		return err
	}
	respRaw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	patient := asMapAny(req.resolve(str(asMapAny(req.claim["patient"])["reference"]))["resource"])
	provider := asMapAny(req.resolve(str(asMapAny(req.claim["provider"])["reference"]))["resource"])
	tx, err := s.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO pas_requests
		(id, trace, member, provider, claim_url, pended, version, created, updated, request) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		id, joinedIDs(req.claim), joinedIDs(patient), joinedIDs(provider), req.claimURL, boolInt(pended),
		now.UnixMilli(), now.UnixMilli(), string(reqRaw)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pas_responses (id, version, response) VALUES (?, 1, ?)`, id, string(respRaw)); err != nil {
		return err
	}
	for _, tracking := range pasTrackingIDs(response) {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pas_tracking (tracking, id) VALUES (?, ?)`, tracking, id); err != nil {
			return err
		}
	}
	for seq, trace := range req.umTraces {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pas_um_traces (trace, id, seq) VALUES (?, ?, ?)`, trace, id, seq); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) handlePASInquire(w http.ResponseWriter, r *http.Request) {
	if s.pasOff(w, r) {
		return
	}
	body, ok := s.pasBody(w, r)
	if !ok {
		return
	}
	req, problems := readPASRequest(body, true)
	if len(problems) > 0 {
		s.refusePAS(w, r, problems)
		return
	}
	patient := asMapAny(req.resolve(str(asMapAny(req.claim["patient"])["reference"]))["resource"])
	provider := asMapAny(req.resolve(str(asMapAny(req.claim["provider"])["reference"]))["resource"])
	found, err := s.findPAS(r.Context(), identifiersOf(req.claim), identifiersOf(patient), identifiersOf(provider))
	if err != nil {
		s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception", err.Error())
		return
	}
	params := []any{}
	for _, b := range found {
		params = append(params, map[string]any{"name": "return", "resource": inquiryResponse(b, s.PAS.now().UTC())})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resourceType": "Parameters", "parameter": params})
}

// findPAS finds the current response to every request from this provider about this member, with one of these trace numbers
// when the inquiry gives any, newest first. The member and provider must both match: an inquiry never reads another
// provider's request.
func (s *Server) findPAS(ctx context.Context, traces, members, providers []string) ([]map[string]any, error) {
	if len(members) == 0 || len(providers) == 0 {
		return nil, nil
	}
	rows, err := s.Store.db.QueryContext(ctx,
		`SELECT r.trace, r.member, r.provider, p.response FROM pas_requests r
		 JOIN pas_responses p ON p.id = r.id AND p.version = r.version ORDER BY r.updated DESC, r.created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	anyIn := func(joined string, ids []string) bool {
		for _, id := range ids {
			if strings.Contains(joined, " "+id+" ") {
				return true
			}
		}
		return false
	}
	var out []map[string]any
	for rows.Next() {
		var trace, member, provider, response string
		if err := rows.Scan(&trace, &member, &provider, &response); err != nil {
			return nil, err
		}
		if !anyIn(member, members) || !anyIn(provider, providers) || (len(traces) > 0 && !anyIn(trace, traces)) {
			continue
		}
		var one map[string]any
		if err := json.Unmarshal([]byte(response), &one); err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	return out, rows.Err()
}

// inquiryResponse is a stored response re-profiled as PAS's inquiry response.
func inquiryResponse(stored map[string]any, now time.Time) map[string]any {
	b := deepCopy(stored)
	b["meta"] = map[string]any{"profile": []any{pasBase + "profile-pas-inquiry-response-bundle|" + pasVersion}}
	b["identifier"] = map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + newUUID()}
	b["timestamp"] = now.Format(time.RFC3339)
	for _, e := range asSliceAny(b["entry"]) {
		res := asMapAny(asMapAny(e)["resource"])
		if res["resourceType"] == "ClaimResponse" {
			res["meta"] = map[string]any{"profile": []any{pasBase + "profile-claiminquiryresponse|" + pasVersion}}
		}
	}
	return b
}

// ErrNotPended means $decide was asked about a request with nothing left to decide.
var ErrNotPended = errors.New("fhirserver: nothing in this prior authorization request is pended")

// handlePASDecide records a reviewer's decision on a pended request and notifies the provider. Parameters: claimResponse (the
// ClaimResponse id, valueString), decision (approve or deny, valueCode), item (valueInteger, repeatable: the items decided;
// none means every pended item), reason (valueString).
func (s *Server) handlePASDecide(w http.ResponseWriter, r *http.Request) {
	if s.pasOff(w, r) {
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var body struct {
		ResourceType string           `json:"resourceType"`
		Parameter    []map[string]any `json:"parameter"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.ResourceType != "Parameters" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body must be a Parameters resource")
		return
	}
	var id, decision, reason, reviewerNPI string
	var quantity float64
	var alternative map[string]any
	items := map[int]bool{}
	for _, p := range body.Parameter {
		switch str(p["name"]) {
		case "claimResponse":
			id = str(p["valueString"])
		case "decision":
			decision = str(p["valueCode"])
		case "reason":
			reason = str(p["valueString"])
		case "reviewer":
			// The reviewer's NPI, which the response carries in claimResponseReviewer.
			reviewerNPI = str(p["valueString"])
		case "quantity":
			// How many units are certified, for decision modify.
			switch {
			case p["valueDecimal"] != nil:
				quantity, _ = p["valueDecimal"].(float64)
			case p["valueInteger"] != nil:
				quantity, _ = p["valueInteger"].(float64)
			case p["valueQuantity"] != nil:
				quantity, _ = asMapAny(p["valueQuantity"])["value"].(float64)
			}
			if quantity <= 0 {
				s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "value", "quantity is a number of units above 0 (valueDecimal)")
				return
			}
		case "alternative":
			// The service approved instead, for decision modify.
			alternative = asMapAny(p["valueCoding"])
			if str(alternative["code"]) == "" {
				s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "value", "alternative is the service approved instead (valueCoding, with a code)")
				return
			}
		case "item":
			n, ok := p["valueInteger"].(float64)
			if !ok || n < 1 {
				s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "value", "item is an item sequence (valueInteger)")
				return
			}
			items[int(n)] = true
		}
	}
	d, err := reviewDecision(PASReview{Decision: decision, Reason: reason, Quantity: quantity, Alternative: alternative})
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "value", err.Error())
		return
	}
	if id == "" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "required", "claimResponse names the request decided")
		return
	}
	out, err := s.decidePAS(r.Context(), id, d, items, reviewerNPI)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found", "no prior authorization request has ClaimResponse "+id)
	case errors.Is(err, ErrNotPended):
		s.writeOutcome(w, r, http.StatusConflict, fhir.SeverityError, "conflict", err.Error())
	case err != nil:
		s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception", err.Error())
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

// decidePAS records a reviewer's decision on the pended items of a request (all of them when items is empty) as a new version
// of its response, and publishes that version on the PAS topic in the same transaction.
func (s *Server) decidePAS(ctx context.Context, id string, d itemDecision, items map[int]bool, reviewerNPI string) (map[string]any, error) {
	if err := s.pasReady(ctx); err != nil {
		return nil, err
	}
	tx, err := s.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var raw, reqRaw string
	if err := tx.QueryRowContext(ctx, `SELECT r.version, p.response, r.request FROM pas_requests r
		JOIN pas_responses p ON p.id = r.id AND p.version = r.version WHERE r.id = ?`, id).Scan(&version, &raw, &reqRaw); err != nil {
		return nil, err
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(reqRaw), &stored); err != nil {
		return nil, err
	}
	req, _ := readPASRequest(stored, false)
	if err := certifiesFewer(d, items, req); err != nil {
		return nil, err
	}
	var bundle map[string]any
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, err
	}
	now := s.PAS.now().UTC()
	changed, stillPended := applyDecision(bundle, d, items, reviewerNPI, now, req)
	if !changed {
		return nil, ErrNotPended
	}
	bundle["timestamp"] = now.Format(time.RFC3339)
	out, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	version++
	if _, err := tx.ExecContext(ctx, `INSERT INTO pas_responses (id, version, response) VALUES (?, ?, ?)`, id, version, string(out)); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pas_requests SET version = ?, pended = ?, updated = ? WHERE id = ?`,
		version, boolInt(stillPended), now.UnixMilli(), id); err != nil {
		return nil, err
	}
	subs := s.Store.Subscriptions()
	if subs != nil {
		if err := subs.Publish(ctx, tx, pasTopicURL, bundle, "Bundle", id, version); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if subs != nil {
		subs.Poke()
	}
	return bundle, nil
}

// applyDecision rewrites the pended items of a response Bundle's ClaimResponse to d.
//
// req is the request as it was submitted, which a modified decision needs: what is certified for fewer units, or approved
// instead, is described from the requested item.
// certifiesFewer refuses a modify that certifies as many units as were asked for or more: that is an approval, or more than the
// provider requested, and recording it as a modification would tell the provider their request was cut when it was not.
func certifiesFewer(d itemDecision, items map[int]bool, req *pasRequest) error {
	if d.answer.AllowedQuantity <= 0 || req == nil {
		return nil
	}
	for _, it := range asSliceAny(req.claim["item"]) {
		item := asMapAny(it)
		seq, _ := item["sequence"].(float64)
		if len(items) > 0 && !items[int(seq)] {
			continue
		}
		asked, _ := asMapAny(item["quantity"])["value"].(float64)
		if asked > 0 && d.answer.AllowedQuantity >= asked {
			return ErrBadReview{fmt.Sprintf("item %v asked for %v units, so certifying %v is not a modification: approve it, or certify fewer",
				seq, asked, d.answer.AllowedQuantity)}
		}
	}
	return nil
}

func applyDecision(bundle map[string]any, d itemDecision, items map[int]bool, reviewerNPI string, now time.Time, req *pasRequest) (changed, stillPended bool) {
	var cr map[string]any
	for _, e := range asSliceAny(bundle["entry"]) {
		if res := asMapAny(asMapAny(e)["resource"]); res["resourceType"] == "ClaimResponse" {
			cr = res
			break
		}
	}
	if cr == nil {
		return false, false
	}
	authNumber := "PA-" + strings.ToUpper(str(cr["id"])[:8])
	if d.number != "" {
		authNumber = d.number
	}
	today := now.Format("2006-01-02")
	until := now.AddDate(0, 0, pasAuthDays).Format("2006-01-02")
	requested := map[string]map[string]any{}
	if req != nil {
		for _, it := range asSliceAny(req.claim["item"]) {
			requested[fmt.Sprint(asMapAny(it)["sequence"])] = asMapAny(it)
		}
	}
	notes := asSliceAny(cr["processNote"])
	for _, it := range asSliceAny(cr["item"]) {
		item := asMapAny(it)
		seq, _ := item["itemSequence"].(float64)
		for _, a := range asSliceAny(item["adjudication"]) {
			for _, e := range asSliceAny(asMapAny(a)["extension"]) {
				ra := asMapAny(e)
				if str(ra["url"]) != pasBase+"extension-reviewAction" {
					continue
				}
				var code map[string]any
				for _, x := range asSliceAny(ra["extension"]) {
					if str(asMapAny(x)["url"]) == pasBase+"extension-reviewActionCode" {
						code = asMapAny(asSliceAny(asMapAny(asMapAny(x)["valueCodeableConcept"])["coding"])[0])
					}
				}
				if code == nil || code["code"] != pasPended.code {
					continue
				}
				if len(items) > 0 && !items[int(seq)] {
					stillPended = true
					continue
				}
				code["code"], code["display"] = d.code, d.display
				changed = true
				why := d.why
				if d.code == pasModified.code {
					asked := requested[fmt.Sprint(seq)]
					if d.answer.AllowedQuantity > 0 && asked != nil {
						detail := []any{map[string]any{"url": "productOrServiceCode", "valueCodeableConcept": asked["productOrService"]},
							map[string]any{"url": "quantity", "valueQuantity": withValue(asMapAny(asked["quantity"]), d.answer.AllowedQuantity)}}
						item["extension"] = append(asSliceAny(item["extension"]),
							map[string]any{"url": pasBase + "extension-itemAuthorizedDetail", "extension": detail})
						why = strings.TrimSpace(why + fmt.Sprintf(" Certified for %v.", d.answer.AllowedQuantity))
					}
					if d.answer.Alternative != nil && asked != nil {
						c := &pasContext{req: req, id: str(cr["id"]), authNumber: authNumber, today: today, until: until}
						cr["addItem"] = append(asSliceAny(cr["addItem"]), c.addedItem(asked, d))
						why = strings.TrimSpace(why + " Approved as the added item instead.")
					}
				}
				if certifies(d) {
					ra["extension"] = append(asSliceAny(ra["extension"]), map[string]any{"url": "number", "valueString": authNumber})
					item["extension"] = append(asSliceAny(item["extension"]), map[string]any{"url": pasBase + "extension-itemPreAuthPeriod",
						"valuePeriod": map[string]any{"start": today, "end": until}})
					cr["preAuthRef"] = authNumber
					cr["preAuthPeriod"] = map[string]any{"start": today, "end": until}
				}
				// The note that said it was pended no longer describes it.
				delete(item, "noteNumber")
				if why != "" {
					n := nextNote(notes)
					notes = append(notes, map[string]any{"number": n, "type": "display", "text": why})
					item["noteNumber"] = []any{n}
				}
			}
		}
	}
	if changed {
		markReviewed(cr, reviewerNPI)
		// What was asked for has been answered by the decision.
		for _, e := range asSliceAny(bundle["entry"]) {
			res := asMapAny(asMapAny(e)["resource"])
			if res["resourceType"] == "Task" {
				res["status"] = "completed"
				res["statusReason"] = map[string]any{"text": "The prior authorization request was decided: " + d.display + "."}
			}
		}
		// A CommunicationRequest is fixed active by its profile; once the request is decided it is no longer asked for.
		var kept []any
		for _, e := range asSliceAny(bundle["entry"]) {
			if asMapAny(asMapAny(e)["resource"])["resourceType"] != "CommunicationRequest" {
				kept = append(kept, e)
			}
		}
		bundle["entry"] = kept
		delete(cr, "communicationRequest")
		if !stillPended {
			for _, a := range asSliceAny(cr["adjudication"]) {
				for _, e := range asSliceAny(asMapAny(a)["extension"]) {
					for _, x := range asSliceAny(asMapAny(e)["extension"]) {
						if xm := asMapAny(x); str(xm["url"]) == pasBase+"extension-reviewActionCode" {
							xm["valueCodeableConcept"] = map[string]any{"coding": []any{map[string]any{"system": x12Action, "code": d.code, "display": d.display}}}
						}
					}
				}
			}
		}
	}
	cited := map[float64]bool{}
	for _, it := range asSliceAny(cr["item"]) {
		for _, n := range asSliceAny(asMapAny(it)["noteNumber"]) {
			switch v := n.(type) {
			case float64:
				cited[v] = true
			case int:
				cited[float64(v)] = true
			}
		}
	}
	var kept []any
	for _, n := range notes {
		switch v := asMapAny(n)["number"].(type) {
		case float64:
			if cited[v] {
				kept = append(kept, n)
			}
		case int:
			if cited[float64(v)] {
				kept = append(kept, n)
			}
		}
	}
	if len(kept) > 0 {
		cr["processNote"] = kept
	} else {
		delete(cr, "processNote")
	}
	return changed, stillPended
}

// nextNote is the number after the highest a ClaimResponse's notes use, so a dropped note's number is never reused.
func nextNote(notes []any) int {
	high := 0
	for _, n := range notes {
		switch v := asMapAny(n)["number"].(type) {
		case float64:
			high = max(high, int(v))
		case int:
			high = max(high, v)
		}
	}
	return high + 1
}

// markReviewed says a person decided: claimResponseReviewer with wasHumanReviewedFlag, and the reviewer's NPI when given.
func markReviewed(cr map[string]any, npi string) {
	parts := []any{map[string]any{"url": "wasHumanReviewedFlag", "valueBoolean": true}}
	if npi != "" {
		parts = append(parts, map[string]any{"url": "reviewerNPI", "valueIdentifier": map[string]any{"system": "http://hl7.org/fhir/sid/us-npi", "value": npi}})
	}
	var ext []any
	for _, e := range asSliceAny(cr["extension"]) {
		if str(asMapAny(e)["url"]) != pasBase+"extension-claimResponseReviewer" {
			ext = append(ext, e)
		}
	}
	cr["extension"] = append(ext, map[string]any{"url": pasBase + "extension-claimResponseReviewer", "extension": parts})
}

// withValue is a quantity with its value replaced and its unit kept.
func withValue(q map[string]any, v float64) map[string]any {
	out := map[string]any{"value": v}
	for _, k := range []string{"unit", "system", "code"} {
		if q[k] != nil {
			out[k] = q[k]
		}
	}
	if out["unit"] == nil && out["code"] == nil {
		out["unit"] = "unit"
	}
	return out
}
