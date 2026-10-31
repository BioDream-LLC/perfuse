package msgstore

import (
	"context"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// MinuteCount is how many messages one channel received in one minute.
type MinuteCount struct {
	Channel string
	Minute  time.Time
	Count   int64
}

// maxMinuteRows bounds one read of history: a fortnight of every minute on forty busy channels, which is far more
// than a pattern needs and far less than would strain the server reading it at start-up.
const maxMinuteRows = 600_000

// MinuteCounts returns each channel's messages per minute since a time, for learning what a channel normally carries.
//
// Counts only, never the messages: the access monitor learns rates and hours, and has no use for content.
func (s *Store) MinuteCounts(ctx context.Context, tenantID string, since time.Time) ([]MinuteCount, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT channel, CAST(strftime('%s', received_at) AS INTEGER) / 60 AS minute, COUNT(*)
		   FROM messages
		  WHERE tenant_id = ? AND received_at >= ?
		  GROUP BY channel, minute
		  ORDER BY minute
		  LIMIT ?`,
		tenantOrDefault(tenantID), dbtime.Format(since), maxMinuteRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MinuteCount
	for rows.Next() {
		var c MinuteCount
		var minute int64
		if err := rows.Scan(&c.Channel, &minute, &c.Count); err != nil {
			return nil, err
		}
		c.Minute = time.Unix(minute*60, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
