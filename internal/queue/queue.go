// Package queue is a durable, per-destination delivery queue.
//
// Without it, a destination that is down for two minutes loses everything sent
// during those two minutes: retries happen in memory, and when they run out the
// message is recorded as failed and gone. With it, the message is on disk and
// keeps its place until the destination comes back.
//
// Two design decisions shape everything else.
//
// **Order is preserved per destination, and that means a queued destination
// stops taking the fast path.** HL7 is a stream of events about the same
// patients: an A01 admits, an A03 discharges. If a failed A01 goes to the queue
// while the next A03 is delivered directly, the receiving system sees a discharge
// for a patient it never admitted. So once anything is queued for a destination,
// everything for that destination queues behind it until the queue drains. That
// costs throughput during an outage and it is not optional, because the
// alternative is silent clinical nonsense. It is also the specific thing that
// goes wrong in other engines when somebody turns on concurrent queue threads to
// make a backlog drain faster.
//
// **A queued message is an accepted message.** The sender is told AA, because the
// bytes are on disk and fsynced before the acknowledgement goes out. Saying AE
// would make a correctly functioning store-and-forward queue look like a fault
// and invite the sender to resend what we already hold.
package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// State is where a queued message is in its life.
type State string

const (
	// Pending is waiting for its next attempt.
	Pending State = "pending"
	// Delivered means it eventually got there. Kept briefly so somebody watching
	// a backlog drain can see it happening.
	Delivered State = "delivered"
	// Failed means attempts were exhausted, or an operator gave up on it. It stays
	// until removed, because a message that left the queue without arriving is
	// the one somebody needs to look at.
	Failed State = "failed"
	// Skipped means an operator deliberately abandoned it. Distinct from failed:
	// one is the system giving up and the other is a person deciding, and
	// conflating them loses who is accountable.
	Skipped State = "skipped"
)

// Item is one queued delivery.
type Item struct {
	ID          int64     `json:"id"`
	Channel     string    `json:"channel"`
	Destination string    `json:"destination"`
	ControlID   string    `json:"controlId"`
	MessageType string    `json:"messageType"`
	Raw         []byte    `json:"-"`
	Size        int       `json:"size"`
	State       State     `json:"state"`
	Attempts    int       `json:"attempts"`
	EnqueuedAt  time.Time `json:"enqueuedAt"`
	NextAttempt time.Time `json:"nextAttempt"`
	LastAttempt time.Time `json:"lastAttempt,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	// Reason is why it entered the queue, kept separate from LastError so the
	// original cause survives a series of different later failures.
	Reason string `json:"reason,omitempty"`
}

// Age reports how long the item has been waiting. This is the number that tells
// an operator whether a backlog is draining or stuck.
func (i Item) Age(now time.Time) time.Duration {
	return now.Sub(i.EnqueuedAt)
}

// Store persists the queue.
type Store struct {
	db   *sql.DB
	read *sql.DB
}

// NewStore prepares the schema.
//
// Pass the write handle and, separately, a read handle. Queue depth is polled by
// the dashboard, and that poll must not sit in front of a delivery.
func NewStore(db, read *sql.DB) (*Store, error) {
	s := &Store{db: db, read: read}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// Reader exposes the handle reports use, for callers in this package that build
// their own queries.
func (s *Store) Reader() *sql.DB { return s.querier() }

func (s *Store) querier() *sql.DB {
	if s.read != nil {
		return s.read
	}
	return s.db
}

// Migrations are append-only: a released statement is never edited, because an
// installation that already ran it will not run it again.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS queue (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		channel       TEXT    NOT NULL,
		destination   TEXT    NOT NULL,
		control_id    TEXT    NOT NULL DEFAULT '',
		message_type  TEXT    NOT NULL DEFAULT '',
		raw           BLOB    NOT NULL,
		state         TEXT    NOT NULL,
		attempts      INTEGER NOT NULL DEFAULT 0,
		enqueued_at   TEXT    NOT NULL,
		next_attempt  TEXT    NOT NULL,
		last_attempt  TEXT,
		last_error    TEXT    NOT NULL DEFAULT '',
		reason        TEXT    NOT NULL DEFAULT ''
	)`,

	// The drain query is "the oldest pending item for this destination whose time
	// has come", so the index has to serve exactly that. Without it, draining a
	// large backlog scans the whole table for every single message, which is how a
	// queue that is meant to recover from an outage becomes the outage.
	`CREATE INDEX IF NOT EXISTS queue_pending
		ON queue (channel, destination, state, next_attempt, id)`,

	// Depth and oldest-age are polled every few seconds by the dashboard.
	`CREATE INDEX IF NOT EXISTS queue_state ON queue (state, enqueued_at)`,
}

func (s *Store) migrate(ctx context.Context) error {
	for _, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("queue schema: %w", err)
		}
	}
	return nil
}

// Enqueue adds a message, returning its id.
//
// The write is committed before this returns, so a caller may acknowledge the
// sender once it has. That ordering is the entire promise of the queue and it
// must not be relaxed for throughput.
func (s *Store) Enqueue(ctx context.Context, item Item) (int64, error) {
	if strings.TrimSpace(item.Channel) == "" || strings.TrimSpace(item.Destination) == "" {
		return 0, errors.New("queue: an item needs a channel and a destination")
	}
	if len(item.Raw) == 0 {
		// An empty payload would drain successfully and deliver nothing, which is
		// worse than refusing it.
		return 0, errors.New("queue: refusing to queue an empty message")
	}

	now := time.Now().UTC()
	if item.EnqueuedAt.IsZero() {
		item.EnqueuedAt = now
	}
	if item.NextAttempt.IsZero() {
		item.NextAttempt = now
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO queue
			(channel, destination, control_id, message_type, raw, state,
			 attempts, enqueued_at, next_attempt, last_error, reason)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, '', ?)`,
		item.Channel, item.Destination, item.ControlID, item.MessageType,
		item.Raw, string(Pending),
		dbtime.Format(item.EnqueuedAt),
		dbtime.Format(item.NextAttempt),
		item.Reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Next returns the head of a destination's queue when it is ready to be sent.
//
// When the head exists but is still backing off, it returns a nil item and how
// long to wait. That distinction is the whole ordering guarantee, and getting it
// wrong is subtle: an earlier version selected the oldest item *whose next
// attempt was due*, which reads as reasonable and is badly broken. A message
// backing off after a failure is not due, so the query skipped it and returned a
// later message that had never been tried — and the later message went out first.
// The queue preserved order right up until the first failure, which is precisely
// when order starts to matter.
//
// So the head is the head. If it is waiting, everything behind it waits too. That
// is the cost of ordering and it is not negotiable.
//
// It returns one item, not a batch, because delivering in order means delivering
// one at a time. A batch would only help if they could be sent concurrently,
// which is the thing that reorders them.
func (s *Store) Next(ctx context.Context, channel, destination string, now time.Time) (*Item, time.Duration, error) {
	row := s.querier().QueryRowContext(ctx, `
		SELECT id, channel, destination, control_id, message_type, raw, state,
		       attempts, enqueued_at, next_attempt, last_attempt, last_error, reason
		FROM queue
		WHERE channel = ? AND destination = ? AND state = ?
		ORDER BY id
		LIMIT 1`,
		channel, destination, string(Pending))

	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}

	if wait := item.NextAttempt.Sub(now.UTC()); wait > 0 {
		return nil, wait, nil
	}
	return item, 0, nil
}

// Blocked reports whether anything is waiting for a destination, regardless of
// whether its next attempt is due.
//
// This is what makes ordering work. A destination with a pending item must not
// take the direct path, or a later message would overtake an earlier one. Note it
// deliberately ignores next_attempt: an item backing off is still ahead in the
// queue.
func (s *Store) Blocked(ctx context.Context, channel, destination string) (bool, error) {
	var n int
	err := s.querier().QueryRowContext(ctx, `
		SELECT count(*) FROM queue
		WHERE channel = ? AND destination = ? AND state = ?`,
		channel, destination, string(Pending)).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Succeeded marks an item delivered.
func (s *Store) Succeeded(ctx context.Context, id int64, attempts int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE queue
		SET state = ?, attempts = ?, last_attempt = ?, last_error = ''
		WHERE id = ?`,
		string(Delivered), attempts, nowText(), id)
	return err
}

// Retry records a failed attempt and schedules the next one.
func (s *Store) Retry(ctx context.Context, id int64, attempts int, next time.Time, cause string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE queue
		SET state = ?, attempts = ?, last_attempt = ?, next_attempt = ?, last_error = ?
		WHERE id = ?`,
		string(Pending), attempts, nowText(),
		dbtime.Format(next), truncateError(cause), id)
	return err
}

// GaveUp marks an item failed after exhausting its attempts.
func (s *Store) GaveUp(ctx context.Context, id int64, attempts int, cause string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE queue
		SET state = ?, attempts = ?, last_attempt = ?, last_error = ?
		WHERE id = ?`,
		string(Failed), attempts, nowText(), truncateError(cause), id)
	return err
}

// Query selects items for the interface.
type Query struct {
	Channel     string
	Destination string
	State       State
	// Limit defaults to 100 and is capped at 1000, because this feeds a page and
	// an unbounded query against a large backlog would be a denial of service on
	// ourselves.
	Limit  int
	Offset int
}

// List returns matching items, newest first, and the total count.
//
// Payloads are not included. A queue page showing a hundred messages does not
// need a hundred payloads, and shipping them would put clinical content into a
// response that only needed counts.
func (s *Store) List(ctx context.Context, q Query) ([]Item, int, error) {
	var where []string
	var args []any
	if q.Channel != "" {
		where = append(where, "channel = ?")
		args = append(args, q.Channel)
	}
	if q.Destination != "" {
		where = append(where, "destination = ?")
		args = append(args, q.Destination)
	}
	if q.State != "" {
		where = append(where, "state = ?")
		args = append(args, string(q.State))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.querier().QueryRowContext(ctx,
		"SELECT count(*) FROM queue"+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	rows, err := s.querier().QueryContext(ctx, `
		SELECT id, channel, destination, control_id, message_type, length(raw), state,
		       attempts, enqueued_at, next_attempt, last_attempt, last_error, reason
		FROM queue`+clause+`
		ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		var enqueued, next string
		var lastAttempt sql.NullString
		if err := rows.Scan(&it.ID, &it.Channel, &it.Destination, &it.ControlID,
			&it.MessageType, &it.Size, &it.State, &it.Attempts,
			&enqueued, &next, &lastAttempt, &it.LastError, &it.Reason); err != nil {
			return nil, 0, err
		}
		it.EnqueuedAt = parseTime(enqueued)
		it.NextAttempt = parseTime(next)
		if lastAttempt.Valid {
			it.LastAttempt = parseTime(lastAttempt.String)
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// Payload returns the stored bytes for one item, for the inspector.
func (s *Store) Payload(ctx context.Context, id int64) ([]byte, error) {
	var raw []byte
	err := s.querier().QueryRowContext(ctx,
		"SELECT raw FROM queue WHERE id = ?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("queue item %d not found", id)
	}
	return raw, err
}

// DepthRow is the per-destination summary the dashboard shows.
type DepthRow struct {
	Channel     string    `json:"channel"`
	Destination string    `json:"destination"`
	Pending     int       `json:"pending"`
	Failed      int       `json:"failed"`
	Oldest      time.Time `json:"oldest,omitzero"`
	// OldestSeconds is the age of the oldest pending item. This is the number to
	// alert on: depth alone cannot distinguish a busy queue that is draining from
	// a small one that has been stuck since Tuesday.
	OldestSeconds float64 `json:"oldestSeconds"`
	// MaxAttempts is the highest attempt count among pending items, which is what
	// says "this is being retried and not progressing".
	MaxAttempts int `json:"maxAttempts"`
}

// Depth summarises the queue per destination.
func (s *Store) Depth(ctx context.Context) ([]DepthRow, error) {
	rows, err := s.querier().QueryContext(ctx, `
		SELECT channel, destination,
		       sum(CASE WHEN state = 'pending' THEN 1 ELSE 0 END),
		       sum(CASE WHEN state = 'failed'  THEN 1 ELSE 0 END),
		       min(CASE WHEN state = 'pending' THEN enqueued_at END),
		       max(CASE WHEN state = 'pending' THEN attempts ELSE 0 END)
		FROM queue
		WHERE state IN ('pending', 'failed')
		GROUP BY channel, destination
		ORDER BY channel, destination`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	var out []DepthRow
	for rows.Next() {
		var r DepthRow
		var oldest sql.NullString
		if err := rows.Scan(&r.Channel, &r.Destination, &r.Pending, &r.Failed,
			&oldest, &r.MaxAttempts); err != nil {
			return nil, err
		}
		if oldest.Valid {
			r.Oldest = parseTime(oldest.String)
			r.OldestSeconds = now.Sub(r.Oldest).Seconds()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- operator actions ------------------------------------------------------
//
// These exist because the alternative is what other engines force: stopping and
// restarting the channel to clear a stuck message, which also interrupts
// everything that was working.

// RetryNow brings forward the next attempt for one item, or for every pending
// item on a destination when id is zero.
//
// It resets the attempt counter, because an operator retrying by hand has usually
// just fixed something, and counting the failures from before the fix against the
// new attempt budget would abandon the message immediately.
func (s *Store) RetryNow(ctx context.Context, id int64, channel, destination string) (int64, error) {
	if id != 0 {
		res, err := s.db.ExecContext(ctx, `
			UPDATE queue SET state = ?, next_attempt = ?, attempts = 0
			WHERE id = ? AND state IN ('pending', 'failed')`,
			string(Pending), nowText(), id)
		return affected(res, err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE queue SET state = ?, next_attempt = ?, attempts = 0
		WHERE channel = ? AND destination = ? AND state IN ('pending', 'failed')`,
		string(Pending), nowText(), channel, destination)
	return affected(res, err)
}

// Skip abandons an item without delivering it, recording that a person decided
// to. The message stays in the table so the decision is auditable.
func (s *Store) Skip(ctx context.Context, id int64, who string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE queue SET state = ?, last_error = ?, last_attempt = ?
		WHERE id = ? AND state IN ('pending', 'failed')`,
		string(Skipped), "skipped by "+who, nowText(), id)
	return affected(res, err)
}

// Drain skips every pending item for a destination. Used when a receiver has been
// down long enough that the backlog is no longer clinically useful — a week of
// stale admissions replayed at once can do more harm than never sending them.
func (s *Store) Drain(ctx context.Context, channel, destination, who string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE queue SET state = ?, last_error = ?, last_attempt = ?
		WHERE channel = ? AND destination = ? AND state IN ('pending', 'failed')`,
		string(Skipped), "drained by "+who, nowText(), channel, destination)
	return affected(res, err)
}

// Remove deletes items that have finished, one by one or in bulk.
//
// Pending items are never deleted here. Deleting a message that has not been
// delivered and has not been explicitly abandoned would lose it silently, so it
// has to be skipped first — which records that somebody chose to.
func (s *Store) Remove(ctx context.Context, id int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM queue WHERE id = ? AND state != ?", id, string(Pending))
	return affected(res, err)
}

// Purge deletes finished items older than a cutoff, and reports how many went.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM queue
		WHERE state IN ('delivered', 'skipped')
		  AND coalesce(last_attempt, enqueued_at) < ?`,
		dbtime.Format(before))
	return affected(res, err)
}

// PendingDestinations lists every destination with work waiting, so a worker
// starting up knows what to drain without being told.
//
// This is what makes the queue survive a restart rather than merely persist: the
// rows would be there either way, but nothing would look at them.
func (s *Store) PendingDestinations(ctx context.Context) ([]DepthRow, error) {
	rows, err := s.querier().QueryContext(ctx, `
		SELECT channel, destination, count(*), 0, min(enqueued_at), max(attempts)
		FROM queue WHERE state = ?
		GROUP BY channel, destination`, string(Pending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DepthRow
	for rows.Next() {
		var r DepthRow
		var oldest sql.NullString
		if err := rows.Scan(&r.Channel, &r.Destination, &r.Pending, &r.Failed,
			&oldest, &r.MaxAttempts); err != nil {
			return nil, err
		}
		if oldest.Valid {
			r.Oldest = parseTime(oldest.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- helpers ---------------------------------------------------------------

func scanItem(row *sql.Row) (*Item, error) {
	var it Item
	var enqueued, next string
	var lastAttempt sql.NullString
	err := row.Scan(&it.ID, &it.Channel, &it.Destination, &it.ControlID,
		&it.MessageType, &it.Raw, &it.State, &it.Attempts,
		&enqueued, &next, &lastAttempt, &it.LastError, &it.Reason)
	if err != nil {
		return nil, err
	}
	it.Size = len(it.Raw)
	it.EnqueuedAt = parseTime(enqueued)
	it.NextAttempt = parseTime(next)
	if lastAttempt.Valid {
		it.LastAttempt = parseTime(lastAttempt.String)
	}
	return &it, nil
}

func affected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nowText() string { return dbtime.Format(time.Now()) }

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// truncateError bounds what is stored. A receiver returning a megabyte of HTML in
// place of an acknowledgement should not put a megabyte into every queue row.
func truncateError(s string) string {
	const limit = 2000
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "… (truncated)"
}
