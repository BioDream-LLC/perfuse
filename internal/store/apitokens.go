package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tenant"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// APIToken is a credential for a machine rather than a person.
//
// The token itself is never stored, only its hash, so this type describes a token without being able to reproduce
// it. That is the same treatment sessions get, and it means a stolen database yields nothing usable.
type APIToken struct {
	Label     string     `json:"label"`
	Role      Role       `json:"role"`
	CreatedAt time.Time  `json:"createdAt"`
	CreatedBy string     `json:"createdBy"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`

	// hash identifies the row without exposing the token.
	hash string
}

// Revoked reports whether this token has been withdrawn.
func (t APIToken) Revoked() bool { return t.RevokedAt != nil }

// ErrTokenNotFound means no such token, or it has been revoked.
var ErrTokenNotFound = errors.New("store: no such API token")

// CreateAPIToken issues a token and returns it once.
//
// The caller must show it to the operator immediately, because it cannot be recovered afterwards. That is a
// deliberate consequence of storing only the hash, and the interface has to say so rather than offering a reveal
// button that could never work.
func (s *Store) CreateAPIToken(ctx context.Context, label string, role Role, createdBy string) (string, error) {
	return s.createAPITokenIn(ctx, DefaultTenant, label, role, createdBy)
}

func (s *Store) createAPITokenIn(ctx context.Context, tid tenant.ID, label string, role Role, createdBy string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		// Required, because a list of tokens with no labels cannot be pruned safely - nobody will revoke one they
		// cannot identify, so they all stay forever.
		return "", errors.New("an API token needs a label saying what it is for, or nobody will ever dare revoke it")
	}
	if !role.Valid() {
		return "", fmt.Errorf("%q is not a role", role)
	}

	token, hash, err := newSessionToken()
	if err != nil {
		return "", err
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (token_hash, tenant_id, label, role, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, string(tid), label, string(role), dbtime.Format(time.Now()), createdBy)
	if err != nil {
		return "", err
	}

	return token, nil
}

// LookupAPIToken resolves a token to a session-like identity.
//
// Returns ErrTokenNotFound for an unknown or revoked token, so a revoked token reads exactly like a wrong one from
// outside. Distinguishing them would tell an attacker which guesses were once valid.
func (s *Store) LookupAPIToken(ctx context.Context, token string) (*Session, error) {
	hash := hashToken(token)

	var (
		tid       string
		label     string
		role      string
		revokedAt sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT tenant_id, label, role, revoked_at FROM api_tokens WHERE token_hash = ?`,
		hash).Scan(&tid, &label, &role, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, err
	}
	if revokedAt.Valid && revokedAt.String != "" {
		return nil, ErrTokenNotFound
	}

	// Recorded so an operator can tell a token in use from one nobody remembers issuing. Best-effort: a failure to
	// write the timestamp must not fail the request, because that would turn a bookkeeping problem into an outage.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used = ? WHERE token_hash = ?`,
		dbtime.Format(time.Now()), hash)

	return &Session{
		// Named so the audit log distinguishes a machine from a person. A fleet poll appearing as a username
		// somebody recognises would be actively misleading.
		Username: "token:" + label,
		Role:     Role(role),
		TenantID: tenant.ID(tid),
	}, nil
}

// ListAPITokens returns every token, revoked ones included.
//
// Revoked tokens are listed rather than hidden, because the audit log names them and an entry pointing at something
// invisible is worse than a row marked withdrawn.
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	return s.listAPITokensIn(ctx, DefaultTenant)
}

func (s *Store) listAPITokensIn(ctx context.Context, tid tenant.ID) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_hash, label, role, created_at, created_by, last_used, revoked_at
		 FROM api_tokens WHERE tenant_id = ?
		 ORDER BY revoked_at IS NOT NULL, label`,
		string(tid))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []APIToken
	for rows.Next() {
		var (
			t                   APIToken
			created, createdBy  string
			lastUsed, revokedAt sql.NullString
		)
		if err := rows.Scan(&t.hash, &t.Label, &t.Role, &created, &createdBy, &lastUsed, &revokedAt); err != nil {
			return nil, err
		}
		t.CreatedBy = createdBy
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if lastUsed.Valid && lastUsed.String != "" {
			if at, err := time.Parse(time.RFC3339Nano, lastUsed.String); err == nil {
				t.LastUsed = &at
			}
		}
		if revokedAt.Valid && revokedAt.String != "" {
			if at, err := time.Parse(time.RFC3339Nano, revokedAt.String); err == nil {
				t.RevokedAt = &at
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken withdraws a token by label.
//
// By label rather than by the token itself, because whoever revokes it does not have it - that is usually the whole
// reason they are revoking it.
func (s *Store) RevokeAPIToken(ctx context.Context, label string) error {
	return s.revokeAPITokenIn(ctx, DefaultTenant, label)
}

func (s *Store) revokeAPITokenIn(ctx context.Context, tid tenant.ID, label string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ?
		 WHERE tenant_id = ? AND label = ? AND revoked_at IS NULL`,
		dbtime.Format(time.Now()), string(tid), label)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}
