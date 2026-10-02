package metrics

import (
	"strings"
	"time"
)

// Aggregations used by alerting.
//
// These exist here rather than in the alerting package because they are about how
// a series is summarised, which is this package's business, and because getting
// them wrong is easy in a way that is quiet: a rule reading running totals looks
// like it works and simply stops responding after a few hours.

// Deltas returns, for every counter, how much it advanced over the window,
// grouped by the first label value.
//
// The first label is the channel for every counter that has one, which is what
// alert rules are keyed on.
//
// Counters are summed from the interval deltas each point carries rather than
// subtracting a running total. That matters for alerting: an error rate computed
// from totals since boot converges on a constant and stops reacting to anything,
// so a channel that has been fine for a week and then breaks would not trip a
// threshold for hours.
func (c *Collector) Deltas(window time.Duration) map[string]map[string]float64 {
	since := time.Now().Add(-window)
	out := map[string]map[string]float64{}

	for _, def := range c.Definitions() {
		if def.Kind != KindCounter {
			continue
		}
		for _, series := range c.Query(def.Name, since) {
			key := firstLabel(series, def)
			total := 0.0
			for _, p := range series.Points {
				total += p.Value
			}
			if out[def.Name] == nil {
				out[def.Name] = map[string]float64{}
			}
			// Summed rather than replaced: two series can share a first label when a
			// counter is labelled by channel and destination both, and an alert on
			// the channel wants the whole channel.
			out[def.Name][key] += total
		}
	}
	return out
}

// Percentile returns the highest recent value of a percentile for a histogram,
// grouped by the first label.
//
// The highest across the window rather than the latest, because a rule watching
// latency is asking whether the tail got bad at all, and a single calm sample at
// the moment of evaluation should not clear it. The debounce in the alert rules is
// what decides whether it persisted.
//
// Quantile must be 0.5, 0.95 or 0.99: those are the three the collector keeps, and
// interpolating between them would invent precision the buckets do not have.
func (c *Collector) Percentile(name string, quantile float64, window time.Duration) map[string]float64 {
	since := time.Now().Add(-window)
	out := map[string]float64{}

	var def Definition
	for _, d := range c.Definitions() {
		if d.Name == name {
			def = d
			break
		}
	}

	for _, series := range c.Query(name, since) {
		key := firstLabel(series, def)
		worst := 0.0
		for _, p := range series.Points {
			var v float64
			switch {
			case quantile >= 0.99:
				v = p.P99
			case quantile >= 0.95:
				v = p.P95
			default:
				v = p.P50
			}
			if v > worst {
				worst = v
			}
		}
		if worst > out[key] {
			out[key] = worst
		}
	}
	return out
}

// firstLabel returns the value of a series' first declared label, which is the
// channel for everything that carries one.
//
// An unlabelled series is grouped under the empty string rather than being
// dropped: process metrics such as goroutine count have no labels and a rule may
// still want them.
func firstLabel(series Series, def Definition) string {
	if len(def.Labels) > 0 {
		if v, ok := series.Labels[def.Labels[0]]; ok {
			return v
		}
	}
	// Fall back to whatever single label is present, so a metric registered
	// without a declaration still groups sensibly.
	if len(series.Labels) == 1 {
		for _, v := range series.Labels {
			return v
		}
	}
	if v, ok := series.Labels["channel"]; ok {
		return v
	}
	return ""
}

// SeriesKey renders a series' labels as a stable identifier, for callers that
// need to tell two series of the same metric apart.
func SeriesKey(series Series, def Definition) string {
	if len(series.Labels) == 0 {
		return series.Name
	}
	var b strings.Builder
	b.WriteString(series.Name)
	// Declaration order rather than map order, so the key is stable between calls.
	for _, name := range def.Labels {
		if v, ok := series.Labels[name]; ok {
			b.WriteByte('/')
			b.WriteString(v)
		}
	}
	return b.String()
}
