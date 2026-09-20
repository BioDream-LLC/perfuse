package msgstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/expr"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Searching stored traffic by what is inside the messages.
//
// The existing search matches metadata - channel, type, control ID - and offers a substring match
// against the payload, which is grep. That covers "find the message with this accession number" and
// nothing else. It cannot answer "find every admission for this patient where the sex code was not
// mapped", which is the shape of most real questions during an incident.
//
// This uses the same expression language channel filters use, so there is one query syntax to
// learn rather than two, and anything somebody works out here can be pasted straight into a
// channel filter. That is not a small thing: the usual outcome of an investigation is a filter
// change, and being able to test the expression against real traffic before deploying it removes
// the step where somebody guesses.
//
// # It is a scan, and it says so
//
// A path cannot be indexed: the payload is opaque bytes in a column. So this narrows with SQL
// first and then parses what is left, which is honest but not free. The bound is explicit and the
// result reports how many messages were examined, so a search that found nothing after looking at
// a thousand of forty thousand messages cannot be mistaken for a search that found nothing.

// ExpressionSearch describes a search by message content.
type ExpressionSearch struct {
	// Where is a filter expression, in the same language channel filters use.
	Where string

	// Narrowing, applied in SQL before anything is parsed. Using these makes the difference
	// between examining a thousand messages and examining a hundred thousand.
	Channel     string
	MessageType string
	Outcome     Outcome
	Since       time.Time
	Until       time.Time

	// Examine bounds how many messages are parsed. Zero uses a default.
	Examine int
	// Limit bounds how many matches are returned. Zero uses a default.
	Limit int
}

// ExpressionResult is what a content search found.
type ExpressionResult struct {
	// Matches are the messages the expression accepted, newest first.
	Matches []Message `json:"matches"`

	// Examined is how many messages were parsed and tested.
	Examined int `json:"examined"`
	// Available is how many matched the narrowing criteria, so a reader can see what fraction was
	// looked at.
	Available int `json:"available"`
	// Unreadable counts messages that could not be parsed and so could not be tested.
	Unreadable int `json:"unreadable"`
	// Truncated says the examine bound was reached, so there may be matches that were not looked
	// at. Without this a partial answer reads as a complete one.
	Truncated bool `json:"truncated"`

	// Paths lists what the expression looked at, which is a useful check that it says what its
	// author meant.
	Paths []string `json:"paths,omitempty"`
}

// defaultExamine bounds a scan when the caller does not.
//
// Two thousand messages parses in well under a second and is enough to answer most questions about
// recent traffic. The caller can ask for more; the point of the default is that somebody typing an
// expression into a box does not accidentally ask the server to parse a million messages.
const defaultExamine = 2000

// maxExamine is the hard ceiling.
const maxExamine = 50000

// SearchByExpression finds stored messages whose content matches an expression.
func (s *Store) SearchByExpression(ctx context.Context, q ExpressionSearch) (*ExpressionResult, error) {
	if q.Where == "" {
		return nil, fmt.Errorf("a search expression is required")
	}

	// Compiled before anything is read. A bad expression should cost nothing, and it is the most
	// likely thing to be wrong.
	e, err := expr.Parse(q.Where)
	if err != nil {
		return nil, fmt.Errorf("that expression could not be understood: %w", err)
	}

	examine := q.Examine
	if examine <= 0 {
		examine = defaultExamine
	}
	if examine > maxExamine {
		examine = maxExamine
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}

	// Matches is initialised rather than left nil, so a search that finds nothing marshals as an
	// empty array and not as null. A client doing result.matches.length on null crashes, and it
	// would crash on exactly the search that found nothing - which is the common case and the one
	// nobody tests by hand.
	res := &ExpressionResult{Paths: e.Paths(), Matches: []Message{}}

	// Narrow in SQL, ordered newest first so a bounded scan looks at recent traffic.
	rows, err := s.querier().QueryContext(ctx, `
		SELECT id, channel, received_at, control_id, message_type, trigger_event,
		       sender, remote, outcome, ack_code, size, segments, duration_ms, error, raw
		  FROM messages
		 WHERE `+narrowClause(q)+`
		   AND raw IS NOT NULL AND length(raw) > 0
		 ORDER BY id DESC
		 LIMIT ?`, append(narrowArgs(q), examine)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			// Checked per row: a scan of fifty thousand messages is long enough that somebody
			// navigates away, and finishing after they have gone is waste.
			return res, err
		}

		m, raw, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		res.Examined++

		msg, err := hl7.Parse(raw)
		if err != nil {
			// Counted, not silently skipped. A search over traffic that is largely unparseable
			// found nothing for a reason worth knowing.
			res.Unreadable++
			continue
		}

		ok, err := e.Eval(msg)
		if err != nil {
			// An expression that cannot be evaluated against a particular message is not a
			// match and is not a failure of the search: a path that does not resolve in one
			// message resolves in the next.
			continue
		}
		if !ok {
			continue
		}

		if len(res.Matches) < limit {
			// The payload is deliberately dropped from a result row. A search returning a
			// hundred messages should not return a hundred payloads to a browser; the detail
			// view fetches one when somebody opens it.
			m.Raw = nil
			res.Matches = append(res.Matches, *m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if res.Examined >= examine {
		res.Truncated = true
	}

	// How many could have been examined, so a reader can judge the answer.
	if err := s.querier().QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE `+narrowClause(q)+
			` AND raw IS NOT NULL AND length(raw) > 0`,
		narrowArgs(q)...).Scan(&res.Available); err != nil {
		res.Available = res.Examined
	}

	return res, nil
}

// narrowClause builds the SQL that runs before anything is parsed.
func narrowClause(q ExpressionSearch) string {
	clause := "1=1"
	if q.Channel != "" {
		clause += " AND channel = ?"
	}
	if q.MessageType != "" {
		clause += " AND message_type = ?"
	}
	if q.Outcome != "" {
		clause += " AND outcome = ?"
	}
	if !q.Since.IsZero() {
		clause += " AND received_at >= ?"
	}
	if !q.Until.IsZero() {
		clause += " AND received_at <= ?"
	}
	return clause
}

func narrowArgs(q ExpressionSearch) []any {
	var args []any
	if q.Channel != "" {
		args = append(args, q.Channel)
	}
	if q.MessageType != "" {
		args = append(args, q.MessageType)
	}
	if q.Outcome != "" {
		args = append(args, string(q.Outcome))
	}
	if !q.Since.IsZero() {
		args = append(args, dbtime.Format(q.Since))
	}
	if !q.Until.IsZero() {
		args = append(args, dbtime.Format(q.Until))
	}
	return args
}

// scanMessage reads one row of the content-search query.
//
// Written out rather than shared with List, because List deliberately does not select the payload
// and this must. Sharing would mean one of the two carrying a column it does not want, and the one
// that does not want it is the one that runs on every page load.
func scanMessage(rows interface {
	Scan(dest ...any) error
}) (*Message, []byte, error) {
	var (
		m          Message
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

	if err := rows.Scan(&m.ID, &m.Channel, &receivedAt, &controlID, &msgType, &event,
		&sender, &remote, &outcome, &ackCode, &m.Size, &m.Segments,
		&m.DurationMS, &errText, &raw); err != nil {
		return nil, nil, err
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

	// Copied out of the scan buffer, because a driver may reuse the backing array between rows.
	// Without this every message in a scan can end up being tested against the same bytes, which
	// would make a search return either everything or nothing for reasons impossible to see.
	body := make([]byte, len(raw))
	copy(body, raw)

	return &m, body, nil
}
