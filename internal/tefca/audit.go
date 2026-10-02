package tefca

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Auditing every exchange, which TEFCA requires and an optional wrapper cannot deliver.
//
// # The shape was wrong, not just incomplete
//
// Query had a QueryWithAudit beside it and the other three patterns had nothing, so the obvious fix is three more
// wrappers. They are here, because callers written against the existing shape expect them.
//
// But the shape itself is the problem. When auditing is mandatory and the unaudited function is exported, the
// unaudited function is the one somebody reaches for: it has the shorter name, it appears first in the
// documentation, and it works. Nothing fails. The audit entry is simply absent, and the absence is discovered by
// an auditor months later asking for records of an exchange that happened and was never written down.
//
// So Participant exists as well, and it is what the rest of Perfuse uses. It holds the configuration and the audit
// log together, and every exchange method on it audits. There is no unaudited path through it, which means there
// is nothing to forget.
//
// The bare functions remain because the package's tests exercise them directly, and because a caller assembling
// its own audit trail from a different source has a legitimate reason to want them. They are now documented as
// what they are.

// Participant is a configured TEFCA participant that audits every exchange.
//
// Constructed once and reused. The audit log travels with it, so no call site has to remember to pass one - which
// is the failure mode the wrappers leave open, because a wrapper that takes an audit log as a parameter can be
// called with a fresh empty one and nobody notices.
type Participant struct {
	cfg   TEFCAConfig
	audit *AuditLog
}

// NewParticipant returns a participant, refusing a configuration that cannot work.
//
// Validated here rather than on each call. A participant that exists is a participant that can exchange, and
// discovering at the moment of a real query that the OID was never set is discovering it at the worst time.
func NewParticipant(cfg TEFCAConfig, audit *AuditLog) (*Participant, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if audit == nil {
		// Refused rather than defaulted to a discarding log. A participant with nowhere to write its audit trail is
		// not a participant; it is a compliance failure that works perfectly until somebody asks for records.
		return nil, errors.New("tefca: a participant needs somewhere to record its audit trail. Every exchange " +
			"must be audited, and a participant with no audit log would exchange data and keep no record of it")
	}
	return &Participant{cfg: cfg, audit: audit}, nil
}

// Config returns the participant's configuration.
func (p *Participant) Config() TEFCAConfig { return p.cfg }

// Query performs an Individual Access Services query, auditing the attempt.
func (p *Participant) Query(ctx context.Context, req QueryRequest) (*QueryResponse, error) {
	return QueryWithAudit(ctx, p.cfg, req, p.audit)
}

// Deliver pushes a bundle to another participant, auditing the attempt.
func (p *Participant) Deliver(ctx context.Context, req DeliveryRequest) (*DeliveryResponse, error) {
	return DeliverWithAudit(ctx, p.cfg, req, p.audit)
}

// Retrieve fetches a document, auditing the attempt.
func (p *Participant) Retrieve(ctx context.Context, req RetrievalRequest) (*RetrievalResponse, error) {
	return RetrieveWithAudit(ctx, p.cfg, req, p.audit)
}

// Notify broadcasts an event notification, auditing the attempt.
func (p *Participant) Notify(ctx context.Context, req NotificationRequest) error {
	return NotifyWithAudit(ctx, p.cfg, req, p.audit)
}

// DeliverWithAudit pushes a bundle and records the exchange whether or not it succeeded.
//
// Failures are audited too, and that is not a technicality. A run of failed deliveries to one recipient is how a
// misconfigured partner shows up, and a run of failed queries for different patients is how probing shows up.
// Auditing only successes hides exactly the patterns an audit trail exists to reveal.
func DeliverWithAudit(ctx context.Context, cfg TEFCAConfig, req DeliveryRequest,
	auditLog *AuditLog) (*DeliveryResponse, error) {

	resp, err := Deliver(ctx, cfg, req)

	entry := TEFCAAudit{
		Timestamp:     time.Now(),
		Direction:     "outbound",
		Purpose:       req.Purpose,
		RequestingOrg: cfg.OrganizationName,
		RespondingOrg: req.Recipient,
		ExchangeType:  "delivery",
		Success:       err == nil,
	}

	// A delivery the far end explicitly refused is recorded as a failure even though no error was returned.
	//
	// Accepted false with a reason is the partner saying no, and recording that as a success would make the audit
	// trail disagree with what happened - which is worse than no trail, because the trail is what gets believed.
	if err == nil && resp != nil && !resp.Accepted {
		entry.Success = false
		entry.ErrorDetail = "the recipient did not accept it: " + resp.Reason
	}
	if err != nil {
		entry.ErrorDetail = err.Error()
	}
	auditLog.Record(entry)

	return resp, err
}

// RetrieveWithAudit fetches a document and records the exchange whether or not it succeeded.
func RetrieveWithAudit(ctx context.Context, cfg TEFCAConfig, req RetrievalRequest,
	auditLog *AuditLog) (*RetrievalResponse, error) {

	resp, err := Retrieve(ctx, cfg, req)

	entry := TEFCAAudit{
		Timestamp:     time.Now(),
		Direction:     "outbound",
		Purpose:       req.Purpose,
		PatientID:     req.PatientID,
		RequestingOrg: cfg.OrganizationName,
		ExchangeType:  "retrieval",
		Success:       err == nil,
	}
	if err != nil {
		entry.ErrorDetail = err.Error()
	}
	if resp != nil && resp.SourceOrg != "" {
		entry.RespondingOrg = resp.SourceOrg
	}
	auditLog.Record(entry)

	return resp, err
}

// NotifyWithAudit broadcasts a notification and records the exchange whether or not it succeeded.
//
// A notification is the pattern most easily mistaken for not needing an audit entry, because it carries no patient
// record and asks for nothing back. But it discloses that a named patient had an event at a named organisation,
// and to everybody subscribed. That is a disclosure, and TEFCA does not exempt it.
func NotifyWithAudit(ctx context.Context, cfg TEFCAConfig, req NotificationRequest, auditLog *AuditLog) error {
	err := Notify(ctx, cfg, req)

	entry := TEFCAAudit{
		Timestamp:     time.Now(),
		Direction:     "outbound",
		Purpose:       req.Purpose,
		PatientID:     req.PatientID,
		RequestingOrg: cfg.OrganizationName,
		ExchangeType:  "notification",
		Success:       err == nil,
	}
	if err != nil {
		entry.ErrorDetail = err.Error()
	}
	auditLog.Record(entry)

	return err
}

// checkPurpose validates a purpose of use against the specification and against what this participant declared.
//
// Both checks matter and they catch different mistakes. An unrecognised purpose is a spelling error or an invention,
// and the network will refuse it. A recognised purpose the participant never declared is a configuration
// disagreement: the exchange may well be permitted by the network and refused by the partner, and finding out from
// a partner's rejection is finding out expensively.
func checkPurpose(cfg TEFCAConfig, purpose, what string) error {
	if purpose == "" {
		return fmt.Errorf("tefca: %s requires a purpose of use. Every exchange has to state why the data is "+
			"being disclosed, and it is recorded in the audit trail", what)
	}
	if !ValidPurpose(purpose) {
		return fmt.Errorf("tefca: %q is not a recognised purpose of use. The recognised ones are %s",
			purpose, listPurposes())
	}

	for _, p := range cfg.SupportedPurposes {
		if p == purpose {
			return nil
		}
	}
	return fmt.Errorf("tefca: %q is a recognised purpose of use but this participant is not configured for it. "+
		"It supports %v. Exchanging for a purpose you have not declared is how a partner comes to refuse an "+
		"exchange that looked correct at this end", purpose, cfg.SupportedPurposes)
}

// listPurposes names the recognised purposes in a stable order for error messages.
//
// Stable because an error message whose wording changes between runs is an error message somebody cannot search
// for, and ranging over a map would reorder it every time.
func listPurposes() string {
	return PurposeTreatment + ", " + PurposePayment + ", " + PurposeOperations + ", " +
		PurposePublicHealth + " and " + PurposeIndividualAccess
}
