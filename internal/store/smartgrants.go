package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SMARTGrants keeps the SMART authorization server's grants in the database, so they survive a restart and are shared by
// every server on one database. It satisfies smartauth.Grants.
type SMARTGrants struct{ s *Store }

// SMARTGrants returns the grants table.
func (s *Store) SMARTGrants() *SMARTGrants { return &SMARTGrants{s: s} }

// Put stores a grant, replacing one under the same key.
func (g *SMARTGrants) Put(ctx context.Context, kind, key string, data []byte, expires time.Time) error {
	_, err := g.s.db.ExecContext(ctx, `INSERT INTO smart_grants (kind, key, data, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (kind, key) DO UPDATE SET data = excluded.data, expires_at = excluded.expires_at`,
		kind, key, string(data), formatTime(expires.UTC()))
	return err
}

// Get reads a live grant.
func (g *SMARTGrants) Get(ctx context.Context, kind, key string, now time.Time) ([]byte, bool, error) {
	var data string
	err := g.s.db.QueryRowContext(ctx, `SELECT data FROM smart_grants WHERE kind = ? AND key = ? AND expires_at > ?`,
		kind, key, formatTime(now.UTC())).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return []byte(data), err == nil, err
}

// Take reads and removes a grant in one statement, so two requests presenting the same code cannot both have it.
func (g *SMARTGrants) Take(ctx context.Context, kind, key string, now time.Time) ([]byte, bool, error) {
	var data, expires string
	err := g.s.db.QueryRowContext(ctx, `DELETE FROM smart_grants WHERE kind = ? AND key = ? RETURNING data, expires_at`,
		kind, key).Scan(&data, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !parseTime(expires).After(now) {
		return nil, false, nil
	}
	return []byte(data), true, nil
}

// Claim stores a key unless a live one is there, in one statement.
func (g *SMARTGrants) Claim(ctx context.Context, kind, key string, expires, now time.Time) (bool, error) {
	res, err := g.s.db.ExecContext(ctx, `INSERT INTO smart_grants (kind, key, data, expires_at) VALUES (?, ?, '{}', ?)
		ON CONFLICT (kind, key) DO UPDATE SET data = excluded.data, expires_at = excluded.expires_at
		WHERE smart_grants.expires_at <= ?`, kind, key, formatTime(expires.UTC()), formatTime(now.UTC()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Delete removes a grant.
func (g *SMARTGrants) Delete(ctx context.Context, kind, key string) error {
	_, err := g.s.db.ExecContext(ctx, `DELETE FROM smart_grants WHERE kind = ? AND key = ?`, kind, key)
	return err
}

// Prune removes expired grants.
func (g *SMARTGrants) Prune(ctx context.Context, now time.Time) error {
	_, err := g.s.db.ExecContext(ctx, `DELETE FROM smart_grants WHERE expires_at <= ?`, formatTime(now.UTC()))
	return err
}
