package fhirserver

// Topic-based subscriptions, per the HL7 FHIR Subscriptions R5 Backport IG for R4.
//
// # Why
//
// The CMS Interoperability Framework (criterion 15, in effect since 4 July 2026) asks networks to send appointment and
// encounter notifications using FHIR subscriptions, and TEFCA's FHIR roadmap uses the same mechanism. Perfuse already
// turns ADT and SIU into Encounter and Appointment; this is the part that tells somebody when one changes.
//
// # The properties this is built around
//
//   - **An acknowledged write is never a lost notification.** The event is recorded in the same database transaction as
//     the resource it describes, so a crash between the two cannot leave a change nobody was told about. Delivery then
//     works from that outbox and retries until the receiver accepts.
//   - **Events arrive in order, and gaps are visible.** Event n+1 is not sent before event n is accepted, and every
//     notification carries events-since-subscription-start, so a receiver that missed something can see that it did.
//   - **The server owns status.** A client asks (status "requested"); the server proves the endpoint answers with a
//     handshake before calling it active. A client cannot declare its own subscription active.
//   - **A FHIR client cannot use this to reach places it should not.** Endpoints must be https unless the operator allows
//     otherwise, the egress policy is applied at creation and again at delivery, redirects are not followed, and nothing
//     a receiver sends back is ever shown to anybody - only its status code.
//   - **Off unless asked for.** A subscription makes this server send patient data to a URL a client chose. That is not a
//     capability anybody should acquire by upgrading.
//
// # What is not implemented
//
// Stated so nobody has to discover it: rest-hook is the only channel type (no websocket, email or messaging), heartbeats
// are not sent, topics fire on create and update but not delete, and there is no $status or $events operation. The
// topics are Perfuse's own, published at their canonical URLs, because the IG deliberately leaves topics to
// implementers.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/egress"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

const (
	backportBase = "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/"

	extFilterCriteria = backportBase + "backport-filter-criteria"
	extPayloadContent = backportBase + "backport-payload-content"
	profileStatusR4   = backportBase + "backport-subscription-status-r4"
	profileBackport   = backportBase + "backport-subscription"

	// TopicEncounter fires when an Encounter is created or updated: an admission, a discharge, a transfer, a visit.
	TopicEncounter = "https://perfuse.health/fhir/SubscriptionTopic/encounter"
	// TopicAppointment fires when an Appointment is created or updated: booked, rescheduled, cancelled, not attended.
	TopicAppointment = "https://perfuse.health/fhir/SubscriptionTopic/appointment"

	// maxFailures is how many consecutive failed deliveries put a subscription into error. Retained events are kept, and
	// delivery resumes from the oldest of them once the subscription is renewed.
	maxFailures = 10
	// handshakeAttempts is how many times a handshake is tried, 1, 2, 4 and 8 seconds apart, before the subscription is an
	// error.
	handshakeAttempts = 5
)

// ErrInvalidSubscription means a Subscription was refused. The REST layer answers 400 with the reason.
var ErrInvalidSubscription = errors.New("fhirserver: invalid subscription")

// Topic is a subscription topic this server can notify about.
type Topic struct {
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	ResourceType string   `json:"resourceType"`
	Filters      []string `json:"filters"`

	// published topics fire only when Publish is called, never on a FHIR write: their events are not a resource changing in
	// this store. Da Vinci PAS's result-available is one: a decision on a pended request, kept with the PAS records.
	published bool
	// match applies a published topic's filters to what was published. Nil matches every subscription on the topic.
	match func(tree map[string]any, filters url.Values) bool
	// payload loads a published event's full resource, for full-resource subscriptions.
	payload func(ctx context.Context, db *sql.DB, id string, version int) (json.RawMessage, error)
}

// Topics lists what can be subscribed to.
var Topics = []Topic{
	{URL: TopicEncounter, Title: "Encounter created or changed", ResourceType: "Encounter",
		Filters: []string{"patient", "status", "class"}},
	{URL: TopicAppointment, Title: "Appointment created or changed", ResourceType: "Appointment",
		Filters: []string{"patient", "status"}},
}

func topicByURL(u string) (Topic, bool) {
	for _, t := range append(Topics, pasTopic) {
		if t.URL == u {
			return t, true
		}
	}
	return Topic{}, false
}

// SubscriptionOptions configures delivery.
type SubscriptionOptions struct {
	// AllowHTTP permits plain-http endpoints. For a test receiver on a laptop; never for patient data on a network.
	AllowHTTP bool
	// Client is used for delivery. Nil means a client with a ten-second timeout that does not follow redirects.
	Client *http.Client
	// BaseURL is this server's FHIR base, used in the references a notification carries.
	BaseURL string
	// PAS offers the Da Vinci PAS topic, for the results of pended prior authorization requests.
	PAS bool
	Log *slog.Logger
}

// Subscriptions records and delivers notifications.
type Subscriptions struct {
	store *Store
	opts  SubscriptionOptions
	log   *slog.Logger
	now   func() time.Time

	mu     sync.Mutex
	active map[string]*parsedSubscription

	wake chan struct{}
}

type parsedSubscription struct {
	id       string
	status   string
	topic    Topic
	filters  url.Values
	endpoint string
	payload  string // empty | id-only | full-resource
	headers  []string
}

type internalWriteKey struct{}

// EnableSubscriptions turns subscriptions on for a store and returns the dispatcher, which must be Run.
func (s *Store) EnableSubscriptions(ctx context.Context, opts SubscriptionOptions) (*Subscriptions, error) {
	if s.version != fhir.R4 {
		// The backport IG is R4's form of subscriptions. R5 has native topic subscriptions with a different shape, and
		// serving R4 semantics under an R5 capability statement would be a claim nobody could rely on.
		return nil, fmt.Errorf("subscriptions follow the R4 Subscriptions Backport IG and need an R4 FHIR server; this one serves %s", s.version)
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS fhir_sub_state (
			sub_id TEXT PRIMARY KEY,
			events INTEGER NOT NULL DEFAULT 0,
			delivered INTEGER NOT NULL DEFAULT 0,
			failures INTEGER NOT NULL DEFAULT 0,
			last_success INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS fhir_sub_outbox (
			sub_id TEXT NOT NULL,
			event_number INTEGER NOT NULL,
			kind TEXT NOT NULL,
			resource_type TEXT NOT NULL DEFAULT '',
			resource_id TEXT NOT NULL DEFAULT '',
			version_id INTEGER NOT NULL DEFAULT 0,
			created INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt INTEGER NOT NULL,
			PRIMARY KEY (sub_id, event_number))`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("preparing subscription tables: %w", err)
		}
	}

	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.Client == nil {
		opts.Client = &http.Client{
			Timeout: 10 * time.Second,
			// A redirect is a second destination the egress check never saw.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	m := &Subscriptions{
		store: s, opts: opts, log: log, now: time.Now,
		active: map[string]*parsedSubscription{},
		wake:   make(chan struct{}, 1),
	}
	if err := m.load(ctx); err != nil {
		return nil, err
	}
	s.subs = m
	return m, nil
}

// Subscriptions returns the dispatcher, or nil when subscriptions are off.
func (s *Store) Subscriptions() *Subscriptions { return s.subs }

func (m *Subscriptions) load(ctx context.Context) error {
	rows, err := m.store.db.QueryContext(ctx,
		`SELECT content FROM fhir_resources WHERE resource_type = 'Subscription' AND deleted = 0`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return err
		}
		r, err := fhir.UnmarshalResource([]byte(content))
		if err != nil {
			continue
		}
		if sub, ok := r.(*fhir.Subscription); ok {
			if p, err := m.parse(sub, false); err == nil {
				m.active[p.id] = p
			}
		}
	}
	return rows.Err()
}

// parse validates a Subscription. checkEgress is false when loading what is already stored, so a policy change does not
// make the server refuse to start; delivery checks again anyway.
func (m *Subscriptions) parse(sub *fhir.Subscription, checkEgress bool) (*parsedSubscription, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidSubscription, fmt.Sprintf(format, args...))
	}
	topic, ok := topicByURL(strings.TrimSpace(sub.Criteria))
	if ok && topic.published && !m.opts.PAS {
		ok = false
	}
	if !ok {
		urls := make([]string, 0, len(Topics)+1)
		for _, t := range m.topics() {
			urls = append(urls, t.URL)
		}
		return nil, bad("criteria must name a subscription topic this server offers (%s), and %q is not one",
			strings.Join(urls, ", "), sub.Criteria)
	}
	p := &parsedSubscription{id: sub.ID, status: sub.Status, topic: topic, filters: url.Values{}}

	if sub.CriteriaElement != nil {
		for _, ext := range sub.CriteriaElement.Extension {
			if ext.URL != extFilterCriteria || ext.ValueString == nil {
				continue
			}
			criteria := *ext.ValueString
			typ, query, found := strings.Cut(criteria, "?")
			if !found && strings.Contains(criteria, "=") {
				// "org-identifier=123", as the PAS IG's own example writes it: the topic's filters, without naming the
				// resource type in front.
				typ, query = topic.ResourceType, criteria
			}
			if typ != topic.ResourceType {
				return nil, bad("filter criteria %q is for %s, and this topic is about %s", criteria, typ, topic.ResourceType)
			}
			values, err := url.ParseQuery(query)
			if err != nil {
				return nil, bad("filter criteria %q is not a valid query: %v", criteria, err)
			}
			for name, vs := range values {
				allowed := false
				for _, f := range topic.Filters {
					allowed = allowed || f == name
				}
				if !allowed {
					// Refused rather than ignored: an ignored filter is a subscription that notifies about every patient
					// when the client asked about one.
					return nil, bad("filter %q is not supported on this topic; supported: %s",
						name, strings.Join(topic.Filters, ", "))
				}
				for _, v := range vs {
					p.filters[name] = append(p.filters[name], strings.Split(v, ",")...)
				}
			}
		}
	}

	ch := sub.Channel
	if ch == nil || ch.Type != "rest-hook" {
		return nil, bad("channel.type must be rest-hook, the only channel this server delivers on")
	}
	u, err := url.Parse(ch.Endpoint)
	if err != nil || u.Host == "" {
		return nil, bad("channel.endpoint %q is not an absolute URL", ch.Endpoint)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && m.opts.AllowHTTP:
	case u.Scheme == "http":
		return nil, bad("channel.endpoint must use https; notifications identify patients")
	default:
		return nil, bad("channel.endpoint must be an https URL")
	}
	if checkEgress {
		if err := egress.Default.CheckResolving(u.Hostname()); err != nil {
			return nil, bad("channel.endpoint is not a permitted destination: %v", err)
		}
	}
	p.endpoint = ch.Endpoint

	if ch.Payload != "" && ch.Payload != "application/fhir+json" && ch.Payload != "application/json" {
		return nil, bad("channel.payload must be application/fhir+json; %q is not delivered", ch.Payload)
	}
	p.payload = "id-only" // the default: say which resource changed, and let the receiver fetch it with its own credentials
	if ch.PayloadElement != nil {
		for _, ext := range ch.PayloadElement.Extension {
			if ext.URL == extPayloadContent && ext.ValueCode != nil {
				switch *ext.ValueCode {
				case "empty", "id-only", "full-resource":
					p.payload = *ext.ValueCode
				default:
					return nil, bad("payload content %q is not empty, id-only or full-resource", *ext.ValueCode)
				}
			}
		}
	}
	for _, h := range ch.Header {
		name, _, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" || strings.ContainsAny(h, "\r\n") {
			return nil, bad("channel.header %q is not a single \"Name: value\" header", redactHeader(h))
		}
	}
	p.headers = ch.Header
	return p, nil
}

// admit runs before a write. A Subscription from a client is validated and its status set to requested; the server,
// not the client, decides when it is active.
func (m *Subscriptions) admit(ctx context.Context, r fhir.Resource) error {
	sub, ok := r.(*fhir.Subscription)
	if !ok {
		return nil
	}
	if ctx.Value(internalWriteKey{}) != nil {
		return nil
	}
	if _, err := m.parse(sub, true); err != nil {
		return err
	}
	if sub.Status != "off" {
		sub.Status = "requested"
	}
	sub.Error = ""
	if sub.Meta == nil {
		sub.Meta = &fhir.Meta{}
	}
	if !hasString(sub.Meta.Profile, profileBackport) {
		sub.Meta.Profile = append(sub.Meta.Profile, profileBackport)
	}
	return nil
}

// record runs inside the write's transaction. That is the whole durability argument: the notification exists if and only
// if the change does.
func (m *Subscriptions) record(ctx context.Context, tx *sql.Tx, r fhir.Resource, versionID int) error {
	now := m.now().UnixMilli()

	if sub, ok := r.(*fhir.Subscription); ok {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fhir_sub_state (sub_id) VALUES (?) ON CONFLICT(sub_id) DO NOTHING`, sub.ID); err != nil {
			return err
		}
		if sub.Status == "requested" {
			// A renewal starts clean: the failures that put it into error are the reason it was renewed.
			if _, err := tx.ExecContext(ctx,
				`UPDATE fhir_sub_state SET failures = 0 WHERE sub_id = ?`, sub.ID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO fhir_sub_outbox (sub_id, event_number, kind, created, next_attempt)
				 VALUES (?, 0, 'handshake', ?, ?)
				 ON CONFLICT(sub_id, event_number) DO UPDATE SET attempts = 0, next_attempt = excluded.next_attempt`,
				sub.ID, now, now)
			return err
		}
		return nil
	}

	matches := m.matching(r)
	if len(matches) == 0 {
		return nil
	}
	for _, sub := range matches {
		var events int
		if err := tx.QueryRowContext(ctx,
			`SELECT events FROM fhir_sub_state WHERE sub_id = ?`, sub.id).Scan(&events); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO fhir_sub_state (sub_id) VALUES (?)`, sub.id); err != nil {
				return err
			}
		}
		events++
		if _, err := tx.ExecContext(ctx,
			`UPDATE fhir_sub_state SET events = ? WHERE sub_id = ?`, events, sub.id); err != nil {
			return err
		}
		if sub.status == "error" {
			// Counted and not queued. The count still rises, so when the subscription is renewed its receiver sees
			// events-since-subscription-start jump and knows exactly how many it missed - rather than a backlog growing
			// without bound behind an endpoint that is not answering.
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fhir_sub_outbox (sub_id, event_number, kind, resource_type, resource_id, version_id, created, next_attempt)
			 VALUES (?, ?, 'event-notification', ?, ?, ?, ?, ?)`,
			sub.id, events, r.ResourceTypeName(), r.ResourceID(), versionID, now, now); err != nil {
			return err
		}
	}
	return nil
}

// topics are the topics this server offers.
func (m *Subscriptions) topics() []Topic {
	if m.opts.PAS {
		return append(append([]Topic{}, Topics...), pasTopic)
	}
	return Topics
}

// Publish records an event on a published topic inside the caller's transaction, for every subscription to it whose
// filters match tree: the same guarantee as a FHIR write, that the notification exists if and only if the change does.
// resourceType, id and version name the focus; the topic's payload loads it. Call Poke after the transaction commits.
func (m *Subscriptions) Publish(ctx context.Context, tx *sql.Tx, topicURL string, tree map[string]any, resourceType, id string, version int) error {
	m.mu.Lock()
	var subs []*parsedSubscription
	for _, p := range m.active {
		if p.topic.URL == topicURL && p.status != "off" && (p.topic.match == nil || p.topic.match(tree, p.filters)) {
			subs = append(subs, p)
		}
	}
	m.mu.Unlock()
	sort.Slice(subs, func(i, j int) bool { return subs[i].id < subs[j].id })
	now := m.now().UnixMilli()
	for _, sub := range subs {
		var events int
		if err := tx.QueryRowContext(ctx, `SELECT events FROM fhir_sub_state WHERE sub_id = ?`, sub.id).Scan(&events); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO fhir_sub_state (sub_id) VALUES (?)`, sub.id); err != nil {
				return err
			}
		}
		events++
		if _, err := tx.ExecContext(ctx, `UPDATE fhir_sub_state SET events = ? WHERE sub_id = ?`, events, sub.id); err != nil {
			return err
		}
		if sub.status == "error" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fhir_sub_outbox (sub_id, event_number, kind, resource_type, resource_id, version_id, created, next_attempt)
			 VALUES (?, ?, 'event-notification', ?, ?, ?, ?, ?)`,
			sub.id, events, resourceType, id, version, now, now); err != nil {
			return err
		}
	}
	return nil
}

// Poke wakes delivery, after a Publish has committed.
func (m *Subscriptions) Poke() { m.poke() }

// committed runs after a write lands.
func (m *Subscriptions) committed(r fhir.Resource) {
	if sub, ok := r.(*fhir.Subscription); ok {
		m.mu.Lock()
		if p, err := m.parse(sub, false); err == nil {
			m.active[sub.ID] = p
		} else {
			delete(m.active, sub.ID)
		}
		m.mu.Unlock()
	}
	m.poke()
}

// deleted runs after a Subscription is deleted.
func (m *Subscriptions) deleted(ctx context.Context, resourceType, id string) {
	if resourceType != "Subscription" {
		return
	}
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
	if _, err := m.store.db.ExecContext(ctx, `DELETE FROM fhir_sub_outbox WHERE sub_id = ?`, id); err != nil {
		m.log.Warn("could not discard a deleted subscription's pending notifications", "subscription", id, "err", err)
	}
}

func (m *Subscriptions) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Subscriptions) matching(r fhir.Resource) []*parsedSubscription {
	m.mu.Lock()
	var candidates []*parsedSubscription
	for _, p := range m.active {
		if !p.topic.published && p.topic.ResourceType == r.ResourceTypeName() && p.status != "off" {
			candidates = append(candidates, p)
		}
	}
	m.mu.Unlock()
	if len(candidates) == 0 {
		return nil
	}

	raw, err := fhir.Marshal(r, fhir.R4)
	if err != nil {
		return nil
	}
	var tree map[string]any
	if json.Unmarshal(raw, &tree) != nil {
		return nil
	}

	var out []*parsedSubscription
	for _, p := range candidates {
		if matchesFilters(tree, p.filters) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// matchesFilters applies a subscription's filters. Values within one filter are alternatives; filters combine with AND,
// as they do in FHIR search.
func matchesFilters(tree map[string]any, filters url.Values) bool {
	for name, values := range filters {
		var have []string
		switch name {
		case "status":
			have = []string{str(tree["status"])}
		case "class":
			if c, ok := tree["class"].(map[string]any); ok {
				have = []string{str(c["code"])}
			}
		case "patient":
			if subj, ok := tree["subject"].(map[string]any); ok {
				have = append(have, str(subj["reference"]))
			}
			if parts, ok := tree["participant"].([]any); ok {
				for _, part := range parts {
					if pm, ok := part.(map[string]any); ok {
						if actor, ok := pm["actor"].(map[string]any); ok {
							have = append(have, str(actor["reference"]))
						}
					}
				}
			}
		}
		if !anyMatch(name, have, values) {
			return false
		}
	}
	return true
}

func anyMatch(name string, have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			w = strings.TrimSpace(w)
			if name == "patient" {
				if patientRef(h) != "" && patientRef(h) == patientRef(w) {
					return true
				}
				continue
			}
			if h != "" && h == w {
				return true
			}
		}
	}
	return false
}

// patientRef reduces "Patient/123", "123" and "https://host/fhir/Patient/123" to "123", and anything else to "".
func patientRef(ref string) string {
	if i := strings.LastIndex(ref, "Patient/"); i >= 0 {
		return strings.TrimSuffix(ref[i+len("Patient/"):], "/")
	}
	if ref != "" && !strings.Contains(ref, "/") {
		return ref
	}
	return ""
}

// Run delivers until the context ends.
func (m *Subscriptions) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		m.DeliverDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-m.wake:
		}
	}
}

type outboxRow struct {
	subID                    string
	event                    int
	kind                     string
	resourceType, resourceID string
	versionID                int
	attempts                 int
	created                  int64
}

// DeliverDue attempts every notification whose turn has come: the oldest outstanding one per subscription, and only once
// its retry time has passed. Exported so a test can drive delivery without a goroutine.
func (m *Subscriptions) DeliverDue(ctx context.Context) {
	rows, err := m.store.db.QueryContext(ctx,
		`SELECT o.sub_id, o.event_number, o.kind, o.resource_type, o.resource_id, o.version_id, o.attempts, o.created
		   FROM fhir_sub_outbox o
		  WHERE o.event_number = (SELECT MIN(i.event_number) FROM fhir_sub_outbox i WHERE i.sub_id = o.sub_id)
		    AND o.next_attempt <= ?
		  ORDER BY o.created
		  LIMIT 100`, m.now().UnixMilli())
	if err != nil {
		m.log.Warn("could not read pending subscription notifications", "err", err)
		return
	}
	var due []outboxRow
	for rows.Next() {
		var o outboxRow
		if err := rows.Scan(&o.subID, &o.event, &o.kind, &o.resourceType, &o.resourceID, &o.versionID, &o.attempts, &o.created); err == nil {
			due = append(due, o)
		}
	}
	rows.Close()

	for _, o := range due {
		if ctx.Err() != nil {
			return
		}
		m.deliver(ctx, o)
	}
}

func (m *Subscriptions) deliver(ctx context.Context, o outboxRow) {
	m.mu.Lock()
	sub := m.active[o.subID]
	m.mu.Unlock()
	if sub == nil {
		_, _ = m.store.db.ExecContext(ctx, `DELETE FROM fhir_sub_outbox WHERE sub_id = ?`, o.subID)
		return
	}
	if o.kind != "handshake" && sub.status != "active" {
		// Waiting for the handshake, or in error until renewed. The event stays queued.
		return
	}

	body, err := m.notification(ctx, sub, o)
	if err != nil {
		m.log.Warn("could not build a subscription notification", "subscription", o.subID, "event", o.event, "err", err)
		return
	}

	failure := m.post(ctx, sub, body)
	now := m.now()
	if failure == "" {
		_, _ = m.store.db.ExecContext(ctx,
			`DELETE FROM fhir_sub_outbox WHERE sub_id = ? AND event_number = ?`, o.subID, o.event)
		_, _ = m.store.db.ExecContext(ctx,
			`UPDATE fhir_sub_state SET failures = 0, last_success = ?, last_error = '',
			        delivered = MAX(delivered, ?) WHERE sub_id = ?`, now.UnixMilli(), o.event, o.subID)
		if o.kind == "handshake" {
			m.setStatus(ctx, o.subID, "active", "")
		}
		m.poke() // the next event for this subscription is now due
		return
	}

	m.log.Warn("a subscription notification was not accepted",
		"subscription", o.subID, "event", o.event, "kind", o.kind, "reason", failure)

	var failures int
	_ = m.store.db.QueryRowContext(ctx,
		`UPDATE fhir_sub_state SET failures = failures + 1, last_error = ? WHERE sub_id = ? RETURNING failures`,
		failure, o.subID).Scan(&failures)

	backoff := time.Duration(1<<min(o.attempts, 8)) * time.Second // 1s doubling to about four minutes
	if o.kind == "handshake" {
		if o.attempts+1 < handshakeAttempts {
			// The handshake goes out the moment the subscription is stored, often before the client that created it has
			// finished reading the 201 and started listening. A refusal in that first moment says little about the
			// endpoint, so it is tried again, a few times over about fifteen seconds, before the subscription is an error.
			_, _ = m.store.db.ExecContext(ctx,
				`UPDATE fhir_sub_outbox SET attempts = attempts + 1, next_attempt = ? WHERE sub_id = ? AND event_number = 0`,
				now.Add(backoff).UnixMilli(), o.subID)
			return
		}
		_, _ = m.store.db.ExecContext(ctx,
			`DELETE FROM fhir_sub_outbox WHERE sub_id = ? AND event_number = 0`, o.subID)
		m.setStatus(ctx, o.subID, "error", "the handshake was not accepted: "+failure)
		return
	}

	_, _ = m.store.db.ExecContext(ctx,
		`UPDATE fhir_sub_outbox SET attempts = attempts + 1, next_attempt = ? WHERE sub_id = ? AND event_number = ?`,
		now.Add(backoff).UnixMilli(), o.subID, o.event)

	if failures >= maxFailures {
		m.setStatus(ctx, o.subID, "error",
			fmt.Sprintf("%d consecutive deliveries failed, most recently: %s. Pending notifications are kept; update the subscription to resume.", failures, failure))
	}
}

// post sends one notification and returns "" on success or a short reason. The reason never includes anything the
// receiver sent back, because it is shown to FHIR clients and an endpoint's response body is not theirs to read.
func (m *Subscriptions) post(ctx context.Context, sub *parsedSubscription, body []byte) string {
	u, err := url.Parse(sub.endpoint)
	if err != nil {
		return "the endpoint is not a URL"
	}
	if err := egress.Default.CheckResolving(u.Hostname()); err != nil {
		return "the endpoint is not a permitted destination"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.endpoint, bytes.NewReader(body))
	if err != nil {
		return "the request could not be built"
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	for _, h := range sub.headers {
		name, value, _ := strings.Cut(h, ":")
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	resp, err := m.opts.Client.Do(req)
	if err != nil {
		return "the endpoint could not be reached"
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "the endpoint answered HTTP " + strconv.Itoa(resp.StatusCode)
	}
	return ""
}

func (m *Subscriptions) setStatus(ctx context.Context, id, status, reason string) {
	r, err := m.store.Get(ctx, "Subscription", id)
	if err != nil {
		return
	}
	sub, ok := r.(*fhir.Subscription)
	if !ok {
		return
	}
	sub.Status, sub.Error = status, reason
	if _, err := m.store.Put(context.WithValue(ctx, internalWriteKey{}, true), sub); err != nil {
		m.log.Warn("could not record a subscription's status", "subscription", id, "status", status, "err", err)
	}
}

// notification builds the history bundle the backport IG defines: a SubscriptionStatus Parameters first, then the
// resource itself when the subscription asked for full resources.
func (m *Subscriptions) notification(ctx context.Context, sub *parsedSubscription, o outboxRow) ([]byte, error) {
	base := strings.TrimRight(m.opts.BaseURL, "/")

	var events int
	_ = m.store.db.QueryRowContext(ctx, `SELECT events FROM fhir_sub_state WHERE sub_id = ?`, sub.id).Scan(&events)

	kind, status := o.kind, sub.status
	if kind == "handshake" {
		status = "requested"
	}

	params := []any{
		map[string]any{"name": "subscription", "valueReference": map[string]any{"reference": base + "/Subscription/" + sub.id}},
		map[string]any{"name": "topic", "valueCanonical": sub.topic.URL},
		map[string]any{"name": "status", "valueCode": status},
		map[string]any{"name": "type", "valueCode": kind},
		map[string]any{"name": "events-since-subscription-start", "valueString": strconv.Itoa(events)},
	}
	entries := []any{}
	if kind == "event-notification" {
		event := []any{
			map[string]any{"name": "event-number", "valueString": strconv.Itoa(o.event)},
			// When the change happened, not when delivery finally succeeded.
			map[string]any{"name": "timestamp", "valueInstant": time.UnixMilli(o.created).UTC().Format(time.RFC3339)},
		}
		focus := base + "/" + o.resourceType + "/" + o.resourceID
		if sub.payload != "empty" {
			event = append(event, map[string]any{"name": "focus", "valueReference": map[string]any{"reference": focus}})
		}
		params = append(params, map[string]any{"name": "notification-event", "part": event})

		if sub.payload == "full-resource" {
			// The version the event describes, not whatever is current by the time delivery succeeds. A discharge
			// notification that arrives carrying the next admission is a notification about the wrong visit.
			var raw []byte
			if sub.topic.payload != nil {
				loaded, err := sub.topic.payload(ctx, m.store.db, o.resourceID, o.versionID)
				if err != nil {
					return nil, err
				}
				raw = loaded
			} else {
				r, err := m.store.GetVersion(ctx, o.resourceType, o.resourceID, o.versionID)
				if err != nil {
					return nil, err
				}
				if raw, err = fhir.MarshalVersioned(r, fhir.R4); err != nil {
					return nil, err
				}
			}
			entries = append(entries, map[string]any{
				"fullUrl":  focus,
				"resource": json.RawMessage(raw),
				"request":  map[string]any{"method": "PUT", "url": o.resourceType + "/" + o.resourceID},
				"response": map[string]any{"status": "200"},
			})
		}
	}

	status0 := map[string]any{
		"fullUrl": "urn:uuid:" + newUUID(),
		"resource": map[string]any{
			"resourceType": "Parameters",
			"id":           newUUID(),
			"meta":         map[string]any{"profile": []string{profileStatusR4}},
			"parameter":    params,
		},
		"request":  map[string]any{"method": "GET", "url": base + "/Subscription/" + sub.id + "/$status"},
		"response": map[string]any{"status": "200"},
	}
	bundle := map[string]any{
		"resourceType": "Bundle",
		"id":           newUUID(),
		"type":         "history",
		"timestamp":    m.now().UTC().Format(time.RFC3339),
		"entry":        append([]any{status0}, entries...),
	}
	return json.Marshal(bundle)
}

// SubscriptionSummary is what the web interface shows about one subscription.
type SubscriptionSummary struct {
	ID          string   `json:"id"`
	Status      string   `json:"status"`
	Topic       string   `json:"topic"`
	TopicTitle  string   `json:"topicTitle"`
	Filter      string   `json:"filter"`
	Endpoint    string   `json:"endpoint"`
	Payload     string   `json:"payload"`
	HeaderNames []string `json:"headerNames"`
	Events      int      `json:"events"`
	Delivered   int      `json:"delivered"`
	Pending     int      `json:"pending"`
	Failures    int      `json:"failures"`
	LastSuccess string   `json:"lastSuccess,omitempty"`
	LastError   string   `json:"lastError,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// Summaries lists every subscription with its delivery state.
//
// Header values are withheld - they are usually a bearer token for the receiver - and so is the endpoint's query string,
// which is where the other half of those tokens tends to live.
func (m *Subscriptions) Summaries(ctx context.Context) ([]SubscriptionSummary, error) {
	rows, err := m.store.db.QueryContext(ctx,
		`SELECT content FROM fhir_resources WHERE resource_type = 'Subscription' AND deleted = 0 ORDER BY resource_id`)
	if err != nil {
		return nil, err
	}
	var subs []*fhir.Subscription
	for rows.Next() {
		var content string
		if rows.Scan(&content) != nil {
			continue
		}
		if r, err := fhir.UnmarshalResource([]byte(content)); err == nil {
			if s, ok := r.(*fhir.Subscription); ok {
				subs = append(subs, s)
			}
		}
	}
	rows.Close()

	out := []SubscriptionSummary{}
	for _, s := range subs {
		sum := SubscriptionSummary{ID: s.ID, Status: s.Status, Topic: s.Criteria, Error: s.Error, HeaderNames: []string{}}
		if t, ok := topicByURL(s.Criteria); ok {
			sum.TopicTitle = t.Title
		}
		if s.CriteriaElement != nil {
			for _, ext := range s.CriteriaElement.Extension {
				if ext.URL == extFilterCriteria && ext.ValueString != nil {
					sum.Filter = *ext.ValueString
				}
			}
		}
		if s.Channel != nil {
			if u, err := url.Parse(s.Channel.Endpoint); err == nil {
				u.RawQuery, u.Fragment, u.User = "", "", nil
				sum.Endpoint = u.String()
			}
			sum.Payload = "id-only"
			if s.Channel.PayloadElement != nil {
				for _, ext := range s.Channel.PayloadElement.Extension {
					if ext.URL == extPayloadContent && ext.ValueCode != nil {
						sum.Payload = *ext.ValueCode
					}
				}
			}
			for _, h := range s.Channel.Header {
				name, _, _ := strings.Cut(h, ":")
				sum.HeaderNames = append(sum.HeaderNames, strings.TrimSpace(name))
			}
		}
		var lastSuccess int64
		_ = m.store.db.QueryRowContext(ctx,
			`SELECT events, delivered, failures, last_success, last_error FROM fhir_sub_state WHERE sub_id = ?`, s.ID).
			Scan(&sum.Events, &sum.Delivered, &sum.Failures, &lastSuccess, &sum.LastError)
		if lastSuccess > 0 {
			sum.LastSuccess = time.UnixMilli(lastSuccess).UTC().Format(time.RFC3339)
		}
		_ = m.store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM fhir_sub_outbox WHERE sub_id = ? AND kind = 'event-notification'`, s.ID).Scan(&sum.Pending)
		out = append(out, sum)
	}
	return out, nil
}

// subscriptionCapability declares the topics on the Subscription entry, in the form the backport IG defines, so a client
// discovers what it can subscribe to rather than guessing canonical URLs.
func subscriptionCapability(resources []any, offered []Topic) []any {
	topics := make([]any, 0, len(offered))
	for _, t := range offered {
		topics = append(topics, map[string]any{
			"url":            backportBase + "capabilitystatement-subscriptiontopic-canonical",
			"valueCanonical": t.URL,
		})
	}
	for _, r := range resources {
		if entry, ok := r.(map[string]any); ok && entry["type"] == "Subscription" {
			entry["extension"] = topics
			entry["supportedProfile"] = []string{profileBackport}
			return resources
		}
	}
	return append(resources, map[string]any{
		"type":             "Subscription",
		"extension":        topics,
		"supportedProfile": []string{profileBackport},
		"interaction": []any{
			map[string]any{"code": "read"}, map[string]any{"code": "create"},
			map[string]any{"code": "update"}, map[string]any{"code": "delete"},
		},
	})
}

func redactHeader(h string) string {
	name, _, _ := strings.Cut(h, ":")
	return strings.TrimSpace(name) + ": …"
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func hasString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
