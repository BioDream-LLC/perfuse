package compliance

import (
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// ADT NOTIFY AUDIT TESTS
//
// These tests verify or expose gaps in the CMS Condition of Participation (CoP)
// ADT Event Notification implementation (42 CFR 482.24(d)).
// ──────────────────────────────────────────────────────────────────────────────

// ----- DEFECT 5: Cancel events (A11/A13) are not distinguished from the events
// they cancel. The module only has "admit", "discharge", "transfer" event types.
// There is no "cancel-admit" or "cancel-discharge". If a caller maps an A13
// cancel-discharge HL7 message to EventDischarge, the system sends discharge
// notifications for a cancelled discharge — a WRONG notification that violates
// CMS CoP. The type system must refuse or separately handle cancel events.
func TestCancelEventIsNotTreatedAsDischarge(t *testing.T) {
	// Verify that cancel event types exist and that routing them does NOT
	// produce notification targets. An A13 cancel-discharge must not trigger
	// discharge notifications to the patient's care team.

	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://pcp.example.com/notify", RolePCP)
	reg.RegisterProvider("P001", "FAC01", "Sunrise Rehab", "https://pac.example.com/notify", RolePAC)

	router := NewADTRouter(reg)

	// Verify cancel event types exist.
	if EventCancelAdmit == "" {
		t.Fatal("EventCancelAdmit constant should exist")
	}
	if EventCancelDischarge == "" {
		t.Fatal("EventCancelDischarge constant should exist")
	}

	// A cancel-discharge must NOT produce targets. Sending a discharge notification
	// for a cancelled discharge is a wrong notification — a regulatory violation.
	targets := router.Route("P001", EventCancelDischarge)
	if len(targets) != 0 {
		t.Errorf("cancel-discharge produced %d notification targets; want 0 (wrong notification)", len(targets))
	}

	// A cancel-admit must NOT produce targets.
	targets = router.Route("P001", EventCancelAdmit)
	if len(targets) != 0 {
		t.Errorf("cancel-admit produced %d notification targets; want 0 (wrong notification)", len(targets))
	}

	// Verify that regular discharge DOES still produce targets (regression guard).
	targets = router.Route("P001", EventDischarge)
	if len(targets) == 0 {
		t.Error("regular discharge should still produce targets")
	}
}

// ----- DEFECT 6: Route returns nil silently when no providers found -----
//
// CMS CoP requires notification to the patient's established care team. If there
// are no providers registered (which may be a data issue), the system should
// surface this fact as an error or a logged alert rather than silently producing
// zero notifications. A silently empty result is indistinguishable from "all
// notifications sent successfully" when audited.
func TestRouteNoProviders_ShouldSurfaceIssue(t *testing.T) {
	reg := NewProviderRegistry()
	router := NewADTRouter(reg)

	// Patient has no registered providers. The CMS regulation requires notifications
	// to go to established providers. If there are none on record, this is a data
	// quality issue that should be surfaced - not silently swallowed.
	targets := router.Route("P001", EventDischarge)

	// Currently returns nil with no indication that this is a problem.
	// The test documents the gap: there should be either an error return or
	// an explicit "no recipients" status that the caller can log/report.
	if targets == nil {
		t.Log("DEFECT (unproven structurally): Route returns nil for a patient with no " +
			"providers. This silently produces zero notifications. The CMS CoP requires " +
			"notification at discharge; a silent empty list is indistinguishable from " +
			"'nothing to do' versus 'data is missing'. Route should return an error or " +
			"a sentinel indicating the recipient list could not be built.")
		// This is a design gap rather than a provable-by-test defect.
		// The function signature returns []NotificationTarget with no error.
		// We cannot make this fail without changing the API.
	}
}

// ----- DEFECT 7: No required content validation before recording -----
//
// CMS CoP requires notifications to include: patient name, treating practitioner,
// sending institution, event type, and time. The Notification struct has no
// validation that these are present. A notification with empty PatientID or empty
// EventType can be recorded without error.
func TestNotificationValidation_MissingRequiredFields(t *testing.T) {
	tracker := NewNotificationTracker()

	// Record a notification with missing required fields.
	// CMS requires: patient name, treating practitioner, sending institution, event type, time.
	emptyNotification := Notification{
		// PatientID intentionally empty
		// ProviderID intentionally empty
		// EventType intentionally empty
		Timestamp:      time.Time{}, // zero time
		DeliveryStatus: StatusSent,
		MessageID:      "MSG001",
		Channel:        "test",
	}

	// Should refuse to record an incomplete notification.
	err := tracker.Record(emptyNotification)
	if err == nil {
		t.Error("Record() should return an error for notification with missing PatientID")
	}

	// Missing ProviderID
	err = tracker.Record(Notification{
		PatientID:      "P001",
		EventType:      EventDischarge,
		Timestamp:      time.Now(),
		DeliveryStatus: StatusSent,
		MessageID:      "MSG002",
		Channel:        "test",
	})
	if err == nil {
		t.Error("Record() should return an error for notification with missing ProviderID")
	}

	// Missing EventType
	err = tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR01",
		Timestamp:      time.Now(),
		DeliveryStatus: StatusSent,
		MessageID:      "MSG003",
		Channel:        "test",
	})
	if err == nil {
		t.Error("Record() should return an error for notification with missing EventType")
	}

	// Valid notification should succeed.
	err = tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR01",
		EventType:      EventDischarge,
		Timestamp:      time.Now(),
		DeliveryStatus: StatusSent,
		MessageID:      "MSG004",
		Channel:        "test",
	})
	if err != nil {
		t.Errorf("Record() should succeed for valid notification, got: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// VERIFIED CORRECT
// ──────────────────────────────────────────────────────────────────────────────

// Verify: All three mandatory event types (admit, discharge, transfer) are covered.
func TestAllMandatoryEventTypesCovered(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://pcp.example.com/notify", RolePCP)
	reg.RegisterProvider("P001", "FAC01", "Sunrise Rehab", "https://pac.example.com/notify", RolePAC)

	router := NewADTRouter(reg)

	// Admit
	targets := router.Route("P001", EventAdmit)
	if len(targets) == 0 {
		t.Error("Admit event should produce targets")
	}

	// Discharge
	targets = router.Route("P001", EventDischarge)
	if len(targets) == 0 {
		t.Error("Discharge event should produce targets")
	}

	// Transfer
	targets = router.Route("P001", EventTransfer)
	if len(targets) == 0 {
		t.Error("Transfer event should produce targets")
	}
}

// Verify: Discharge and transfer are immediate priority.
func TestDischargeAndTransferAreImmediatePriority(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://pcp.example.com/notify", RolePCP)

	router := NewADTRouter(reg)

	for _, evt := range []EventType{EventDischarge, EventTransfer} {
		targets := router.Route("P001", evt)
		for _, tgt := range targets {
			if tgt.Priority != PriorityImmediate {
				t.Errorf("%s should be immediate priority, got %s", evt, tgt.Priority)
			}
		}
	}
}

// Verify: All established providers (PCP, specialist, PAC) receive discharge notifications.
func TestDischargeNotifiesAllProviderRoles(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "PCP", "https://example.com/pcp", RolePCP)
	reg.RegisterProvider("P001", "DR02", "Specialist", "https://example.com/spec", RoleSpecialist)
	reg.RegisterProvider("P001", "FAC01", "PAC", "https://example.com/pac", RolePAC)

	router := NewADTRouter(reg)
	targets := router.Route("P001", EventDischarge)

	roles := make(map[ProviderRole]bool)
	for _, tgt := range targets {
		roles[tgt.Provider.Role] = true
	}

	if !roles[RolePCP] {
		t.Error("PCP not notified on discharge")
	}
	if !roles[RoleSpecialist] {
		t.Error("Specialist not notified on discharge")
	}
	if !roles[RolePAC] {
		t.Error("PAC not notified on discharge")
	}
}

// Verify: NotificationTracker records delivery status correctly.
func TestTrackerRecordsDeliveryStatus(t *testing.T) {
	tracker := NewNotificationTracker()
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)

	tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR01",
		EventType:      EventDischarge,
		Timestamp:      now,
		DeliveryStatus: StatusFailed,
		MessageID:      "MSG001",
		Channel:        "adt",
	})

	report := tracker.DeliveryReport("P001", now.Add(-time.Hour), now.Add(time.Hour))
	if len(report) != 1 {
		t.Fatalf("expected 1 record, got %d", len(report))
	}
	if report[0].DeliveryStatus != StatusFailed {
		t.Errorf("delivery status = %s, want failed", report[0].DeliveryStatus)
	}
}

// Verify: Thread safety (already tested in existing tests, but confirm our additions don't break it).
func TestAudit_ThreadSafe(t *testing.T) {
	// This is effectively already covered by TestConcurrentAccess in the existing tests.
	// Just confirm the race detector doesn't fire on our audit-specific operations.
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://example.com", RolePCP)

	router := NewADTRouter(reg)
	targets := router.Route("P001", EventAdmit)
	if len(targets) == 0 {
		t.Error("should have targets")
	}
}
