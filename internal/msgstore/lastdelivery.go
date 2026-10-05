package msgstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// LastDelivery is the most recent outcome of a destination, for the connections dashboard: when it last delivered, when it
// last failed, and the error that failure gave.
type LastDelivery struct {
	Channel     string     `json:"channel"`
	Destination string     `json:"destination"`
	Delivered   *time.Time `json:"lastDelivered,omitempty"`
	Failed      *time.Time `json:"lastFailed,omitempty"`
	Error       string     `json:"lastError,omitempty"`
}

// LastDeliveries reports, for every channel and destination with traffic since a time, its last delivery and last failure.
// It carries no message content: the error is the transport's, the times are the message's arrival.
func (s *Store) LastDeliveries(ctx context.Context, tenantID string, since time.Time) ([]LastDelivery, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT m.channel, d.destination,
			MAX(CASE WHEN d.status = 'delivered' THEN m.received_at END),
			MAX(CASE WHEN d.status = 'failed' THEN m.received_at END)
		 FROM message_deliveries d JOIN messages m ON m.id = d.message_id
		 WHERE m.tenant_id = ? AND m.received_at >= ?
		 GROUP BY m.channel, d.destination ORDER BY m.channel, d.destination`,
		tenantOrDefault(tenantID), dbtime.Format(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LastDelivery
	for rows.Next() {
		var d LastDelivery
		var ok, bad sql.NullString
		if err := rows.Scan(&d.Channel, &d.Destination, &ok, &bad); err != nil {
			return nil, err
		}
		if t, err := dbtime.Parse(ok.String); ok.Valid && err == nil {
			d.Delivered = &t
		}
		if t, err := dbtime.Parse(bad.String); bad.Valid && err == nil {
			d.Failed = &t
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Failed == nil {
			continue
		}
		var msg sql.NullString
		_ = s.querier().QueryRowContext(ctx,
			`SELECT d.error FROM message_deliveries d JOIN messages m ON m.id = d.message_id
			 WHERE m.tenant_id = ? AND m.channel = ? AND d.destination = ? AND d.status = 'failed'
			 ORDER BY m.received_at DESC LIMIT 1`, tenantOrDefault(tenantID), out[i].Channel, out[i].Destination).Scan(&msg)
		out[i].Error = msg.String
	}
	return out, nil
}
