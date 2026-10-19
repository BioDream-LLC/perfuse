package fhirserver

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// A data subject's records in the FHIR store, for a request under data protection law: their Patient resources (found by
// identifier), every resource whose indexed references point at one of them, and their prior authorization requests.
// Erasure removes them for good, history included; an ordinary FHIR delete keeps the history, which is right for a
// clinical record and wrong for an erasure.

// ResourceRef names one stored resource.
type ResourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// SubjectResources finds the Patient resources carrying any of the identifiers (system|value, or a bare value) and every
// resource that refers to one of them through an indexed reference.
func (s *Store) SubjectResources(ctx context.Context, idents []string) (patients []string, refs []ResourceRef, err error) {
	seen := map[string]bool{}
	for _, ident := range idents {
		sys, val, ok := strings.Cut(ident, "|")
		if !ok {
			sys, val = "", ident
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		q := `SELECT DISTINCT resource_id FROM fhir_search WHERE resource_type = 'Patient' AND param = 'identifier' AND value = ?`
		args := []any{val}
		if sys != "" {
			q += ` AND system = ?`
			args = append(args, sys)
		}
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if !seen[id] {
				seen[id] = true
				patients = append(patients, id)
			}
		}
		rows.Close()
	}
	have := map[ResourceRef]bool{}
	for _, pid := range patients {
		r := ResourceRef{"Patient", pid}
		if !have[r] {
			have[r] = true
			refs = append(refs, r)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT resource_type, resource_id FROM fhir_search
			WHERE ref_type = 'Patient' AND value = ? ORDER BY resource_type, resource_id`, pid)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var r ResourceRef
			if err := rows.Scan(&r.Type, &r.ID); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if !have[r] {
				have[r] = true
				refs = append(refs, r)
			}
		}
		rows.Close()
	}
	return patients, refs, nil
}

// ResourceVersions gives every stored version of a resource, oldest first (a deletion has no content).
func (s *Store) ResourceVersions(ctx context.Context, ref ResourceRef) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT content FROM fhir_history WHERE resource_type = ? AND resource_id = ?
		ORDER BY version_id`, ref.Type, ref.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var c sql.NullString
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if c.Valid && c.String != "" {
			out = append(out, []byte(c.String))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		var c string
		err := s.db.QueryRowContext(ctx, `SELECT content FROM fhir_resources WHERE resource_type = ? AND resource_id = ? AND deleted = 0`,
			ref.Type, ref.ID).Scan(&c)
		if err == nil {
			out = append(out, []byte(c))
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}

// Expunge removes resources and every version of them, with their search entries.
func (s *Store) Expunge(ctx context.Context, refs []ResourceRef) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	for _, r := range refs {
		res, err := tx.ExecContext(ctx, `DELETE FROM fhir_resources WHERE resource_type = ? AND resource_id = ?`, r.Type, r.ID)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
		for _, q := range []string{`DELETE FROM fhir_history WHERE resource_type = ? AND resource_id = ?`,
			`DELETE FROM fhir_search WHERE resource_type = ? AND resource_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, r.Type, r.ID); err != nil {
				return 0, err
			}
		}
	}
	return n, tx.Commit()
}

// PASRequest is one stored prior authorization request about the subject.
type PASRequest struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
}

// SubjectPAS finds the prior authorization requests whose patient carried any of the identifiers. None when PAS has never
// run on this server (its tables do not exist).
func (s *Store) SubjectPAS(ctx context.Context, idents []string) ([]PASRequest, error) {
	out := []PASRequest{}
	seen := map[string]bool{}
	for _, ident := range idents {
		ident = strings.TrimSpace(ident)
		if ident == "" {
			continue
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, created FROM pas_requests WHERE member LIKE ? ORDER BY created`,
			"% "+strings.NewReplacer("%", "", "_", "").Replace(ident)+" %")
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such table") || strings.Contains(err.Error(), "does not exist") {
				return out, nil
			}
			return nil, err
		}
		for rows.Next() {
			var p PASRequest
			if err := rows.Scan(&p.ID, &p.Created); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[p.ID] {
				seen[p.ID] = true
				out = append(out, p)
			}
		}
		rows.Close()
	}
	return out, nil
}

// PASContent gives a stored request and its responses, as stored.
func (s *Store) PASContent(ctx context.Context, id string) (request []byte, responses [][]byte, err error) {
	var req string
	if err := s.db.QueryRowContext(ctx, `SELECT request FROM pas_requests WHERE id = ?`, id).Scan(&req); err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT response FROM pas_responses WHERE id = ? ORDER BY version`, id)
	if err != nil {
		return []byte(req), nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return []byte(req), nil, err
		}
		responses = append(responses, []byte(r))
	}
	return []byte(req), responses, rows.Err()
}

// ErasePAS removes prior authorization requests with their responses, attachments and tracking numbers.
func (s *Store) ErasePAS(ctx context.Context, ids []string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	for _, id := range ids {
		for _, q := range []string{`DELETE FROM pas_responses WHERE id = ?`, `DELETE FROM pas_attachments WHERE id = ?`,
			`DELETE FROM pas_tracking WHERE id = ?`, `DELETE FROM pas_um_traces WHERE id = ?`} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return 0, err
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM pas_requests WHERE id = ?`, id)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, tx.Commit()
}

// Reader is the store's database, for a data protection search across stores.
func (s *Store) Reader() *sql.DB { return s.db }
