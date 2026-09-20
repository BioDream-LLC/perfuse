package msgstore

import (
	"context"
	"sort"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// The flow map: what is moving where, over time.
//
// A channel list tells you what exists and a throughput chart tells you how much arrived. Neither tells you that the pharmacy feed
// stopped delivering at 03:40 while everything else kept running - which is the shape of most real incidents. One strand goes dark and
// the totals barely move, because the failing feed was never the busiest one.
//
// So this returns a series per strand, where a strand is one channel-to-destination edge, and the series covers the whole window
// including the parts where nothing happened. A strand that went quiet is a line at zero, not an absent line. That distinction is the
// entire feature: absent draws as nothing, and nothing looks like a feed that was never configured.

// Strand is one channel-to-destination edge over time.
type Strand struct {
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	// Buckets covers the full window, zero-filled.
	Buckets []Bucket `json:"buckets"`
}

// FlowChannel is one channel: what it received, and where it sent.
type FlowChannel struct {
	Channel string `json:"channel"`
	// Received is the source edge - what arrived at this channel, before any destination.
	Received []Bucket `json:"received"`
	// Strands are the destination edges, sorted by name.
	Strands []Strand `json:"strands"`
}

// Flow is the whole map over one window.
type Flow struct {
	Since    time.Time     `json:"since"`
	Bucket   time.Duration `json:"bucketNanos"`
	Channels []FlowChannel `json:"channels"`
}

// Flow returns per-channel and per-destination series over a window.
//
// One query for the destination edges rather than one per strand. A site with twenty channels and three destinations each would otherwise
// issue sixty queries to draw one screen, and the screen polls.
func (s *Store) Flow(ctx context.Context, tenantID string, since time.Time, bucket time.Duration) (*Flow, error) {
	if bucket <= 0 {
		bucket = time.Minute
	}
	seconds := int64(bucket.Seconds())
	if seconds <= 0 {
		seconds = 60
	}

	// The channel edge: what arrived, regardless of what happened to it afterwards.
	received, err := s.flowReceived(ctx, tenantID, since, seconds)
	if err != nil {
		return nil, err
	}

	// The destination edges. message_deliveries carries no time of its own, so the bucket comes from the message it belongs to -
	// which is also what makes a delivery line up with the arrival that caused it.
	strands, err := s.flowStrands(ctx, tenantID, since, seconds)
	if err != nil {
		return nil, err
	}

	// Every channel that appears on either side. A channel that received messages and delivered none still belongs on the map: that
	// is precisely the state somebody is looking for.
	names := map[string]bool{}
	for channel := range received {
		names[channel] = true
	}
	for key := range strands {
		names[key.channel] = true
	}

	out := &Flow{Since: since.UTC(), Bucket: bucket, Channels: []FlowChannel{}}

	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	for _, name := range ordered {
		fc := FlowChannel{
			Channel:  name,
			Received: fillBuckets(received[name], since, seconds),
			Strands:  []Strand{},
		}

		// Sorted, because Go maps range randomly and a flow map whose strands change order between polls is unreadable - lines
		// would swap places under the pointer while somebody is trying to follow one.
		var dests []string
		for key := range strands {
			if key.channel == name {
				dests = append(dests, key.destination)
			}
		}
		sort.Strings(dests)

		for _, d := range dests {
			fc.Strands = append(fc.Strands, Strand{
				Channel:     name,
				Destination: d,
				Buckets:     fillBuckets(strands[strandKey{name, d}], since, seconds),
			})
		}

		out.Channels = append(out.Channels, fc)
	}

	return out, nil
}

// strandKey identifies one channel-to-destination edge.
type strandKey struct {
	channel     string
	destination string
}

// flowReceived counts arrivals per channel per bucket.
func (s *Store) flowReceived(ctx context.Context, tenantID string, since time.Time, seconds int64) (map[string]map[int64]Bucket, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT channel,
		        (CAST(strftime('%s', received_at) AS INTEGER) / ?) * ? AS bucket_start,
		        COUNT(*),
		        SUM(CASE WHEN outcome = 'delivered' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN outcome = 'filtered' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN outcome IN ('failed','partial') THEN 1 ELSE 0 END),
		        SUM(CASE WHEN outcome = 'unparseable' THEN 1 ELSE 0 END)
		   FROM messages
		  WHERE tenant_id = ? AND received_at >= ?
		  GROUP BY channel, bucket_start
		  ORDER BY channel, bucket_start`,
		seconds, seconds, tenantOrDefault(tenantID), dbtime.Format(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]map[int64]Bucket{}
	for rows.Next() {
		var channel string
		var start int64
		var b Bucket
		if err := rows.Scan(&channel, &start, &b.Total, &b.Delivered, &b.Filtered, &b.Failed, &b.Unparseable); err != nil {
			return nil, err
		}
		b.Start = time.Unix(start, 0).UTC()
		if out[channel] == nil {
			out[channel] = map[int64]Bucket{}
		}
		out[channel][start] = b
	}

	return out, rows.Err()
}

// flowStrands counts deliveries per channel, destination and bucket.
func (s *Store) flowStrands(ctx context.Context, tenantID string, since time.Time, seconds int64) (map[strandKey]map[int64]Bucket, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT m.channel,
		        d.destination,
		        (CAST(strftime('%s', m.received_at) AS INTEGER) / ?) * ? AS bucket_start,
		        COUNT(*),
		        SUM(CASE WHEN d.status = 'delivered' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN d.status = 'filtered' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN d.status = 'failed' THEN 1 ELSE 0 END)
		   FROM message_deliveries d
		   JOIN messages m ON m.id = d.message_id
		  WHERE m.tenant_id = ? AND m.received_at >= ?
		  GROUP BY m.channel, d.destination, bucket_start
		  ORDER BY m.channel, d.destination, bucket_start`,
		seconds, seconds, tenantOrDefault(tenantID), dbtime.Format(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[strandKey]map[int64]Bucket{}
	for rows.Next() {
		var channel, destination string
		var start int64
		var b Bucket
		if err := rows.Scan(&channel, &destination, &start, &b.Total, &b.Delivered, &b.Filtered, &b.Failed); err != nil {
			return nil, err
		}
		b.Start = time.Unix(start, 0).UTC()

		key := strandKey{channel, destination}
		if out[key] == nil {
			out[key] = map[int64]Bucket{}
		}
		out[key][start] = b
	}

	return out, rows.Err()
}
