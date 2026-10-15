package fhirserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/measure"
)

// Measure/$evaluate-measure (FHIR R4): evaluate a stored Measure whose population criteria are FHIRPath over this server's
// patients for a measurement period, and answer with a MeasureReport (summary, subject or subject-list). The report is
// returned, not stored. Measures in CQL are refused with the reason.

// maxMeasurePatients bounds one evaluation.
const maxMeasurePatients = 100_000

// EvaluateMeasureRequest is one evaluation.
type EvaluateMeasureRequest struct {
	// MeasureID or MeasureURL names the stored Measure; Measure carries one inline instead.
	MeasureID, MeasureURL string
	Measure               map[string]any
	PeriodStart           string
	PeriodEnd             string
	// ReportType is summary, subject or subject-list; subject needs Subject.
	ReportType string
	// Subject is Patient/{id}: evaluate that patient only.
	Subject string
	// Reporter is the reporting Organization (Organization/{id}); empty names this server's ReporterName.
	Reporter string
}

// ErrMeasureNotFound is a Measure this server does not hold.
var ErrMeasureNotFound = errors.New("no such Measure is stored here")

// EvaluateMeasure runs a measure for the console and the operation. caller nil is the console, which has checked access.
func (s *Server) EvaluateMeasure(ctx context.Context, req EvaluateMeasureRequest, caller *Caller) (map[string]any, error) {
	m := req.Measure
	if m == nil {
		raw, err := s.findMeasure(ctx, req.MeasureID, req.MeasureURL)
		if err != nil {
			return nil, err
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			return nil, err
		}
	}
	compiled, err := measure.Compile(m)
	if err != nil {
		return nil, err
	}
	period, err := measure.ParsePeriod(req.PeriodStart, req.PeriodEnd)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", measure.ErrInvalid, err)
	}
	rt := measure.ReportType(req.ReportType)
	switch rt {
	case "":
		rt = measure.Summary
		if req.Subject != "" {
			rt = measure.Individual
		}
	case "population":
		rt = measure.Summary
	case measure.Summary, measure.Individual, measure.SubjectList:
	default:
		return nil, fmt.Errorf("%w: reportType is summary, subject or subject-list, not %q", measure.ErrInvalid, req.ReportType)
	}
	subject := strings.TrimPrefix(strings.TrimSpace(req.Subject), "Patient/")
	if rt == measure.Individual && subject == "" {
		return nil, fmt.Errorf("%w: a subject report needs subject=Patient/{id}", measure.ErrInvalid)
	}
	if caller != nil && caller.Patient != "" {
		if subject != "" && subject != caller.Patient {
			return nil, errPatientScope
		}
		subject = caller.Patient
		if rt != measure.Individual {
			rt = measure.Individual
		}
	}
	var ids []string
	if subject != "" {
		ids = []string{subject}
	} else {
		rows, err := s.Store.db.QueryContext(ctx, `SELECT resource_id FROM fhir_resources WHERE resource_type = 'Patient' AND deleted = 0 ORDER BY resource_id`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) > maxMeasurePatients {
			return nil, fmt.Errorf("%w: this server holds %d patients and one evaluation reads at most %d; evaluate per subject",
				measure.ErrUnsupported, len(ids), maxMeasurePatients)
		}
	}
	ev := measure.NewEvaluator(compiled, period)
	for _, id := range ids {
		subj, err := s.measureSubject(ctx, id, caller)
		if err != nil {
			if errors.Is(err, ErrNotFound) && subject != "" {
				return nil, fmt.Errorf("%w: Patient/%s", ErrMeasureNotFound, subject)
			}
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if err := ev.Add(subj); err != nil {
			return nil, fmt.Errorf("%w: %v", measure.ErrInvalid, err)
		}
	}
	report := ev.Report(measure.Options{Type: rt, Subject: "Patient/" + subject, R5: s.Store.Version() == fhir.R5,
		Reporter: req.Reporter, ReporterName: s.MeasureReporter})
	if s.Log != nil {
		s.Log.Info("fhir $evaluate-measure", "measure", compiled.URL+compiled.ID, "patients", len(ids), "type", string(rt),
			"caller", callerName(caller))
	}
	return report, nil
}

var errPatientScope = errors.New("this token may evaluate only its own patient")

func (s *Server) findMeasure(ctx context.Context, id, canonical string) ([]byte, error) {
	if id != "" {
		res, err := s.Store.Get(ctx, "Measure", id)
		if err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrDeleted) {
				return nil, fmt.Errorf("%w: Measure/%s", ErrMeasureNotFound, id)
			}
			return nil, err
		}
		return fhir.Marshal(res, s.Store.Version())
	}
	if canonical == "" {
		return nil, fmt.Errorf("%w: name the Measure (Measure/{id}/$evaluate-measure, or measure=its url)", measure.ErrInvalid)
	}
	u, version, _ := strings.Cut(canonical, "|")
	rows, err := s.Store.db.QueryContext(ctx, `SELECT content FROM fhir_resources WHERE resource_type = 'Measure' AND deleted = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		var head struct{ URL, Version string }
		if json.Unmarshal([]byte(content), &head) == nil && head.URL == u && (version == "" || version == head.Version) {
			return []byte(content), nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrMeasureNotFound, canonical)
}

// measureSubject is a patient and their compartment as JSON, in the version this server speaks.
func (s *Server) measureSubject(ctx context.Context, id string, caller *Caller) (measure.Subject, error) {
	res, err := s.Store.Everything(ctx, &EverythingRequest{PatientID: id}, caller)
	if err != nil {
		return measure.Subject{}, err
	}
	toMap := func(r fhir.Resource) map[string]any {
		raw, err := fhir.MarshalVersioned(r, s.Store.Version())
		if err != nil {
			return nil
		}
		var m map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&m) != nil {
			return nil
		}
		return m
	}
	var subj measure.Subject
	for _, r := range res.Resources {
		m := toMap(r)
		if m == nil {
			continue
		}
		if m["resourceType"] == "Patient" && m["id"] == id {
			subj.Patient = m
			continue
		}
		subj.Resources = append(subj.Resources, m)
	}
	if subj.Patient == nil {
		return measure.Subject{}, ErrNotFound
	}
	return subj, nil
}

func (s *Server) handleEvaluateMeasure(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code, msg string) { s.writeOutcome(w, r, status, fhir.SeverityError, code, msg) }
	req := EvaluateMeasureRequest{MeasureID: r.PathValue("id")}
	vals := url.Values{}
	for k, v := range r.URL.Query() {
		vals[k] = v
	}
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			fail(http.StatusBadRequest, "invalid", "the request body could not be read")
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			var p map[string]any
			d := json.NewDecoder(bytes.NewReader(body))
			d.UseNumber()
			if err := d.Decode(&p); err != nil || p["resourceType"] != "Parameters" {
				fail(http.StatusBadRequest, "invalid", "a POST to $evaluate-measure carries a Parameters resource")
				return
			}
			list, _ := p["parameter"].([]any)
			for _, x := range list {
				pm, _ := x.(map[string]any)
				name, _ := pm["name"].(string)
				if name == "measure" {
					if res, ok := pm["resource"].(map[string]any); ok {
						req.Measure = res
						continue
					}
				}
				for k, v := range pm {
					if !strings.HasPrefix(k, "value") {
						continue
					}
					switch x := v.(type) {
					case string:
						vals.Add(name, x)
					case map[string]any:
						if ref, ok := x["reference"].(string); ok {
							vals.Add(name, ref)
						}
					}
				}
			}
		}
	}
	for k, v := range vals {
		if len(v) == 0 {
			continue
		}
		switch k {
		case "periodStart":
			req.PeriodStart = v[0]
		case "periodEnd":
			req.PeriodEnd = v[0]
		case "reportType":
			req.ReportType = v[0]
		case "subject":
			if !strings.HasPrefix(v[0], "Patient/") && strings.Contains(v[0], "/") {
				fail(http.StatusBadRequest, "not-supported", "subject is a Patient here; Group and Practitioner subjects are not evaluated")
				return
			}
			req.Subject = v[0]
		case "measure":
			req.MeasureURL = v[0]
		case "reporter":
			if !strings.HasPrefix(v[0], "Organization/") {
				fail(http.StatusBadRequest, "invalid", "reporter is an Organization reference (Organization/{id})")
				return
			}
			req.Reporter = v[0]
		case "_format":
		case "practitioner", "lastReceivedOn", "provider", "location", "productLine":
			fail(http.StatusBadRequest, "not-supported", k+" is not supported by this server's $evaluate-measure")
			return
		}
	}
	caller := CallerFrom(r.Context())
	if caller != nil && (!caller.Allows("Measure", false) || !caller.Allows("Patient", false)) {
		fail(http.StatusForbidden, "forbidden", "evaluating a measure needs read access to Measure and to the patients' records")
		return
	}
	report, err := s.EvaluateMeasure(r.Context(), req, caller)
	switch {
	case err == nil:
	case errors.Is(err, errPatientScope):
		fail(http.StatusForbidden, "forbidden", err.Error())
		return
	case errors.Is(err, ErrMeasureNotFound):
		fail(http.StatusNotFound, "not-found", err.Error())
		return
	case errors.Is(err, measure.ErrUnsupported):
		fail(http.StatusUnprocessableEntity, "not-supported", err.Error())
		return
	case errors.Is(err, measure.ErrInvalid):
		fail(http.StatusBadRequest, "invalid", err.Error())
		return
	default:
		fail(http.StatusInternalServerError, "exception", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// MeasureSummary is one stored Measure, for the console.
type MeasureSummary struct {
	ID          string `json:"id"`
	URL         string `json:"url,omitempty"`
	Version     string `json:"version,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Scoring     string `json:"scoring,omitempty"`
	// Problem is why this server cannot evaluate it (CQL criteria, say); empty when it can.
	Problem string `json:"problem,omitempty"`
}

// ListMeasures is every stored Measure, with whether it can be evaluated.
func (s *Server) ListMeasures(ctx context.Context) ([]MeasureSummary, error) {
	rows, err := s.Store.db.QueryContext(ctx, `SELECT content FROM fhir_resources WHERE resource_type = 'Measure' AND deleted = 0 ORDER BY resource_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MeasureSummary{}
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(content))
		d.UseNumber()
		if d.Decode(&m) != nil {
			continue
		}
		sum := MeasureSummary{}
		sum.ID, _ = m["id"].(string)
		sum.URL, _ = m["url"].(string)
		sum.Version, _ = m["version"].(string)
		sum.Title, _ = m["title"].(string)
		if sum.Title == "" {
			sum.Title, _ = m["name"].(string)
		}
		sum.Description, _ = m["description"].(string)
		if c, err := measure.Compile(m); err != nil {
			sum.Problem = err.Error()
		} else {
			sum.Scoring = c.Scoring
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

// PutMeasure stores a Measure after checking that it compiles. It returns the stored id.
func (s *Server) PutMeasure(ctx context.Context, raw []byte) (string, error) {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return "", fmt.Errorf("%w: %v", measure.ErrInvalid, err)
	}
	if _, err := measure.Compile(m); err != nil {
		return "", err
	}
	id, _ := m["id"].(string)
	if !validID(id) {
		return "", fmt.Errorf("%w: the Measure needs an id of letters, digits, '-' and '.' (64 at most)", measure.ErrInvalid)
	}
	res, err := fhir.UnmarshalResource(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", measure.ErrInvalid, err)
	}
	if _, err := s.Store.Put(ctx, res); err != nil {
		return "", err
	}
	return id, nil
}

// DeleteMeasure removes a stored Measure.
func (s *Server) DeleteMeasure(ctx context.Context, id string) error {
	if _, err := s.Store.Get(ctx, "Measure", id); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrDeleted) {
			return fmt.Errorf("%w: Measure/%s", ErrMeasureNotFound, id)
		}
		return err
	}
	return s.Store.Delete(ctx, "Measure", id)
}

// validID is a FHIR id: 1 to 64 of A-Z, a-z, 0-9, '-' and '.'.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// MeasureJSON is a stored Measure as JSON.
func (s *Server) MeasureJSON(ctx context.Context, id string) ([]byte, error) {
	return s.findMeasure(ctx, id, "")
}
