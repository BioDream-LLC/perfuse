package fhirserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Bulk export, the $export operation.
//
// What payers, registries and research groups ask for first, and the one FHIR interaction that is deliberately asynchronous: a request that
// might produce four gigabytes cannot be answered on the connection that asked for it. The client kicks it off, polls, and then fetches
// newline-delimited JSON files.
//
// The asynchrony is the whole design problem, and it is why this keeps its state in the database rather than in a map. A job that exists only
// in memory disappears on restart, and what the client sees then is a poll returning 404 for a job the server told it to poll - which reads
// as the client's bug.

// ExportStatus is where a job has got to.
type ExportStatus string

const (
	// ExportRunning means the job is still producing files.
	ExportRunning ExportStatus = "running"
	// ExportComplete means every file is ready.
	ExportComplete ExportStatus = "complete"
	// ExportFailed means the job stopped and will not produce anything more.
	ExportFailed ExportStatus = "failed"
)

// ExportJob is one bulk export request.
type ExportJob struct {
	ID          string
	Status      ExportStatus
	RequestedAt time.Time
	CompletedAt time.Time
	Request     string
	Types       []string
	Since       string
	PatientID   string
	Error       string

	// Files are the outputs, one per resource type that had anything.
	Files []ExportFile
}

// ExportFile is one NDJSON output.
type ExportFile struct {
	ResourceType string
	Count        int
}

var exportSchema = []string{
	`CREATE TABLE IF NOT EXISTS fhir_export_jobs (
		id            TEXT PRIMARY KEY,
		status        TEXT NOT NULL,
		requested_at  TEXT NOT NULL,
		completed_at  TEXT,
		request_url   TEXT NOT NULL,
		types         TEXT,
		since         TEXT,
		patient_id    TEXT,
		error         TEXT
	)`,

	// One row per resource type, holding the whole NDJSON body.
	//
	// In the database rather than on disk, which is a deliberate limit rather than an oversight. It means an export is
	// bounded by what SQLite will hold in a row and by the memory to assemble it, and in exchange there is no export
	// directory to configure, no permissions to get wrong, and nothing left behind on disk holding patient data after
	// the job is deleted. For an installation exporting a hospital rather than a country that is the right trade, and
	// the size bound is enforced explicitly rather than discovered.
	`CREATE TABLE IF NOT EXISTS fhir_export_files (
		job_id        TEXT NOT NULL,
		resource_type TEXT NOT NULL,
		row_count     INTEGER NOT NULL,
		body          TEXT NOT NULL,
		PRIMARY KEY (job_id, resource_type)
	)`,
}

// maxExportBytes bounds one output file.
//
// Stated and enforced rather than left to fail somewhere in SQLite. An export that dies partway through with a driver error tells the operator
// nothing; one that stops and says it exceeded the limit tells them to narrow the request by type or by _since.
const maxExportBytes = 256 << 20

// ExportManager runs bulk exports.
type ExportManager struct {
	store *Store

	// running tracks jobs this process started, so a second poll does not start a second copy of the same work. Not the
	// source of truth for status - the database is - because this map is empty after a restart while the jobs are not.
	mu      sync.Mutex
	running map[string]bool
}

// NewExportManager prepares the export tables.
func NewExportManager(ctx context.Context, store *Store) (*ExportManager, error) {
	for i, stmt := range exportSchema {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("fhirserver: export schema statement %d: %w", i, err)
		}
	}

	m := &ExportManager{store: store, running: map[string]bool{}}

	// Any job left running by a previous process is marked failed.
	//
	// It cannot be resumed - nothing recorded how far it got - and leaving it as running means a client polls forever
	// against a job nobody is working on. Failing it with a reason is the only honest state, and the client can ask
	// again.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE fhir_export_jobs SET status = ?, completed_at = ?, error = ?
		 WHERE status = ?`,
		ExportFailed, dbtime.Format(time.Now()),
		"the server restarted while this export was running; request it again", ExportRunning,
	); err != nil {
		return nil, fmt.Errorf("fhirserver: clearing interrupted exports: %w", err)
	}

	return m, nil
}

// ExportRequest is what to export.
type ExportRequest struct {
	// Types narrows the export. Empty means every supported type, which is what _type absent means.
	Types []string

	// Since restricts to resources changed at or after this instant.
	Since string

	// PatientID restricts the export to one patient, for a caller whose token is limited to one.
	PatientID string

	// RequestURL is recorded and returned in the manifest, because the specification requires the manifest to say what
	// was asked for - a file with no record of its query is unusable six months later.
	RequestURL string
}

// Start records a job and begins producing it.
func (m *ExportManager) Start(ctx context.Context, req ExportRequest) (*ExportJob, error) {
	types := req.Types
	if len(types) == 0 {
		for _, t := range fhir.SupportedResourceTypes() {
			if !supportedType(t) {
				continue
			}
			types = append(types, t)
		}
	}

	for _, t := range types {
		if !supportedType(t) {
			return nil, fmt.Errorf("resource type %q cannot be exported because this server does not hold it", t)
		}
	}

	job := &ExportJob{
		ID:          newExportID(),
		Status:      ExportRunning,
		RequestedAt: time.Now().UTC(),
		Request:     req.RequestURL,
		Types:       types,
		Since:       req.Since,
		PatientID:   req.PatientID,
	}

	if _, err := m.store.db.ExecContext(ctx,
		`INSERT INTO fhir_export_jobs (id, status, requested_at, request_url, types, since, patient_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Status, dbtime.Format(job.RequestedAt),
		job.Request, strings.Join(types, ","), job.Since, job.PatientID); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.running[job.ID] = true
	m.mu.Unlock()

	// Its own context, deliberately.
	//
	// The request context is cancelled when the HTTP response is written, which for an accepted export is immediately -
	// so inheriting it would cancel every job at the moment it started. This is exactly the sort of thing that works in
	// a test where the export finishes before the handler returns.
	go m.run(context.WithoutCancel(ctx), job)

	return job, nil
}

// run produces the files for a job.
func (m *ExportManager) run(ctx context.Context, job *ExportJob) {
	defer func() {
		m.mu.Lock()
		delete(m.running, job.ID)
		m.mu.Unlock()
	}()

	for _, resourceType := range job.Types {
		body, count, err := m.exportType(ctx, resourceType, job)
		if err != nil {
			m.fail(ctx, job.ID, err)

			return
		}
		if count == 0 {
			// No file for a type with nothing in it.
			//
			// The specification says to omit it, and the reason is worth keeping: a manifest listing twelve
			// files of which nine are empty makes a client fetch nine times for nothing, and an empty file is
			// indistinguishable from a type that failed to export.
			continue
		}

		if _, err := m.store.db.ExecContext(ctx,
			`INSERT INTO fhir_export_files (job_id, resource_type, row_count, body)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(job_id, resource_type) DO UPDATE SET
				row_count = excluded.row_count, body = excluded.body`,
			job.ID, resourceType, count, body); err != nil {
			m.fail(ctx, job.ID, err)

			return
		}
	}

	if _, err := m.store.db.ExecContext(ctx,
		`UPDATE fhir_export_jobs SET status = ?, completed_at = ? WHERE id = ?`,
		ExportComplete, dbtime.Format(time.Now()), job.ID); err != nil {
		m.fail(ctx, job.ID, err)
	}
}

// exportType writes every resource of one type as newline-delimited JSON.
func (m *ExportManager) exportType(ctx context.Context, resourceType string, job *ExportJob) (string, int, error) {
	query := `SELECT content FROM fhir_resources WHERE resource_type = ? AND deleted = 0`
	args := []any{resourceType}

	if job.Since != "" {
		query += ` AND last_updated >= ?`
		args = append(args, job.Since)
	}

	// A patient-limited export is narrowed in the query rather than filtered afterwards, so a resource belonging to
	// another patient is never loaded at all.
	if job.PatientID != "" {
		if resourceType == "Patient" {
			query += ` AND resource_id = ?`
			args = append(args, job.PatientID)
		} else {
			query += ` AND EXISTS (SELECT 1 FROM fhir_search x
				WHERE x.resource_type = fhir_resources.resource_type
				  AND x.resource_id = fhir_resources.resource_id
				  AND x.param IN ('patient', 'subject')
				  AND x.value = ?
				  AND (x.ref_type = 'Patient' OR x.ref_type IS NULL OR x.ref_type = ''))`
			args = append(args, job.PatientID)
		}
	}

	query += ` ORDER BY resource_id`

	rows, err := m.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()

	var (
		out   strings.Builder
		count int
	)

	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return "", 0, err
		}

		// Compacted, because stored content may be indented and NDJSON requires one resource per line - an
		// indented resource would break the format in a way that is only visible to the consumer.
		compact, err := compactJSON([]byte(content))
		if err != nil {
			// Skipped rather than failing the export. A resource that cannot be re-encoded is one row, and
			// losing the whole export over it helps nobody - but the count reflects what was written, so the
			// manifest does not overstate.
			continue
		}

		if out.Len()+len(compact)+1 > maxExportBytes {
			return "", 0, fmt.Errorf(
				"the %s export exceeds %d bytes; narrow the request with _type or _since",
				resourceType, maxExportBytes)
		}

		out.Write(compact)
		out.WriteByte('\n')
		count++
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}

	return out.String(), count, nil
}

// fail records that a job stopped.
func (m *ExportManager) fail(ctx context.Context, id string, cause error) {
	_, _ = m.store.db.ExecContext(ctx,
		`UPDATE fhir_export_jobs SET status = ?, completed_at = ?, error = ? WHERE id = ?`,
		ExportFailed, dbtime.Format(time.Now()), cause.Error(), id)
}

// Job reads a job's current state.
func (m *ExportManager) Job(ctx context.Context, id string) (*ExportJob, error) {
	var (
		job       ExportJob
		requested string
		completed sql.NullString
		types     sql.NullString
		since     sql.NullString
		patient   sql.NullString
		failure   sql.NullString
	)

	err := m.store.db.QueryRowContext(ctx,
		`SELECT id, status, requested_at, completed_at, request_url, types, since, patient_id, error
		 FROM fhir_export_jobs WHERE id = ?`, id).
		Scan(&job.ID, &job.Status, &requested, &completed, &job.Request,
			&types, &since, &patient, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	job.RequestedAt, _ = time.Parse(time.RFC3339Nano, requested)
	if completed.Valid {
		job.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed.String)
	}
	if types.Valid && types.String != "" {
		job.Types = strings.Split(types.String, ",")
	}
	job.Since = since.String
	job.PatientID = patient.String
	job.Error = failure.String

	rows, err := m.store.db.QueryContext(ctx,
		`SELECT resource_type, row_count FROM fhir_export_files WHERE job_id = ? ORDER BY resource_type`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var f ExportFile
		if err := rows.Scan(&f.ResourceType, &f.Count); err != nil {
			return nil, err
		}
		job.Files = append(job.Files, f)
	}

	return &job, rows.Err()
}

// File reads one output body.
func (m *ExportManager) File(ctx context.Context, jobID, resourceType string) (string, error) {
	var body string

	err := m.store.db.QueryRowContext(ctx,
		`SELECT body FROM fhir_export_files WHERE job_id = ? AND resource_type = ?`,
		jobID, resourceType).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}

	return body, err
}

// Delete removes a job and its files.
//
// The specification makes this a DELETE on the status endpoint, and it matters more here than usual: the files hold patient data, so a client
// that has finished with an export needs a way to say so rather than waiting for something to expire.
func (m *ExportManager) Delete(ctx context.Context, id string) error {
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM fhir_export_jobs WHERE id = ?`, id)
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

	// The files go in the same transaction, because files without a job are patient data nothing will ever clean up and
	// nothing will ever serve - which is the worst possible residue.
	if _, err := tx.ExecContext(ctx, `DELETE FROM fhir_export_files WHERE job_id = ?`, id); err != nil {
		return err
	}

	return tx.Commit()
}

// compactJSON removes insignificant whitespace.
func compactJSON(raw []byte) ([]byte, error) {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}

	return json.Marshal(tree)
}

// newExportID makes a job identifier.
//
// Random rather than sequential, because the status URL is the only thing guarding an export: a client holding one can fetch the files. A
// sequential id would let anyone who obtained one poll for every other export on the server.
func newExportID() string {
	return randomToken(16)
}

// randomToken returns n bytes of randomness as hex.
//
// crypto/rand rather than math/rand, and it panics rather than falling back. A predictable export identifier is a way to read another client's
// export, and a silent fallback to a weak source is the failure that never gets noticed - whereas a server that will not start does.
func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("fhirserver: no randomness available for an export identifier: " + err.Error())
	}

	return hex.EncodeToString(buf)
}
