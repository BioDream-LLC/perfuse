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

	// FHIRGroups limits the token, on the FHIR endpoint, to these Group ids. Empty means no limit.
	FHIRGroups []string `json:"fhirGroups"`

	// ChannelGroups limits the token to the channels in these groups, as for a person. Empty means every channel.
	ChannelGroups []string `json:"channelGroups,omitempty"`

	// Scopes are what the token may do beyond reading through the MCP server: ScopeMCPWrite. Empty means read only.
	Scopes []string `json:"scopes,omitempty"`

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
		groups    string
		channels  string
		scopes    string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT tenant_id, label, role, revoked_at, fhir_groups, channel_groups, scopes FROM api_tokens WHERE token_hash = ?`,
		hash).Scan(&tid, &label, &role, &revokedAt, &groups, &channels, &scopes)
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
		Username:   "token:" + label,
		Role:       Role(role),
		TenantID:   tenant.ID(tid),
		FHIRGroups: splitGroups(groups),
		// A token can be limited to channel groups like a person, so a facility's script sees only its own channels.
		ChannelGroups: splitGroups(channels),
		Scopes:        splitGroups(scopes),
	}, nil
}

// LimitAPITokenToGroups restricts a token, on the FHIR endpoint, to the given Group ids. An empty list removes the limit.
func (s *Store) LimitAPITokenToGroups(ctx context.Context, label string, groups []string) error {
	return s.limitAPITokenIn(ctx, DefaultTenant, label, groups)
}

func (s *Store) limitAPITokenIn(ctx context.Context, tid tenant.ID, label string, groups []string) error {
	clean := make([]string, 0, len(groups))
	for _, g := range groups {
		g = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(g), "Group/"))
		if g == "" {
			continue
		}
		if strings.ContainsAny(g, ", ") {
			return fmt.Errorf("%q is not a FHIR Group id", g)
		}
		clean = append(clean, g)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET fhir_groups = ? WHERE tenant_id = ? AND label = ? AND revoked_at IS NULL`,
		strings.Join(clean, ","), string(tid), label)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTokenNotFound
	}

	return nil
}

// joinGroups is splitGroups' inverse: trimmed, empty entries dropped, duplicates removed, order kept.
func joinGroups(groups []string) string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" && !seen[g] && !strings.Contains(g, ",") {
			seen[g] = true
			out = append(out, g)
		}
	}
	return strings.Join(out, ",")
}

// LimitAPITokenToChannelGroups limits a token to the channels in these groups; an empty list removes the limit.
func (sc *Scoped) LimitAPITokenToChannelGroups(ctx context.Context, label string, groups []string) error {
	res, err := sc.store.db.ExecContext(ctx,
		`UPDATE api_tokens SET channel_groups = ? WHERE tenant_id = ? AND label = ?`, joinGroups(groups), string(sc.id), label)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// ScopeMCPWrite lets a token act through the MCP server, up to its role: retry and reprocess (operator), start and stop
// channels (admin). Without it every MCP tool that changes something is refused, whatever the role.
const ScopeMCPWrite = "mcp:write"

// SetAPITokenScopes replaces a token's scopes; only ScopeMCPWrite is known.
func (sc *Scoped) SetAPITokenScopes(ctx context.Context, label string, scopes []string) error {
	for _, v := range scopes {
		if v != ScopeMCPWrite {
			return fmt.Errorf("%q is not a token scope; the one scope is %s", v, ScopeMCPWrite)
		}
	}
	res, err := sc.store.db.ExecContext(ctx,
		`UPDATE api_tokens SET scopes = ? WHERE tenant_id = ? AND label = ?`, joinGroups(scopes), string(sc.id), label)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// HasScope says whether a session carries a scope. A person's session carries none: MCP is for machines.
func (s *Session) HasScope(scope string) bool {
	for _, v := range s.Scopes {
		if v == scope {
			return true
		}
	}
	return false
}

func splitGroups(v string) []string {
	var out []string
	for _, g := range strings.Split(v, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}

	return out
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
		`SELECT token_hash, label, role, created_at, created_by, last_used, revoked_at, fhir_groups, channel_groups, scopes
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
			groups, channels    string
			scopes              string
		)
		if err := rows.Scan(&t.hash, &t.Label, &t.Role, &created, &createdBy, &lastUsed, &revokedAt, &groups,
			&channels, &scopes); err != nil {
			return nil, err
		}
		t.ChannelGroups = splitGroups(channels)
		t.Scopes = splitGroups(scopes)
		t.FHIRGroups = splitGroups(groups)
		if t.FHIRGroups == nil {
			t.FHIRGroups = []string{}
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
