package metrics

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The built-in metrics and the Prometheus exposition live here.
//
// Names follow Prometheus convention - lower case, underscore separated, a unit
// suffix - because the whole point of exposing them is that existing monitoring
// can scrape them without a translation layer. A hospital that already runs
// Prometheus and Grafana should not have to treat this as a special case.

// Metric names, as constants so that a recording site and a dashboard cannot
// disagree about spelling.
const (
	MessagesReceived  = "perfuse_messages_received_total"
	MessagesDelivered = "perfuse_messages_delivered_total"
	MessagesFiltered  = "perfuse_messages_filtered_total"

	// DatabaseQuarantined counts rows a database source gave up on.
	//
	// The most important number this connector produces. Anything above zero means a
	// row was abandoned, and the alternative design would have stalled the whole feed
	// on it instead - so this metric is the visible cost of not stalling, and it needs
	// a person rather than a dashboard.
	DatabaseQuarantined = "perfuse_database_rows_quarantined_total"

	// DatabaseRowsRead counts rows a database source has polled.
	DatabaseRowsRead = "perfuse_database_rows_read_total"

	// DatabasePollFailures counts polls that could not run at all.
	DatabasePollFailures = "perfuse_database_poll_failures_total"

	// SFTPFilesRead counts files collected from an SFTP server.
	SFTPFilesRead = "perfuse_sftp_files_read_total"

	// SFTPFilesFailed counts files that could not be processed.
	//
	// Like the database quarantine, any increase needs a person: the file has been set
	// aside, its messages were never delivered, and nothing else will mention it.
	SFTPFilesFailed = "perfuse_sftp_files_failed_total"

	// SFTPFilesWaiting is how many files are present but not yet settled.
	//
	// A gauge rather than a counter, and worth watching: a number that climbs and
	// never falls means files are arriving faster than they settle, or something is
	// writing a file it never finishes.
	SFTPFilesWaiting = "perfuse_sftp_files_waiting"

	// SFTPPollFailures counts polls that could not run.
	SFTPPollFailures    = "perfuse_sftp_poll_failures_total"
	MessagesFailed      = "perfuse_messages_failed_total"
	MessagesUnparseable = "perfuse_messages_unparseable_total"
	MessageBytes        = "perfuse_message_bytes_total"

	MessageDuration  = "perfuse_message_duration_seconds"
	DeliveryDuration = "perfuse_delivery_duration_seconds"
	DeliveryAttempts = "perfuse_delivery_attempts_total"
	DeliveryFailures = "perfuse_delivery_failures_total"

	ScriptDuration = "perfuse_script_duration_seconds"
	ScriptErrors   = "perfuse_script_errors_total"
	ScriptTimeouts = "perfuse_script_timeouts_total"

	TransformSteps  = "perfuse_transform_steps_applied_total"
	TransformErrors = "perfuse_transform_errors_total"

	FHIRResources = "perfuse_fhir_resources_total"
	FHIRErrors    = "perfuse_fhir_validation_errors_total"

	ChannelsRunning     = "perfuse_channels_running"
	ChannelsTotal       = "perfuse_channels_total"
	ChannelsBroken      = "perfuse_channels_broken"
	ConnectionsOpen     = "perfuse_connections_open"
	QueuedTotal         = "perfuse_queue_messages_total"
	QueueDrainedTotal   = "perfuse_queue_drained_total"
	QueueRetriesTotal   = "perfuse_queue_retries_total"
	QueueAbandonedTotal = "perfuse_queue_abandoned_total"
	QueueWaitSeconds    = "perfuse_queue_wait_seconds"
	QueueOldestSeconds  = "perfuse_queue_oldest_seconds"
	QueueFailedDepth    = "perfuse_queue_failed"

	QueueDepth = "perfuse_queue_depth"

	// Delivery admission. Published because a limit nobody can see is a limit somebody will eventually
	// blame for something else: a queue filling up looks the same whether the receiver is slow or the engine
	// is holding back, and these are what tell them apart.
	DeliveriesInFlight = "perfuse_deliveries_in_flight"
	DeliveriesWaiting  = "perfuse_deliveries_waiting"
	DeliveriesRefused  = "perfuse_deliveries_refused_total"
	DeliveryLimit      = "perfuse_delivery_limit"

	GoRoutines = "perfuse_goroutines"
	HeapBytes  = "perfuse_heap_bytes"
	UptimeSecs = "perfuse_uptime_seconds"
)

func (c *Collector) registerBuiltins() {
	for _, def := range []Definition{
		{MessagesReceived, KindCounter, UnitCount, "Messages accepted from a source. On a delimited channel this counts rows, not files, because one document becomes one message per row.", []string{"tenant", "channel"}},
		{MessagesDelivered, KindCounter, UnitCount, "Messages delivered to every destination.", []string{"tenant", "channel"}},
		{MessagesFiltered, KindCounter, UnitCount, "Messages a filter declined.", []string{"tenant", "channel"}},
		{DatabaseQuarantined, KindCounter, UnitCount, "Rows a database source abandoned after failing every attempt. Alert on any increase.", []string{"tenant", "channel"}},
		{DatabaseRowsRead, KindCounter, UnitCount, "Rows read from a database source.", []string{"tenant", "channel"}},
		{DatabasePollFailures, KindCounter, UnitCount, "Database polls that could not run.", []string{"tenant", "channel"}},
		{SFTPFilesRead, KindCounter, UnitCount, "Files collected from an SFTP server.", []string{"tenant", "channel"}},
		{SFTPFilesFailed, KindCounter, UnitCount, "Files set aside because they could not be processed. Alert on any increase.", []string{"tenant", "channel"}},
		{SFTPFilesWaiting, KindGauge, UnitCount, "Files present but not yet settled enough to read.", []string{"tenant", "channel"}},
		{SFTPPollFailures, KindCounter, UnitCount, "SFTP polls that could not run.", []string{"tenant", "channel"}},
		{MessagesFailed, KindCounter, UnitCount, "Messages that could not be delivered.", []string{"tenant", "channel"}},
		{MessagesUnparseable, KindCounter, UnitCount, "Messages that were not valid HL7.", []string{"tenant", "channel"}},
		{MessageBytes, KindCounter, UnitBytes, "Bytes received.", []string{"tenant", "channel"}},

		{MessageDuration, KindHistogram, UnitSeconds, "Time to handle one message end to end.", []string{"tenant", "channel"}},
		{DeliveryDuration, KindHistogram, UnitSeconds, "Time for one delivery attempt.", []string{"tenant", "channel", "destination"}},
		{DeliveryAttempts, KindCounter, UnitCount, "Delivery attempts, including retries.", []string{"tenant", "channel", "destination"}},
		{DeliveryFailures, KindCounter, UnitCount, "Delivery attempts that failed.", []string{"tenant", "channel", "destination"}},

		{ScriptDuration, KindHistogram, UnitSeconds, "Time spent in a script.", []string{"tenant", "channel", "kind"}},
		{ScriptErrors, KindCounter, UnitCount, "Scripts that threw.", []string{"tenant", "channel", "kind"}},
		{ScriptTimeouts, KindCounter, UnitCount, "Scripts stopped for running too long.", []string{"tenant", "channel", "kind"}},

		{TransformSteps, KindCounter, UnitCount, "Declarative steps that changed something.", []string{"tenant", "channel"}},
		{TransformErrors, KindCounter, UnitCount, "Transformations that failed.", []string{"tenant", "channel"}},

		{FHIRResources, KindCounter, UnitCount, "FHIR resources produced.", []string{"tenant", "channel", "type"}},
		{FHIRErrors, KindCounter, UnitCount, "FHIR validation errors.", []string{"tenant", "channel"}},

		{ChannelsRunning, KindGauge, UnitCount, "Channels currently running.", nil},
		{ChannelsTotal, KindGauge, UnitCount, "Channels configured.", nil},
		// Worth a metric of its own, because a file that will not load produces no traffic and no errors - it produces
		// nothing at all, which looks identical to a quiet feed. Something has to be non-zero for anyone to notice.
		{ChannelsBroken, KindGauge, UnitCount, "Channel files that could not be loaded.", nil},
		{ConnectionsOpen, KindGauge, UnitCount, "Open inbound connections.", []string{"tenant", "channel"}},
		{DeliveriesInFlight, KindGauge, UnitCount,
			"Deliveries being attempted right now, across every channel.", nil},
		{DeliveriesWaiting, KindGauge, UnitCount,
			"Deliveries waiting for a slot because a destination or the process is at its limit. " +
				"Persistently above zero means a receiver is not keeping up with what is aimed at it.", nil},
		{DeliveriesRefused, KindCounter, UnitCount,
			"Deliveries that waited for a slot and did not get one, so they were queued or reported as " +
				"failed. Zero on a healthy server.", nil},
		{DeliveryLimit, KindGauge, UnitCount,
			"How many deliveries may be in flight, derived at startup from the file descriptor limit.",
			[]string{"scope"}},

		{QueueDepth, KindGauge, UnitCount, "Messages waiting to be delivered.", []string{"tenant", "channel", "destination"}},
		{QueueFailedDepth, KindGauge, UnitCount, "Queued messages that ran out of attempts.", []string{"tenant", "channel", "destination"}},
		// Age, not just depth. A queue of four hundred that is draining is healthy;
		// a queue of two that has not moved since Tuesday is not, and only this
		// number tells them apart.
		{QueueOldestSeconds, KindGauge, UnitSeconds, "Age of the oldest message still waiting.", []string{"tenant", "channel", "destination"}},
		{QueuedTotal, KindCounter, UnitCount, "Messages put on the queue.", []string{"tenant", "channel", "destination"}},
		{QueueDrainedTotal, KindCounter, UnitCount, "Messages eventually delivered from the queue.", []string{"tenant", "channel", "destination"}},
		{QueueRetriesTotal, KindCounter, UnitCount, "Failed attempts on queued messages.", []string{"tenant", "channel", "destination"}},
		{QueueAbandonedTotal, KindCounter, UnitCount, "Queued messages given up on.", []string{"tenant", "channel", "destination"}},
		{QueueWaitSeconds, KindHistogram, UnitSeconds, "How long a message waited in the queue before delivery.", []string{"tenant", "channel", "destination"}},

		{GoRoutines, KindGauge, UnitCount, "Goroutines in the process.", nil},
		{HeapBytes, KindGauge, UnitBytes, "Heap in use.", nil},
		{UptimeSecs, KindGauge, UnitSeconds, "Seconds since start.", nil},
	} {
		c.Register(def)
	}
}

// SampleRuntime records the process gauges. It is called from the same timer as
// Sample so that every series shares a timestamp, which matters when two are
// drawn on one chart.
func (c *Collector) SampleRuntime() {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	c.Set(GoRoutines, float64(runtime.NumGoroutine()))
	c.Set(HeapBytes, float64(mem.HeapAlloc))
	c.Set(UptimeSecs, time.Since(c.started).Seconds())
}

// Run samples on a timer until the channel closes. It is the only thing that
// needs to be scheduled; everything else records inline.
func (c *Collector) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(c.resolution)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case at := <-ticker.C:
			c.SampleRuntime()
			c.Sample(at)
		}
	}
}

// WritePrometheus writes the exposition format.
//
// Supporting it is a deliberate choice not to invent a monitoring stack. A site
// that already runs Prometheus gets alerting, long-term storage and Grafana for
// free, and the built-in dashboard becomes a convenience rather than the only way
// to see anything.
func (c *Collector) WritePrometheus(w io.Writer) error {
	return c.writePrometheus(w, "")
}

// WritePrometheusForTenant writes only one tenant's series.
//
// Needed because the scrape endpoint is reachable by a tenant's own operator, and a metric label names a channel - which
// usually names the system at the other end. One customer reading another's interface names from a monitoring endpoint is
// a disclosure even though no message content is involved.
//
// A series with no tenant label is process-wide - memory, goroutines, uptime - and is included, because a tenant's
// operator cannot infer anything about another customer from the server's heap size, and excluding it would leave their
// dashboard unable to show whether the server is healthy at all.
func (c *Collector) WritePrometheusForTenant(w io.Writer, tenantID string) error {
	if tenantID == "" {
		// Refused rather than treated as "everything". An empty tenant reaching this function means a caller failed
		// to resolve one, and the permissive reading of that mistake is the disclosure this exists to prevent.
		return fmt.Errorf("a tenant is required; use WritePrometheus to write every tenant's series deliberately")
	}

	return c.writePrometheus(w, tenantID)
}

// writePrometheus writes the exposition. An empty tenant means every series.
func (c *Collector) writePrometheus(w io.Writer, onlyTenant string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.series))
	for name := range c.series {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder

	for _, name := range names {
		set := c.series[name]
		def := set.def

		if def.Help != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n", name, escapeHelp(def.Help))
		}
		kind := "counter"
		switch def.Kind {
		case KindGauge:
			kind = "gauge"
		case KindHistogram:
			kind = "histogram"
		}
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, kind)

		keys := make([]string, 0, len(set.streams))
		for k := range set.streams {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			s := set.streams[key]

			// Filtered here rather than by rewriting the output, so a series belonging to another tenant is never
			// written down at all - including its name, which is the part that discloses something.
			//
			// Labels are positional: def.Labels holds the names and s.labels the values for this stream.
			if onlyTenant != "" && !streamBelongsTo(def.Labels, s.labels, onlyTenant) {
				continue
			}

			labels := promLabels(def.Labels, s.labels)

			if def.Kind != KindHistogram {
				fmt.Fprintf(&b, "%s%s %g\n", name, labels, s.value)
				continue
			}

			// A histogram is exposed as cumulative buckets plus sum and count,
			// which is what a scraper expects and what lets it compute
			// percentiles over any window rather than only ours.
			var cumulative uint64
			for i, bound := range bucketBounds {
				cumulative += s.counts[i]
				fmt.Fprintf(&b, "%s_bucket%s %d\n", name,
					withLabel(labels, "le", formatFloat(bound)), cumulative)
			}
			cumulative += s.counts[len(bucketBounds)]
			fmt.Fprintf(&b, "%s_bucket%s %d\n", name, withLabel(labels, "le", "+Inf"), cumulative)
			fmt.Fprintf(&b, "%s_sum%s %g\n", name, labels, s.sum)
			fmt.Fprintf(&b, "%s_count%s %d\n", name, labels, s.count)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func promLabels(names, values []string) string {
	if len(values) == 0 {
		return ""
	}
	var parts []string
	for i, v := range values {
		name := "label"
		if i < len(names) {
			name = names[i]
		}
		parts = append(parts, fmt.Sprintf("%s=%q", name, escapeLabel(v)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func withLabel(existing, name, value string) string {
	pair := fmt.Sprintf("%s=%q", name, value)
	if existing == "" {
		return "{" + pair + "}"
	}
	return existing[:len(existing)-1] + "," + pair + "}"
}

func formatFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", f), "0"), ".")
}

func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// Snapshot is a whole-collector reading, for the dashboard's overview panels.
type Snapshot struct {
	At            time.Time        `json:"at"`
	Uptime        float64          `json:"uptimeSeconds"`
	Metrics       []Series         `json:"metrics"`
	Dropped       map[string]int64 `json:"dropped,omitempty"`
	Resolution    float64          `json:"resolutionSeconds"`
	WindowSeconds float64          `json:"windowSeconds"`
}

// SnapshotSince returns every series with points from the given time.
func (c *Collector) SnapshotSince(since time.Time) Snapshot {
	defs := c.Definitions()

	out := Snapshot{
		At:            time.Now(),
		Uptime:        time.Since(c.started).Seconds(),
		Dropped:       c.Dropped(),
		Resolution:    c.resolution.Seconds(),
		WindowSeconds: c.window.Seconds(),
	}
	for _, def := range defs {
		out.Metrics = append(out.Metrics, c.Query(def.Name, since)...)
	}
	return out
}

// streamBelongsTo reports whether a stream may be shown to one tenant.
//
// A stream with no tenant label is process-wide - heap size, uptime, goroutines - and is shown, because nothing about
// another customer can be inferred from it and hiding it would leave a tenant's dashboard unable to say whether the server
// is up.
//
// A stream whose tenant label is present but empty is shown only to the default tenant, which is where an unlabelled
// message ends up.
func streamBelongsTo(names, values []string, tenantID string) bool {
	for i, name := range names {
		if name != "tenant" {
			continue
		}
		if i >= len(values) {
			// Fewer values than names should not happen. Treated as not belonging, because the permissive reading of
			// an inconsistency here is the disclosure this function exists to prevent.
			return false
		}

		return values[i] == tenantID
	}

	// No tenant label at all: process-wide.
	return true
}
