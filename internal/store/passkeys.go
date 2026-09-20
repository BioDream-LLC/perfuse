package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/internal/tenant"
	"github.com/biodream-llc/perfuse/internal/webauthn"
)

// Passkey storage.
//
// Nothing here is secret. This table holds public keys, so it could be published without letting anybody sign in - which is
// the point of passkeys. A stolen password database is a breach; a stolen copy of this is a list of public keys.
//
// The consequence worth stating is that the interesting risk moves from confidentiality to integrity. Nobody gains anything by
// reading this table; somebody who could *write* to it could add their own passkey to an administrator's account. So the
// writing paths are narrow and audited, and there is deliberately no method that adds a credential without naming the account
// it belongs to.

// ErrChallengeNotFound means a challenge was not found, was already used, or has expired.
//
// One error for all three deliberately. Distinguishing them would tell somebody probing the endpoint whether a particular
// challenge value had ever existed, and there is nothing a legitimate caller does differently in each case.
var ErrChallengeNotFound = errors.New("store: the challenge is not valid")

// AddPasskey stores a credential for an account.
//
// The account is named rather than inferred. There is no variant that takes only a credential, because a credential added
// without an account would have to be attached to one later - and that later step is where somebody's passkey ends up on
// somebody else's account.
func (sc *Scoped) AddPasskey(ctx context.Context, userID int64, credential *webauthn.Credential) error {
	if credential == nil || len(credential.ID) == 0 {
		return fmt.Errorf("%w: a passkey needs a credential identifier", ErrInvalid)
	}
	if len(credential.PublicKey) == 0 {
		return fmt.Errorf("%w: a passkey needs a public key", ErrInvalid)
	}

	// The account must exist in this tenant. Checked rather than left to the foreign key, because a foreign key would
	// accept a user in another tenant - the constraint is on the users table, not on the tenant.
	if _, err := sc.GetUserByID(ctx, userID); err != nil {
		return err
	}

	verified := 0
	if credential.UserVerified {
		verified = 1
	}

	label := credential.Label
	if label == "" {
		label = "passkey"
	}

	_, err := sc.store.db.ExecContext(ctx,
		`INSERT INTO passkeys
			(credential_id, user_id, tenant_id, public_key, algorithm, sign_count, aaguid, user_verified,
			 label, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		encodeCredentialID(credential.ID), userID, string(sc.id), credential.PublicKey,
		int64(credential.Algorithm), credential.SignCount, credential.AAGUID, verified,
		label, formatTime(time.Now().UTC()))
	if err != nil {
		if isUniqueViolation(err) {
			// The same credential offered twice. A conflict rather than an overwrite: overwriting would let somebody
			// who knew an existing credential identifier replace the key behind it, which is precisely the integrity
			// attack this table has to resist.
			return ErrDuplicate
		}

		return err
	}

	return nil
}

// ListPasskeys returns an account's passkeys, oldest first.
func (sc *Scoped) ListPasskeys(ctx context.Context, userID int64) ([]webauthn.Credential, error) {
	rows, err := sc.store.db.QueryContext(ctx,
		`SELECT credential_id, public_key, algorithm, sign_count, aaguid, user_verified, label,
		        created_at, last_used
		 FROM passkeys WHERE user_id = ? AND tenant_id = ? ORDER BY created_at`,
		userID, string(sc.id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []webauthn.Credential
	for rows.Next() {
		credential, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, credential)
	}

	return out, rows.Err()
}

// PasskeyByCredentialID finds a credential and the account it belongs to.
//
// Deliberately unscoped by tenant, and this is the one place that is correct. A sign-in arrives carrying nothing but a
// credential identifier: there is no tenant to scope by until the credential has been found, because finding it is what
// identifies the account and therefore the tenant.
//
// Named to say so. The tenant is returned alongside, so the caller scopes everything after this point.
func (s *Store) PasskeyByCredentialID(
	ctx context.Context, credentialID []byte,
) (credential webauthn.Credential, userID int64, tid tenant.ID, err error) {
	if len(credentialID) == 0 {
		// Refused rather than matching whatever an empty identifier happens to match. An empty value reaching here
		// means a caller failed to decode one, and the permissive reading returns an arbitrary credential.
		return webauthn.Credential{}, 0, "", ErrNotFound
	}

	var tenantID string
	row := s.db.QueryRowContext(ctx,
		`SELECT credential_id, public_key, algorithm, sign_count, aaguid, user_verified, label,
		        created_at, last_used, user_id, tenant_id
		 FROM passkeys WHERE credential_id = ?`, encodeCredentialID(credentialID))

	var (
		id       string
		key      []byte
		alg      int64
		count    int64
		aaguid   []byte
		verified int
		label    string
		created  string
		lastUsed sql.NullString
	)
	if err := row.Scan(&id, &key, &alg, &count, &aaguid, &verified, &label, &created, &lastUsed,
		&userID, &tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return webauthn.Credential{}, 0, "", ErrNotFound
		}

		return webauthn.Credential{}, 0, "", err
	}

	credential = webauthn.Credential{
		ID:           credentialID,
		PublicKey:    key,
		Algorithm:    webauthn.Algorithm(alg),
		SignCount:    uint32(count),
		AAGUID:       aaguid,
		UserVerified: verified != 0,
		Label:        label,
		CreatedAt:    parseTime(created),
	}
	if lastUsed.Valid {
		t := parseTime(lastUsed.String)
		credential.LastUsed = &t
	}

	return credential, userID, tenant.ID(tenantID), nil
}

// scanPasskey reads one row.
func scanPasskey(rows *sql.Rows) (webauthn.Credential, error) {
	var (
		id       string
		key      []byte
		alg      int64
		count    int64
		aaguid   []byte
		verified int
		label    string
		created  string
		lastUsed sql.NullString
	)
	if err := rows.Scan(&id, &key, &alg, &count, &aaguid, &verified, &label, &created, &lastUsed); err != nil {
		return webauthn.Credential{}, err
	}

	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("store: a stored credential identifier is not base64url: %w", err)
	}

	credential := webauthn.Credential{
		ID:           decoded,
		PublicKey:    key,
		Algorithm:    webauthn.Algorithm(alg),
		SignCount:    uint32(count),
		AAGUID:       aaguid,
		UserVerified: verified != 0,
		Label:        label,
		CreatedAt:    parseTime(created),
	}
	if lastUsed.Valid {
		t := parseTime(lastUsed.String)
		credential.LastUsed = &t
	}

	return credential, nil
}

// RecordPasskeyUse updates the sign counter and the last-used time after a successful sign-in.
//
// The counter is written unconditionally rather than only when it advances. An authenticator that resets its counter - which
// happens after a firmware update on some models - would otherwise have every subsequent sign-in reported as a possible clone
// for ever, and a warning that is always on is a warning nobody reads.
func (s *Store) RecordPasskeyUse(ctx context.Context, credentialID []byte, signCount uint32) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE passkeys SET sign_count = ?, last_used = ? WHERE credential_id = ?`,
		signCount, formatTime(time.Now().UTC()), encodeCredentialID(credentialID))

	return err
}

// DeletePasskey removes one of an account's passkeys.
//
// Scoped by both account and tenant, so a caller cannot delete somebody else's passkey by knowing its identifier. Removing an
// administrator's only passkey is how somebody would lock them out or force them back onto a password, so the narrower query
// is the right one even though it costs an extra clause.
func (sc *Scoped) DeletePasskey(ctx context.Context, userID int64, credentialID []byte) error {
	res, err := sc.store.db.ExecContext(ctx,
		`DELETE FROM passkeys WHERE credential_id = ? AND user_id = ? AND tenant_id = ?`,
		encodeCredentialID(credentialID), userID, string(sc.id))
	if err != nil {
		return err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}

	return nil
}

// CountPasskeys reports how many an account has.
//
// Exists so an interface can warn before removing the last one, and so a sign-in path can tell "this account has no passkeys"
// from "this passkey is wrong" without leaking which to the caller.
func (sc *Scoped) CountPasskeys(ctx context.Context, userID int64) (int, error) {
	var n int
	err := sc.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM passkeys WHERE user_id = ? AND tenant_id = ?`,
		userID, string(sc.id)).Scan(&n)

	return n, err
}

// StoreChallenge records a challenge so the response can be checked against it.
//
// userID is zero for a sign-in, where the account is not known until the credential comes back.
func (sc *Scoped) StoreChallenge(ctx context.Context, challenge []byte, userID int64, purpose string) error {
	var user any
	if userID > 0 {
		user = userID
	}

	_, err := sc.store.db.ExecContext(ctx,
		`INSERT INTO webauthn_challenges (challenge, user_id, tenant_id, purpose, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		encodeCredentialID(challenge), user, string(sc.id), purpose,
		formatTime(time.Now().UTC().Add(webauthn.ChallengeLifetime)))

	return err
}

// ConsumeChallenge takes a challenge and removes it in one step.
//
// The delete is what makes a challenge single-use, and it happens before the caller verifies anything. Two requests presenting
// the same challenge cannot both succeed, because the second finds nothing - and that ordering matters more than it looks: a
// verify-then-delete would let two concurrent requests both pass verification before either deleted.
//
// An expired challenge is treated as absent. Returning a distinct error would tell somebody probing the endpoint that a
// particular value had once existed.
func (s *Store) ConsumeChallenge(ctx context.Context, challenge []byte, purpose string) (userID int64, err error) {
	encoded := encodeCredentialID(challenge)

	// DELETE ... RETURNING, so finding and removing are one statement and there is no window between them.
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM webauthn_challenges
		 WHERE challenge = ? AND purpose = ? AND expires_at > ?
		 RETURNING COALESCE(user_id, 0)`,
		encoded, purpose, formatTime(time.Now().UTC()))

	if err := row.Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Not found, already used, or expired - one answer for all three.
			//
			// An expired row is also cleaned up here rather than left, because a table of dead challenges is a table
			// that grows for ever on a busy installation.
			_, _ = s.db.ExecContext(ctx,
				`DELETE FROM webauthn_challenges WHERE challenge = ?`, encoded)

			return 0, ErrChallengeNotFound
		}

		return 0, err
	}

	return userID, nil
}

// PurgeExpiredChallenges removes challenges nobody used, and reports how many went.
//
// Called on the same schedule as expired sessions, from startSessionSweeper. That sentence was in this comment before
// anything called it - the function was written, documented as scheduled, and then never wired up, so the table grew by
// one row per abandoned sign-in from the day passkeys shipped. A comment describing an intention reads exactly like a
// comment describing behaviour.
//
// Reports a count for the same reason the session purge does: a sweeper that silently does nothing and a sweeper that is
// not running look identical in a log.
func (s *Store) PurgeExpiredChallenges(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM webauthn_challenges WHERE expires_at < ?`, formatTime(time.Now().UTC()))
	if err != nil {
		return 0, err
	}

	n, _ := res.RowsAffected()

	return n, nil
}

// encodeCredentialID renders raw bytes for storage.
//
// base64url rather than a BLOB primary key, because a text primary key compares and indexes predictably in SQLite and the
// values appear in logs and interfaces where the encoded form is what somebody can copy.
func encodeCredentialID(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}

// StartSessionFor mints a session for an account that has already been authenticated by other means.
//
// Used by the passkey path, which verifies a signature rather than a password. Exposes the same helper the federated sign-in
// uses rather than duplicating it: two code paths creating sessions differently is how one of them ends up without an expiry,
// or without updating the last-login time that tells an operator an account is in use.
//
// Named to be explicit that no credential is checked here. The caller has already done that, and a reader should not have to
// guess whether this function authenticates anything.
func (sc *Scoped) StartSessionFor(ctx context.Context, user *User, ip, userAgent string) (string, error) {
	if user == nil {
		return "", errors.New("store: no account to start a session for")
	}
	if user.Disabled {
		// Refused here as well as at the caller. A disabled account must not get a session by any route, and a check in
		// one place is a check somebody can forget in another.
		return "", ErrDisabled
	}

	// Confirms the account belongs to this tenant before minting anything, so a caller that resolved the wrong tenant
	// cannot issue a session into it.
	if _, err := sc.GetUserByID(ctx, user.ID); err != nil {
		return "", err
	}

	return sc.store.startSession(ctx, user.ID, ip, userAgent)
}
