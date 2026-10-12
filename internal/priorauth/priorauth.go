// Package priorauth implements the provider-side prior authorization workflow
// required by CMS-0057. It submits prior auth requests to payer FHIR APIs,
// tracks responses, and converts between internal types and FHIR resources.
package priorauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PriorAuthRequest represents a prior authorization request submitted by a
// provider to a payer.
type PriorAuthRequest struct {
	ID                 string
	PatientID          string
	ProviderID         string
	PayerID            string
	ServiceCode        string
	ServiceDescription string
	Urgency            string // "standard" or "expedited"
	Diagnosis          []string
	RequestDate        time.Time
	SupportingDocs     []string
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
	Sequence      int
	Code          string // the X12 306 code as sent
	Decision      string
	Reason        string
	AuthNumber    string
	ApprovedUnits int
}

// PriorAuthResponse represents a payer's decision on a prior auth request.
type PriorAuthResponse struct {
	RequestID     string
	Decision      string // one of the Decision constants
	Items         []ItemDecision
	Reason        string
	ValidFrom     time.Time
	ValidTo       time.Time
	ApprovedUnits int
	ResponseDate  time.Time
	ReviewerID    string
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

// ClientConfig holds configuration for connecting to a payer FHIR API.
type ClientConfig struct {
	PayerEndpoint string
	BearerToken   string
	Timeout       time.Duration
}

// PriorAuthClient submits prior authorization requests to payer FHIR APIs.
type PriorAuthClient struct {
	Config ClientConfig
	HTTP   *http.Client
}

// NewClient creates a PriorAuthClient with the given configuration.
func NewClient(cfg ClientConfig) *PriorAuthClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &PriorAuthClient{
		Config: cfg,
		HTTP:   &http.Client{Timeout: timeout},
	}
}

// Submit sends a prior authorization request to the payer's FHIR endpoint and
// returns the payer's response.
func (c *PriorAuthClient) Submit(ctx context.Context, req PriorAuthRequest) (*PriorAuthResponse, error) {
	fhir := ToFHIR(req)
	body, err := json.Marshal(fhir)
	if err != nil {
		return nil, fmt.Errorf("priorauth: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Config.PayerEndpoint+"/Claim/$submit", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("priorauth: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/fhir+json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Config.BearerToken)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("priorauth: submit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("priorauth: payer returned %d: %s", resp.StatusCode, string(b))
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("priorauth: decode response: %w", err)
	}

	return FromFHIR(data)
}

// Check queries the status of a previously submitted prior auth request.
func (c *PriorAuthClient) Check(ctx context.Context, requestID string) (*PriorAuthResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Config.PayerEndpoint+"/Claim/"+requestID, nil)
	if err != nil {
		return nil, fmt.Errorf("priorauth: create check request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/fhir+json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Config.BearerToken)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("priorauth: check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("priorauth: payer returned %d: %s", resp.StatusCode, string(b))
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("priorauth: decode check response: %w", err)
	}

	return FromFHIR(data)
}

// Cancel requests cancellation of a previously submitted prior auth request.
func (c *PriorAuthClient) Cancel(ctx context.Context, requestID string) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.Config.PayerEndpoint+"/Claim/"+requestID, nil)
	if err != nil {
		return fmt.Errorf("priorauth: create cancel request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Config.BearerToken)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("priorauth: cancel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("priorauth: cancel returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// PriorAuthTracker tracks all prior auth requests and their statuses.
//
// It is held in memory and is lost when the process ends. Nothing in Perfuse uses it today; a caller that needs pending and
// expiring authorisations to survive a restart must store them itself.
type PriorAuthTracker struct {
	mu      sync.Mutex
	entries []trackerEntry
}

type trackerEntry struct {
	req  PriorAuthRequest
	resp *PriorAuthResponse
}

// Track records a prior auth request and its response (which may be nil if
// still pending).
func (t *PriorAuthTracker) Track(req PriorAuthRequest, resp *PriorAuthResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = append(t.entries, trackerEntry{req: req, resp: resp})
}

// Pending returns all requests that have no decision yet (response is nil or
// decision is "pended").
func (t *PriorAuthTracker) Pending() []PriorAuthRequest {
	t.mu.Lock()
	defer t.mu.Unlock()

	var pending []PriorAuthRequest
	for _, e := range t.entries {
		if e.resp == nil || e.resp.Decision == "pended" {
			pending = append(pending, e.req)
		}
	}
	return pending
}

// Expiring returns all approved authorizations whose ValidTo falls within the
// given duration from now.
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
		if e.resp.Decision == "approved" && !e.resp.ValidTo.IsZero() &&
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

// ToFHIR converts a PriorAuthRequest to a FHIR Claim resource with
// use=preauthorization.
func ToFHIR(req PriorAuthRequest) map[string]interface{} {
	diagnoses := make([]map[string]interface{}, 0, len(req.Diagnosis))
	for i, code := range req.Diagnosis {
		diagnoses = append(diagnoses, map[string]interface{}{
			"sequence": i + 1,
			"diagnosisCodeableConcept": map[string]interface{}{
				"coding": []map[string]interface{}{
					{
						"system": "http://hl7.org/fhir/sid/icd-10-cm",
						"code":   code,
					},
				},
			},
		})
	}

	supportingInfo := make([]map[string]interface{}, 0, len(req.SupportingDocs))
	for i, doc := range req.SupportingDocs {
		supportingInfo = append(supportingInfo, map[string]interface{}{
			"sequence": i + 1,
			"valueReference": map[string]interface{}{
				"reference": doc,
			},
		})
	}

	items := []map[string]interface{}{
		{
			"sequence": 1,
			"productOrService": map[string]interface{}{
				"coding": []map[string]interface{}{
					{
						"system": "http://www.ama-assn.org/go/cpt",
						"code":   req.ServiceCode,
					},
				},
				"text": req.ServiceDescription,
			},
		},
	}

	claim := map[string]interface{}{
		"resourceType": "Claim",
		"id":           req.ID,
		"status":       "active",
		"use":          "preauthorization",
		"patient": map[string]interface{}{
			"reference": "Patient/" + req.PatientID,
		},
		"provider": map[string]interface{}{
			"reference": "Practitioner/" + req.ProviderID,
		},
		"insurer": map[string]interface{}{
			"reference": "Organization/" + req.PayerID,
		},
		"priority": map[string]interface{}{
			"coding": []map[string]interface{}{
				{
					"system": "http://terminology.hl7.org/CodeSystem/processpriority",
					"code":   req.Urgency,
				},
			},
		},
		"created":        req.RequestDate.Format(time.RFC3339),
		"diagnosis":      diagnoses,
		"supportingInfo": supportingInfo,
		"item":           items,
	}

	return claim
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
