package msgstore

import (
	"context"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Figures for the team dashboards: recent messages narrowed by channel, type and outcome, with what each dashboard figure
// needs from them. The figures are aggregated by the caller; nothing read here is shown as it is.

// RecentFilter narrows Recent.
type RecentFilter struct {
	// Channels limits it to these channels; empty is every channel.
	Channels []string
	// Types are message_type prefixes (ORU, 999, 277, CT): a message matches when its type starts with one, or for X12
	// contains it among its transaction sets. Empty is every type.
	Types []string
	// Outcomes limits it to these outcomes; empty is every outcome.
	Outcomes []string
	// WithRaw reads the payload too.
	WithRaw bool
	// Limit caps the rows; 0 is 5000.
	Limit int
}

// RecentMessage is one message, as much of it as a figure needs.
type RecentMessage struct {
	ID         int64
	Channel    string
	Type       string
	Event      string
	Outcome    Outcome
	AckCode    string
	Sender     string
	DurationMS int64
	ReceivedAt time.Time
	// FailedDeliveries counts destinations that failed or are still queued.
	FailedDeliveries int
	Raw              []byte
}

// Recent lists messages received since a time, newest first.
func (s *Store) Recent(ctx context.Context, tenantID string, since time.Time, f RecentFilter) ([]RecentMessage, error) {
	args := []any{tenantOrDefault(tenantID), dbtime.Format(since)}
	where := "m.tenant_id = ? AND m.received_at >= ?"
	if len(f.Channels) > 0 {
		where += " AND m.channel IN (" + placeholders(len(f.Channels)) + ")"
		for _, c := range f.Channels {
			args = append(args, c)
		}
	}
	if len(f.Types) > 0 {
		var ors []string
		for _, t := range f.Types {
			ors = append(ors, "m.message_type LIKE ? OR m.message_type LIKE ?")
			args = append(args, t+"%", "%,"+t+"%")
		}
		where += " AND (" + strings.Join(ors, " OR ") + ")"
	}
	if len(f.Outcomes) > 0 {
		where += " AND m.outcome IN (" + placeholders(len(f.Outcomes)) + ")"
		for _, o := range f.Outcomes {
			args = append(args, o)
		}
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 5000
	}
	raw := "NULL"
	if f.WithRaw {
		raw = "m.raw"
	}
	rows, err := s.querier().QueryContext(ctx, `SELECT m.id, m.channel, COALESCE(m.message_type,''), COALESCE(m.trigger_event,''),
		m.outcome, COALESCE(m.ack_code,''), COALESCE(m.sender,''), m.duration_ms, m.received_at,
		(SELECT COUNT(*) FROM message_deliveries d WHERE d.message_id = m.id AND d.status IN ('failed','queued')), `+raw+`
		FROM messages m WHERE `+where+` ORDER BY m.received_at DESC, m.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentMessage
	for rows.Next() {
		var m RecentMessage
		var outcome, received string
		var payload []byte
		if err := rows.Scan(&m.ID, &m.Channel, &m.Type, &m.Event, &outcome, &m.AckCode, &m.Sender, &m.DurationMS, &received,
			&m.FailedDeliveries, &payload); err != nil {
			return nil, err
		}
		m.Outcome, m.ReceivedAt, m.Raw = Outcome(outcome), parseTime(received), payload
		out = append(out, m)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
