// Package compliance provides HIPAA-compliant audit logging and real-time
// anomaly detection for protected health information (PHI) movement.
//
// Integration engines move PHI between systems constantly—ADT feeds, lab
// results, radiology orders—and most have terrible audit trails. When a breach
// happens, the first question is always "what moved where and when?" and the
// answer is always "we'll get back to you in six weeks."
//
// This package builds that answer in real time. Every PHI event is recorded in
// an append-only log, a baseline builder learns normal traffic patterns, and an
// anomaly detector fires alerts when something deviates from the norm: volume
// spikes, after-hours activity on daytime-only channels, data flowing to new
// destinations, or bulk patient access patterns.
package compliance

import (
	"sort"
	"sync"
	"time"
)

// PHIEvent represents a single movement of protected health information
// through the integration engine.
type PHIEvent struct {
	Timestamp       time.Time
	Channel         string
	Destination     string
	PatientID       string
	Direction       string // "inbound" or "outbound"
	MessageType     string // HL7 message type, e.g. "ADT^A01", "ORU^R01"
	ByteCount       int
	SourceIP        string
	DestinationAddr string
}

// AuditFilter controls which events are returned from a query.
type AuditFilter struct {
	Channel   string
	PatientID string
	From      time.Time
	To        time.Time
	Direction string
	Limit     int
}

// AuditLog is an append-only log of all PHI movement through the engine.
// It is safe for concurrent use.
type AuditLog struct {
	mu     sync.RWMutex
	events []PHIEvent
}

// NewAuditLog creates a new empty audit log.
func NewAuditLog() *AuditLog {
	return &AuditLog{}
}

// Record appends a PHI event to the log.
func (l *AuditLog) Record(event PHIEvent) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

// Query returns events matching the given filter.
func (l *AuditLog) Query(filter AuditFilter) []PHIEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var results []PHIEvent
	for _, e := range l.events {
		if filter.Channel != "" && e.Channel != filter.Channel {
			continue
		}
		if filter.PatientID != "" && e.PatientID != filter.PatientID {
			continue
		}
		if !filter.From.IsZero() && e.Timestamp.Before(filter.From) {
			continue
		}
		if !filter.To.IsZero() && e.Timestamp.After(filter.To) {
			continue
		}
		if filter.Direction != "" && e.Direction != filter.Direction {
			continue
		}
		results = append(results, e)
		if filter.Limit > 0 && len(results) >= filter.Limit {
			break
		}
	}
	return results
}

// Count returns the total number of events in the log.
func (l *AuditLog) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.events)
}

// Alert represents a detected anomaly in PHI access patterns.
type Alert struct {
	Severity    string // "info", "warning", "critical"
	Description string
	Timestamp   time.Time
	Channel     string
	Details     map[string]string
}

// AnomalyConfig controls what the anomaly detector watches for.
type AnomalyConfig struct {
	VolumeThreshold     int  // messages per minute above normal to trigger
	AfterHoursStart     int  // hour (0-23) when after-hours begins
	AfterHoursEnd       int  // hour (0-23) when after-hours ends
	NewDestinationAlert bool // whether to fire on unknown destinations
}

// AnomalyDetector performs real-time anomaly detection on PHI events.
// It is safe for concurrent use.
type AnomalyDetector struct {
	mu       sync.Mutex
	config   AnomalyConfig
	baseline *BaselineBuilder

	// For bulk patient access detection: track recent patient IDs per source.
	recentAccess map[string]*accessWindow
}

// accessWindow tracks distinct patient IDs accessed by a source within a
// sliding window.
type accessWindow struct {
	entries []accessEntry
}

type accessEntry struct {
	patientID string
	at        time.Time
}

// NewAnomalyDetector creates an anomaly detector with the given config and
// baseline. The baseline is used to determine normal traffic patterns.
func NewAnomalyDetector(config AnomalyConfig, baseline *BaselineBuilder) *AnomalyDetector {
	return &AnomalyDetector{
		config:       config,
		baseline:     baseline,
		recentAccess: make(map[string]*accessWindow),
	}
}

// Observe processes a PHI event and returns any alerts it triggers.
func (d *AnomalyDetector) Observe(event PHIEvent) []Alert {
	d.mu.Lock()
	defer d.mu.Unlock()

	var alerts []Alert

	// Rule 1: Volume spike - >3x rolling average for this channel/hour.
	if d.baseline != nil {
		avg := d.baseline.HourlyAverage(event.Channel, event.Timestamp.Hour())
		if avg > 0 {
			// Count recent events in this minute for this channel/hour.
			d.baseline.mu.RLock()
			minuteKey := event.Channel + "|" + event.Timestamp.Truncate(time.Minute).Format(time.RFC3339)
			count := d.baseline.minuteCounts[minuteKey]
			d.baseline.mu.RUnlock()

			if float64(count) > 3*avg {
				alerts = append(alerts, Alert{
					Severity:    "warning",
					Description: "Volume spike detected: message rate exceeds 3x rolling average",
					Timestamp:   event.Timestamp,
					Channel:     event.Channel,
					Details: map[string]string{
						"current_rate":    itoa(count),
						"rolling_average": ftoa(avg),
					},
				})
			}
		}
	}

	// Rule 2: After-hours activity.
	if d.baseline != nil && d.baseline.IsAfterHoursChannel(event.Channel) {
		hour := event.Timestamp.Hour()
		if d.isAfterHours(hour) {
			alerts = append(alerts, Alert{
				Severity:    "warning",
				Description: "After-hours PHI activity on a daytime-only channel",
				Timestamp:   event.Timestamp,
				Channel:     event.Channel,
				Details: map[string]string{
					"hour": itoa(hour),
				},
			})
		}
	}

	// Rule 3: New destination.
	if d.config.NewDestinationAlert && d.baseline != nil && event.DestinationAddr != "" {
		known := d.baseline.KnownDestinations(event.Channel)
		if len(known) > 0 && !contains(known, event.DestinationAddr) {
			alerts = append(alerts, Alert{
				Severity:    "critical",
				Description: "PHI sent to previously unknown destination",
				Timestamp:   event.Timestamp,
				Channel:     event.Channel,
				Details: map[string]string{
					"destination": event.DestinationAddr,
				},
			})
		}
	}

	// Rule 4: Bulk patient access - same source querying many different
	// patient IDs rapidly (10+ distinct patients in 1 minute).
	if event.PatientID != "" && event.SourceIP != "" {
		w := d.recentAccess[event.SourceIP]
		if w == nil {
			w = &accessWindow{}
			d.recentAccess[event.SourceIP] = w
		}

		// Evict entries older than 1 minute.
		cutoff := event.Timestamp.Add(-1 * time.Minute)
		fresh := w.entries[:0]
		for _, e := range w.entries {
			if !e.at.Before(cutoff) {
				fresh = append(fresh, e)
			}
		}
		w.entries = fresh

		// Add current.
		w.entries = append(w.entries, accessEntry{patientID: event.PatientID, at: event.Timestamp})

		// Count distinct patient IDs.
		seen := make(map[string]struct{})
		for _, e := range w.entries {
			seen[e.patientID] = struct{}{}
		}
		if len(seen) >= 10 {
			alerts = append(alerts, Alert{
				Severity:    "critical",
				Description: "Bulk patient access: single source querying many patients rapidly",
				Timestamp:   event.Timestamp,
				Channel:     event.Channel,
				Details: map[string]string{
					"source_ip":         event.SourceIP,
					"distinct_patients": itoa(len(seen)),
					"window":            "1m",
				},
			})
		}
	}

	return alerts
}

func (d *AnomalyDetector) isAfterHours(hour int) bool {
	start := d.config.AfterHoursStart
	end := d.config.AfterHoursEnd
	if start <= end {
		// e.g. start=18, end=6 does NOT apply here
		// e.g. start=0, end=6 means 0-6 is after hours
		return hour >= start && hour < end
	}
	// Wraps midnight: e.g. start=18, end=6 means 18-23 or 0-5 is after hours.
	return hour >= start || hour < end
}

// BaselineBuilder learns normal traffic patterns for each channel.
// It is safe for concurrent use.
type BaselineBuilder struct {
	mu sync.RWMutex

	// hourlyTotals[channel][hour] = total message count observed in that hour slot.
	hourlyTotals map[string][24]int
	// hourlyObservations[channel][hour] = number of distinct minutes observed.
	hourlyObservations map[string][24]int

	// minuteCounts tracks per-minute counts keyed by "channel|minute".
	minuteCounts map[string]int

	// destinations[channel] = set of known destination addresses.
	destinations map[string]map[string]struct{}

	// afterHoursActivity[channel][hour] = count of events observed.
	// A channel is considered "after hours" if it has significantly less
	// traffic during configured off-hours.
	hourActivity map[string][24]int
}

// NewBaselineBuilder creates a new baseline builder.
func NewBaselineBuilder() *BaselineBuilder {
	return &BaselineBuilder{
		hourlyTotals:       make(map[string][24]int),
		hourlyObservations: make(map[string][24]int),
		minuteCounts:       make(map[string]int),
		destinations:       make(map[string]map[string]struct{}),
		hourActivity:       make(map[string][24]int),
	}
}

// Observe feeds an event into the baseline to learn normal patterns.
func (b *BaselineBuilder) Observe(event PHIEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	hour := event.Timestamp.Hour()

	// Track hourly totals.
	totals := b.hourlyTotals[event.Channel]
	totals[hour]++
	b.hourlyTotals[event.Channel] = totals

	// Track minute-level counts.
	minuteKey := event.Channel + "|" + event.Timestamp.Truncate(time.Minute).Format(time.RFC3339)
	b.minuteCounts[minuteKey]++

	// Track observations (distinct minutes) for averaging.
	// Only increment when this is the first event in a new minute bucket.
	if b.minuteCounts[minuteKey] == 1 {
		obs := b.hourlyObservations[event.Channel]
		obs[hour]++
		b.hourlyObservations[event.Channel] = obs
	}

	// Track destinations.
	if event.DestinationAddr != "" {
		dests := b.destinations[event.Channel]
		if dests == nil {
			dests = make(map[string]struct{})
			b.destinations[event.Channel] = dests
		}
		dests[event.DestinationAddr] = struct{}{}
	}

	// Track hour activity for after-hours determination.
	activity := b.hourActivity[event.Channel]
	activity[hour]++
	b.hourActivity[event.Channel] = activity
}

// HourlyAverage returns the average messages per minute for a channel at a
// given hour of day.
func (b *BaselineBuilder) HourlyAverage(channel string, hour int) float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()

	obs := b.hourlyObservations[channel]
	if obs[hour] == 0 {
		return 0
	}
	totals := b.hourlyTotals[channel]
	return float64(totals[hour]) / float64(obs[hour])
}

// KnownDestinations returns the list of destinations previously seen for a channel.
func (b *BaselineBuilder) KnownDestinations(channel string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	dests := b.destinations[channel]
	if len(dests) == 0 {
		return nil
	}
	result := make([]string, 0, len(dests))
	for d := range dests {
		result = append(result, d)
	}
	sort.Strings(result)
	return result
}

// IsAfterHoursChannel reports whether a channel is primarily active during
// business hours (and thus after-hours activity is unusual). A channel is
// considered daytime-only if it has at least 10x more activity during business
// hours (8-17) than during off-hours (0-7, 18-23).
func (b *BaselineBuilder) IsAfterHoursChannel(channel string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	activity, ok := b.hourActivity[channel]
	if !ok {
		return false
	}

	var businessTotal, offTotal int
	for h := 0; h < 24; h++ {
		if h >= 8 && h < 18 {
			businessTotal += activity[h]
		} else {
			offTotal += activity[h]
		}
	}

	if offTotal == 0 && businessTotal > 0 {
		return true
	}
	if offTotal == 0 {
		return false
	}
	return float64(businessTotal)/float64(offTotal) >= 10
}

// PHIComplianceReport is a summary of PHI movement over a time period,
// suitable for HIPAA compliance audits.
type PHIComplianceReport struct {
	Period           [2]time.Time
	TotalEvents      int
	UniquePatients   int
	ChannelBreakdown []ChannelSummary
	Anomalies        []Alert
	TopDestinations  []DestCount
}

// ChannelSummary summarizes PHI activity for a single channel.
type ChannelSummary struct {
	Channel        string
	EventCount     int
	UniquePatients int
	LastEvent      time.Time
}

// DestCount pairs a destination with a count of events sent to it.
type DestCount struct {
	Destination string
	Count       int
}

// GenerateReport produces a compliance report for the given time period.
func GenerateReport(log *AuditLog, from, to time.Time) *PHIComplianceReport {
	events := log.Query(AuditFilter{From: from, To: to})

	report := &PHIComplianceReport{
		Period:      [2]time.Time{from, to},
		TotalEvents: len(events),
	}

	patients := make(map[string]struct{})
	channels := make(map[string]*channelAccum)
	destinations := make(map[string]int)

	for _, e := range events {
		if e.PatientID != "" {
			patients[e.PatientID] = struct{}{}
		}

		ch := channels[e.Channel]
		if ch == nil {
			ch = &channelAccum{patients: make(map[string]struct{})}
			channels[e.Channel] = ch
		}
		ch.count++
		if e.PatientID != "" {
			ch.patients[e.PatientID] = struct{}{}
		}
		if e.Timestamp.After(ch.lastEvent) {
			ch.lastEvent = e.Timestamp
		}

		if e.DestinationAddr != "" {
			destinations[e.DestinationAddr]++
		}
	}

	report.UniquePatients = len(patients)

	// Channel breakdown.
	for name, accum := range channels {
		report.ChannelBreakdown = append(report.ChannelBreakdown, ChannelSummary{
			Channel:        name,
			EventCount:     accum.count,
			UniquePatients: len(accum.patients),
			LastEvent:      accum.lastEvent,
		})
	}
	sort.Slice(report.ChannelBreakdown, func(i, j int) bool {
		return report.ChannelBreakdown[i].EventCount > report.ChannelBreakdown[j].EventCount
	})

	// Top destinations.
	for dest, count := range destinations {
		report.TopDestinations = append(report.TopDestinations, DestCount{
			Destination: dest,
			Count:       count,
		})
	}
	sort.Slice(report.TopDestinations, func(i, j int) bool {
		return report.TopDestinations[i].Count > report.TopDestinations[j].Count
	})

	return report
}

// SetAnomalies attaches detected anomalies to a compliance report.
func (r *PHIComplianceReport) SetAnomalies(alerts []Alert) {
	r.Anomalies = alerts
}

type channelAccum struct {
	count     int
	patients  map[string]struct{}
	lastEvent time.Time
}

// Helper functions.

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa(f float64) string {
	// Simple float formatting - integer part + 2 decimal places.
	neg := f < 0
	if neg {
		f = -f
	}
	intPart := int(f)
	frac := int((f - float64(intPart)) * 100)
	s := itoa(intPart) + "."
	if frac < 10 {
		s += "0"
	}
	s += itoa(frac)
	if neg {
		return "-" + s
	}
	return s
}
