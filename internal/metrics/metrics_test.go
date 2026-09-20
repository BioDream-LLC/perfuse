package metrics

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestCounterAndRate(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})

	base := time.Now()
	c.Add(MessagesReceived, 10, "main", "adt")
	c.Sample(base)
	c.Add(MessagesReceived, 5, "main", "adt")
	c.Sample(base.Add(time.Second))

	series := c.Query(MessagesReceived, time.Time{})
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1", len(series))
	}
	s := series[0]

	if s.Latest != 15 {
		t.Errorf("latest = %v, want 15", s.Latest)
	}
	if s.Labels["channel"] != "adt" {
		t.Errorf("labels = %v", s.Labels)
	}
	if len(s.Points) != 2 {
		t.Fatalf("got %d points, want 2", len(s.Points))
	}
	// A counter's points carry the interval delta, not the running total, because
	// that is what a chart of throughput needs.
	if s.Points[0].Value != 10 || s.Points[1].Value != 5 {
		t.Errorf("point values = %v, %v; want 10, 5", s.Points[0].Value, s.Points[1].Value)
	}
	if s.Points[1].Rate != 5 {
		t.Errorf("rate = %v, want 5 per second", s.Points[1].Rate)
	}
}

// TestCounterResetDoesNotShowNegativeRate pins a decision. A counter that goes
// backwards means it was reset, and reporting a huge negative rate would put a
// spike on the chart that looks like a real event.
func TestCounterResetDoesNotShowNegativeRate(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})
	base := time.Now()

	c.Add(MessagesReceived, 100, "main", "adt")
	c.Sample(base)
	c.Set(MessagesReceived, 0, "main", "adt") // as if reset
	c.Sample(base.Add(time.Second))

	s := c.Query(MessagesReceived, time.Time{})[0]
	for i, p := range s.Points {
		if p.Rate < 0 || p.Value < 0 {
			t.Errorf("point %d has a negative rate %v / value %v", i, p.Rate, p.Value)
		}
	}
}

func TestGauge(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})
	c.Set(ChannelsRunning, 3)
	c.Sample(time.Now())

	s := c.Query(ChannelsRunning, time.Time{})[0]
	if s.Latest != 3 {
		t.Errorf("latest = %v, want 3", s.Latest)
	}
	// A gauge's point is its reading, not a delta.
	if s.Points[0].Value != 3 {
		t.Errorf("point = %v, want 3", s.Points[0].Value)
	}
}

// TestHistogramPercentiles is the one that matters for latency. A mean would hide
// the tail, which is the only part anybody cares about.
func TestHistogramPercentiles(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})

	// Ninety-five fast messages and five slow ones. The mean is about 150ms,
	// which looks tolerable and is the reason a mean is not kept: the p50 says
	// the typical message is fine while the p99 says one in a hundred takes
	// three seconds, and only the second number tells you a destination is
	// struggling.
	for i := 0; i < 95; i++ {
		c.ObserveDuration(DeliveryDuration, 5*time.Millisecond, "main", "adt", "registry")
	}
	for i := 0; i < 5; i++ {
		c.ObserveDuration(DeliveryDuration, 3*time.Second, "main", "adt", "registry")
	}
	c.Sample(time.Now())

	s := c.Query(DeliveryDuration, time.Time{})[0]
	p := s.Points[0]

	if p.P50 > 0.02 {
		t.Errorf("p50 = %v seconds, should be near 5ms", p.P50)
	}
	if p.P99 < 0.5 {
		t.Errorf("p99 = %v seconds, should reflect the slow tail", p.P99)
	}
	if p.P50 > p.P95 || p.P95 > p.P99 {
		t.Errorf("percentiles out of order: p50 %v, p95 %v, p99 %v", p.P50, p.P95, p.P99)
	}
	// The headline value for a histogram is the p95, since one fast message says
	// nothing about whether a destination is slow.
	if s.Latest < 0.001 {
		t.Errorf("latest = %v, should be the p95", s.Latest)
	}
}

func TestHistogramBucketAccuracy(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})

	// Everything at exactly 100ms; the median must land near it.
	for i := 0; i < 1000; i++ {
		c.Observe(MessageDuration, 0.1, "main", "adt")
	}
	c.Sample(time.Now())

	p := c.Query(MessageDuration, time.Time{})[0].Points[0]
	if math.Abs(p.P50-0.1) > 0.05 {
		t.Errorf("p50 = %v, want near 0.1", p.P50)
	}
}

// TestRingBufferIsBounded checks history cannot grow without limit, which matters
// because an interface engine runs for months.
func TestRingBufferIsBounded(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: 10 * time.Second})
	base := time.Now()

	for i := 0; i < 100; i++ {
		c.Add(MessagesReceived, 1, "main", "adt")
		c.Sample(base.Add(time.Duration(i) * time.Second))
	}

	s := c.Query(MessagesReceived, time.Time{})[0]
	if len(s.Points) > 12 {
		t.Errorf("kept %d points for a 10 second window; the ring is not bounded", len(s.Points))
	}
	// And the points kept must be the most recent ones, in order.
	for i := 1; i < len(s.Points); i++ {
		if !s.Points[i].At.After(s.Points[i-1].At) {
			t.Errorf("points are out of order at %d", i)
		}
	}
}

// TestCardinalityIsCapped is a protection against monitoring taking down the
// thing it monitors. A label taken from message data can explode.
func TestCardinalityIsCapped(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})

	for i := 0; i < maxLabelValues+500; i++ {
		c.Add(MessagesReceived, 1, "channel-"+string(rune('a'+i%26))+itoa(i))
	}

	series := c.Query(MessagesReceived, time.Time{})
	if len(series) > maxLabelValues+1 {
		t.Errorf("got %d series, cap is %d plus an overflow bucket", len(series), maxLabelValues)
	}
	if dropped := c.Dropped()[MessagesReceived]; dropped == 0 {
		t.Error("hitting the cap should be reported, not silent")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestUnregisteredMetricIsKept pins the choice to record rather than drop. Losing
// a measurement because of a missing declaration is the worse outcome.
func TestUnregisteredMetricIsKept(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})
	c.Add("something_nobody_declared", 4)
	c.Sample(time.Now())

	if got := c.Query("something_nobody_declared", time.Time{}); len(got) != 1 {
		t.Fatal("an undeclared metric should still be recorded")
	}
}

func TestQuerySince(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Hour})
	base := time.Now().Add(-time.Hour)

	for i := 0; i < 60; i++ {
		c.Add(MessagesReceived, 1, "main", "adt")
		c.Sample(base.Add(time.Duration(i) * time.Minute))
	}

	cutoff := base.Add(30 * time.Minute)
	s := c.Query(MessagesReceived, cutoff)[0]
	for _, p := range s.Points {
		if p.At.Before(cutoff) {
			t.Errorf("point at %s is before the cutoff %s", p.At, cutoff)
		}
	}
	if len(s.Points) == 0 {
		t.Error("expected some points after the cutoff")
	}
}

// TestPrometheusExposition checks the format is what a scraper expects, since the
// point of supporting it is that existing monitoring works without a translator.
func TestPrometheusExposition(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})
	c.Add(MessagesReceived, 7, "main", "adt")
	c.Set(ChannelsRunning, 2)
	c.ObserveDuration(DeliveryDuration, 50*time.Millisecond, "main", "adt", "registry")

	var b strings.Builder
	if err := c.WritePrometheus(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	for _, want := range []string{
		"# TYPE perfuse_messages_received_total counter",
		`perfuse_messages_received_total{tenant="main",channel="adt"} 7`,
		"# TYPE perfuse_channels_running gauge",
		"perfuse_channels_running 2",
		"# TYPE perfuse_delivery_duration_seconds histogram",
		`perfuse_delivery_duration_seconds_bucket{tenant="main",channel="adt",destination="registry",le="+Inf"} 1`,
		`perfuse_delivery_duration_seconds_count{tenant="main",channel="adt",destination="registry"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition is missing:\n  %s\ngot:\n%s", want, out)
		}
	}

	// Help text must be present, since a scraper surfaces it to whoever is
	// reading a dashboard six months from now.
	if !strings.Contains(out, "# HELP perfuse_messages_received_total") {
		t.Error("help text is missing")
	}
}

// TestPrometheusBucketsAreCumulative is a correctness property of the format: a
// scraper computing percentiles from non-cumulative buckets gets wrong answers.
func TestPrometheusBucketsAreCumulative(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})
	c.Observe(MessageDuration, 0.001, "main", "adt")
	c.Observe(MessageDuration, 0.5, "main", "adt")
	c.Observe(MessageDuration, 50, "main", "adt")

	var b strings.Builder
	if err := c.WritePrometheus(&b); err != nil {
		t.Fatal(err)
	}

	var last int
	for _, line := range strings.Split(b.String(), "\n") {
		if !strings.HasPrefix(line, "perfuse_message_duration_seconds_bucket") {
			continue
		}
		fields := strings.Fields(line)
		n := 0
		for _, ch := range fields[len(fields)-1] {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			}
		}
		if n < last {
			t.Errorf("buckets are not cumulative: %d after %d in %q", n, last, line)
		}
		last = n
	}
	if last != 3 {
		t.Errorf("the +Inf bucket should hold every observation, got %d", last)
	}
}

func TestLabelEscaping(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})
	c.Add(MessagesReceived, 1, `awkward"name\with`+"\n"+`escapes`)

	var b strings.Builder
	if err := c.WritePrometheus(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "\n"+`escapes`) {
		t.Error("a newline in a label value would break the exposition format")
	}
	if !strings.Contains(out, `\"`) {
		t.Error("a quote in a label value must be escaped")
	}
}

func TestSnapshotIncludesEverything(t *testing.T) {
	c := New(Options{Resolution: time.Second, Window: time.Minute})
	c.Add(MessagesReceived, 1, "main", "adt")
	c.SampleRuntime()
	c.Sample(time.Now())

	snap := c.SnapshotSince(time.Time{})
	if len(snap.Metrics) < 3 {
		t.Errorf("snapshot has %d series, expected the built-ins", len(snap.Metrics))
	}
	if snap.Resolution != 1 {
		t.Errorf("resolution = %v, want 1", snap.Resolution)
	}

	// The runtime gauges should be populated, since a dashboard shows them.
	found := false
	for _, s := range snap.Metrics {
		if s.Name == GoRoutines && s.Latest > 0 {
			found = true
		}
	}
	if !found {
		t.Error("runtime metrics were not sampled")
	}
}

func TestConcurrentRecording(t *testing.T) {
	c := New(Options{Resolution: 10 * time.Millisecond, Window: time.Minute})

	stop := make(chan struct{})
	go c.Run(stop)

	done := make(chan struct{}, 8)
	for w := 0; w < 8; w++ {
		go func(w int) {
			for i := 0; i < 500; i++ {
				c.Inc(MessagesReceived, "main", "adt")
				c.ObserveDuration(DeliveryDuration, time.Millisecond, "main", "adt", "registry")
				c.Set(QueueDepth, float64(i), "adt")
			}
			done <- struct{}{}
		}(w)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	close(stop)

	s := c.Query(MessagesReceived, time.Time{})[0]
	if s.Latest != 4000 {
		t.Errorf("counter = %v, want 4000; increments were lost", s.Latest)
	}
}
