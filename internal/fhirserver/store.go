// Package fhirserver implements a FHIR REST server.
//
// This exists because the useful end of a v2-to-FHIR mapper is somewhere to put
// the result. Being able to convert a message is a demo; being able to convert it,
// store it, and then find the patient again by identifier is a product.
//
// It is a real FHIR server in the parts that matter for that: a capability
// statement, create, read, update, delete, search on the parameters people
// actually use, and transaction bundles applied atomically. It is not a complete
// FHIR server — no history, no chained search, no _include — and it says so in its
// own capability statement rather than letting a client discover the gaps.
package fhirserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Store persists FHIR resources.
//
// Resources are stored as JSON with a small extracted index for the searchable
// parameters. That is how real FHIR servers work, and the alternative — searching
// by scanning JSON — stops working at the first thousand patients.
type Store struct {
	db      *sql.DB
	version fhir.Version
}

// Errors returned by the store.
var (
	// ErrNotFound means no such resource.
	ErrNotFound = errors.New("fhirserver: resource not found")
	// ErrDeleted means the resource existed and was deleted.
	ErrDeleted = errors.New("fhirserver: resource was deleted")
	// ErrConflict means a conditional operation matched more than one resource.
	ErrConflict = errors.New("fhirserver: conditional match found more than one resource")
	// ErrUnsupportedType means the resource type is not implemented.
	ErrUnsupportedType = errors.New("fhirserver: resource type is not supported")
)

// NewStore prepares the schema on an existing database handle.
//
// It shares the caller's database rather than opening its own, so a deployment has
// one file to back up rather than several that can be restored out of step.
func NewStore(db *sql.DB, version fhir.Version) (*Store, error) {
	if !version.Valid() {
		return nil, fmt.Errorf("fhirserver: unsupported FHIR version %q", version)
	}

	s := &Store{db: db, version: version}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// Version returns the FHIR release this store serves.
func (s *Store) Version() fhir.Version { return s.version }

var schema = []string{
	`CREATE TABLE IF NOT EXISTS fhir_resources (
		resource_type TEXT    NOT NULL,
		resource_id   TEXT    NOT NULL,
		version_id    INTEGER NOT NULL DEFAULT 1,
		last_updated  TEXT    NOT NULL,
		deleted       INTEGER NOT NULL DEFAULT 0,
		content       TEXT    NOT NULL,
		PRIMARY KEY (resource_type, resource_id)
	)`,

	`CREATE INDEX IF NOT EXISTS fhir_resources_updated
		ON fhir_resources(resource_type, last_updated DESC)`,

	// The search index holds one row per searchable value. Storing values rather
	// than scanning JSON is what keeps search working past the first few thousand
	// resources.
	// ref_type is the type a reference points at, for the reference parameters.
	//
	// Its absence was a real defect rather than a missing refinement. The index held a bare id, so Group/123 and
	// Patient/123 were the same row - a search for one could return the other, and a reference search returning the
	// wrong patient's records as the requested patient's is the worst outcome this server has available.
	//
	// Empty for a value that is not a reference, which is most of them.
	`CREATE TABLE IF NOT EXISTS fhir_search (
		resource_type TEXT NOT NULL,
		resource_id   TEXT NOT NULL,
		param         TEXT NOT NULL,
		value         TEXT NOT NULL,
		system        TEXT,
		ref_type      TEXT,
		value_raw     TEXT
	)`,

	`CREATE INDEX IF NOT EXISTS fhir_search_lookup
		ON fhir_search(resource_type, param, value)`,

	`CREATE INDEX IF NOT EXISTS fhir_search_owner
		ON fhir_search(resource_type, resource_id)`,

	// Every version of every resource, including the deletions.
	//
	// A separate table rather than versioning fhir_resources in place, because the current version is read constantly
	// and the history almost never - and a query for "the current Patient" should not have to filter a table that grows
	// without bound.
	//
	// The deletion is a row like any other, with no content. A history that omits deletions cannot answer the question
	// history is for: this record was here last week and is not now, what happened. An audit trail with the deletions
	// removed is not an audit trail.
	`CREATE TABLE IF NOT EXISTS fhir_history (
		resource_type TEXT    NOT NULL,
		resource_id   TEXT    NOT NULL,
		version_id    INTEGER NOT NULL,
		last_updated  TEXT    NOT NULL,
		deleted       INTEGER NOT NULL DEFAULT 0,
		content       TEXT,
		PRIMARY KEY (resource_type, resource_id, version_id)
	)`,

	`CREATE INDEX IF NOT EXISTS fhir_history_instance
		ON fhir_history(resource_type, resource_id, version_id DESC)`,

	`CREATE INDEX IF NOT EXISTS fhir_history_type
		ON fhir_history(resource_type, last_updated DESC)`,
}

func (s *Store) migrate(ctx context.Context) error {
	for i, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("fhirserver: schema statement %d: %w", i, err)
		}
	}

	if err := s.addRefType(ctx); err != nil {
		return err
	}

	return s.addValueRaw(ctx)
}

// addRefType brings a database created before the ref_type column forward.
//
// CREATE TABLE IF NOT EXISTS does nothing to a table that already exists, so a store written by an earlier build keeps the old shape and
// every reference query against it fails on an unknown column. That is a server that starts and then answers errors, which is worse than
// one that refuses to start.
func (s *Store) addRefType(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('fhir_search') WHERE name = 'ref_type'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("fhirserver: checking for the ref_type column: %w", err)
	}
	if count > 0 {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, `ALTER TABLE fhir_search ADD COLUMN ref_type TEXT`); err != nil {
		return fmt.Errorf("fhirserver: adding the ref_type column: %w", err)
	}

	// The index is rebuilt rather than left with the column empty.
	//
	// An empty ref_type has to mean something, and both available meanings are wrong for existing rows. Treated as
	// "matches nothing" a type-qualified search silently stops finding resources indexed by the old build. Treated as
	// "matches anything" it can return a Group where a Patient was asked for, which is the defect the column exists to
	// fix. So the rows are re-derived from the stored resources, which are the truth.
	return s.reindexAll(ctx)
}

// addValueRaw brings a database created before the value_raw column forward.
//
// String search parameters are indexed case-folded, because FHIR string search is case-insensitive and comparing folded values is how that is
// done. The :exact modifier is the one case that needs the original back: it means no prefix matching, no case folding and no accent folding,
// so a folded value cannot answer it.
//
// Stored beside the folded value rather than replacing it. Folding at query time instead would mean LOWER(value) on every string search, which
// cannot use an index and turns the common query into a table scan to serve the rare one.
func (s *Store) addValueRaw(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('fhir_search') WHERE name = 'value_raw'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("fhirserver: checking for the value_raw column: %w", err)
	}
	if count > 0 {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, `ALTER TABLE fhir_search ADD COLUMN value_raw TEXT`); err != nil {
		return fmt.Errorf("fhirserver: adding the value_raw column: %w", err)
	}

	// Rebuilt rather than backfilled from value.
	//
	// The folded value cannot be un-folded - "dubois" does not tell you whether the record said Dubois or DUBOIS - so copying it across would
	// populate the column with a confident wrong answer, and :exact would then quietly fail to match records written by an earlier build. The
	// stored resources are the truth.
	return s.reindexAll(ctx)
}

// Outcome describes what a write did, which is what a REST layer needs in order
// to answer 201 versus 200.
type Outcome struct {
	ResourceType string
	ID           string
	VersionID    int
	Created      bool
	LastUpdated  time.Time
}

// Put stores a resource, creating or replacing it.
//
// The version counter increments on every write. Without it a client has no way to
// detect that the resource changed under them, and lost-update bugs in clinical
// data are the kind nobody finds until an audit.
func (s *Store) Put(ctx context.Context, r fhir.Resource) (*Outcome, error) {
	if !supportedType(r.ResourceTypeName()) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, r.ResourceTypeName())
	}
	if r.ResourceID() == "" {
		return nil, fmt.Errorf("fhirserver: cannot store a resource with no id")
	}

	now := time.Now().UTC()

	// Stamp meta before serialising, so what is stored is what a client will read
	// back. A stored resource whose meta disagrees with its row is a debugging
	// nightmare.
	previous, err := s.currentVersion(ctx, r.ResourceTypeName(), r.ResourceID())
	if err != nil {
		return nil, err
	}
	versionID := previous + 1
	stampMeta(r, versionID, now)

	// Stored in the canonical internal form, not in the served release's form.
	//
	// This used to marshal with s.version, which meant an R4-configured server persisted R4-downgraded JSON and then failed to
	// read it back: Encounter.class is a single Coding in R4 and a list in R5, so the stored object could not be parsed into the
	// struct that wrote it. Every read, search and history entry for that resource then returned an error.
	//
	// It was invisible while the default was R5, because no downgrade happened. Changing the default to R4 exposed it
	// immediately, which is the argument for running the thing rather than reasoning about it.
	//
	// The release only affects what goes over the wire, so the downgrade belongs at the HTTP boundary and nowhere else.
	content, err := fhir.Marshal(r, fhir.CanonicalVersion)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO fhir_resources
			(resource_type, resource_id, version_id, last_updated, deleted, content)
		 VALUES (?, ?, ?, ?, 0, ?)
		 ON CONFLICT(resource_type, resource_id) DO UPDATE SET
			version_id = excluded.version_id,
			last_updated = excluded.last_updated,
			deleted = 0,
			content = excluded.content`,
		r.ResourceTypeName(), r.ResourceID(), versionID,
		dbtime.Format(now), string(content)); err != nil {
		return nil, err
	}

	// The same version recorded in the history, in the same transaction.
	//
	// In the transaction because a current version with no history row is a resource whose audit trail has a hole in it,
	// and a hole is indistinguishable from a version that was never written. Either both land or neither does.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO fhir_history
			(resource_type, resource_id, version_id, last_updated, deleted, content)
		 VALUES (?, ?, ?, ?, 0, ?)
		 ON CONFLICT(resource_type, resource_id, version_id) DO UPDATE SET
			last_updated = excluded.last_updated,
			deleted = 0,
			content = excluded.content`,
		r.ResourceTypeName(), r.ResourceID(), versionID,
		dbtime.Format(now), string(content)); err != nil {
		return nil, err
	}

	// The index is rebuilt rather than patched. Patching means working out which
	// values disappeared, and getting that wrong leaves a resource findable by a
	// value it no longer has.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM fhir_search WHERE resource_type = ? AND resource_id = ?`,
		r.ResourceTypeName(), r.ResourceID()); err != nil {
		return nil, err
	}
	if err := insertIndexEntries(ctx, tx, r); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Outcome{
		ResourceType: r.ResourceTypeName(),
		ID:           r.ResourceID(),
		VersionID:    versionID,
		Created:      previous == 0,
		LastUpdated:  now,
	}, nil
}

func (s *Store) currentVersion(ctx context.Context, resourceType, id string) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx,
		`SELECT version_id FROM fhir_resources WHERE resource_type = ? AND resource_id = ?`,
		resourceType, id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return version, nil
}

// Get reads a resource.
func (s *Store) Get(ctx context.Context, resourceType, id string) (fhir.Resource, error) {
	var (
		content string
		deleted int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT content, deleted FROM fhir_resources WHERE resource_type = ? AND resource_id = ?`,
		resourceType, id).Scan(&content, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// A deleted resource is reported as deleted rather than missing, because a
	// client resending it needs to know the difference.
	if deleted != 0 {
		return nil, ErrDeleted
	}
	return fhir.UnmarshalResource([]byte(content))
}

// Delete marks a resource deleted.
//
// The row is kept rather than removed, so a later read can say the resource was
// deleted rather than that it never existed.
func (s *Store) Delete(ctx context.Context, resourceType, id string) error {
	now := dbtime.Format(time.Now())

	// In a transaction, because the deletion, the history row and the index removal are one change.
	//
	// Previously three statements outside one: a failure between them left a resource marked deleted but still findable
	// by search, which is the worst of both - the read says it is gone and the search says it is here.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE fhir_resources SET deleted = 1, version_id = version_id + 1, last_updated = ?
		 WHERE resource_type = ? AND resource_id = ?`,
		now, resourceType, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}

	// The deletion recorded as a version with no content.
	//
	// A history that omits deletions cannot answer the question history exists for: this record was here last week and
	// is not now, what happened. The version number is read back rather than computed, so it matches whatever the update
	// above produced even if something else wrote in between.
	var versionID int
	if err := tx.QueryRowContext(ctx,
		`SELECT version_id FROM fhir_resources WHERE resource_type = ? AND resource_id = ?`,
		resourceType, id).Scan(&versionID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO fhir_history (resource_type, resource_id, version_id, last_updated, deleted, content)
		 VALUES (?, ?, ?, ?, 1, NULL)
		 ON CONFLICT(resource_type, resource_id, version_id) DO UPDATE SET
			last_updated = excluded.last_updated, deleted = 1, content = NULL`,
		resourceType, id, versionID, now); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM fhir_search WHERE resource_type = ? AND resource_id = ?`,
		resourceType, id); err != nil {
		return err
	}

	return tx.Commit()
}

// Count returns how many live resources of a type exist.
func (s *Store) Count(ctx context.Context, resourceType string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM fhir_resources WHERE resource_type = ? AND deleted = 0`,
		resourceType).Scan(&n)
	return n, err
}

// Counts returns live resource counts by type, for a dashboard.
func (s *Store) Counts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT resource_type, COUNT(*) FROM fhir_resources
		 WHERE deleted = 0 GROUP BY resource_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var (
			resourceType string
			n            int
		)
		if err := rows.Scan(&resourceType, &n); err != nil {
			return nil, err
		}
		out[resourceType] = n
	}
	return out, rows.Err()
}

// stampMeta sets versionId and lastUpdated, which is what makes a stored resource
// self-describing.
func stampMeta(r fhir.Resource, versionID int, now time.Time) {
	meta := &fhir.Meta{
		VersionID:   fmt.Sprintf("%d", versionID),
		LastUpdated: dbtime.Format(now),
	}

	switch v := r.(type) {
	case *fhir.Patient:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.Encounter:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.Observation:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.DiagnosticReport:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.Practitioner:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.Organization:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.Location:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.Specimen:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	case *fhir.ServiceRequest:
		meta.Profile = existingProfiles(v.Meta)
		v.Meta = meta
	}
}

func existingProfiles(meta *fhir.Meta) []string {
	if meta == nil {
		return nil
	}
	return meta.Profile
}

func supportedType(resourceType string) bool {
	for _, t := range fhir.SupportedResourceTypes() {
		if t == resourceType && t != "Bundle" && t != "OperationOutcome" {
			return true
		}
	}
	return false
}

// jsonPath reads a nested value out of a raw resource, for indexing.
func jsonPath(raw []byte, path ...string) any {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil
	}
	cur := tree
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

func referenceID(ref *fhir.Reference) string {
	if ref == nil {
		return ""
	}
	if i := strings.LastIndex(ref.Reference, "/"); i >= 0 {
		return ref.Reference[i+1:]
	}
	return ref.Reference
}

// reindexAll re-derives the whole search index from the stored resources.
//
// Used by the ref_type migration, and useful on its own for the same reason: the resources are the truth and the index is a cache of
// what is searchable in them. Anything that changes what indexEntries extracts - a new search parameter, a corrected one - leaves
// existing rows describing the old rules, and a search index that silently describes an earlier version of itself is very hard to
// notice from the outside.
//
// Done in one transaction. A half-reindexed store answers searches with some resources missing, which reads as data loss.
func (s *Store) reindexAll(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT resource_type, resource_id, content FROM fhir_resources WHERE deleted = 0`)
	if err != nil {
		return fmt.Errorf("fhirserver: reading resources to reindex: %w", err)
	}

	type stored struct {
		resourceType string
		id           string
		content      []byte
	}

	var all []stored
	for rows.Next() {
		var st stored
		if err := rows.Scan(&st.resourceType, &st.id, &st.content); err != nil {
			rows.Close()

			return fmt.Errorf("fhirserver: reading a resource to reindex: %w", err)
		}
		all = append(all, st)
	}
	if err := rows.Err(); err != nil {
		rows.Close()

		return fmt.Errorf("fhirserver: reading resources to reindex: %w", err)
	}
	rows.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fhir_search`); err != nil {
		return fmt.Errorf("fhirserver: clearing the search index: %w", err)
	}

	for _, st := range all {
		r, err := fhir.UnmarshalResource(st.content)
		if err != nil {
			// Skipped rather than failing the migration.
			//
			// A resource this build cannot parse is one an earlier build stored, which means either a type
			// since removed or a shape since changed. Refusing to start over one such row would strand a
			// whole database, and the resource itself is untouched - it is still readable by id, it is
			// simply not findable by search until something can parse it.
			continue
		}
		if err := insertIndexEntries(ctx, tx, r); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// insertIndexEntries writes the search rows for one resource.
//
// Shared by Put and by the reindex so the two cannot disagree about what is searchable. Two copies would drift, and the direction of
// drift is a resource that is findable when written and not after a reindex, or the reverse.
func insertIndexEntries(ctx context.Context, tx *sql.Tx, r fhir.Resource) error {
	for _, entry := range indexEntries(r) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fhir_search (resource_type, resource_id, param, value, system, ref_type, value_raw)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ResourceTypeName(), r.ResourceID(), entry.param,
			entry.value, entry.system, entry.refType, entry.valueRaw); err != nil {
			return err
		}
	}

	return nil
}

// referenceType is the resource type a reference points at, or empty when it does not say.
//
// Read from the reference string rather than from the Type field, because the Type field is optional and almost never populated by real
// senders, while the string form nearly always carries it: "Patient/123", "http://host/fhir/Patient/123", "Patient/123/_history/2".
//
// Empty rather than guessed when there is no type. A guess here would be the same defect in a new place: a reference recorded as
// pointing at a Patient when it does not is worse than one recorded as pointing at nothing, because the first is trusted.
func referenceType(ref *fhir.Reference) string {
	if ref == nil {
		return ""
	}

	raw := strings.TrimSpace(ref.Reference)
	if raw == "" {
		// A contained or logical reference may carry only a Type, so it is worth having.
		return strings.TrimSpace(ref.Type)
	}

	// A version suffix goes first, so the type is not read out of "_history".
	if i := strings.Index(raw, "/_history/"); i >= 0 {
		raw = raw[:i]
	}

	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) < 2 {
		// A bare id, which names no type. Common in bundles that rely on fullUrl.
		return strings.TrimSpace(ref.Type)
	}

	// The segment before the id. For an absolute URL that is still the type, because a FHIR endpoint's path always ends
	// [base]/Type/id - so taking the second-to-last segment works for both forms without parsing the URL.
	candidate := parts[len(parts)-2]

	// A resource type in FHIR always begins with a capital. Checked because the second-to-last segment of a URL is not
	// always a type: for "http://host/fhir/123" it is "fhir", and recording that as a resource type would produce a
	// reference that matches nothing and looks deliberate.
	if candidate == "" || candidate[0] < 'A' || candidate[0] > 'Z' {
		return strings.TrimSpace(ref.Type)
	}

	return candidate
}
