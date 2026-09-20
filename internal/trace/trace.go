// Package trace produces distributed traces for messages moving through a
// channel, and exports them over OTLP/HTTP with the JSON encoding.
//
// Why this exists rather than the OpenTelemetry SDK: the SDK plus an OTLP
// exporter pulls in roughly sixty modules and adds about as much to the binary as
// the whole of the rest of this program. What is actually needed here is narrow -
// W3C trace context propagation, a span model, head sampling, and a batched HTTP
// POST - and OTLP/JSON is a documented stable encoding that can be produced with
// the standard library alone. Same trade as choosing lib/pq over pgx: take the
// small thing when only part of the big thing is wanted.
//
// The consequence worth stating: there is no auto-instrumentation and no
// vendor-specific exporter. Spans are the ones this program creates deliberately,
// and they go to any OTLP endpoint - a collector, Jaeger, Grafana Tempo,
// Honeycomb, Datadog's OTLP ingest.
//
// # The property that matters more than the spans
//
// A message frequently arrives carrying a traceparent header from the system that
// sent it, and leaves toward a system that will record one of its own. Continuing
// that trace rather than starting a fresh one is what makes a single view of
// "the EHR sent this, the engine transformed it, the registry rejected it"
// possible at all. Interface problems are almost always reported as "we sent it
// and nothing happened", and that question is unanswerable without an identifier
// that survives the hop.
//
// # Tracing must never affect delivery
//
// Every operation here is best-effort. Spans are dropped when the buffer is full,
// export failures are counted and never retried into the message path, and no
// call blocks on the network. A dropped span is an inconvenience. A message
// delayed or lost because the tracing backend was slow is a clinical incident, so
// the trade is never in question.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TraceID is a 16-byte identifier shared by every span in one trace.
type TraceID [16]byte

// SpanID is an 8-byte identifier for a single span.
type SpanID [8]byte

func (t TraceID) String() string { return hex.EncodeToString(t[:]) }
func (s SpanID) String() string  { return hex.EncodeToString(s[:]) }

// IsZero reports whether the ID is unset. An all-zero ID is invalid per the W3C
// specification, which is what makes it usable as a sentinel.
func (t TraceID) IsZero() bool { return t == TraceID{} }
func (s SpanID) IsZero() bool  { return s == SpanID{} }

// Kind describes a span's role, using the OTLP numeric values directly so no
// mapping table is needed at export.
type Kind int

const (
	KindInternal Kind = 1
	KindServer   Kind = 2
	KindClient   Kind = 3
	KindProducer Kind = 4
	KindConsumer Kind = 5
)

// Status codes, again matching OTLP.
const (
	statusUnset = 0
	statusOK    = 1
	statusError = 2
)

// SpanContext is the part of a span that crosses process boundaries.
type SpanContext struct {
	TraceID TraceID
	SpanID  SpanID
	// Sampled carries the sampling decision onward. Once a trace is sampled every
	// participant should sample it, or the resulting trace has holes exactly
	// where the interesting hop was.
	Sampled bool
	// Remote marks a context parsed from an inbound header rather than created
	// here, which is worth knowing when deciding whether to trust its sampling
	// decision.
	Remote bool
}

// Valid reports whether the context can parent a span.
func (sc SpanContext) Valid() bool { return !sc.TraceID.IsZero() && !sc.SpanID.IsZero() }

// Span is one timed operation.
//
// A nil *Span is valid and every method on it is a no-op, so instrumentation can
// be written without checking whether tracing is enabled. That matters because the
// alternative is an `if tracer != nil` around every call site, which is where
// somebody eventually forgets and panics on a message.
type Span struct {
	tracer *Tracer

	ctx    SpanContext
	parent SpanID
	name   string
	kind   Kind

	start time.Time

	mu     sync.Mutex
	end    time.Time
	attrs  []attr
	status int
	errMsg string
	ended  bool
}

type attr struct {
	key string
	// Exactly one of these is used. Kept as separate fields rather than an `any`
	// so export needs no reflection and no type switch on unknown types.
	str   string
	num   int64
	isNum bool
	b     bool
	isB   bool
}

// Context returns the span's own context, for propagation.
func (s *Span) Context() SpanContext {
	if s == nil {
		return SpanContext{}
	}
	return s.ctx
}

// SetString records a string attribute.
//
// Deliberately no method taking arbitrary values: an attribute set from message
// content is how patient data ends up in a third-party observability platform
// that has no agreement covering it. Callers must pass something they have
// decided is safe, and the type signature is the reminder.
func (s *Span) SetString(key, value string) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || len(s.attrs) >= maxAttributes {
		return
	}
	s.attrs = append(s.attrs, attr{key: key, str: clip(value)})
}

// SetInt records an integer attribute.
func (s *Span) SetInt(key string, value int64) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || len(s.attrs) >= maxAttributes {
		return
	}
	s.attrs = append(s.attrs, attr{key: key, num: value, isNum: true})
}

// SetBool records a boolean attribute.
func (s *Span) SetBool(key string, value bool) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || len(s.attrs) >= maxAttributes {
		return
	}
	s.attrs = append(s.attrs, attr{key: key, b: value, isB: true})
}

// SetError marks the span failed and records the message.
//
// The error text is clipped and comes from this program's own errors, not from
// message content.
func (s *Span) SetError(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.status = statusError
	s.errMsg = clip(err.Error())
}

// SetOK marks the span explicitly successful.
func (s *Span) SetOK() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.status = statusOK
	}
}

// End closes the span and hands it to the exporter.
//
// Safe to call twice; the second call does nothing. That matters because the
// natural way to write this is a defer plus an explicit End on an early return,
// and a double-End should not produce a duplicate span.
func (s *Span) End() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.end = time.Now()
	s.mu.Unlock()

	if s.tracer != nil {
		s.tracer.finish(s)
	}
}

// Duration reports how long the span took, or how long it has been open.
func (s *Span) Duration() time.Duration {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s.end.Sub(s.start)
	}
	return time.Since(s.start)
}

const (
	// maxAttributes bounds one span. A runaway loop adding attributes would
	// otherwise grow a span until the export payload is rejected, losing the whole
	// batch rather than the one bad span.
	maxAttributes = 64
	// maxAttrLength bounds one value.
	maxAttrLength = 512
)

func clip(v string) string {
	if len(v) <= maxAttrLength {
		return v
	}
	return v[:maxAttrLength] + "…"
}

// --- context plumbing -------------------------------------------------------

type ctxKey struct{}

// WithSpan returns a context carrying the span, so functions further down can
// parent their own work without the span being threaded through every signature.
func WithSpan(ctx context.Context, s *Span) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the span in the context, or nil.
//
// nil is a working span, so callers do not need to check.
func FromContext(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(ctxKey{}).(*Span)
	return s
}

type remoteKey struct{}

// WithRemote attaches an inbound span context to a context.
//
// Separate from WithSpan because they mean different things: WithSpan carries a
// span this process created and is a parent, while WithRemote carries what another
// system told us and is a parent only if we have nothing closer. A source parses
// the header, and the channel decides what to do with it - which keeps header
// formats out of the engine and channel semantics out of the transports.
func WithRemote(ctx context.Context, sc SpanContext) context.Context {
	if !sc.Valid() {
		return ctx
	}
	return context.WithValue(ctx, remoteKey{}, sc)
}

// RemoteFromContext returns the inbound span context, if a source recorded one.
func RemoteFromContext(ctx context.Context) SpanContext {
	if ctx == nil {
		return SpanContext{}
	}
	sc, _ := ctx.Value(remoteKey{}).(SpanContext)
	return sc
}

// --- W3C trace context ------------------------------------------------------

// TraceparentHeader is the W3C header name.
const TraceparentHeader = "traceparent"

// Traceparent formats a span context as a W3C traceparent value.
//
// Returns "" for an invalid context rather than emitting a malformed header,
// because a malformed traceparent is worse than none: a compliant receiver must
// discard it and start a new trace, and the resulting broken link is far harder
// to diagnose than an absent one.
func Traceparent(sc SpanContext) string {
	if !sc.Valid() {
		return ""
	}
	flags := "00"
	if sc.Sampled {
		flags = "01"
	}
	return "00-" + sc.TraceID.String() + "-" + sc.SpanID.String() + "-" + flags
}

// ParseTraceparent reads a W3C traceparent value.
//
// Unparseable input yields an invalid context and no error, deliberately. A
// caller's only sensible response to a malformed inbound header is to start a
// fresh trace, and making that an error invites somebody to reject the message
// over a header problem.
func ParseTraceparent(v string) SpanContext {
	v = strings.TrimSpace(v)
	// version(2) + traceid(32) + spanid(16) + flags(2) + 3 dashes
	if len(v) < 55 {
		return SpanContext{}
	}
	parts := strings.Split(v, "-")
	if len(parts) < 4 {
		return SpanContext{}
	}
	// Only version 00 is defined. A future version must still be parseable for
	// its first four fields, so anything that looks like hex is accepted rather
	// than refused - refusing would break the trace the day the spec moves.
	if len(parts[0]) != 2 || parts[0] == "ff" {
		return SpanContext{}
	}
	if len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) < 2 {
		return SpanContext{}
	}

	tid, err := hex.DecodeString(parts[1])
	if err != nil {
		return SpanContext{}
	}
	sid, err := hex.DecodeString(parts[2])
	if err != nil {
		return SpanContext{}
	}
	flags, err := hex.DecodeString(parts[3][:2])
	if err != nil {
		return SpanContext{}
	}

	var sc SpanContext
	copy(sc.TraceID[:], tid)
	copy(sc.SpanID[:], sid)
	// All-zero IDs are invalid per the specification.
	if sc.TraceID.IsZero() || sc.SpanID.IsZero() {
		return SpanContext{}
	}
	sc.Sampled = flags[0]&0x01 == 1
	sc.Remote = true
	return sc
}

// --- ID generation ----------------------------------------------------------

// newTraceID generates a random trace ID.
//
// crypto/rand rather than math/rand: trace IDs from separate instances must not
// collide, and a seeded PRNG in several replicas started by the same deployment
// is exactly how that happens.
func newTraceID() TraceID {
	var t TraceID
	for {
		_, _ = rand.Read(t[:])
		if !t.IsZero() {
			return t
		}
	}
}

func newSpanID() SpanID {
	var s SpanID
	for {
		_, _ = rand.Read(s[:])
		if !s.IsZero() {
			return s
		}
	}
}

// itoa formats for the OTLP JSON encoding, where 64-bit integers are strings.
//
// This is the protobuf JSON mapping and it is not optional: emitting a bare
// number for a nanosecond timestamp either loses precision in a JavaScript
// consumer or gets the batch rejected outright.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
