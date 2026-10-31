// Package priorauth implements the provider-side prior authorization workflow
// required by CMS-0057. It submits prior auth requests to payer FHIR APIs,
// tracks responses, and converts between internal types and FHIR resources.
package priorauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PriorAuthRequest represents a prior authorization request submitted by a
// provider to a payer.
type PriorAuthRequest struct {
	ID                 string    `json:"id,omitempty"`
	PatientID          string    `json:"patientId,omitempty"`
	ProviderID         string    `json:"providerId,omitempty"`
	PayerID            string    `json:"payerId,omitempty"`
	ServiceCode        string    `json:"serviceCode,omitempty"`
	ServiceDescription string    `json:"serviceDescription,omitempty"`
	Urgency            string    `json:"urgency,omitempty"` // "standard" or "expedited"
	Diagnosis          []string  `json:"diagnosis,omitempty"`
	RequestDate        time.Time `json:"requestDate,omitempty"`
	SupportingDocs     []string  `json:"supportingDocs,omitempty"`
	// Tenant is the tenant whose channel sent the request, empty on a single-tenant server; Destination is the
	// destination that sent it, and SubmittedAt when.
	Tenant      string    `json:"tenant,omitempty"`
	Destination string    `json:"destination,omitempty"`
	SubmittedAt time.Time `json:"submittedAt,omitempty"`
}

// Decisions a PriorAuthResponse can carry.
const (
	DecisionApproved = "approved" // A1: certified in total
	DecisionPartial  = "partial"  // A2, or some items approved and some denied
	DecisionModified = "modified" // A6: certified, but not as requested
	DecisionDenied   = "denied"   // A3: not certified
	DecisionPended   = "pended"   // A4, or queued: no decision yet
	DecisionCanceled = "canceled" // C: the request was cancelled
	DecisionUnknown  = "unknown"  // no decision code, or one this package does not know
)

// ItemDecision is the payer's decision on one item of a request.
type ItemDecision struct {
	Sequence      int    `json:"sequence,omitempty"`
	Code          string `json:"code,omitempty"` // the X12 306 code as sent
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
	AuthNumber    string `json:"authNumber,omitempty"`
	ApprovedUnits int    `json:"approvedUnits,omitempty"`
}

// PriorAuthResponse represents a payer's decision on a prior auth request.
type PriorAuthResponse struct {
	RequestID     string         `json:"requestId,omitempty"`
	Decision      string         `json:"decision,omitempty"` // one of the Decision constants
	Items         []ItemDecision `json:"items,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	ValidFrom     time.Time      `json:"validFrom,omitempty"`
	ValidTo       time.Time      `json:"validTo,omitempty"`
	ApprovedUnits int            `json:"approvedUnits,omitempty"`
	ResponseDate  time.Time      `json:"responseDate,omitempty"`
	ReviewerID    string         `json:"reviewerId,omitempty"`
	// PreAuthRef is the payer's reference for the request, which a later inquiry or update names.
	PreAuthRef string `json:"preAuthRef,omitempty"`
}

// PriorAuthStats holds aggregate statistics about prior auth activity.
type PriorAuthStats struct {
	TotalRequests    int
	Approved         int
	Denied           int
	Pended           int
	MeanResponseTime time.Duration
	ExpeditedCount   int
}

// Submitting a Da Vinci PAS request.
//
// What is sent is the provider's PAS request Bundle as it was built (by the EHR, by DTR, by a channel that assembled it),
// not a Claim rebuilt here: the payer judges the Bundle against the PAS profiles, and a Claim reconstructed from a summary
// would lose the supporting resources the decision rests on. An earlier version of this package did rebuild one, as a bare
// Claim with references to resources the payer had never seen, and nothing used it.

// MaxResponse bounds what a payer's answer may be.
const MaxResponse = 16 << 20

// Summarise reads what a PAS request Bundle asks for, for the tracker and for the screen that lists what was sent.
func Summarise(bundle map[string]any) (PriorAuthRequest, error) {
	var req PriorAuthRequest
	if bundle["resourceType"] != "Bundle" {
		return req, errors.New("priorauth: a PAS request is a Bundle")
	}
	byRef := map[string]map[string]any{}
	var claim map[string]any
	for _, e := range slice(bundle["entry"]) {
		em, _ := e.(map[string]any)
		r, _ := em["resource"].(map[string]any)
		if r == nil {
			continue
		}
		rt, _ := r["resourceType"].(string)
		id, _ := r["id"].(string)
		if full, _ := em["fullUrl"].(string); full != "" {
			byRef[full] = r
		}
		if id != "" {
			byRef[rt+"/"+id] = r
		}
		if rt == "Claim" && claim == nil {
			claim = r
		}
	}
	if claim == nil {
		return req, errors.New("priorauth: the Bundle holds no Claim")
	}
	if use, _ := claim["use"].(string); use != "preauthorization" {
		return req, fmt.Errorf("priorauth: the Claim's use is %q, and a prior authorization request is preauthorization", use)
	}
	req.ID, _ = claim["id"].(string)
	if ids := slice(claim["identifier"]); len(ids) > 0 {
		if v, _ := ids[0].(map[string]any)["value"].(string); v != "" {
			req.ID = v
		}
	}
	ref := func(field string) map[string]any {
		r, _ := claim[field].(map[string]any)
		target, _ := r["reference"].(string)
		return byRef[target]
	}
	if p := ref("patient"); p != nil {
		if ids := slice(p["identifier"]); len(ids) > 0 {
			req.PatientID, _ = ids[0].(map[string]any)["value"].(string)
		}
		if req.PatientID == "" {
			req.PatientID, _ = p["id"].(string)
		}
	}
	nameOf := func(r map[string]any) string {
		if r == nil {
			return ""
		}
		if n, _ := r["name"].(string); n != "" {
			return n
		}
		if ids := slice(r["identifier"]); len(ids) > 0 {
			v, _ := ids[0].(map[string]any)["value"].(string)
			return v
		}
		id, _ := r["id"].(string)
		return id
	}
	req.ProviderID = nameOf(ref("provider"))
	req.PayerID = nameOf(ref("insurer"))
	if items := slice(claim["item"]); len(items) > 0 {
		item, _ := items[0].(map[string]any)
		pos, _ := item["productOrService"].(map[string]any)
		if codings := slice(pos["coding"]); len(codings) > 0 {
			c, _ := codings[0].(map[string]any)
			req.ServiceCode, _ = c["code"].(string)
			req.ServiceDescription, _ = c["display"].(string)
		}
	}
	if pr, _ := claim["priority"].(map[string]any); pr != nil {
		if codings := slice(pr["coding"]); len(codings) > 0 {
			code, _ := codings[0].(map[string]any)["code"].(string)
			req.Urgency = map[bool]string{true: "expedited", false: "standard"}[code == "stat" || code == "urgent"]
		}
	}
	if created, _ := claim["created"].(string); created != "" {
		req.RequestDate = parseFHIRTime(created)
	}
	for _, d := range slice(claim["diagnosis"]) {
		dm, _ := d.(map[string]any)
		cc, _ := dm["diagnosisCodeableConcept"].(map[string]any)
		if codings := slice(cc["coding"]); len(codings) > 0 {
			if code, _ := codings[0].(map[string]any)["code"].(string); code != "" {
				req.Diagnosis = append(req.Diagnosis, code)
			}
		}
	}
	return req, nil
}

func slice(v any) []any { s, _ := v.([]any); return s }

// ClaimResponseOf finds the ClaimResponse in a payer's answer: a PAS response Bundle, or a bare ClaimResponse.
func ClaimResponseOf(answer map[string]any) (map[string]any, error) {
	switch answer["resourceType"] {
	case "ClaimResponse":
		return answer, nil
	case "Bundle":
		for _, e := range slice(answer["entry"]) {
			em, _ := e.(map[string]any)
			if r, _ := em["resource"].(map[string]any); r != nil && r["resourceType"] == "ClaimResponse" {
				return r, nil
			}
		}
		return nil, errors.New("priorauth: the payer's Bundle holds no ClaimResponse")
	case "OperationOutcome":
		return nil, fmt.Errorf("priorauth: the payer refused the request: %s", outcomeText(answer))
	}
	return nil, fmt.Errorf("priorauth: the payer answered a %v, not a ClaimResponse", answer["resourceType"])
}

func outcomeText(oo map[string]any) string {
	var parts []string
	for _, i := range slice(oo["issue"]) {
		im, _ := i.(map[string]any)
		if d, _ := im["diagnostics"].(string); d != "" {
			parts = append(parts, d)
		} else if det, _ := im["details"].(map[string]any); det != nil {
			if t, _ := det["text"].(string); t != "" {
				parts = append(parts, t)
			}
		}
	}
	if len(parts) == 0 {
		return "no reason given"
	}
	return strings.Join(parts, "; ")
}

// Submit posts a PAS request Bundle to a payer's Claim/$submit and reads the decision.
//
// authorize adds the credential the payer wants to the request (its body is passed for signing schemes); nil sends none.
// The payer's whole answer is returned with the decision, so it can be kept, inspected or passed on. A non-2xx answer,
// an OperationOutcome, or a ClaimResponse whose outcome is error is an error: the payer did not decide.
func Submit(ctx context.Context, client *http.Client, payer string, bundle []byte,
	authorize func(*http.Request, []byte) error) ([]byte, *PriorAuthResponse, error) {
	target := strings.TrimRight(payer, "/") + "/Claim/$submit"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(bundle))
	if err != nil {
		return nil, nil, fmt.Errorf("priorauth: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/fhir+json")
	httpReq.Header.Set("Accept", "application/fhir+json")
	if authorize != nil {
		if err := authorize(httpReq, bundle); err != nil {
			return nil, nil, fmt.Errorf("priorauth: authorizing with the payer: %w", err)
		}
	}
	res, err := client.Do(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("priorauth: submitting to %s: %w", target, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxResponse))
	if err != nil {
		return nil, nil, fmt.Errorf("priorauth: reading the payer's answer: %w", err)
	}
	var answer map[string]any
	jsonErr := json.Unmarshal(raw, &answer)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		if jsonErr == nil && answer["resourceType"] == "OperationOutcome" {
			return raw, nil, fmt.Errorf("priorauth: the payer answered %d: %s", res.StatusCode, outcomeText(answer))
		}
		return raw, nil, fmt.Errorf("priorauth: the payer answered %d: %.200s", res.StatusCode, raw)
	}
	if jsonErr != nil {
		return raw, nil, fmt.Errorf("priorauth: the payer's answer is not FHIR JSON: %w", jsonErr)
	}
	cr, err := ClaimResponseOf(answer)
	if err != nil {
		return raw, nil, err
	}
	decision, err := FromFHIR(cr)
	return raw, decision, err
}

// PriorAuthTracker tracks prior auth requests and their statuses.
//
// A zero tracker is held in memory only. One from OpenTracker is kept in a file as well, so pending and expiring
// authorisations survive a restart: every change is written to a temporary file and renamed over the old one, so a crash
// mid-write leaves the previous state rather than half a file. The file names patients and services, so it is written
// readable by this user only (0600) and belongs wherever the site keeps other patient data at rest.
type PriorAuthTracker struct {
	mu      sync.Mutex
	entries []trackerEntry
	path    string
}

type trackerEntry struct {
	req  PriorAuthRequest
	resp *PriorAuthResponse
}

// savedEntry is a tracker entry as stored in the file.
type savedEntry struct {
	Request  PriorAuthRequest   `json:"request"`
	Response *PriorAuthResponse `json:"response,omitempty"`
}

// OpenTracker returns a tracker kept in the file at path, reading what is already there. A missing file is an empty
// tracker; one that cannot be read is an error, rather than starting empty and overwriting what it held at the next change.
func OpenTracker(path string) (*PriorAuthTracker, error) {
	t := &PriorAuthTracker{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("priorauth: reading the tracker: %w", err)
	}
	var saved []savedEntry
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, fmt.Errorf("priorauth: the tracker file %s is not readable (%v); move it aside to start empty", path, err)
	}
	for _, e := range saved {
		t.entries = append(t.entries, trackerEntry{req: e.Request, resp: e.Response})
	}
	return t, nil
}

// save writes the entries to the tracker's file, if it has one. Called with t.mu held.
func (t *PriorAuthTracker) save() error {
	if t.path == "" {
		return nil
	}
	saved := make([]savedEntry, 0, len(t.entries))
	for _, e := range t.entries {
		saved = append(saved, savedEntry{Request: e.req, Response: e.resp})
	}
	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.path), ".priorauth-*.tmp")
	if err != nil {
		return fmt.Errorf("priorauth: saving the tracker: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), t.path)
}

// Track records a prior auth request and its response (nil while there is none). A request already tracked, by ID, is
// updated rather than added again, so a decision that arrives later replaces the pending entry instead of sitting beside
// it. The error is from saving; the entry is recorded in memory either way.
func (t *PriorAuthTracker) Track(req PriorAuthRequest, resp *PriorAuthResponse) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.entries {
		if req.ID != "" && t.entries[i].req.ID == req.ID {
			t.entries[i] = trackerEntry{req: req, resp: resp}
			return t.save()
		}
	}
	t.entries = append(t.entries, trackerEntry{req: req, resp: resp})
	// Bounded: the oldest decided requests go first once there are more than maxTracked, and a pending one is never
	// dropped, because it is the one somebody is waiting on.
	for len(t.entries) > maxTracked {
		dropped := false
		for i, e := range t.entries {
			if e.resp != nil && e.resp.Decision != DecisionPended {
				t.entries = append(t.entries[:i], t.entries[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			break
		}
	}
	return t.save()
}

// maxTracked is how many requests the tracker keeps.
const maxTracked = 10000

// Tracked is one request and the payer's latest answer to it, nil when there is none yet.
type Tracked struct {
	Request  PriorAuthRequest   `json:"request"`
	Response *PriorAuthResponse `json:"response,omitempty"`
}

// List returns what one tenant's channels submitted ("" on a single-tenant server), newest first, at most limit
// (zero for all).
func (t *PriorAuthTracker) List(tenant string, limit int) []Tracked {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Tracked, 0, len(t.entries))
	for i := len(t.entries) - 1; i >= 0; i-- {
		if t.entries[i].req.Tenant != tenant {
			continue
		}
		out = append(out, Tracked{Request: t.entries[i].req, Response: t.entries[i].resp})
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// Pending returns all requests that have no decision yet (response is nil or
// decision is "pended").
func (t *PriorAuthTracker) Pending() []PriorAuthRequest {
	t.mu.Lock()
	defer t.mu.Unlock()

	var pending []PriorAuthRequest
	for _, e := range t.entries {
		if e.resp == nil || e.resp.Decision == DecisionPended {
			pending = append(pending, e.req)
		}
	}
	return pending
}

// Expiring returns the certified authorizations (approved, partly approved or modified) whose ValidTo falls within
// the given duration from now.
func (t *PriorAuthTracker) Expiring(within time.Duration) []PriorAuthResponse {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	deadline := now.Add(within)

	var expiring []PriorAuthResponse
	for _, e := range t.entries {
		if e.resp == nil {
			continue
		}
		certified := e.resp.Decision == DecisionApproved || e.resp.Decision == DecisionPartial ||
			e.resp.Decision == DecisionModified
		if certified && !e.resp.ValidTo.IsZero() &&
			e.resp.ValidTo.After(now) && e.resp.ValidTo.Before(deadline) {
			expiring = append(expiring, *e.resp)
		}
	}
	return expiring
}

// Stats returns aggregate statistics about tracked prior auth activity.
func (t *PriorAuthTracker) Stats() PriorAuthStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	var stats PriorAuthStats
	var totalResponseTime time.Duration
	var responseCount int

	for _, e := range t.entries {
		stats.TotalRequests++
		if e.req.Urgency == "expedited" {
			stats.ExpeditedCount++
		}
		if e.resp != nil {
			switch e.resp.Decision {
			case "approved":
				stats.Approved++
			case "denied":
				stats.Denied++
			case "pended":
				stats.Pended++
			}
			if !e.resp.ResponseDate.IsZero() && !e.req.RequestDate.IsZero() {
				totalResponseTime += e.resp.ResponseDate.Sub(e.req.RequestDate)
				responseCount++
			}
		}
	}
	if responseCount > 0 {
		stats.MeanResponseTime = totalResponseTime / time.Duration(responseCount)
	}
	return stats
}

// FromFHIR parses a FHIR ClaimResponse resource into a PriorAuthResponse.
//
// The decision is read item by item from each item's PAS reviewAction extension (X12 306 codes: A1 certified, A2 certified in
// part, A3 not certified, A4 pended, A6 modified). The older form, a "reviewAction" adjudication whose reason carries the code, is
// read too. Nothing is assumed: a response with no decision code, an outcome this package does not know, or a code it does not
// know comes back as DecisionUnknown, so a provider is never told "approved" for care the payer did not approve. A ClaimResponse
// with outcome "error" means the payer could not process the request, not that it denied it, and is returned as an error.
func FromFHIR(data map[string]interface{}) (*PriorAuthResponse, error) {
	resp := &PriorAuthResponse{}

	if id, ok := data["id"].(string); ok {
		resp.RequestID = id
	}
	if disposition, ok := data["disposition"].(string); ok {
		resp.Reason = disposition
	}
	resp.PreAuthRef, _ = data["preAuthRef"].(string)

	outcome, _ := data["outcome"].(string)
	if outcome == "error" {
		why := resp.Reason
		if why == "" {
			why = "no reason given"
		}
		return nil, fmt.Errorf("priorauth: the payer could not process request %q (outcome=error): %s", resp.RequestID, why)
	}

	if items, ok := data["item"].([]interface{}); ok {
		for _, it := range items {
			item, ok := it.(map[string]interface{})
			if !ok {
				continue
			}
			resp.Items = append(resp.Items, itemDecision(item))
		}
	}

	switch outcome {
	case "queued":
		resp.Decision = DecisionPended
	case "complete", "partial":
		// "partial" is what PAS sends when some items are still pended, so the item codes decide in both cases.
		resp.Decision = overallDecision(resp.Items)
	default:
		resp.Decision = DecisionUnknown
	}

	// CMS-0057 requires that a denial include a reason code so the provider
	// can appeal. A denial without a reason is non-compliant.
	if resp.Decision == DecisionDenied && resp.Reason == "" {
		for _, it := range resp.Items {
			if it.Reason != "" {
				resp.Reason = it.Reason
				break
			}
		}
		if resp.Reason == "" {
			return nil, fmt.Errorf("priorauth: ClaimResponse %q is a denial with no reason; CMS-0057 requires a denial reason for appeal", resp.RequestID)
		}
	}

	if created, ok := data["created"].(string); ok {
		resp.ResponseDate = parseFHIRTime(created)
	}
	if period, ok := data["preAuthPeriod"].(map[string]interface{}); ok {
		if start, ok := period["start"].(string); ok {
			resp.ValidFrom = parseFHIRTime(start)
		}
		if end, ok := period["end"].(string); ok {
			resp.ValidTo = parseFHIRTime(end)
		}
	}

	// ApprovedUnits is kept for a single-item request, where it is unambiguous; a request with several items has each item's
	// own count in Items.
	if len(resp.Items) == 1 {
		resp.ApprovedUnits = resp.Items[0].ApprovedUnits
	}

	// Extract reviewer from extension if present.
	if exts, ok := data["extension"].([]interface{}); ok {
		for _, ext := range exts {
			e, ok := ext.(map[string]interface{})
			if !ok {
				continue
			}
			if ref, ok := e["valueReference"].(map[string]interface{}); ok {
				if reviewer, ok := ref["reference"].(string); ok {
					resp.ReviewerID = reviewer
					break
				}
			}
		}
	}

	return resp, nil
}

// itemDecision reads one ClaimResponse item.
func itemDecision(item map[string]interface{}) ItemDecision {
	d := ItemDecision{}
	if seq, ok := item["itemSequence"].(float64); ok {
		d.Sequence = int(seq)
	}
	code := ""
	for _, ext := range asList(item["extension"]) {
		e, _ := ext.(map[string]interface{})
		if strings.HasSuffix(str(e["url"]), "extension-itemAuthorizedDetail") {
			for _, sub := range asList(e["extension"]) {
				se, _ := sub.(map[string]interface{})
				if str(se["url"]) == "quantity" {
					if q, ok := se["valueQuantity"].(map[string]interface{}); ok {
						if v, ok := q["value"].(float64); ok {
							d.ApprovedUnits = int(v)
						}
					}
				}
			}
		}
	}
	for _, adj := range asList(item["adjudication"]) {
		a, _ := adj.(map[string]interface{})
		// PAS 2.x: the code is in the reviewAction extension on the adjudication.
		for _, ext := range asList(a["extension"]) {
			e, _ := ext.(map[string]interface{})
			if !strings.HasSuffix(str(e["url"]), "extension-reviewAction") {
				continue
			}
			for _, sub := range asList(e["extension"]) {
				se, _ := sub.(map[string]interface{})
				switch {
				case strings.HasSuffix(str(se["url"]), "extension-reviewActionCode") || str(se["url"]) == "reviewActionCode":
					if c := firstCode(se["valueCodeableConcept"]); c != "" {
						code = c
					}
				case str(se["url"]) == "number":
					d.AuthNumber = str(se["valueString"])
				case strings.HasSuffix(str(se["url"]), "extension-reviewActionReason") || str(se["url"]) == "reasonCode":
					if c := firstCode(se["valueCodeableConcept"]); c != "" {
						d.Reason = c
					}
				}
			}
		}
		// The older form: an adjudication of category reviewAction whose reason holds the code.
		if code == "" && firstCode(a["category"]) == "reviewAction" {
			code = firstCode(a["reason"])
		}
	}
	d.Code = code
	d.Decision = decisionForCode(code)
	return d
}

// decisionForCode maps an X12 306 review action code. Anything else, including no code, is DecisionUnknown.
func decisionForCode(code string) string {
	switch code {
	case "A1":
		return DecisionApproved
	case "A2":
		return DecisionPartial
	case "A3":
		return DecisionDenied
	case "A4":
		return DecisionPended
	case "A6":
		return DecisionModified
	case "C":
		return DecisionCanceled
	default:
		return DecisionUnknown
	}
}

// overallDecision summarises the items. No items, or any item undecided, is unknown; any pended item makes the request pended;
// every item alike is that decision; a mix of approvals and denials is partial.
func overallDecision(items []ItemDecision) string {
	if len(items) == 0 {
		return DecisionUnknown
	}
	all := items[0].Decision
	pended := false
	for _, it := range items {
		if it.Decision == DecisionUnknown {
			return DecisionUnknown
		}
		if it.Decision == DecisionPended {
			pended = true
		}
		if it.Decision != all {
			all = ""
		}
	}
	if all != "" {
		return all
	}
	if pended {
		return DecisionPended
	}
	return DecisionPartial
}

func parseFHIRTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func asList(v interface{}) []interface{} { l, _ := v.([]interface{}); return l }

func str(v interface{}) string { s, _ := v.(string); return s }

// firstCode returns the first coding's code of a CodeableConcept.
func firstCode(v interface{}) string {
	cc, _ := v.(map[string]interface{})
	for _, c := range asList(cc["coding"]) {
		if m, ok := c.(map[string]interface{}); ok && str(m["code"]) != "" {
			return str(m["code"])
		}
	}
	return ""
}
