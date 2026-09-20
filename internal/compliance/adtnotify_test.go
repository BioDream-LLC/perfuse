package compliance

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProviderRegistryReturnsEstablishedProviders(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://pcp.example.com/notify", RolePCP)
	reg.RegisterProvider("P001", "DR02", "Dr. Jones", "https://cardio.example.com/notify", RoleSpecialist)
	reg.RegisterProvider("P001", "FAC01", "Sunrise Rehab", "https://pac.example.com/notify", RolePAC)

	providers := reg.EstablishedProviders("P001")
	if len(providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(providers))
	}

	// Verify each provider is present.
	byID := make(map[string]Provider)
	for _, p := range providers {
		byID[p.ID] = p
	}
	if p, ok := byID["DR01"]; !ok {
		t.Error("DR01 not found")
	} else if p.Role != RolePCP {
		t.Errorf("DR01 role = %q, want PCP", p.Role)
	}
	if p, ok := byID["FAC01"]; !ok {
		t.Error("FAC01 not found")
	} else if p.Role != RolePAC {
		t.Errorf("FAC01 role = %q, want PAC", p.Role)
	}
}

func TestProviderRegistryReturnsNilForUnknownPatient(t *testing.T) {
	reg := NewProviderRegistry()
	if got := reg.EstablishedProviders("NOBODY"); got != nil {
		t.Errorf("expected nil for unknown patient, got %v", got)
	}
}

func TestProviderRegistryDeduplicates(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://example.com", RolePCP)
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://example.com", RolePCP)
	if got := len(reg.EstablishedProviders("P001")); got != 1 {
		t.Errorf("expected 1 provider after duplicate registration, got %d", got)
	}
}

func TestRouterDischargeNotifiesPCPAndPAC(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://pcp.example.com/notify", RolePCP)
	reg.RegisterProvider("P001", "DR02", "Dr. Jones", "https://cardio.example.com/notify", RoleSpecialist)
	reg.RegisterProvider("P001", "FAC01", "Sunrise Rehab", "https://pac.example.com/notify", RolePAC)

	router := NewADTRouter(reg)
	targets := router.Route("P001", EventDischarge)

	if len(targets) != 3 {
		t.Fatalf("discharge should notify all 3 providers, got %d", len(targets))
	}

	for _, tgt := range targets {
		if tgt.Priority != PriorityImmediate {
			t.Errorf("discharge notifications should be immediate, got %q for %s", tgt.Priority, tgt.Provider.ID)
		}
	}

	// Verify PCP and PAC are included.
	roles := make(map[ProviderRole]bool)
	for _, tgt := range targets {
		roles[tgt.Provider.Role] = true
	}
	if !roles[RolePCP] {
		t.Error("PCP should be notified on discharge")
	}
	if !roles[RolePAC] {
		t.Error("PAC should be notified on discharge")
	}
}

func TestRouterAdmitNotifiesAllAtNormalPriority(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://pcp.example.com/notify", RolePCP)
	reg.RegisterProvider("P001", "DR02", "Dr. Jones", "https://cardio.example.com/notify", RoleSpecialist)

	router := NewADTRouter(reg)
	targets := router.Route("P001", EventAdmit)

	if len(targets) != 2 {
		t.Fatalf("admit should notify all 2 providers, got %d", len(targets))
	}
	for _, tgt := range targets {
		if tgt.Priority != PriorityNormal {
			t.Errorf("admit notifications should be normal priority, got %q for %s", tgt.Priority, tgt.Provider.ID)
		}
	}
}

func TestRouterTransferIsImmediate(t *testing.T) {
	reg := NewProviderRegistry()
	reg.RegisterProvider("P001", "DR01", "Dr. Smith", "https://pcp.example.com/notify", RolePCP)

	router := NewADTRouter(reg)
	targets := router.Route("P001", EventTransfer)

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Priority != PriorityImmediate {
		t.Errorf("transfer should be immediate priority, got %q", targets[0].Priority)
	}
}

func TestRouterNoProvidersReturnsNil(t *testing.T) {
	reg := NewProviderRegistry()
	router := NewADTRouter(reg)
	if got := router.Route("UNKNOWN", EventAdmit); got != nil {
		t.Errorf("expected nil for unknown patient, got %v", got)
	}
}

func TestTrackerRecordsAndRetrieves(t *testing.T) {
	tracker := NewNotificationTracker()

	now := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
	tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR01",
		EventType:      EventDischarge,
		Timestamp:      now,
		DeliveryStatus: StatusSent,
		MessageID:      "MSG001",
		Channel:        "adt-inbound",
	})
	tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR02",
		EventType:      EventDischarge,
		Timestamp:      now.Add(time.Second),
		DeliveryStatus: StatusConfirmed,
		MessageID:      "MSG001",
		Channel:        "adt-inbound",
	})
	tracker.Record(Notification{
		PatientID:      "P002",
		ProviderID:     "DR03",
		EventType:      EventAdmit,
		Timestamp:      now.Add(time.Minute),
		DeliveryStatus: StatusFailed,
		MessageID:      "MSG002",
		Channel:        "adt-inbound",
	})

	// Query for P001 in the window.
	report := tracker.DeliveryReport("P001", now.Add(-time.Hour), now.Add(time.Hour))
	if len(report) != 2 {
		t.Fatalf("expected 2 notifications for P001, got %d", len(report))
	}

	// P002 should not appear.
	report = tracker.DeliveryReport("P002", now.Add(-time.Hour), now.Add(time.Hour))
	if len(report) != 1 {
		t.Fatalf("expected 1 notification for P002, got %d", len(report))
	}
}

func TestTrackerTimeWindowFiltering(t *testing.T) {
	tracker := NewNotificationTracker()

	base := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
	tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR01",
		EventType:      EventAdmit,
		Timestamp:      base,
		DeliveryStatus: StatusSent,
		MessageID:      "MSG001",
		Channel:        "adt",
	})
	tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR01",
		EventType:      EventDischarge,
		Timestamp:      base.Add(48 * time.Hour),
		DeliveryStatus: StatusConfirmed,
		MessageID:      "MSG002",
		Channel:        "adt",
	})

	// Only the first notification should match.
	report := tracker.DeliveryReport("P001", base.Add(-time.Hour), base.Add(time.Hour))
	if len(report) != 1 {
		t.Fatalf("expected 1 notification in narrow window, got %d", len(report))
	}
	if report[0].EventType != EventAdmit {
		t.Errorf("expected admit event, got %s", report[0].EventType)
	}
}

func TestComplianceReportGeneratesCorrectStatistics(t *testing.T) {
	tracker := NewNotificationTracker()
	now := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)

	// Simulate a discharge event with two notifications (one confirmed, one failed).
	tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "DR01",
		EventType:      EventDischarge,
		Timestamp:      now,
		DeliveryStatus: StatusConfirmed,
		MessageID:      "MSG001",
		Channel:        "adt-inbound",
	})
	tracker.Record(Notification{
		PatientID:      "P001",
		ProviderID:     "FAC01",
		EventType:      EventDischarge,
		Timestamp:      now.Add(time.Second),
		DeliveryStatus: StatusFailed,
		MessageID:      "MSG001",
		Channel:        "adt-inbound",
	})

	// An admit event with one sent notification.
	tracker.Record(Notification{
		PatientID:      "P002",
		ProviderID:     "DR02",
		EventType:      EventAdmit,
		Timestamp:      now.Add(5 * time.Minute),
		DeliveryStatus: StatusSent,
		MessageID:      "MSG002",
		Channel:        "adt-inbound",
	})

	cr := NewComplianceReport(tracker)
	report := cr.Generate(now.Add(-time.Hour), now.Add(time.Hour))

	if report.TotalEvents != 2 {
		t.Errorf("TotalEvents = %d, want 2", report.TotalEvents)
	}
	if report.NotificationsSent != 3 {
		t.Errorf("NotificationsSent = %d, want 3", report.NotificationsSent)
	}
	if report.Confirmed != 1 {
		t.Errorf("Confirmed = %d, want 1", report.Confirmed)
	}
	if report.Failed != 1 {
		t.Errorf("Failed = %d, want 1", report.Failed)
	}

	// Check ByEventType.
	discharge, ok := report.ByEventType[EventDischarge]
	if !ok {
		t.Fatal("ByEventType missing discharge")
	}
	if discharge.Total != 2 {
		t.Errorf("discharge.Total = %d, want 2", discharge.Total)
	}
	if discharge.Confirmed != 1 {
		t.Errorf("discharge.Confirmed = %d, want 1", discharge.Confirmed)
	}
	if discharge.Failed != 1 {
		t.Errorf("discharge.Failed = %d, want 1", discharge.Failed)
	}

	admit, ok := report.ByEventType[EventAdmit]
	if !ok {
		t.Fatal("ByEventType missing admit")
	}
	if admit.Total != 1 {
		t.Errorf("admit.Total = %d, want 1", admit.Total)
	}
	if admit.Sent != 1 {
		t.Errorf("admit.Sent = %d, want 1", admit.Sent)
	}
}

func TestConcurrentAccess(t *testing.T) {
	reg := NewProviderRegistry()
	tracker := NewNotificationTracker()

	var wg sync.WaitGroup
	const goroutines = 50

	// Concurrent writes to the registry.
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			pid := fmt.Sprintf("P%03d", i%10)
			provID := fmt.Sprintf("DR%03d", i)
			reg.RegisterProvider(pid, provID, "Dr. "+provID, "https://example.com/"+provID, RolePCP)
		}(i)
	}
	wg.Wait()

	// Concurrent reads from the registry.
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			pid := fmt.Sprintf("P%03d", i%10)
			_ = reg.EstablishedProviders(pid)
		}(i)
	}
	wg.Wait()

	// Concurrent writes to the tracker.
	now := time.Now()
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			tracker.Record(Notification{
				PatientID:      fmt.Sprintf("P%03d", i%10),
				ProviderID:     fmt.Sprintf("DR%03d", i),
				EventType:      EventAdmit,
				Timestamp:      now,
				DeliveryStatus: StatusSent,
				MessageID:      fmt.Sprintf("MSG%03d", i),
				Channel:        "adt",
			})
		}(i)
	}
	wg.Wait()

	// Concurrent reads from the tracker.
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			pid := fmt.Sprintf("P%03d", i%10)
			_ = tracker.DeliveryReport(pid, now.Add(-time.Hour), now.Add(time.Hour))
		}(i)
	}
	wg.Wait()

	// If we get here without the race detector firing, we are thread-safe.
}
