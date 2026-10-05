package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DirectoryGroups are the directory groups a federated user was in at their last sign-in. Empty for a local account.
func (s *Store) DirectoryGroups(ctx context.Context, userID int64) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT directory_groups FROM users WHERE id = ?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || raw == "" {
		return nil, err
	}
	return strings.Split(raw, "\n"), nil
}

// DashboardView is a person's saved dashboard view, as the JSON the console saved, or "" when they have none.
func (s *Store) DashboardView(ctx context.Context, userID int64) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT view FROM dashboard_views WHERE user_id = ?`, userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SaveDashboardView stores a person's dashboard view, replacing any they had. An empty view removes it, which returns them
// to the dashboard their role or group is given.
func (s *Store) SaveDashboardView(ctx context.Context, userID int64, view string) error {
	if view == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM dashboard_views WHERE user_id = ?`, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO dashboard_views (user_id, view, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET view = excluded.view, updated_at = excluded.updated_at`,
		userID, view, formatTime(time.Now().UTC()))
	return err
}
