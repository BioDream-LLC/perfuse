package compliance

import (
	"sync"
	"testing"
	"time"
)

func baseTime() time.Time {
	return time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
}

func TestAuditLogRecordAndQuery(t *testing.T) {
	log := NewAuditLog()

	events := []PHIEvent{
		{Timestamp: baseTime(), Channel: "ADT", PatientID: "P001", Direction: "inbound", MessageType: "ADT^A01", DestinationAddr: "10.0.0.1:2575"},
		{Timestamp: baseTime().Add(time.Minute), Channel: "ADT", PatientID: "P002", Direction: "inbound", MessageType: "ADT^A08", DestinationAddr: "10.0.0.1:2575"},
		{Timestamp: baseTime().Add(2 * time.Minute), Channel: "LAB", PatientID: "P001", Direction: "outbound", MessageType: "ORU^R01", DestinationAddr: "10.0.0.2:2575"},
		{Timestamp: baseTime().Add(3 * time.Minute), Channel: "RAD", PatientID: "P003", Direction: "outbound", MessageType: "ORM^O01", DestinationAddr: "10.0.0.3:2575"},
	}
	for _, e := range events {
		log.Record(e)
	}

	if log.Count() != 4 {
		t.Fatalf("expected 4 events, got %d", log.Count())
	}

	// Filter by channel.
	got := log.Query(AuditFilter{Channel: "ADT"})
	if len(got) != 2 {
		t.Errorf("channel filter: expected 2, got %d", len(got))
	}

	// Filter by patient.
	got = log.Query(AuditFilter{PatientID: "P001"})
	if len(got) != 2 {
		t.Errorf("patient filter: expected 2, got %d", len(got))
	}

	// Filter by direction.
	got = log.Query(AuditFilter{Direction: "outbound"})
	if len(got) != 2 {
		t.Errorf("direction filter: expected 2, got %d", len(got))
	}

	// Filter by time range.
	got = log.Query(AuditFilter{
		From: baseTime().Add(90 * time.Second),
		To:   baseTime().Add(4 * time.Minute),
	})
	if len(got) != 2 {
		t.Errorf("time range filter: expected 2, got %d", len(got))
	}

	// Limit.
	got = log.Query(AuditFilter{Limit: 1})
	if len(got) != 1 {
		t.Errorf("limit filter: expected 1, got %d", len(got))
	}

	// Combined filters.
	got = log.Query(AuditFilter{Channel: "ADT", Direction: "inbound", PatientID: "P001"})
	if len(got) != 1 {
		t.Errorf("combined filter: expected 1, got %d", len(got))
	}
}

func TestVolumeSpikeDetection(t *testing.T) {
	baseline := NewBaselineBuilder()

	// Build a baseline: 2 messages per minute is normal at hour 10 for the ADT channel.
	for i := 0; i < 20; i++ {
		ts := baseTime().Add(time.Duration(i) * time.Minute)
		baseline.Observe(PHIEvent{
			Timestamp: ts,
			Channel:   "ADT",
			Direction: "inbound",
		})
		baseline.Observe(PHIEvent{
			Timestamp: ts,
			Channel:   "ADT",
			Direction: "inbound",
		})
	}

	avg := baseline.HourlyAverage("ADT", 10)
	if avg < 1 {
		t.Fatalf("expected positive average, got %f", avg)
	}

	detector := NewAnomalyDetector(AnomalyConfig{
		VolumeThreshold: 10,
	}, baseline)

	// Now simulate a spike: many messages in the same minute.
	spikeTime := baseTime().Add(30 * time.Minute)
	threshold := int(3*avg) + 1
	for i := 0; i < threshold; i++ {
		baseline.Observe(PHIEvent{
			Timestamp: spikeTime,
			Channel:   "ADT",
			Direction: "inbound",
		})
	}

	// The observe call that crosses 3x should fire.
	alerts := detector.Observe(PHIEvent{
		Timestamp: spikeTime,
		Channel:   "ADT",
		Direction: "inbound",
	})

	var found bool
	for _, a := range alerts {
		if a.Description == "Volume spike detected: message rate exceeds 3x rolling average" {
			found = true
			if a.Severity != "warning" {
				t.Errorf("expected warning severity, got %s", a.Severity)
			}
		}
	}
	if !found {
		t.Error("expected volume spike alert to fire")
	}
}

func TestAfterHoursDetection(t *testing.T) {
	baseline := NewBaselineBuilder()

	// Build a baseline with activity only during business hours (9-17).
	for hour := 9; hour < 17; hour++ {
		for i := 0; i < 100; i++ {
			baseline.Observe(PHIEvent{
				Timestamp: time.Date(2026, 8, 25, hour, i%60, 0, 0, time.UTC),
				Channel:   "ADT",
				Direction: "inbound",
			})
		}
	}

	if !baseline.IsAfterHoursChannel("ADT") {
		t.Fatal("expected ADT to be classified as after-hours channel")
	}

	detector := NewAnomalyDetector(AnomalyConfig{
		AfterHoursStart: 18,
		AfterHoursEnd:   6,
	}, baseline)

	// Activity at 3 AM should trigger.
	alerts := detector.Observe(PHIEvent{
		Timestamp: time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC),
		Channel:   "ADT",
		Direction: "inbound",
	})

	var found bool
	for _, a := range alerts {
		if a.Description == "After-hours PHI activity on a daytime-only channel" {
			found = true
			if a.Severity != "warning" {
				t.Errorf("expected warning severity, got %s", a.Severity)
			}
		}
	}
	if !found {
		t.Error("expected after-hours alert to fire")
	}

	// Activity at 10 AM should NOT trigger.
	alerts = detector.Observe(PHIEvent{
		Timestamp: time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
		Channel:   "ADT",
		Direction: "inbound",
	})
	for _, a := range alerts {
		if a.Description == "After-hours PHI activity on a daytime-only channel" {
			t.Error("should not fire after-hours alert during business hours")
		}
	}
}

func TestNewDestinationDetection(t *testing.T) {
	baseline := NewBaselineBuilder()

	// Establish known destinations.
	for i := 0; i < 10; i++ {
		baseline.Observe(PHIEvent{
			Timestamp:       baseTime().Add(time.Duration(i) * time.Minute),
			Channel:         "ADT",
			DestinationAddr: "10.0.0.1:2575",
			Direction:       "outbound",
		})
		baseline.Observe(PHIEvent{
			Timestamp:       baseTime().Add(time.Duration(i) * time.Minute),
			Channel:         "ADT",
			DestinationAddr: "10.0.0.2:2575",
			Direction:       "outbound",
		})
	}

	known := baseline.KnownDestinations("ADT")
	if len(known) != 2 {
		t.Fatalf("expected 2 known destinations, got %d", len(known))
	}

	detector := NewAnomalyDetector(AnomalyConfig{
		NewDestinationAlert: true,
	}, baseline)

	// Sending to a known destination should not alert.
	alerts := detector.Observe(PHIEvent{
		Timestamp:       baseTime().Add(20 * time.Minute),
		Channel:         "ADT",
		DestinationAddr: "10.0.0.1:2575",
		Direction:       "outbound",
	})
	for _, a := range alerts {
		if a.Description == "PHI sent to previously unknown destination" {
			t.Error("should not alert for known destination")
		}
	}

	// Sending to a new destination should alert.
	alerts = detector.Observe(PHIEvent{
		Timestamp:       baseTime().Add(21 * time.Minute),
		Channel:         "ADT",
		DestinationAddr: "192.168.99.99:4444",
		Direction:       "outbound",
	})

	var found bool
	for _, a := range alerts {
		if a.Description == "PHI sent to previously unknown destination" {
			found = true
			if a.Severity != "critical" {
				t.Errorf("expected critical severity, got %s", a.Severity)
			}
			if a.Details["destination"] != "192.168.99.99:4444" {
				t.Errorf("expected destination in details, got %v", a.Details)
			}
		}
	}
	if !found {
		t.Error("expected new destination alert to fire")
	}
}

func TestBulkPatientAccessDetection(t *testing.T) {
	baseline := NewBaselineBuilder()
	detector := NewAnomalyDetector(AnomalyConfig{}, baseline)

	now := baseTime()
	var lastAlerts []Alert

	// 9 distinct patients in rapid succession - should NOT fire (threshold is 10).
	for i := 0; i < 9; i++ {
		lastAlerts = detector.Observe(PHIEvent{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Channel:   "ADT",
			PatientID: "P" + itoa(i),
			SourceIP:  "10.0.0.50",
			Direction: "inbound",
		})
	}
	for _, a := range lastAlerts {
		if a.Description == "Bulk patient access: single source querying many patients rapidly" {
			t.Error("should not fire at 9 distinct patients")
		}
	}

	// 10th distinct patient should fire.
	alerts := detector.Observe(PHIEvent{
		Timestamp: now.Add(9 * time.Second),
		Channel:   "ADT",
		PatientID: "P9",
		SourceIP:  "10.0.0.50",
		Direction: "inbound",
	})

	var found bool
	for _, a := range alerts {
		if a.Description == "Bulk patient access: single source querying many patients rapidly" {
			found = true
			if a.Severity != "critical" {
				t.Errorf("expected critical severity, got %s", a.Severity)
			}
		}
	}
	if !found {
		t.Error("expected bulk patient access alert to fire")
	}
}

func TestBulkPatientAccessWindowExpiry(t *testing.T) {
	baseline := NewBaselineBuilder()
	detector := NewAnomalyDetector(AnomalyConfig{}, baseline)

	now := baseTime()

	// Send 5 patients now.
	for i := 0; i < 5; i++ {
		detector.Observe(PHIEvent{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Channel:   "ADT",
			PatientID: "P" + itoa(i),
			SourceIP:  "10.0.0.50",
			Direction: "inbound",
		})
	}

	// Send 5 more patients 2 minutes later (first batch should have expired).
	later := now.Add(2 * time.Minute)
	var lastAlerts []Alert
	for i := 5; i < 10; i++ {
		lastAlerts = detector.Observe(PHIEvent{
			Timestamp: later.Add(time.Duration(i) * time.Second),
			Channel:   "ADT",
			PatientID: "P" + itoa(i),
			SourceIP:  "10.0.0.50",
			Direction: "inbound",
		})
	}

	for _, a := range lastAlerts {
		if a.Description == "Bulk patient access: single source querying many patients rapidly" {
			t.Error("should not fire when window has expired between batches")
		}
	}
}

func TestComplianceReportGeneration(t *testing.T) {
	log := NewAuditLog()

	start := baseTime()
	end := start.Add(time.Hour)

	events := []PHIEvent{
		{Timestamp: start.Add(time.Minute), Channel: "ADT", PatientID: "P001", Direction: "inbound", DestinationAddr: "10.0.0.1:2575"},
		{Timestamp: start.Add(2 * time.Minute), Channel: "ADT", PatientID: "P002", Direction: "inbound", DestinationAddr: "10.0.0.1:2575"},
		{Timestamp: start.Add(3 * time.Minute), Channel: "LAB", PatientID: "P001", Direction: "outbound", DestinationAddr: "10.0.0.2:2575"},
		{Timestamp: start.Add(4 * time.Minute), Channel: "LAB", PatientID: "P003", Direction: "outbound", DestinationAddr: "10.0.0.2:2575"},
		{Timestamp: start.Add(5 * time.Minute), Channel: "LAB", PatientID: "P003", Direction: "outbound", DestinationAddr: "10.0.0.3:2575"},
	}
	for _, e := range events {
		log.Record(e)
	}

	report := GenerateReport(log, start, end)

	if report.TotalEvents != 5 {
		t.Errorf("expected 5 total events, got %d", report.TotalEvents)
	}
	if report.UniquePatients != 3 {
		t.Errorf("expected 3 unique patients, got %d", report.UniquePatients)
	}
	if len(report.ChannelBreakdown) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(report.ChannelBreakdown))
	}
	// LAB has 3 events, should be first (sorted by count desc).
	if report.ChannelBreakdown[0].Channel != "LAB" {
		t.Errorf("expected LAB first, got %s", report.ChannelBreakdown[0].Channel)
	}
	if report.ChannelBreakdown[0].EventCount != 3 {
		t.Errorf("expected 3 LAB events, got %d", report.ChannelBreakdown[0].EventCount)
	}
	if report.ChannelBreakdown[0].UniquePatients != 2 {
		t.Errorf("expected 2 unique patients for LAB, got %d", report.ChannelBreakdown[0].UniquePatients)
	}

	// ADT has 2 events.
	if report.ChannelBreakdown[1].Channel != "ADT" {
		t.Errorf("expected ADT second, got %s", report.ChannelBreakdown[1].Channel)
	}

	// Top destinations.
	if len(report.TopDestinations) != 3 {
		t.Fatalf("expected 3 destinations, got %d", len(report.TopDestinations))
	}
	// 10.0.0.1:2575 and 10.0.0.2:2575 each have 2, 10.0.0.3:2575 has 1.
	if report.TopDestinations[0].Count != 2 {
		t.Errorf("expected top destination count 2, got %d", report.TopDestinations[0].Count)
	}

	// Period check.
	if report.Period[0] != start || report.Period[1] != end {
		t.Errorf("period mismatch")
	}
}

func TestThreadSafety(t *testing.T) {
	log := NewAuditLog()
	baseline := NewBaselineBuilder()
	detector := NewAnomalyDetector(AnomalyConfig{
		NewDestinationAlert: true,
		AfterHoursStart:     22,
		AfterHoursEnd:       6,
	}, baseline)

	// Seed baseline so detections can fire.
	for i := 0; i < 10; i++ {
		baseline.Observe(PHIEvent{
			Timestamp:       baseTime().Add(time.Duration(i) * time.Minute),
			Channel:         "ADT",
			DestinationAddr: "10.0.0.1:2575",
			Direction:       "outbound",
		})
	}

	var wg sync.WaitGroup
	const goroutines = 10
	const eventsPerGoroutine = 100

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				event := PHIEvent{
					Timestamp:       baseTime().Add(time.Duration(id*eventsPerGoroutine+i) * time.Second),
					Channel:         "ADT",
					PatientID:       "P" + itoa(id),
					Direction:       "inbound",
					SourceIP:        "10.0.0." + itoa(id),
					DestinationAddr: "10.0.0.1:2575",
				}
				log.Record(event)
				baseline.Observe(event)
				detector.Observe(event)
			}
		}(g)
	}
	wg.Wait()

	if log.Count() != goroutines*eventsPerGoroutine {
		t.Errorf("expected %d events, got %d", goroutines*eventsPerGoroutine, log.Count())
	}

	// Query should still work.
	got := log.Query(AuditFilter{Channel: "ADT"})
	if len(got) != goroutines*eventsPerGoroutine {
		t.Errorf("query after concurrent writes: expected %d, got %d", goroutines*eventsPerGoroutine, len(got))
	}

	// Report generation should work on concurrent data.
	report := GenerateReport(log, baseTime(), baseTime().Add(time.Hour))
	if report.TotalEvents != goroutines*eventsPerGoroutine {
		t.Errorf("report total: expected %d, got %d", goroutines*eventsPerGoroutine, report.TotalEvents)
	}
}

func TestBaselineBuilder(t *testing.T) {
	b := NewBaselineBuilder()

	// Feed events at hour 14.
	for i := 0; i < 30; i++ {
		b.Observe(PHIEvent{
			Timestamp:       time.Date(2026, 8, 25, 14, i%60, 0, 0, time.UTC),
			Channel:         "LAB",
			DestinationAddr: "10.0.0.5:2575",
			Direction:       "outbound",
		})
	}

	avg := b.HourlyAverage("LAB", 14)
	if avg <= 0 {
		t.Errorf("expected positive hourly average, got %f", avg)
	}

	// No data at hour 3.
	avg = b.HourlyAverage("LAB", 3)
	if avg != 0 {
		t.Errorf("expected 0 for hour with no data, got %f", avg)
	}

	// Known destinations.
	dests := b.KnownDestinations("LAB")
	if len(dests) != 1 || dests[0] != "10.0.0.5:2575" {
		t.Errorf("expected [10.0.0.5:2575], got %v", dests)
	}

	// Unknown channel.
	dests = b.KnownDestinations("UNKNOWN")
	if dests != nil {
		t.Errorf("expected nil for unknown channel, got %v", dests)
	}
}

// ===== AUDIT TESTS - Proving defects =====

// DEFECT 1: HourlyAverage is always 1.0 because hourlyObservations counts
// events, not distinct time periods. This means the volume spike rule fires
// based on an absolute threshold of 3 per minute, not relative to actual
// traffic patterns. A channel with 1000 messages/minute baseline would still
// only alert at >3 messages/minute in a new minute bucket.
func TestHourlyAverageMeaningful(t *testing.T) {
	b := NewBaselineBuilder()

	// Simulate 10 messages per minute for 5 distinct minutes at hour 14.
	for minute := 0; minute < 5; minute++ {
		for i := 0; i < 10; i++ {
			b.Observe(PHIEvent{
				Timestamp: time.Date(2026, 8, 25, 14, minute, i, 0, time.UTC),
				Channel:   "LABX",
				Direction: "outbound",
			})
		}
	}

	// The average should reflect ~10 messages per minute (or per observation period).
	// If the bug exists, it will return 1.0 (50/50) instead of something meaningful
	// like 10.0 (50/5).
	avg := b.HourlyAverage("LABX", 14)
	if avg <= 1.5 {
		t.Errorf("DEFECT: HourlyAverage returned %f; expected ~10.0 (messages per observation period). "+
			"hourlyObservations counts total events instead of distinct minutes, making average always ~1.0", avg)
	}
}

// DEFECT 2: Volume spike rule uses the broken average (always ~1.0).
// This means a channel with 100 msgs/min baseline will alert at >3 msgs
// in a new minute, which is absurdly sensitive. Conversely, if somehow fixed,
// a channel with 1 msg/min would never alert because 3*1=3 and with > (not >=)
// you need 4 msgs. Test that volume spike is relative to ACTUAL traffic.
func TestVolumeSpikeRelativeToActualTraffic(t *testing.T) {
	baseline := NewBaselineBuilder()

	// Build a high-volume baseline: 50 messages per minute for 10 minutes at hour 10.
	for minute := 0; minute < 10; minute++ {
		for i := 0; i < 50; i++ {
			baseline.Observe(PHIEvent{
				Timestamp: time.Date(2026, 8, 25, 10, minute, i, 0, time.UTC),
				Channel:   "HIGH_VOL",
				Direction: "inbound",
			})
		}
	}

	detector := NewAnomalyDetector(AnomalyConfig{
		VolumeThreshold: 10,
	}, baseline)

	// Now send 60 messages in a new minute - this is only slightly above normal (50/min).
	// It should NOT fire because 60 < 3*50 = 150.
	spikeTime := time.Date(2026, 8, 25, 10, 20, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		baseline.Observe(PHIEvent{
			Timestamp: spikeTime,
			Channel:   "HIGH_VOL",
			Direction: "inbound",
		})
	}

	alerts := detector.Observe(PHIEvent{
		Timestamp: spikeTime,
		Channel:   "HIGH_VOL",
		Direction: "inbound",
	})

	for _, a := range alerts {
		if a.Description == "Volume spike detected: message rate exceeds 3x rolling average" {
			t.Errorf("DEFECT: Volume spike fired at 61 msgs/min on a channel with 50 msgs/min baseline. " +
				"The rule should only fire at >150 msgs/min (3x baseline). " +
				"This proves the average is broken (returns ~1.0 instead of ~50.0)")
		}
	}
}

// DEFECT 3: Concurrency race - AnomalyDetector.Observe holds d.mu.Lock() and
// then acquires d.baseline.mu.RLock(). If baseline.Observe is called concurrently
// (holding baseline.mu.Lock()), and another goroutine is in detector.Observe
// (holding d.mu.Lock() waiting for baseline.mu.RLock()), we have potential
// issues. More importantly, the test with 50+ goroutines doing concurrent
// writes and reads with -race should catch any actual data race.
func TestConcurrency50Goroutines(t *testing.T) {
	log := NewAuditLog()
	baseline := NewBaselineBuilder()
	detector := NewAnomalyDetector(AnomalyConfig{
		NewDestinationAlert: true,
		AfterHoursStart:     22,
		AfterHoursEnd:       6,
	}, baseline)

	// Seed baseline.
	for i := 0; i < 20; i++ {
		baseline.Observe(PHIEvent{
			Timestamp:       baseTime().Add(time.Duration(i) * time.Minute),
			Channel:         "ADT",
			DestinationAddr: "10.0.0.1:2575",
			Direction:       "outbound",
		})
	}

	var wg sync.WaitGroup
	const goroutines = 50
	const eventsPerGoroutine = 50

	// Writer goroutines.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				event := PHIEvent{
					Timestamp:       baseTime().Add(time.Duration(id*eventsPerGoroutine+i) * time.Second),
					Channel:         "ADT",
					PatientID:       "P" + itoa(id*100+i),
					Direction:       "inbound",
					SourceIP:        "10.0.0." + itoa(id),
					DestinationAddr: "10.0.0.1:2575",
				}
				log.Record(event)
				baseline.Observe(event)
				detector.Observe(event)
			}
		}(g)
	}

	// Reader goroutine that queries while writes are happening.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			log.Query(AuditFilter{Channel: "ADT"})
			log.Count()
			baseline.HourlyAverage("ADT", 10)
			baseline.KnownDestinations("ADT")
			baseline.IsAfterHoursChannel("ADT")
		}
	}()

	wg.Wait()

	total := log.Count()
	if total != goroutines*eventsPerGoroutine {
		t.Errorf("expected %d events, got %d", goroutines*eventsPerGoroutine, total)
	}
}

// DEFECT 4: Timestamps are not enforced as UTC. The audit log stores whatever
// time.Time is passed in. If events arrive with different timezone locations,
// the Hour() calls in anomaly detection will use the event's local time, which
// is correct per-event but means the after-hours rule gives different results
// for the same physical instant depending on how the timestamp was constructed.
// This is a design issue more than a code bug, but verifiable.
func TestAfterHoursTimezoneAmbiguity(t *testing.T) {
	baseline := NewBaselineBuilder()

	// Build daytime-only baseline in UTC.
	for hour := 9; hour < 17; hour++ {
		for i := 0; i < 100; i++ {
			baseline.Observe(PHIEvent{
				Timestamp: time.Date(2026, 8, 25, hour, i%60, 0, 0, time.UTC),
				Channel:   "TZ_TEST",
				Direction: "inbound",
			})
		}
	}

	detector := NewAnomalyDetector(AnomalyConfig{
		AfterHoursStart: 18,
		AfterHoursEnd:   6,
	}, baseline)

	// 3 AM UTC should trigger.
	alerts := detector.Observe(PHIEvent{
		Timestamp: time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC),
		Channel:   "TZ_TEST",
		Direction: "inbound",
	})
	var found bool
	for _, a := range alerts {
		if a.Description == "After-hours PHI activity on a daytime-only channel" {
			found = true
		}
	}
	if !found {
		t.Error("expected after-hours alert for 3 AM UTC event")
	}

	// Same physical instant expressed in US Central (CDT = UTC-5) is 10 PM previous day.
	// hour=22 which IS after hours. So this should also fire.
	cdt := time.FixedZone("CDT", -5*3600)
	alerts = detector.Observe(PHIEvent{
		Timestamp: time.Date(2026, 8, 25, 22, 0, 0, 0, cdt), // same instant as 3AM UTC Aug 26
		Channel:   "TZ_TEST",
		Direction: "inbound",
	})
	found = false
	for _, a := range alerts {
		if a.Description == "After-hours PHI activity on a daytime-only channel" {
			found = true
		}
	}
	if !found {
		t.Error("after-hours detection is timezone-sensitive: same physical instant in CDT (hour=22) should also fire")
	}
	// Note: This test should PASS since hour=22 >= 18 (start). But the baseline was
	// built in UTC, so IsAfterHoursChannel uses UTC hours. The event at hour 22 CDT
	// won't match the baseline's hour distribution correctly if baseline uses UTC hours.
	// This is the timezone ambiguity issue.
}

// Test that after-hours with start=20 end=6 using AND logic would fail.
// The current implementation uses OR for wraparound, which is correct.
// This test verifies the OR logic handles the wraparound case.
func TestAfterHoursWraparound(t *testing.T) {
	baseline := NewBaselineBuilder()

	// Build a strong daytime-only baseline.
	for hour := 8; hour < 18; hour++ {
		for i := 0; i < 200; i++ {
			baseline.Observe(PHIEvent{
				Timestamp: time.Date(2026, 8, 25, hour, i%60, 0, 0, time.UTC),
				Channel:   "WRAP_TEST",
				Direction: "inbound",
			})
		}
	}

	detector := NewAnomalyDetector(AnomalyConfig{
		AfterHoursStart: 20,
		AfterHoursEnd:   6,
	}, baseline)

	// Hour 21 should be after hours (>= 20).
	alerts := detector.Observe(PHIEvent{
		Timestamp: time.Date(2026, 8, 25, 21, 0, 0, 0, time.UTC),
		Channel:   "WRAP_TEST",
		Direction: "inbound",
	})
	var found bool
	for _, a := range alerts {
		if a.Description == "After-hours PHI activity on a daytime-only channel" {
			found = true
		}
	}
	if !found {
		t.Error("hour 21 should be detected as after-hours with start=20, end=6")
	}

	// Hour 4 should be after hours (< 6).
	alerts = detector.Observe(PHIEvent{
		Timestamp: time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC),
		Channel:   "WRAP_TEST",
		Direction: "inbound",
	})
	found = false
	for _, a := range alerts {
		if a.Description == "After-hours PHI activity on a daytime-only channel" {
			found = true
		}
	}
	if !found {
		t.Error("hour 4 should be detected as after-hours with start=20, end=6")
	}

	// Hour 10 should NOT be after hours.
	alerts = detector.Observe(PHIEvent{
		Timestamp: time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
		Channel:   "WRAP_TEST",
		Direction: "inbound",
	})
	for _, a := range alerts {
		if a.Description == "After-hours PHI activity on a daytime-only channel" {
			t.Error("hour 10 should NOT be after-hours with start=20, end=6")
		}
	}
}

// Test that volume spike does NOT fire on normal traffic (negative test).
func TestVolumeSpikeDoesNotFireOnNormalTraffic(t *testing.T) {
	baseline := NewBaselineBuilder()

	// Build baseline: 5 messages per minute for 10 minutes at hour 10.
	for minute := 0; minute < 10; minute++ {
		for i := 0; i < 5; i++ {
			baseline.Observe(PHIEvent{
				Timestamp: time.Date(2026, 8, 25, 10, minute, i*10, 0, time.UTC),
				Channel:   "NORMAL",
				Direction: "inbound",
			})
		}
	}

	detector := NewAnomalyDetector(AnomalyConfig{
		VolumeThreshold: 10,
	}, baseline)

	// Send a normal amount (2 messages) in a new minute - should NOT fire.
	normalTime := time.Date(2026, 8, 25, 10, 15, 0, 0, time.UTC)
	baseline.Observe(PHIEvent{
		Timestamp: normalTime,
		Channel:   "NORMAL",
		Direction: "inbound",
	})
	baseline.Observe(PHIEvent{
		Timestamp: normalTime,
		Channel:   "NORMAL",
		Direction: "inbound",
	})

	alerts := detector.Observe(PHIEvent{
		Timestamp: normalTime,
		Channel:   "NORMAL",
		Direction: "inbound",
	})
	for _, a := range alerts {
		if a.Description == "Volume spike detected: message rate exceeds 3x rolling average" {
			t.Error("volume spike should not fire on normal traffic levels")
		}
	}
}

// Test that bulk access rule does NOT fire when same patient is accessed
// repeatedly (not distinct patients).
func TestBulkAccessSamePatientNoFire(t *testing.T) {
	baseline := NewBaselineBuilder()
	detector := NewAnomalyDetector(AnomalyConfig{}, baseline)

	now := baseTime()

	// Access the SAME patient 20 times rapidly - should NOT fire.
	var lastAlerts []Alert
	for i := 0; i < 20; i++ {
		lastAlerts = detector.Observe(PHIEvent{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Channel:   "ADT",
			PatientID: "P_SAME",
			SourceIP:  "10.0.0.50",
			Direction: "inbound",
		})
	}
	for _, a := range lastAlerts {
		if a.Description == "Bulk patient access: single source querying many patients rapidly" {
			t.Error("bulk access rule should not fire when accessing same patient repeatedly")
		}
	}
}

// Test new destination does NOT fire when NewDestinationAlert is disabled.
func TestNewDestinationDisabled(t *testing.T) {
	baseline := NewBaselineBuilder()
	baseline.Observe(PHIEvent{
		Timestamp:       baseTime(),
		Channel:         "ADT",
		DestinationAddr: "10.0.0.1:2575",
		Direction:       "outbound",
	})

	detector := NewAnomalyDetector(AnomalyConfig{
		NewDestinationAlert: false, // disabled
	}, baseline)

	alerts := detector.Observe(PHIEvent{
		Timestamp:       baseTime().Add(time.Minute),
		Channel:         "ADT",
		DestinationAddr: "192.168.99.99:9999",
		Direction:       "outbound",
	})
	for _, a := range alerts {
		if a.Description == "PHI sent to previously unknown destination" {
			t.Error("new destination alert should not fire when disabled")
		}
	}
}
