package mpi

import (
	"context"
	"database/sql"
	"strings"

	"github.com/biodream-llc/perfuse/internal/natid"
)

// A data subject's entries in the patient index, for a request under data protection law.

// SubjectPersons finds the people holding any of the identifiers (system|value), written as the index stores them, and the
// person named by an EUID when one of the identifiers is EUIDSystem|euid.
func (s *Store) SubjectPersons(ctx context.Context, tenant string, idents []string, set Settings) ([]*Person, error) {
	set = set.withDefaults()
	seen := map[string]bool{}
	var out []*Person
	add := func(euid string) error {
		if euid == "" || seen[euid] {
			return nil
		}
		seen[euid] = true
		p, err := s.Person(ctx, tenant, euid)
		if err != nil {
			if err == ErrNotFound {
				return nil
			}
			return err
		}
		out = append(out, p)
		return nil
	}
	for _, ident := range idents {
		sys, val, ok := strings.Cut(strings.TrimSpace(ident), "|")
		if !ok {
			// A number on its own, as a data protection request is often given one: the person whose record carries it as
			// its own number, in whatever system, when exactly one person does. Two people holding the same bare number (two
			// hospitals' MRN 1001) is not an answer, and erasing either would be wrong, so neither is found by it; the request
			// then needs system|value. It was skipped altogether, so an erasure by a bare record number left the index record.
			val = strings.TrimSpace(ident)
			if val == "" {
				continue
			}
			rows, qerr := s.db.QueryContext(ctx, `SELECT DISTINCT p.euid FROM mpi_records r JOIN mpi_persons p ON p.id = r.person_id
				WHERE r.tenant = ? AND r.value = ?`, tenant, val)
			if qerr != nil {
				return nil, qerr
			}
			var found []string
			for rows.Next() {
				var e string
				if rows.Scan(&e) == nil {
					found = append(found, e)
				}
			}
			rows.Close()
			if len(found) == 1 {
				if err := add(found[0]); err != nil {
					return nil, err
				}
			}
			continue
		}
		if sys == "" || val == "" {
			continue
		}
		if sys == set.EUIDSystem {
			if err := add(val); err != nil {
				return nil, err
			}
			continue
		}
		c := natid.Canonicalise(sys, val, set.NationalSystems)
		key := c.System + "|" + c.Value
		var euid string
		err := s.db.QueryRowContext(ctx, `SELECT p.euid FROM mpi_identifiers i JOIN mpi_records r ON r.id = i.record_id
			JOIN mpi_persons p ON p.id = r.person_id WHERE i.tenant = ? AND i.ident = ? LIMIT 1`, tenant, key).Scan(&euid)
		if err == nil {
			if err := add(euid); err != nil {
				return nil, err
			}
			continue
		}
		// A record's own number, or a national number kept but not indexed (its check digit failed).
		rows, qerr := s.db.QueryContext(ctx, `SELECT p.euid FROM mpi_records r JOIN mpi_persons p ON p.id = r.person_id
			WHERE r.tenant = ? AND ((r.system = ? AND r.value = ?) OR r.identifiers LIKE ?)`, tenant, c.System, c.Value,
			`%"`+strings.NewReplacer("%", "", "_", "").Replace(key)+`"%`)
		if qerr != nil {
			return nil, qerr
		}
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err == nil {
				if err := add(e); err != nil {
					rows.Close()
					return nil, err
				}
			}
		}
		rows.Close()
	}
	return out, nil
}

// ErasePerson removes a person and every record linked to them: demographics, identifiers, reviews and the history of
// what happened to them. One event says an erasure happened, by whom, with no detail about the person.
func (s *Store) ErasePerson(ctx context.Context, tenant, actor, euid, why string) (records int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var pid int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM mpi_persons WHERE tenant = ? AND euid = ?`, tenant, euid).Scan(&pid); err != nil {
		return 0, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM mpi_records WHERE tenant = ? AND person_id = ?`, tenant, pid)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		for _, q := range []string{`DELETE FROM mpi_identifiers WHERE tenant = ? AND record_id = ?`,
			`DELETE FROM mpi_reviews WHERE tenant = ? AND record_id = ?`,
			`DELETE FROM mpi_events WHERE tenant = ? AND record_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, tenant, id); err != nil {
				return 0, err
			}
		}
	}
	for _, q := range []string{`DELETE FROM mpi_reviews WHERE tenant = ? AND person_id = ?`, `DELETE FROM mpi_records WHERE tenant = ? AND person_id = ?`} {
		if _, err := tx.ExecContext(ctx, q, tenant, pid); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mpi_events WHERE tenant = ? AND (euid = ? OR other_euid = ?)`, tenant, euid, euid); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mpi_persons WHERE id = ?`, pid); err != nil {
		return 0, err
	}
	if err := s.event(ctx, tx, tenant, actor, "erased", 0, "", "", strings.TrimSpace(why)); err != nil {
		return 0, err
	}
	return len(ids), tx.Commit()
}

// Reader is the index's database, for a data protection search across stores.
func (s *Store) Reader() *sql.DB { return s.db }
