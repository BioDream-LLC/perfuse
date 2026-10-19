package msgstore

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
	"github.com/biodream-llc/perfuse/internal/identity"
)

// A data subject's messages, for a request under data protection law: found through the identity index (so only when it is
// on), read whole for an access request, deleted whole for an erasure.

// SubjectMessage is one message about the subject.
type SubjectMessage struct {
	ID         int64     `json:"id"`
	Channel    string    `json:"channel"`
	ReceivedAt time.Time `json:"receivedAt"`
	ControlID  string    `json:"controlId,omitempty"`
	Type       string    `json:"type,omitempty"`
	// Matched is the identifier it was found by, as the index holds it.
	Matched string `json:"matched"`
}

// IndexesIdentity says whether messages can be found by identifier at all.
func (s *Store) IndexesIdentity() bool { return s.indexIdentity() }

// NormaliseIdentifiers reduces identifiers (system|value, or a bare value) the way the identity index stores them.
func NormaliseIdentifiers(idents []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range idents {
		v := i
		if _, val, ok := strings.Cut(i, "|"); ok {
			v = val
		}
		if n := identity.Normalise(v); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// SubjectMessages lists the messages whose extracted identifiers include any of norms (NormaliseIdentifiers), oldest first.
func (s *Store) SubjectMessages(ctx context.Context, tenantID string, norms []string, limit int) ([]SubjectMessage, error) {
	out := []SubjectMessage{}
	if len(norms) == 0 {
		return out, nil
	}
	if limit <= 0 {
		limit = 100000
	}
	args := []any{tenantOrDefault(tenantID)}
	for _, n := range norms {
		args = append(args, n)
	}
	args = append(args, limit)
	rows, err := s.querier().QueryContext(ctx, `SELECT m.id, m.channel, m.received_at, COALESCE(m.control_id,''),
		COALESCE(m.message_type,''), MIN(i.value)
		FROM message_identity i JOIN messages m ON m.id = i.message_id
		WHERE i.tenant_id = ? AND i.norm IN (`+placeholders(len(norms))+`)
		GROUP BY m.id, m.channel, m.received_at, m.control_id, m.message_type ORDER BY m.received_at, m.id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m SubjectMessage
		var at string
		if err := rows.Scan(&m.ID, &m.Channel, &at, &m.ControlID, &m.Type, &m.Matched); err != nil {
			return nil, err
		}
		m.ReceivedAt, _ = dbtime.Parse(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// EraseMessages deletes messages whole: the record, its body, its deliveries, its identifiers and attachments no other
// message uses. It answers how many were deleted.
func (s *Store) EraseMessages(ctx context.Context, ids []int64) (int64, error) {
	var total int64
	for len(ids) > 0 {
		n := len(ids)
		if n > 500 {
			n = 500
		}
		batch := ids[:n]
		ids = ids[n:]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		in := placeholders(len(batch))
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return total, err
		}
		for _, q := range []string{
			`DELETE FROM message_deliveries WHERE message_id IN (` + in + `)`,
			`DELETE FROM message_identity WHERE message_id IN (` + in + `)`,
			`DELETE FROM message_attachments WHERE message_id IN (` + in + `)`,
		} {
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				_ = tx.Rollback()
				return total, err
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id IN (`+in+`)`, args...)
		if err != nil {
			_ = tx.Rollback()
			return total, err
		}
		if err := tx.Commit(); err != nil {
			return total, err
		}
		n64, _ := res.RowsAffected()
		total += n64
	}
	_, err := s.SweepAttachments(ctx)
	return total, err
}

// SetHolds tells the pruner which identifiers are under a legal hold: messages carrying one are not deleted by the
// retention window while the hold lasts. The function is asked each time the pruner runs.
func (s *Store) SetHolds(held func(ctx context.Context) []string) {
	s.holdsMu.Lock()
	s.holds = held
	s.holdsMu.Unlock()
}

// heldClause is the part of a pruning statement that spares held messages, with its arguments.
func (s *Store) heldClause(ctx context.Context) (string, []any) {
	s.holdsMu.Lock()
	held := s.holds
	s.holdsMu.Unlock()
	if held == nil {
		return "", nil
	}
	norms := held(ctx)
	if len(norms) == 0 {
		return "", nil
	}
	args := make([]any, len(norms))
	for i, n := range norms {
		args[i] = n
	}
	return ` AND id NOT IN (SELECT message_id FROM message_identity WHERE norm IN (` + placeholders(len(norms)) + `))`, args
}

// Reader is the read pool, for a data protection search across stores.
func (s *Store) Reader() *sql.DB { return s.querier() }
