package tefca

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// validConfig returns a TEFCAConfig that passes validation.
func validConfig() TEFCAConfig {
	return TEFCAConfig{
		OrganizationName:  "Acme Health",
		OrganizationOID:   "2.16.840.1.113883.3.9999",
		QHINEndpoint:      "https://qhin.example.com/api",
		ParticipantType:   "provider",
		CertificatePath:   "/etc/tefca/cert.pem",
		KeyPath:           "/etc/tefca/key.pem",
		SupportedPurposes: []string{PurposeTreatment, PurposePayment},
	}
}

// ---------------------------------------------------------------------------
// Purpose of Use
// ---------------------------------------------------------------------------

func TestValidPurpose(t *testing.T) {
	valid := []string{
		PurposeTreatment,
		PurposePayment,
		PurposeOperations,
		PurposePublicHealth,
		PurposeIndividualAccess,
	}
	for _, p := range valid {
		if !ValidPurpose(p) {
			t.Errorf("ValidPurpose(%q) = false, want true", p)
		}
	}
}

func TestInvalidPurpose(t *testing.T) {
	invalid := []string{"", "research", "marketing", "TREATMENT", "Treatment"}
	for _, p := range invalid {
		if ValidPurpose(p) {
			t.Errorf("ValidPurpose(%q) = true, want false", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Config Validation
// ---------------------------------------------------------------------------

func TestConfigValidate_Valid(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config returned error: %v", err)
	}
}

func TestConfigValidate_MissingFields(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*TEFCAConfig)
	}{
		{"missing OrganizationName", func(c *TEFCAConfig) { c.OrganizationName = "" }},
		{"missing OrganizationOID", func(c *TEFCAConfig) { c.OrganizationOID = "" }},
		{"missing QHINEndpoint", func(c *TEFCAConfig) { c.QHINEndpoint = "" }},
		{"invalid ParticipantType", func(c *TEFCAConfig) { c.ParticipantType = "researcher" }},
		{"empty ParticipantType", func(c *TEFCAConfig) { c.ParticipantType = "" }},
		{"missing CertificatePath", func(c *TEFCAConfig) { c.CertificatePath = "" }},
		{"missing KeyPath", func(c *TEFCAConfig) { c.KeyPath = "" }},
		{"no SupportedPurposes", func(c *TEFCAConfig) { c.SupportedPurposes = nil }},
		{"invalid purpose in list", func(c *TEFCAConfig) { c.SupportedPurposes = []string{"marketing"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.modify(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Query/Response
// ---------------------------------------------------------------------------

func TestQueryRefusesRatherThanReportingAnExchangeThatDidNotHappen(t *testing.T) {
	// This test used to assert that Query succeeded. It did succeed - it validated its inputs, made no network call, and returned an
	// empty response with a timestamp, which the audit layer then recorded as a completed exchange. The test was defending the defect
	// rather than catching it, which is the third time that shape has turned up in this codebase.
	ctx := context.Background()
	req := QueryRequest{
		PatientID:   "P12345",
		PatientName: "Jane Doe",
		DOB:         "1990-01-15",
		Gender:      "female",
		Purpose:     PurposeTreatment,
	}

	resp, err := Query(ctx, validConfig(), req)
	if !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatalf("Query returned %v, want ErrExchangeNotImplemented", err)
	}
	if resp != nil {
		t.Errorf("Query returned a response alongside the refusal: %+v", resp)
	}

	// The message has to say what does work, because somebody reading it needs to know whether to abandon the feature or wait for the
	// transport. Purpose checking and the audit trail are real.
	if !strings.Contains(err.Error(), "audit trail") {
		t.Errorf("the refusal does not say what still works: %v", err)
	}
}

func TestQuery_MissingPatientIdentifier(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := QueryRequest{
		Purpose: PurposeTreatment,
	}
	_, err := Query(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for missing patient identifier")
	}
}

func TestQuery_InvalidPurpose(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := QueryRequest{
		PatientID: "P12345",
		Purpose:   "invalid",
	}
	_, err := Query(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for invalid purpose")
	}
}

func TestQuery_InvalidConfig(t *testing.T) {
	ctx := context.Background()
	cfg := TEFCAConfig{} // empty config
	req := QueryRequest{
		PatientID: "P12345",
		Purpose:   PurposeTreatment,
	}
	_, err := Query(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for invalid config")
	}
}

func TestQuery_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := validConfig()
	req := QueryRequest{
		PatientID: "P12345",
		Purpose:   PurposeTreatment,
	}
	_, err := Query(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

// ---------------------------------------------------------------------------
// Message Delivery
// ---------------------------------------------------------------------------

func TestDeliverInventsNoTrackingIdentifier(t *testing.T) {
	// The worst of the four. Deliver returned Accepted with a fabricated tracking identifier, so a caller held a reference number for a
	// bundle that had never left the building - and would go looking for it at the far end, which is the most expensive way to find
	// this out.
	resp, err := Deliver(context.Background(), validConfig(), DeliveryRequest{
		Recipient: "Other Hospital",
		Purpose:   PurposeTreatment,
		Bundle:    []byte(`{"resourceType":"Bundle"}`),
	})
	if !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatalf("Deliver returned %v, want ErrExchangeNotImplemented", err)
	}
	if resp != nil {
		t.Fatalf("Deliver returned a response alongside the refusal: %+v", resp)
	}
}

func TestDeliver_EmptyBundle(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := DeliveryRequest{
		Recipient: "urn:oid:2.16.840.1.113883.3.1234",
		Purpose:   PurposePayment,
		Bundle:    nil,
	}
	_, err := Deliver(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for empty bundle")
	}
}

func TestDeliver_InvalidPurpose(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := DeliveryRequest{
		Recipient: "urn:oid:2.16.840.1.113883.3.1234",
		Purpose:   "badpurpose",
		Bundle:    []byte(`{"resourceType":"Bundle"}`),
	}
	_, err := Deliver(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for invalid purpose")
	}
}

func TestDeliver_MissingRecipient(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := DeliveryRequest{
		Purpose: PurposePayment,
		Bundle:  []byte(`{"resourceType":"Bundle"}`),
	}
	_, err := Deliver(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for missing recipient")
	}
}

// ---------------------------------------------------------------------------
// Document Retrieval
// ---------------------------------------------------------------------------

func TestRetrieveRefusesRatherThanReturningAnEmptyDocument(t *testing.T) {
	// Retrieve returned a nil document with a content type of application/fhir+json, which reads as "the document is empty" rather
	// than "nothing was fetched". A caller writing that to a file gets a zero-byte FHIR document.
	resp, err := Retrieve(context.Background(), validConfig(), RetrievalRequest{
		DocumentRef: "urn:oid:1.2.3.4.5",
		PatientID:   "P12345",
		Purpose:     PurposeTreatment,
	})
	if !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatalf("Retrieve returned %v, want ErrExchangeNotImplemented", err)
	}
	if resp != nil {
		t.Fatalf("Retrieve returned a response alongside the refusal: %+v", resp)
	}
}

func TestRetrieve_MissingDocumentRef(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := RetrievalRequest{
		PatientID: "P12345",
		Purpose:   PurposeTreatment,
	}
	_, err := Retrieve(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for missing DocumentRef")
	}
}

func TestRetrieve_MissingPatientID(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := RetrievalRequest{
		DocumentRef: "DocumentReference/abc123",
		Purpose:     PurposeTreatment,
	}
	_, err := Retrieve(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for missing PatientID")
	}
}

func TestRetrieve_InvalidPurpose(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := RetrievalRequest{
		DocumentRef: "DocumentReference/abc123",
		PatientID:   "P12345",
		Purpose:     "nope",
	}
	_, err := Retrieve(ctx, cfg, req)
	if err == nil {
		t.Error("expected error for invalid purpose")
	}
}

// ---------------------------------------------------------------------------
// Broadcast Notification
// ---------------------------------------------------------------------------

// notificationForTest is a notification that passes validation, so a test can reach the transport rather than the input checks.
func notificationForTest() NotificationRequest {
	return NotificationRequest{
		PatientID: "P12345",
		EventType: "admission",
		Purpose:   PurposeTreatment,
		Bundle:    []byte(`{"resourceType":"Bundle"}`),
	}
}

func TestNotifyRefusesRatherThanReturningNil(t *testing.T) {
	// Notify returned nil, which for a function whose only result is an error means "the notification was broadcast". Nothing was.
	err := Notify(context.Background(), validConfig(), notificationForTest())
	if !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatalf("Notify returned %v, want ErrExchangeNotImplemented", err)
	}
}

func TestNotify_MissingEventType(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := NotificationRequest{
		PatientID: "P12345",
		Bundle:    []byte(`{"resourceType":"Bundle"}`),
	}
	if err := Notify(ctx, cfg, req); err == nil {
		t.Error("expected error for missing EventType")
	}
}

func TestNotify_MissingPatientID(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := NotificationRequest{
		EventType: "admission",
		Bundle:    []byte(`{"resourceType":"Bundle"}`),
	}
	if err := Notify(ctx, cfg, req); err == nil {
		t.Error("expected error for missing PatientID")
	}
}

func TestNotify_EmptyBundle(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	req := NotificationRequest{
		EventType: "admission",
		PatientID: "P12345",
	}
	if err := Notify(ctx, cfg, req); err == nil {
		t.Error("expected error for empty Bundle")
	}
}

func TestNotify_InvalidConfig(t *testing.T) {
	ctx := context.Background()
	cfg := TEFCAConfig{}
	req := NotificationRequest{
		EventType: "admission",
		PatientID: "P12345",
		Bundle:    []byte(`{"resourceType":"Bundle"}`),
	}
	if err := Notify(ctx, cfg, req); err == nil {
		t.Error("expected error for invalid config")
	}
}

// ---------------------------------------------------------------------------
// Audit Log
// ---------------------------------------------------------------------------

func TestAuditLog_RecordAndQuery(t *testing.T) {
	log := &AuditLog{}

	now := time.Now()
	entry1 := TEFCAAudit{
		Timestamp:     now.Add(-2 * time.Hour),
		Direction:     "outbound",
		Purpose:       PurposeTreatment,
		PatientID:     "P001",
		RequestingOrg: "Acme Health",
		RespondingOrg: "Beta Hospital",
		ExchangeType:  "query",
		Success:       true,
	}
	entry2 := TEFCAAudit{
		Timestamp:     now.Add(-1 * time.Hour),
		Direction:     "inbound",
		Purpose:       PurposePayment,
		PatientID:     "P002",
		RequestingOrg: "Gamma Insurance",
		RespondingOrg: "Acme Health",
		ExchangeType:  "delivery",
		Success:       false,
		ErrorDetail:   "timeout",
	}
	entry3 := TEFCAAudit{
		Timestamp:     now.Add(1 * time.Hour),
		Direction:     "outbound",
		Purpose:       PurposeOperations,
		PatientID:     "P003",
		RequestingOrg: "Acme Health",
		RespondingOrg: "Delta Lab",
		ExchangeType:  "retrieval",
		Success:       true,
	}

	log.Record(entry1)
	log.Record(entry2)
	log.Record(entry3)

	// Query a window that includes entry1 and entry2 but not entry3.
	results := log.Query(now.Add(-3*time.Hour), now)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].PatientID != "P001" {
		t.Errorf("results[0].PatientID = %q, want P001", results[0].PatientID)
	}
	if results[1].PatientID != "P002" {
		t.Errorf("results[1].PatientID = %q, want P002", results[1].PatientID)
	}
}

func TestAuditLog_RecordSetsTimestamp(t *testing.T) {
	log := &AuditLog{}
	entry := TEFCAAudit{
		Direction:    "outbound",
		Purpose:      PurposeTreatment,
		ExchangeType: "query",
		Success:      true,
	}
	log.Record(entry)

	// Query a wide window to get everything.
	results := log.Query(time.Time{}, time.Now().Add(time.Hour))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Timestamp.IsZero() {
		t.Error("expected non-zero timestamp to be set")
	}
}

func TestAuditLog_QueryEmptyRange(t *testing.T) {
	log := &AuditLog{}
	log.Record(TEFCAAudit{
		Timestamp:    time.Now(),
		Direction:    "inbound",
		ExchangeType: "notification",
		Success:      true,
	})

	// Query a range in the past.
	results := log.Query(
		time.Now().Add(-10*time.Hour),
		time.Now().Add(-5*time.Hour),
	)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// DEFECT 1 (TEFCA): Patient matching allows query with PatientName alone,
// which is insufficient demographics. Matching on name alone produces
// wrong-patient data release. A query with only a name (no PatientID, no DOB)
// should be refused.
// ---------------------------------------------------------------------------

func TestQuery_RejectsNameOnlyWithoutIdentifier(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()

	// PatientName alone without any PatientID or DOB should be rejected.
	req := QueryRequest{
		PatientName: "John Smith",
		Purpose:     PurposeTreatment,
	}
	_, err := Query(ctx, cfg, req)
	if err == nil {
		t.Error("DEFECT: Query accepted PatientName alone without DOB or identifier; this produces wrong-patient matches")
	}
}

// ---------------------------------------------------------------------------
// DEFECT 2 (TEFCA): Exchange functions do NOT record audit entries.
// The AuditLog type exists but is never wired to the exchange functions.
// We prove this by adding QueryWithAudit that integrates auditing.
// First we verify the defect: the existing Query function cannot audit.
// ---------------------------------------------------------------------------

func TestQueryAuditsTheAttemptWithItsPurposeOfUse(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	log := &AuditLog{}

	req := QueryRequest{
		PatientID:   "P12345",
		PatientName: "Jane Doe",
		DOB:         "1990-01-15",
		Gender:      "female",
		Purpose:     PurposeTreatment,
	}
	// The refusal is expected. What this test is about is that the attempt reached the audit log carrying the purpose of use, which is
	// the field every question asked of a TEFCA audit trail starts from.
	_, err := QueryWithAudit(ctx, cfg, req, log)
	if err != nil && !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatalf("QueryWithAudit returned an unexpected error: %v", err)
	}

	entries := log.Query(time.Time{}, time.Now().Add(time.Hour))
	if len(entries) == 0 {
		t.Fatal("expected an audit entry after an attempted query, got none")
	}
	entry := entries[0]
	if entry.Purpose != PurposeTreatment {
		t.Errorf("audit Purpose = %q, want %q", entry.Purpose, PurposeTreatment)
	}
	if entry.PatientID != "P12345" {
		t.Errorf("audit PatientID = %q, want %q", entry.PatientID, "P12345")
	}
	if entry.RequestingOrg != cfg.OrganizationName {
		t.Errorf("audit RequestingOrg = %q, want %q", entry.RequestingOrg, cfg.OrganizationName)
	}
	// Whether the attempt succeeded is not this test's subject and cannot be asserted until a transport exists. What must hold is that
	// the entry does not claim success when the exchange did not happen, which is the assertion that would have caught the stubs.
	if err == nil && !entry.Success {
		t.Error("a query that succeeded was audited as a failure")
	}
	if err != nil && entry.Success {
		t.Error("a query that did not happen was audited as a success")
	}
	if entry.ExchangeType != "query" {
		t.Errorf("audit ExchangeType = %q, want %q", entry.ExchangeType, "query")
	}
}

func TestQuery_AuditsFailedExchange(t *testing.T) {
	ctx := context.Background()
	cfg := validConfig()
	log := &AuditLog{}

	// Invalid purpose - should fail but STILL be audited
	req := QueryRequest{
		PatientID: "P12345",
		Purpose:   "bogus_purpose",
	}
	_, err := QueryWithAudit(ctx, cfg, req, log)
	if err == nil {
		t.Fatal("expected error for bogus purpose")
	}

	entries := log.Query(time.Time{}, time.Now().Add(time.Hour))
	if len(entries) == 0 {
		t.Fatal("DEFECT: failed query was NOT audited; an unaudited failure hides probing attacks")
	}
	if entries[0].Success {
		t.Error("expected audit Success = false for failed query")
	}
	if entries[0].ErrorDetail == "" {
		t.Error("expected non-empty ErrorDetail for failed query")
	}
}
