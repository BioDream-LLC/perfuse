package msgstore

import (
	"context"
	"fmt"
)

// Reading stored payloads in bulk, for replaying a change against real traffic.
//
// List deliberately does not load payloads, because listing a thousand messages must not drag a
// thousand bodies along with it. Replay needs exactly the opposite: the bodies and almost none
// of the metadata. Fetching them one at a time through Get would work and would issue a query
// per message, which for the five thousand messages this feature is meant to be pointed at is
// several thousand round trips to answer one question.

// MaxPayloadBatch bounds one bulk read.
//
// Ten thousand messages at a few kilobytes each is a few tens of megabytes held at once, which
// is acceptable for a deliberate action somebody waits on. It is also comfortably more traffic
// than anybody needs to be convinced by: a change that looks safe across ten thousand real
// messages and turns out not to be was not going to be caught by fifty thousand either.
const MaxPayloadBatch = 10000

// RecentPayloads returns the stored bodies for a channel, newest first.
//
// Only messages that were actually parsed and acted on are returned. A message the engine could
// not read tells you nothing about whether a transformation change is safe, and including them
// would put noise in a report whose value depends on being believed.
func (s *Store) RecentPayloads(ctx context.Context, channel string, limit int) ([][]byte, error) {
	if channel == "" {
		return nil, fmt.Errorf("a channel name is required")
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > MaxPayloadBatch {
		limit = MaxPayloadBatch
	}

	// Ordered newest first so that a small limit samples recent traffic. Recent is the right
	// bias: a feed's shape drifts, and a change is being judged against what the sender is
	// doing now rather than what it did two years ago.
	rows, err := s.querier().QueryContext(ctx,
		`SELECT raw FROM messages
		  WHERE channel = ? AND raw IS NOT NULL AND length(raw) > 0
		  ORDER BY id DESC
		  LIMIT ?`, channel, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([][]byte, 0, min(limit, 512))
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			continue
		}
		// Copied, because the driver may reuse the backing array between rows. Without this
		// every entry in the slice can end up pointing at the last row read, which would
		// show as a replay where every message is identical - a false all-clear, which is
		// the worst possible failure for this feature.
		body := make([]byte, len(raw))
		copy(body, raw)
		out = append(out, body)
	}

	return out, rows.Err()
}

// CountPayloads reports how many stored messages a channel has bodies for.
//
// Shown next to a replay so somebody can see what fraction of their history was examined. A
// report over the last thousand of two hundred thousand messages is a different kind of
// evidence from one over the last thousand of eleven hundred.
func (s *Store) CountPayloads(ctx context.Context, channel string) (int, error) {
	var n int
	err := s.querier().QueryRowContext(ctx,
		`SELECT count(*) FROM messages
		  WHERE channel = ? AND raw IS NOT NULL AND length(raw) > 0`, channel).Scan(&n)
	return n, err
}

// CountRecords counts messages recorded for a channel, with or without their bodies.
//
// Paired with CountPayloads so a caller can tell "nothing has arrived" from "things arrived and their bodies were not kept".
// Those need different answers and look identical from a count of payloads alone: replay reported "no messages have been
// recorded for this channel yet" to somebody who had forty thousand of them and payload storage switched off.
//
// That is a bad answer in a specific way. It sends them to look at the feed, which is working, instead of at a setting.
func (s *Store) CountRecords(ctx context.Context, channel string) (int, error) {
	var n int
	err := s.querier().QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE channel = ?`, channel).Scan(&n)

	return n, err
}
