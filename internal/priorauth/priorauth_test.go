package priorauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToFHIR_ResourceType(t *testing.T) {
	req := PriorAuthRequest{
		ID:                 "pa-001",
		PatientID:          "patient-123",
		ProviderID:         "provider-456",
		PayerID:            "payer-789",
		ServiceCode:        "27447",
		ServiceDescription: "Total knee replacement",
		Urgency:            "standard",
		Diagnosis:          []string{"M17.11", "M17.12"},
		RequestDate:        time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		SupportingDocs:     []string{"DocumentReference/doc-1"},
	}

	fhir := ToFHIR(req)

	if fhir["resourceType"] != "Claim" {
		t.Errorf("resourceType = %v, want Claim", fhir["resourceType"])
	}
	if fhir["use"] != "preauthorization" {
		t.Errorf("use = %v, want preauthorization", fhir["use"])
	}
	if fhir["status"] != "active" {
		t.Errorf("status = %v, want active", fhir["status"])
	}
	if fhir["id"] != "pa-001" {
		t.Errorf("id = %v, want pa-001", fhir["id"])
	}
}

func TestToFHIR_Structure(t *testing.T) {
	req := PriorAuthRequest{
		ID:                 "pa-002",
		PatientID:          "patient-abc",
		ProviderID:         "provider-def",
		PayerID:            "payer-ghi",
		ServiceCode:        "99213",
		ServiceDescription: "Office visit",
		Urgency:            "expedited",
		Diagnosis:          []string{"J06.9"},
		RequestDate:        time.Date(2026, 7, 15, 14, 30, 0, 0, time.UTC),
		SupportingDocs:     []string{"DocumentReference/doc-2", "DocumentReference/doc-3"},
	}

	fhir := ToFHIR(req)

	patient, ok := fhir["patient"].(map[string]interface{})
	if !ok {
		t.Fatal("patient field missing or wrong type")
	}
	if patient["reference"] != "Patient/patient-abc" {
		t.Errorf("patient reference = %v, want Patient/patient-abc", patient["reference"])
	}

	provider, ok := fhir["provider"].(map[string]interface{})
	if !ok {
		t.Fatal("provider field missing or wrong type")
	}
	if provider["reference"] != "Practitioner/provider-def" {
		t.Errorf("provider reference = %v, want Practitioner/provider-def", provider["reference"])
	}

	insurer, ok := fhir["insurer"].(map[string]interface{})
	if !ok {
		t.Fatal("insurer field missing or wrong type")
	}
	if insurer["reference"] != "Organization/payer-ghi" {
		t.Errorf("insurer reference = %v, want Organization/payer-ghi", insurer["reference"])
	}

	diagnoses, ok := fhir["diagnosis"].([]map[string]interface{})
	if !ok {
		t.Fatal("diagnosis field missing or wrong type")
	}
	if len(diagnoses) != 1 {
		t.Errorf("diagnosis count = %d, want 1", len(diagnoses))
	}

	supportingInfo, ok := fhir["supportingInfo"].([]map[string]interface{})
	if !ok {
		t.Fatal("supportingInfo field missing or wrong type")
	}
	if len(supportingInfo) != 2 {
		t.Errorf("supportingInfo count = %d, want 2", len(supportingInfo))
	}
}

func TestFromFHIR_ParsesClaimResponse(t *testing.T) {
	data := map[string]interface{}{
		"resourceType": "ClaimResponse",
		"id":           "resp-001",
		"outcome":      "complete",
		"disposition":  "Approved based on clinical necessity",
		"created":      "2026-08-03T10:00:00Z",
		"preAuthPeriod": map[string]interface{}{
			"start": "2026-08-05T00:00:00Z",
			"end":   "2026-11-05T00:00:00Z",
		},
		"item": []interface{}{paItem(1, "A1", 12)},
		"extension": []interface{}{
			map[string]interface{}{
				"url": "http://example.org/fhir/reviewer",
				"valueReference": map[string]interface{}{
					"reference": "Practitioner/reviewer-100",
				},
			},
		},
	}

	resp, err := FromFHIR(data)
	if err != nil {
		t.Fatalf("FromFHIR error: %v", err)
	}

	if resp.RequestID != "resp-001" {
		t.Errorf("RequestID = %v, want resp-001", resp.RequestID)
	}
	if resp.Decision != "approved" {
		t.Errorf("Decision = %v, want approved", resp.Decision)
	}
	if resp.Reason != "Approved based on clinical necessity" {
		t.Errorf("Reason = %v, want 'Approved based on clinical necessity'", resp.Reason)
	}
	if resp.ApprovedUnits != 12 {
		t.Errorf("ApprovedUnits = %d, want 12", resp.ApprovedUnits)
	}
	if resp.ReviewerID != "Practitioner/reviewer-100" {
		t.Errorf("ReviewerID = %v, want Practitioner/reviewer-100", resp.ReviewerID)
	}

	expectedFrom := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	if !resp.ValidFrom.Equal(expectedFrom) {
		t.Errorf("ValidFrom = %v, want %v", resp.ValidFrom, expectedFrom)
	}

	expectedTo := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	if !resp.ValidTo.Equal(expectedTo) {
		t.Errorf("ValidTo = %v, want %v", resp.ValidTo, expectedTo)
	}
}

func TestFromFHIR_DeniedOutcome(t *testing.T) {
	data := map[string]interface{}{
		"id":          "resp-002",
		"outcome":     "complete",
		"disposition": "Not medically necessary",
		"created":     "2026-08-04T12:00:00Z",
		"item":        []interface{}{paItem(1, "A3", 0)},
	}
	resp, err := FromFHIR(data)
	if err != nil {
		t.Fatalf("FromFHIR error: %v", err)
	}
	if resp.Decision != DecisionDenied {
		t.Errorf("Decision = %v, want denied (A3 is not certified)", resp.Decision)
	}
}

// outcome=error is the payer failing to process the request. It is not a denial.
func TestFromFHIR_ErrorIsNotADecision(t *testing.T) {
	_, err := FromFHIR(map[string]interface{}{"id": "resp-err", "outcome": "error", "disposition": "Member not found"})
	if err == nil || !strings.Contains(err.Error(), "could not process") {
		t.Fatalf("err = %v, want the payer-could-not-process error", err)
	}
}

// A4 is pended. It was once read as a denial.
func TestFromFHIR_A4IsPended(t *testing.T) {
	resp, err := FromFHIR(map[string]interface{}{"id": "r", "outcome": "complete", "item": []interface{}{paItem(1, "A4", 0)}})
	if err != nil || resp.Decision != DecisionPended {
		t.Fatalf("Decision = %v, err = %v; want pended", resp, err)
	}
}

// No decision code is not an approval.
func TestFromFHIR_CompleteWithoutACodeIsUnknown(t *testing.T) {
	resp, err := FromFHIR(map[string]interface{}{"id": "r", "outcome": "complete"})
	if err != nil || resp.Decision != DecisionUnknown {
		t.Fatalf("got %+v, %v; want unknown", resp, err)
	}
}

func TestFromFHIR_UnknownOutcomeOrCodeIsUnknown(t *testing.T) {
	for _, data := range []map[string]interface{}{
		{"id": "r", "outcome": "approved-ish"},
		{"id": "r", "outcome": "complete", "item": []interface{}{paItem(1, "ZZ", 0)}},
	} {
		resp, err := FromFHIR(data)
		if err != nil || resp.Decision != DecisionUnknown {
			t.Errorf("%v: got %+v, %v; want unknown", data, resp, err)
		}
	}
}

// Every item is read, and the request's decision summarises them.
func TestFromFHIR_EveryItemIsRead(t *testing.T) {
	resp, err := FromFHIR(map[string]interface{}{
		"id": "r", "outcome": "complete", "disposition": "see items",
		"item": []interface{}{paItem(1, "A1", 10), paItem(2, "A3", 0), paItem(3, "A6", 4)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != DecisionPartial || len(resp.Items) != 3 {
		t.Fatalf("Decision = %v with %d items, want partial with 3", resp.Decision, len(resp.Items))
	}
	if resp.Items[1].Decision != DecisionDenied || resp.Items[2].Decision != DecisionModified || resp.Items[2].ApprovedUnits != 4 {
		t.Errorf("items = %+v", resp.Items)
	}
	if resp.ApprovedUnits != 0 {
		t.Errorf("ApprovedUnits = %d for a three-item request; it is per item", resp.ApprovedUnits)
	}
}

// paItem is a ClaimResponse item in the PAS 2.x form Perfuse's own PAS server writes.
func paItem(seq int, code string, qty float64) map[string]interface{} {
	item := map[string]interface{}{
		"itemSequence": float64(seq),
		"adjudication": []interface{}{map[string]interface{}{
			"category": map[string]interface{}{"coding": []interface{}{map[string]interface{}{"code": "submitted"}}},
			"extension": []interface{}{map[string]interface{}{
				"url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction",
				"extension": []interface{}{
					map[string]interface{}{
						"url":                  "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode",
						"valueCodeableConcept": map[string]interface{}{"coding": []interface{}{map[string]interface{}{"system": "https://codesystem.x12.org/005010/306", "code": code}}},
					},
				},
			}},
		}},
	}
	if qty > 0 {
		item["extension"] = []interface{}{map[string]interface{}{
			"url":       "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-itemAuthorizedDetail",
			"extension": []interface{}{map[string]interface{}{"url": "quantity", "valueQuantity": map[string]interface{}{"value": qty}}},
		}}
	}
	return item
}

func TestFromFHIR_PendedOutcome(t *testing.T) {
	data := map[string]interface{}{
		"id":      "resp-003",
		"outcome": "queued",
	}

	resp, err := FromFHIR(data)
	if err != nil {
		t.Fatalf("FromFHIR error: %v", err)
	}
	if resp.Decision != "pended" {
		t.Errorf("Decision = %v, want pended", resp.Decision)
	}
}

func TestTracker_TrackAndPending(t *testing.T) {
	tracker := &PriorAuthTracker{}

	req1 := PriorAuthRequest{ID: "pa-1", PatientID: "p1", Urgency: "standard"}
	req2 := PriorAuthRequest{ID: "pa-2", PatientID: "p2", Urgency: "expedited"}
	req3 := PriorAuthRequest{ID: "pa-3", PatientID: "p3", Urgency: "standard"}

	tracker.Track(req1, nil) // no response yet
	tracker.Track(req2, &PriorAuthResponse{RequestID: "pa-2", Decision: "approved"})
	tracker.Track(req3, &PriorAuthResponse{RequestID: "pa-3", Decision: "pended"})

	pending := tracker.Pending()
	if len(pending) != 2 {
		t.Fatalf("Pending() returned %d, want 2", len(pending))
	}

	ids := map[string]bool{}
	for _, p := range pending {
		ids[p.ID] = true
	}
	if !ids["pa-1"] {
		t.Error("expected pa-1 in pending")
	}
	if !ids["pa-3"] {
		t.Error("expected pa-3 in pending (pended decision)")
	}
}

func TestTracker_Expiring(t *testing.T) {
	tracker := &PriorAuthTracker{}

	now := time.Now()

	req1 := PriorAuthRequest{ID: "pa-1"}
	resp1 := &PriorAuthResponse{
		RequestID: "pa-1",
		Decision:  "approved",
		ValidTo:   now.Add(5 * 24 * time.Hour), // expires in 5 days
	}

	req2 := PriorAuthRequest{ID: "pa-2"}
	resp2 := &PriorAuthResponse{
		RequestID: "pa-2",
		Decision:  "approved",
		ValidTo:   now.Add(60 * 24 * time.Hour), // expires in 60 days
	}

	req3 := PriorAuthRequest{ID: "pa-3"}
	resp3 := &PriorAuthResponse{
		RequestID: "pa-3",
		Decision:  "denied",
		ValidTo:   now.Add(3 * 24 * time.Hour), // denied, should not appear
	}

	tracker.Track(req1, resp1)
	tracker.Track(req2, resp2)
	tracker.Track(req3, resp3)

	expiring := tracker.Expiring(30 * 24 * time.Hour) // within 30 days
	if len(expiring) != 1 {
		t.Fatalf("Expiring(30d) returned %d, want 1", len(expiring))
	}
	if expiring[0].RequestID != "pa-1" {
		t.Errorf("Expiring returned request %v, want pa-1", expiring[0].RequestID)
	}
}

func TestTracker_Stats(t *testing.T) {
	tracker := &PriorAuthTracker{}

	baseTime := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

	tracker.Track(
		PriorAuthRequest{ID: "pa-1", Urgency: "standard", RequestDate: baseTime},
		&PriorAuthResponse{RequestID: "pa-1", Decision: "approved", ResponseDate: baseTime.Add(24 * time.Hour)},
	)
	tracker.Track(
		PriorAuthRequest{ID: "pa-2", Urgency: "expedited", RequestDate: baseTime},
		&PriorAuthResponse{RequestID: "pa-2", Decision: "approved", ResponseDate: baseTime.Add(4 * time.Hour)},
	)
	tracker.Track(
		PriorAuthRequest{ID: "pa-3", Urgency: "standard", RequestDate: baseTime},
		&PriorAuthResponse{RequestID: "pa-3", Decision: "denied", ResponseDate: baseTime.Add(48 * time.Hour)},
	)
	tracker.Track(
		PriorAuthRequest{ID: "pa-4", Urgency: "expedited", RequestDate: baseTime},
		&PriorAuthResponse{RequestID: "pa-4", Decision: "pended"},
	)
	tracker.Track(
		PriorAuthRequest{ID: "pa-5", Urgency: "standard"},
		nil,
	)

	stats := tracker.Stats()

	if stats.TotalRequests != 5 {
		t.Errorf("TotalRequests = %d, want 5", stats.TotalRequests)
	}
	if stats.Approved != 2 {
		t.Errorf("Approved = %d, want 2", stats.Approved)
	}
	if stats.Denied != 1 {
		t.Errorf("Denied = %d, want 1", stats.Denied)
	}
	if stats.Pended != 1 {
		t.Errorf("Pended = %d, want 1", stats.Pended)
	}
	if stats.ExpeditedCount != 2 {
		t.Errorf("ExpeditedCount = %d, want 2", stats.ExpeditedCount)
	}

	// Mean response time: (24h + 4h + 48h) / 3 = 76h / 3 ≈ 25h 20m
	// pa-4 has zero ResponseDate so it's excluded, pa-5 has no response.
	expectedMean := (24*time.Hour + 4*time.Hour + 48*time.Hour) / 3
	if stats.MeanResponseTime != expectedMean {
		t.Errorf("MeanResponseTime = %v, want %v", stats.MeanResponseTime, expectedMean)
	}
}

func TestTracker_PendingEmpty(t *testing.T) {
	tracker := &PriorAuthTracker{}

	tracker.Track(
		PriorAuthRequest{ID: "pa-1"},
		&PriorAuthResponse{RequestID: "pa-1", Decision: "approved"},
	)

	pending := tracker.Pending()
	if len(pending) != 0 {
		t.Errorf("Pending() returned %d, want 0", len(pending))
	}
}

func TestTracker_ExpiringNone(t *testing.T) {
	tracker := &PriorAuthTracker{}

	now := time.Now()
	tracker.Track(
		PriorAuthRequest{ID: "pa-1"},
		&PriorAuthResponse{
			RequestID: "pa-1",
			Decision:  "approved",
			ValidTo:   now.Add(365 * 24 * time.Hour),
		},
	)

	expiring := tracker.Expiring(30 * 24 * time.Hour)
	if len(expiring) != 0 {
		t.Errorf("Expiring(30d) returned %d, want 0", len(expiring))
	}
}

// ---------------------------------------------------------------------------
// DEFECT 1: FromFHIR maps outcome="complete" unconditionally to "approved".
// In FHIR, outcome="complete" means adjudication is finished. The actual
// approve/deny decision is carried in item-level adjudication (reviewAction).
// A ClaimResponse with outcome="complete" but a deny reviewAction must be
// "denied", not "approved".
// ---------------------------------------------------------------------------

func TestFromFHIR_CompleteWithDenyAdjudication(t *testing.T) {
	// A ClaimResponse where outcome is "complete" but the item adjudication
	// reviewAction indicates denial.
	data := map[string]interface{}{
		"id":          "resp-deny-complete",
		"outcome":     "complete",
		"disposition": "Service not covered under plan",
		"created":     "2026-08-10T09:00:00Z",
		"item": []interface{}{
			map[string]interface{}{
				"adjudication": []interface{}{
					map[string]interface{}{
						"category": map[string]interface{}{
							"coding": []interface{}{
								map[string]interface{}{
									"system": "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes",
									"code":   "reviewAction",
								},
							},
						},
						"reason": map[string]interface{}{
							"coding": []interface{}{
								map[string]interface{}{
									"system": "http://codesystem.x12.org/005010/306",
									"code":   "A3", // not certified
								},
							},
						},
					},
				},
			},
		},
	}

	resp, err := FromFHIR(data)
	if err != nil {
		t.Fatalf("FromFHIR error: %v", err)
	}
	// DEFECT: outcome="complete" is mapped to "approved" but this should be "denied"
	// because the reviewAction adjudication category indicates denial.
	if resp.Decision != "denied" {
		t.Errorf("Decision = %q, want %q (outcome=complete with deny adjudication must not be 'approved')", resp.Decision, "denied")
	}
}

// ---------------------------------------------------------------------------
// DEFECT 2: FromFHIR allows a denial to be returned with no Reason.
// CMS-0057 requires that a denial include a reason so the provider can appeal.
// A denial without a reason cannot be appealed.
// ---------------------------------------------------------------------------

func TestFromFHIR_DenialRequiresReason(t *testing.T) {
	_, err := FromFHIR(map[string]interface{}{"id": "resp-no-reason", "outcome": "complete", "item": []interface{}{paItem(1, "A3", 0)}})
	if err == nil {
		t.Errorf("FromFHIR accepted a denial without a reason code; CMS-0057 requires denials to include a reason for appeal")
	}
}

// ---------------------------------------------------------------------------
// DEFECT 3 (TEFCA-related but tested in priorauth context): Verify that pended
// status is not confused with approved or denied.
// ---------------------------------------------------------------------------

func TestFromFHIR_PartialOutcomeIsPended(t *testing.T) {
	data := map[string]interface{}{
		"id":      "resp-partial",
		"outcome": "partial",
		"item":    []interface{}{paItem(1, "A1", 0), paItem(2, "A4", 0)},
	}

	resp, err := FromFHIR(data)
	if err != nil {
		t.Fatalf("FromFHIR error: %v", err)
	}
	if resp.Decision != "pended" {
		t.Errorf("Decision = %q, want %q (partial outcome is still-deciding)", resp.Decision, "pended")
	}
}

func TestAnOpenedTrackerSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "priorauth.json")
	tr, err := OpenTracker(path)
	if err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(48 * time.Hour)
	if err := tr.Track(PriorAuthRequest{ID: "pa-1", PatientID: "p1"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(PriorAuthRequest{ID: "pa-2", PatientID: "p2"},
		&PriorAuthResponse{RequestID: "pa-2", Decision: DecisionModified, ValidTo: soon}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the tracker file is %v (%v), want readable by its owner only", info, err)
	}

	// The process ends; a new one opens the same file.
	again, err := OpenTracker(path)
	if err != nil {
		t.Fatal(err)
	}
	if p := again.Pending(); len(p) != 1 || p[0].ID != "pa-1" {
		t.Errorf("after a restart pending is %+v, want pa-1", p)
	}
	if e := again.Expiring(72 * time.Hour); len(e) != 1 || e[0].RequestID != "pa-2" {
		t.Errorf("after a restart expiring is %+v, want pa-2 (modified is certified too)", e)
	}

	// A decision for a pending request replaces it rather than sitting beside it.
	if err := again.Track(PriorAuthRequest{ID: "pa-1", PatientID: "p1"},
		&PriorAuthResponse{RequestID: "pa-1", Decision: DecisionDenied}); err != nil {
		t.Fatal(err)
	}
	if p := again.Pending(); len(p) != 0 {
		t.Errorf("a decided request is still pending: %+v", p)
	}
	if s := again.Stats(); s.TotalRequests != 2 {
		t.Errorf("tracking a decision added a request: %d tracked, want 2", s.TotalRequests)
	}
}

func TestAnUnreadableTrackerFileIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "priorauth.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTracker(path); err == nil {
		t.Fatal("a damaged tracker file was opened as empty, and would have been overwritten at the next change")
	}
}
