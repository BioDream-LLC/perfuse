// Package store holds the data that is genuinely stateful: users, sessions and
// an audit trail.
//
// Channels are deliberately not in here. They live in files, because that is
// what gives git, diffs, code review and environment promotion for free. Putting
// channels in a database is the decision that forces every one of those to be
// bought separately, and it is the reason a third-party product exists purely to
// shuttle Mirth channels between its database and a git repository.
//
// So the split is: configuration in files, identity and history in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/sqlitedb"

	"github.com/biodream-llc/perfuse/internal/tenant"

	_ "modernc.org/sqlite" // pure Go, so CGO_ENABLED=0 still produces one static binary

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Role is what a user is allowed to do.
type Role string

// Roles, from least to most privileged. Each includes everything below it, so a role is one word an administrator can
// reason about rather than a matrix of permissions nobody configures correctly.
const (
	// RoleMonitor can see whether things are working - channel status, counts, alerts, dashboards and metrics - and
	// nothing that holds patient data or credentials: no message contents, no channel files, no queue items. It is for a
	// help desk or an operations screen on a wall.
	RoleMonitor Role = "monitor"
	// RoleViewer can see channels, their status and the audit log.
	RoleViewer Role = "viewer"
	// RoleOperator can additionally resend: reprocess a message, replay a channel and retry queued deliveries. It cannot
	// change a channel, so the people who fix a failed delivery at night need not be the people who can rewrite the feed.
	RoleOperator Role = "operator"
	// RoleEditor can additionally create, change and delete channels.
	RoleEditor Role = "editor"
	// RoleAdmin can additionally manage users and start or stop channels.
	//
	// Within one tenant. An admin is the most powerful role a customer's own staff
	// can hold, and it deliberately stops at their organisation's boundary.
	RoleAdmin Role = "admin"
	// RolePlatform operates the engine itself: creating tenants, reading across
	// them, and investigating incidents that span customers.
	//
	// Separate from admin rather than being the top of it, because otherwise every
	// customer's own administrator could read every other customer's data. In a
	// managed service that is not a privilege escalation bug, it is the entire
	// product being wrong.
	RolePlatform Role = "platform"
)

// Valid reports whether the role is one we know.
func (r Role) Valid() bool {
	switch r {
	case RoleMonitor, RoleViewer, RoleOperator, RoleEditor, RoleAdmin, RolePlatform:
		return true
	}
	return false
}

// rank orders roles for permission checks.
func (r Role) rank() int {
	switch r {
	case RoleMonitor:
		return 1
	case RoleViewer:
		return 2
	case RoleOperator:
		return 3
	case RoleEditor:
		return 4
	case RoleAdmin:
		return 5
	case RolePlatform:
		return 6
	default:
		return 0
	}
}

// AtLeast reports whether this role includes the privileges of another.
func (r Role) AtLeast(other Role) bool {
	return r.rank() >= other.rank() && r.rank() > 0
}

// Common errors.
var (
	// ErrNotFound means no such record.
	ErrNotFound = errors.New("store: not found")
	// ErrDuplicate means a unique constraint was violated, typically a username.
	ErrDuplicate = errors.New("store: already exists")

	// ErrInvalid marks a request that was never going to work: a missing name, a role that does not exist,
	// an identity with half its parts.
	//
	// It exists because the alternative is what shipped: a validation failure with no sentinel fell through
	// to the default branch of the error mapper and became 500 "something went wrong". An administrator who
	// left the username blank was told the server had broken. They would reasonably then check logs, restart
	// the service, and eventually open a ticket - for a blank field.
	//
	// Wrap rather than replace, so the specific message survives to be shown. Anything wrapping this is a
	// 400 by definition: the caller can fix it, and telling them what to fix is the whole job.
	ErrInvalid = errors.New("invalid request")
	// ErrInvalidCredentials is returned for both an unknown user and a wrong
	// password, so the response cannot be used to enumerate accounts.
	ErrInvalidCredentials = errors.New("store: invalid credentials")
	// ErrDisabled means the account exists but has been turned off.
	ErrDisabled = errors.New("store: account is disabled")
)

// User is an account.
type User struct {
	ID        int64      `json:"id"`
	Username  string     `json:"username"`
	Role      Role       `json:"role"`
	Disabled  bool       `json:"disabled"`
	CreatedAt time.Time  `json:"createdAt"`
	LastLogin *time.Time `json:"lastLogin,omitempty"`

	// ExternalID is the identity provider's own identifier, when this account was provisioned over SCIM.
	//
	// Empty for a locally created account. Its presence is worth knowing beyond provisioning: an account the identity
	// provider owns should be changed there rather than here, because a local change is overwritten at the next sync
	// and a local role grant silently disappears.
	ExternalID string `json:"externalId,omitempty"`
	// ChannelGroups limits the account to the channels in these groups (a channel's `group:`). Empty: every channel.
	ChannelGroups []string `json:"channelGroups,omitempty"`
	// GroupRoles gives the account a different role in some of its channel groups (each key one of ChannelGroups). A group
	// absent here has the account's Role.
	GroupRoles map[string]Role `json:"groupRoles,omitempty"`
	// hash is never serialised.
	hash string
}

// Session is an authenticated login.
type Session struct {
	UserID   int64
	Username string
	Role     Role
	// TenantID is which organisation this session acts for.
	//
	// Read from the user's row on every lookup rather than stored on the session,
	// so there is one source of truth. A user cannot move between tenants, so this
	// cannot drift - and if that ever changes, a denormalised copy would be the
	// thing that quietly authorised the wrong organisation.
	TenantID  tenant.ID
	ExpiresAt time.Time

	// FHIRGroups limits an API token to these FHIR Group ids on the FHIR endpoint. Empty means no limit, and it is always
	// empty for a person's session.
	FHIRGroups []string

	// ChannelGroups limits the session to channels in these groups. Empty means every channel of the tenant.
	ChannelGroups []string

	// Scopes are an API token's grants beyond reading through MCP (ScopeMCPWrite); always empty for a person.
	Scopes []string

	// baseRole is the account's own role when GroupRoles raised Role above it.
	baseRole Role

	// GroupRoles is the role in each channel group that differs from the account's own. Role is then the strongest of them,
	// so a route needing it is reached, and the channels it acts on are narrowed to the groups where the role is held.
	GroupRoles map[string]Role
}

// RoleIn is the session's role for a channel in this group: its role there if it has one of its own, the account's role in its
// other groups, and nothing outside them.
func (s *Session) RoleIn(group string) Role {
	if s == nil {
		return ""
	}
	if !s.SeesChannelGroup(group) {
		return ""
	}
	if r, ok := s.GroupRoles[group]; ok {
		return r
	}
	if s.baseRole != "" {
		return s.baseRole
	}
	return s.Role
}

// strongest returns the stronger of two roles.
func strongest(a, b Role) Role {
	if b.rank() > a.rank() {
		return b
	}
	return a
}

// joinGroupRoles writes group roles as "group=role,group=role", sorted.
func joinGroupRoles(m map[string]Role) string {
	parts := make([]string, 0, len(m))
	for g, r := range m {
		g = strings.TrimSpace(g)
		if g == "" || strings.ContainsAny(g, ",=") || !r.Valid() {
			continue
		}
		parts = append(parts, g+"="+string(r))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// splitGroupRoles reads what joinGroupRoles wrote.
func splitGroupRoles(v string) map[string]Role {
	var out map[string]Role
	for _, part := range strings.Split(v, ",") {
		g, r, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || g == "" || !Role(r).Valid() {
			continue
		}
		if out == nil {
			out = map[string]Role{}
		}
		out[g] = Role(r)
	}
	return out
}

// SeesChannelGroup reports whether the session may see a channel in this group. A limited session never sees a channel
// with no group, because nobody chose to share it with them.
func (s *Session) SeesChannelGroup(group string) bool {
	if s == nil || len(s.ChannelGroups) == 0 {
		return true
	}
	for _, g := range s.ChannelGroups {
		if g == group {
			return true
		}
	}
	return false
}

// AuditEntry is one recorded action.
type AuditEntry struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Username string    `json:"username"`
	Action   string    `json:"action"`
	Target   string    `json:"target,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	IP       string    `json:"ip,omitempty"`
}

// Store is the database.
type Store struct {
	db *sql.DB

	// OnAudit, when set, is told of every audit row once it is written: the patient-data access monitor reads who is
	// reading what from it. Set before the server starts serving; it must not block.
	OnAudit func(tenant.ID, AuditEntry)

	// SessionLifetimeFn supersedes DefaultSessionLifetime when set, so the sign-in duration can be changed
	// through settings without a restart. Assigned in serve.go.
	//
	// A function rather than a value for the reason the message store uses one: reading a plain field while
	// somebody saves settings is a data race, and the settings store already holds the lock that makes this
	// safe. Every read goes through SessionLifetime, and the const is the fallback rather than a second
	// source - one value read from two places is exactly how the retention window ended up with two
	// different cutoffs.
	SessionLifetimeFn func() time.Duration
}

// SessionLifetime is how long a new or refreshed login lasts without activity.
func (s *Store) SessionLifetime() time.Duration {
	if s.SessionLifetimeFn != nil {
		if d := s.SessionLifetimeFn(); d > 0 {
			return d
		}
	}

	return DefaultSessionLifetime
}

// Open opens or creates the database at path and applies the schema.
//
// Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	// Foreign keys are enabled for both, and that matters more than it looks.
	//
	// SQLite disables foreign key enforcement by default, per connection. An earlier version of this function set the
	// pragma only for a file database, so tests running against ":memory:" enforced no constraints at all while
	// production enforced them - which means every ON DELETE CASCADE in the schema was inert in tests and live in
	// production, and a test asserting a cascade would pass for the wrong reason or fail for one.
	//
	// Found by writing a test that a deleted account takes its passkeys with it. It failed, the cascade was correct, and
	// the divergence was here.
	if sqlitedb.IsPostgresURL(path) || sqlitedb.TestPostgres() {
		db, err := sqlitedb.OpenSQL(path)
		if err != nil {
			return nil, err
		}
		return OpenDB(db)
	}
	dsn := "file::memory:?_pragma=foreign_keys(1)"
	if path != ":memory:" {
		// WAL for concurrent readers alongside a writer, and a busy timeout so a
		// brief lock is a wait rather than an error.
		dsn = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	// SQLite takes one writer at a time. Limiting the pool to a single
	// connection turns write contention into queuing instead of "database is
	// locked" errors under load.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenDB prepares the schema on a database handle the caller already owns.
//
// This is how the whole application shares one SQLite file: users, messages and
// FHIR resources in one place, so a deployment has one thing to back up rather
// than several that can be restored out of step with each other.
func OpenDB(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the database.
//
// When the handle was supplied by the caller through OpenDB, closing the store
// closes their handle too, so ownership stays with whoever opened it.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for callers that need it.
func (s *Store) DB() *sql.DB { return s.db }

// schema is applied in order. Migrations are append-only: a released statement
// is never edited, because an installation that already ran it will not run it
// again.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT    NOT NULL,
		role          TEXT    NOT NULL,
		disabled      INTEGER NOT NULL DEFAULT 0,
		created_at    TEXT    NOT NULL,
		last_login    TEXT
	)`,

	// Sessions store a hash of the token, never the token itself, so a copy of
	// this database does not hand over live logins.
	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT    NOT NULL,
		expires_at TEXT    NOT NULL,
		ip         TEXT,
		user_agent TEXT
	)`,

	`CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires_at)`,

	// The audit log keeps the username as text rather than only a foreign key,
	// so deleting a user does not erase the record of what they did.
	`CREATE TABLE IF NOT EXISTS audit (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		at       TEXT NOT NULL,
		user_id  INTEGER,
		username TEXT NOT NULL,
		action   TEXT NOT NULL,
		target   TEXT,
		detail   TEXT,
		ip       TEXT
	)`,

	`CREATE INDEX IF NOT EXISTS audit_at ON audit(at DESC)`,
}

func (s *Store) migrate(ctx context.Context) error {
	// The base schema first. Every statement in it is CREATE ... IF NOT EXISTS, so
	// re-running it is harmless and it establishes the tables the versioned
	// migrations then alter.
	for i, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: applying schema statement %d: %w", i, err)
		}
	}
	// Then anything that cannot be expressed idempotently - adding a column,
	// changing a constraint - gated on schema_version so it runs once.
	return s.applyMigrations(ctx)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return sqlitedb.IsUniqueViolation(err)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func formatTime(t time.Time) string {
	return dbtime.Format(t)
}
