package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/biodream-llc/perfuse/internal/sof"
	"github.com/biodream-llc/perfuse/internal/store"
)

// SQL on FHIR views: the library of ViewDefinitions kept beside the database (one JSON file each, named by the view's name),
// which the FHIR endpoint's $sql-run also resolves by reference or canonical URL; a run over this server's FHIR store or over
// resources pasted in; and downloads as CSV, NDJSON or Parquet.

// ViewRunner runs a view over this server's FHIR store (fhirserver.Server.RunView).
type ViewRunner func(ctx context.Context, v *sof.View, patients []string, limit int) ([]sof.Row, bool, error)

var viewNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

type viewSummary struct {
	Name     string   `json:"name"`
	Title    string   `json:"title,omitempty"`
	URL      string   `json:"url,omitempty"`
	Resource string   `json:"resource"`
	Columns  []string `json:"columns"`
	Problem  string   `json:"problem,omitempty"`
}

type viewLibrary struct {
	mu  sync.Mutex
	dir string
}

func (l *viewLibrary) list() []viewSummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []viewSummary{}
	if l.dir == "" {
		return out
	}
	files, _ := filepath.Glob(filepath.Join(l.dir, "*.json"))
	sort.Strings(files)
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		raw, err := os.ReadFile(f)
		sum := viewSummary{Name: name, Columns: []string{}}
		if err != nil {
			sum.Problem = err.Error()
		} else if v, err := sof.Compile(raw); err != nil {
			sum.Problem = err.Error()
		} else {
			sum.Title, sum.URL, sum.Resource = v.Title, v.URL, v.Resource
			for _, c := range v.Columns {
				sum.Columns = append(sum.Columns, c.Name)
			}
		}
		out = append(out, sum)
	}
	return out
}

func (l *viewLibrary) read(name string) ([]byte, bool) {
	if l.dir == "" || !viewNameRE.MatchString(name) {
		return nil, false
	}
	raw, err := os.ReadFile(filepath.Join(l.dir, name+".json"))
	return raw, err == nil
}

// Resolve is the FHIR endpoint's ViewLibrary: ViewDefinition/{name}, or a canonical url (|version ignored).
func (l *viewLibrary) Resolve(_ context.Context, reference, canonical string) ([]byte, bool) {
	if reference != "" {
		name := strings.TrimPrefix(reference, "ViewDefinition/")
		if i := strings.LastIndex(name, "/ViewDefinition/"); i >= 0 {
			name = name[i+len("/ViewDefinition/"):]
		}
		return l.read(name)
	}
	url, _, _ := strings.Cut(canonical, "|")
	for _, v := range l.list() {
		if v.URL != "" && v.URL == url {
			return l.read(v.Name)
		}
	}
	return nil, false
}

func (s *Server) views() *viewLibrary {
	s.viewsOnce.Do(func() { s.viewsLib = &viewLibrary{dir: s.ViewsDir} })
	return s.viewsLib
}

// ViewLibrary is the views directory as the FHIR endpoint's resolver.
func (s *Server) ViewLibrary() func(ctx context.Context, reference, canonical string) ([]byte, bool) {
	return s.views().Resolve
}

func (s *Server) registerViews(mux *recordingMux) {
	mux.Handle("GET /api/views", s.require(store.RoleViewer, s.handleViews))
	mux.Handle("GET /api/views/{name}", s.require(store.RoleViewer, s.handleViewGet))
	mux.Handle("PUT /api/views/{name}", s.require(store.RoleEditor, s.handleViewPut))
	mux.Handle("DELETE /api/views/{name}", s.require(store.RoleEditor, s.handleViewDelete))
	mux.Handle("POST /api/views/run", s.require(store.RoleViewer, s.handleViewRun))
}

func (s *Server) handleViews(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	s.ok(w, map[string]any{"views": s.views().list(), "examples": sof.Examples(), "fhir": s.RunView != nil,
		"dir": s.ViewsDir, "formats": []string{"csv", "ndjson", "json", "parquet"}})
}

func (s *Server) handleViewGet(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	raw, ok := s.views().read(r.PathValue("name"))
	if !ok {
		s.fail(w, r, http.StatusNotFound, "no view "+r.PathValue("name")+" is kept")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

type viewIn struct {
	View json.RawMessage `json:"view"`
}

func (s *Server) handleViewPut(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")
	if !viewNameRE.MatchString(name) {
		s.fail(w, r, http.StatusBadRequest, "a view's name starts with a letter and holds letters, digits and _ (at most 64)")
		return
	}
	if s.ViewsDir == "" {
		s.fail(w, r, http.StatusServiceUnavailable, "this server has no views directory")
		return
	}
	var in viewIn
	if !s.decode(w, r, &in) {
		return
	}
	v, err := sof.Compile(in.View)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if v.Name != "" && v.Name != name {
		s.fail(w, r, http.StatusBadRequest, "the view is named "+v.Name+"; save it under that name, or change its name")
		return
	}
	var pretty map[string]any
	_ = json.Unmarshal(in.View, &pretty)
	pretty["name"] = name
	out, _ := json.MarshalIndent(pretty, "", "  ")
	lib := s.views()
	lib.mu.Lock()
	err = os.MkdirAll(s.ViewsDir, 0o750)
	if err == nil {
		tmp := filepath.Join(s.ViewsDir, name+".json.tmp")
		if err = os.WriteFile(tmp, append(out, '\n'), 0o640); err == nil {
			err = os.Rename(tmp, filepath.Join(s.ViewsDir, name+".json"))
		}
	}
	lib.mu.Unlock()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "view.save", Target: name, IP: clientIP(r)})
	s.ok(w, map[string]string{"saved": name})
}

func (s *Server) handleViewDelete(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")
	if _, ok := s.views().read(name); !ok {
		s.fail(w, r, http.StatusNotFound, "no view "+name+" is kept")
		return
	}
	lib := s.views()
	lib.mu.Lock()
	err := os.Remove(filepath.Join(s.ViewsDir, name+".json"))
	lib.mu.Unlock()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "view.delete", Target: name, IP: clientIP(r)})
	s.ok(w, map[string]string{"deleted": name})
}

type viewRunIn struct {
	View json.RawMessage `json:"view"`
	// Resources, when given, are run instead of the FHIR store: NDJSON, a Bundle, a JSON array or one resource.
	Resources string `json:"resources"`
	Patient   string `json:"patient"`
	Limit     int    `json:"limit"`
	// Format is table (rows as JSON for the screen, the default) or csv, ndjson, json, parquet (a download).
	Format string `json:"format"`
}

const viewRunMax = 100_000

func (s *Server) handleViewRun(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var in viewRunIn
	if !s.decode(w, r, &in) {
		return
	}
	v, err := sof.Compile(in.View)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > viewRunMax {
		limit = viewRunMax
	}
	var rows []sof.Row
	truncated := false
	source := "store"
	if strings.TrimSpace(in.Resources) != "" {
		source = "pasted"
		err = sof.ReadResourcesBytes([]byte(in.Resources), func(res map[string]any) error {
			out, err := v.Run(res)
			if err != nil {
				return err
			}
			for _, row := range out {
				if len(rows) >= limit {
					truncated = true
					return errViewEnough
				}
				rows = append(rows, row)
			}
			return nil
		})
		if errors.Is(err, errViewEnough) {
			err = nil
		}
	} else {
		if s.RunView == nil {
			s.fail(w, r, http.StatusConflict, "this server has no FHIR store (start it with -fhir), so paste resources to run the view over them")
			return
		}
		if sess.TenantID != store.DefaultTenant || len(sess.ChannelGroups) > 0 {
			s.fail(w, r, http.StatusForbidden, "the FHIR store belongs to the whole server; an account limited to a tenant or to channel "+
				"groups may run views only over resources it pastes")
			return
		}
		var patients []string
		if p := strings.TrimPrefix(strings.TrimSpace(in.Patient), "Patient/"); p != "" {
			patients = []string{p}
		}
		rows, truncated, err = s.RunView(r.Context(), v, patients, limit)
		_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "view.run", Target: v.Resource,
			Detail: strings.TrimSpace(v.Name + " " + in.Patient), IP: clientIP(r)})
	}
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if in.Format != "" && in.Format != "table" {
		ct, ok := sof.Formats[in.Format]
		if !ok {
			s.fail(w, r, http.StatusBadRequest, "the format is one of table, csv, ndjson, json and parquet")
			return
		}
		name := v.Name
		if !viewNameRE.MatchString(name) {
			name = "view"
		}
		ext := map[string]string{"csv": "csv", "ndjson": "ndjson", "json": "json", "parquet": "parquet"}[in.Format]
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+"."+ext+`"`)
		wr, err := sof.NewWriter(w, in.Format, v.Columns, true)
		if err != nil {
			s.failErr(w, r, err)
			return
		}
		for _, row := range rows {
			_ = wr.Write(row)
		}
		_ = wr.Close()
		return
	}
	cols := make([]map[string]any, len(v.Columns))
	for i, c := range v.Columns {
		cols[i] = map[string]any{"name": c.Name, "type": c.Type, "collection": c.Collection}
	}
	if rows == nil {
		rows = []sof.Row{}
	}
	s.ok(w, map[string]any{"columns": cols, "rows": rows, "truncated": truncated, "source": source, "resource": v.Resource})
}

var errViewEnough = errors.New("enough")
