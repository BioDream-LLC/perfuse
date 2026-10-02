package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures tracing.
type Config struct {
	// Endpoint is the OTLP/HTTP base URL, for example http://localhost:4318.
	// Empty disables tracing entirely.
	Endpoint string
	// ServiceName identifies this instance in the trace backend.
	ServiceName string
	// ServiceVersion is reported as a resource attribute.
	ServiceVersion string
	// Instance distinguishes replicas. Without it, spans from three pods are
	// indistinguishable, which defeats the point of tracing a scaled deployment.
	Instance string
	// SampleRatio is the fraction of new traces recorded, 0 to 1.
	//
	// Head sampling, decided once at the root and propagated, because the
	// alternative - deciding per span - produces traces with holes in them.
	SampleRatio float64
	// Headers are added to every export request, for backends that authenticate
	// with one.
	Headers map[string]string
	// Timeout bounds a single export request.
	Timeout time.Duration
	// MaxQueue bounds spans held awaiting export.
	MaxQueue int
	// BatchSize is how many spans go in one request.
	BatchSize int
	// FlushInterval is how often a partial batch is sent anyway, so a quiet
	// system still reports promptly rather than holding spans until the batch
	// fills.
	FlushInterval time.Duration
}

func (c *Config) setDefaults() {
	if c.ServiceName == "" {
		c.ServiceName = "perfuse"
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.MaxQueue <= 0 {
		c.MaxQueue = 2048
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 256
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 5 * time.Second
	}
	if c.SampleRatio <= 0 {
		c.SampleRatio = 1
	}
	if c.SampleRatio > 1 {
		c.SampleRatio = 1
	}
}

// Validate reports configuration that cannot work.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return nil
	}
	if !strings.HasPrefix(c.Endpoint, "http://") && !strings.HasPrefix(c.Endpoint, "https://") {
		return fmt.Errorf("trace endpoint %q must start with http:// or https://", c.Endpoint)
	}
	// Refused rather than silently clamped. A ratio of 2 means somebody thought
	// this field was a multiplier, and clamping it to 1 hides the misunderstanding
	// until they wonder why sampling never changes.
	if c.SampleRatio < 0 || c.SampleRatio > 1 {
		return fmt.Errorf("trace sample ratio %v must be between 0 and 1", c.SampleRatio)
	}
	if strings.HasPrefix(c.Endpoint, "http://") && !isLoopback(c.Endpoint) {
		// A warning rather than a refusal: spans carry channel and destination
		// names, which are configuration rather than patient data. Worth saying
		// out loud all the same, because trace attributes are the sort of thing
		// that accumulates content over time.
		return nil
	}
	return nil
}

// Warnings returns advice that does not prevent startup.
func (c *Config) Warnings() []string {
	var out []string
	if c.Endpoint == "" {
		return nil
	}
	if strings.HasPrefix(c.Endpoint, "http://") && !isLoopback(c.Endpoint) {
		out = append(out, fmt.Sprintf(
			"traces are being sent to %s over plain HTTP; span attributes include channel and destination names",
			c.Endpoint))
	}
	if c.SampleRatio < 1 {
		out = append(out, fmt.Sprintf(
			"sampling %.0f%% of traces; the message somebody asks about is the unusual one and may not be recorded",
			c.SampleRatio*100))
	}
	return out
}

func isLoopback(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	// Hostname strips the port and the brackets around an IPv6 literal, which is
	// the part hand-written string slicing reliably gets wrong.
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Tracer creates spans and exports them.
//
// A nil *Tracer is usable and produces nil spans, so a caller never needs to know
// whether tracing is configured.
type Tracer struct {
	cfg  Config
	log  *slog.Logger
	http *http.Client
	url  string

	// sampleThreshold is the trace ID cutoff for sampling, precomputed so the
	// decision is a comparison rather than a random draw. Deterministic on the
	// trace ID, which means every service seeing the same trace independently
	// reaches the same decision.
	sampleThreshold uint64

	queue chan *Span

	stop     chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once

	dropped  atomic.Int64
	exported atomic.Int64
	failures atomic.Int64
}

// New starts a tracer. A Config with no Endpoint returns nil, which is a working
// no-op tracer.
func New(cfg Config, log *slog.Logger) (*Tracer, error) {
	if cfg.Endpoint == "" {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.setDefaults()
	if log == nil {
		log = slog.Default()
	}

	t := &Tracer{
		cfg:     cfg,
		log:     log,
		http:    &http.Client{Timeout: cfg.Timeout},
		url:     strings.TrimSuffix(cfg.Endpoint, "/") + "/v1/traces",
		queue:   make(chan *Span, cfg.MaxQueue),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	t.sampleThreshold = uint64(math.Round(cfg.SampleRatio * float64(math.MaxUint64)))

	go t.run()
	return t, nil
}

// Start begins a span.
//
// The parent is taken from the context when one is present. Otherwise a new trace
// begins, unless remote carries a valid inbound context - which is the case that
// makes a trace span two systems.
func (t *Tracer) Start(ctx context.Context, name string, kind Kind, remote SpanContext) (context.Context, *Span) {
	if t == nil {
		return ctx, nil
	}

	var sc SpanContext
	var parent SpanID

	switch {
	case FromContext(ctx) != nil:
		p := FromContext(ctx).Context()
		sc.TraceID = p.TraceID
		sc.Sampled = p.Sampled
		parent = p.SpanID

	case remote.Valid():
		// Continue the caller's trace, and honour their sampling decision. A
		// system that decided to record this request expects the whole path.
		sc.TraceID = remote.TraceID
		sc.Sampled = remote.Sampled
		parent = remote.SpanID

	default:
		sc.TraceID = newTraceID()
		sc.Sampled = t.sample(sc.TraceID)
	}

	sc.SpanID = newSpanID()

	// An unsampled trace still gets a real span object so its context propagates
	// - a downstream system may sample where we did not, and it needs the IDs.
	// It simply is not exported.
	s := &Span{
		tracer: t,
		ctx:    sc,
		parent: parent,
		name:   name,
		kind:   kind,
		start:  time.Now(),
		status: statusUnset,
	}
	return WithSpan(ctx, s), s
}

// sample decides deterministically from the trace ID.
func (t *Tracer) sample(id TraceID) bool {
	if t.cfg.SampleRatio >= 1 {
		return true
	}
	// The last eight bytes, per the usual convention.
	var v uint64
	for _, b := range id[8:] {
		v = v<<8 | uint64(b)
	}
	return v < t.sampleThreshold
}

// finish queues an ended span.
func (t *Tracer) finish(s *Span) {
	if t == nil || s == nil || !s.ctx.Sampled {
		return
	}
	select {
	case t.queue <- s:
	default:
		// Dropped rather than blocked. This is the load-shedding decision that
		// keeps a slow tracing backend from becoming a slow interface engine.
		t.dropped.Add(1)
	}
}

// Stats reports export counters, so tracing's own health is visible. A tracing
// system that silently stops working is worse than none, because decisions get
// made on the assumption that an absent span means an absent operation.
type Stats struct {
	Exported int64 `json:"exported"`
	Dropped  int64 `json:"dropped"`
	Failures int64 `json:"failures"`
	Queued   int   `json:"queued"`
}

func (t *Tracer) Stats() Stats {
	if t == nil {
		return Stats{}
	}
	return Stats{
		Exported: t.exported.Load(),
		Dropped:  t.dropped.Load(),
		Failures: t.failures.Load(),
		Queued:   len(t.queue),
	}
}

// Close flushes what it can and stops.
func (t *Tracer) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.stopOnce.Do(func() { close(t.stop) })
	select {
	case <-t.stopped:
		return nil
	case <-ctx.Done():
		// Reported rather than waited on. Shutdown must not be held up by a
		// tracing backend that has stopped answering.
		return errors.New("tracing did not flush before the deadline; some spans were dropped")
	}
}

func (t *Tracer) run() {
	defer close(t.stopped)

	ticker := time.NewTicker(t.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]*Span, 0, t.cfg.BatchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		t.export(batch)
		batch = batch[:0]
	}

	for {
		select {
		case s := <-t.queue:
			batch = append(batch, s)
			if len(batch) >= t.cfg.BatchSize {
				flush()
			}

		case <-ticker.C:
			flush()

		case <-t.stop:
			// Drain what is already queued, without waiting for more.
			for {
				select {
				case s := <-t.queue:
					batch = append(batch, s)
					if len(batch) >= t.cfg.BatchSize {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// export posts one batch. Failures are counted and dropped, never retried into
// the message path.
func (t *Tracer) export(batch []*Span) {
	body, err := t.encode(batch)
	if err != nil {
		t.failures.Add(1)
		t.log.Debug("encoding spans failed", "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		t.failures.Add(1)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := t.http.Do(req)
	if err != nil {
		t.failures.Add(1)
		// Debug rather than warn. A trace backend being down should not fill the
		// log of a healthy interface engine with noise; the counter is how
		// somebody finds out.
		t.log.Debug("exporting spans failed", "err", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		t.failures.Add(1)
		t.log.Debug("trace endpoint refused the batch", "status", resp.StatusCode)
		return
	}
	t.exported.Add(int64(len(batch)))
}

// --- OTLP/JSON encoding -----------------------------------------------------
//
// The shape below is the protobuf JSON mapping of ExportTraceServiceRequest.
// Two details are easy to get wrong and produce silent non-ingestion: 64-bit
// integers are encoded as strings, and IDs are lowercase hex strings rather than
// base64 or byte arrays.

type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource    `json:"resource"`
	ScopeSpans []otlpScopeSpan `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpAttr `json:"attributes"`
}

type otlpScopeSpan struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []otlpAttr `json:"attributes,omitempty"`
	Status            otlpStatus `json:"status"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpAttr struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
	BoolValue   *bool   `json:"boolValue,omitempty"`
}

func stringAttr(k, v string) otlpAttr {
	return otlpAttr{Key: k, Value: otlpValue{StringValue: &v}}
}

func (t *Tracer) encode(batch []*Span) ([]byte, error) {
	res := otlpResource{Attributes: []otlpAttr{
		stringAttr("service.name", t.cfg.ServiceName),
	}}
	if t.cfg.ServiceVersion != "" {
		res.Attributes = append(res.Attributes, stringAttr("service.version", t.cfg.ServiceVersion))
	}
	if t.cfg.Instance != "" {
		res.Attributes = append(res.Attributes, stringAttr("service.instance.id", t.cfg.Instance))
	}

	spans := make([]otlpSpan, 0, len(batch))
	for _, s := range batch {
		spans = append(spans, s.otlp())
	}

	return json.Marshal(otlpRequest{ResourceSpans: []otlpResourceSpans{{
		Resource: res,
		ScopeSpans: []otlpScopeSpan{{
			Scope: otlpScope{Name: "perfuse", Version: t.cfg.ServiceVersion},
			Spans: spans,
		}},
	}}})
}

func (s *Span) otlp() otlpSpan {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := otlpSpan{
		TraceID:           s.ctx.TraceID.String(),
		SpanID:            s.ctx.SpanID.String(),
		Name:              s.name,
		Kind:              int(s.kind),
		StartTimeUnixNano: itoa(s.start.UnixNano()),
		EndTimeUnixNano:   itoa(s.end.UnixNano()),
		Status:            otlpStatus{Code: s.status, Message: s.errMsg},
	}
	if !s.parent.IsZero() {
		out.ParentSpanID = s.parent.String()
	}
	for _, a := range s.attrs {
		switch {
		case a.isNum:
			v := itoa(a.num)
			out.Attributes = append(out.Attributes, otlpAttr{Key: a.key, Value: otlpValue{IntValue: &v}})
		case a.isB:
			b := a.b
			out.Attributes = append(out.Attributes, otlpAttr{Key: a.key, Value: otlpValue{BoolValue: &b}})
		default:
			out.Attributes = append(out.Attributes, stringAttr(a.key, a.str))
		}
	}
	return out
}
