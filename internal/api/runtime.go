package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/tenant"
	"github.com/biodream-llc/perfuse/internal/trace"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/admit"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/hl7dict"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
	"github.com/biodream-llc/perfuse/mllp"
)

// Runtime owns the running engine, so the interface can show what is happening and
// start or stop it.
//
// Without this the web interface is a text editor with a login: you can change a
// channel but not see whether it is running, how many messages it has handled, or
// look at one that failed. That gap is the difference between a configuration tool
// and an operations console.
type Runtime struct {
	// Channels supplies channel definitions from disk.
	Repo *ChannelRepo
	// Messages persists what each channel handled. Optional.
	Messages *msgstore.Store
	// FHIR is the embedded FHIR store, if one is configured.
	FHIR *fhirserver.Store
	// Metrics collects what the engine is doing. Optional; when nil the metrics
	// endpoints say so rather than returning empty series that look like an idle
	// system.
	Metrics *metrics.Collector

	// Queues is the durable delivery queue, nil when queueing is unavailable.
	Queues *engine.Queues

	// Admit bounds deliveries in flight, shared with every channel this runtime starts.
	//
	// The same controller the engine uses, on purpose. A channel started from the interface must draw on the
	// same descriptor budget as one started at boot, or the limit is per channel and therefore not a limit.
	Admit *admit.Controller

	// QueryState records what a DICOM query source has already seen, nil when there is no database.
	//
	// A channel that polls an archive refuses to start without it rather than running stateless: with nothing to
	// remember it re-emits every matching study on every poll, which against a real archive is a message storm that
	// looks like the archive malfunctioning.
	QueryState engine.DICOMQueryState

	// Tracer exports message traces. Nil when tracing is not configured.
	Tracer *trace.Tracer

	// Contracts checks each channel's contract against recent traffic, on a timer.
	//
	// Nil when nothing has a contract. Created lazily by EnableContracts so a server with no contracts does
	// no work and holds no state for them.
	Contracts *contractChecker

	// Tenant is which tenant this runtime serves. Empty in single-tenant operation,
	// which reports as the default tenant in metrics rather than as an empty label.
	Tenant tenant.ID

	mu      sync.RWMutex
	running map[string]*engine.Channel
	started map[string]time.Time
	failed  map[string]string
}

// NewRuntime prepares a runtime.
func NewRuntime(repo *ChannelRepo, messages *msgstore.Store, fhirStore *fhirserver.Store) *Runtime {
	// The process-wide delivery limiter, taken rather than passed in. A caller that had to remember to set
	// it is a caller that will forget, and forgetting means channels started from the interface draw on no
	// budget at all while ones started at boot are bounded.
	controller, _, _ := admit.Shared()

	return &Runtime{
		Repo:     repo,
		Messages: messages,
		FHIR:     fhirStore,
		Admit:    controller,
		running:  map[string]*engine.Channel{},
		started:  map[string]time.Time{},
		failed:   map[string]string{},
	}
}

// ChannelState is the live state of one channel.
type ChannelState struct {
	Name      string     `json:"name"`
	Running   bool       `json:"running"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Error     string     `json:"error,omitempty"`

	Received    int64 `json:"received"`
	Delivered   int64 `json:"delivered"`
	Filtered    int64 `json:"filtered"`
	Partial     int64 `json:"partial"`
	Failed      int64 `json:"failed"`
	Unparseable int64 `json:"unparseable"`

	Destinations []DestinationState `json:"destinations,omitempty"`
}

// DestinationState is per-destination counts for a running channel.
type DestinationState struct {
	Name      string `json:"name"`
	Delivered int64  `json:"delivered"`
	Failed    int64  `json:"failed"`
	Filtered  int64  `json:"filtered"`
}

// Start starts one channel by name.
func (rt *Runtime) Start(name string, log *slog.Logger) error {
	rt.mu.Lock()
	if _, already := rt.running[name]; already {
		rt.mu.Unlock()
		return fmt.Errorf("channel %q is already running", name)
	}
	rt.mu.Unlock()

	cfg, err := rt.Repo.Get(name)
	if err != nil {
		return err
	}

	// The runtime is the router: it is the thing that knows which channels are running. Passing it
	// here is what makes a channel destination work.
	ch, err := engine.NewChannel(cfg, engine.RoutingSenderFactory(rt), log)
	if err != nil {
		rt.mu.Lock()
		rt.failed[name] = err.Error()
		rt.mu.Unlock()
		return err
	}

	if rt.Messages != nil {
		ch.SetRecorder(msgstore.NewRecorder(rt.Messages, log))
	}
	if rt.Metrics != nil {
		ch.SetMetrics(rt.Metrics)
		defer rt.publishChannelGauges()
	}
	if rt.Queues != nil {
		ch.SetQueues(rt.Queues)
	}
	if rt.QueryState != nil {
		ch.SetQueryState(rt.QueryState)
	}
	// Nil when tracing is off, and a nil tracer is a working no-op, so this is
	// unconditional.
	ch.SetTracer(rt.Tracer)
	ch.SetAdmit(rt.Admit)
	// Labels this channel's metrics. Without it, two tenants with a channel of the
	// same name share one series, and one organisation's dashboard shows another's
	// message volumes.
	ch.SetTenant(string(rt.Tenant))

	if err := ch.Start(); err != nil {
		rt.mu.Lock()
		rt.failed[name] = err.Error()
		rt.mu.Unlock()
		return err
	}

	rt.mu.Lock()
	rt.running[name] = ch
	rt.started[name] = time.Now().UTC()
	delete(rt.failed, name)
	rt.mu.Unlock()
	return nil
}

// Stop stops one channel, waiting for messages in flight.
func (rt *Runtime) Stop(ctx context.Context, name string) error {
	defer rt.publishChannelGauges()

	rt.mu.Lock()
	ch, ok := rt.running[name]
	if !ok {
		rt.mu.Unlock()
		return fmt.Errorf("channel %q is not running", name)
	}
	delete(rt.running, name)
	delete(rt.started, name)
	rt.mu.Unlock()

	// Stopping waits for the current message rather than cutting it off, because
	// cutting a connection mid-message loses the acknowledgement and makes the
	// sender resend work that was already done.
	return ch.Stop(ctx)
}

// StopAll stops every running channel.
func (rt *Runtime) StopAll(ctx context.Context) {
	rt.mu.Lock()
	names := make([]string, 0, len(rt.running))
	for name := range rt.running {
		names = append(names, name)
	}
	rt.mu.Unlock()

	for _, name := range names {
		_ = rt.Stop(ctx, name)
	}
}

// StartEnabled starts every channel whose file says it is enabled.
func (rt *Runtime) StartEnabled(log *slog.Logger) []error {
	valid, _, err := rt.Repo.List()
	if err != nil {
		return []error{err}
	}

	// Checked before anything binds. Two channels wanting one port is invisible to
	// each of them individually; only the second to start fails, and it then looks
	// configured while the sender pointed at it gets connection refused. Reported as
	// a single error naming both, rather than as a bind failure buried in the log of
	// whichever one happened to lose the race.
	bindings := config.NewBindings()
	for _, summary := range valid {
		if !summary.Enabled {
			continue
		}
		// summary.File is a base name, not a path. Joining with the repository
		// directory rather than trusting it: the first version of this passed the
		// bare name to LoadFile, every load failed, the error was skipped as
		// "broken channel", and the whole check silently passed while doing
		// nothing. A conflict detector that quietly detects nothing is worse than
		// none, because it is trusted.
		c, loadErr := config.LoadFile(filepath.Join(rt.Repo.Dir, summary.File))
		if loadErr != nil {
			continue // reported separately by the caller as a broken channel
		}
		bindings.Add("", c)
	}
	if err := bindings.Check(); err != nil {
		return []error{err}
	}

	var errs []error
	for _, summary := range valid {
		if !summary.Enabled {
			continue
		}
		if err := rt.Start(summary.Name, log); err != nil {
			errs = append(errs, fmt.Errorf("channel %q: %w", summary.Name, err))
		}
	}
	return errs
}

// States returns the live state of every channel on disk, running or not.
//
// Never returns nil. The result is serialised straight into the status response, where the contract
// says a list, and a nil slice would marshal to null and crash a caller that trusted it. This returned
// nil when the channels directory could not be read - a mount dropping out, or permissions changing -
// which is exactly the moment the dashboard needs to stay up in order to say so.
func (rt *Runtime) States() []ChannelState {
	valid, _, err := rt.Repo.List()
	if err != nil {
		return []ChannelState{}
	}

	rt.mu.RLock()
	defer rt.mu.RUnlock()

	out := make([]ChannelState, 0, len(valid))
	for _, summary := range valid {
		state := ChannelState{Name: summary.Name}

		if failure, ok := rt.failed[summary.Name]; ok {
			state.Error = failure
		}

		ch, running := rt.running[summary.Name]
		state.Running = running
		if running {
			if at, ok := rt.started[summary.Name]; ok {
				started := at
				state.StartedAt = &started
			}

			stats := ch.Stats()
			state.Received = stats.Received
			state.Delivered = stats.Delivered
			state.Filtered = stats.Filtered
			state.Partial = stats.Partial
			state.Failed = stats.Failed
			state.Unparseable = stats.Unparseable

			names := map[string]bool{}
			for name := range stats.DestDelivered {
				names[name] = true
			}
			for name := range stats.DestFailed {
				names[name] = true
			}
			for name := range stats.DestFiltered {
				names[name] = true
			}

			dests := make([]string, 0, len(names))
			for name := range names {
				dests = append(dests, name)
			}
			sort.Strings(dests)

			for _, name := range dests {
				state.Destinations = append(state.Destinations, DestinationState{
					Name:      name,
					Delivered: stats.DestDelivered[name],
					Failed:    stats.DestFailed[name],
					Filtered:  stats.DestFiltered[name],
				})
			}
		}

		out = append(out, state)
	}
	return out
}

// IsRunning reports whether a channel is running.
func (rt *Runtime) IsRunning(name string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	_, ok := rt.running[name]
	return ok
}

// ---------- HTTP handlers ----------

func (s *Server) requireRuntime(w http.ResponseWriter, r *http.Request) bool {
	if s.Runtime == nil {
		s.fail(w, r, http.StatusServiceUnavailable,
			"the engine is not running in this process")
		return false
	}
	return true
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireRuntime(w, r) {
		return
	}

	body := statusBody(rt)

	if rt.FHIR != nil {
		counts, err := rt.FHIR.Counts(r.Context())
		if err == nil {
			body["fhirResources"] = counts
		}
		body["fhirVersion"] = string(rt.FHIR.Version())
	}

	s.ok(w, body)
}

func (s *Server) handleStartChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireRuntime(w, r) {
		return
	}
	name := r.PathValue("name")

	if err := rt.Start(name, s.log()); err != nil {
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}

	s.log().Info("channel started from the interface", "channel", name, "user", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "channel.start", Target: name, IP: clientIP(r),
	})
	s.ok(w, map[string]string{"status": "started"})
}

func (s *Server) handleStopChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireRuntime(w, r) {
		return
	}
	name := r.PathValue("name")

	// Checked against the tenant's own channels before touching the engine.
	//
	// Without this, asking to stop a channel belonging to another tenant answered 409 "is not running" - which is
	// true, and which confirms the name is one this system recognises. A name is a small disclosure, but it is a
	// disclosure, and 404 costs nothing.
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}
	if _, err := channels.Get(name); err != nil {
		s.failErr(w, r, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	if err := rt.Stop(ctx, name); err != nil {
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}

	s.log().Info("channel stopped from the interface", "channel", name, "user", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "channel.stop", Target: name, IP: clientIP(r),
	})
	s.ok(w, map[string]string{"status": "stopped"})
}

// ---------- messages ----------

func (s *Server) requireMessages(w http.ResponseWriter, r *http.Request) bool {
	if s.Runtime == nil || s.Runtime.Messages == nil {
		s.fail(w, r, http.StatusServiceUnavailable,
			"message storage is not enabled in this process")
		return false
	}
	return true
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}

	q := r.URL.Query()
	query := msgstore.Query{
		// From the session, never from the request. A tenant supplied by the caller is not a filter, it is a
		// parameter an attacker chooses.
		TenantID:     string(sess.TenantID),
		Channel:      q.Get("channel"),
		Outcome:      msgstore.Outcome(q.Get("outcome")),
		MessageType:  q.Get("type"),
		TriggerEvent: q.Get("event"),
		ControlID:    q.Get("controlId"),
		Sender:       q.Get("sender"),
		Search:       q.Get("search"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			query.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			query.Offset = n
		}
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			query.Since = t
		}
	}

	messages, total, err := rt.Messages.List(r.Context(), query)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if messages == nil {
		messages = []msgstore.Message{}
	}

	channels, _ := rt.Messages.Channels(r.Context(), string(sess.TenantID))
	// Always an array. See apinull_test.go: a nil slice marshals as null and the browser throws on .length.
	if channels == nil {
		channels = []string{}
	}
	s.ok(w, map[string]any{
		"messages": messages,
		"total":    total,
		"offset":   query.Offset,
		"channels": channels,
	})
}

func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid message id")
		return
	}

	m, err := rt.Messages.Get(r.Context(), string(sess.TenantID), id)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	body := map[string]any{"message": m}

	// The parsed view is what makes the viewer useful: field names instead of
	// counting separators.
	if len(m.Raw) > 0 {
		if parsed, err := explainMessage(m.Raw); err == nil {
			body["parsed"] = parsed
		} else {
			body["parseError"] = err.Error()
		}
	}

	s.ok(w, body)
}

// handleReprocessMessage sends a stored message through its channel again.
//
// This is the operation people reach for after fixing a downstream outage, and
// doing it by hand means finding the original file and replaying it with a
// command-line tool.
func (s *Server) handleReprocessMessage(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid message id")
		return
	}

	m, err := rt.Messages.Get(r.Context(), string(sess.TenantID), id)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if len(m.Raw) == 0 {
		// The payload was pruned, so there is nothing to resend. Saying so is
		// better than sending an empty message.
		s.fail(w, r, http.StatusGone,
			"this message's payload has been pruned, so it cannot be reprocessed")
		return
	}
	if !rt.IsRunning(m.Channel) {
		s.fail(w, r, http.StatusConflict,
			fmt.Sprintf("channel %q is not running, so there is nothing to reprocess into", m.Channel))
		return
	}

	cfg, err := rt.Repo.Get(m.Channel)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	// Sent over the loopback interface to the channel's own listener, so the
	// message takes exactly the same path as one arriving from the hospital. A
	// shortcut into the pipeline would test something other than production.
	addr := cfg.Source.Listen
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}

	ack, err := replayToChannel(r.Context(), addr, m.Raw)
	if err != nil {
		s.log().Error("reprocess failed", "id", id, "channel", m.Channel, "err", err)
		s.fail(w, r, http.StatusBadGateway, "reprocessing failed: "+err.Error())
		return
	}

	s.log().Info("message reprocessed",
		"id", id, "channel", m.Channel, "user", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "message.reprocess",
		Target: fmt.Sprintf("%s#%d", m.Channel, id), IP: clientIP(r),
	})

	response := map[string]any{"status": "reprocessed"}
	if parsed, err := hl7.Parse(ack); err == nil {
		response["ackCode"] = parsed.MustGet("MSA-1")
		response["ackText"] = parsed.MustGet("MSA-3")
	}
	s.ok(w, response)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}

	since := time.Now().UTC().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}

	stats, err := rt.Messages.Stats(r.Context(), string(sess.TenantID), since)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	dests, err := rt.Messages.DestinationStats(r.Context(), string(sess.TenantID), since)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if dests == nil {
		dests = []msgstore.DestinationStats{}
	}

	s.ok(w, map[string]any{"stats": stats, "destinations": dests, "since": since})
}

func (s *Server) handleThroughput(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}

	q := r.URL.Query()
	window := time.Hour
	if v := q.Get("window"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			window = d
		}
	}
	bucket := time.Minute
	if v := q.Get("bucket"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			bucket = d
		}
	}

	buckets, err := rt.Messages.Throughput(r.Context(), string(sess.TenantID),
		time.Now().UTC().Add(-window), bucket, q.Get("channel"))
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if buckets == nil {
		buckets = []msgstore.Bucket{}
	}

	s.ok(w, map[string]any{"buckets": buckets, "bucket": bucket.String(), "window": window.String()})
}

// handleEvents streams live status over server-sent events.
//
// SSE rather than a WebSocket: the data only flows one way, it works through any
// proxy that handles HTTP, and it reconnects on its own. A WebSocket would be a
// protocol to maintain for no benefit here.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireRuntime(w, r) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, r, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this, a reverse proxy may buffer the stream and the dashboard sits
	// blank until the connection closes.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) bool {
		raw, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	interval := 2 * time.Second
	if v := r.URL.Query().Get("interval"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			interval = d
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if !send("status", statusBody(rt)) {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send("status", statusBody(rt)) {
				return
			}
		}
	}
}

// ---------- inspector ----------

// SegmentView is one segment, explained.
type SegmentView struct {
	Index       int         `json:"index"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Known       bool        `json:"known"`
	Raw         string      `json:"raw"`
	Fields      []FieldView `json:"fields"`
}

// FieldView is one field, explained.
type FieldView struct {
	Number      int             `json:"number"`
	Path        string          `json:"path"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Value       string          `json:"value"`
	Raw         string          `json:"raw"`
	Empty       bool            `json:"empty"`
	Repeats     int             `json:"repeats,omitempty"`
	Meaning     string          `json:"meaning,omitempty"`
	Components  []ComponentView `json:"components,omitempty"`
}

// ComponentView is one component of a composite field.
type ComponentView struct {
	Number int    `json:"number"`
	Path   string `json:"path"`
	Name   string `json:"name,omitempty"`
	Value  string `json:"value"`
}

// MessageView is a whole message, explained.
type MessageView struct {
	MessageType  string        `json:"messageType"`
	TriggerEvent string        `json:"triggerEvent"`
	EventMeaning string        `json:"eventMeaning,omitempty"`
	ControlID    string        `json:"controlId"`
	Version      string        `json:"version"`
	Sender       string        `json:"sender"`
	Segments     []SegmentView `json:"segments"`
	Separators   string        `json:"separators"`
}

// explainMessage parses a message and annotates every field with its name.
func explainMessage(raw []byte) (*MessageView, error) {
	m, err := hl7.Parse(raw)
	if err != nil {
		return nil, err
	}

	msgType, event, _ := m.Type()
	view := &MessageView{
		MessageType:  msgType,
		TriggerEvent: event,
		ControlID:    m.ControlID(),
		Separators:   m.Separators().EncodingCharacters(),
	}
	if meaning, ok := hl7dict.ExplainCode("0003", event); ok {
		view.EventMeaning = meaning
	}
	if msh, ok := m.Segment("MSH", 1); ok {
		view.Version = msh.Field(12).String()
		view.Sender = msh.Field(4).String()
	}

	for i := 0; i < m.SegmentCount(); i++ {
		seg, ok := m.SegmentAt(i)
		if !ok {
			continue
		}

		name := seg.Name()
		definition, known := hl7dict.LookupSegment(name)

		sv := SegmentView{
			Index:       i,
			Name:        name,
			Known:       known,
			Description: definition.Description,
			Raw:         string(seg.Raw()),
		}

		for f := 1; f <= seg.FieldCount(); f++ {
			value := seg.Field(f)
			if !value.Exists() {
				continue
			}

			field, fieldKnown := hl7dict.LookupField(name, f)
			fv := FieldView{
				Number: f,
				Path:   fmt.Sprintf("%s-%d", name, f),
				Name:   field.Name,
				Value:  value.String(),
				Raw:    value.Raw(),
				Empty:  value.IsEmpty(),
			}
			if !fieldKnown {
				fv.Name = "(not in the dictionary)"
			}
			fv.Description = field.Description

			if n := value.RepeatCount(); n > 1 {
				fv.Repeats = n
			}

			if field.Table != "" {
				if meaning, ok := hl7dict.ExplainCode(field.Table, value.String()); ok {
					fv.Meaning = meaning
				}
			}

			// Components are only worth showing when the field actually has more
			// than one, or when the dictionary names them.
			if count := value.ComponentCount(); count > 1 || len(field.Components) > 0 {
				for c := 1; c <= maxInt(count, 0); c++ {
					comp := value.Component(c)
					if !comp.Exists() {
						continue
					}
					cv := ComponentView{
						Number: c,
						Path:   fmt.Sprintf("%s-%d.%d", name, f, c),
						Value:  comp.String(),
					}
					if c <= len(field.Components) {
						cv.Name = field.Components[c-1]
					}
					if cv.Value != "" || cv.Name != "" {
						fv.Components = append(fv.Components, cv)
					}
				}
			}

			sv.Fields = append(sv.Fields, fv)
		}

		view.Segments = append(view.Segments, sv)
	}

	return view, nil
}

type inspectRequest struct {
	Message string `json:"message"`
}

func (s *Server) handleInspectHL7(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req inspectRequest
	if !s.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		s.fail(w, r, http.StatusBadRequest, "no message was supplied")
		return
	}

	view, err := explainMessage([]byte(normaliseTerminators(req.Message)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.ok(w, map[string]any{"parsed": view})
}

type convertRequest struct {
	Message string `json:"message"`
	Version string `json:"version,omitempty"`
	USCore  bool   `json:"usCore,omitempty"`
	// System namespaces identifiers that have no assigning authority.
	System string `json:"system,omitempty"`
	// Timezone is applied to v2 timestamps with no offset.
	Timezone string `json:"timezone,omitempty"`
}

// handleConvertToFHIR converts a pasted v2 message to FHIR without storing
// anything.
//
// This is the screen that answers "what will this actually turn into", which is
// the question every interoperability project spends weeks on with a spreadsheet.
func (s *Server) handleConvertToFHIR(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req convertRequest
	if !s.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		s.fail(w, r, http.StatusBadRequest, "no message was supplied")
		return
	}

	version := fhir.ResourceShapeVersion
	if req.Version != "" {
		parsed, err := fhir.ParseVersion(req.Version)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		version = parsed
	}

	location := time.UTC
	if req.Timezone != "" {
		loc, err := time.LoadLocation(req.Timezone)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "unknown timezone "+req.Timezone)
			return
		}
		location = loc
	}

	m, err := hl7.Parse([]byte(normaliseTerminators(req.Message)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the message is not valid HL7 v2: "+err.Error())
		return
	}

	result, err := v2fhir.Convert(m, v2fhir.Options{
		Version:                 version,
		DefaultIdentifierSystem: req.System,
		ClaimUSCore:             req.USCore,
		Timezone:                location,
	})
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	bundle, err := fhir.MarshalBundleIndent(result.Bundle, version)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	validation := fhir.Validate(result.Bundle, version)
	errCount, warnCount, infoCount := validation.Counts()

	s.ok(w, map[string]any{
		"bundle":       json.RawMessage(bundle),
		"notes":        result.Notes,
		"counts":       result.ResourceCounts(),
		"messageType":  result.MessageType,
		"triggerEvent": result.TriggerEvent,
		"version":      string(version),
		"validation": map[string]any{
			"valid":    validation.Valid(),
			"errors":   errCount,
			"warnings": warnCount,
			"notes":    infoCount,
			"findings": validation.Findings,
		},
	})
}

func (s *Server) handleFHIRVersions(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	versions := make([]map[string]any, 0, len(fhir.AllVersions))
	for _, v := range fhir.AllVersions {
		note := ""
		switch v {
		case fhir.R5:
			note = "latest published release, and the default"
		case fhir.R4B:
			note = "maintenance release between R4 and R5"
		case fhir.R4:
			note = "what most production systems and US regulation use"
		}
		versions = append(versions, map[string]any{
			"name":   v.Name(),
			"number": string(v),
			"note":   note,
		})
	}

	s.ok(w, map[string]any{
		"versions": versions,
		// What the GUI preselects. Reporting the newest release here put R5 in the dropdown while the hint beside it advised
		// choosing R4, which is a default arguing with its own help text.
		"default": string(fhir.ResourceShapeVersion),
		"note": "R6 is in ballot and is not implemented: shipping a guess at an " +
			"unpublished specification would be worse than not supporting it.",
	})
}

func (s *Server) handleDictionary(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if name := r.URL.Query().Get("segment"); name != "" {
		seg, ok := hl7dict.LookupSegment(name)
		if !ok {
			s.fail(w, r, http.StatusNotFound, "no such segment in the dictionary")
			return
		}

		fields := make([]map[string]any, 0, len(seg.Fields))
		numbers := make([]int, 0, len(seg.Fields))
		for n := range seg.Fields {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		for _, n := range numbers {
			f := seg.Fields[n]
			fields = append(fields, map[string]any{
				"number":      n,
				"name":        f.Name,
				"description": f.Description,
				"components":  f.Components,
				"table":       f.Table,
				"repeats":     f.Repeats,
			})
		}

		s.ok(w, map[string]any{
			"segment":     seg.Name,
			"description": seg.Description,
			"fields":      fields,
		})
		return
	}

	// The whole dictionary in one document, for the script editor's completion.
	//
	// A completion menu cannot make a request per keystroke - an answer that arrives
	// after the next character is worse than no answer - so the editor loads the lot
	// once and completes from memory. It is a few tens of kilobytes and never changes at
	// run time.
	//
	// A separate mode rather than the default, because every existing caller wants the
	// names and would otherwise start receiving fifty times as much data.
	if r.URL.Query().Get("all") != "" {
		names := hl7dict.KnownSegments()
		sort.Strings(names)

		all := make([]map[string]any, 0, len(names))
		for _, name := range names {
			seg, ok := hl7dict.LookupSegment(name)
			if !ok {
				continue
			}

			numbers := make([]int, 0, len(seg.Fields))
			for n := range seg.Fields {
				numbers = append(numbers, n)
			}
			sort.Ints(numbers)

			// A list rather than the map the dictionary keeps, because a completion menu
			// needs field order and JSON object key order is not guaranteed.
			fields := make([]map[string]any, 0, len(numbers))
			for _, n := range numbers {
				f := seg.Fields[n]
				fields = append(fields, map[string]any{
					"number":      n,
					"name":        f.Name,
					"description": f.Description,
					"components":  f.Components,
					"table":       f.Table,
					"repeats":     f.Repeats,
				})
			}

			all = append(all, map[string]any{
				"segment":     seg.Name,
				"description": seg.Description,
				"fields":      fields,
			})
		}
		s.ok(w, map[string]any{"all": all})
		return
	}

	s.ok(w, map[string]any{"segments": hl7dict.KnownSegments()})
}

// normaliseTerminators accepts a message pasted from anywhere.
//
// A message copied out of a log or an email arrives with line feeds instead of
// carriage returns, and refusing it on that basis would make the tool useless for
// the case it exists to serve.
func normaliseTerminators(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\r")
	s = strings.ReplaceAll(s, "\n", "\r")
	return strings.TrimSpace(s) + "\r"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// replayToChannel sends a stored message to a channel's own listener.
//
// Going in through the front door matters: the message takes exactly the same
// path as one arriving from the hospital, including the filter and every
// destination. A shortcut straight into the pipeline would exercise something
// other than production and give false confidence.
func replayToChannel(ctx context.Context, addr string, raw []byte) ([]byte, error) {
	client := &mllp.Client{Addr: addr, Timeout: 30 * time.Second}
	defer client.Close()

	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("channel listen address %q is not usable for replay: %w", addr, err)
	}

	return client.Send(ctx, raw)
}

// unused keeps config imported for the runtime's channel lookups.
var _ = config.SourceMLLP

// publishChannelGauges refreshes the counts the dashboard leads with.
//
// They are set from the runtime rather than counted by each channel, because
// "how many are running" is a property of the set and a channel cannot know it.
func (rt *Runtime) publishChannelGauges() {
	if rt.Metrics == nil {
		return
	}
	rt.mu.RLock()
	running := len(rt.running)
	rt.mu.RUnlock()

	rt.Metrics.Set(metrics.ChannelsRunning, float64(running))
	if list, broken, err := rt.Repo.List(); err == nil {
		rt.Metrics.Set(metrics.ChannelsTotal, float64(len(list)))

		// Refreshed here rather than only at startup, so fixing the file clears the gauge without a restart. A monitoring
		// signal that needs a restart to go green teaches people to ignore it.
		rt.Metrics.Set(metrics.ChannelsBroken, float64(len(broken)))
	}
}

// ChannelStates reports which enabled channels exist and which are running.
//
// Used by alerting to notice a channel that is configured, enabled, and not
// listening — which looks identical to a healthy channel on every other measure,
// because a channel that never started also never fails.
func (rt *Runtime) ChannelStates() (configured []string, running map[string]bool) {
	running = map[string]bool{}

	rt.mu.RLock()
	for name := range rt.running {
		running[name] = true
	}
	rt.mu.RUnlock()

	list, _, err := rt.Repo.List()
	if err != nil {
		// Report what is running rather than nothing. A directory that cannot be
		// read is its own problem and will be reported elsewhere; inventing an
		// empty channel list here would raise a channel-down alert for every one
		// of them.
		for name := range running {
			configured = append(configured, name)
		}
		return configured, running
	}

	for _, ch := range list {
		// A channel deliberately turned off is not a channel that is down.
		if !ch.Enabled {
			continue
		}
		configured = append(configured, ch.Name)
	}
	return configured, running
}

// Route hands a message to another running channel, satisfying engine.ChannelRouter.
//
// The message goes in at the top of the target channel, so its filter, transformations and
// destinations all run while its source does not. A routed message is already inside the server; the
// source connector exists to receive from outside.
func (rt *Runtime) Route(ctx context.Context, name string, raw []byte) error {
	rt.mu.RLock()
	target, running := rt.running[name]
	rt.mu.RUnlock()

	if !running {
		// Named plainly, because this is the failure somebody will actually hit: the routing is
		// correct and the child channel is simply stopped. Anything vaguer sends them looking at the
		// wrong channel.
		return fmt.Errorf("channel %q is not running, so nothing can be routed to it", name)
	}

	// The acknowledgement the target would have sent is discarded on purpose. It is an HL7 ACK
	// addressed to whoever sent the message, and the sender here is another channel, which has no use
	// for it. What the parent needs is whether the delivery succeeded, and that is the error.
	if _, err := target.Route(ctx, raw); err != nil {
		return err
	}
	return nil
}

// statusBody builds the status payload, for every route that sends one.
//
// There were three copies: the REST handler included channelsTotal and channelRunning, and the two server-sent-event writers did
// not. The dashboard reads both sources into the same variable, so it showed the right numbers for two seconds and then
// "undefined/undefined" for as long as the page stayed open - on the first tile of the landing page, which is the most looked-at
// number in the product.
//
// TypeScript did not object because the event payload was cast to the shared response type on arrival. A cast is a promise that
// something has a shape, and this one was false.
//
// One builder, so a field added for one transport cannot be missing from the other.
func statusBody(rt *Runtime) map[string]any {
	states := rt.States()

	var running int
	for _, st := range states {
		if st.Running {
			running++
		}
	}

	// channelsBroken counts files that would not load at all.
	//
	// Reported here because the dashboard was saying "all running" while a channel file sat on disk with a typo in it. Nothing was
	// wrong with the channels that loaded, so every number was truthful and the conclusion was false. A feed that failed to load
	// produces no traffic and no errors, which looks exactly like a quiet feed.
	//
	// An error reading the directory is deliberately not fatal here: this payload is also the live event, and a status stream that
	// stops because of a transient directory read would take the console down with it.
	broken := 0
	if _, b, err := rt.Repo.List(); err == nil {
		broken = len(b)
	}

	return map[string]any{
		"channels":       states,
		"channelsTotal":  len(states),
		"channelRunning": running,
		"channelsBroken": broken,
		"fhirVersion":    nil,
		"time":           time.Now().UTC(),
	}
}
