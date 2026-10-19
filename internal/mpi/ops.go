package mpi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/natid"
)

// Match is one person found for a query, with how sure the index is.
type Match struct {
	Person *Person    `json:"person"`
	Score  float64    `json:"score"`
	Grade  string     `json:"grade"` // certain, probable, possible (FHIR's match-grade)
	Why    Comparison `json:"why"`
}

// Find scores the people who could be the person described, best first: what FHIR's Patient/$match and a demographic
// search answer. An identifier in the query that a person holds makes that person certain.
func (s *Store) Find(ctx context.Context, tenant string, q Record, set Settings, limit int) ([]Match, error) {
	set = set.withDefaults()
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	if d := normDate(q.BirthDate); d != "" {
		q.BirthDate = d
	}
	// The query's identifiers are written as stored ones are, so an NHS number typed with spaces, or sent under its OID, is
	// found. A national number that fails its check finds nobody: it names someone, but probably not this patient.
	q.Identifiers = append([]string(nil), q.Identifiers...)
	bad, _ := set.canonical(&q)
	byPerson := map[int64]*Match{}
	var order []int64
	for _, ident := range append(q.Identifiers, q.Key()) {
		if bad[ident] || !strings.Contains(ident, "|") || strings.HasPrefix(ident, "|") || strings.HasSuffix(ident, "|") {
			continue
		}
		var pid int64
		err := s.db.QueryRowContext(ctx, `SELECT r.person_id FROM mpi_identifiers i JOIN mpi_records r ON r.id = i.record_id
			WHERE i.tenant = ? AND i.ident = ? LIMIT 1`, tenant, ident).Scan(&pid)
		if err == nil {
			if _, ok := byPerson[pid]; !ok {
				byPerson[pid] = &Match{Score: 1, Grade: "certain", Why: Comparison{Score: 1, Fields: []FieldResult{{Field: "identifier", Result: "agree"}}}}
				order = append(order, pid)
			}
		}
	}
	cands, persons, err := s.candidates(ctx, s.db, tenant, blockKeys(&q))
	if err != nil {
		return nil, err
	}
	for i, c := range cands {
		if m, ok := byPerson[persons[i]]; ok && m.Grade == "certain" && m.Score == 1 && len(m.Why.Fields) == 1 {
			continue
		}
		cmp := Compare(&q, c, set.weights())
		if cmp.Score < set.Review*0.85 {
			continue
		}
		if m, ok := byPerson[persons[i]]; !ok {
			byPerson[persons[i]] = &Match{Score: cmp.Score, Why: cmp}
			order = append(order, persons[i])
		} else if cmp.Score > m.Score {
			m.Score, m.Why = cmp.Score, cmp
		}
	}
	var out []Match
	for _, pid := range order {
		m := byPerson[pid]
		euid := s.euidOf(ctx, s.db, pid)
		p, err := s.Person(ctx, tenant, euid)
		if err != nil {
			continue
		}
		m.Person = p
		if m.Grade == "" {
			switch {
			case m.Score >= set.AutoLink && m.Why.Safe:
				m.Grade = "certain"
			case m.Score >= set.Review:
				m.Grade = "probable"
			default:
				m.Grade = "possible"
			}
		}
		out = append(out, *m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Review is a possible match waiting for a person.
type Review struct {
	ID        int64      `json:"id"`
	Record    *Record    `json:"record"`
	Candidate *Person    `json:"candidate"`
	Score     float64    `json:"score"`
	Why       Comparison `json:"why"`
	Status    string     `json:"status"` // open, same, different
	Created   time.Time  `json:"created"`
	DecidedBy string     `json:"decidedBy,omitempty"`
}

// Reviews lists the open reviews, highest score first.
func (s *Store) Reviews(ctx context.Context, tenant string, limit int) ([]Review, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, record_id, person_id, score, detail, status, created_at, decided_by FROM mpi_reviews
		WHERE tenant = ? AND status = 'open' ORDER BY score DESC, id LIMIT ?`, tenant, limit)
	if err != nil {
		return nil, err
	}
	type row struct {
		r              Review
		recID, persID  int64
		detail, create string
	}
	var list []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.r.ID, &x.recID, &x.persID, &x.r.Score, &x.detail, &x.r.Status, &x.create, &x.r.DecidedBy); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	rows.Close()
	out := []Review{}
	for _, x := range list {
		_ = json.Unmarshal([]byte(x.detail), &x.r.Why)
		x.r.Created, _ = time.Parse(time.RFC3339Nano, x.create)
		x.r.Record, _ = s.Record(ctx, tenant, x.recID)
		if p, err := s.Person(ctx, tenant, s.euidOf(ctx, s.db, x.persID)); err == nil {
			x.r.Candidate = p
		}
		if x.r.Record == nil || x.r.Candidate == nil {
			continue
		}
		out = append(out, x.r)
	}
	return out, nil
}

// Decide closes a review: same links the record to the candidate person; different records that the two are not the
// same, so the pair is not offered again.
func (s *Store) Decide(ctx context.Context, tenant, actor string, reviewID int64, same bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var recID, persID int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT record_id, person_id, status FROM mpi_reviews WHERE tenant = ? AND id = ?`, tenant, reviewID).
		Scan(&recID, &persID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "open" {
		return fmt.Errorf("mpi: review %d was already decided (%s)", reviewID, status)
	}
	decision := "different"
	if same {
		decision = "same"
		if err := s.move(ctx, tx, tenant, actor, recID, persID, fmt.Sprintf("review %d: the same person", reviewID)); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mpi_not_same (tenant, record_id, person_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			tenant, recID, persID); err != nil {
			return err
		}
		var euid string
		_ = tx.QueryRowContext(ctx, `SELECT p.euid FROM mpi_records r JOIN mpi_persons p ON p.id = r.person_id WHERE r.id = ?`, recID).Scan(&euid)
		if err := s.event(ctx, tx, tenant, actor, "not-same", recID, euid, s.euidOf(ctx, tx, persID),
			fmt.Sprintf("review %d: not the same person", reviewID)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mpi_reviews SET status = ?, decided_by = ?, decided_at = ? WHERE id = ?`,
		decision, actor, s.stamp(), reviewID); err != nil {
		return err
	}
	return tx.Commit()
}

// move links a record to a person, removing the person it leaves if that one has no records left.
func (s *Store) move(ctx context.Context, tx *sql.Tx, tenant, actor string, recID, toPerson int64, why string) error {
	var from int64
	if err := tx.QueryRowContext(ctx, `SELECT person_id FROM mpi_records WHERE tenant = ? AND id = ?`, tenant, recID).Scan(&from); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if from == toPerson {
		return nil
	}
	fromEUID, toEUID := s.euidOf(ctx, tx, from), s.euidOf(ctx, tx, toPerson)
	if _, err := tx.ExecContext(ctx, `UPDATE mpi_records SET person_id = ? WHERE id = ?`, toPerson, recID); err != nil {
		return err
	}
	var left int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mpi_records WHERE person_id = ?`, from).Scan(&left)
	if left == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM mpi_persons WHERE id = ?`, from); err != nil {
			return err
		}
		// Reviews that offered the emptied person are moot.
		if _, err := tx.ExecContext(ctx, `UPDATE mpi_reviews SET status = 'moot' WHERE person_id = ? AND status = 'open'`, from); err != nil {
			return err
		}
	}
	// Any open review of this record is settled by the move.
	if _, err := tx.ExecContext(ctx, `UPDATE mpi_reviews SET status = 'moot' WHERE record_id = ? AND status = 'open'`, recID); err != nil {
		return err
	}
	return s.event(ctx, tx, tenant, actor, "link", recID, toEUID, fromEUID, why)
}

// Link moves a record to a person by hand.
func (s *Store) Link(ctx context.Context, tenant, actor string, recID int64, euid, why string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pid int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM mpi_persons WHERE tenant = ? AND euid = ?`, tenant, euid).Scan(&pid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := s.move(ctx, tx, tenant, actor, recID, pid, orWhy(why, "linked by hand")); err != nil {
		return err
	}
	return tx.Commit()
}

// Unlink takes a record out of its person into a new one of its own, and remembers the two are not the same.
func (s *Store) Unlink(ctx context.Context, tenant, actor string, recID int64, why string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var from int64
	if err := tx.QueryRowContext(ctx, `SELECT person_id FROM mpi_records WHERE tenant = ? AND id = ?`, tenant, recID).Scan(&from); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	var n int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mpi_records WHERE person_id = ?`, from).Scan(&n)
	if n < 2 {
		return "", errors.New("mpi: the record is the person's only one, so there is nothing to unlink it from")
	}
	pid, euid, err := s.newPerson(ctx, tx, tenant)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mpi_not_same (tenant, record_id, person_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		tenant, recID, from); err != nil {
		return "", err
	}
	if err := s.move(ctx, tx, tenant, actor, recID, pid, orWhy(why, "unlinked by hand")); err != nil {
		return "", err
	}
	return euid, tx.Commit()
}

// Merge moves every record of one person into another, as an ADT merge (A40) or a reviewer would.
func (s *Store) Merge(ctx context.Context, tenant, actor, fromEUID, intoEUID, why string) error {
	if fromEUID == intoEUID {
		return errors.New("mpi: a person cannot be merged into itself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from, into int64
	for _, x := range []struct {
		euid string
		id   *int64
	}{{fromEUID, &from}, {intoEUID, &into}} {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM mpi_persons WHERE tenant = ? AND euid = ?`, tenant, x.euid).Scan(x.id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM mpi_records WHERE person_id = ?`, from)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.move(ctx, tx, tenant, actor, id, into, orWhy(why, "merged "+fromEUID+" into "+intoEUID)); err != nil {
			return err
		}
	}
	if err := s.event(ctx, tx, tenant, actor, "merge", 0, intoEUID, fromEUID, orWhy(why, "")); err != nil {
		return err
	}
	return tx.Commit()
}

func orWhy(why, d string) string {
	if strings.TrimSpace(why) == "" {
		return d
	}
	return why
}

// Event is one entry in the index's history.
type Event struct {
	At        time.Time `json:"at"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	RecordID  int64     `json:"recordId,omitempty"`
	EUID      string    `json:"euid,omitempty"`
	OtherEUID string    `json:"otherEuid,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// History lists what happened to a person, newest first: registrations, links, unlinks, merges and review decisions.
func (s *Store) History(ctx context.Context, tenant, euid string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at, actor, action, record_id, euid, other_euid, detail FROM mpi_events
		WHERE tenant = ? AND (euid = ? OR other_euid = ?) ORDER BY id DESC LIMIT ?`, tenant, euid, euid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&at, &e.Actor, &e.Action, &e.RecordID, &e.EUID, &e.OtherEUID, &e.Detail); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Counts are the index's size, for the console.
type Counts struct {
	Persons int `json:"persons"`
	Records int `json:"records"`
	Reviews int `json:"reviews"`
	// Linked is how many persons have more than one record: the duplicates the index has joined.
	Linked int `json:"linked"`
}

// Count returns the index's size.
func (s *Store) Count(ctx context.Context, tenant string) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM mpi_persons WHERE tenant = ?), (SELECT COUNT(*) FROM mpi_records WHERE tenant = ?),
		(SELECT COUNT(*) FROM mpi_reviews WHERE tenant = ? AND status = 'open'),
		(SELECT COUNT(*) FROM (SELECT person_id FROM mpi_records WHERE tenant = ? GROUP BY person_id HAVING COUNT(*) > 1))`,
		tenant, tenant, tenant, tenant).Scan(&c.Persons, &c.Records, &c.Reviews, &c.Linked)
	return c, err
}

// Recent lists the persons most recently changed, for the console's first view.
func (s *Store) Recent(ctx context.Context, tenant, q string, limit int) ([]*Person, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where, args := `r.tenant = ?`, []any{tenant}
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + strings.ToUpper(q) + "%"
		where += ` AND (UPPER(r.family) LIKE ? OR UPPER(r.given) LIKE ? OR r.value LIKE ? OR p.euid LIKE ? OR r.birth_date = ?)`
		args = append(args, like, like, "%"+q+"%", "%"+strings.ToUpper(q)+"%", q)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.euid FROM mpi_records r JOIN mpi_persons p ON p.id = r.person_id WHERE `+where+`
		GROUP BY p.euid ORDER BY MAX(r.updated_at) DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var euids []string
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		euids = append(euids, e)
	}
	rows.Close()
	out := []*Person{}
	for _, e := range euids {
		if p, err := s.Person(ctx, tenant, e); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// Settings reads the matching settings saved for a tenant, with defaults filled in.
func (s *Store) Settings(ctx context.Context, tenant string) (Settings, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM mpi_settings WHERE tenant = ?`, tenant).Scan(&raw)
	var set Settings
	if err == nil {
		_ = json.Unmarshal([]byte(raw), &set)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return set, err
	}
	if set.Trusted == nil {
		set.Trusted = []string{}
	}
	return set.withDefaults(), nil
}

// SaveSettings stores a tenant's matching settings.
func (s *Store) SaveSettings(ctx context.Context, tenant, actor string, set Settings) error {
	if set.AutoLink < 0 || set.AutoLink > 1 || set.Review < 0 || set.Review > 1 {
		return errors.New("mpi: thresholds are between 0 and 1")
	}
	if set.Review > 0 && set.AutoLink > 0 && set.Review > set.AutoLink {
		return errors.New("mpi: the review threshold cannot be above the automatic link threshold")
	}
	for field, w := range set.Weights {
		if _, known := DefaultWeights[field]; !known {
			return fmt.Errorf("mpi: %q is not a field the index compares", field)
		}
		if !w.valid() {
			return fmt.Errorf("mpi: the weights for %s need 0 < u < m < 1", field)
		}
	}
	switch set.ShowNumbersTo {
	case "", "viewer", "operator", "editor", "admin":
	default:
		return fmt.Errorf("mpi: who sees national numbers in full is viewer, operator, editor or admin, not %q", set.ShowNumbersTo)
	}
	for id, uri := range set.NationalSystems {
		if _, ok := natid.ByID(id); !ok {
			return fmt.Errorf("mpi: %q is not a national identifier scheme", id)
		}
		if uri = strings.TrimSpace(uri); !strings.Contains(uri, ":") {
			return fmt.Errorf("mpi: the system for %s should be a URL or URN, not %q", id, uri)
		}
		if other, taken := natid.ForSystem(uri); taken && other.ID != id {
			return fmt.Errorf("mpi: %s is already the system of %s", uri, other.Name)
		}
	}
	set = set.withDefaults()
	b, _ := json.Marshal(set)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO mpi_settings (tenant, value) VALUES (?, ?)
		ON CONFLICT (tenant) DO UPDATE SET value = excluded.value`, tenant, string(b)); err != nil {
		return err
	}
	return s.event(ctx, s.db, tenant, actor, "settings", 0, "", "", string(b))
}

// RegisterWithSettings registers a record using the tenant's saved settings.
func (s *Store) RegisterWithSettings(ctx context.Context, tenant, actor string, r Record) (*Outcome, error) {
	set, err := s.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return s.Register(ctx, tenant, actor, r, set)
}

// MergeIdentifiers merges the person holding one identifier into the person holding another, as an ADT A40 names them.
// An identifier the index does not hold is not an error: there is nothing to merge.
func (s *Store) MergeIdentifiers(ctx context.Context, tenant, actor, fromIdent, intoIdent, why string) (bool, error) {
	from, err := s.ByIdentifier(ctx, tenant, fromIdent)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	into, err := s.ByIdentifier(ctx, tenant, intoIdent)
	if err != nil {
		return false, err
	}
	if from.EUID == into.EUID {
		return false, nil
	}
	return true, s.Merge(ctx, tenant, actor, from.EUID, into.EUID, why)
}
