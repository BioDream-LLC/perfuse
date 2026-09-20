package trace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// receiver is a stand-in for an OTLP collector. Using a real HTTP server rather
// than asserting on the encoder's output is the only way to know the wire format
// is actually accepted; a unit test on the struct would pass just as happily with
// the field names spelled wrong.
type receiver struct {
	*httptest.Server
	mu       sync.Mutex
	requests []otlpRequest
	raw      [][]byte
	status   int
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{status: http.StatusOK}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		r.mu.Lock()
		defer r.mu.Unlock()
		r.raw = append(r.raw, body)

		var parsed otlpRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Errorf("the collector could not parse the batch: %v\n%s", err, body)
		}
		r.requests = append(r.requests, parsed)
		w.WriteHeader(r.status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) spans() []otlpSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []otlpSpan
	for _, req := range r.requests {
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				out = append(out, ss.Spans...)
			}
		}
	}
	return out
}

// count and rawAt exist because reading len(r.requests) beside a handler that
// appends to it is a data race - which is exactly what the detector caught.
func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *receiver) rawCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.raw)
}

func (r *receiver) resourceAttrs() []otlpAttr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		return nil
	}
	return r.requests[0].ResourceSpans[0].Resource.Attributes
}

func (r *receiver) rawFirst() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.raw) == 0 {
		return nil
	}
	return r.raw[0]
}

func (r *receiver) setStatus(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = code
}

func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func tracerFor(t *testing.T, rec *receiver) *Tracer {
	t.Helper()
	tr, err := New(Config{
		Endpoint:       rec.URL,
		ServiceName:    "perfuse-test",
		ServiceVersion: "v0.0.1",
		Instance:       "instance-a",
		BatchSize:      1,
		FlushInterval:  10 * time.Millisecond,
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tr.Close(ctx)
	})
	return tr
}

// --- the nil tracer must work -----------------------------------------------

func TestNilTracerIsUsable(t *testing.T) {
	// The whole design depends on this. If a nil tracer panicked, every call site
	// would need a guard, and the one somebody forgets would panic on a message.
	var tr *Tracer

	ctx, span := tr.Start(context.Background(), "x", KindServer, SpanContext{})
	span.SetString("k", "v")
	span.SetInt("n", 1)
	span.SetBool("b", true)
	span.SetError(errors.New("boom"))
	span.SetOK()
	span.End()
	span.End()

	if span != nil {
		t.Error("a nil tracer produced a non-nil span")
	}
	if FromContext(ctx) != nil {
		t.Error("a nil span was stored in the context")
	}
	if got := tr.Stats(); got != (Stats{}) {
		t.Errorf("Stats on a nil tracer = %+v", got)
	}
	if err := tr.Close(context.Background()); err != nil {
		t.Errorf("Close on a nil tracer = %v", err)
	}
}

func TestNoEndpointMeansNoTracer(t *testing.T) {
	// Tracing off is the default, and it must produce a working no-op rather than
	// an error somebody has to handle at every startup path.
	tr, err := New(Config{}, quiet())
	if err != nil {
		t.Fatalf("New with no endpoint = %v", err)
	}
	if tr != nil {
		t.Error("New with no endpoint returned a tracer")
	}
}

// --- W3C trace context ------------------------------------------------------

func TestTraceparentRoundTrips(t *testing.T) {
	sc := SpanContext{TraceID: newTraceID(), SpanID: newSpanID(), Sampled: true}

	got := ParseTraceparent(Traceparent(sc))
	if got.TraceID != sc.TraceID {
		t.Errorf("trace id: got %s want %s", got.TraceID, sc.TraceID)
	}
	if got.SpanID != sc.SpanID {
		t.Errorf("span id: got %s want %s", got.SpanID, sc.SpanID)
	}
	if !got.Sampled {
		t.Error("the sampling flag was lost")
	}
	if !got.Remote {
		t.Error("a parsed context should be marked remote")
	}
}

func TestTraceparentCarriesTheUnsampledFlag(t *testing.T) {
	sc := SpanContext{TraceID: newTraceID(), SpanID: newSpanID(), Sampled: false}
	if got := ParseTraceparent(Traceparent(sc)); got.Sampled {
		t.Error("an unsampled context came back sampled")
	}
}

func TestTraceparentFormat(t *testing.T) {
	sc := SpanContext{TraceID: newTraceID(), SpanID: newSpanID(), Sampled: true}
	got := Traceparent(sc)

	// 2 + 1 + 32 + 1 + 16 + 1 + 2
	if len(got) != 55 {
		t.Errorf("traceparent is %d chars, want 55: %q", len(got), got)
	}
	if got[:3] != "00-" {
		t.Errorf("version prefix = %q", got[:3])
	}
	if got[len(got)-2:] != "01" {
		t.Errorf("sampled flags = %q", got[len(got)-2:])
	}
}

func TestInvalidTraceparentYieldsAnInvalidContext(t *testing.T) {
	// Every one of these must produce an unusable context and no panic. A
	// malformed inbound header is somebody else's bug and must not become a
	// rejected message.
	for _, in := range []string{
		"",
		"   ",
		"nonsense",
		"00",
		"00-",
		"00-tooshort-tooshort-01",
		// All-zero IDs are invalid per the specification.
		"00-00000000000000000000000000000000-0000000000000000-01",
		"00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01",
		// Version ff is forbidden.
		"ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		// Not hex.
		"00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-b7ad6b7169203331-01",
		"00-0af7651916cd43dd8448eb211c80319c-zzzzzzzzzzzzzzzz-01",
	} {
		if got := ParseTraceparent(in); got.Valid() {
			t.Errorf("ParseTraceparent(%q) produced a valid context", in)
		}
	}
}

func TestTraceparentAcceptsTheCanonicalExample(t *testing.T) {
	// The example from the W3C specification, so a regression in parsing is caught
	// against something external rather than against my own formatter.
	got := ParseTraceparent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if !got.Valid() {
		t.Fatal("the canonical W3C example did not parse")
	}
	if got.TraceID.String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace id = %s", got.TraceID)
	}
	if got.SpanID.String() != "b7ad6b7169203331" {
		t.Errorf("span id = %s", got.SpanID)
	}
	if !got.Sampled {
		t.Error("flags 01 should mean sampled")
	}
}

func TestTraceparentToleratesFutureVersionsAndExtraFields(t *testing.T) {
	// A future version must still parse its first four fields, or the trace breaks
	// on the day the specification moves.
	got := ParseTraceparent("01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra")
	if !got.Valid() {
		t.Error("a future version with extra fields should still parse")
	}
}

func TestInvalidContextProducesNoHeader(t *testing.T) {
	// Emitting a malformed traceparent is worse than emitting none: a compliant
	// receiver discards it and starts a new trace, and the broken link is harder to
	// find than a missing one.
	if got := Traceparent(SpanContext{}); got != "" {
		t.Errorf("Traceparent of an invalid context = %q, want empty", got)
	}
}

// --- span behaviour ---------------------------------------------------------

func TestSpanIsExported(t *testing.T) {
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "channel.handle", KindServer, SpanContext{})
	span.SetString("channel", "adt-inbound")
	span.SetInt("segments", 7)
	span.SetBool("queued", false)
	span.End()

	waitFor(t, "the span to be exported", func() bool { return len(rec.spans()) == 1 })

	got := rec.spans()[0]
	if got.Name != "channel.handle" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Kind != int(KindServer) {
		t.Errorf("kind = %d, want %d", got.Kind, KindServer)
	}
	if len(got.TraceID) != 32 {
		t.Errorf("trace id %q is not 32 hex chars", got.TraceID)
	}
	if len(got.SpanID) != 16 {
		t.Errorf("span id %q is not 16 hex chars", got.SpanID)
	}
	if got.ParentSpanID != "" {
		t.Errorf("a root span reported a parent: %q", got.ParentSpanID)
	}
	if len(got.Attributes) != 3 {
		t.Fatalf("got %d attributes, want 3: %+v", len(got.Attributes), got.Attributes)
	}
}

func TestTimestampsAreStringsNotNumbers(t *testing.T) {
	// The protobuf JSON mapping requires 64-bit integers as strings. Emitting a
	// bare number either loses precision in a JavaScript consumer or gets the batch
	// rejected, and both failures are silent.
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.SetInt("n", 42)
	span.End()

	waitFor(t, "the span", func() bool { return rec.rawCount() == 1 })

	body := rec.rawFirst()

	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		t.Fatal(err)
	}
	rs := generic["resourceSpans"].([]any)[0].(map[string]any)
	ss := rs["scopeSpans"].([]any)[0].(map[string]any)
	sp := ss["spans"].([]any)[0].(map[string]any)

	if _, ok := sp["startTimeUnixNano"].(string); !ok {
		t.Errorf("startTimeUnixNano is %T, want string", sp["startTimeUnixNano"])
	}
	if _, ok := sp["endTimeUnixNano"].(string); !ok {
		t.Errorf("endTimeUnixNano is %T, want string", sp["endTimeUnixNano"])
	}

	attrs := sp["attributes"].([]any)
	val := attrs[0].(map[string]any)["value"].(map[string]any)
	if _, ok := val["intValue"].(string); !ok {
		t.Errorf("intValue is %T, want string", val["intValue"])
	}
}

func TestResourceAttributesIdentifyTheInstance(t *testing.T) {
	// Without service.instance.id, spans from three replicas are
	// indistinguishable, which defeats the point of tracing a scaled deployment.
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.End()
	waitFor(t, "the span", func() bool { return rec.count() == 1 })

	attrs := rec.resourceAttrs()

	want := map[string]string{
		"service.name":        "perfuse-test",
		"service.version":     "v0.0.1",
		"service.instance.id": "instance-a",
	}
	for _, a := range attrs {
		if exp, ok := want[a.Key]; ok {
			if a.Value.StringValue == nil || *a.Value.StringValue != exp {
				t.Errorf("%s = %v, want %q", a.Key, a.Value.StringValue, exp)
			}
			delete(want, a.Key)
		}
	}
	for k := range want {
		t.Errorf("resource attribute %s was missing", k)
	}
}

func TestChildSpanSharesTheTraceAndNamesItsParent(t *testing.T) {
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	ctx, parent := tr.Start(context.Background(), "channel.handle", KindServer, SpanContext{})
	_, child := tr.Start(ctx, "destination.send", KindClient, SpanContext{})
	child.End()
	parent.End()

	waitFor(t, "both spans", func() bool { return len(rec.spans()) == 2 })

	var p, c otlpSpan
	for _, s := range rec.spans() {
		if s.Name == "channel.handle" {
			p = s
		} else {
			c = s
		}
	}
	if c.TraceID != p.TraceID {
		t.Errorf("child trace %s != parent trace %s", c.TraceID, p.TraceID)
	}
	if c.ParentSpanID != p.SpanID {
		t.Errorf("child parent = %s, want %s", c.ParentSpanID, p.SpanID)
	}
}

func TestRemoteContextContinuesTheCallersTrace(t *testing.T) {
	// This is the property the whole package exists for: a message arriving with a
	// traceparent from the sending system produces spans in *their* trace, so one
	// view spans both systems.
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	remote := ParseTraceparent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	_, span := tr.Start(context.Background(), "channel.handle", KindServer, remote)
	span.End()

	waitFor(t, "the span", func() bool { return len(rec.spans()) == 1 })

	got := rec.spans()[0]
	if got.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace id = %s; the caller's trace was not continued", got.TraceID)
	}
	if got.ParentSpanID != "b7ad6b7169203331" {
		t.Errorf("parent = %s, want the caller's span", got.ParentSpanID)
	}
}

func TestAContextParentBeatsARemoteOne(t *testing.T) {
	// An in-process parent is more specific than an inbound header, and preferring
	// the header would detach a child span from its own parent.
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	ctx, parent := tr.Start(context.Background(), "outer", KindServer, SpanContext{})
	remote := ParseTraceparent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	_, child := tr.Start(ctx, "inner", KindInternal, remote)

	if child.Context().TraceID != parent.Context().TraceID {
		t.Error("the remote context overrode an in-process parent")
	}
	child.End()
	parent.End()
}

func TestErrorStatusIsRecorded(t *testing.T) {
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "destination.send", KindClient, SpanContext{})
	span.SetError(errors.New("connection refused"))
	span.End()

	waitFor(t, "the span", func() bool { return len(rec.spans()) == 1 })

	got := rec.spans()[0]
	if got.Status.Code != statusError {
		t.Errorf("status = %d, want %d", got.Status.Code, statusError)
	}
	if got.Status.Message != "connection refused" {
		t.Errorf("message = %q", got.Status.Message)
	}
}

func TestDoubleEndExportsOneSpan(t *testing.T) {
	// The natural way to write this is a defer plus an explicit End on an early
	// return. A duplicate span would show the same operation happening twice.
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.End()
	span.End()
	span.End()

	waitFor(t, "the span", func() bool { return len(rec.spans()) >= 1 })
	time.Sleep(60 * time.Millisecond)

	if n := len(rec.spans()); n != 1 {
		t.Errorf("got %d spans, want 1", n)
	}
}

func TestAttributesAfterEndAreIgnored(t *testing.T) {
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.End()
	span.SetString("late", "value")

	waitFor(t, "the span", func() bool { return len(rec.spans()) == 1 })
	for _, a := range rec.spans()[0].Attributes {
		if a.Key == "late" {
			t.Error("an attribute set after End was exported")
		}
	}
}

func TestAttributeCountIsBounded(t *testing.T) {
	// A runaway loop would otherwise grow a span until the payload is rejected,
	// losing the whole batch rather than the one bad span.
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	for i := 0; i < maxAttributes*3; i++ {
		span.SetString("k"+strconv.Itoa(i), "v")
	}
	span.End()

	waitFor(t, "the span", func() bool { return len(rec.spans()) == 1 })
	if n := len(rec.spans()[0].Attributes); n > maxAttributes {
		t.Errorf("got %d attributes, want at most %d", n, maxAttributes)
	}
}

func TestAttributeValueIsClipped(t *testing.T) {
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	long := make([]byte, maxAttrLength*4)
	for i := range long {
		long[i] = 'x'
	}
	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.SetString("big", string(long))
	span.End()

	waitFor(t, "the span", func() bool { return len(rec.spans()) == 1 })
	v := rec.spans()[0].Attributes[0].Value.StringValue
	if v == nil || len(*v) > maxAttrLength+8 {
		t.Errorf("attribute was not clipped: %d bytes", len(*v))
	}
}

func TestEmptyAttributeKeyIsRejected(t *testing.T) {
	rec := newReceiver(t)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.SetString("", "value")
	span.End()

	waitFor(t, "the span", func() bool { return len(rec.spans()) == 1 })
	if n := len(rec.spans()[0].Attributes); n != 0 {
		t.Errorf("an empty key was exported: %+v", rec.spans()[0].Attributes)
	}
}

// --- failure handling -------------------------------------------------------

func TestExportFailuresAreCountedNotRetried(t *testing.T) {
	// A trace backend refusing batches must not turn into unbounded retries in a
	// process whose actual job is delivering messages.
	rec := newReceiver(t)
	rec.setStatus(http.StatusInternalServerError)
	tr := tracerFor(t, rec)

	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.End()

	waitFor(t, "a recorded failure", func() bool { return tr.Stats().Failures > 0 })
	if got := tr.Stats().Exported; got != 0 {
		t.Errorf("exported = %d after a refusal, want 0", got)
	}
}

func TestAFullQueueDropsRatherThanBlocks(t *testing.T) {
	// The load-shedding decision. A slow tracing backend must never become a slow
	// interface engine.
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked)

	tr, err := New(Config{
		Endpoint:      srv.URL,
		MaxQueue:      2,
		BatchSize:     1,
		FlushInterval: time.Hour,
		Timeout:       time.Hour,
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
			span.End()
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ending spans blocked on a stalled exporter")
	}
	if tr.Stats().Dropped == 0 {
		t.Error("nothing was dropped despite a stalled exporter and a queue of 2")
	}
}

func TestCloseRespectsItsDeadline(t *testing.T) {
	// Shutdown must not be held up by a tracing backend that stopped answering.
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked)

	tr, _ := New(Config{Endpoint: srv.URL, BatchSize: 1, FlushInterval: time.Millisecond, Timeout: time.Hour}, quiet())
	_, span := tr.Start(context.Background(), "x", KindInternal, SpanContext{})
	span.End()
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := tr.Close(ctx)
	if time.Since(start) > time.Second {
		t.Fatalf("Close took %v; it must respect its deadline", time.Since(start))
	}
	if err == nil {
		t.Error("Close should report that it could not flush")
	}
}

// --- sampling ---------------------------------------------------------------

func TestSamplingIsDeterministicOnTheTraceID(t *testing.T) {
	// Every service seeing the same trace must independently reach the same
	// decision, or traces have holes where a hop declined to record.
	tr, _ := New(Config{Endpoint: "http://127.0.0.1:1", SampleRatio: 0.5}, quiet())
	defer tr.Close(context.Background())

	id := newTraceID()
	first := tr.sample(id)
	for i := 0; i < 100; i++ {
		if tr.sample(id) != first {
			t.Fatal("the sampling decision for one trace id was not stable")
		}
	}
}

func TestSamplingRatioIsRoughlyHonoured(t *testing.T) {
	tr, _ := New(Config{Endpoint: "http://127.0.0.1:1", SampleRatio: 0.25}, quiet())
	defer tr.Close(context.Background())

	sampled := 0
	const n = 20000
	for i := 0; i < n; i++ {
		if tr.sample(newTraceID()) {
			sampled++
		}
	}
	ratio := float64(sampled) / n
	if ratio < 0.20 || ratio > 0.30 {
		t.Errorf("sampled %.3f of traces, want about 0.25", ratio)
	}
}

func TestFullSamplingRecordsEverything(t *testing.T) {
	tr, _ := New(Config{Endpoint: "http://127.0.0.1:1", SampleRatio: 1}, quiet())
	defer tr.Close(context.Background())

	for i := 0; i < 1000; i++ {
		if !tr.sample(newTraceID()) {
			t.Fatal("a ratio of 1 declined to sample")
		}
	}
}

func TestUnsampledSpanStillPropagatesItsContext(t *testing.T) {
	// A downstream system may sample where we did not, and it needs real IDs to do
	// so. An unsampled span is not exported but must still be a valid parent.
	tr, _ := New(Config{Endpoint: "http://127.0.0.1:1", SampleRatio: 1}, quiet())
	defer tr.Close(context.Background())

	remote := SpanContext{TraceID: newTraceID(), SpanID: newSpanID(), Sampled: false, Remote: true}
	_, span := tr.Start(context.Background(), "x", KindServer, remote)

	sc := span.Context()
	if !sc.Valid() {
		t.Fatal("an unsampled span has an invalid context")
	}
	if sc.Sampled {
		t.Error("the unsampled decision was not honoured")
	}
	if Traceparent(sc) == "" {
		t.Error("an unsampled span produced no traceparent")
	}
	span.End()
	if got := tr.Stats().Exported; got != 0 {
		t.Errorf("an unsampled span was exported: %d", got)
	}
}

// --- configuration ----------------------------------------------------------

func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"tracing off", Config{}, false},
		{"http", Config{Endpoint: "http://localhost:4318"}, false},
		{"https", Config{Endpoint: "https://otel.example.com"}, false},
		{"no scheme", Config{Endpoint: "localhost:4318"}, true},
		{"wrong scheme", Config{Endpoint: "grpc://localhost:4317"}, true},
		{"ratio above one", Config{Endpoint: "http://localhost:4318", SampleRatio: 2}, true},
		{"negative ratio", Config{Endpoint: "http://localhost:4318", SampleRatio: -1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestPlainHTTPToARemoteHostWarns(t *testing.T) {
	// Trace attributes are configuration rather than patient data today, but they
	// are the sort of thing that accumulates content, so the warning is worth
	// having.
	c := Config{Endpoint: "http://otel.example.com:4318"}
	if len(c.Warnings()) == 0 {
		t.Error("plain HTTP to a remote host produced no warning")
	}

	local := Config{Endpoint: "http://127.0.0.1:4318"}
	for _, w := range local.Warnings() {
		if w != "" && containsPlainHTTP(w) {
			t.Errorf("loopback warned about plain HTTP: %q", w)
		}
	}
}

func containsPlainHTTP(s string) bool {
	for i := 0; i+10 <= len(s); i++ {
		if s[i:i+10] == "plain HTTP" {
			return true
		}
	}
	return false
}

func TestSamplingBelowOneWarns(t *testing.T) {
	// The message somebody asks about is the unusual one, and it is exactly the one
	// sampling is likely to have discarded. Saying so beats a support call.
	c := Config{Endpoint: "http://localhost:4318", SampleRatio: 0.1}
	if len(c.Warnings()) == 0 {
		t.Error("partial sampling produced no warning")
	}
}

func TestIsLoopback(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"http://localhost:4318", true},
		{"http://127.0.0.1:4318", true},
		{"http://127.0.0.1", true},
		{"https://[::1]:4318", true},
		{"http://otel.example.com:4318", false},
		{"http://10.0.0.5:4318", false},
	} {
		if got := isLoopback(tc.in); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
