// Package measure evaluates FHIR Measures (digital quality measures) whose population criteria are FHIRPath, and writes the
// result as a FHIR MeasureReport: summary, individual or subject-list, with stratifiers and a proportion score.
//
// Each criterion is evaluated once per patient, with the focus a Bundle of the patient's record (their compartment) and
// these variables: %subject (the Patient), %periodStart and %periodEnd (the measurement period, as DateTimes) and
// %"Measurement Period" (the same as a Period). Criteria in CQL are refused with the reason: Perfuse has no CQL engine.
package measure

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhirpath"
)

// Population codes (http://terminology.hl7.org/CodeSystem/measure-population).
const (
	InitialPopulation    = "initial-population"
	Denominator          = "denominator"
	DenominatorExclusion = "denominator-exclusion"
	DenominatorException = "denominator-exception"
	Numerator            = "numerator"
	NumeratorExclusion   = "numerator-exclusion"
)

const populationSystem = "http://terminology.hl7.org/CodeSystem/measure-population"

// ErrUnsupported is a Measure this evaluator cannot run (CQL criteria, continuous-variable scoring), with the reason.
var ErrUnsupported = errors.New("the measure cannot be evaluated here")

// ErrInvalid is a Measure that is not well formed.
var ErrInvalid = errors.New("the measure is not valid")

type population struct {
	code string
	expr *fhirpath.Expression
}

type stratifier struct {
	id   string
	code map[string]any
	expr *fhirpath.Expression
}

type group struct {
	id          string
	code        map[string]any
	pops        []population
	stratifiers []stratifier
}

// Measure is a compiled Measure.
type Measure struct {
	ID, URL, Version, Name, Title, Scoring string
	Improvement                            map[string]any
	groups                                 []group
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, args...))
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func coding(cc any) (system, code string) {
	m, _ := cc.(map[string]any)
	list, _ := m["coding"].([]any)
	for _, c := range list {
		cm, _ := c.(map[string]any)
		s, _ := cm["system"].(string)
		k, _ := cm["code"].(string)
		if k != "" {
			return s, k
		}
	}
	return "", ""
}

func compileExpr(e any, where string) (*fhirpath.Expression, error) {
	em, ok := e.(map[string]any)
	if !ok {
		return nil, invalid("%s has no criteria", where)
	}
	lang, _ := em["language"].(string)
	src, _ := em["expression"].(string)
	switch lang {
	case "text/fhirpath":
	case "text/cql", "text/cql-identifier", "text/cql.identifier", "text/cql-expression":
		return nil, unsupported("%s is written in CQL, and Perfuse has no CQL engine: write the criteria as text/fhirpath", where)
	default:
		return nil, unsupported("%s is in %q; only text/fhirpath criteria are evaluated", where, lang)
	}
	if strings.TrimSpace(src) == "" {
		return nil, invalid("%s has an empty expression", where)
	}
	x, err := fhirpath.Parse(src)
	if err != nil {
		return nil, invalid("%s: %v", where, err)
	}
	return x, nil
}

// Compile reads a Measure resource (decoded with UseNumber).
func Compile(m map[string]any) (*Measure, error) {
	if m["resourceType"] != "Measure" {
		return nil, invalid("the resource is a %v, not a Measure", m["resourceType"])
	}
	out := &Measure{}
	out.ID, _ = m["id"].(string)
	out.URL, _ = m["url"].(string)
	out.Version, _ = m["version"].(string)
	out.Name, _ = m["name"].(string)
	out.Title, _ = m["title"].(string)
	_, out.Scoring = coding(m["scoring"])
	if out.Improvement, _ = m["improvementNotation"].(map[string]any); out.Improvement == nil {
		out.Improvement = nil
	}
	groups, _ := m["group"].([]any)
	if len(groups) == 0 {
		return nil, invalid("the measure has no group")
	}
	for gi, g := range groups {
		gm, _ := g.(map[string]any)
		grp := group{}
		grp.id, _ = gm["id"].(string)
		grp.code, _ = gm["code"].(map[string]any)
		// An R5 Measure may score per group.
		if _, s := coding(gm["scoring"]); s != "" && out.Scoring == "" {
			out.Scoring = s
		}
		pops, _ := gm["population"].([]any)
		seen := map[string]bool{}
		for pi, p := range pops {
			pm, _ := p.(map[string]any)
			_, code := coding(pm["code"])
			switch code {
			case InitialPopulation, Denominator, DenominatorExclusion, DenominatorException, Numerator, NumeratorExclusion:
			case "measure-population", "measure-population-exclusion", "measure-observation":
				return nil, unsupported("group %d uses %s, which is continuous-variable scoring", gi+1, code)
			default:
				return nil, invalid("group %d population %d has code %q, not a measure-population code", gi+1, pi+1, code)
			}
			if seen[code] {
				return nil, invalid("group %d has two %s populations", gi+1, code)
			}
			seen[code] = true
			x, err := compileExpr(pm["criteria"], fmt.Sprintf("group %d %s", gi+1, code))
			if err != nil {
				return nil, err
			}
			grp.pops = append(grp.pops, population{code: code, expr: x})
		}
		if !seen[InitialPopulation] {
			return nil, invalid("group %d has no initial-population", gi+1)
		}
		strats, _ := gm["stratifier"].([]any)
		for si, s := range strats {
			sm, _ := s.(map[string]any)
			st := stratifier{}
			st.id, _ = sm["id"].(string)
			st.code, _ = sm["code"].(map[string]any)
			if _, ok := sm["component"]; ok {
				return nil, unsupported("group %d stratifier %d has components; only single-criteria stratifiers are evaluated", gi+1, si+1)
			}
			x, err := compileExpr(sm["criteria"], fmt.Sprintf("group %d stratifier %d", gi+1, si+1))
			if err != nil {
				return nil, err
			}
			st.expr = x
			grp.stratifiers = append(grp.stratifiers, st)
		}
		out.groups = append(out.groups, grp)
	}
	switch out.Scoring {
	case "", "proportion":
		out.Scoring = "proportion"
		for gi, g := range out.groups {
			has := map[string]bool{}
			for _, p := range g.pops {
				has[p.code] = true
			}
			if !has[Denominator] || !has[Numerator] {
				return nil, invalid("group %d is proportion-scored and needs a denominator and a numerator", gi+1)
			}
		}
	case "cohort":
	case "ratio":
	case "continuous-variable":
		return nil, unsupported("continuous-variable scoring is not evaluated; proportion, ratio and cohort are")
	default:
		return nil, invalid("scoring %q is not a measure-scoring code", out.Scoring)
	}
	return out, nil
}

// Period is the measurement period.
type Period struct{ Start, End time.Time }

// ParsePeriod reads periodStart and periodEnd (dates or date-times); a date end means the whole day.
func ParsePeriod(start, end string) (Period, error) {
	parse := func(s string, endOfDay bool) (time.Time, error) {
		s = strings.TrimSpace(s)
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, nil
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			if endOfDay {
				return t.Add(24*time.Hour - time.Millisecond), nil
			}
			return t, nil
		}
		return time.Time{}, fmt.Errorf("%q is not a date (2026-01-01) or a date-time", s)
	}
	if start == "" || end == "" {
		return Period{}, errors.New("periodStart and periodEnd are both required")
	}
	s, err := parse(start, false)
	if err != nil {
		return Period{}, fmt.Errorf("periodStart: %w", err)
	}
	e, err := parse(end, true)
	if err != nil {
		return Period{}, fmt.Errorf("periodEnd: %w", err)
	}
	if e.Before(s) {
		return Period{}, errors.New("periodEnd is before periodStart")
	}
	return Period{Start: s, End: e}, nil
}

func dtItem(t time.Time) fhirpath.Item {
	d, _ := fhirpath.ParseDateTime(t.UTC().Format("2006-01-02T15:04:05.000Z"), "DateTime")
	return fhirpath.Item{V: d, T: "dateTime"}
}

// Subject is one patient's record: the Patient and every resource in their compartment.
type Subject struct {
	Patient   map[string]any
	Resources []map[string]any
}

// patientResult is where one patient fell.
type patientResult struct {
	ref    string
	in     map[string]bool
	strata []string
}

// Evaluator accumulates patients for one measure and period.
type Evaluator struct {
	m      *Measure
	period Period
	// results per group
	results [][]patientResult
}

// NewEvaluator starts an evaluation.
func NewEvaluator(m *Measure, p Period) *Evaluator {
	return &Evaluator{m: m, period: p, results: make([][]patientResult, len(m.groups))}
}

func (e *Evaluator) env(subj fhirpath.Collection) *fhirpath.Env {
	start, end := dtItem(e.period.Start), dtItem(e.period.End)
	return &fhirpath.Env{Vars: map[string]fhirpath.Collection{
		"subject":     subj,
		"periodStart": {start},
		"periodEnd":   {end},
		"Measurement Period": {{V: map[string]any{
			"start": e.period.Start.UTC().Format(time.RFC3339), "end": e.period.End.UTC().Format(time.RFC3339)}, T: "Period"}},
	}}
}

func truth(c fhirpath.Collection) (bool, error) {
	switch len(c) {
	case 0:
		return false, nil
	case 1:
		if b, ok := c[0].V.(bool); ok {
			return b, nil
		}
		return true, nil // a single value that exists counts as true, as FHIRPath's singleton evaluation does
	}
	return false, fmt.Errorf("gave %d values, not one Boolean", len(c))
}

// Add evaluates one patient.
func (e *Evaluator) Add(s Subject) error {
	id, _ := s.Patient["id"].(string)
	ref := "Patient/" + id
	entries := make([]any, 0, len(s.Resources)+1)
	entries = append(entries, map[string]any{"resource": s.Patient})
	for _, r := range s.Resources {
		if rt, _ := r["resourceType"].(string); rt == "Patient" {
			if rid, _ := r["id"].(string); rid == id {
				continue
			}
		}
		entries = append(entries, map[string]any{"resource": r})
	}
	bundle := fhirpath.Collection{fhirpath.FromJSON(map[string]any{"resourceType": "Bundle", "type": "collection", "entry": entries}, "")}
	env := e.env(fhirpath.Collection{fhirpath.FromJSON(s.Patient, "")})
	for gi, g := range e.m.groups {
		raw := map[string]bool{}
		for _, p := range g.pops {
			v, err := p.expr.Evaluate(bundle, env)
			if err != nil {
				return fmt.Errorf("%s, group %d %s: %v", ref, gi+1, p.code, err)
			}
			b, err := truth(v)
			if err != nil {
				return fmt.Errorf("%s, group %d %s %s", ref, gi+1, p.code, err)
			}
			raw[p.code] = b
		}
		in := membership(e.m.Scoring, raw)
		if !in[InitialPopulation] {
			e.results[gi] = append(e.results[gi], patientResult{ref: ref, in: in})
			continue
		}
		var strata []string
		for si, st := range g.stratifiers {
			v, err := st.expr.Evaluate(bundle, env)
			if err != nil {
				return fmt.Errorf("%s, group %d stratifier %d: %v", ref, gi+1, si+1, err)
			}
			strata = append(strata, stratumValue(v))
		}
		e.results[gi] = append(e.results[gi], patientResult{ref: ref, in: in, strata: strata})
	}
	return nil
}

func stratumValue(v fhirpath.Collection) string {
	if len(v) == 0 {
		return "(none)"
	}
	var parts []string
	for _, it := range v {
		switch x := fhirpath.ToJSON(it).(type) {
		case string:
			parts = append(parts, x)
		default:
			b, _ := json.Marshal(x)
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, ", ")
}

// membership applies the population rules: each population is a subset of the one it qualifies.
func membership(scoring string, raw map[string]bool) map[string]bool {
	in := map[string]bool{}
	ip := raw[InitialPopulation]
	in[InitialPopulation] = ip
	if scoring == "cohort" || !ip {
		return in
	}
	if scoring == "ratio" {
		in[Denominator] = raw[Denominator]
		in[DenominatorExclusion] = in[Denominator] && raw[DenominatorExclusion]
		in[Numerator] = raw[Numerator]
		in[NumeratorExclusion] = in[Numerator] && raw[NumeratorExclusion]
		return in
	}
	den := raw[Denominator]
	in[Denominator] = den
	denEx := den && raw[DenominatorExclusion]
	in[DenominatorExclusion] = denEx
	num := den && !denEx && raw[Numerator]
	in[Numerator] = num
	in[NumeratorExclusion] = num && raw[NumeratorExclusion]
	in[DenominatorException] = den && !denEx && !num && raw[DenominatorException]
	return in
}

// ReportType is summary, subject (individual) or subject-list.
type ReportType string

// The report types.
const (
	Summary     ReportType = "summary"
	Individual  ReportType = "subject"
	SubjectList ReportType = "subject-list"
)

func counts(rs []patientResult, codes []string) map[string][]string {
	out := map[string][]string{}
	for _, r := range rs {
		for _, c := range codes {
			if r.in[c] {
				out[c] = append(out[c], r.ref)
			}
		}
	}
	return out
}

// score is the proportion (numerator - numerator exclusions) / (denominator - exclusions - exceptions), or for ratio
// numerator / denominator; nil when the denominator is empty.
func score(scoring string, c map[string][]string) *big.Rat {
	n := len(c[Numerator]) - len(c[NumeratorExclusion])
	d := len(c[Denominator]) - len(c[DenominatorExclusion])
	if scoring == "proportion" {
		d -= len(c[DenominatorException])
	}
	if d <= 0 {
		return nil
	}
	return big.NewRat(int64(n), int64(d))
}

func ratNumber(r *big.Rat) json.Number {
	s := r.FloatString(6)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return json.Number(s)
}

// Options shape the report.
type Options struct {
	Type ReportType
	// Subject is the patient of an individual report.
	Subject string
	// R5 writes measureScoreQuantity (FHIR R5) instead of measureScore (R4).
	R5 bool
	// Now is the report's date; zero uses the time.
	Now time.Time
	// Reporter is the Organization reporting (Organization/{id}); empty contains one named ReporterName.
	Reporter, ReporterName string
}

const (
	scoringSystem    = "http://terminology.hl7.org/CodeSystem/measure-scoring"
	scoringExtension = "http://hl7.org/fhir/us/davinci-deqm/StructureDefinition/extension-measureScoring"
)

// Report writes the MeasureReport.
func (e *Evaluator) Report(o Options) map[string]any {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	// DEQM (deqm-0) wants the canonical with its version.
	measureRef := e.m.URL
	if measureRef == "" {
		measureRef = "Measure/" + e.m.ID
	} else if e.m.Version != "" {
		measureRef += "|" + e.m.Version
	}
	typ := map[ReportType]string{Summary: "summary", Individual: "individual", SubjectList: "subject-list"}[o.Type]
	if typ == "" {
		typ = "summary"
	}
	rep := map[string]any{
		"resourceType": "MeasureReport",
		"status":       "complete",
		"type":         typ,
		"measure":      measureRef,
		"date":         o.Now.UTC().Format(time.RFC3339),
		"period":       map[string]any{"start": e.period.Start.UTC().Format(time.RFC3339), "end": e.period.End.UTC().Format(time.RFC3339)},
	}
	if o.Type == Individual && o.Subject != "" {
		rep["subject"] = map[string]any{"reference": o.Subject}
	}
	// The scoring, restated on the report (DEQM extension-measureScoring, once at the root).
	rep["extension"] = []any{map[string]any{"url": scoringExtension, "valueCodeableConcept": map[string]any{
		"coding": []any{map[string]any{"system": scoringSystem, "code": e.m.Scoring}}}}}
	var contained []any
	if o.Reporter != "" {
		rep["reporter"] = map[string]any{"reference": o.Reporter}
	} else {
		name := o.ReporterName
		if name == "" {
			name = "Perfuse"
		}
		contained = append(contained, map[string]any{"resourceType": "Organization", "id": "reporter", "active": true, "name": name})
		rep["reporter"] = map[string]any{"reference": "#reporter", "display": name}
	}
	if e.m.Improvement != nil {
		rep["improvementNotation"] = e.m.Improvement
	}
	order := []string{InitialPopulation, Denominator, DenominatorExclusion, DenominatorException, Numerator, NumeratorExclusion}
	var groups []any
	for gi, g := range e.m.groups {
		present := map[string]bool{}
		for _, p := range g.pops {
			present[p.code] = true
		}
		var codes []string
		for _, c := range order {
			if present[c] {
				codes = append(codes, c)
			}
		}
		rs := e.results[gi]
		c := counts(rs, codes)
		popList := func(c map[string][]string, listPrefix string) []any {
			var out []any
			for _, code := range codes {
				p := map[string]any{
					"code":  map[string]any{"coding": []any{map[string]any{"system": populationSystem, "code": code}}},
					"count": len(c[code]),
				}
				// A subject-list report names every population's patients, an empty population by an empty List.
				if o.Type == SubjectList && listPrefix != "" {
					id := fmt.Sprintf("%s-%s", listPrefix, code)
					list := map[string]any{"resourceType": "List", "id": id, "status": "current", "mode": "snapshot"}
					var entries []any
					for _, ref := range c[code] {
						entries = append(entries, map[string]any{"item": map[string]any{"reference": ref}})
					}
					if len(entries) > 0 {
						list["entry"] = entries
					} else {
						list["emptyReason"] = map[string]any{"coding": []any{map[string]any{
							"system": "http://terminology.hl7.org/CodeSystem/list-empty-reason", "code": "nilknown"}}}
					}
					contained = append(contained, list)
					p["subjectResults"] = map[string]any{"reference": "#" + id}
				}
				out = append(out, p)
			}
			return out
		}
		gr := map[string]any{"population": popList(c, fmt.Sprintf("g%d", gi+1))}
		if g.id != "" {
			gr["id"] = g.id
		}
		if g.code != nil {
			gr["code"] = g.code
		}
		scoreField := "measureScore"
		if o.R5 {
			scoreField = "measureScoreQuantity"
		}
		if e.m.Scoring != "cohort" {
			if s := score(e.m.Scoring, c); s != nil {
				gr[scoreField] = map[string]any{"value": ratNumber(s)}
			}
		}
		var strats []any
		for si, st := range g.stratifiers {
			byValue := map[string][]patientResult{}
			for _, r := range rs {
				if si < len(r.strata) {
					byValue[r.strata[si]] = append(byValue[r.strata[si]], r)
				}
			}
			values := make([]string, 0, len(byValue))
			for v := range byValue {
				values = append(values, v)
			}
			sort.Strings(values)
			var strata []any
			for vi, v := range values {
				sc := counts(byValue[v], codes)
				stratum := map[string]any{"value": map[string]any{"text": v},
					"population": popList(sc, fmt.Sprintf("g%d-s%d-%d", gi+1, si+1, vi+1))}
				if e.m.Scoring != "cohort" {
					if s := score(e.m.Scoring, sc); s != nil {
						stratum[scoreField] = map[string]any{"value": ratNumber(s)}
					}
				}
				strata = append(strata, stratum)
			}
			sm := map[string]any{"stratum": strata}
			// The Measure's own code, unchanged: the report's stratifier is matched to the Measure's by it.
			if st.code != nil {
				sm["code"] = []any{st.code}
			}
			if st.id != "" {
				sm["id"] = st.id
			}
			if len(strata) == 0 {
				delete(sm, "stratum")
			}
			strats = append(strats, sm)
		}
		if len(strats) > 0 {
			gr["stratifier"] = strats
		}
		groups = append(groups, gr)
	}
	rep["group"] = groups
	if len(contained) > 0 {
		rep["contained"] = contained
	}
	return rep
}

// Populations summarises a report's first group for a screen: count per population and the score.
func (e *Evaluator) Patients() int {
	if len(e.results) == 0 {
		return 0
	}
	return len(e.results[0])
}

// SubjectsFrom groups loose resources by patient: each Patient with every resource whose subject or patient names it.
// Resources naming no stored Patient are left out.
func SubjectsFrom(resources []map[string]any) []Subject {
	byID := map[string]*Subject{}
	var order []string
	for _, r := range resources {
		if r["resourceType"] == "Patient" {
			id, _ := r["id"].(string)
			if id == "" || byID[id] != nil {
				continue
			}
			byID[id] = &Subject{Patient: r}
			order = append(order, id)
		}
	}
	for _, r := range resources {
		if r["resourceType"] == "Patient" {
			continue
		}
		for _, field := range []string{"subject", "patient", "beneficiary"} {
			ref, _ := r[field].(map[string]any)
			s, _ := ref["reference"].(string)
			if id, ok := strings.CutPrefix(s, "Patient/"); ok && byID[id] != nil {
				byID[id].Resources = append(byID[id].Resources, r)
				break
			}
		}
	}
	out := make([]Subject, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}
