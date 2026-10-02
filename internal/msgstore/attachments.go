package msgstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/biodream-llc/perfuse/internal/attach"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Attachments are stored once per distinct payload and referenced from messages by digest.
//
// The table is deliberately separate from messages rather than a column on it. A payload is shared - the same
// document resent, or one message fanned out to five destinations - and a column would store it once per message,
// which is the problem this exists to solve.

// PutAttachment stores a payload, or does nothing if that exact payload is already held.
//
// Deduplication is the point, so an insert that collides on the digest is success rather than an error. Two callers
// storing the same document concurrently is normal in a fan-out, and treating the loser as a failure would fail a
// delivery for having nothing to do.
func (s *Store) PutAttachment(ctx context.Context, a attach.Attachment) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO attachments (digest, size, payload, first_seen)
		 VALUES (?, ?, ?, ?)`,
		string(a.Digest), a.Size, a.Payload, dbtime.Format(time.Now()))
	return err
}

// Attachment fetches a payload by digest.
func (s *Store) Attachment(ctx context.Context, digest attach.Digest) ([]byte, bool, error) {
	var payload []byte
	err := s.querier().QueryRowContext(ctx,
		`SELECT payload FROM attachments WHERE digest = ?`, string(digest)).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return payload, true, nil
}

// AttachmentLookup returns a lookup bound to this store, for reassembly.
//
// Errors are folded into "not found", because the caller's only correct response to either is to refuse the delivery,
// and attach.Reassemble already produces an error that says exactly that. A separate error path would mean two ways
// of describing the same outcome.
func (s *Store) AttachmentLookup(ctx context.Context) attach.Lookup {
	return func(d attach.Digest) ([]byte, bool) {
		payload, found, err := s.Attachment(ctx, d)
		if err != nil || !found {
			return nil, false
		}
		return payload, true
	}
}

// AttachmentInfo describes a stored payload without loading it.
type AttachmentInfo struct {
	Digest    attach.Digest `json:"digest"`
	Size      int           `json:"size"`
	FirstSeen time.Time     `json:"firstSeen"`

	// Messages is how many stored messages refer to this payload. The number that makes deduplication visible: a
	// document referenced forty times was stored once and would otherwise have been stored forty times.
	Messages int `json:"messages"`
}

// AttachmentStats summarises the attachment store.
type AttachmentStats struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`

	// References is the total number of message-to-payload references. Reported alongside Count because the gap
	// between them is the saving, and Count alone does not show it.
	References int `json:"references"`

	// Orphans are payloads no stored message refers to. Non-zero is not an error - a message can be pruned before
	// the next sweep - but a number that keeps growing means the sweep is not running.
	Orphans int `json:"orphans"`
}

// AttachmentStats reports what the attachment store holds.
func (s *Store) AttachmentStats(ctx context.Context) (AttachmentStats, error) {
	var out AttachmentStats

	err := s.querier().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM attachments`).Scan(&out.Count, &out.Bytes)
	if err != nil {
		return out, err
	}

	err = s.querier().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message_attachments`).Scan(&out.References)
	if err != nil {
		return out, err
	}

	err = s.querier().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM attachments
		 WHERE digest NOT IN (SELECT digest FROM message_attachments)`).Scan(&out.Orphans)
	if err != nil {
		return out, err
	}

	return out, nil
}

// LinkAttachments records which payloads a message refers to.
//
// Recorded explicitly rather than derived by scanning stored messages for tokens. A message's raw bytes are dropped
// at half the retention window, so a scan would stop finding references while the message still exists - and the
// sweep would then delete a payload that a queued delivery still needs.
func (s *Store) LinkAttachments(ctx context.Context, messageID int64, digests []attach.Digest) error {
	if len(digests) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, d := range digests {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO message_attachments (message_id, digest) VALUES (?, ?)`,
			messageID, string(d)); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SweepAttachments deletes payloads no stored message refers to.
//
// A sweep rather than reference counting. A count is one number that has to be correct across crashes, concurrent
// fan-out and partial transactions, and when it drifts the failure is either a leak nobody notices or a payload
// deleted while something still needs it. A sweep recomputes the truth every time and cannot drift.
//
// Called from Prune, so it runs on the same schedule as message retention.
func (s *Store) SweepAttachments(ctx context.Context) (int64, error) {
	// Links belonging to messages that no longer exist go first, or their digests would look referenced forever and
	// nothing would ever be swept.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM message_attachments
		 WHERE message_id NOT IN (SELECT id FROM messages)`); err != nil {
		return 0, err
	}

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM attachments
		 WHERE digest NOT IN (SELECT digest FROM message_attachments)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MessageAttachments lists what one message refers to.
func (s *Store) MessageAttachments(ctx context.Context, messageID int64) ([]AttachmentInfo, error) {
	rows, err := s.querier().QueryContext(ctx,
		`SELECT a.digest, a.size, a.first_seen,
		        (SELECT COUNT(*) FROM message_attachments m2 WHERE m2.digest = a.digest)
		 FROM attachments a
		 JOIN message_attachments ma ON ma.digest = a.digest
		 WHERE ma.message_id = ?
		 ORDER BY a.size DESC, a.digest`,
		messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []AttachmentInfo
	for rows.Next() {
		var (
			info      AttachmentInfo
			firstSeen string
		)
		if err := rows.Scan(&info.Digest, &info.Size, &firstSeen, &info.Messages); err != nil {
			return nil, err
		}
		info.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
		out = append(out, info)
	}
	return out, rows.Err()
}
