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

// PriorAuthResponse represents a payer's decision on a prior auth request.
type PriorAuthResponse struct {
	RequestID     string
	Decision      string // "approved", "denied", or "pended"
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
func FromFHIR(data map[string]interface{}) (*PriorAuthResponse, error) {
	resp := &PriorAuthResponse{}

	if id, ok := data["id"].(string); ok {
		resp.RequestID = id
	}

	// Extract the item-level reviewAction adjudication code if present.
	// Da Vinci PAS uses item adjudication with category "reviewAction" to carry
	// the actual approve/deny/pend decision when outcome is "complete".
	reviewAction := extractReviewAction(data)

	// Map FHIR outcome to our decision vocabulary.
	if outcome, ok := data["outcome"].(string); ok {
		switch outcome {
		case "complete":
			// outcome=complete means adjudication is finished, but the decision
			// is in the item-level reviewAction. We must check it.
			switch reviewAction {
			case "A1", "A2", "A3", "A6": // approved codes per X12 306
				resp.Decision = "approved"
			case "A4", "A5": // denied codes per X12 306
				resp.Decision = "denied"
			case "": // No reviewAction present — fall back to approved for
				// backward compatibility with simple ClaimResponses that use
				// outcome=complete to mean approved (no item adjudication).
				resp.Decision = "approved"
			default:
				// Unknown review action code — treat as pended to be safe.
				resp.Decision = "pended"
			}
		case "error":
			resp.Decision = "denied"
		case "partial", "queued":
			resp.Decision = "pended"
		default:
			resp.Decision = outcome
		}
	}

	// Extract disposition as reason.
	if disposition, ok := data["disposition"].(string); ok {
		resp.Reason = disposition
	}

	// CMS-0057 requires that a denial include a reason code so the provider
	// can appeal. A denial without a reason is non-compliant.
	if resp.Decision == "denied" && resp.Reason == "" {
		return nil, fmt.Errorf("priorauth: ClaimResponse %q has outcome=denied but no reason/disposition; CMS-0057 requires a denial reason for appeal", resp.RequestID)
	}

	// Parse created date as response date.
	if created, ok := data["created"].(string); ok {
		t, err := time.Parse(time.RFC3339, created)
		if err == nil {
			resp.ResponseDate = t
		}
	}

	// Extract pre-auth period if present.
	if period, ok := data["preAuthPeriod"].(map[string]interface{}); ok {
		if start, ok := period["start"].(string); ok {
			t, err := time.Parse(time.RFC3339, start)
			if err == nil {
				resp.ValidFrom = t
			}
		}
		if end, ok := period["end"].(string); ok {
			t, err := time.Parse(time.RFC3339, end)
			if err == nil {
				resp.ValidTo = t
			}
		}
	}

	// Extract approved units from first item adjudication if present.
	if items, ok := data["item"].([]interface{}); ok && len(items) > 0 {
		if item, ok := items[0].(map[string]interface{}); ok {
			if adjs, ok := item["adjudication"].([]interface{}); ok {
				for _, adj := range adjs {
					a, ok := adj.(map[string]interface{})
					if !ok {
						continue
					}
					if val, ok := a["value"].(float64); ok {
						resp.ApprovedUnits = int(val)
						break
					}
				}
			}
		}
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

// extractReviewAction inspects item-level adjudication entries for a
// "reviewAction" category and returns the X12 306 code if found.
func extractReviewAction(data map[string]interface{}) string {
	items, ok := data["item"].([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	item, ok := items[0].(map[string]interface{})
	if !ok {
		return ""
	}
	adjs, ok := item["adjudication"].([]interface{})
	if !ok {
		return ""
	}
	for _, adj := range adjs {
		a, ok := adj.(map[string]interface{})
		if !ok {
			continue
		}
		cat, ok := a["category"].(map[string]interface{})
		if !ok {
			continue
		}
		codings, ok := cat["coding"].([]interface{})
		if !ok {
			continue
		}
		for _, c := range codings {
			coding, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			code, _ := coding["code"].(string)
			if code == "reviewAction" {
				// The decision code is in the "reason" field of this adjudication.
				reason, ok := a["reason"].(map[string]interface{})
				if !ok {
					return ""
				}
				rCodings, ok := reason["coding"].([]interface{})
				if !ok {
					return ""
				}
				for _, rc := range rCodings {
					rcMap, ok := rc.(map[string]interface{})
					if !ok {
						continue
					}
					if rCode, ok := rcMap["code"].(string); ok {
						return rCode
					}
				}
			}
		}
	}
	return ""
}
