package cms0057

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html/template"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Prior authorisation metrics, as CMS-0057-F requires impacted payers to post publicly every year (42 CFR 422.122(c),
// 438.210(f), 440.230(e)(3), 457.732(c), 457.1230(d), 45 CFR 156.223(c)), beginning in 2026 for calendar year 2025.
//
// The arithmetic follows CMS's "Prior Authorization Metrics Reporting - Overview and Template": percentages of standard and
// expedited requests approved and denied, the share approved after appeal (out of those appealed), the share approved after the
// review time was extended, and the mean and median time from the payer receiving a request to its decision. Turnaround is in
// hours or calendar days with the unit always written, and a median under a day is given in hours rather than rounded to
// "0 days" - which the template states and which is easy to get wrong.
//
// Drugs are excluded, as the rule excludes them. A row whose service is a drug is refused rather than silently counted.

// Decision is one prior authorisation request and what happened to it.
type Decision struct {
	RequestID      string    `json:"requestId"`
	LineOfBusiness string    `json:"lineOfBusiness"`
	Expedited      bool      `json:"expedited"`
	Received       time.Time `json:"received"`
	Decided        time.Time `json:"decided"`
	// Outcome is approved, denied, partial or pending.
	Outcome  string `json:"outcome"`
	Extended bool   `json:"extended"`
	Appealed bool   `json:"appealed"`
	// AppealOutcome is approved, denied or empty for an appeal not yet decided.
	AppealOutcome      string `json:"appealOutcome"`
	ServiceCode        string `json:"serviceCode"`
	ServiceDescription string `json:"serviceDescription"`
}

// Service is one item or service on the payer's list of those requiring prior authorisation.
type Service struct {
	Category    string `json:"category"`
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Count is a numerator over a denominator.
type Count struct {
	Count   int     `json:"count"`
	Of      int     `json:"of"`
	Percent float64 `json:"percent"`
}

func count(n, of int) Count {
	c := Count{Count: n, Of: of}
	if of > 0 {
		c.Percent = math.Round(float64(n)*1000/float64(of)) / 10
	}

	return c
}

// Turnaround is the time from receipt to decision.
type Turnaround struct {
	MeanHours   float64 `json:"meanHours"`
	MedianHours float64 `json:"medianHours"`
	// Mean and Median are written with their unit, as the public report must show them.
	Mean   string `json:"mean"`
	Median string `json:"median"`
}

// Section is the metrics for one priority within one line of business.
type Section struct {
	Requests               int        `json:"requests"`
	Approved               Count      `json:"approved"`
	Denied                 Count      `json:"denied"`
	PartlyApproved         int        `json:"partlyApproved"`
	ApprovedWithinDeadline Count      `json:"approvedWithinDeadline"`
	DeniedWithinDeadline   Count      `json:"deniedWithinDeadline"`
	ApprovedAfterExtension Count      `json:"approvedAfterExtension"`
	DeniedAfterExtension   Count      `json:"deniedAfterExtension"`
	ApprovedAfterAppeal    Count      `json:"approvedAfterAppeal"`
	DeniedAfterAppeal      Count      `json:"deniedAfterAppeal"`
	Turnaround             Turnaround `json:"turnaround"`
	DeadlineLabel          string     `json:"deadlineLabel"`
}

// LineReport is the report for one line of business, which is the level CMS requires metrics at.
type LineReport struct {
	Name      string  `json:"name"`
	Standard  Section `json:"standard"`
	Expedited Section `json:"expedited"`
	// ExtendedAndApproved is the rule's aggregate metric: requests whose review time was extended and that were approved,
	// out of all requests, standard and expedited together.
	ExtendedAndApproved Count `json:"extendedAndApproved"`
}

// MetricsOptions configure a report.
type MetricsOptions struct {
	Year         int
	Organization string
	Contact      string
	// StandardDeadline and ExpeditedDeadline are the decision timeframes the "within" counts are measured against. Zero
	// means CMS-0057's: seven calendar days and 72 hours. QHP issuers on the federal exchanges keep their own 15-day
	// standard timeframe, so they set this.
	StandardDeadline  time.Duration
	ExpeditedDeadline time.Duration
	Services          []Service
}

// MetricsReport is the whole report.
type MetricsReport struct {
	Year         int          `json:"year"`
	Organization string       `json:"organization"`
	Contact      string       `json:"contact"`
	Lines        []LineReport `json:"linesOfBusiness"`
	Services     []Service    `json:"services"`
	// Excluded explains every row left out, by reason, so the totals can be reconciled with the source.
	Excluded map[string]int `json:"excluded"`
	// DataQuality lists problems CMS asks payers to disclose next to the affected metric.
	DataQuality []string `json:"dataQuality"`
}

// BuildMetrics computes the report for a calendar year.
//
// A request belongs to the year it was decided in. One still pending at year end has no decision to count and is listed under
// Excluded rather than counted as anything.
func BuildMetrics(decisions []Decision, opt MetricsOptions) (*MetricsReport, error) {
	if opt.Year < 2000 || opt.Year > 2200 {
		return nil, fmt.Errorf("the reporting year %d is not a calendar year", opt.Year)
	}
	if opt.StandardDeadline == 0 {
		opt.StandardDeadline = 7 * 24 * time.Hour
	}
	if opt.ExpeditedDeadline == 0 {
		opt.ExpeditedDeadline = 72 * time.Hour
	}
	start := time.Date(opt.Year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)

	r := &MetricsReport{Year: opt.Year, Organization: opt.Organization, Contact: opt.Contact, Services: opt.Services,
		Excluded: map[string]int{}}

	type acc struct {
		rows []Decision
	}
	byLine := map[string]*[2]acc{}
	negative := 0
	for _, d := range decisions {
		switch {
		case d.Outcome == "pending" || d.Decided.IsZero():
			r.Excluded["still pending, so not decided in the year"]++
			continue
		case d.Decided.Before(start) || !d.Decided.Before(end):
			r.Excluded[fmt.Sprintf("decided outside %d", opt.Year)]++
			continue
		case d.Received.IsZero():
			r.Excluded["no time received, so no turnaround can be measured"]++
			continue
		case d.Decided.Before(d.Received):
			negative++
			r.Excluded["decided before it was received"]++
			continue
		}
		line := d.LineOfBusiness
		if line == "" {
			line = "All lines of business"
		}
		if byLine[line] == nil {
			byLine[line] = &[2]acc{}
		}
		i := 0
		if d.Expedited {
			i = 1
		}
		byLine[line][i].rows = append(byLine[line][i].rows, d)
	}
	if negative > 0 {
		r.DataQuality = append(r.DataQuality, fmt.Sprintf(
			"%d request(s) record a decision before receipt, which is a clock or data-entry error in the source system; they are excluded from every metric", negative))
	}

	names := make([]string, 0, len(byLine))
	for n := range byLine {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		a := byLine[n]
		lr := LineReport{
			Name:      n,
			Standard:  section(a[0].rows, opt.StandardDeadline),
			Expedited: section(a[1].rows, opt.ExpeditedDeadline),
		}
		total := len(a[0].rows) + len(a[1].rows)
		extApproved := 0
		for _, rows := range [][]Decision{a[0].rows, a[1].rows} {
			for _, d := range rows {
				if d.Extended && approvedOutcome(d.Outcome) {
					extApproved++
				}
			}
		}
		lr.ExtendedAndApproved = count(extApproved, total)
		r.Lines = append(r.Lines, lr)
	}

	if len(r.Lines) == 0 {
		return nil, fmt.Errorf("no prior authorisation was decided in %d, so there is nothing to report", opt.Year)
	}
	if len(opt.Services) == 0 {
		r.DataQuality = append(r.DataQuality,
			"the list of items and services requiring prior authorisation is missing; CMS requires it on the same page, "+
				"with plain-language descriptions rather than codes alone")
	} else {
		bare := 0
		for _, s := range opt.Services {
			if strings.TrimSpace(s.Description) == "" {
				bare++
			}
		}
		if bare > 0 {
			r.DataQuality = append(r.DataQuality, fmt.Sprintf(
				"%d service(s) on the prior authorisation list have a code but no description; CMS does not accept a list of codes alone", bare))
		}
	}

	return r, nil
}

func approvedOutcome(o string) bool { return o == "approved" || o == "partial" }

func section(rows []Decision, deadline time.Duration) Section {
	s := Section{Requests: len(rows), DeadlineLabel: humanDuration(deadline)}
	var approved, denied, partial, inApproved, inDenied, extApproved, extDenied, appealed, appealApproved, appealDenied int
	hours := make([]float64, 0, len(rows))
	for _, d := range rows {
		took := d.Decided.Sub(d.Received)
		hours = append(hours, took.Hours())
		within := took <= deadline
		switch {
		case approvedOutcome(d.Outcome):
			approved++
			if d.Outcome == "partial" {
				partial++
			}
			if within {
				inApproved++
			}
			if d.Extended {
				extApproved++
			}
		case d.Outcome == "denied":
			denied++
			if within {
				inDenied++
			}
			if d.Extended {
				extDenied++
			}
		}
		if d.Appealed {
			appealed++
			switch d.AppealOutcome {
			case "approved":
				appealApproved++
			case "denied":
				appealDenied++
			}
		}
	}
	n := len(rows)
	s.Approved, s.Denied, s.PartlyApproved = count(approved, n), count(denied, n), partial
	s.ApprovedWithinDeadline, s.DeniedWithinDeadline = count(inApproved, n), count(inDenied, n)
	s.ApprovedAfterExtension, s.DeniedAfterExtension = count(extApproved, n), count(extDenied, n)
	s.ApprovedAfterAppeal, s.DeniedAfterAppeal = count(appealApproved, appealed), count(appealDenied, appealed)

	if n > 0 {
		sort.Float64s(hours)
		sum := 0.0
		for _, h := range hours {
			sum += h
		}
		mean := sum / float64(n)
		median := hours[n/2]
		if n%2 == 0 {
			median = (hours[n/2-1] + hours[n/2]) / 2
		}
		s.Turnaround = Turnaround{
			MeanHours: math.Round(mean*100) / 100, MedianHours: math.Round(median*100) / 100,
			Mean: formatTurnaround(mean), Median: formatTurnaround(median),
		}
	}

	return s
}

// formatTurnaround writes a duration in hours below a day and in calendar days otherwise, always with the unit.
func formatTurnaround(hours float64) string {
	if hours < 24 {
		v := math.Round(hours*10) / 10
		unit := "hours"
		if v == 1 {
			unit = "hour"
		}
		return strconv.FormatFloat(v, 'f', -1, 64) + " " + unit
	}
	days := math.Round(hours/24*100) / 100
	unit := "calendar days"
	if days == 1 {
		unit = "calendar day"
	}

	return strconv.FormatFloat(days, 'f', -1, 64) + " " + unit
}

func humanDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 && d >= 24*time.Hour {
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	}

	return fmt.Sprintf("%d hours", int(d.Hours()))
}

// ParseDecisionsCSV reads decisions exported from a utilisation management system.
//
// Columns are found by header name, in any order and any case: request_id, line_of_business, priority (standard or expedited;
// urgent is accepted for expedited), received, decided, decision (approved, denied, partial, pending), extended, appealed,
// appeal_decision, service_code, service_description. Times may be RFC 3339, "2006-01-02 15:04" or a bare date.
//
// Every row that cannot be read is reported with its line number, rather than the file being read as far as it happens to go.
func ParseDecisionsCSV(r io.Reader) ([]Decision, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading the header row: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	for _, need := range []string{"priority", "received", "decided", "decision"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("the CSV has no %q column; the header row needs at least priority, received, decided and decision", need)
		}
	}
	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	var (
		out      []Decision
		problems []string
	)
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		d := Decision{
			RequestID:          get(rec, "request_id"),
			LineOfBusiness:     get(rec, "line_of_business"),
			ServiceCode:        get(rec, "service_code"),
			ServiceDescription: get(rec, "service_description"),
		}
		switch p := strings.ToLower(get(rec, "priority")); p {
		case "standard", "normal", "non-urgent":
			d.Expedited = false
		case "expedited", "urgent", "stat":
			d.Expedited = true
		default:
			problems = append(problems, fmt.Sprintf("line %d: priority %q is neither standard nor expedited", line, p))
			continue
		}
		switch o := strings.ToLower(get(rec, "decision")); o {
		case "approved", "approve", "certified", "a1":
			d.Outcome = "approved"
		case "denied", "deny", "not certified", "a3":
			d.Outcome = "denied"
		case "partial", "partially approved", "modified", "a2", "a6":
			d.Outcome = "partial"
		case "pending", "pended", "a4", "":
			d.Outcome = "pending"
		default:
			problems = append(problems, fmt.Sprintf("line %d: decision %q is not approved, denied, partial or pending", line, o))
			continue
		}
		if d.Received, err = parseWhen(get(rec, "received")); err != nil {
			problems = append(problems, fmt.Sprintf("line %d: received: %v", line, err))
			continue
		}
		if v := get(rec, "decided"); v != "" {
			if d.Decided, err = parseWhen(v); err != nil {
				problems = append(problems, fmt.Sprintf("line %d: decided: %v", line, err))
				continue
			}
		}
		d.Extended = truthy(get(rec, "extended"))
		d.Appealed = truthy(get(rec, "appealed"))
		switch a := strings.ToLower(get(rec, "appeal_decision")); a {
		case "approved", "overturned", "a1":
			d.AppealOutcome, d.Appealed = "approved", true
		case "denied", "upheld", "a3":
			d.AppealOutcome, d.Appealed = "denied", true
		case "":
		default:
			problems = append(problems, fmt.Sprintf("line %d: appeal_decision %q is not approved or denied", line, a))
			continue
		}
		if looksLikeDrug(d.ServiceCode) {
			problems = append(problems, fmt.Sprintf("line %d: service %q is a drug code; CMS-0057 metrics exclude drugs, so remove the row", line, d.ServiceCode))
			continue
		}
		out = append(out, d)
	}
	if len(problems) > 0 {
		if len(problems) > 20 {
			problems = append(problems[:20], fmt.Sprintf("and %d more", len(problems)-20))
		}
		return nil, fmt.Errorf("%d row(s) could not be read:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}

	return out, nil
}

// ParseServicesCSV reads the list of items and services requiring prior authorisation: category, code, description.
func ParseServicesCSV(r io.Reader) ([]Service, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	col := map[string]int{"category": -1, "code": -1, "description": -1}
	for i, h := range rows[0] {
		if _, ok := col[strings.ToLower(strings.TrimSpace(h))]; ok {
			col[strings.ToLower(strings.TrimSpace(h))] = i
		}
	}
	if col["code"] < 0 && col["description"] < 0 {
		return nil, fmt.Errorf("the services list needs a code or description column")
	}
	at := func(rec []string, name string) string {
		if i := col[name]; i >= 0 && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var out []Service
	for _, rec := range rows[1:] {
		s := Service{Category: at(rec, "category"), Code: at(rec, "code"), Description: at(rec, "description")}
		if s.Code == "" && s.Description == "" {
			continue
		}
		out = append(out, s)
	}

	return out, nil
}

// looksLikeDrug spots an NDC (11 digits, often hyphenated 4-4-2, 5-3-2 or 5-4-1) or a HCPCS J-code drug.
func looksLikeDrug(code string) bool {
	digits := strings.ReplaceAll(code, "-", "")
	if len(digits) >= 10 && len(digits) <= 11 && strings.Trim(digits, "0123456789") == "" {
		return true
	}

	return len(code) == 5 && code[0] == 'J' && strings.Trim(code[1:], "0123456789") == ""
}

func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "y", "yes", "true", "1", "t":
		return true
	}

	return false
}

func parseWhen(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02", "01/02/2006 15:04", "01/02/2006"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("%q is not a date and time this reads (RFC 3339, 2006-01-02 15:04, or 2006-01-02)", v)
}

// MetricsCSV renders the report in a flat, machine-readable form: one row per metric.
func MetricsCSV(r *MetricsReport) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"reporting_year", "line_of_business", "priority", "metric", "count", "out_of", "percent", "value", "unit"})
	y := strconv.Itoa(r.Year)
	row := func(line, pri, metric string, c Count) {
		_ = w.Write([]string{y, line, pri, metric, strconv.Itoa(c.Count), strconv.Itoa(c.Of), strconv.FormatFloat(c.Percent, 'f', 1, 64), "", ""})
	}
	for _, l := range r.Lines {
		for _, p := range []struct {
			name string
			s    Section
		}{{"standard", l.Standard}, {"expedited", l.Expedited}} {
			row(l.Name, p.name, "approved", p.s.Approved)
			row(l.Name, p.name, "denied", p.s.Denied)
			row(l.Name, p.name, "approved_within_"+strings.ReplaceAll(p.s.DeadlineLabel, " ", "_"), p.s.ApprovedWithinDeadline)
			row(l.Name, p.name, "denied_within_"+strings.ReplaceAll(p.s.DeadlineLabel, " ", "_"), p.s.DeniedWithinDeadline)
			row(l.Name, p.name, "approved_after_extension", p.s.ApprovedAfterExtension)
			row(l.Name, p.name, "denied_after_extension", p.s.DeniedAfterExtension)
			row(l.Name, p.name, "approved_after_appeal", p.s.ApprovedAfterAppeal)
			row(l.Name, p.name, "denied_after_appeal", p.s.DeniedAfterAppeal)
			if p.s.Requests > 0 {
				_ = w.Write([]string{y, l.Name, p.name, "mean_turnaround", "", "", "", strconv.FormatFloat(p.s.Turnaround.MeanHours, 'f', 2, 64), "hours"})
				_ = w.Write([]string{y, l.Name, p.name, "median_turnaround", "", "", "", strconv.FormatFloat(p.s.Turnaround.MedianHours, 'f', 2, 64), "hours"})
			}
		}
		row(l.Name, "all", "extended_and_approved", l.ExtendedAndApproved)
	}
	w.Flush()

	return b.Bytes()
}

// MetricsHTML renders the report as a public web page in the layout of CMS's template: a table per priority with how many
// times each thing happened, out of how many, and the percentage, and a bar for each so the page reads at a glance.
//
// Self-contained - no script, no external stylesheet, no fonts - because it is posted on a payer's public website by whoever
// maintains that, and anything it loads from elsewhere is something else that can break or track a visitor.
func MetricsHTML(r *MetricsReport) ([]byte, error) {
	var b bytes.Buffer
	if err := metricsTemplate.Execute(&b, r); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}

var metricsTemplate = template.Must(template.New("metrics").Funcs(template.FuncMap{
	"pct": func(c Count) string { return strconv.FormatFloat(c.Percent, 'f', 1, 64) + "%" },
	"width": func(c Count) string {
		return strconv.FormatFloat(math.Max(0, math.Min(100, c.Percent)), 'f', 1, 64)
	},
	// dict lets a template pass two values to a sub-template, which html/template has no literal for.
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Prior Authorization Metrics for Medical Items and Services (Excluding Drugs){{if .Organization}} - {{.Organization}}{{end}} - {{.Year}}</title>
<style>
body{font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:#1a1a1a;max-width:60rem;margin:2rem auto;padding:0 1rem}
h1{font-size:1.6rem;line-height:1.25}h2{font-size:1.3rem;margin-top:2.5rem;border-bottom:2px solid #1a1a1a;padding-bottom:.25rem}
h3{font-size:1.1rem;margin-top:1.75rem}
table{border-collapse:collapse;width:100%;margin:.75rem 0 1.25rem}
th,td{border:1px solid #767676;padding:.45rem .6rem;text-align:left;vertical-align:top}
th{background:#f2f2f2}td.n{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
.bar{background:#e6e6e6;height:.7rem;min-width:6rem;border-radius:2px}.bar>span{display:block;height:100%;background:#1f5fa8;border-radius:2px}
.note{background:#fff8e1;border-left:4px solid #b26a00;padding:.6rem .9rem;margin:1rem 0}
caption{text-align:left;font-weight:600;padding:.25rem 0}
</style>
</head>
<body>
<h1>Prior Authorization Metrics for Medical Items and Services (Excluding Drugs)</h1>
<p>To comply with the CMS Interoperability and Prior Authorization final rule (CMS-0057-F){{if .Organization}}, {{.Organization}}{{end}} reports aggregated prior authorization metrics on its website each year: the medical items and services (excluding drugs) that require prior authorization, and data on the prior authorization requests for those items and services over the previous calendar year.{{if .Contact}} For questions about this data, contact: {{.Contact}}.{{end}}</p>
<p><strong>Reporting period:</strong> January 1 to December 31, {{.Year}}.</p>
{{if .DataQuality}}<div class="note" role="note"><strong>About this data.</strong><ul>{{range .DataQuality}}<li>{{.}}</li>{{end}}</ul></div>{{end}}

<h2>Items and services that require prior authorization (excluding drugs)</h2>
{{if .Services}}<table><thead><tr><th scope="col">Category</th><th scope="col">Code</th><th scope="col">Description</th></tr></thead><tbody>
{{range .Services}}<tr><td>{{.Category}}</td><td>{{.Code}}</td><td>{{.Description}}</td></tr>
{{end}}</tbody></table>{{else}}<p class="note">The list of items and services requiring prior authorization has not been supplied.</p>{{end}}

{{range .Lines}}
<h2>{{.Name}}</h2>
{{template "section" (dict "Title" "Standard (non-urgent) prior authorization requests" "S" .Standard)}}
{{template "section" (dict "Title" "Expedited (urgent) prior authorization requests" "S" .Expedited)}}
<h3>Review time extended, all requests</h3>
<table><caption>Requests where the timeframe for review was extended and the request was approved</caption>
<thead><tr><th scope="col">Metric</th><th scope="col">How many times this happened</th><th scope="col">Out of total requests</th><th scope="col">Percentage</th><th scope="col">Share</th></tr></thead>
<tbody><tr><th scope="row">Request approved after time for review was extended</th><td class="n">{{.ExtendedAndApproved.Count}}</td><td class="n">{{.ExtendedAndApproved.Of}}</td><td class="n">{{pct .ExtendedAndApproved}}</td><td aria-hidden="true"><div class="bar"><span style="width:{{width .ExtendedAndApproved}}%"></span></div></td></tr></tbody></table>
{{end}}
<p><small>Produced by Perfuse from the payer's prior authorization records. Turnaround is measured from when the payer received each request to its decision.</small></p>
</body>
</html>
{{define "row"}}<tr><th scope="row">{{.Label}}</th><td class="n">{{.C.Count}}</td><td class="n">{{.C.Of}}</td><td class="n">{{pct .C}}</td><td aria-hidden="true"><div class="bar"><span style="width:{{width .C}}%"></span></div></td></tr>
{{end}}
{{define "section"}}<h3>{{.Title}}</h3>
{{if eq .S.Requests 0}}<p>No requests of this kind were decided in the reporting period.</p>{{else}}
<table><caption>Decisions</caption><thead><tr><th scope="col">Metric</th><th scope="col">How many times this happened</th><th scope="col">Out of total requests</th><th scope="col">Percentage</th><th scope="col">Share</th></tr></thead><tbody>
{{template "row" (dict "Label" "Request approved" "C" .S.Approved)}}{{template "row" (dict "Label" "Request denied" "C" .S.Denied)}}
{{template "row" (dict "Label" (print "Request approved within " .S.DeadlineLabel) "C" .S.ApprovedWithinDeadline)}}{{template "row" (dict "Label" (print "Request denied within " .S.DeadlineLabel) "C" .S.DeniedWithinDeadline)}}
{{template "row" (dict "Label" "Request approved only after time for review was extended" "C" .S.ApprovedAfterExtension)}}{{template "row" (dict "Label" "Request denied after time for review was extended" "C" .S.DeniedAfterExtension)}}
</tbody></table>
<table><caption>Appeals</caption><thead><tr><th scope="col">Metric</th><th scope="col">How many times this happened</th><th scope="col">Out of total appeals</th><th scope="col">Percentage</th><th scope="col">Share</th></tr></thead><tbody>
{{template "row" (dict "Label" "Request approved only after appeal" "C" .S.ApprovedAfterAppeal)}}{{template "row" (dict "Label" "Request denied after appeal" "C" .S.DeniedAfterAppeal)}}
</tbody></table>
<table><caption>Time from receipt to decision</caption><thead><tr><th scope="col">Average (mean)</th><th scope="col">Median</th></tr></thead>
<tbody><tr><td>{{.S.Turnaround.Mean}}</td><td>{{.S.Turnaround.Median}}</td></tr></tbody></table>
{{if gt .S.PartlyApproved 0}}<p><small>{{.S.PartlyApproved}} of the approved requests were approved in part. They are counted as approved, as the CMS template has no separate category for them.</small></p>{{end}}
{{end}}{{end}}`))
