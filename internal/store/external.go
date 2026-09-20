package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tenant"
)

// AuthSource is where an account's identity comes from.
type AuthSource string

const (
	// AuthLocal is a username and password held here.
	AuthLocal AuthSource = "local"

	// AuthOIDC is an identity vouched for by an OpenID Connect provider.
	AuthOIDC AuthSource = "oidc"

	// AuthSAML is an identity asserted by a SAML 2.0 identity provider.
	//
	// Distinct from AuthOIDC, which it used to be recorded as. They are different protocols with different trust arrangements - one verifies a
	// signed XML document against a certificate, the other a token against a discovered key set - and an administrator reading the users
	// screen was told the wrong one. A site running both could not tell its accounts apart, which is the question that matters when somebody
	// leaves and every way they could still get in has to be closed.
	AuthSAML AuthSource = "saml"

	// AuthLDAP is an identity held in a directory, checked by binding to it.
	AuthLDAP AuthSource = "ldap"

	// AuthUnknown means no account of that name exists.
	//
	// Distinct from AuthLocal, which used to be returned for both. Conflating them means a caller cannot tell "this
	// is a local account, check the local password" from "nobody by this name, so a directory may still know them" -
	// and a first directory sign-in, where no account exists yet, would never reach the directory at all.
	AuthUnknown AuthSource = "unknown"
)

// ExternalIdentity is who an identity provider says somebody is.
type ExternalIdentity struct {
	// Issuer and Subject together identify the person.
	//
	// Both, because a subject is only unique within its issuer: two providers can each mint "1000" for different people.
	Issuer  string
	Subject string

	// Email and DisplayName are for display and for the audit log.
	Email       string
	DisplayName string

	// Username is what to call the account if one has to be created.
	Username string

	// Role is the role decided from the provider's claims.
	Role Role

	// Source says which protocol vouched for this identity, when the caller knows.
	//
	// Empty falls back to derivation from the issuer, which is what LDAP relies on. SAML has to state it: a SAML issuer and an OIDC issuer are
	// both https URLs and nothing about either says which protocol produced it, so derivation recorded every SAML account as OIDC. The comment
	// on sourceForIssuer argued for deriving rather than passing so the two could not disagree, which was right for LDAP and does not
	// generalise to two protocols that look identical from the outside.
	Source AuthSource
}

// sourceForIssuer names where an identity came from.
//
// Derived from the issuer rather than passed in, so the two cannot disagree. Hardcoding AuthOIDC here was wrong the
// moment a second kind of external identity existed: an LDAP account recorded as OIDC would be refused a password sign-in
// with a message telling them to use a single sign-on button that may not even be configured.
func sourceForIssuer(issuer string) AuthSource {
	if strings.HasPrefix(issuer, "ldap:") {
		return AuthLDAP
	}
	return AuthOIDC
}

// sourceFor picks the source for an identity, preferring what the caller stated.
//
// Derivation stays for LDAP, whose issuer is recognisable. Anything that knows better says so, because the alternative is what happened to
// SAML: recorded as OIDC, shown as OIDC on the users screen, and indistinguishable from OIDC to an administrator closing off somebody's access.
func sourceFor(identity ExternalIdentity) AuthSource {
	if identity.Source != "" {
		return identity.Source
	}
	return sourceForIssuer(identity.Issuer)
}

// ErrNoExternalMatch reports that no account matches an external identity.
var ErrNoExternalMatch = errors.New("store: no account matches this external identity")

// ErrLocalAccountExists reports that the chosen username is already a local account.
var ErrLocalAccountExists = errors.New("store: a local account already has that username")

// SignInExternal finds or creates the account for an external identity and starts a session.
//
// Matching is on issuer and subject only. Never on email or username, and that is the security decision in this file: an
// identity provider that lets people choose their own email address would otherwise be a way to take over a local
// administrator's account by matching its name. The cost is that somebody who has both a local account and a federated one
// gets two accounts, which is visible and fixable; the alternative fails silently and hands over administrator.
func (s *Store) SignInExternal(ctx context.Context, id ExternalIdentity, allowCreate bool, ip, userAgent string) (
	token string, u *User, created bool, err error) {

	if strings.TrimSpace(id.Issuer) == "" || strings.TrimSpace(id.Subject) == "" {
		return "", nil, false, fmt.Errorf("%w: an external identity needs both an issuer and a subject", ErrInvalid)
	}

	u, err = s.userByExternal(ctx, id.Issuer, id.Subject)
	switch {
	case err == nil:
		// Found. The role is refreshed from the provider's claims on every sign-in, because the directory is the authority
		// on group membership and a role that only updated at creation would keep somebody's access after they changed
		// teams.
		if id.Role != "" && u.Role != id.Role {
			if _, err := s.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, string(id.Role), u.ID); err != nil {
				return "", nil, false, err
			}
			u.Role = id.Role
		}
		if err := s.refreshExternalProfile(ctx, u.ID, id); err != nil {
			return "", nil, false, err
		}

	case errors.Is(err, ErrNoExternalMatch):
		if !allowCreate {
			return "", nil, false, ErrNoExternalMatch
		}
		u, err = s.createExternalUser(ctx, id)
		if err != nil {
			return "", nil, false, err
		}
		created = true

	default:
		return "", nil, false, err
	}

	if u.Disabled {
		return "", nil, false, ErrDisabled
	}

	token, err = s.startSession(ctx, u.ID, ip, userAgent)
	if err != nil {
		return "", nil, false, err
	}

	return token, u, created, nil
}

// userByExternal finds an account by its external identity.
func (s *Store) userByExternal(ctx context.Context, issuer, subject string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, role, disabled FROM users
		 WHERE external_issuer = ? AND external_subject = ?`,
		strings.TrimSuffix(issuer, "/"), subject)

	var u User
	var role string
	var disabled int
	if err := row.Scan(&u.ID, &u.Username, &role, &disabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoExternalMatch
		}
		return nil, err
	}

	u.Role = Role(role)
	u.Disabled = disabled != 0

	return &u, nil
}

// createExternalUser makes an account for a federated identity.
func (s *Store) createExternalUser(ctx context.Context, id ExternalIdentity) (*User, error) {
	username := strings.TrimSpace(id.Username)
	if username == "" {
		username = strings.TrimSpace(id.Email)
	}
	if username == "" {
		// The subject is unlovely as a display name but it is the only thing guaranteed present, and an account with no
		// name at all cannot appear in an audit log usefully.
		username = id.Subject
	}

	// A collision with a local account is refused rather than resolved. Silently appending a suffix would produce two
	// accounts with confusingly similar names and no indication why; taking the existing one over is the attack this file
	// exists to prevent.
	var localCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE username = ? AND (auth_source = 'local' OR auth_source IS NULL)`,
		username).Scan(&localCount); err != nil {
		return nil, err
	}
	if localCount > 0 {
		return nil, fmt.Errorf("%w: %q; rename one of them, because taking the existing account over is exactly what "+
			"this refuses to do", ErrLocalAccountExists, username)
	}

	role := id.Role
	if role == "" {
		// No role means the claims did not map to one. Refused rather than defaulted: a default of viewer is a silent
		// grant, and a default of nothing is an account that exists and cannot be used.
		return nil, errors.New("store: this person's groups did not map to any role, so there is no access to grant")
	}

	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (tenant_id, username, password_hash, role, disabled, created_at, auth_source,
		                    external_issuer, external_subject, email)
		 VALUES (?, ?, '', ?, 0, ?, ?, ?, ?, ?)`,
		string(DefaultTenant), username, string(role), formatTime(now), string(sourceFor(id)),
		strings.TrimSuffix(id.Issuer, "/"), id.Subject, id.Email)
	if err != nil {
		return nil, err
	}

	newID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	// An empty password hash, which no password can produce. That is what makes this account unusable through the local
	// sign-in form rather than merely unlikely to be guessed.
	return &User{ID: newID, Username: username, Role: role}, nil
}

// refreshExternalProfile updates the display fields from the provider.
func (s *Store) refreshExternalProfile(ctx context.Context, userID int64, id ExternalIdentity) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, id.Email, userID)
	return err
}

// startSession mints a session for a user who has already been authenticated.
//
// Extracted so a federated sign-in produces exactly the same kind of session as a local one. Two code paths creating sessions
// differently is how one of them ends up without an expiry.
func (s *Store) startSession(ctx context.Context, userID int64, ip, userAgent string) (string, error) {
	token, hash, err := newSessionToken()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, ip, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, userID, formatTime(now), formatTime(now.Add(s.SessionLifetime())), ip, userAgent); err != nil {
		return "", err
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login = ? WHERE id = ?`, formatTime(now), userID); err != nil {
		return "", err
	}

	return token, nil
}

// AuthSourceOf reports where an account's identity comes from.
//
// Used by the local sign-in path to refuse a federated account with a message that says where to sign in instead, rather than
// "incorrect username or password" for a password that does not exist.
func (s *Store) AuthSourceOf(ctx context.Context, tid tenant.ID, username string) (AuthSource, error) {
	var source string
	err := s.db.QueryRowContext(ctx,
		`SELECT auth_source FROM users WHERE username = ?`, username).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthUnknown, nil
	}
	if err != nil {
		return AuthUnknown, err
	}
	if source == "" {
		return AuthLocal, nil
	}
	return AuthSource(source), nil
}

// SourceForTesting exposes the source decision to tests in other packages.
//
// Exported for the api package's tests, which check that a SAML sign-in is not recorded as OIDC. The decision is worth testing from where the
// mistake was visible - the users screen - rather than only from inside this package.
func SourceForTesting(identity ExternalIdentity) AuthSource {
	return sourceFor(identity)
}
