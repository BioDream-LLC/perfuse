package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/tenant"
	"math/big"
	"strings"
	"time"
)

// bigLen is a helper for crypto/rand.Int.
func bigLen(n int) *big.Int { return big.NewInt(int64(n)) }

// DefaultSessionLifetime is how long a login lasts without activity when nothing overrides it.
//
// The override is Store.SessionLifetimeFn, and every read goes through Store.SessionLifetime. This is deliberately not
// referenced directly outside that method: the retention window was read in two places, one of which kept using the
// startup value after the setting became editable, and it silently deleted content for anyone who changed it.
const DefaultSessionLifetime = 12 * time.Hour

// CreateUser adds an account.
func (s *Store) CreateUser(ctx context.Context, username, password string, role Role) (*User, error) {
	return s.createUserIn(ctx, DefaultTenant, username, password, role)
}

// createUserIn adds a user to one tenant.
//
// The exported CreateUser delegates here with the default tenant, so a
// single-tenant installation and all existing callers keep working without ever
// mentioning tenancy.
func (s *Store) createUserIn(ctx context.Context, tid tenant.ID, username, password string, role Role) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("%w: a username is required", ErrInvalid)
	}
	if !role.Valid() {
		return nil, fmt.Errorf("%w: %q is not a role; use viewer, editor or admin", ErrInvalid, role)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (tenant_id, username, password_hash, role, disabled, created_at)
		 VALUES (?, ?, ?, ?, 0, ?)`,
		string(tid), username, hash, string(role), formatTime(now))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: user %q", ErrDuplicate, username)
		}
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, Role: role, CreatedAt: now}, nil
}

// UserCount returns how many accounts exist, used to detect first run.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// GetUser looks up an account by name.
func (s *Store) GetUser(ctx context.Context, username string) (*User, error) {
	return s.getUserIn(ctx, DefaultTenant, username)
}

// getUserIn reads a user within one tenant.
func (s *Store) getUserIn(ctx context.Context, tid tenant.ID, username string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, last_login, external_id
		 FROM users WHERE tenant_id = ? AND username = ? COLLATE NOCASE`,
		string(tid), username))
}

// userIn reads a user by id, refusing one that belongs to another tenant.
//
// The id is a global integer, so without the tenant clause a caller could read
// another organisation's user by guessing a number. That is the textbook insecure
// direct object reference, and the fix is one clause.
func (s *Store) userIn(ctx context.Context, tid tenant.ID, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, last_login, external_id
		 FROM users WHERE tenant_id = ? AND id = ?`, string(tid), id))
}

// GetUserByID looks up an account by identifier.
func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return s.getUserByIDIn(ctx, "", id)
}

// getUserByIDIn looks a user up by identifier within one tenant, or in any tenant when tid is empty.
//
// The tenant filter is why this exists. User identifiers are global integers, and the by-identifier handlers looked users
// up with WHERE id = ? alone - so one tenant's administrator, counting upwards, could read, demote, disable and delete
// accounts in another organisation. Demonstrated: alpha's administrator deleted beta's.
//
// Both the update and delete handlers look a user up before doing anything, so filtering here closes reading, modifying and
// deleting in one place rather than in three that each have to remember.
func (s *Store) getUserByIDIn(ctx context.Context, tid tenant.ID, id int64) (*User, error) {
	if tid == "" {
		return s.scanUser(s.db.QueryRowContext(ctx,
			`SELECT id, username, password_hash, role, disabled, created_at, last_login, external_id
			 FROM users WHERE id = ?`, id))
	}

	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, last_login, external_id
		 FROM users WHERE id = ? AND tenant_id = ?`, id, string(tid)))
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var (
		u        User
		role     string
		disabled int
		created  string
		last     sql.NullString
		external sql.NullString
	)
	err := row.Scan(&u.ID, &u.Username, &u.hash, &role, &disabled, &created, &last, &external)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	u.Role = Role(role)
	u.Disabled = disabled != 0
	u.CreatedAt = parseTime(created)
	if last.Valid {
		t := parseTime(last.String)
		u.LastLogin = &t
	}
	if external.Valid {
		u.ExternalID = external.String
	}
	return &u, nil
}

// ListUsers returns every account, ordered by name.
func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	return s.listUsersIn(ctx, DefaultTenant)
}

// listUsersIn returns one tenant's users and nobody else's.
func (s *Store) listUsersIn(ctx context.Context, tid tenant.ID) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, last_login, external_id
		 FROM users WHERE tenant_id = ? ORDER BY username`, string(tid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*User
	for rows.Next() {
		var (
			u        User
			role     string
			disabled int
			created  string
			last     sql.NullString
			external sql.NullString
		)
		if err := rows.Scan(&u.ID, &u.Username, &u.hash, &role, &disabled, &created, &last,
			&external); err != nil {
			return nil, err
		}
		u.Role = Role(role)
		u.Disabled = disabled != 0
		u.CreatedAt = parseTime(created)
		if last.Valid {
			t := parseTime(last.String)
			u.LastLogin = &t
		}
		if external.Valid {
			u.ExternalID = external.String
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// SetPassword changes a password.
func (s *Store) SetPassword(ctx context.Context, userID int64, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// SetRole changes a user's role.
func (s *Store) SetRole(ctx context.Context, userID int64, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("store: %q is not a valid role", role)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET role = ? WHERE id = ?`, string(role), userID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// SetDisabled enables or disables an account.
//
// Disabling also removes the user's sessions. Otherwise a disabled account keeps
// working until its cookie happens to expire, which is not what anyone means by
// disabled.
func (s *Store) SetDisabled(ctx context.Context, userID int64, disabled bool) error {
	flag := 0
	if disabled {
		flag = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET disabled = ? WHERE id = ?`, flag, userID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res); err != nil {
		return err
	}
	if disabled {
		return s.DeleteUserSessions(ctx, userID)
	}
	return nil
}

// DeleteUser removes an account and its sessions. The audit log keeps their
// username, so history is not rewritten by removing the account.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	return s.deleteUserIn(ctx, "", userID)
}

// deleteUserIn removes an account within one tenant, or in any tenant when tid is empty.
//
// The tenant filter is belt as well as braces: the handler already looks the user up through a scoped store and refuses
// when it finds nothing. But that protection is invisible at this call site, and invisible coupling between a lookup and a
// delete is exactly how one tenant came to be able to delete another's administrator.
func (s *Store) deleteUserIn(ctx context.Context, tid tenant.ID, userID int64) error {
	if tid != "" {
		// Checked first, so a delete for another tenant's identifier removes nothing and says so.
		if _, err := s.getUserByIDIn(ctx, tid, userID); err != nil {
			return err
		}
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// CountAdmins returns how many enabled administrators exist, so the last one
// cannot be removed or demoted into locking everybody out.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	return s.countAdminsIn(ctx, "")
}

// countAdminsIn counts enabled administrators in one tenant, or in every tenant when tid is empty.
//
// The scoped form matters for the check it feeds: refusing to remove the last administrator. Counting every tenant's would
// let one organisation demote its own last administrator as long as some other organisation still had one - and the person
// discovering that would be locked out of their own engine.
func (s *Store) countAdminsIn(ctx context.Context, tid tenant.ID) (int, error) {
	var n int
	if tid == "" {
		err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0`,
			string(RoleAdmin)).Scan(&n)
		return n, err
	}

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0 AND tenant_id = ?`,
		string(RoleAdmin), string(tid)).Scan(&n)
	return n, err
}

// Authenticate verifies a username and password and creates a session.
//
// An unknown user and a wrong password return the same error, so the response
// cannot be used to find out which accounts exist.
func (s *Store) Authenticate(ctx context.Context, username, password, ip, userAgent string) (token string, u *User, err error) {
	return s.authenticateIn(ctx, DefaultTenant, username, password, ip, userAgent)
}

// authenticateIn verifies credentials within one tenant.
//
// Scoped, which matters more here than anywhere: two tenants can each have an
// "admin", so a lookup by username alone would either fail ambiguously or sign
// somebody into the wrong organisation.
//
// An unknown tenant returns exactly the same error as a wrong password, and does the
// same work. Distinguishing them would let anybody enumerate which organisations are
// hosted on an instance simply by trying names at the login form - and for an MSP the
// customer list is commercially sensitive.
func (s *Store) authenticateIn(ctx context.Context, tid tenant.ID, username, password, ip, userAgent string) (token string, u *User, err error) {
	u, err = s.getUserIn(ctx, tid, username)
	if errors.Is(err, ErrNotFound) {
		// Still spend the time hashing. Returning immediately would make a
		// missing account measurably faster to probe than a wrong password.
		_, _ = HashPassword("dummy-password-for-constant-work")
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}

	ok, needsRehash := VerifyPassword(u.hash, password)
	if !ok {
		return "", nil, ErrInvalidCredentials
	}
	if u.Disabled {
		return "", nil, ErrDisabled
	}

	if needsRehash {
		// The stored hash was made with a lower cost than we now use. Upgrade it
		// while the plaintext is briefly available.
		if err := s.SetPassword(ctx, u.ID, password); err != nil {
			return "", nil, err
		}
	}

	token, hash, err := newSessionToken()
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, ip, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, u.ID, formatTime(now), formatTime(now.Add(s.SessionLifetime())), ip, userAgent); err != nil {
		return "", nil, err
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login = ? WHERE id = ?`, formatTime(now), u.ID); err != nil {
		return "", nil, err
	}

	return token, u, nil
}

// Lookup resolves a session token.
//
// It extends the expiry on use, so an active operator is not logged out mid-task,
// and it rejects a session whose user has since been disabled.
func (s *Store) Lookup(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	hash := hashToken(token)

	var (
		userID   int64
		username string
		role     string
		disabled int
		expires  string
		tenantID string
		// A tenant disabled mid-session must stop working immediately. Read here
		// rather than at login, or somebody suspended for non-payment keeps
		// operating until their session happens to expire.
		tenantDisabled int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT s.user_id, u.username, u.role, u.disabled, s.expires_at, u.tenant_id,
		        COALESCE(t.disabled, 0)
		 FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 LEFT JOIN tenants t ON t.id = u.tenant_id
		 WHERE s.token_hash = ?`, hash).
		Scan(&userID, &username, &role, &disabled, &expires, &tenantID, &tenantDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	expiresAt := parseTime(expires)
	if time.Now().After(expiresAt) {
		// Remove it on the way past rather than leaving it to accumulate.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
		return nil, ErrNotFound
	}
	if disabled != 0 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
		return nil, ErrDisabled
	}
	if tenantDisabled != 0 {
		// The session is deleted, not merely refused. An operator whose organisation
		// has been suspended should be signed out rather than left holding a token
		// that starts working again by itself.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
		return nil, ErrDisabled
	}

	newExpiry := time.Now().UTC().Add(s.SessionLifetime())
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
		formatTime(newExpiry), hash); err != nil {
		return nil, err
	}

	return &Session{
		TenantID:  tenant.ID(tenantID),
		UserID:    userID,
		Username:  username,
		Role:      Role(role),
		ExpiresAt: newExpiry,
	}, nil
}

// DeleteSession logs one session out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// DeleteUserSessions logs a user out everywhere.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PurgeExpiredSessions removes sessions that have lapsed.
func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at < ?`, formatTime(time.Now().UTC()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Audit records an action.
//
// Failing to write the audit log must not fail the operation it describes, but it
// must be visible, so the caller decides what to do with the error rather than
// having it swallowed here.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	return s.auditIn(ctx, DefaultTenant, e)
}

// auditIn records an action against one tenant.
func (s *Store) auditIn(ctx context.Context, auditTenant tenant.ID, e AuditEntry) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	var userID any
	if e.ID != 0 {
		userID = e.ID
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (at, user_id, username, action, target, detail, ip, tenant_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(e.At), userID, e.Username, e.Action, e.Target, e.Detail, e.IP, string(auditTenant))
	return err
}

// ListAudit returns the most recent entries, newest first.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	return s.listAuditIn(ctx, "", limit)
}

// listAuditIn returns one tenant's audit rows, or every tenant's when tid is empty.
//
// The empty case is for a platform administrator investigating across customers. It
// is the one place in this file that deliberately crosses the boundary, which is why
// it is a distinct argument rather than an optional filter somebody might omit by
// accident.
func (s *Store) listAuditIn(ctx context.Context, tid tenant.ID, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	query := `SELECT id, at, username, action, COALESCE(target, ''), COALESCE(detail, ''), COALESCE(ip, '')
		 FROM audit ORDER BY at DESC, id DESC LIMIT ?`
	args := []any{limit}
	if tid != "" {
		query = `SELECT id, at, username, action, COALESCE(target, ''), COALESCE(detail, ''), COALESCE(ip, '')
		 FROM audit WHERE tenant_id = ? ORDER BY at DESC, id DESC LIMIT ?`
		args = []any{string(tid), limit}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var (
			e  AuditEntry
			at string
		)
		if err := rows.Scan(&e.ID, &at, &e.Username, &e.Action, &e.Target, &e.Detail, &e.IP); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
