package terminology

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The unmapped-values list.
//
// Every value a map step could not find is counted, so the person who keeps a table can see what arrives that it does not
// cover - a new ward code, a lab that started sending a new status - and map it, rather than finding out from a receiver.
//
// Counted in memory and written in batches, so a feed sending one unmapped code in every message costs a map increment, not a
// database write per message. Only short values are kept (64 characters or fewer): a map step pointed at a free-text field by
// mistake would otherwise record whatever patients' names or notes passed through it.

const (
	maxUnmappedValue = 64
	maxPending       = 10000
)

type unmappedKey struct{ tenant, channel, table, value string }

type unmappedCount struct {
	n           int64
	first, last time.Time
}

// NoteUnmapped counts one value a table did not have.
func (s *Store) NoteUnmapped(tenant, channel, table, value string) {
	if s == nil || value == "" || len(value) > maxUnmappedValue || !utf8.ValidString(value) {
		return
	}
	if tenant == "" {
		tenant = "main"
	}
	now := time.Now().UTC()
	k := unmappedKey{tenant, channel, table, value}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.pending[k]; c != nil {
		c.n++
		c.last = now
		return
	}
	if len(s.pending) >= maxPending {
		s.overflow++
		return
	}
	s.pending[k] = &unmappedCount{n: 1, first: now, last: now}
}

// Flush writes what has been counted since the last flush.
func (s *Store) Flush(ctx context.Context) error {
	s.mu.Lock()
	batch := s.pending
	s.pending = map[unmappedKey]*unmappedCount{}
	s.overflow = 0
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.PrepareContext(ctx, `INSERT INTO unmapped_values (tenant, channel, table_name, value, count, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant, channel, table_name, value) DO UPDATE SET count = unmapped_values.count + excluded.count,
			last_seen = excluded.last_seen, dismissed = 0`)
	if err != nil {
		return err
	}
	defer st.Close()
	for k, c := range batch {
		if _, err := st.ExecContext(ctx, k.tenant, k.channel, k.table, k.value, c.n,
			c.first.Format(time.RFC3339), c.last.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Run flushes every interval until ctx ends, and once more at the end.
func (s *Store) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.Flush(context.Background())
			return
		case <-t.C:
			_ = s.Flush(ctx)
		}
	}
}

// Unmapped is one value a table did not have, across the channels that met it.
type Unmapped struct {
	Table     string    `json:"table"`
	Value     string    `json:"value"`
	Count     int64     `json:"count"`
	Channels  []string  `json:"channels"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// ListUnmapped lists what has not been dismissed for a tenant, the most frequent first. allowed, when not nil, limits the
// channels a caller may see.
func (s *Store) ListUnmapped(ctx context.Context, tenant string, allowed func(channel string) bool) ([]Unmapped, error) {
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	if tenant == "" {
		tenant = "main"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT table_name, value, channel, count, first_seen, last_seen FROM unmapped_values
		WHERE tenant = ? AND dismissed = 0`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ table, value string }
	by := map[key]*Unmapped{}
	var order []key
	for rows.Next() {
		var table, value, channel, first, last string
		var n int64
		if err := rows.Scan(&table, &value, &channel, &n, &first, &last); err != nil {
			return nil, err
		}
		if allowed != nil && !allowed(channel) {
			continue
		}
		f, _ := time.Parse(time.RFC3339, first)
		l, _ := time.Parse(time.RFC3339, last)
		k := key{table, value}
		u := by[k]
		if u == nil {
			u = &Unmapped{Table: table, Value: value, FirstSeen: f, LastSeen: l, Channels: []string{}}
			by[k] = u
			order = append(order, k)
		}
		u.Count += n
		u.Channels = append(u.Channels, channel)
		if f.Before(u.FirstSeen) {
			u.FirstSeen = f
		}
		if l.After(u.LastSeen) {
			u.LastSeen = l
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Unmapped, 0, len(order))
	for _, k := range order {
		sort.Strings(by[k].Channels)
		out = append(out, *by[k])
	}
	// Most seen first, then by table and value, so a refresh does not reorder rows with equal counts.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		return a.Value < b.Value
	})
	return out, nil
}

// Dismiss hides a value from the list until it is seen again.
func (s *Store) Dismiss(ctx context.Context, tenant, table, value string) error {
	if tenant == "" {
		tenant = "main"
	}
	if err := s.Flush(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE unmapped_values SET dismissed = 1 WHERE tenant = ? AND table_name = ? AND value = ?`,
		tenant, table, value)
	return err
}

// Forget removes values a table now maps, and anything not seen within the retention window.
func (s *Store) Forget(ctx context.Context, tenant, table string, values []string, olderThan time.Duration) error {
	if tenant == "" {
		tenant = "main"
	}
	// What is still counted in memory first, or a value mapped a moment ago would come back at the next flush.
	if err := s.Flush(ctx); err != nil {
		return err
	}
	if len(values) > 0 {
		q := `DELETE FROM unmapped_values WHERE tenant = ? AND table_name = ? AND value IN (?` + strings.Repeat(", ?", len(values)-1) + `)`
		args := []any{tenant, table}
		for _, v := range values {
			args = append(args, v)
		}
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	// Retention is the server's, not a tenant's: it prunes every tenant's old values, which is what the server's
	// retention setting means (cmd/perfuse calls it with no tenant for exactly that). A caller pruning for one tenant
	// passes olderThan 0 and names the values instead.
	if olderThan > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM unmapped_values WHERE last_seen < ?`,
			time.Now().UTC().Add(-olderThan).Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}
