package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/internal/tenant"
)

// ErrTenantNotFound is returned when a tenant id does not exist.
var ErrTenantNotFound = errors.New("tenant not found")

// ErrTenantExists is returned when creating a tenant that is already there.
var ErrTenantExists = errors.New("tenant already exists")

// CreateTenant adds a tenant.
func (s *Store) CreateTenant(ctx context.Context, t *tenant.Tenant) (*tenant.Tenant, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}

	created := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenants (id, name, disabled, created_at, notes) VALUES (?, ?, ?, ?, ?)`,
		string(t.ID), t.Name, boolToInt(t.Disabled), created, t.Notes)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrTenantExists, t.ID)
		}
		return nil, fmt.Errorf("store: creating tenant: %w", err)
	}

	out := *t
	out.CreatedAt = created
	return &out, nil
}

// Tenant reads one tenant.
func (s *Store) Tenant(ctx context.Context, id tenant.ID) (*tenant.Tenant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, disabled, created_at, COALESCE(notes, '') FROM tenants WHERE id = ?`,
		string(id))

	var t tenant.Tenant
	var disabled int
	if err := row.Scan(&t.ID, &t.Name, &disabled, &t.CreatedAt, &t.Notes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrTenantNotFound, id)
		}
		return nil, fmt.Errorf("store: reading tenant: %w", err)
	}
	t.Disabled = disabled != 0
	return &t, nil
}

// ListTenants returns every tenant, ordered by id so a list is stable between
// requests. An unstable order makes a paginated interface repeat and skip rows.
func (s *Store) ListTenants(ctx context.Context) ([]*tenant.Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, disabled, created_at, COALESCE(notes, '') FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: listing tenants: %w", err)
	}
	defer rows.Close()

	var out []*tenant.Tenant
	for rows.Next() {
		var t tenant.Tenant
		var disabled int
		if err := rows.Scan(&t.ID, &t.Name, &disabled, &t.CreatedAt, &t.Notes); err != nil {
			return nil, err
		}
		t.Disabled = disabled != 0
		out = append(out, &t)
	}
	return out, rows.Err()
}

// UpdateTenant changes the display name, disabled flag and notes.
//
// The id is deliberately not changeable. It names a directory of channel files and
// appears in every historical audit row and message; renaming it would either orphan
// that history or require rewriting it, and rewriting an audit trail is not something
// this program should be able to do.
func (s *Store) UpdateTenant(ctx context.Context, id tenant.ID, name string, disabled bool, notes string) (*tenant.Tenant, error) {
	if name == "" {
		return nil, fmt.Errorf("a tenant needs a display name")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE tenants SET name = ?, disabled = ?, notes = ? WHERE id = ?`,
		name, boolToInt(disabled), notes, string(id))
	if err != nil {
		return nil, fmt.Errorf("store: updating tenant: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: %s", ErrTenantNotFound, id)
	}
	return s.Tenant(ctx, id)
}

// DeleteTenant removes a tenant and, by cascade, its users and sessions.
//
// It deliberately does NOT remove the tenant's messages or audit rows. Deleting a
// customer must not destroy the record of what was already delivered on their
// behalf - that record is frequently the only evidence that a result reached a
// clinician, and retention obligations outlive the commercial relationship.
// Orphaned rows are the correct outcome here, and they remain readable by a
// platform administrator.
func (s *Store) DeleteTenant(ctx context.Context, id tenant.ID) error {
	if id == tenant.Platform {
		return fmt.Errorf("the platform pseudo-tenant cannot be deleted")
	}
	if string(id) == DefaultTenant {
		// Refused, because a single-tenant installation's rows all belong to it and
		// there would be nowhere for new ones to go.
		return fmt.Errorf("the default tenant %q cannot be deleted; disable it instead", DefaultTenant)
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("store: deleting tenant: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrTenantNotFound, id)
	}
	return nil
}

// TenantExists reports whether a tenant is present and enabled.
//
// Both in one call on purpose. A caller that checks existence and then acts on a
// disabled tenant has done the wrong thing, and splitting the question invites
// exactly that.
func (s *Store) TenantExists(ctx context.Context, id tenant.ID) (exists, enabled bool, err error) {
	var disabled int
	row := s.db.QueryRowContext(ctx, `SELECT disabled FROM tenants WHERE id = ?`, string(id))
	switch err := row.Scan(&disabled); {
	case errors.Is(err, sql.ErrNoRows):
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("store: checking tenant: %w", err)
	}
	return true, disabled == 0, nil
}

// CountTenants reports how many tenants exist.
//
// Used to decide whether the interface should mention tenancy at all: an
// installation with one tenant should not grow a tenant switcher it has no use for.
func (s *Store) CountTenants(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tenants`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting tenants: %w", err)
	}
	return n, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
