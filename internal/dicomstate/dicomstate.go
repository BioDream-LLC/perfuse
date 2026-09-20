package dicomstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Store remembers what a DICOM query source has already seen.
//
// Separate from the message store because it is not a message and outlives one. A study seen last night must still be
// recognised as seen tonight, whether or not the message it produced was pruned - and message retention is a policy
// somebody sets for entirely unrelated reasons.
type Store struct {
	write *sql.DB
	read  *sql.DB
}

// New opens a state store over an existing pool.
func New(write, read *sql.DB) *Store {
	return &Store{write: write, read: read}
}

// Mark is where a channel's polling has got to.
type Mark struct {
	// HighWater is the point the next query starts from, before the overlap is subtracted.
	HighWater time.Time

	// PolledAt is when the last poll completed.
	PolledAt time.Time

	// FirstPoll reports that this channel has never polled.
	//
	// The distinction matters more than it looks. A first poll against an archive holding a million studies must not emit
	// a message for each, so it records and stays quiet; every poll after reports what is new. Without this flag a poll
	// that found nothing is indistinguishable from never having looked.
	FirstPoll bool
}

// Load reads a channel's mark.
func (s *Store) Load(ctx context.Context, tenantID, channel string) (Mark, error) {
	if s == nil || s.read == nil {
		return Mark{}, errors.New("dicomstate: no database")
	}

	var highWater, polledAt string
	err := s.read.QueryRowContext(ctx,
		`SELECT high_water, polled_at FROM dicom_query_marks WHERE tenant_id = ? AND channel = ?`,
		tenantID, channel).Scan(&highWater, &polledAt)

	if errors.Is(err, sql.ErrNoRows) {
		return Mark{FirstPoll: true}, nil
	}
	if err != nil {
		return Mark{}, fmt.Errorf("dicomstate: reading the mark for %q: %w", channel, err)
	}

	hw, err := time.Parse(time.RFC3339Nano, highWater)
	if err != nil {
		return Mark{}, fmt.Errorf("dicomstate: the stored high water mark for %q is unreadable: %w", channel, err)
	}
	pa, err := time.Parse(time.RFC3339Nano, polledAt)
	if err != nil {
		return Mark{}, fmt.Errorf("dicomstate: the stored poll time for %q is unreadable: %w", channel, err)
	}

	return Mark{HighWater: hw, PolledAt: pa}, nil
}

// SaveMark records where polling has got to.
func (s *Store) SaveMark(ctx context.Context, tenantID, channel string, highWater, polledAt time.Time) error {
	if s == nil || s.write == nil {
		return errors.New("dicomstate: no database")
	}

	_, err := s.write.ExecContext(ctx,
		`INSERT INTO dicom_query_marks (tenant_id, channel, high_water, polled_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(tenant_id, channel) DO UPDATE SET high_water = excluded.high_water, polled_at = excluded.polled_at`,
		tenantID, channel, dbtime.Format(highWater), dbtime.Format(polledAt))
	if err != nil {
		return fmt.Errorf("dicomstate: saving the mark for %q: %w", channel, err)
	}

	return nil
}

// Unseen filters identifiers down to those not already recorded.
//
// Order is preserved, because the caller emits in the order the archive answered and a reordered batch would make the
// message history harder to reconcile against the archive's own listing.
func (s *Store) Unseen(ctx context.Context, tenantID, channel string, identifiers []string) ([]string, error) {
	if s == nil || s.read == nil {
		return nil, errors.New("dicomstate: no database")
	}
	if len(identifiers) == 0 {
		return nil, nil
	}

	// Queried as a set rather than one row at a time. A poll returning five hundred studies would otherwise be five
	// hundred round trips, and the whole point of the limit is that five hundred is a normal number here.
	seen := make(map[string]bool, len(identifiers))

	const batch = 200
	for start := 0; start < len(identifiers); start += batch {
		end := start + batch
		if end > len(identifiers) {
			end = len(identifiers)
		}
		chunk := identifiers[start:end]

		query := `SELECT identifier FROM dicom_query_seen WHERE tenant_id = ? AND channel = ? AND identifier IN (`
		args := []any{tenantID, channel}
		for i, id := range chunk {
			if i > 0 {
				query += ","
			}
			query += "?"
			args = append(args, id)
		}
		query += ")"

		rows, err := s.read.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("dicomstate: checking what has been seen for %q: %w", channel, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			seen[id] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}

	out := make([]string, 0, len(identifiers))
	for _, id := range identifiers {
		if !seen[id] {
			out = append(out, id)
		}
	}

	return out, nil
}

// MarkSeen records identifiers as seen.
//
// Called after the message has been handled rather than before. The consequence is that a crash between handling and
// recording re-emits the study on the next poll, which is a duplicate - and a duplicate a downstream system can recognise
// by its SOP instance UID is a far better failure than a study nobody ever hears about.
func (s *Store) MarkSeen(ctx context.Context, tenantID, channel string, identifiers []string, at time.Time) error {
	if s == nil || s.write == nil {
		return errors.New("dicomstate: no database")
	}
	if len(identifiers) == 0 {
		return nil
	}

	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO dicom_query_seen (tenant_id, channel, identifier, seen_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(tenant_id, channel, identifier) DO NOTHING`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	stamp := dbtime.Format(at)
	for _, id := range identifiers {
		if _, err := stmt.ExecContext(ctx, tenantID, channel, id, stamp); err != nil {
			return fmt.Errorf("dicomstate: recording %q as seen: %w", id, err)
		}
	}

	return tx.Commit()
}

// Prune drops identifiers older than the retention period.
//
// Bounded by the same overlap the queries use. An identifier older than the window can never be returned by a query again,
// so keeping it grows the table forever to prevent a duplicate that cannot happen.
//
// A generous multiple of the overlap rather than exactly the overlap, because the two are compared against different
// clocks - seen_at is ours, the query window is the archive's - and pruning right at the boundary would reintroduce the
// duplicate this exists to prevent.
func (s *Store) Prune(ctx context.Context, tenantID, channel string, retain time.Duration, now time.Time) (int64, error) {
	if s == nil || s.write == nil {
		return 0, errors.New("dicomstate: no database")
	}

	cutoff := dbtime.Format(now.Add(-retain))
	res, err := s.write.ExecContext(ctx,
		`DELETE FROM dicom_query_seen WHERE tenant_id = ? AND channel = ? AND seen_at < ?`,
		tenantID, channel, cutoff)
	if err != nil {
		return 0, fmt.Errorf("dicomstate: pruning seen studies for %q: %w", channel, err)
	}

	return res.RowsAffected()
}

// Forget removes all state for a channel.
//
// Exists so that a deliberate replay is possible. Without it the only way to re-emit studies already seen would be to edit
// the database by hand, which is the kind of thing people do at three in the morning and get wrong.
func (s *Store) Forget(ctx context.Context, tenantID, channel string) error {
	if s == nil || s.write == nil {
		return errors.New("dicomstate: no database")
	}

	if _, err := s.write.ExecContext(ctx,
		`DELETE FROM dicom_query_seen WHERE tenant_id = ? AND channel = ?`, tenantID, channel); err != nil {
		return err
	}
	if _, err := s.write.ExecContext(ctx,
		`DELETE FROM dicom_query_marks WHERE tenant_id = ? AND channel = ?`, tenantID, channel); err != nil {
		return err
	}

	return nil
}

// Count reports how many identifiers are remembered for a channel, for the interface to show.
func (s *Store) Count(ctx context.Context, tenantID, channel string) (int, error) {
	if s == nil || s.read == nil {
		return 0, errors.New("dicomstate: no database")
	}

	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dicom_query_seen WHERE tenant_id = ? AND channel = ?`,
		tenantID, channel).Scan(&n)

	return n, err
}
