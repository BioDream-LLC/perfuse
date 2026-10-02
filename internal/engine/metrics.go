package engine

import (
	"time"

	"github.com/biodream-llc/perfuse/internal/metrics"
)

// Metrics recording lives here rather than being scattered through the message
// path, so that the cost and the cardinality of what is recorded can be seen in
// one place.
//
// Every label used below is bounded by configuration - a channel name, a
// destination name - and never by message content. That restraint is the whole
// discipline of instrumenting an interface engine: labelling by sending
// application or patient identifier would look useful for a week and then take
// the process down when a misconfigured feed produced ten thousand distinct
// values.

// SetMetrics attaches a collector. Passing nil disables recording, which is what
// tests and one-shot command-line runs do.
func (c *Channel) SetMetrics(m *metrics.Collector) { c.metrics = m }

// SetTenant records which tenant this channel belongs to, for metric labels.
//
// Empty means single-tenant, which reports as the default tenant rather than as an
// empty label. An empty label value in Prometheus is indistinguishable from an
// absent one and would make a single-tenant series impossible to select.
func (c *Channel) SetTenant(id string) { c.tenantID = id }

// SetQueryState gives a channel somewhere to record what its DICOM query source has already seen.
//
// Set from outside rather than constructed here, because the state lives in the same database as the queue and the engine
// does not otherwise know about a database at all.
func (c *Channel) SetQueryState(s DICOMQueryState) { c.queryState = s }

// tenantLabel is the first label on every channel-scoped metric.
//
// It has to be there rather than being optional. Two tenants can each have a channel
// called adt-inbound, and without this their counters merge into one series - so one
// organisation's dashboard would show another organisation's message volumes. That is
// both wrong and an information leak, and it would look like a traffic spike rather
// than like a bug.
func (c *Channel) tenantLabel() string {
	if c.tenantID == "" {
		return "main"
	}
	return c.tenantID
}

// recordOutcome notes the result of handling one message.
func (c *Channel) recordOutcome(outcome Outcome, bytes int, took time.Duration) {
	if c.metrics == nil {
		return
	}
	name := c.cfg.Name

	c.metrics.Add(metrics.MessageBytes, int64(bytes), c.tenantLabel(), name)
	c.metrics.ObserveDuration(metrics.MessageDuration, took, c.tenantLabel(), name)

	switch outcome {
	case Delivered:
		c.metrics.Inc(metrics.MessagesReceived, c.tenantLabel(), name)
		c.metrics.Inc(metrics.MessagesDelivered, c.tenantLabel(), name)
	case Filtered:
		c.metrics.Inc(metrics.MessagesReceived, c.tenantLabel(), name)
		c.metrics.Inc(metrics.MessagesFiltered, c.tenantLabel(), name)
	case Unparseable:
		// An unparseable message is counted as received: it arrived, and a
		// dashboard that omitted it would understate the load and hide a sender
		// that has started emitting rubbish.
		c.metrics.Inc(metrics.MessagesReceived, c.tenantLabel(), name)
		c.metrics.Inc(metrics.MessagesUnparseable, c.tenantLabel(), name)
	default:
		c.metrics.Inc(metrics.MessagesReceived, c.tenantLabel(), name)
		c.metrics.Inc(metrics.MessagesFailed, c.tenantLabel(), name)
	}
}

// incMetric adds one to a channel-labelled counter, if metrics are collected.
func (c *Channel) incMetric(name string) {
	if c.metrics == nil {
		return
	}
	c.metrics.Inc(name, c.tenantLabel(), c.cfg.Name)
}

// addMetric adds n to a channel-labelled counter, if metrics are collected.
func (c *Channel) addMetric(name string, n int64) {
	if c.metrics == nil || n == 0 {
		return
	}
	c.metrics.Add(name, n, c.tenantLabel(), c.cfg.Name)
}

// setGauge records a channel-labelled gauge, if metrics are collected.
//
// A gauge is set rather than added, and the zero has to be written as deliberately
// as any other value: a gauge that stops being published keeps its last value
// forever, which reads as a permanent problem long after it cleared.
func (c *Channel) setGauge(name string, v float64) {
	if c.metrics == nil {
		return
	}
	c.metrics.Set(name, v, c.tenantLabel(), c.cfg.Name)
}

// recordDelivery notes one delivery, including how many attempts it took.
func (c *Channel) recordDelivery(destination string, attempts int, took time.Duration, failed bool) {
	if c.metrics == nil {
		return
	}
	name := c.cfg.Name

	c.metrics.ObserveDuration(metrics.DeliveryDuration, took, c.tenantLabel(), name, destination)
	c.metrics.Add(metrics.DeliveryAttempts, int64(max(attempts, 1)), c.tenantLabel(), name, destination)
	if failed {
		c.metrics.Inc(metrics.DeliveryFailures, c.tenantLabel(), name, destination)
	}
}

// recordTransform notes what the transformation stage did.
func (c *Channel) recordTransform(changes int, failed bool) {
	if c.metrics == nil {
		return
	}
	name := c.cfg.Name
	if changes > 0 {
		c.metrics.Add(metrics.TransformSteps, int64(changes), c.tenantLabel(), name)
	}
	if failed {
		c.metrics.Inc(metrics.TransformErrors, c.tenantLabel(), name)
	}
}

// recordScript notes a script run.
func (c *Channel) recordScript(kind string, took time.Duration, err error, timedOut bool) {
	if c.metrics == nil {
		return
	}
	name := c.cfg.Name
	c.metrics.ObserveDuration(metrics.ScriptDuration, took, name, kind)
	if timedOut {
		c.metrics.Inc(metrics.ScriptTimeouts, c.tenantLabel(), name, kind)
	}
	if err != nil {
		c.metrics.Inc(metrics.ScriptErrors, c.tenantLabel(), name, kind)
	}
}

// recordConnections reports how many inbound connections are open.
func (c *Channel) recordConnections(n int) {
	if c.metrics == nil {
		return
	}
	c.metrics.Set(metrics.ConnectionsOpen, float64(n), c.tenantLabel(), c.cfg.Name)
}

// Queue metrics. These are what an alert fires on, and there are three of them
// rather than one because "how many are waiting" cannot on its own distinguish a
// busy queue that is draining from a small one stuck since Tuesday.
func (c *Channel) recordQueued(destination string) {
	c.count(func(s *Stats) { s.DestQueued[destination]++ })
	if c.metrics == nil {
		return
	}
	c.metrics.Inc(metrics.QueuedTotal, c.tenantLabel(), c.cfg.Name, destination)
}

func (c *Channel) recordQueueDrained(destination string, waited time.Duration) {
	if c.metrics == nil {
		return
	}
	c.metrics.Inc(metrics.QueueDrainedTotal, c.tenantLabel(), c.cfg.Name, destination)
	// How long a message actually waited is the number that tells somebody whether
	// the outage was seconds or hours, and a mean would hide the hours.
	c.metrics.ObserveDuration(metrics.QueueWaitSeconds, waited, c.cfg.Name, destination)
}

func (c *Channel) recordQueueRetry(destination string) {
	if c.metrics == nil {
		return
	}
	c.metrics.Inc(metrics.QueueRetriesTotal, c.tenantLabel(), c.cfg.Name, destination)
}

func (c *Channel) recordQueueAbandoned(destination string) {
	if c.metrics == nil {
		return
	}
	c.metrics.Inc(metrics.QueueAbandonedTotal, c.tenantLabel(), c.cfg.Name, destination)
}
