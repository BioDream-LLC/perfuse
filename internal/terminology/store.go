// Package terminology keeps published code systems (LOINC, SNOMED CT, RxNorm, ICD-10-CM, CPT and local ones) for looking codes
// up, and the values mapping tables did not have, for somebody to map.
//
// The code systems are imported from the files their publishers distribute. Several are licensed (LOINC needs a free account,
// SNOMED CT an affiliate licence, RxNorm a UMLS licence, CPT an AMA licence), so none ships with Perfuse: an administrator who
// holds the licence uploads the file, and Perfuse records who imported it and when.
package terminology

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Well-known system URLs.
const (
	SystemLOINC    = "http://loinc.org"
	SystemSNOMED   = "http://snomed.info/sct"
	SystemRxNorm   = "http://www.nlm.nih.gov/research/umls/rxnorm"
	SystemICD10CM  = "http://hl7.org/fhir/sid/icd-10-cm"
	SystemCPT      = "http://www.ama-assn.org/go/cpt"
	SystemHCPCS    = "https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"
	SystemICD10PCS = "http://www.cms.gov/Medicare/Coding/ICD10"
)

// Store keeps code systems and unmapped values in the server's database.
type Store struct {
	db *sql.DB

	mu       sync.Mutex
	pending  map[unmappedKey]*unmappedCount
	overflow int64
}

// Open prepares the tables.
func Open(ctx context.Context, db *sql.DB) (*Store, error) {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS term_systems (
			system TEXT NOT NULL, version TEXT NOT NULL, name TEXT NOT NULL, format TEXT NOT NULL,
			concepts INTEGER NOT NULL, imported_at TEXT NOT NULL, imported_by TEXT NOT NULL, licence TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (system, version))`,
		`CREATE TABLE IF NOT EXISTS term_concepts (
			system TEXT NOT NULL, version TEXT NOT NULL, code TEXT NOT NULL, display TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1, PRIMARY KEY (system, version, code))`,
		`CREATE INDEX IF NOT EXISTS term_concepts_display ON term_concepts (system, display COLLATE NOCASE)`,
		`CREATE TABLE IF NOT EXISTS unmapped_values (
			tenant TEXT NOT NULL, channel TEXT NOT NULL, table_name TEXT NOT NULL, value TEXT NOT NULL,
			count INTEGER NOT NULL, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL, dismissed INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (tenant, channel, table_name, value))`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("preparing the terminology tables: %w", err)
		}
	}
	return &Store{db: db, pending: map[unmappedKey]*unmappedCount{}}, nil
}

// System is one imported code system version.
type System struct {
	System     string    `json:"system"`
	Version    string    `json:"version"`
	Name       string    `json:"name"`
	Format     string    `json:"format"`
	Concepts   int       `json:"concepts"`
	ImportedAt time.Time `json:"importedAt"`
	ImportedBy string    `json:"importedBy"`
	Licence    string    `json:"licence,omitempty"`
}

// Concept is one code.
type Concept struct {
	System  string `json:"system"`
	Version string `json:"version"`
	Code    string `json:"code"`
	Display string `json:"display"`
	Active  bool   `json:"active"`
}

// Systems lists what has been imported, by system then version.
func (s *Store) Systems(ctx context.Context) ([]System, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT system, version, name, format, concepts, imported_at, imported_by, licence
		FROM term_systems ORDER BY name, system, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []System{}
	for rows.Next() {
		var sys System
		var at string
		if err := rows.Scan(&sys.System, &sys.Version, &sys.Name, &sys.Format, &sys.Concepts, &at, &sys.ImportedBy, &sys.Licence); err != nil {
			return nil, err
		}
		sys.ImportedAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, sys)
	}
	return out, rows.Err()
}

// HasSystem reports whether any version of a system is imported.
func (s *Store) HasSystem(ctx context.Context, system string) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM term_systems WHERE system = ?`, system).Scan(&n)
	return n > 0
}

// Delete removes one imported version and its concepts.
func (s *Store) Delete(ctx context.Context, system, version string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM term_systems WHERE system = ? AND version = ?`, system, version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM term_concepts WHERE system = ? AND version = ?`, system, version); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrNotFound is a system or code that is not there.
var ErrNotFound = errors.New("not found")

// NormaliseCode writes a code as the system writes it: ICD-10-CM with its dot (the CMS files leave it out).
func NormaliseCode(system, code string) string {
	code = strings.TrimSpace(code)
	if system == SystemICD10CM {
		code = strings.ToUpper(strings.ReplaceAll(code, ".", ""))
		if len(code) > 3 {
			code = code[:3] + "." + code[3:]
		}
	}
	return code
}

// Lookup finds a code in the newest imported version of a system, or in the version named.
func (s *Store) Lookup(ctx context.Context, system, version, code string) (Concept, error) {
	code = NormaliseCode(system, code)
	q := `SELECT c.system, c.version, c.code, c.display, c.active FROM term_concepts c
		JOIN term_systems v ON v.system = c.system AND v.version = c.version
		WHERE c.system = ? AND c.code = ?`
	args := []any{system, code}
	if version != "" {
		q += ` AND c.version = ?`
		args = append(args, version)
	}
	q += ` ORDER BY v.imported_at DESC LIMIT 1`
	var c Concept
	var active int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&c.System, &c.Version, &c.Code, &c.Display, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return Concept{}, ErrNotFound
	}
	c.Active = active == 1
	return c, err
}

// Search finds codes by code prefix or by words in the display, newest version only.
func (s *Store) Search(ctx context.Context, system, text string, limit int) ([]Concept, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return []Concept{}, nil
	}
	var version string
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM term_systems WHERE system = ? ORDER BY imported_at DESC LIMIT 1`,
		system).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []Concept{}, nil
		}
		return nil, err
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
	codePrefix := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(NormaliseCode(system, text)) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT system, version, code, display, active FROM term_concepts
		WHERE system = ? AND version = ? AND (code LIKE ? ESCAPE '\' OR display LIKE ? ESCAPE '\')
		ORDER BY CASE WHEN code = ? THEN 0 WHEN code LIKE ? ESCAPE '\' THEN 1 ELSE 2 END, active DESC, length(display), code
		LIMIT ?`, system, version, codePrefix, like, NormaliseCode(system, text), codePrefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Concept{}
	for rows.Next() {
		var c Concept
		var active int
		if err := rows.Scan(&c.System, &c.Version, &c.Code, &c.Display, &active); err != nil {
			return nil, err
		}
		c.Active = active == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// Missing returns the codes of a list that are not in the newest version of a system. Nil when the system is not imported,
// because then nothing can be said either way.
func (s *Store) Missing(ctx context.Context, system string, codes []string) ([]string, error) {
	if system == "" || !s.HasSystem(ctx, system) {
		return nil, nil
	}
	missing := []string{}
	seen := map[string]bool{}
	for _, c := range codes {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := s.Lookup(ctx, system, "", c); errors.Is(err, ErrNotFound) {
			missing = append(missing, c)
		} else if err != nil {
			return nil, err
		}
	}
	return missing, nil
}

// LookupCode answers the FHIR server's CodeSystem operations.
func (s *Store) LookupCode(ctx context.Context, system, version, code string) (display, foundVersion string, active, found bool, err error) {
	c, err := s.Lookup(ctx, system, version, code)
	if errors.Is(err, ErrNotFound) {
		return "", "", false, false, nil
	}
	if err != nil {
		return "", "", false, false, err
	}
	return c.Display, c.Version, c.Active, true, nil
}
