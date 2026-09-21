// Package msgstore persists the messages a channel handled.
//
// This is the screen people leave open all day in an interface engine: what
// arrived, what happened to it, and the ability to look at one and send it again.
// Without it an engine can only answer "how many" and never "which one".
//
// Retention is built in rather than added later. The most commonly reported
// operational failure of an interface engine is its message tables filling the
// disk, and a store with no pruning is that failure waiting for enough traffic.
package msgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/attach"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Outcome is what happened to a message. These mirror the engine's outcomes,
// because collapsing them here would lose the distinction the engine went to
// trouble to preserve.
type Outcome string

// Message outcomes.
const (
	// Delivered means every destination that wanted it accepted it.
	Delivered Outcome = "delivered"
	// Filtered means a filter rejected it. Nothing was sent and nothing failed.
	Filtered Outcome = "filtered"
	// Partial means some destinations have it and some do not.
	Partial Outcome = "partial"
	// Failed means nothing accepted it.
	Failed Outcome = "failed"
	// Unparseable means the bytes were not an HL7 message.
	Unparseable Outcome = "unparseable"
	// Pending means processing has not finished, which is what an on_receipt
	// acknowledgement leaves behind briefly.
	Pending Outcome = "pending"
)

// DeliveryStatus is what happened at one destination.
type DeliveryStatus string

// Delivery statuses.
const (
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryFailed    DeliveryStatus = "failed"
	DeliveryFiltered  DeliveryStatus = "filtered"
)

// ErrNotFound means no such message.
var ErrNotFound = errors.New("msgstore: message not found")

// Message is one recorded message.
type Message struct {
	ID      int64  `json:"id"`
	Channel string `json:"channel"`
	// TenantID is which organisation this message belongs to.
	//
	// On the message rather than derived from the channel name, because channel
	// names are not unique across tenants - two clinics both having an "adt-in" is
	// the first thing that happens - so a name could not identify an owner.
	TenantID     string    `json:"tenantId,omitempty"`
	ReceivedAt   time.Time `json:"receivedAt"`
	ControlID    string    `json:"controlId,omitempty"`
	MessageType  string    `json:"messageType,omitempty"`
	TriggerEvent string    `json:"triggerEvent,omitempty"`
	Sender       string    `json:"sender,omitempty"`
	Remote       string    `json:"remote,omitempty"`
	Outcome      Outcome   `json:"outcome"`
	AckCode      string    `json:"ackCode,omitempty"`
	Size         int       `json:"size"`
	DurationMS   int64     `json:"durationMs"`
	Error        string    `json:"error,omitempty"`
	Segments     int       `json:"segments,omitempty"`

	// Raw is the original bytes, loaded only when a single message is fetched.
	// Listing thousands of messages must not drag their payloads along.
	Raw []byte `json:"raw,omitempty"`

	// Deliveries is the per-destination outcome, loaded with a single message.
	Deliveries []Delivery `json:"deliveries,omitempty"`

	// Attachments are the payloads this message carries, loaded with a single
	// message. Described rather than included: the interface needs to say "a 4 MB
	// PDF" without streaming four megabytes to do it.
	Attachments []AttachmentInfo `json:"attachments,omitempty"`

	// AttachmentError explains why Raw could not be fully reassembled, empty when
	// it could. Reported rather than failing the fetch, because a message with an
	// unresolvable payload still records that something arrived.
	AttachmentError string `json:"attachmentError,omitempty"`
}

// Delivery is the outcome at one destination.
type Delivery struct {
	Destination string         `json:"destination"`
	Status      DeliveryStatus `json:"status"`
	Attempts    int            `json:"attempts"`
	DurationMS  int64          `json:"durationMs"`
	Error       string         `json:"error,omitempty"`
}

// Store persists messages.
type Store struct {
	db *sql.DB

	// read is used for queries. It is a separate pool with several connections,
	// because a report that scans the payload column must not hold the connection
	// a delivery needs: a write is on the acknowledgement path, and blocking it
	// behind somebody's search turns a report into a sender timeout.
	//
	// It falls back to db when unset, so a caller with one handle still works.
	read *sql.DB

	// RetentionDays bounds how long messages are kept. Zero means forever, which
	// is a choice an operator should have to make deliberately rather than get by
	// default.
	RetentionDays int

	// StorePayloads controls whether the original bytes are kept. Turning it off
	// keeps counts and outcomes while storing no clinical content, which is what
	// a deployment that cannot hold PHI at rest needs.
	StorePayloads bool

	// IndexIdentity controls whether identifiers are extracted from payloads into a
	// searchable index, so a message can be found by MRN or by name whatever format it
	// arrived in.
	//
	// This is a privacy decision and not only a performance one. The identifiers are already
	// in this database inside the payload column, so indexing them adds no data - but it does
	// make them enumerable, and a database that could previously only be grepped for a
	// number somebody already had can now be asked to list patients. That is a decision for
	// the site.
	//
	// Independent of StorePayloads except in one direction, enforced in indexIdentity below:
	// an installation that keeps no payloads is not given an identity index either, because
	// there the extraction would be retaining clinical content that the site chose not to
	// retain.
	IndexIdentity bool

	// PayloadDays is how long message bodies are kept, when set.
	//
	// Zero means half the retention window, which is what this did before the value
	// was configurable - so an installation that says nothing keeps behaving as it did.
	PayloadDays int

	// RetentionDaysFn, StorePayloadsFn and PayloadDaysFn supersede the fields above when set.
	//
	// They exist so these can be changed while the server runs. Reading the fields
	// directly while something else writes them is a data race, and the settings
	// store already holds a lock around its values - so the safe arrangement is to
	// ask it each time rather than copy the value in at startup.
	//
	// Assigned once before serving begins, like the fields they replace. It is the
	// values behind them that change, not the functions.
	RetentionDaysFn func() int
	StorePayloadsFn func() bool
	PayloadDaysFn   func() int
	IndexIdentityFn func() bool
}

// payloadDays is how long to keep message bodies.
//
// Falls back to half the retention window, which is what this was before it could be configured. Halving rather than matching is the
// original decision and it stands: losing the fact that a message arrived is worse than losing its contents, so the audit trail
// deliberately outlives the content.
//
// Bounded by the retention window at the call site rather than here, because a body cannot outlive the row that holds it - deleting the
// row takes the body with it, so a larger value simply has no effect.
func (s *Store) payloadDays() int {
	if s.PayloadDaysFn != nil {
		if d := s.PayloadDaysFn(); d > 0 {
			return d
		}
	}
	if s.PayloadDays > 0 {
		return s.PayloadDays
	}

	return maxInt(1, s.retentionDays()/2)
}

// indexIdentity reports whether to extract identifiers from payloads.
//
// Refused outright when payloads are not stored. Turning StorePayloads off is how a site says
// it cannot hold clinical content at rest; writing patient names into an index at the same
// time would defeat that, and it would do so quietly, which is worse. The stronger choice
// wins.
func (s *Store) indexIdentity() bool {
	if !s.storePayloads() {
		return false
	}
	if s.IndexIdentityFn != nil {
		return s.IndexIdentityFn()
	}
	return s.IndexIdentity
}

// retentionDays is how many days to keep, from the live source if there is one.
func (s *Store) retentionDays() int {
	if s.RetentionDaysFn != nil {
		return s.RetentionDaysFn()
	}

	return s.RetentionDays
}

// storePayloads reports whether to keep message bodies, from the live source if there is one.
func (s *Store) storePayloads() bool {
	if s.StorePayloadsFn != nil {
		return s.StorePayloadsFn()
	}

	return s.StorePayloads
}

// NewStore prepares the schema on an existing database handle. Queries and
// writes share it.
func NewStore(db *sql.DB) (*Store, error) {
	return NewStoreWithReader(db, nil)
}

// NewStoreWithReader prepares the schema and uses a separate pool for queries.
//
// Pass the read handle from internal/sqlitedb. Without it everything still
// works; reports just compete with delivery for the single write connection,
// which is measurably bad once the message table is large.
func NewStoreWithReader(db, read *sql.DB) (*Store, error) {
	s := &Store{db: db, read: read, StorePayloads: true}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// querier returns the handle reports should use.
func (s *Store) querier() *sql.DB {
	if s.read != nil {
		return s.read
	}
	return s.db
}

var schema = []string{
	// Attachments are stored once per distinct payload, keyed by content.
	//
	// A separate table rather than a column on messages, because a payload is shared: the same document resent, or
	// one message fanned out to five destinations. A column would store it once per message, which is the problem
	// this exists to solve.
	`CREATE TABLE IF NOT EXISTS attachments (
		digest     TEXT PRIMARY KEY,
		size       INTEGER NOT NULL,
		payload    BLOB    NOT NULL,
		first_seen TEXT    NOT NULL
	)`,

	// Which messages refer to which payloads.
	//
	// Recorded explicitly rather than derived by scanning stored messages for tokens, because raw bytes are dropped
	// at half the retention window - a scan would stop finding references while the message still exists, and the
	// sweep would then delete a payload a queued delivery still needs.
	`CREATE TABLE IF NOT EXISTS message_attachments (
		message_id INTEGER NOT NULL,
		digest     TEXT    NOT NULL,
		PRIMARY KEY (message_id, digest)
	)`,

	// Leads with digest, because the sweep and the reference count both ask "who refers to this payload" rather
	// than "what does this message refer to".
	`CREATE INDEX IF NOT EXISTS message_attachments_digest ON message_attachments(digest)`,

	`CREATE TABLE IF NOT EXISTS messages (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		channel        TEXT    NOT NULL,
		received_at    TEXT    NOT NULL,
		control_id     TEXT,
		message_type   TEXT,
		trigger_event  TEXT,
		sender         TEXT,
		remote         TEXT,
		outcome        TEXT    NOT NULL,
		ack_code       TEXT,
		size           INTEGER NOT NULL DEFAULT 0,
		segments       INTEGER NOT NULL DEFAULT 0,
		duration_ms    INTEGER NOT NULL DEFAULT 0,
		error          TEXT,
		raw            BLOB
	)`,

	// The indexes are chosen for the queries the browser actually runs: newest
	// first per channel, and lookup by control ID during an incident.
	`CREATE INDEX IF NOT EXISTS messages_recent ON messages(received_at DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS messages_channel ON messages(channel, received_at DESC)`,
	`CREATE INDEX IF NOT EXISTS messages_outcome ON messages(outcome, received_at DESC)`,
	`CREATE INDEX IF NOT EXISTS messages_control ON messages(control_id)`,
	`CREATE INDEX IF NOT EXISTS messages_type ON messages(message_type, trigger_event)`,

	`CREATE TABLE IF NOT EXISTS message_deliveries (
		message_id  INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		destination TEXT    NOT NULL,
		status      TEXT    NOT NULL,
		attempts    INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		error       TEXT
	)`,

	`CREATE INDEX IF NOT EXISTS deliveries_message ON message_deliveries(message_id)`,
	`CREATE INDEX IF NOT EXISTS deliveries_dest ON message_deliveries(destination, status)`,

	// Identity extracted from the payload, so a message can be found by what somebody knows
	// about it rather than by a path into whichever format it arrived in.
	//
	// A table rather than columns on messages, for two reasons. A patient has several
	// identifiers at once - an MRN, an enterprise number, a payer member ID - and a message
	// can concern more than one person, as a merge does. Squashing those into one column
	// means matching by substring again, which is the thing this replaces.
	//
	// tenant_id is carried here rather than reached through the join. A scoped query that
	// forgets it returns another organisation's patients, and the join is exactly where that
	// is easy to forget.
	`CREATE TABLE IF NOT EXISTS message_identity (
		message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		tenant_id  TEXT    NOT NULL DEFAULT 'main',
		kind       TEXT    NOT NULL,
		value      TEXT    NOT NULL,
		norm       TEXT    NOT NULL
	)`,

	// norm leads the lookup index because that is what a search matches on: the value as
	// typed, reduced the same way the stored one was. An index on value would not be used by
	// a query on norm, and the point of extracting at record time is that this is an index
	// hit rather than a scan.
	`CREATE INDEX IF NOT EXISTS identity_lookup ON message_identity(norm, kind, tenant_id)`,
	`CREATE INDEX IF NOT EXISTS identity_message ON message_identity(message_id)`,
}

func (s *Store) migrate(ctx context.Context) error {
	for i, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("msgstore: schema statement %d: %w", i, err)
		}
	}

	// Additive columns, applied separately because SQLite has no ADD COLUMN IF NOT EXISTS and a second run must not
	// fail. Errors are ignored for that reason and only for that reason: if the column genuinely cannot be added,
	// every query naming it fails loudly on the next line rather than silently doing the wrong thing.
	//
	// Defaulted to the main tenant so an existing single-tenant installation keeps seeing its own messages after an
	// upgrade. A null would be a row every scoped query excludes, which presents as "all my history disappeared".
	for _, stmt := range []string{
		`ALTER TABLE messages ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'main'`,
	} {
		_, _ = s.db.ExecContext(ctx, stmt)
	}

	// Leads with tenant_id, because every scoped query filters on it first. One that did not would make a
	// fifty-tenant instance scan forty-nine tenants' rows to answer one tenant's question.
	if _, err := s.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS messages_tenant ON messages(tenant_id, received_at DESC, id DESC)`); err != nil {
		return fmt.Errorf("msgstore: tenant index: %w", err)
	}

	return nil
}

// tenantOrDefault fills an unset tenant with the default.
//
// Defaulted here rather than refused, because most callers are single-tenant and requiring them to say so would be
// noise. The safety property that matters is the opposite direction: a query must never be allowed to omit the filter,
// and that is enforced by Query carrying it as a required field rather than by trusting call sites.
func tenantOrDefault(id string) string {
	if id == "" {
		return "main"
	}
	return id
}

// Record stores a message and its per-destination outcomes.
func (s *Store) Record(ctx context.Context, m *Message) (int64, error) {
	raw := m.Raw
	if !s.storePayloads() {
		raw = nil
	}
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = time.Now().UTC()
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages
			(channel, tenant_id, received_at, control_id, message_type, trigger_event, sender,
			 remote, outcome, ack_code, size, segments, duration_ms, error, raw)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Channel, tenantOrDefault(m.TenantID), dbtime.Format(m.ReceivedAt), m.ControlID,
		m.MessageType, m.TriggerEvent, m.Sender, m.Remote, string(m.Outcome),
		m.AckCode, m.Size, m.Segments, m.DurationMS, m.Error, raw)
	if err != nil {
		return 0, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, d := range m.Deliveries {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO message_deliveries
				(message_id, destination, status, attempts, duration_ms, error)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, d.Destination, string(d.Status), d.Attempts, d.DurationMS, d.Error); err != nil {
			return id, err
		}
	}

	// Extracted from m.Raw rather than from raw, which is nil when payloads are not stored.
	// The distinction does not arise in practice because indexIdentity refuses in that case,
	// and using m.Raw here would silently index content the site asked not to keep if that
	// rule were ever removed. So the guard is what decides, and it is asked first.
	if s.indexIdentity() {
		s.recordIdentity(ctx, id, tenantOrDefault(m.TenantID), m.Raw)
	}

	return id, nil
}

// Get loads one message with its payload and deliveries.
// Get fetches one message, scoped to a tenant.
//
// The tenant is a required argument rather than an optional filter, because identifiers are sequential and guessing one
// takes no skill at all. A signature that allowed it to be omitted would make the unsafe call the shorter one.
//
// A message belonging to another tenant reports as not found rather than forbidden: telling a caller that a record
// exists but is not theirs is itself a disclosure.
func (s *Store) Get(ctx context.Context, tenantID string, id int64) (*Message, error) {
	m := &Message{}
	var (
		receivedAt string
		outcome    string
		raw        []byte
		controlID  sql.NullString
		msgType    sql.NullString
		event      sql.NullString
		sender     sql.NullString
		remote     sql.NullString
		ackCode    sql.NullString
		errText    sql.NullString
	)

	err := s.querier().QueryRowContext(ctx,
		`SELECT id, channel, received_at, control_id, message_type, trigger_event,
				sender, remote, outcome, ack_code, size, segments, duration_ms, error, raw
		 FROM messages WHERE id = ? AND tenant_id = ?`, id, tenantOrDefault(tenantID)).
		Scan(&m.ID, &m.Channel, &receivedAt, &controlID, &msgType, &event,
			&sender, &remote, &outcome, &ackCode, &m.Size, &m.Segments,
			&m.DurationMS, &errText, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	m.ReceivedAt = parseTime(receivedAt)
	m.Outcome = Outcome(outcome)
	m.ControlID = controlID.String
	m.MessageType = msgType.String
	m.TriggerEvent = event.String
	m.Sender = sender.String
	m.Remote = remote.String
	m.AckCode = ackCode.String
	m.Error = errText.String
	m.Raw = raw

	// Reassembled here rather than left to every caller. The browser, replay, trace, search and export all want the
	// message as it arrived, and a token leaking into any one of them would be read as the content of the field.
	//
	// A missing payload does not fail the fetch. The message is still worth showing - it records that something
	// arrived and what happened to it - and the attachment list below says what could not be resolved, which is more
	// useful than an error page. What must never happen is a message with an unresolved token being *delivered*, and
	// that path refuses separately.
	if len(m.Raw) > 0 {
		if whole, err := attach.Reassemble(m.Raw, s.AttachmentLookup(ctx)); err == nil {
			m.Raw = whole
		} else {
			m.AttachmentError = err.Error()
		}
	}

	attachments, err := s.MessageAttachments(ctx, id)
	if err != nil {
		return nil, err
	}
	m.Attachments = attachments

	deliveries, err := s.deliveriesFor(ctx, id)
	if err != nil {
		return nil, err
	}
	m.Deliveries = deliveries

	return m, nil
}

func (s *Store) deliveriesFor(ctx context.Context, id int64) ([]Delivery, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT destination, status, attempts, duration_ms, COALESCE(error, '')
		 FROM message_deliveries WHERE message_id = ? ORDER BY destination`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Delivery
	for rows.Next() {
		var d Delivery
		var status string
		if err := rows.Scan(&d.Destination, &status, &d.Attempts, &d.DurationMS, &d.Error); err != nil {
			return nil, err
		}
		d.Status = DeliveryStatus(status)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Query filters a message list.
type Query struct {
	// TenantID scopes the query to one organisation. Required in multi-tenant operation.
	//
	// A field on the query rather than a separate argument, so it travels with everything else and a caller cannot
	// build a query that forgets it. Empty means the main tenant, which keeps single-tenant callers unchanged - and
	// the alternative, empty meaning "all tenants", is the sort of default that turns one missed line into a
	// disclosure of patient data.
	TenantID string

	Channel      string
	Outcome      Outcome
	MessageType  string
	TriggerEvent string
	ControlID    string
	Sender       string
	// Search matches against the stored payload. It is a substring match, which is
	// slow on a large table and is exactly what somebody needs during an incident
	// when all they have is an accession number.
	Search string
	Since  time.Time
	Until  time.Time
	Limit  int
	Offset int
}

// DefaultLimit is the page size when none is given.
const DefaultLimit = 100

// MaxLimit bounds a page, because an unbounded one asks the server to load the
// whole table into memory.
const MaxLimit = 1000

// List returns matching messages, newest first, without their payloads.
func (s *Store) List(ctx context.Context, q Query) ([]Message, int, error) {
	where, args := q.build()

	var total int
	if err := s.querier().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	pageArgs := append(append([]any(nil), args...), limit, q.Offset)
	rows, err := s.querier().QueryContext(ctx,
		`SELECT id, channel, received_at, COALESCE(control_id,''), COALESCE(message_type,''),
				COALESCE(trigger_event,''), COALESCE(sender,''), COALESCE(remote,''),
				outcome, COALESCE(ack_code,''), size, segments, duration_ms, COALESCE(error,'')
		 FROM messages WHERE `+where+`
		 ORDER BY received_at DESC, id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var (
			m          Message
			receivedAt string
			outcome    string
		)
		if err := rows.Scan(&m.ID, &m.Channel, &receivedAt, &m.ControlID, &m.MessageType,
			&m.TriggerEvent, &m.Sender, &m.Remote, &outcome, &m.AckCode,
			&m.Size, &m.Segments, &m.DurationMS, &m.Error); err != nil {
			return nil, 0, err
		}
		m.ReceivedAt = parseTime(receivedAt)
		m.Outcome = Outcome(outcome)
		out = append(out, m)
	}
	return out, total, rows.Err()
}

func (q Query) build() (string, []any) {
	// The tenant filter is unconditional and first, deliberately. Every other clause here is optional, and if this
	// one were optional too then a query that simply failed to set it would return every tenant's messages - which
	// is patient data, and exactly the bug this replaced.
	clauses := []string{"tenant_id = ?"}
	args := []any{tenantOrDefault(q.TenantID)}

	if q.Channel != "" {
		clauses = append(clauses, "channel = ?")
		args = append(args, q.Channel)
	}
	if q.Outcome != "" {
		clauses = append(clauses, "outcome = ?")
		args = append(args, string(q.Outcome))
	}
	if q.MessageType != "" {
		clauses = append(clauses, "message_type = ?")
		args = append(args, q.MessageType)
	}
	if q.TriggerEvent != "" {
		clauses = append(clauses, "trigger_event = ?")
		args = append(args, q.TriggerEvent)
	}
	if q.ControlID != "" {
		clauses = append(clauses, "control_id = ?")
		args = append(args, q.ControlID)
	}
	if q.Sender != "" {
		clauses = append(clauses, "sender = ?")
		args = append(args, q.Sender)
	}
	if q.Search != "" {
		// A substring search over the payload. Parameterised, so a search term
		// containing a quote is a search term rather than a SQL injection.
		//
		// The cast is required: raw is a BLOB, and SQLite's LIKE does not match
		// text patterns against blob values, so without it every search silently
		// returns nothing.
		clauses = append(clauses, "CAST(raw AS TEXT) LIKE ?")
		args = append(args, "%"+q.Search+"%")
	}
	if !q.Since.IsZero() {
		clauses = append(clauses, "received_at >= ?")
		args = append(args, dbtime.Format(q.Since))
	}
	if !q.Until.IsZero() {
		clauses = append(clauses, "received_at <= ?")
		args = append(args, dbtime.Format(q.Until))
	}

	return strings.Join(clauses, " AND "), args
}

// Stats summarises activity for a dashboard.
type Stats struct {
	Total       int64            `json:"total"`
	ByOutcome   map[string]int64 `json:"byOutcome"`
	ByChannel   map[string]int64 `json:"byChannel"`
	ByType      map[string]int64 `json:"byType"`
	OldestKept  *time.Time       `json:"oldestKept,omitempty"`
	NewestKept  *time.Time       `json:"newestKept,omitempty"`
	StoredBytes int64            `json:"storedBytes"`
}

// Stats returns aggregate counts since a time.
func (s *Store) Stats(ctx context.Context, tenantID string, since time.Time) (*Stats, error) {
	out := &Stats{
		ByOutcome: map[string]int64{},
		ByChannel: map[string]int64{},
		ByType:    map[string]int64{},
	}

	// Unconditional and first, like every other scoped query here. A count is a disclosure too: telling a tenant
	// there are forty thousand messages today when four are theirs says something about the platform's other
	// customers.
	args := []any{tenantOrDefault(tenantID)}
	where := "tenant_id = ?"
	if !since.IsZero() {
		where += " AND received_at >= ?"
		args = append(args, dbtime.Format(since))
	}

	if err := s.querier().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM messages WHERE `+where, args...).
		Scan(&out.Total, &out.StoredBytes); err != nil {
		return nil, err
	}

	groups := []struct {
		column string
		target map[string]int64
	}{
		{"outcome", out.ByOutcome},
		{"channel", out.ByChannel},
	}
	for _, g := range groups {
		rows, err := s.querier().QueryContext(ctx,
			`SELECT `+g.column+`, COUNT(*) FROM messages WHERE `+where+
				` GROUP BY `+g.column, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				key string
				n   int64
			)
			if err := rows.Scan(&key, &n); err != nil {
				rows.Close()
				return nil, err
			}
			g.target[key] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Message type and trigger event read better combined, since that is how
	// people talk about them.
	rows, err := s.querier().QueryContext(ctx,
		`SELECT COALESCE(message_type,'') , COALESCE(trigger_event,''), COUNT(*)
		 FROM messages WHERE `+where+` GROUP BY message_type, trigger_event`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			msgType string
			event   string
			n       int64
		)
		if err := rows.Scan(&msgType, &event, &n); err != nil {
			rows.Close()
			return nil, err
		}
		label := msgType
		if event != "" {
			label += "^" + event
		}
		if label == "" {
			label = "unknown"
		}
		out.ByType[label] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var oldest, newest sql.NullString
	if err := s.querier().QueryRowContext(ctx,
		`SELECT MIN(received_at), MAX(received_at) FROM messages`).Scan(&oldest, &newest); err != nil {
		return nil, err
	}
	if oldest.Valid {
		t := parseTime(oldest.String)
		out.OldestKept = &t
	}
	if newest.Valid {
		t := parseTime(newest.String)
		out.NewestKept = &t
	}

	return out, nil
}

// Bucket is one point on a throughput chart.
type Bucket struct {
	Start       time.Time `json:"start"`
	Total       int64     `json:"total"`
	Delivered   int64     `json:"delivered"`
	Filtered    int64     `json:"filtered"`
	Failed      int64     `json:"failed"`
	Unparseable int64     `json:"unparseable"`
}

// Throughput returns message counts bucketed over time.
//
// Aggregating in SQL rather than loading rows and counting in Go is what keeps a
// chart cheap enough to poll. A dashboard that costs a table scan is a dashboard
// somebody turns off.
func (s *Store) Throughput(ctx context.Context, tenantID string, since time.Time, bucket time.Duration, channel string) ([]Bucket, error) {
	if bucket <= 0 {
		bucket = time.Minute
	}
	seconds := int64(bucket.Seconds())

	args := []any{seconds, seconds, tenantOrDefault(tenantID), dbtime.Format(since)}
	where := "tenant_id = ? AND received_at >= ?"
	if channel != "" {
		where += " AND channel = ?"
		args = append(args, channel)
	}

	rows, err := s.querier().QueryContext(ctx,
		`SELECT
			CAST(strftime('%s', received_at) / ? AS INTEGER) * ? AS bucket_start,
			COUNT(*),
			SUM(CASE WHEN outcome = 'delivered' THEN 1 ELSE 0 END),
			SUM(CASE WHEN outcome = 'filtered' THEN 1 ELSE 0 END),
			SUM(CASE WHEN outcome IN ('failed','partial') THEN 1 ELSE 0 END),
			SUM(CASE WHEN outcome = 'unparseable' THEN 1 ELSE 0 END)
		 FROM messages WHERE `+where+`
		 GROUP BY bucket_start ORDER BY bucket_start`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Collected by bucket start, then filled in below.
	found := map[int64]Bucket{}

	for rows.Next() {
		var (
			start int64
			b     Bucket
		)
		if err := rows.Scan(&start, &b.Total, &b.Delivered, &b.Filtered,
			&b.Failed, &b.Unparseable); err != nil {
			return nil, err
		}
		b.Start = time.Unix(start, 0).UTC()
		found[start] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Every bucket in the window is returned, including the empty ones.
	//
	// GROUP BY only produces buckets that have rows, so the series used to contain nothing but busy periods. A feed that stopped
	// twenty minutes ago did not appear as a flat line at zero - it simply was not in the data, and the chart drew the last busy
	// stretch and ended. So the one thing this chart exists to show, a feed going quiet, was the one thing it could not show.
	//
	// It also made a single message unplottable: one bucket has no width, so an area chart drew nothing at all while the tile beside
	// it said four messages. Two numbers on one page disagreeing is how people stop believing the page.
	return fillBuckets(found, since, int64(seconds)), nil
}

// fillBuckets returns every bucket in the window, including the empty ones.
//
// Shared, because the flow map needs exactly this and a second implementation of it would be a second chance to reintroduce the bug it
// was written to fix. The two views would then disagree about whether a quiet feed exists.
func fillBuckets(found map[int64]Bucket, since time.Time, seconds int64) []Bucket {
	if seconds <= 0 {
		seconds = 60
	}

	first := (since.UTC().Unix() / seconds) * seconds
	last := (time.Now().UTC().Unix() / seconds) * seconds

	// A guard against an absurd combination - a one-second bucket over a month - building a vast slice. The dashboard's own
	// combinations produce between sixty and a hundred buckets.
	const maxBuckets = 5000
	if last > first && (last-first)/seconds > maxBuckets {
		first = last - maxBuckets*seconds
	}

	out := make([]Bucket, 0, (last-first)/seconds+1)
	for at := first; at <= last; at += seconds {
		if b, ok := found[at]; ok {
			out = append(out, b)

			continue
		}
		out = append(out, Bucket{Start: time.Unix(at, 0).UTC()})
	}

	return out
}

// DestinationStats summarises per-destination delivery outcomes.
type DestinationStats struct {
	Destination string  `json:"destination"`
	Delivered   int64   `json:"delivered"`
	Failed      int64   `json:"failed"`
	Filtered    int64   `json:"filtered"`
	AvgAttempts float64 `json:"avgAttempts"`
	AvgDuration float64 `json:"avgDurationMs"`
}

// DestinationStats returns per-destination counts since a time.
func (s *Store) DestinationStats(ctx context.Context, tenantID string, since time.Time) ([]DestinationStats, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT d.destination,
			SUM(CASE WHEN d.status = 'delivered' THEN 1 ELSE 0 END),
			SUM(CASE WHEN d.status = 'failed' THEN 1 ELSE 0 END),
			SUM(CASE WHEN d.status = 'filtered' THEN 1 ELSE 0 END),
			AVG(d.attempts), AVG(d.duration_ms)
		 FROM message_deliveries d
		 JOIN messages m ON m.id = d.message_id
		 WHERE m.tenant_id = ? AND m.received_at >= ?
		 GROUP BY d.destination ORDER BY d.destination`,
		tenantOrDefault(tenantID), dbtime.Format(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DestinationStats
	for rows.Next() {
		var (
			d           DestinationStats
			avgAttempts sql.NullFloat64
			avgDuration sql.NullFloat64
		)
		if err := rows.Scan(&d.Destination, &d.Delivered, &d.Failed, &d.Filtered,
			&avgAttempts, &avgDuration); err != nil {
			return nil, err
		}
		d.AvgAttempts = avgAttempts.Float64
		d.AvgDuration = avgDuration.Float64
		out = append(out, d)
	}
	return out, rows.Err()
}

// Prune removes messages older than the retention window.
//
// Content is removed before metadata, so a store under disk pressure loses the
// payloads first and keeps the record that a message existed. Losing the fact
// that something arrived is worse than losing its contents.
func (s *Store) Prune(ctx context.Context) (payloads, rows int64, err error) {
	days := s.retentionDays()
	if days <= 0 {
		return 0, 0, nil
	}
	cutoff := dbtime.Format(time.Now().AddDate(0, 0, -days))

	// Drop payloads at half the retention window, which keeps searchable content
	// recent while keeping the audit trail longer.
	//
	// Both cutoffs read the same source. This line used the raw field while the one
	// above used the live one, which meant a retention changed through the interface
	// moved the row deletion and left the payload deletion where the startup flag put
	// it - so somebody setting ten years kept the records for ten years and lost every
	// message body after fifteen days. Introduced when retention became live-editable
	// and found by being asked how pruning works.
	//
	// The window is now configurable and defaults to half the retention window, which
	// is what it always was.
	//
	// Deliberately not capped at the retention window, having briefly been. A body
	// cannot outlive its row anyway - the delete below takes it - so the cap changed no
	// data. What it did change was the count reported here: it blanked a payload and
	// then deleted the whole row, so the log claimed work that was about to be undone.
	// Two plants proved the cap unobservable, which is the tell for a check that should
	// not exist.
	payloadCutoff := dbtime.Format(time.Now().
		AddDate(0, 0, -maxInt(1, s.payloadDays())))

	res, err := s.db.ExecContext(ctx,
		`UPDATE messages SET raw = NULL WHERE raw IS NOT NULL AND received_at < ?`,
		payloadCutoff)
	if err != nil {
		return 0, 0, err
	}
	payloads, _ = res.RowsAffected()

	res, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE received_at < ?`, cutoff)
	if err != nil {
		return payloads, 0, err
	}
	rows, _ = res.RowsAffected()

	// Deliveries cascade only when foreign keys are on, which depends on the
	// connection pragma, so they are cleaned explicitly.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM message_deliveries
		 WHERE message_id NOT IN (SELECT id FROM messages)`); err != nil {
		return payloads, rows, err
	}

	// Identity next, for the same reason and with a sharper edge. A message row deleted by
	// retention while its extracted identifiers survive leaves a searchable index of patient
	// names and MRNs belonging to messages the site has already decided not to keep - so the
	// retention window would be deleting the evidence and keeping the identification.
	//
	// The declaration carries ON DELETE CASCADE and that is not what cleans this up. The note
	// above says exactly why, and this table was written relying on the cascade anyway; a test
	// asserting the rows were gone is what caught it.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM message_identity
		 WHERE message_id NOT IN (SELECT id FROM messages)`); err != nil {
		return payloads, rows, err
	}

	// Attachments last, and on the same schedule, because a payload outliving every message that referred to it is
	// a leak that grows at the rate of the documents rather than the traffic - which in a document workflow is the
	// fastest-growing thing in the store.
	if _, err := s.SweepAttachments(ctx); err != nil {
		return payloads, rows, err
	}

	return payloads, rows, nil
}

// StartPruner prunes on a schedule and returns a stop function.
func (s *Store) StartPruner(ctx context.Context, every time.Duration, onResult func(payloads, rows int64, err error)) func() {
	if every <= 0 {
		every = time.Hour
	}
	ticker := time.NewTicker(every)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				payloads, rows, err := s.Prune(ctx)
				if onResult != nil {
					onResult(payloads, rows, err)
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		ticker.Stop()
		close(done)
	}
}

// Channels lists the channels that have recorded messages, for filter dropdowns.
func (s *Store) Channels(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT DISTINCT channel FROM messages WHERE tenant_id = ? ORDER BY channel`,
		tenantOrDefault(tenantID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
