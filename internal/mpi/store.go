// Package mpi is a master patient index: it takes patient records from every source system (each identified by its
// assigning authority and number), decides which belong to the same person, and gives each person an enterprise
// identifier (EUID). Certain matches link on their own; uncertain ones wait in a review queue for a person to decide; every
// link, unlink, merge and decision is recorded with who made it.
//
// Matching is deterministic first (an identifier from a system the site trusts to be unique per person, such as a national
// identifier or an enterprise number) and probabilistic otherwise: Fellegi-Sunter weights over name, birth date, sex,
// postal code, phone and address, with Jaro-Winkler for names. It decides nothing clinical: it says which records are the
// same person, with its reasons, for a person to confirm when it is not sure.
package mpi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/natid"
)

// Record is one source system's record of a patient.
type Record struct {
	ID int64 `json:"id"`
	// System is the assigning authority (a URI or OID) and Value the number it gave: what makes the record unique.
	System string `json:"system"`
	Value  string `json:"value"`

	Family    string `json:"family,omitempty"`
	Given     string `json:"given,omitempty"`
	BirthDate string `json:"birthDate,omitempty"` // YYYY-MM-DD, or digits
	Sex       string `json:"sex,omitempty"`       // M, F, O, U
	Phone     string `json:"phone,omitempty"`
	Line      string `json:"line,omitempty"`
	City      string `json:"city,omitempty"`
	Postal    string `json:"postal,omitempty"`
	// Identifiers are other identifiers the record carries (system|value): an enterprise number, a payer member number.
	Identifiers []string `json:"identifiers,omitempty"`
	// Source is where it came from, such as a channel name.
	Source string `json:"source,omitempty"`

	EUID      string    `json:"euid,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Key is system|value.
func (r *Record) Key() string { return r.System + "|" + r.Value }

// Settings are the matching thresholds and rules.
type Settings struct {
	// Automatic links a record to a person by score without asking anybody. Off by default: linking two people into one
	// (an overlay) puts one patient's results in another's chart, so until a site has watched the review queue and trusts
	// what it would have done, every scored match goes to a person. Trusted identifiers link either way.
	Automatic bool `json:"automatic"`
	// AutoLink is the score at or above which a pair links without review, when Automatic is on and name and birth date
	// also agree. Default 0.9. With Automatic off, matches above it are still queued, marked certain.
	AutoLink float64 `json:"autoLink"`
	// Review is the score at or above which a pair goes to the review queue. Default 0.6: low enough that twins and a sex
	// keyed wrongly reach a person, since only a person can tell those apart.
	Review float64 `json:"review"`
	// Trusted are identifier systems unique per person (a national identifier, an enterprise number): records sharing one
	// link at once. An identifier system shared by family members, such as a payer's subscriber number, must not be listed.
	Trusted []string `json:"trusted"`
	// EUIDSystem names the enterprise identifier in FHIR and PIX answers.
	EUIDSystem string `json:"euidSystem"`
	// Weights replaces DefaultWeights field by field, typically with values estimated from the site's own records
	// (EstimateWeights). Empty: the defaults.
	Weights map[string]Weights `json:"weights,omitempty"`
	// NationalSystems replaces a national identifier scheme's system URI (natid scheme ID to URI) where the country's guide
	// names it differently from Perfuse's default.
	NationalSystems map[string]string `json:"nationalSystems,omitempty"`
	// ShowNumbersTo is the lowest role that sees national numbers in full: viewer, operator, editor (the default) or admin.
	// Everybody below sees the last few characters only.
	ShowNumbersTo string `json:"showNumbersTo,omitempty"`
}

// weights is the full weight table: the site's where it has them, the defaults for the rest.
func (s Settings) weights() map[string]Weights {
	if len(s.Weights) == 0 {
		return DefaultWeights
	}
	out := make(map[string]Weights, len(DefaultWeights))
	for k, w := range DefaultWeights {
		out[k] = w
	}
	for k, w := range s.Weights {
		if _, known := DefaultWeights[k]; known && w.valid() {
			out[k] = w
		}
	}
	return out
}

func (s Settings) withDefaults() Settings {
	if s.AutoLink <= 0 {
		s.AutoLink = 0.9
	}
	if s.Review <= 0 {
		s.Review = 0.6
	}
	if s.EUIDSystem == "" {
		s.EUIDSystem = "urn:perfuse:mpi"
	}
	if s.ShowNumbersTo == "" {
		s.ShowNumbersTo = "editor"
	}
	return s
}

// canonical writes a record's identifiers the one way the index stores them: a national number under its scheme's system
// (an OID or an older URL rewritten), without spacing. A national number whose check digit fails is kept on the record
// for people to see, but never indexed, so it cannot link the record to the person whose number it happens to be.
func (s Settings) canonical(in *Record) (bad map[string]bool, problems []string) {
	c := natid.Canonicalise(in.System, in.Value, s.NationalSystems)
	in.System, in.Value = c.System, c.Value
	bad = map[string]bool{}
	if c.Problem != "" {
		problems = append(problems, c.Problem)
		bad[in.Key()] = true
	}
	for i, ident := range in.Identifiers {
		sys, val, ok := strings.Cut(ident, "|")
		if !ok {
			continue
		}
		c := natid.Canonicalise(sys, val, s.NationalSystems)
		in.Identifiers[i] = c.System + "|" + c.Value
		if c.Problem != "" {
			problems = append(problems, c.Problem)
			bad[in.Identifiers[i]] = true
		}
	}
	return bad, problems
}

// Store is the index, in a database of its own.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open prepares the tables.
func Open(ctx context.Context, db *sql.DB) (*Store, error) {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS mpi_persons (id INTEGER PRIMARY KEY, tenant TEXT NOT NULL, euid TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS mpi_records (id INTEGER PRIMARY KEY, tenant TEXT NOT NULL, system TEXT NOT NULL,
			value TEXT NOT NULL, family TEXT NOT NULL DEFAULT '', given TEXT NOT NULL DEFAULT '', birth_date TEXT NOT NULL DEFAULT '',
			sex TEXT NOT NULL DEFAULT '', phone TEXT NOT NULL DEFAULT '', line TEXT NOT NULL DEFAULT '', city TEXT NOT NULL DEFAULT '',
			postal TEXT NOT NULL DEFAULT '', identifiers TEXT NOT NULL DEFAULT '[]', source TEXT NOT NULL DEFAULT '',
			person_id INTEGER NOT NULL, family_sx TEXT NOT NULL DEFAULT '', dob TEXT NOT NULL DEFAULT '', phone_key TEXT NOT NULL DEFAULT '',
			name_key TEXT NOT NULL DEFAULT '', place_key TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL, UNIQUE (tenant, system, value))`,
		`CREATE INDEX IF NOT EXISTS mpi_records_person ON mpi_records (person_id)`,
		`CREATE INDEX IF NOT EXISTS mpi_records_block_name ON mpi_records (tenant, family_sx, dob)`,
		`CREATE INDEX IF NOT EXISTS mpi_records_block_dob ON mpi_records (tenant, dob)`,
		`CREATE INDEX IF NOT EXISTS mpi_records_block_phone ON mpi_records (tenant, phone_key)`,
		`CREATE INDEX IF NOT EXISTS mpi_records_block_names ON mpi_records (tenant, name_key)`,
		`CREATE INDEX IF NOT EXISTS mpi_records_block_place ON mpi_records (tenant, place_key)`,
		`CREATE TABLE IF NOT EXISTS mpi_identifiers (tenant TEXT NOT NULL, ident TEXT NOT NULL, record_id INTEGER NOT NULL,
			PRIMARY KEY (tenant, ident, record_id))`,
		`CREATE TABLE IF NOT EXISTS mpi_reviews (id INTEGER PRIMARY KEY, tenant TEXT NOT NULL, record_id INTEGER NOT NULL,
			person_id INTEGER NOT NULL, score REAL NOT NULL, detail TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'open',
			created_at TEXT NOT NULL, decided_by TEXT NOT NULL DEFAULT '', decided_at TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS mpi_reviews_open ON mpi_reviews (tenant, status)`,
		`CREATE TABLE IF NOT EXISTS mpi_not_same (tenant TEXT NOT NULL, record_id INTEGER NOT NULL, person_id INTEGER NOT NULL,
			PRIMARY KEY (tenant, record_id, person_id))`,
		`CREATE TABLE IF NOT EXISTS mpi_events (id INTEGER PRIMARY KEY, tenant TEXT NOT NULL, at TEXT NOT NULL, actor TEXT NOT NULL,
			action TEXT NOT NULL, record_id INTEGER NOT NULL DEFAULT 0, euid TEXT NOT NULL DEFAULT '', other_euid TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS mpi_events_euid ON mpi_events (tenant, euid)`,
		`CREATE TABLE IF NOT EXISTS mpi_settings (tenant TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("preparing the patient index: %w", err)
		}
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func newEUID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "E" + strings.ToUpper(hex.EncodeToString(b))
}

// blocks are the keys that bring records together for comparison. Each catches what the others miss: a typo in the year
// (both names), a missing birth date (both names, or postal code and given name), a changed surname (birth date, phone).
type blocks struct{ sx, dob, phone, names, place string }

func blockKeys(r *Record) blocks {
	b := blocks{sx: soundex(r.Family), dob: digits(r.BirthDate), phone: phoneKey(r.Phone)}
	fs, gs := soundex(r.Family), soundex(r.Given)
	if fs != "" && gs != "" {
		// In order, so a swapped family and given name block together.
		if gs < fs {
			fs, gs = gs, fs
		}
		b.names = fs + "|" + gs
	}
	if pk, g := postalKey(r.Postal), soundex(r.Given); pk != "" && g != "" {
		b.place = pk + "|" + g
	}
	return b
}

// normDate writes a year-first date (1980-04-03, 19800403, an HL7 timestamp) as YYYY-MM-DD, or "" for anything else. A
// date written day or month first is left as the source wrote it: 03/04/1980 is a different day in the US and in Europe,
// and guessing would link the wrong people.
func normDate(s string) string {
	t := strings.TrimSpace(s)
	d := digits(t)
	if len(d) < 8 || len(t) < 8 || t[0] < '0' || t[0] > '9' || len(digits(t[:4])) != 4 {
		return ""
	}
	y, m, day := d[:4], d[4:6], d[6:8]
	if y < "1800" || y > "2200" || m < "01" || m > "12" || day < "01" || day > "31" {
		return ""
	}
	return y + "-" + m + "-" + day
}

// Outcome is what registering a record did.
type Outcome struct {
	Record *Record `json:"record"`
	// Action is linked (to an existing person, How saying why), new (a new person), updated (a known record's
	// demographics), or review (a new person, and a possible match queued for a person to decide).
	Action string  `json:"action"`
	How    string  `json:"how,omitempty"` // identifier, score
	Score  float64 `json:"score,omitempty"`
	// ReviewID is the queued decision, when there is one.
	ReviewID int64 `json:"reviewId,omitempty"`
	// Warnings are national numbers whose check digit failed (masked): kept on the record, never used to link it.
	Warnings []string `json:"warnings,omitempty"`
}

type txer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) event(ctx context.Context, q txer, tenant, actor, action string, recordID int64, euid, other, detail string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO mpi_events (tenant, at, actor, action, record_id, euid, other_euid, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tenant, s.stamp(), actor, action, recordID, euid, other, detail)
	return err
}

func (s *Store) newPerson(ctx context.Context, q txer, tenant string) (int64, string, error) {
	euid := newEUID()
	res, err := q.ExecContext(ctx, `INSERT INTO mpi_persons (tenant, euid, created_at) VALUES (?, ?, ?)`, tenant, euid, s.stamp())
	if err != nil {
		return 0, "", err
	}
	id, err := res.LastInsertId()
	return id, euid, err
}

func (s *Store) euidOf(ctx context.Context, q txer, personID int64) string {
	var e string
	_ = q.QueryRowContext(ctx, `SELECT euid FROM mpi_persons WHERE id = ?`, personID).Scan(&e)
	return e
}

const recordCols = `r.id, r.system, r.value, r.family, r.given, r.birth_date, r.sex, r.phone, r.line, r.city, r.postal, r.identifiers,
	r.source, r.updated_at, r.person_id, p.euid`

func scanRecord(sc interface{ Scan(...any) error }) (*Record, int64, error) {
	var r Record
	var ids, updated string
	var person int64
	if err := sc.Scan(&r.ID, &r.System, &r.Value, &r.Family, &r.Given, &r.BirthDate, &r.Sex, &r.Phone, &r.Line, &r.City, &r.Postal,
		&ids, &r.Source, &updated, &person, &r.EUID); err != nil {
		return nil, 0, err
	}
	_ = json.Unmarshal([]byte(ids), &r.Identifiers)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &r, person, nil
}

func (s *Store) queryRecords(ctx context.Context, q txer, where string, args ...any) ([]*Record, []int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+recordCols+` FROM mpi_records r JOIN mpi_persons p ON p.id = r.person_id WHERE `+where, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var recs []*Record
	var persons []int64
	for rows.Next() {
		r, p, err := scanRecord(rows)
		if err != nil {
			return nil, nil, err
		}
		recs, persons = append(recs, r), append(persons, p)
	}
	return recs, persons, rows.Err()
}

// Register adds or updates a record and links it: to the person a trusted identifier names, to the best-scoring person
// when the match is certain, or to a new person, queueing a review when there is a possible match.
func (s *Store) Register(ctx context.Context, tenant, actor string, in Record, set Settings) (*Outcome, error) {
	set = set.withDefaults()
	in.System, in.Value = strings.TrimSpace(in.System), strings.TrimSpace(in.Value)
	if in.System == "" || in.Value == "" {
		return nil, errors.New("mpi: a record needs the identifier's system (its assigning authority) and value")
	}
	bad, problems := set.canonical(&in)
	if d := normDate(in.BirthDate); d != "" {
		in.BirthDate = d
	}
	in.Sex = strings.ToUpper(strings.TrimSpace(in.Sex))
	if len(in.Sex) > 1 {
		in.Sex = in.Sex[:1]
	}
	idents := uniqueIdents(in.Identifiers, in.Key())
	in.Identifiers = idents
	idJSON, _ := json.Marshal(idents)

	if idents == nil {
		idJSON = []byte("[]")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	bk := blockKeys(&in)
	existing, persons, err := s.queryRecords(ctx, tx, `r.tenant = ? AND r.system = ? AND r.value = ?`, tenant, in.System, in.Value)
	if err != nil {
		return nil, err
	}
	if len(existing) == 1 {
		// Known: its demographics are refreshed and its link kept. A changed name or address is the patient's news, not a
		// reason to move the record to somebody else.
		id := existing[0].ID
		if _, err := tx.ExecContext(ctx, `UPDATE mpi_records SET family = ?, given = ?, birth_date = ?, sex = ?, phone = ?, line = ?,
			city = ?, postal = ?, identifiers = ?, source = ?, family_sx = ?, dob = ?, phone_key = ?, name_key = ?, place_key = ?,
			updated_at = ? WHERE id = ?`,
			in.Family, in.Given, in.BirthDate, in.Sex, in.Phone, in.Line, in.City, in.Postal, string(idJSON), in.Source,
			bk.sx, bk.dob, bk.phone, bk.names, bk.place, s.stamp(), id); err != nil {
			return nil, err
		}
		if err := s.indexIdentifiers(ctx, tx, tenant, id, idents, bad); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		rec, _ := s.Record(ctx, tenant, id)
		_ = persons
		return &Outcome{Record: rec, Action: "updated", Warnings: problems}, nil
	}

	// A trusted identifier shared with a known record: the same person, no scoring needed.
	var personID int64
	how, score := "", 0.0
	for _, ident := range append([]string{in.Key()}, idents...) {
		sys, _, _ := strings.Cut(ident, "|")
		if bad[ident] || !contains(set.Trusted, sys) {
			continue
		}
		var pid int64
		err := tx.QueryRowContext(ctx, `SELECT r.person_id FROM mpi_identifiers i JOIN mpi_records r ON r.id = i.record_id
			WHERE i.tenant = ? AND i.ident = ? LIMIT 1`, tenant, ident).Scan(&pid)
		if err == nil {
			personID, how = pid, "identifier "+ident
			break
		}
	}

	type cand struct {
		person int64
		cmp    Comparison
	}
	var best, second cand
	if personID == 0 {
		cands, cpersons, err := s.candidates(ctx, tx, tenant, bk)
		if err != nil {
			return nil, err
		}
		byPerson := map[int64]cand{}
		for i, c := range cands {
			cmp := Compare(&in, c, set.weights())
			if prev, ok := byPerson[cpersons[i]]; !ok || cmp.Score > prev.cmp.Score {
				byPerson[cpersons[i]] = cand{cpersons[i], cmp}
			}
		}
		for _, c := range byPerson {
			if c.cmp.Score > best.cmp.Score {
				best, second = c, best
			} else if c.cmp.Score > second.cmp.Score {
				second = c
			}
		}
		// Certain only when one person stands out: two people scoring above the line is exactly the case for a reviewer.
		if set.Automatic && best.person != 0 && best.cmp.Score >= set.AutoLink && best.cmp.Safe && second.cmp.Score < set.AutoLink {
			personID, how, score = best.person, "score", best.cmp.Score
		}
	}

	out := &Outcome{Action: "linked", How: how, Score: score}
	if personID == 0 {
		var euid string
		if personID, euid, err = s.newPerson(ctx, tx, tenant); err != nil {
			return nil, err
		}
		out.Action = "new"
		_ = euid
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO mpi_records (tenant, system, value, family, given, birth_date, sex, phone, line, city,
		postal, identifiers, source, person_id, family_sx, dob, phone_key, name_key, place_key, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tenant, in.System, in.Value, in.Family, in.Given, in.BirthDate, in.Sex, in.Phone, in.Line, in.City, in.Postal, string(idJSON),
		in.Source, personID, bk.sx, bk.dob, bk.phone, bk.names, bk.place, s.stamp())
	if err != nil {
		return nil, err
	}
	recID, _ := res.LastInsertId()
	if err := s.indexIdentifiers(ctx, tx, tenant, recID, idents, bad); err != nil {
		return nil, err
	}
	euid := s.euidOf(ctx, tx, personID)
	detail := "new person"
	if out.Action == "linked" {
		detail = "linked by " + how
		if how == "score" {
			detail = fmt.Sprintf("linked by score %.3f", score)
		}
	}
	if err := s.event(ctx, tx, tenant, actor, "register", recID, euid, "", in.Key()+": "+detail); err != nil {
		return nil, err
	}
	if out.Action == "new" && best.person != 0 && best.cmp.Score >= set.Review {
		d, _ := json.Marshal(best.cmp)
		r, err := tx.ExecContext(ctx, `INSERT INTO mpi_reviews (tenant, record_id, person_id, score, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			tenant, recID, best.person, best.cmp.Score, string(d), s.stamp())
		if err != nil {
			return nil, err
		}
		out.ReviewID, _ = r.LastInsertId()
		out.Action, out.Score = "review", best.cmp.Score
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out.Record, _ = s.Record(ctx, tenant, recID)
	out.Warnings = problems
	return out, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), v) {
			return true
		}
	}
	return false
}

func uniqueIdents(list []string, self string) []string {
	seen := map[string]bool{self: true}
	var out []string
	for _, i := range list {
		i = strings.TrimSpace(i)
		if sys, val, ok := strings.Cut(i, "|"); !ok || sys == "" || val == "" || seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, i)
	}
	sort.Strings(out)
	return out
}

func (s *Store) indexIdentifiers(ctx context.Context, q txer, tenant string, recID int64, idents []string, bad map[string]bool) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM mpi_identifiers WHERE tenant = ? AND record_id = ?`, tenant, recID); err != nil {
		return err
	}
	var key string
	if err := q.QueryRowContext(ctx, `SELECT system || '|' || value FROM mpi_records WHERE id = ?`, recID).Scan(&key); err != nil {
		return err
	}
	for _, i := range append([]string{key}, idents...) {
		if bad[i] {
			continue
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO mpi_identifiers (tenant, ident, record_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, tenant, i, recID); err != nil {
			return err
		}
	}
	return nil
}

// candidates finds records that could be the same person: same sounding family name and birth year, same birth date, or
// the same phone number. A search that compared every record would not scale; these blocks are where nearly every true
// match is found.
func (s *Store) candidates(ctx context.Context, q txer, tenant string, b blocks) ([]*Record, []int64, error) {
	var conds []string
	var args []any
	args = append(args, tenant)
	if b.sx != "" && len(b.dob) >= 4 {
		conds = append(conds, `(r.family_sx = ? AND substr(r.dob, 1, 4) = ?)`)
		args = append(args, b.sx, b.dob[:4])
	}
	if len(b.dob) >= 8 {
		conds = append(conds, `r.dob = ?`)
		args = append(args, b.dob)
	}
	if b.phone != "" {
		conds = append(conds, `r.phone_key = ?`)
		args = append(args, b.phone)
	}
	if b.names != "" {
		conds = append(conds, `r.name_key = ?`)
		args = append(args, b.names)
	}
	if b.place != "" {
		conds = append(conds, `r.place_key = ?`)
		args = append(args, b.place)
	}
	if len(conds) == 0 {
		return nil, nil, nil
	}
	return s.queryRecords(ctx, q, `r.tenant = ? AND (`+strings.Join(conds, " OR ")+`) LIMIT 500`, args...)
}

// Record reads one record.
func (s *Store) Record(ctx context.Context, tenant string, id int64) (*Record, error) {
	recs, _, err := s.queryRecords(ctx, s.db, `r.tenant = ? AND r.id = ?`, tenant, id)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrNotFound
	}
	return recs[0], nil
}

// ErrNotFound is a record, person or review that is not there.
var ErrNotFound = errors.New("mpi: not found")

// Person is one enterprise identity and every record linked to it.
type Person struct {
	EUID    string    `json:"euid"`
	Records []*Record `json:"records"`
	Created time.Time `json:"created"`
}

// Person reads one person by EUID.
func (s *Store) Person(ctx context.Context, tenant, euid string) (*Person, error) {
	var id int64
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, created_at FROM mpi_persons WHERE tenant = ? AND euid = ?`, tenant, euid).Scan(&id, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	recs, _, err := s.queryRecords(ctx, s.db, `r.tenant = ? AND r.person_id = ? ORDER BY r.updated_at DESC`, tenant, id)
	if err != nil {
		return nil, err
	}
	p := &Person{EUID: euid, Records: recs}
	p.Created, _ = time.Parse(time.RFC3339Nano, created)
	if p.Records == nil {
		p.Records = []*Record{}
	}
	return p, nil
}

// ByIdentifier finds the person holding an identifier (system|value), on its record or as one of a record's identifiers.
func (s *Store) ByIdentifier(ctx context.Context, tenant, ident string) (*Person, error) {
	var euid string
	err := s.db.QueryRowContext(ctx, `SELECT p.euid FROM mpi_identifiers i JOIN mpi_records r ON r.id = i.record_id
		JOIN mpi_persons p ON p.id = r.person_id WHERE i.tenant = ? AND i.ident = ? LIMIT 1`, tenant, ident).Scan(&euid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.Person(ctx, tenant, euid)
}

// Identifiers lists every identifier a person has across its records, the EUID first.
func (p *Person) Identifiers(euidSystem string) []string {
	out := []string{euidSystem + "|" + p.EUID}
	seen := map[string]bool{out[0]: true}
	for _, r := range p.Records {
		for _, i := range append([]string{r.Key()}, r.Identifiers...) {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	return out
}

// Golden is the person's best-known demographics: the most recently updated value of each field.
func (p *Person) Golden() Record {
	var g Record
	for _, r := range p.Records { // newest first
		set := func(dst *string, v string) {
			if *dst == "" && strings.TrimSpace(v) != "" {
				*dst = v
			}
		}
		set(&g.Family, r.Family)
		set(&g.Given, r.Given)
		set(&g.BirthDate, r.BirthDate)
		set(&g.Sex, r.Sex)
		set(&g.Phone, r.Phone)
		set(&g.Line, r.Line)
		set(&g.City, r.City)
		set(&g.Postal, r.Postal)
	}
	g.EUID = p.EUID
	return g
}

// DefaultTenant is the tenant of a single-tenant server, as the account store names it.
const DefaultTenant = "main"

// TenantOf returns id, or DefaultTenant when it is empty: a single-tenant channel has no tenant set, and its session's is
// the default.
func TenantOf(id string) string {
	if strings.TrimSpace(id) == "" {
		return DefaultTenant
	}
	return id
}

// NumbersRole is the lowest role that sees national numbers in full.
func (s Settings) NumbersRole() string { return s.withDefaults().ShowNumbersTo }

// MaskedRecord is a copy of a record with its national numbers masked (all but the last few characters).
func (s Settings) MaskedRecord(r *Record) *Record {
	if r == nil {
		return nil
	}
	c := *r
	if sch := natid.Canonicalise(c.System, c.Value, s.NationalSystems).Scheme; sch != nil {
		c.Value = natid.Mask(c.Value)
	}
	c.Identifiers = s.MaskedIdentifiers(r.Identifiers)
	return &c
}

// MaskedIdentifiers masks the national numbers in a list of system|value identifiers.
func (s Settings) MaskedIdentifiers(list []string) []string {
	if list == nil {
		return nil
	}
	out := make([]string, len(list))
	for i, ident := range list {
		sys, val, ok := strings.Cut(ident, "|")
		if ok && natid.Canonicalise(sys, val, s.NationalSystems).Scheme != nil {
			val = natid.Mask(val)
		}
		out[i] = sys + "|" + val
	}
	return out
}

// MaskedPerson is a copy of a person with every record's national numbers masked.
func (s Settings) MaskedPerson(p *Person) *Person {
	if p == nil {
		return nil
	}
	c := *p
	c.Records = make([]*Record, len(p.Records))
	for i, r := range p.Records {
		c.Records[i] = s.MaskedRecord(r)
	}
	return &c
}

// KnowsSystem reports whether any record in the index has an identifier from the system: whether it is a patient
// identification domain the index manages, which an IHE query naming an unknown one must be told it is not.
func (s *Store) KnowsSystem(ctx context.Context, tenant, system string) (bool, error) {
	if system == "" {
		return false, nil
	}
	prefix := system + "|"
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM mpi_records WHERE tenant = ? AND system = ? LIMIT 1`, tenant, system).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM mpi_identifiers WHERE tenant = ? AND substr(ident, 1, ?) = ? LIMIT 1`,
			tenant, len(prefix), prefix).Scan(&one)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
