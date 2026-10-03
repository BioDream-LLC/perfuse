package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/biodream-llc/perfuse/internal/tenant"
)

// SMART Health Links hosted here.
//
// The server holds the encrypted file and nothing that opens it: the key is generated, put into the link, and forgotten. So the
// questions this table can answer are "who was it shared with and how often was it fetched", never "what was in it".

// SHLink is one hosted link, without its file.
type SHLink struct {
	ID            string     `json:"id"`
	Label         string     `json:"label"`
	ContentType   string     `json:"contentType"`
	HasPasscode   bool       `json:"hasPasscode"`
	AttemptsLeft  int        `json:"attemptsLeft"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	CreatedBy     string     `json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	Accesses      int        `json:"accesses"`
	LastRecipient string     `json:"lastRecipient,omitempty"`
}

// ErrSHLNotFound means no such link, or it is revoked, expired or out of attempts - which a manifest request is told alike, so that a
// guess at an id learns nothing about which ids exist.
var ErrSHLNotFound = errors.New("no such SMART Health Link")

// ErrSHLPasscode means the passcode was wrong; Remaining says how many tries are left.
type ErrSHLPasscode struct{ Remaining int }

func (e *ErrSHLPasscode) Error() string { return "the passcode is wrong" }

// SHLAttempts is how many wrong passcodes a link survives. The specification leaves it to the server; ten is enough for a patient
// mistyping and far too few for anybody guessing.
const SHLAttempts = 10

// CreateSHLink stores an encrypted file under a new link id.
func (s *Store) CreateSHLink(ctx context.Context, id, label, contentType, jwe, passcode string, expires *time.Time, by string) error {
	return s.createSHLinkIn(ctx, DefaultTenant, id, label, contentType, jwe, passcode, expires, by)
}

func (s *Store) createSHLinkIn(ctx context.Context, tid tenant.ID, id, label, contentType, jwe, passcode string, expires *time.Time,
	by string) error {
	hash := ""
	if passcode != "" {
		h, err := hashSecret(passcode)
		if err != nil {
			return err
		}
		hash = h
	}
	exp := ""
	if expires != nil {
		exp = expires.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO shl_links (id, tenant_id, label, content_type, jwe, passcode_hash, attempts_left,
		expires_at, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(tid), label, contentType, jwe, hash, SHLAttempts, exp, by, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// OpenSHLink is a manifest request: it checks the passcode and the link's validity, counts the access, and returns the file.
//
// Deliberately not tenant-scoped. The id is 256 random bits and is the only thing a patient's phone knows; the link was created inside a
// tenant and is answered wherever it is fetched.
func (s *Store) OpenSHLink(ctx context.Context, id, passcode, recipient string) (contentType, jwe string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback() }()

	var hash, exp string
	var attempts int
	var revoked sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT content_type, jwe, passcode_hash, attempts_left, expires_at, revoked_at FROM shl_links
		WHERE id = ?`, id).Scan(&contentType, &jwe, &hash, &attempts, &exp, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrSHLNotFound
	}
	if err != nil {
		return "", "", err
	}
	if revoked.Valid && revoked.String != "" {
		return "", "", ErrSHLNotFound
	}
	if exp != "" {
		if t, perr := time.Parse(time.RFC3339, exp); perr == nil && time.Now().After(t) {
			return "", "", ErrSHLNotFound
		}
	}
	if hash != "" {
		if attempts <= 0 {
			return "", "", ErrSHLNotFound
		}
		if ok, _ := VerifyPassword(hash, passcode); !ok {
			attempts--
			if _, err := tx.ExecContext(ctx, `UPDATE shl_links SET attempts_left = ? WHERE id = ?`, attempts, id); err != nil {
				return "", "", err
			}
			if err := tx.Commit(); err != nil {
				return "", "", err
			}
			return "", "", &ErrSHLPasscode{Remaining: attempts}
		}
	}
	if len(recipient) > 200 {
		recipient = recipient[:200]
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shl_links SET accesses = accesses + 1, last_recipient = ? WHERE id = ?`, recipient, id); err != nil {
		return "", "", err
	}
	return contentType, jwe, tx.Commit()
}

// ListSHLinks returns this tenant's links, newest first, without their files.
func (s *Store) ListSHLinks(ctx context.Context) ([]SHLink, error) {
	return s.listSHLinksIn(ctx, DefaultTenant)
}

func (s *Store) listSHLinksIn(ctx context.Context, tid tenant.ID) ([]SHLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, content_type, passcode_hash, attempts_left, expires_at, created_by, created_at,
		revoked_at, accesses, last_recipient FROM shl_links WHERE tenant_id = ? ORDER BY created_at DESC`, string(tid))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SHLink{}
	for rows.Next() {
		var l SHLink
		var hash, exp, created string
		var revoked sql.NullString
		if err := rows.Scan(&l.ID, &l.Label, &l.ContentType, &hash, &l.AttemptsLeft, &exp, &l.CreatedBy, &created, &revoked,
			&l.Accesses, &l.LastRecipient); err != nil {
			return nil, err
		}
		l.HasPasscode = hash != ""
		l.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if t, err := time.Parse(time.RFC3339, exp); err == nil {
			l.ExpiresAt = &t
		}
		if revoked.Valid && revoked.String != "" {
			if t, err := time.Parse(time.RFC3339Nano, revoked.String); err == nil {
				l.RevokedAt = &t
			}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeSHLink stops a link answering. The file is deleted with it: nothing can be done with it afterwards, and keeping ciphertext
// around for no reason is keeping patient data around for no reason.
func (s *Store) RevokeSHLink(ctx context.Context, id string) error {
	return s.revokeSHLinkIn(ctx, DefaultTenant, id)
}

func (s *Store) revokeSHLinkIn(ctx context.Context, tid tenant.ID, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE shl_links SET revoked_at = ?, jwe = '' WHERE id = ? AND tenant_id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), id, string(tid))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSHLNotFound
	}
	return nil
}
