// Package metrics collects what the engine is doing and keeps enough history to
// draw it.
//
// An interface engine is watched, not just run. When a ward says results stopped
// arriving twenty minutes ago, the question is what changed twenty minutes ago,
// and that cannot be answered by current counters alone. So this keeps series
// rather than totals, in memory, at a fixed resolution.
//
// Three decisions shape it.
//
// Latency is kept as a histogram rather than a mean. An average delivery time is
// nearly useless in this domain: the number that matters is whether the slowest
// few per cent of messages are slow enough to breach a turnaround commitment, and
// a mean hides exactly that. Buckets are exponential and fixed at build time, so
// recording a duration is a comparison and an increment with no allocation.
//
// Series are bounded ring buffers, sized when the collector is made. Nothing here
// grows without limit, because an interface engine runs for months at a time and
// a metrics system that needs restarting is worse than none.
//
// Cardinality is capped. A label value taken from message data - a sending
// application, say - can take thousands of values if somebody misconfigures a
// feed, and unbounded labels are the standard way monitoring takes down the thing
// it monitors. Past the cap, values collapse into an "other" bucket and the cap
// is itself reported.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind distinguishes how a metric is read.
type Kind string

const (
	// KindCounter only ever increases. Rates are derived from differences.
	KindCounter Kind = "counter"
	// KindGauge is a current value that can move either way.
	KindGauge Kind = "gauge"
	// KindHistogram records a distribution, read as percentiles.
	KindHistogram Kind = "histogram"
)

// Unit tells the interface how to format a value, so that a byte count and a
// message count do not both get rendered as a bare number.
type Unit string

const (
	UnitCount   Unit = "count"
	UnitBytes   Unit = "bytes"
	UnitSeconds Unit = "seconds"
	UnitRatio   Unit = "ratio"
)

// Definition describes a metric once, so that every place which displays it
// agrees on the name, the help text and the unit.
type Definition struct {
	Name   string
	Kind   Kind
	Unit   Unit
	Help   string
	Labels []string
}

// maxLabelValues caps distinct label combinations per metric.
//
// Two thousand is generous for a real deployment - a hospital has tens of
// channels and a handful of destinations each - and small enough that a runaway
// label cannot exhaust memory before the cap reports the problem.
const maxLabelValues = 2000

// Collector holds every metric and its history.
type Collector struct {
	mu sync.RWMutex

	defs   map[string]Definition
	series map[string]*seriesSet

	// resolution is how often Sample is expected to be called, and therefore the
	// spacing of every point.
	resolution time.Duration
	// window is how much history to keep.
	window time.Duration
	points int

	started time.Time
	dropped map[string]int64
}

// Options configures a collector.
type Options struct {
	// Resolution is the spacing between samples. Zero means ten seconds.
	Resolution time.Duration
	// Window is how much history to keep. Zero means six hours.
	Window time.Duration
}

// New makes a collector.
func New(opts Options) *Collector {
	if opts.Resolution <= 0 {
		opts.Resolution = 10 * time.Second
	}
	if opts.Window <= 0 {
		opts.Window = 6 * time.Hour
	}
	points := int(opts.Window / opts.Resolution)
	if points < 2 {
		points = 2
	}
	// Cap the ring so that an absurd window cannot allocate gigabytes.
	if points > 20000 {
		points = 20000
	}

	c := &Collector{
		defs:       map[string]Definition{},
		series:     map[string]*seriesSet{},
		resolution: opts.Resolution,
		window:     opts.Window,
		points:     points,
		started:    time.Now(),
		dropped:    map[string]int64{},
	}
	c.registerBuiltins()
	return c
}

// Resolution reports the sample spacing.
func (c *Collector) Resolution() time.Duration { return c.resolution }

// Window reports how much history is kept.
func (c *Collector) Window() time.Duration { return c.window }

// Uptime reports how long the collector has been running.
func (c *Collector) Uptime() time.Duration { return time.Since(c.started) }

// Register declares a metric. Registering the same name twice is allowed and
// keeps the first definition, so that a caller need not coordinate.
func (c *Collector) Register(def Definition) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.defs[def.Name]; exists {
		return
	}
	c.defs[def.Name] = def
	c.series[def.Name] = newSeriesSet(def, c.points)
}

// Definitions returns every registered metric, ordered by name.
func (c *Collector) Definitions() []Definition {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]Definition, 0, len(c.defs))
	for _, d := range c.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Add increases a counter.
func (c *Collector) Add(name string, delta int64, labels ...string) {
	c.withStream(name, labels, func(s *stream) { s.value += float64(delta) })
}

// Inc increases a counter by one.
func (c *Collector) Inc(name string, labels ...string) { c.Add(name, 1, labels...) }

// Set writes a gauge.
func (c *Collector) Set(name string, value float64, labels ...string) {
	c.withStream(name, labels, func(s *stream) { s.value = value })
}

// Observe records a value into a histogram.
func (c *Collector) Observe(name string, value float64, labels ...string) {
	c.withStream(name, labels, func(s *stream) { s.observe(value) })
}

// ObserveDuration records a duration in seconds, which is the unit every
// convention in this area uses.
func (c *Collector) ObserveDuration(name string, d time.Duration, labels ...string) {
	c.Observe(name, d.Seconds(), labels...)
}

func (c *Collector) withStream(name string, labels []string, fn func(*stream)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	set, ok := c.series[name]
	if !ok {
		// An unregistered metric is recorded anyway, as a counter, rather than
		// dropped. Losing a measurement because of a missing declaration is a
		// worse outcome than an under-described one.
		def := Definition{Name: name, Kind: KindCounter, Unit: UnitCount, Help: "(undeclared)"}
		c.defs[name] = def
		set = newSeriesSet(def, c.points)
		c.series[name] = set
	}

	key := labelKey(labels)
	s, ok := set.streams[key]
	if !ok {
		if len(set.streams) >= maxLabelValues {
			// Collapse rather than grow. The cap is reported so that the cause
			// is visible instead of the data silently going wrong.
			c.dropped[name]++
			key = "__other__"
			if s, ok = set.streams[key]; !ok {
				s = newStream(set.def, []string{"other"}, c.points)
				set.streams[key] = s
			}
		} else {
			s = newStream(set.def, labels, c.points)
			set.streams[key] = s
		}
	}
	fn(s)
}

// Sample takes one point for every stream. It is expected to be called on a timer
// at the collector's resolution.
func (c *Collector) Sample(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, set := range c.series {
		for _, s := range set.streams {
			s.sample(at)
		}
	}
}

// seriesSet is every labelled stream of one metric.
type seriesSet struct {
	def     Definition
	streams map[string]*stream
}

func newSeriesSet(def Definition, points int) *seriesSet {
	return &seriesSet{def: def, streams: map[string]*stream{}}
}

// stream is one labelled time series.
type stream struct {
	def    Definition
	labels []string

	// value is the live counter or gauge.
	value float64

	// buckets and counts hold histogram observations.
	counts []uint64
	sum    float64
	count  uint64

	ring   []point
	filled int
	next   int

	// lastSampled is the counter value at the previous sample, so that a rate can
	// be computed without the caller needing to know it is a counter.
	lastSampled float64
}

// point is one sample.
type point struct {
	at time.Time
	// value is the counter total or gauge reading at this instant.
	value float64
	// delta is how much a counter moved since the previous sample. Storing it
	// avoids every reader having to difference the series, and avoids the
	// classic mistake of showing a negative rate after a restart.
	delta float64
	// p50, p95, p99 are histogram percentiles at this instant.
	p50, p95, p99 float64
	// observed is how many histogram observations fell in this interval.
	observed uint64
}

func newStream(def Definition, labels []string, points int) *stream {
	s := &stream{
		def:    def,
		labels: append([]string(nil), labels...),
		ring:   make([]point, points),
	}
	if def.Kind == KindHistogram {
		s.counts = make([]uint64, len(bucketBounds)+1)
	}
	return s
}

func (s *stream) observe(v float64) {
	if s.counts == nil {
		// A histogram observation on a metric declared as something else is
		// still recorded, as a gauge of the last value, rather than lost.
		s.value = v
		return
	}
	idx := bucketFor(v)
	s.counts[idx]++
	s.sum += v
	s.count++
	s.value = v
}

func (s *stream) sample(at time.Time) {
	p := point{at: at, value: s.value}

	switch s.def.Kind {
	case KindCounter:
		p.delta = s.value - s.lastSampled
		if p.delta < 0 {
			// A counter that went backwards means it was reset. Reporting zero
			// is honest; reporting a huge negative rate is not.
			p.delta = 0
		}
		s.lastSampled = s.value

	case KindHistogram:
		p.p50 = s.quantile(0.50)
		p.p95 = s.quantile(0.95)
		p.p99 = s.quantile(0.99)
		p.observed = s.count
		p.delta = float64(s.count) - s.lastSampled
		if p.delta < 0 {
			p.delta = 0
		}
		s.lastSampled = float64(s.count)
	}

	s.ring[s.next] = p
	s.next = (s.next + 1) % len(s.ring)
	if s.filled < len(s.ring) {
		s.filled++
	}
}

// points returns the samples in chronological order.
func (s *stream) points() []point {
	out := make([]point, 0, s.filled)
	if s.filled < len(s.ring) {
		out = append(out, s.ring[:s.filled]...)
		return out
	}
	out = append(out, s.ring[s.next:]...)
	out = append(out, s.ring[:s.next]...)
	return out
}

// bucketBounds are the histogram edges, in seconds.
//
// They span a hundred microseconds to two minutes, which covers everything from a
// local file write to a remote system that has stopped answering. The spacing is
// roughly a factor of two, giving percentile estimates within about a third of the
// true value - ample for deciding whether something is slow.
var bucketBounds = []float64{
	0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1,
	0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120,
}

func bucketFor(v float64) int {
	// A linear scan over nineteen bounds beats a binary search at this size and
	// keeps the hot path free of branches that mispredict.
	for i, bound := range bucketBounds {
		if v <= bound {
			return i
		}
	}
	return len(bucketBounds)
}

// quantile estimates a percentile by linear interpolation within the bucket the
// rank falls into.
func (s *stream) quantile(q float64) float64 {
	if s.count == 0 {
		return 0
	}
	target := q * float64(s.count)

	var cumulative float64
	for i, c := range s.counts {
		prev := cumulative
		cumulative += float64(c)
		if cumulative < target {
			continue
		}
		if c == 0 {
			continue
		}

		lower := 0.0
		if i > 0 {
			lower = bucketBounds[i-1]
		}
		upper := math.Inf(1)
		if i < len(bucketBounds) {
			upper = bucketBounds[i]
		}
		if math.IsInf(upper, 1) {
			// Everything above the last bound is reported at the bound rather
			// than as infinity, which would make a chart unreadable.
			return bucketBounds[len(bucketBounds)-1]
		}

		within := (target - prev) / float64(c)
		return lower + within*(upper-lower)
	}
	return bucketBounds[len(bucketBounds)-1]
}

func labelKey(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return strings.Join(labels, "\x00")
}

// Series is one labelled time series, ready to draw.
type Series struct {
	Name   string            `json:"name"`
	Kind   Kind              `json:"kind"`
	Unit   Unit              `json:"unit"`
	Help   string            `json:"help,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Points []Point           `json:"points"`

	// Latest is the current value, so a caller drawing a single number does not
	// have to reach into the last point.
	Latest float64 `json:"latest"`
	// RatePerSecond is the most recent rate for a counter.
	RatePerSecond float64 `json:"ratePerSecond"`
}

// Point is one sample, as the interface consumes it.
type Point struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
	// Rate is the per-second rate over the interval ending here, for counters.
	Rate float64 `json:"rate,omitempty"`
	// P50, P95 and P99 are percentiles for histograms, in seconds.
	P50 float64 `json:"p50,omitempty"`
	P95 float64 `json:"p95,omitempty"`
	P99 float64 `json:"p99,omitempty"`
}

// Query reads back series for a metric.
//
// Since is inclusive; a zero time means everything held.
func (c *Collector) Query(name string, since time.Time) []Series {
	c.mu.RLock()
	defer c.mu.RUnlock()

	set, ok := c.series[name]
	if !ok {
		return nil
	}

	out := make([]Series, 0, len(set.streams))
	for _, s := range set.streams {
		out = append(out, s.export(since, c.resolution))
	}

	// Ordered by current value, descending, so the busiest series is drawn first
	// and a legend is stable between refreshes.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Latest != out[j].Latest {
			return out[i].Latest > out[j].Latest
		}
		return labelString(out[i].Labels) < labelString(out[j].Labels)
	})
	return out
}

func (s *stream) export(since time.Time, resolution time.Duration) Series {
	labels := map[string]string{}
	for i, name := range s.def.Labels {
		if i < len(s.labels) {
			labels[name] = s.labels[i]
		}
	}
	if len(s.def.Labels) == 0 && len(s.labels) > 0 {
		labels["label"] = s.labels[0]
	}

	seconds := resolution.Seconds()
	if seconds <= 0 {
		seconds = 1
	}

	raw := s.points()
	out := Series{
		Name:   s.def.Name,
		Kind:   s.def.Kind,
		Unit:   s.def.Unit,
		Help:   s.def.Help,
		Labels: labels,
		Latest: s.value,
		Points: make([]Point, 0, len(raw)),
	}

	for _, p := range raw {
		if !since.IsZero() && p.at.Before(since) {
			continue
		}
		point := Point{At: p.at, Value: p.value}
		switch s.def.Kind {
		case KindCounter:
			point.Rate = p.delta / seconds
			point.Value = p.delta
		case KindHistogram:
			point.P50, point.P95, point.P99 = p.p50, p.p95, p.p99
			point.Rate = p.delta / seconds
			point.Value = p.delta
		}
		out.Points = append(out.Points, point)
	}

	if n := len(out.Points); n > 0 {
		out.RatePerSecond = out.Points[n-1].Rate
	}
	if s.def.Kind == KindHistogram && s.count > 0 {
		// For a histogram the useful "latest" is the p95, not the last value:
		// one fast message says nothing about whether the destination is slow.
		out.Latest = s.quantile(0.95)
	}
	return out
}

func labelString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%s", k, labels[k])
	}
	return b.String()
}

// Dropped reports metrics whose label cardinality hit the cap, so that the
// interface can say so rather than showing quietly wrong data.
func (c *Collector) Dropped() map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]int64, len(c.dropped))
	for k, v := range c.dropped {
		out[k] = v
	}
	return out
}
