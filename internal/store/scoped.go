package store

import (
	"context"
	"fmt"

	"github.com/biodream-llc/perfuse/internal/tenant"
)

// Scoped is a Store bound to one tenant.
//
// This type exists because of how cross-tenant leaks actually happen. Nobody writes
// `WHERE tenant_id = 'other-customer'`. What happens is that somebody adds a query
// six months from now and does not think about tenancy at all, and that query looks
// completely correct in review because there is nothing visibly missing from it.
//
// So the tenant is not an argument that can be omitted. It is part of the handle,
// and the methods that read tenant-owned data live here rather than on Store. A
// caller who has a *Scoped cannot address another tenant's rows, and a caller who
// has only a *Store has to say out loud which tenant they mean.
//
// The remaining hole is honest and worth naming: Store still has the unscoped
// methods, because a platform administrator legitimately needs to list every tenant
// and read the whole audit trail. That is the boundary to be careful about, and it
// is one small set of functions rather than every query in the program.
type Scoped struct {
	store *Store
	id    tenant.ID
}

// Scope binds a store to a tenant after checking the tenant exists and is enabled.
//
// Checked here rather than trusted, because the tenant id usually arrives from a
// session or a URL. A handle for a tenant that was deleted five minutes ago would
// otherwise read and write rows belonging to nobody.
func (s *Store) Scope(ctx context.Context, id tenant.ID) (*Scoped, error) {
	// Checked before ValidateID, which would reject it as a reserved name with a
	// less useful message. Somebody passing tenant.Platform did it on purpose and
	// deserves to be told what to do instead.
	if id == tenant.Platform {
		return nil, fmt.Errorf("the platform pseudo-tenant owns no data and cannot be scoped to; use the unscoped methods deliberately")
	}
	if err := tenant.ValidateID(id); err != nil {
		return nil, err
	}

	exists, enabled, err := s.TenantExists(ctx, id)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrTenantNotFound, id)
	}
	if !enabled {
		return nil, fmt.Errorf("tenant %q is disabled", id)
	}
	return &Scoped{store: s, id: id}, nil
}

// ScopeUnchecked binds without verifying the tenant.
//
// For startup paths that have already resolved the tenant, and for tests. Named to
// be uncomfortable to type, because the check it skips is the one that stops a
// deleted tenant's handle from writing rows.
func (s *Store) ScopeUnchecked(id tenant.ID) *Scoped {
	return &Scoped{store: s, id: id}
}

// TenantID reports which tenant this handle is bound to.
func (sc *Scoped) TenantID() tenant.ID { return sc.id }

// Unscoped returns the underlying store.
//
// Deliberately explicit. Somebody reading a call to this can see that tenant
// isolation is being stepped around, which is the point - the alternative is a
// field access that reviewers skim past.
func (sc *Scoped) Unscoped() *Store { return sc.store }

// --- users ------------------------------------------------------------------

// CreateUser adds a user to this tenant.
func (sc *Scoped) CreateUser(ctx context.Context, username, password string, role Role) (*User, error) {
	return sc.store.createUserIn(ctx, sc.id, username, password, role)
}

// ListUsers returns this tenant's users and nobody else's.
func (sc *Scoped) ListUsers(ctx context.Context) ([]*User, error) {
	return sc.store.listUsersIn(ctx, sc.id)
}

// Authenticate signs a user in within this tenant.
func (sc *Scoped) Authenticate(ctx context.Context, username, password, ip, userAgent string) (string, *User, error) {
	return sc.store.authenticateIn(ctx, sc.id, username, password, ip, userAgent)
}

// User reads one user by id, refusing one that belongs to another tenant.
func (sc *Scoped) User(ctx context.Context, id int64) (*User, error) {
	return sc.store.userIn(ctx, sc.id, id)
}

// --- audit ------------------------------------------------------------------

// Audit records an action against this tenant.
func (sc *Scoped) Audit(ctx context.Context, e AuditEntry) error {
	return sc.store.auditIn(ctx, sc.id, e)
}

// ListAudit returns this tenant's audit rows and nobody else's.
func (sc *Scoped) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	return sc.store.listAuditIn(ctx, sc.id, limit)
}

// GetUserByID returns one of this tenant's users, and reports not found for anybody else's.
//
// Not found rather than forbidden, deliberately. Telling a caller that an identifier exists but belongs to somebody else
// confirms the account's existence, which for a managed service also confirms that the other customer exists.
func (sc *Scoped) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return sc.store.getUserByIDIn(ctx, sc.id, id)
}

// CountAdmins counts this tenant's enabled administrators.
//
// Scoped, because the check it feeds - refusing to remove the last administrator - would otherwise count every tenant's
// and happily let one organisation demote its own last administrator as long as another organisation had one.
func (sc *Scoped) CountAdmins(ctx context.Context) (int, error) {
	return sc.store.countAdminsIn(ctx, sc.id)
}

// DeleteUser removes one of this tenant's accounts, and reports not found for anybody else's.
func (sc *Scoped) DeleteUser(ctx context.Context, userID int64) error {
	return sc.store.deleteUserIn(ctx, sc.id, userID)
}

// The by-identifier mutators below all confirm the account belongs to this tenant first.
//
// Belt as well as braces: the handlers already look a user up through GetUserByID above and refuse when it finds nothing.
// But that protection is invisible at each call site, and invisible coupling between a lookup and a mutation is exactly how
// one tenant came to be able to delete another's administrator. A reader of any one of these lines should be able to see
// that it is safe without tracing what happened earlier in the handler.

// SetRole changes one of this tenant's users' roles.
func (sc *Scoped) SetRole(ctx context.Context, userID int64, role Role) error {
	if _, err := sc.GetUserByID(ctx, userID); err != nil {
		return err
	}
	return sc.store.SetRole(ctx, userID, role)
}

// SetPassword changes one of this tenant's users' passwords.
func (sc *Scoped) SetPassword(ctx context.Context, userID int64, password string) error {
	if _, err := sc.GetUserByID(ctx, userID); err != nil {
		return err
	}
	return sc.store.SetPassword(ctx, userID, password)
}

// SetDisabled enables or disables one of this tenant's users.
func (sc *Scoped) SetDisabled(ctx context.Context, userID int64, disabled bool) error {
	if _, err := sc.GetUserByID(ctx, userID); err != nil {
		return err
	}
	return sc.store.SetDisabled(ctx, userID, disabled)
}

// DeleteUserSessions ends one of this tenant's users' sessions.
func (sc *Scoped) DeleteUserSessions(ctx context.Context, userID int64) error {
	if _, err := sc.GetUserByID(ctx, userID); err != nil {
		return err
	}
	return sc.store.DeleteUserSessions(ctx, userID)
}

// CreateAPIToken issues a token belonging to this tenant.
//
// Scoped because a token carries a tenant and acts inside it. A token created without one would act in the default tenant,
// which for a machine caller is a leak nobody is watching: there is no person at the other end to notice that the data
// looks like somebody else's.
func (sc *Scoped) CreateAPIToken(ctx context.Context, label string, role Role, createdBy string) (string, error) {
	return sc.store.createAPITokenIn(ctx, sc.id, label, role, createdBy)
}

// ListAPITokens returns this tenant's tokens and nobody else's.
func (sc *Scoped) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	return sc.store.listAPITokensIn(ctx, sc.id)
}

// RevokeAPIToken withdraws one of this tenant's tokens.
func (sc *Scoped) RevokeAPIToken(ctx context.Context, label string) error {
	return sc.store.revokeAPITokenIn(ctx, sc.id, label)
}

// SetExternalID records the identity provider's own identifier for an account.
//
// The stable join between the two systems. Somebody who marries and changes their username is the same person, and matching
// on username alone would have the provider provision a second account, disable that one, and leave the original enabled -
// a former employee with working access, arrived at by way of a wedding.
//
// A conflict is returned as ErrDuplicate rather than silently overwritten. Two accounts claiming the same provider identity
// means one of them will be deprovisioned in place of the other, and that is worth refusing.
func (sc *Scoped) SetExternalID(ctx context.Context, userID int64, externalID string) error {
	res, err := sc.store.db.ExecContext(ctx,
		`UPDATE users SET external_id = ? WHERE id = ? AND tenant_id = ?`,
		externalID, userID, string(sc.id))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}

		return err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Scoped, so a user in another tenant looks exactly like one that does not exist.
		return ErrNotFound
	}

	return nil
}

// UserByExternalID finds an account by the identity provider's identifier.
func (sc *Scoped) UserByExternalID(ctx context.Context, externalID string) (*User, error) {
	if externalID == "" {
		// Refused rather than matching every account without one. An empty external identifier reaching here means a
		// caller failed to resolve one, and the permissive reading would return an arbitrary local account for a
		// provider to deprovision.
		return nil, ErrNotFound
	}

	return sc.store.scanUser(sc.store.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, last_login, external_id
		 FROM users WHERE tenant_id = ? AND external_id = ?`, string(sc.id), externalID))
}

// CountUserSessions reports how many sessions an account currently has.
//
// Exists so that disabling an account can be verified rather than assumed. That verification matters here more than anywhere
// else in the server: a provisioning system told a deprovisioning succeeded will never ask again, so "probably worked" leaves
// a terminated employee with a working browser tab and nobody looking for it.
func (sc *Scoped) CountUserSessions(ctx context.Context, userID int64) (int, error) {
	var n int
	err := sc.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.user_id = ? AND u.tenant_id = ?`, userID, string(sc.id)).Scan(&n)

	return n, err
}
