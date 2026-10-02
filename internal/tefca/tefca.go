// Package tefca implements the parts of TEFCA participation that do not require a QHIN connection.
//
// # What is real
//
// Purpose-of-use validation, configuration validation, and the audit trail - including its persistence, its query interface and its
// obligation to record failed attempts as well as successful ones. That last part is not a technicality: a run of failed deliveries to
// one recipient is how a misconfigured partner shows up, and a run of failed queries for different patients is how probing shows up.
//
// # What is not
//
// The network. There is no QHIN transport in this build, so no exchange happens, and every exchange function refuses with
// ErrExchangeNotImplemented rather than returning success.
//
// That distinction was not always drawn, and the way it went wrong is worth keeping. All four exchange functions used to validate their
// inputs, make no network call, and return success; Deliver returned an Accepted response with a tracking identifier built from the
// clock. The audit layer then recorded each as a completed exchange - in the log that exists to prove what was exchanged when somebody
// asks months later. Four tests asserted that behaviour and passed.
//
// # What completing it requires
//
// Onboarding with a Qualified Health Information Network, mutually authenticated TLS using certificates issued through that process,
// and either the IHE profiles the Common Agreement started with or Facilitated FHIR as defined by the Sequoia Project's standard
// operating procedure, effective 8 March 2026. None of it can be written speculatively: it is tested against a real QHIN or it is not
// tested, and an untested exchange implementation is the same defect as no implementation wearing a better disguise.
package tefca

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Purpose of Use constants per TEFCA specification.
const (
	PurposeTreatment        = "treatment"
	PurposePayment          = "payment"
	PurposeOperations       = "operations"
	PurposePublicHealth     = "public_health"
	PurposeIndividualAccess = "individual_access"
)

// validPurposes is the authoritative set of TEFCA-recognized purposes.
var validPurposes = map[string]bool{
	PurposeTreatment:        true,
	PurposePayment:          true,
	PurposeOperations:       true,
	PurposePublicHealth:     true,
	PurposeIndividualAccess: true,
}

// ErrExchangeNotImplemented means no transport exists to carry a TEFCA exchange.
//
// # Why this error exists rather than a stub that returns success
//
// Every exchange function in this package validated its inputs, made no network call, and returned success. Deliver went further and
// returned Accepted with a fabricated tracking identifier. The audit layer above then recorded the exchange as having happened - in a
// persistent log whose entire purpose is to prove what was exchanged when somebody asks months later, prompted by a complaint or an
// investigation.
//
// The audit code's own comment says that a trail disagreeing with what happened is "worse than no trail, because the trail is what gets
// believed". That was exactly right, and the layer underneath it made the comment false.
//
// So the functions refuse. A refusal is auditable as a failure, which TEFCA requires anyway, and it cannot be mistaken for an exchange.
// This project has a standing invariant that a feature which cannot work yet is refused at load rather than silently skipped at run
// time; this is that rule applied one level down, because the audit trail is real and worth keeping available even while exchange is
// not.
//
// # What is missing
//
// A QHIN transport: mutually authenticated TLS to a Qualified Health Information Network, using certificates issued through QHIN
// onboarding, speaking either the IHE profiles the Common Agreement started with or Facilitated FHIR as defined by the Sequoia
// Project's standard operating procedure. None of that can be written speculatively - it is tested against a real QHIN or it is not
// tested at all.
var ErrExchangeNotImplemented = errors.New(
	"tefca: this build has no QHIN transport, so nothing was exchanged. " +
		"Purpose-of-use checking, configuration validation and the audit trail are real and work; the network call is not " +
		"implemented and is refused rather than reported as a success. Onboarding with a QHIN, mutually authenticated TLS and " +
		"either the IHE profiles or Facilitated FHIR are what remain")

// ValidPurpose reports whether purpose is a recognized TEFCA Purpose of Use.
func ValidPurpose(purpose string) bool {
	return validPurposes[purpose]
}

// TEFCAConfig holds the configuration for a TEFCA network participant.
type TEFCAConfig struct {
	// OrganizationName is the human-readable name of the participating organization.
	OrganizationName string

	// OrganizationOID is the OID assigned to the organization.
	OrganizationOID string

	// QHINEndpoint is the base URL of the Qualified Health Information Network.
	QHINEndpoint string

	// ParticipantType is one of: provider, payer, public_health.
	ParticipantType string

	// CertificatePath is the filesystem path to the mTLS certificate.
	CertificatePath string

	// KeyPath is the filesystem path to the mTLS private key.
	KeyPath string

	// SupportedPurposes lists the purposes of use this participant supports.
	SupportedPurposes []string
}

// validParticipantTypes enumerates allowed participant types.
var validParticipantTypes = map[string]bool{
	"provider":      true,
	"payer":         true,
	"public_health": true,
}

// Validate checks that all required TEFCAConfig fields are present and valid.
func (c TEFCAConfig) Validate() error {
	if c.OrganizationName == "" {
		return errors.New("tefca: OrganizationName is required")
	}
	if c.OrganizationOID == "" {
		return errors.New("tefca: OrganizationOID is required")
	}
	if c.QHINEndpoint == "" {
		return errors.New("tefca: QHINEndpoint is required")
	}
	if !validParticipantTypes[c.ParticipantType] {
		return fmt.Errorf("tefca: invalid ParticipantType %q (must be provider, payer, or public_health)", c.ParticipantType)
	}
	if c.CertificatePath == "" {
		return errors.New("tefca: CertificatePath is required")
	}
	if c.KeyPath == "" {
		return errors.New("tefca: KeyPath is required")
	}
	if len(c.SupportedPurposes) == 0 {
		return errors.New("tefca: at least one SupportedPurpose is required")
	}
	for _, p := range c.SupportedPurposes {
		if !ValidPurpose(p) {
			return fmt.Errorf("tefca: unsupported purpose %q", p)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Query/Response (Individual Access Services)
// ---------------------------------------------------------------------------

// QueryRequest describes a patient-matching query sent to the TEFCA network.
type QueryRequest struct {
	PatientID   string
	PatientName string
	DOB         string
	Gender      string
	Purpose     string
}

// FHIRBundle is a placeholder for a FHIR Bundle resource returned from a query.
type FHIRBundle struct {
	Raw []byte // JSON-encoded FHIR Bundle
}

// QueryResponse contains the results of a TEFCA query.
type QueryResponse struct {
	Records   []FHIRBundle
	SourceOrg string
	Timestamp time.Time
}

// Query performs a TEFCA Individual Access Services query against the configured
// QHIN endpoint. The context controls cancellation and deadlines.
func Query(ctx context.Context, cfg TEFCAConfig, req QueryRequest) (*QueryResponse, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := validateQuery(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("tefca: context error: %w", err)
	}

	// Validation above is real and worth running first: a caller with a malformed request should be told that rather than told the
	// transport is missing, because the malformed request is still a fault they have to fix.
	return nil, ErrExchangeNotImplemented
}

// QueryWithAudit performs a TEFCA query and records an audit entry for the
// exchange regardless of success or failure. Every exchange MUST be audited
// per TEFCA requirements — including failed attempts, which could indicate
// probing attacks.
func QueryWithAudit(ctx context.Context, cfg TEFCAConfig, req QueryRequest, auditLog *AuditLog) (*QueryResponse, error) {
	resp, err := Query(ctx, cfg, req)

	entry := TEFCAAudit{
		Timestamp:     time.Now(),
		Direction:     "outbound",
		Purpose:       req.Purpose,
		PatientID:     req.PatientID,
		RequestingOrg: cfg.OrganizationName,
		ExchangeType:  "query",
		Success:       err == nil,
	}
	if err != nil {
		entry.ErrorDetail = err.Error()
	}
	auditLog.Record(entry)

	return resp, err
}

// ---------------------------------------------------------------------------
// Message Delivery
// ---------------------------------------------------------------------------

// DeliveryRequest describes a FHIR bundle push to another TEFCA participant.
type DeliveryRequest struct {
	Recipient string
	Purpose   string
	Bundle    []byte // FHIR Bundle JSON
}

// DeliveryResponse describes the outcome of a message delivery.
type DeliveryResponse struct {
	Accepted   bool
	TrackingID string
	Reason     string
}

// Deliver pushes a FHIR bundle to the specified recipient via the QHIN.
func Deliver(ctx context.Context, cfg TEFCAConfig, req DeliveryRequest) (*DeliveryResponse, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if req.Recipient == "" {
		return nil, errors.New("tefca: DeliveryRequest requires Recipient")
	}
	if !ValidPurpose(req.Purpose) {
		return nil, fmt.Errorf("tefca: invalid purpose %q", req.Purpose)
	}
	if len(req.Bundle) == 0 {
		return nil, errors.New("tefca: DeliveryRequest requires non-empty Bundle")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("tefca: context error: %w", err)
	}

	// No tracking identifier is invented. A caller holding a reference number believes something is in flight and will go looking for
	// it at the far end, which is the most expensive way to discover this.
	return nil, ErrExchangeNotImplemented
}

// ---------------------------------------------------------------------------
// Document Retrieval
// ---------------------------------------------------------------------------

// RetrievalRequest describes a request to fetch a specific document by reference.
type RetrievalRequest struct {
	DocumentRef string
	PatientID   string
	Purpose     string
}

// RetrievalResponse contains the retrieved document.
type RetrievalResponse struct {
	Document    []byte
	ContentType string
	SourceOrg   string
}

// Retrieve fetches a specific document from the TEFCA network by reference.
func Retrieve(ctx context.Context, cfg TEFCAConfig, req RetrievalRequest) (*RetrievalResponse, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if req.DocumentRef == "" {
		return nil, errors.New("tefca: RetrievalRequest requires DocumentRef")
	}
	if req.PatientID == "" {
		return nil, errors.New("tefca: RetrievalRequest requires PatientID")
	}
	if !ValidPurpose(req.Purpose) {
		return nil, fmt.Errorf("tefca: invalid purpose %q", req.Purpose)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("tefca: context error: %w", err)
	}

	return nil, ErrExchangeNotImplemented
}

// ---------------------------------------------------------------------------
// Broadcast Notification
// ---------------------------------------------------------------------------

// NotificationRequest describes an ADT-style event notification broadcast.
type NotificationRequest struct {
	EventType string
	PatientID string
	Bundle    []byte

	// Purpose is why the disclosure is being made.
	//
	// Absent from the first version of this type, which made notification the one pattern that could disclose
	// without stating why. A notification is easy to think of as exempt: it carries no record and asks for nothing
	// back. But it tells everybody subscribed that a named patient had an event at a named organisation, and that
	// is a disclosure. TEFCA does not exempt it, and an audit entry with an empty purpose column is an audit entry
	// that answers no question anybody asks.
	Purpose string
}

// Notify broadcasts an event notification across the TEFCA network.
func Notify(ctx context.Context, cfg TEFCAConfig, req NotificationRequest) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if req.EventType == "" {
		return errors.New("tefca: NotificationRequest requires EventType")
	}
	if req.PatientID == "" {
		return errors.New("tefca: NotificationRequest requires PatientID")
	}
	if len(req.Bundle) == 0 {
		return errors.New("tefca: NotificationRequest requires non-empty Bundle")
	}
	if err := checkPurpose(cfg, req.Purpose, "a notification"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tefca: context error: %w", err)
	}

	return ErrExchangeNotImplemented
}

// ---------------------------------------------------------------------------
// Audit Logging
// ---------------------------------------------------------------------------

// TEFCAAudit represents a single TEFCA-mandated audit entry.
type TEFCAAudit struct {
	Timestamp     time.Time
	Direction     string // "inbound" or "outbound"
	Purpose       string
	PatientID     string
	RequestingOrg string
	RespondingOrg string
	ExchangeType  string // "query", "delivery", "retrieval", "notification"
	Success       bool
	ErrorDetail   string
}

// AuditLog records and queries TEFCA audit entries. It is safe for concurrent use.
type AuditLog struct {
	mu      sync.Mutex
	entries []TEFCAAudit
}

// Record appends an audit entry to the log.
func (a *AuditLog) Record(entry TEFCAAudit) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	a.entries = append(a.entries, entry)
}

// Query returns all audit entries whose timestamp falls within [from, to].
func (a *AuditLog) Query(from, to time.Time) []TEFCAAudit {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Initialised empty rather than left nil.
	//
	// This is marshalled to JSON for the interface, and a nil slice becomes null - so "no exchanges in this window"
	// arrives as a missing field rather than as zero. For an audit trail those are very different answers: one says
	// nothing happened, the other says the log could not be read.
	results := []TEFCAAudit{}
	for _, e := range a.entries {
		if !e.Timestamp.Before(from) && !e.Timestamp.After(to) {
			results = append(results, e)
		}
	}
	return results
}

// validateQuery applies the patient-matching rules a query has to satisfy before it leaves.
//
// Extracted so the stub path and the Facilitated FHIR path cannot drift apart. They did not share this once, and a rule enforced on one
// route and not the other is worse than no rule: it reads as protection everywhere and exists in one place.
func validateQuery(req QueryRequest) error {
	if req.PatientID == "" && req.PatientName == "" {
		return errors.New("tefca: QueryRequest requires PatientID or PatientName")
	}

	// A name with no date of birth is refused. Matching on a name alone does not produce an error when it is wrong, it produces somebody
	// else's medical record, and the person reading it has no way to tell.
	if req.PatientID == "" && req.DOB == "" {
		return errors.New("tefca: QueryRequest by name requires DOB for safe patient matching; name alone is insufficient")
	}

	if !ValidPurpose(req.Purpose) {
		return fmt.Errorf("tefca: invalid purpose %q", req.Purpose)
	}

	return nil
}
