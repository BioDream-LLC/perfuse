package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/measure"
	"github.com/biodream-llc/perfuse/internal/sof"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Digital quality measures in the console: the Measures the FHIR store holds, the shipped examples, and evaluation to a
// MeasureReport over the store or over pasted resources.

// MeasureService is the FHIR server's measure side (fhirserver.Server); nil without -fhir.
type MeasureService interface {
	ListMeasures(ctx context.Context) ([]fhirserver.MeasureSummary, error)
	MeasureJSON(ctx context.Context, id string) ([]byte, error)
	PutMeasure(ctx context.Context, raw []byte) (string, error)
	DeleteMeasure(ctx context.Context, id string) error
	EvaluateMeasure(ctx context.Context, req fhirserver.EvaluateMeasureRequest, caller *fhirserver.Caller) (map[string]any, error)
}

func (s *Server) registerMeasures(mux *recordingMux) {
	mux.Handle("GET /api/measures", s.require(store.RoleViewer, s.handleMeasures))
	mux.Handle("GET /api/measures/{id}", s.require(store.RoleViewer, s.handleMeasureGet))
	mux.Handle("PUT /api/measures", s.require(store.RoleEditor, s.handleMeasurePut))
	mux.Handle("DELETE /api/measures/{id}", s.require(store.RoleEditor, s.handleMeasureDelete))
	mux.Handle("POST /api/measures/evaluate", s.require(store.RoleViewer, s.handleMeasureEvaluate))
}

// wholeServerAccount is an account that may read the server-wide FHIR store.
func wholeServerAccount(sess *store.Session) bool {
	return sess.TenantID == store.DefaultTenant && len(sess.ChannelGroups) == 0
}

func (s *Server) handleMeasures(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	out := map[string]any{"fhir": s.Measures != nil, "examples": measure.Examples(), "measures": []fhirserver.MeasureSummary{}}
	if s.Measures != nil && wholeServerAccount(sess) {
		list, err := s.Measures.ListMeasures(r.Context())
		if err != nil {
			s.failErr(w, r, err)
			return
		}
		out["measures"] = list
	}
	s.ok(w, out)
}

func (s *Server) measuresReady(w http.ResponseWriter, r *http.Request, sess *store.Session) bool {
	if s.Measures == nil {
		s.fail(w, r, http.StatusConflict, "this server has no FHIR store (start it with -fhir); paste resources to evaluate a measure over them")
		return false
	}
	if !wholeServerAccount(sess) {
		s.fail(w, r, http.StatusForbidden, "the FHIR store belongs to the whole server; an account limited to a tenant or to channel "+
			"groups may evaluate measures only over resources it pastes")
		return false
	}
	return true
}

func (s *Server) measureError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, fhirserver.ErrMeasureNotFound):
		s.fail(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, measure.ErrUnsupported):
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, measure.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, err.Error())
	default:
		s.failErr(w, r, err)
	}
}

func (s *Server) handleMeasureGet(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.measuresReady(w, r, sess) {
		return
	}
	raw, err := s.Measures.MeasureJSON(r.Context(), r.PathValue("id"))
	if err != nil {
		s.measureError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	_, _ = w.Write(raw)
}

type measureIn struct {
	Measure json.RawMessage `json:"measure"`
}

func (s *Server) handleMeasurePut(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.measuresReady(w, r, sess) {
		return
	}
	var in measureIn
	if !s.decode(w, r, &in) {
		return
	}
	id, err := s.Measures.PutMeasure(r.Context(), in.Measure)
	if err != nil {
		s.measureError(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "measure.save", Target: "Measure/" + id, IP: clientIP(r)})
	s.ok(w, map[string]string{"saved": id})
}

func (s *Server) handleMeasureDelete(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.measuresReady(w, r, sess) {
		return
	}
	id := r.PathValue("id")
	if err := s.Measures.DeleteMeasure(r.Context(), id); err != nil {
		s.measureError(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "measure.delete", Target: "Measure/" + id, IP: clientIP(r)})
	s.ok(w, map[string]string{"deleted": id})
}

type measureEvalIn struct {
	// ID names a stored Measure; Measure carries one inline instead (an example, or one being edited).
	ID          string          `json:"id"`
	Measure     json.RawMessage `json:"measure"`
	PeriodStart string          `json:"periodStart"`
	PeriodEnd   string          `json:"periodEnd"`
	ReportType  string          `json:"reportType"`
	Subject     string          `json:"subject"`
	// Resources, when set, are evaluated instead of the FHIR store: NDJSON, a Bundle or a JSON array.
	Resources string `json:"resources"`
}

func (s *Server) handleMeasureEvaluate(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var in measureEvalIn
	if !s.decode(w, r, &in) {
		return
	}
	var inline map[string]any
	if len(bytes.TrimSpace(in.Measure)) > 0 && string(bytes.TrimSpace(in.Measure)) != "null" {
		d := json.NewDecoder(bytes.NewReader(in.Measure))
		d.UseNumber()
		if err := d.Decode(&inline); err != nil {
			s.fail(w, r, http.StatusBadRequest, "the measure is not JSON: "+err.Error())
			return
		}
	}
	if strings.TrimSpace(in.Resources) != "" {
		if inline == nil {
			if !s.measuresReady(w, r, sess) {
				return
			}
			raw, err := s.Measures.MeasureJSON(r.Context(), in.ID)
			if err != nil {
				s.measureError(w, r, err)
				return
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			_ = d.Decode(&inline)
		}
		report, err := evaluatePasted(inline, in)
		if err != nil {
			s.measureError(w, r, err)
			return
		}
		s.ok(w, map[string]any{"report": report, "source": "pasted"})
		return
	}
	if !s.measuresReady(w, r, sess) {
		return
	}
	if inline == nil && in.ID == "" {
		s.fail(w, r, http.StatusBadRequest, "choose a measure")
		return
	}
	report, err := s.Measures.EvaluateMeasure(r.Context(), fhirserver.EvaluateMeasureRequest{MeasureID: in.ID, Measure: inline,
		PeriodStart: in.PeriodStart, PeriodEnd: in.PeriodEnd, ReportType: in.ReportType, Subject: in.Subject}, nil)
	if err != nil {
		s.measureError(w, r, err)
		return
	}
	target := in.ID
	if target == "" {
		target, _ = inline["id"].(string)
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "measure.evaluate", Target: "Measure/" + target,
		Detail: strings.TrimSpace(in.PeriodStart + ".." + in.PeriodEnd + " " + in.Subject), IP: clientIP(r)})
	s.ok(w, map[string]any{"report": report, "source": "store"})
}

// maxPastedResources bounds an evaluation over pasted resources.
const maxPastedResources = 200_000

var errTooManyResources = errors.New("too many resources")

func evaluatePasted(m map[string]any, in measureEvalIn) (map[string]any, error) {
	compiled, err := measure.Compile(m)
	if err != nil {
		return nil, err
	}
	period, err := measure.ParsePeriod(in.PeriodStart, in.PeriodEnd)
	if err != nil {
		return nil, errors.Join(measure.ErrInvalid, err)
	}
	var all []map[string]any
	err = sof.ReadResourcesBytes([]byte(in.Resources), func(res map[string]any) error {
		if len(all) >= maxPastedResources {
			return errTooManyResources
		}
		all = append(all, res)
		return nil
	})
	if errors.Is(err, errTooManyResources) {
		return nil, errors.Join(measure.ErrUnsupported, errors.New("paste at most 200,000 resources; load more into the FHIR store"))
	}
	if err != nil {
		return nil, errors.Join(measure.ErrInvalid, err)
	}
	subjects := measure.SubjectsFrom(all)
	if len(subjects) == 0 {
		return nil, errors.Join(measure.ErrInvalid, errors.New("the pasted resources hold no Patient"))
	}
	subject := strings.TrimPrefix(strings.TrimSpace(in.Subject), "Patient/")
	rt := measure.ReportType(in.ReportType)
	if rt == "" {
		rt = measure.Summary
		if subject != "" {
			rt = measure.Individual
		}
	}
	if rt != measure.Summary && rt != measure.Individual && rt != measure.SubjectList {
		return nil, errors.Join(measure.ErrInvalid, errors.New("the report type is summary, subject or subject-list"))
	}
	if rt == measure.Individual && subject == "" {
		return nil, errors.Join(measure.ErrInvalid, errors.New("a subject report needs a patient"))
	}
	ev := measure.NewEvaluator(compiled, period)
	found := false
	for _, sub := range subjects {
		if id, _ := sub.Patient["id"].(string); subject != "" && id != subject {
			continue
		}
		found = true
		if err := ev.Add(sub); err != nil {
			return nil, errors.Join(measure.ErrInvalid, err)
		}
	}
	if !found {
		return nil, errors.Join(fhirserver.ErrMeasureNotFound, errors.New("no pasted Patient/"+subject))
	}
	return ev.Report(measure.Options{Type: rt, Subject: "Patient/" + subject}), nil
}
