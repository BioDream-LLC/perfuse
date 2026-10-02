// Package compliance implements the CMS ADT Event Notification workflow
// required by 42 CFR 482.24(d) since May 2021.
//
// Hospitals must electronically notify a patient's established providers —
// primary care, specialists, post-acute care facilities — when the patient is
// admitted, discharged, or transferred. The regulation exists because patients
// fall through the cracks during transitions: a PCP who does not know about a
// discharge cannot schedule the follow-up that prevents a readmission.
//
// This package provides the machinery to:
//   - Track which providers are established for each patient.
//   - Route ADT events to the correct providers at the correct priority.
//   - Record every notification sent, with delivery status, for CMS surveyor
//     audits.
//   - Generate compliance reports showing notification rates and failures.
//
// Thread safety is required throughout because ADT messages arrive concurrently
// from multiple channels and the notification pipeline runs in parallel.
package compliance

import (
	"errors"
	"sync"
	"time"
)

// Errors returned by the notification tracker when a required field is missing.
var (
	ErrMissingPatientID  = errors.New("compliance: notification missing patient ID")
	ErrMissingProviderID = errors.New("compliance: notification missing provider ID")
	ErrMissingEventType  = errors.New("compliance: notification missing event type")
	ErrMissingTimestamp  = errors.New("compliance: notification missing timestamp")
)

// ProviderRole categorizes how a provider relates to a patient.
type ProviderRole string

const (
	RolePCP        ProviderRole = "PCP"
	RoleSpecialist ProviderRole = "specialist"
	RolePAC        ProviderRole = "PAC" // post-acute care
)

// Provider is a clinician or facility that has an established relationship
// with a patient and must receive ADT notifications.
type Provider struct {
	ID       string
	Name     string
	Endpoint string       // where to send notifications (URL, Direct address, etc.)
	Role     ProviderRole // PCP, specialist, or PAC
}

// ProviderRegistry maps patients to their established providers. Thread-safe.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string][]Provider // keyed by patientID
}

// NewProviderRegistry returns an empty registry ready for use.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string][]Provider),
	}
}

// RegisterProvider adds a provider as established for a patient. Duplicate
// provider IDs for the same patient are silently ignored.
func (r *ProviderRegistry) RegisterProvider(patientID, providerID, providerName, endpoint string, role ProviderRole) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.providers[patientID] {
		if p.ID == providerID {
			return
		}
	}
	r.providers[patientID] = append(r.providers[patientID], Provider{
		ID:       providerID,
		Name:     providerName,
		Endpoint: endpoint,
		Role:     role,
	})
}

// EstablishedProviders returns the providers established for a patient. The
// returned slice is a copy; callers may modify it freely.
func (r *ProviderRegistry) EstablishedProviders(patientID string) []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.providers[patientID]
	if len(src) == 0 {
		return nil
	}
	out := make([]Provider, len(src))
	copy(out, src)
	return out
}

// EventType is the kind of ADT event (admit, discharge, transfer).
type EventType string

const (
	EventAdmit           EventType = "admit"
	EventDischarge       EventType = "discharge"
	EventTransfer        EventType = "transfer"
	EventCancelAdmit     EventType = "cancel-admit"
	EventCancelDischarge EventType = "cancel-discharge"
)

// Priority indicates how urgently a notification must be delivered.
type Priority string

const (
	PriorityImmediate Priority = "immediate"
	PriorityNormal    Priority = "normal"
)

// NotificationTarget is a provider that must receive a notification, with
// the priority at which it should be sent.
type NotificationTarget struct {
	Provider Provider
	Priority Priority
}

// ADTRouter determines which providers must be notified for a given ADT event.
type ADTRouter struct {
	registry *ProviderRegistry
}

// NewADTRouter creates a router backed by the given provider registry.
func NewADTRouter(registry *ProviderRegistry) *ADTRouter {
	return &ADTRouter{registry: registry}
}

// Route returns the notification targets for a patient event. Discharge and
// transfer events are immediate priority (the patient is leaving now and follow-
// up must be arranged); admits are normal priority.
//
// For discharge events, PCP and PAC providers are always notified. Specialists
// are notified for all event types.
// For admit events, all established providers are notified at normal priority.
// For transfer events, all established providers are notified at immediate priority.
//
// Cancel events (A11 cancel-admit, A13 cancel-discharge) produce NO notification
// targets. Sending a discharge notification for a cancelled discharge is a wrong
// notification — a regulatory violation under the CMS CoP. The caller is expected
// to record the cancel event in the tracker for audit purposes but no outbound
// notifications are generated.
func (r *ADTRouter) Route(patientID string, eventType EventType) []NotificationTarget {
	// Cancel events must never generate outbound notifications. An A13 cancel-discharge
	// routed as a discharge notification tells a PCP to prepare for a patient who is
	// not actually coming home.
	if eventType == EventCancelAdmit || eventType == EventCancelDischarge {
		return nil
	}

	providers := r.registry.EstablishedProviders(patientID)
	if len(providers) == 0 {
		return nil
	}

	var targets []NotificationTarget
	switch eventType {
	case EventDischarge:
		for _, p := range providers {
			switch p.Role {
			case RolePCP, RolePAC:
				targets = append(targets, NotificationTarget{Provider: p, Priority: PriorityImmediate})
			case RoleSpecialist:
				targets = append(targets, NotificationTarget{Provider: p, Priority: PriorityImmediate})
			}
		}
	case EventTransfer:
		for _, p := range providers {
			targets = append(targets, NotificationTarget{Provider: p, Priority: PriorityImmediate})
		}
	case EventAdmit:
		for _, p := range providers {
			targets = append(targets, NotificationTarget{Provider: p, Priority: PriorityNormal})
		}
	}
	return targets
}

// DeliveryStatus tracks whether a notification reached its recipient.
type DeliveryStatus string

const (
	StatusSent      DeliveryStatus = "sent"
	StatusConfirmed DeliveryStatus = "confirmed"
	StatusFailed    DeliveryStatus = "failed"
)

// Notification is a record of a single notification sent to a provider.
type Notification struct {
	PatientID      string
	ProviderID     string
	EventType      EventType
	Timestamp      time.Time
	DeliveryStatus DeliveryStatus
	MessageID      string
	Channel        string
}

// NotificationTracker records notifications for CMS surveyor audits. Thread-safe.
type NotificationTracker struct {
	mu      sync.RWMutex
	records []Notification
}

// NewNotificationTracker returns an empty tracker.
func NewNotificationTracker() *NotificationTracker {
	return &NotificationTracker{}
}

// Record stores a notification. This is the write path; it must not block reads
// longer than necessary.
//
// Returns an error if the notification is missing required elements per CMS CoP:
// patient identifier, provider identifier, event type, and timestamp. A notification
// missing these cannot satisfy a surveyor audit and must not be recorded as if it were
// complete.
func (t *NotificationTracker) Record(n Notification) error {
	if n.PatientID == "" {
		return ErrMissingPatientID
	}
	if n.ProviderID == "" {
		return ErrMissingProviderID
	}
	if n.EventType == "" {
		return ErrMissingEventType
	}
	if n.Timestamp.IsZero() {
		return ErrMissingTimestamp
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, n)
	return nil
}

// DeliveryReport returns all notifications for a patient within a time range,
// suitable for presenting to a CMS surveyor.
func (t *NotificationTracker) DeliveryReport(patientID string, from, to time.Time) []Notification {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Notification
	for _, n := range t.records {
		if n.PatientID != patientID {
			continue
		}
		if n.Timestamp.Before(from) || n.Timestamp.After(to) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// EventTypeSummary holds notification counts for a single event type.
type EventTypeSummary struct {
	Total     int
	Sent      int
	Confirmed int
	Failed    int
}

// Report is an audit-ready compliance report covering a time window.
type Report struct {
	From              time.Time
	To                time.Time
	TotalEvents       int
	NotificationsSent int
	Confirmed         int
	Failed            int
	MeanDeliveryTime  time.Duration
	ByEventType       map[EventType]*EventTypeSummary
}

// ComplianceReport generates audit-ready reports from tracker data.
type ComplianceReport struct {
	tracker *NotificationTracker
}

// NewComplianceReport creates a report generator backed by the given tracker.
func NewComplianceReport(tracker *NotificationTracker) *ComplianceReport {
	return &ComplianceReport{tracker: tracker}
}

// Generate produces a compliance report for the given time window.
func (c *ComplianceReport) Generate(from, to time.Time) *Report {
	c.tracker.mu.RLock()
	defer c.tracker.mu.RUnlock()

	r := &Report{
		From:        from,
		To:          to,
		ByEventType: make(map[EventType]*EventTypeSummary),
	}

	// We track unique events by patient+eventType+timestamp combinations.
	// For mean delivery time, we measure time from the earliest notification for
	// an event to the first confirmed delivery.
	type eventKey struct {
		patientID string
		eventType EventType
		messageID string
	}
	seen := make(map[eventKey]bool)

	for _, n := range c.tracker.records {
		if n.Timestamp.Before(from) || n.Timestamp.After(to) {
			continue
		}

		// Count unique events (by messageID).
		key := eventKey{n.PatientID, n.EventType, n.MessageID}
		if !seen[key] {
			seen[key] = true
			r.TotalEvents++
		}

		r.NotificationsSent++

		summary, ok := r.ByEventType[n.EventType]
		if !ok {
			summary = &EventTypeSummary{}
			r.ByEventType[n.EventType] = summary
		}
		summary.Total++

		switch n.DeliveryStatus {
		case StatusSent:
			summary.Sent++
		case StatusConfirmed:
			summary.Confirmed++
			r.Confirmed++
		case StatusFailed:
			summary.Failed++
			r.Failed++
		}
	}

	// Mean delivery time: for simplicity, we calculate it as the average time
	// between Timestamp and the report generation time for confirmed notifications.
	// In production this would use actual delivery confirmation timestamps.
	// Here we use a simple heuristic: confirmed notifications are assumed to have
	// zero additional delay (instant confirmation), so mean delivery time is zero
	// when all are confirmed. Failed ones don't count.
	if r.Confirmed > 0 {
		r.MeanDeliveryTime = 0 // placeholder: real impl would track send→ack delta
	}

	return r
}
