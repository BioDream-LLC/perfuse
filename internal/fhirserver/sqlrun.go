package fhirserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/sof"
)

// SQL on FHIR v2 $sql-run (system level): run a ViewDefinition over this server's resources, or over resources sent with
// the request, and answer with the rows as CSV, NDJSON, JSON or Parquet. Only ViewDefinition subjects are run; SQLQuery and
// SQLView Libraries (SQL over the views' tables) are refused with the reason, since running SQL needs a SQL engine.

// ViewLibrary resolves a ViewDefinition named by subjectReference (ViewDefinition/{id}) or subjectCanonical (its url,
// optionally |version). nil when the server keeps no views.
type ViewLibrary func(ctx context.Context, reference, canonical string) ([]byte, bool)

// maxRunRows bounds a synchronous run.
const maxRunRows = 1_000_000

type sqlRunParams struct {
	subject   []byte
	subjects  int
	resources []map[string]any
	format    string
	header    bool
	patients  []string
	since     string
	limit     int
}

func (s *Server) handleSQLRun(w http.ResponseWriter, r *http.Request) {
	p := sqlRunParams{format: "ndjson", header: true}
	fail := func(status int, code, msg string) { s.writeOutcome(w, r, status, fhir.SeverityError, code, msg) }
	set := func(name, v string) string {
		switch name {
		case "_format":
			p.format = strings.ToLower(strings.TrimSpace(v))
			switch p.format {
			case "text/csv":
				p.format = "csv"
			case "application/x-ndjson", "application/fhir+ndjson":
				p.format = "ndjson"
			case "application/json":
				p.format = "json"
			}
		case "header":
			p.header = v != "false"
		case "patient":
			p.patients = append(p.patients, strings.TrimPrefix(strings.TrimSpace(v), "Patient/"))
		case "_since":
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
			if err != nil {
				return "_since must be an instant such as 2026-01-01T00:00:00Z"
			}
			p.since = t.UTC().Format(time.RFC3339Nano)
		case "_limit":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 1 {
				return "_limit must be a positive whole number"
			}
			p.limit = n
		case "subjectReference", "subjectCanonical":
			p.subjects++
			if s.Views == nil {
				return "\x00404this server keeps no ViewDefinitions to name; send the view as subjectResource"
			}
			ref, can := "", ""
			if name == "subjectReference" {
				ref = strings.TrimSpace(v)
			} else {
				can = strings.TrimSpace(v)
			}
			raw, ok := s.Views(r.Context(), ref, can)
			if !ok {
				return "\x00404no ViewDefinition " + v + " is kept on this server"
			}
			p.subject = raw
		case "source":
			return "source names an external data source, and this server runs views over its own data or the resources sent"
		case "group":
			return "group filtering is not supported; use patient"
		}
		return ""
	}
	problem := ""
	q := r.URL.Query()
	for name, vals := range q {
		for _, v := range vals {
			if msg := set(name, v); msg != "" && problem == "" {
				problem = msg
			}
		}
	}
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			fail(http.StatusBadRequest, "invalid", "the request body could not be read")
			return
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		var generic map[string]any
		if err := d.Decode(&generic); err != nil || generic["resourceType"] != "Parameters" {
			fail(http.StatusBadRequest, "invalid", "a POST to $sql-run carries a Parameters resource")
			return
		}
		list, _ := generic["parameter"].([]any)
		for _, x := range list {
			pm, _ := x.(map[string]any)
			name, _ := pm["name"].(string)
			switch name {
			case "subjectResource":
				res, _ := pm["resource"].(map[string]any)
				if res == nil {
					problem = "subjectResource carries the ViewDefinition as its resource"
					continue
				}
				p.subjects++
				raw, _ := json.Marshal(res)
				p.subject = raw
			case "resource":
				res, _ := pm["resource"].(map[string]any)
				if res == nil {
					problem = "resource carries a FHIR resource"
					continue
				}
				if res["resourceType"] == "Bundle" {
					entries, _ := res["entry"].([]any)
					for _, e := range entries {
						em, _ := e.(map[string]any)
						if rr, ok := em["resource"].(map[string]any); ok {
							p.resources = append(p.resources, rr)
						}
					}
					continue
				}
				p.resources = append(p.resources, res)
			case "parameters":
				problem = "parameters binds a SQLQuery's parameters, and a ViewDefinition declares none"
			case "context":
				problem = "context supplies artifacts for SQLQuery and SQLView subjects, which this server does not run"
			default:
				val := ""
				for k, v := range pm {
					if strings.HasPrefix(k, "value") {
						switch x := v.(type) {
						case string:
							val = x
						case bool:
							val = strconv.FormatBool(x)
						case json.Number:
							val = x.String()
						case map[string]any:
							val, _ = x["reference"].(string)
						}
					}
				}
				if msg := set(name, val); msg != "" && problem == "" {
					problem = msg
				}
			}
		}
	}
	if strings.HasPrefix(problem, "\x00404") {
		fail(http.StatusNotFound, "not-found", strings.TrimPrefix(problem, "\x00404"))
		return
	}
	if problem != "" {
		fail(http.StatusBadRequest, "invalid", problem)
		return
	}
	if p.subjects != 1 {
		fail(http.StatusBadRequest, "invalid", "name exactly one subject: subjectResource, subjectReference or subjectCanonical")
		return
	}
	if p.format == "fhir" {
		fail(http.StatusBadRequest, "not-supported", "_format=fhir is not offered; use csv, ndjson, json or parquet")
		return
	}
	ct, ok := sof.Formats[p.format]
	if !ok {
		fail(http.StatusBadRequest, "invalid", fmt.Sprintf("_format %q is not one of csv, ndjson, json and parquet", p.format))
		return
	}
	var kind map[string]any
	_ = json.Unmarshal(p.subject, &kind)
	if rt, _ := kind["resourceType"].(string); rt == "Library" {
		fail(http.StatusUnprocessableEntity, "not-supported", "SQLQuery and SQLView Libraries are not run here: only ViewDefinitions")
		return
	}
	view, err := sof.Compile(p.subject)
	if err != nil {
		fail(http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	caller := CallerFrom(r.Context())
	if caller != nil && !caller.Allows(view.Resource, false) {
		fail(http.StatusForbidden, "forbidden", "this token's scopes do not allow reading "+view.Resource)
		return
	}
	// A token bound to one patient sees that patient's rows only, whatever patient it asks for.
	if caller != nil && caller.Patient != "" {
		for _, id := range p.patients {
			if id != caller.Patient {
				fail(http.StatusForbidden, "forbidden", "this token may read only its own patient")
				return
			}
		}
		p.patients = []string{caller.Patient}
	}

	var rows []sof.Row
	limit := maxRunRows
	if p.limit > 0 && p.limit < limit {
		limit = p.limit
	}
	run := func(res map[string]any) error {
		out, err := view.Run(res)
		if err != nil {
			return err
		}
		for _, row := range out {
			if len(rows) >= limit {
				return errEnough
			}
			rows = append(rows, row)
		}
		return nil
	}
	if len(p.resources) > 0 {
		for _, res := range p.resources {
			if err := run(res); err != nil {
				if err == errEnough {
					break
				}
				fail(http.StatusUnprocessableEntity, "processing", err.Error())
				return
			}
		}
	} else {
		if err := s.eachForView(r.Context(), caller, view.Resource, p.patients, p.since, run); err != nil && err != errEnough {
			fail(http.StatusUnprocessableEntity, "processing", err.Error())
			return
		}
		if len(rows) >= maxRunRows && p.limit == 0 {
			fail(http.StatusRequestEntityTooLarge, "too-costly", fmt.Sprintf("the view gives more than %d rows; add _limit, patient "+
				"or _since, or run it over a bulk export with perfuse sof", maxRunRows))
			return
		}
	}
	w.Header().Set("Content-Type", ct)
	if p.format == "parquet" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(view.Name)+`.parquet"`)
	}
	wr, err := sof.NewWriter(w, p.format, view.Columns, p.header)
	if err != nil {
		fail(http.StatusBadRequest, "invalid", err.Error())
		return
	}
	for _, row := range rows {
		if err := wr.Write(row); err != nil {
			return
		}
	}
	_ = wr.Close()
	if s.Log != nil {
		s.Log.Info("fhir $sql-run", "view", view.Name, "resource", view.Resource, "rows", len(rows), "format", p.format,
			"caller", callerName(caller))
	}
}

var errEnough = fmt.Errorf("enough rows")

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "view"
	}
	return b.String()
}

// eachForView reads the stored resources of one type (narrowed to patients and _since in the query, as export does), in
// the FHIR version this server speaks, and hands each to fn. Granular SMART scopes are applied to each resource.
func (s *Server) eachForView(ctx context.Context, caller *Caller, resourceType string, patients []string, since string,
	fn func(map[string]any) error) error {
	query := `SELECT content FROM fhir_resources WHERE resource_type = ? AND deleted = 0`
	args := []any{resourceType}
	if since != "" {
		query += ` AND last_updated >= ?`
		args = append(args, since)
	}
	if len(patients) > 0 {
		if resourceType == "Patient" {
			query += ` AND resource_id IN (` + placeholders(len(patients)) + `)`
		} else {
			query += ` AND EXISTS (SELECT 1 FROM fhir_search x
				WHERE x.resource_type = fhir_resources.resource_type
				  AND x.resource_id = fhir_resources.resource_id
				  AND x.param IN ('patient', 'subject')
				  AND x.value IN (` + placeholders(len(patients)) + `)
				  AND (x.ref_type = 'Patient' OR x.ref_type IS NULL OR x.ref_type = ''))`
		}
		for _, id := range patients {
			args = append(args, id)
		}
	}
	query += ` ORDER BY resource_id`
	rows, err := s.Store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return err
		}
		raw := []byte(content)
		res, err := fhir.UnmarshalResource(raw)
		if err != nil {
			continue
		}
		if caller != nil && !permitsScopes(caller, res) {
			continue
		}
		if converted, err := fhir.MarshalVersioned(res, s.Store.Version()); err == nil {
			raw = converted
		}
		var m map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			continue
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return rows.Err()
}

// RunView runs a compiled view over this server's resources, for the console (which has already checked the person may).
// limit bounds the rows; truncated says there were more.
func (s *Server) RunView(ctx context.Context, view *sof.View, patients []string, limit int) (rows []sof.Row, truncated bool, err error) {
	err = s.eachForView(ctx, nil, view.Resource, patients, "", func(res map[string]any) error {
		out, err := view.Run(res)
		if err != nil {
			return err
		}
		for _, r := range out {
			if len(rows) >= limit {
				truncated = true
				return errEnough
			}
			rows = append(rows, r)
		}
		return nil
	})
	if err == errEnough {
		err = nil
	}
	return rows, truncated, err
}
