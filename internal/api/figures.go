package api

// The team dashboards' figures that need more than a count: prior authorization timeliness, imaging by modality, lab result
// delivery, X12 rejections and remittance matching, and who read patient records. Each is an aggregate - a label and a value
// - and never shows a message, a patient or an identifier, which is what lets a dashboard go to a manager or a wall screen.
// A figure that cannot be computed here (PAS off, message storage off) says why rather than showing zeros.

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/x12"
)

// FigureRow is one line of a figure: a label, a value, and how to colour it (ok, warn, bad, or empty for neutral).
type FigureRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Tone  string `json:"tone,omitempty"`
}

// Figure is one tile's content.
type Figure struct {
	Rows []FigureRow `json:"rows"`
	// Note says what the figure covers (the window, the source); Unavailable, when set, is why it could not be computed.
	Note        string `json:"note,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
	Empty       string `json:"empty,omitempty"`
}

type figureContext struct {
	s    *Server
	r    *http.Request
	sess *store.Session
	rt   *Runtime
	now  time.Time
}

var figureFuncs = map[string]func(*figureContext) Figure{
	"pas-timeliness":         figPASTimeliness,
	"pas-decisions":          figPASDecisions,
	"dicom-modality":         figDICOMModality,
	"dicom-routing":          figDICOMRouting,
	"dicom-queries":          figDICOMQueries,
	"reports-undelivered":    figReportsUndelivered,
	"lab-delivery-time":      figLabDeliveryTime,
	"lab-critical":           figLabCritical,
	"x12-999":                figX12Acks,
	"x12-277ca":              figX12ClaimAcks,
	"x12-835-match":          figX12RemittanceMatch,
	"x12-rejection-reasons":  figX12RejectionReasons,
	"privacy-patient-access": figPatientAccess,
	"token-use":              figTokenUse,
}

// FigureTiles are the tiles this endpoint draws.
func FigureTiles() []string {
	out := make([]string, 0, len(figureFuncs))
	for id := range figureFuncs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *Server) handleDashboardFigures(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}
	fc := &figureContext{s: s, r: r, sess: sess, rt: rt, now: time.Now().UTC()}
	out := map[string]Figure{}
	for _, id := range strings.Split(r.URL.Query().Get("tiles"), ",") {
		if f, ok := figureFuncs[strings.TrimSpace(id)]; ok {
			fig := f(fc)
			if fig.Rows == nil {
				fig.Rows = []FigureRow{}
			}
			out[strings.TrimSpace(id)] = fig
		}
	}
	s.ok(w, map[string]any{"figures": out})
}

// --- prior authorization -------------------------------------------------------------------------------------------

func figPASTimeliness(fc *figureContext) Figure {
	if !fc.s.PAS.PASEnabled() {
		return Figure{Unavailable: "Da Vinci PAS is not enabled on this server (serve -fhir -pas)"}
	}
	f, err := fc.s.PAS.Figures(fc.r.Context(), fc.now.AddDate(0, 0, -30))
	if err != nil {
		return Figure{Unavailable: err.Error()}
	}
	var rows []FigureRow
	for _, p := range []struct {
		name  string
		t     struct{ r, in, late, pend, over int }
		limit string
		med   float64
	}{
		{"Expedited", struct{ r, in, late, pend, over int }{f.Expedited.Requests, f.Expedited.DecidedInTime, f.Expedited.DecidedLate,
			f.Expedited.PendingInTime, f.Expedited.Overdue}, "72 hours", f.Expedited.MedianHours},
		{"Standard", struct{ r, in, late, pend, over int }{f.Standard.Requests, f.Standard.DecidedInTime, f.Standard.DecidedLate,
			f.Standard.PendingInTime, f.Standard.Overdue}, "7 days", f.Standard.MedianHours},
	} {
		if p.t.r == 0 {
			continue
		}
		rows = append(rows,
			FigureRow{Label: p.name + " requests (" + p.limit + ")", Value: fmt.Sprint(p.t.r)},
			FigureRow{Label: "  decided within " + p.limit, Value: pct(p.t.in, p.t.in+p.t.late), Tone: toneAtLeast(p.t.in, p.t.in+p.t.late, 1)},
			FigureRow{Label: "  decided late", Value: fmt.Sprint(p.t.late), Tone: badIf(p.t.late > 0)},
			FigureRow{Label: "  waiting, still in time", Value: fmt.Sprint(p.t.pend)},
			FigureRow{Label: "  overdue now", Value: fmt.Sprint(p.t.over), Tone: badIf(p.t.over > 0)})
		if p.med > 0 || p.t.in+p.t.late > 0 {
			rows = append(rows, FigureRow{Label: "  median time to decide", Value: hours(p.med)})
		}
	}
	return Figure{Rows: rows, Note: "PAS requests received in the last 30 days, against CMS-0057's timeframes.",
		Empty: "No prior authorization requests in 30 days."}
}

func figPASDecisions(fc *figureContext) Figure {
	if !fc.s.PAS.PASEnabled() {
		return Figure{Unavailable: "Da Vinci PAS is not enabled on this server (serve -fhir -pas)"}
	}
	f, err := fc.s.PAS.Figures(fc.r.Context(), fc.now.AddDate(0, 0, -30))
	if err != nil {
		return Figure{Unavailable: err.Error()}
	}
	var rows []FigureRow
	for _, k := range []string{"approved", "partially approved", "modified", "denied", "pended", "cancelled", "other"} {
		if n := f.Decisions[k]; n > 0 {
			rows = append(rows, FigureRow{Label: k, Value: fmt.Sprintf("%d (%s)", n, pct(n, f.Items))})
		}
	}
	return Figure{Rows: rows, Note: fmt.Sprintf("%d services asked for in the last 30 days, as each stands now.", f.Items),
		Empty: "No prior authorization requests in 30 days."}
}

// --- messages ------------------------------------------------------------------------------------------------------

// channelsOfSource names the channels whose source is one of these types.
func (fc *figureContext) channelsOfSource(types ...config.SourceType) []string {
	if fc.rt == nil || fc.rt.Repo == nil {
		return nil
	}
	list, _, err := fc.rt.Repo.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range list {
		for _, t := range types {
			if c.SourceType == string(t) {
				out = append(out, c.Name)
			}
		}
	}
	return out
}

func (fc *figureContext) recent(window time.Duration, f msgstore.RecentFilter) ([]msgstore.RecentMessage, string) {
	if fc.rt == nil || fc.rt.Messages == nil {
		return nil, "message storage is not enabled in this process"
	}
	rows, err := fc.rt.Messages.Recent(fc.r.Context(), string(fc.sess.TenantID), fc.now.Add(-window), f)
	if err != nil {
		return nil, err.Error()
	}
	return rows, ""
}

func undelivered(m msgstore.RecentMessage) bool {
	switch m.Outcome {
	case msgstore.Failed, msgstore.Partial, msgstore.Pending, msgstore.Unparseable:
		return true
	}
	return m.FailedDeliveries > 0
}

func figDICOMModality(fc *figureContext) Figure {
	chans := fc.channelsOfSource(config.SourceDICOM)
	if len(chans) == 0 {
		return Figure{Empty: "No channel receives DICOM (C-STORE)."}
	}
	msgs, why := fc.recent(24*time.Hour, msgstore.RecentFilter{Channels: chans})
	if why != "" {
		return Figure{Unavailable: why}
	}
	type count struct{ in, failed int }
	by := map[string]*count{}
	for _, m := range msgs {
		mod := m.Type
		if mod == "" {
			mod = "(no modality)"
		}
		if by[mod] == nil {
			by[mod] = &count{}
		}
		by[mod].in++
		if undelivered(m) {
			by[mod].failed++
		}
	}
	keys := sortKeys(by, func(a, b string) bool { return by[a].failed > by[b].failed || by[a].failed == by[b].failed && a < b })
	var rows []FigureRow
	for _, k := range keys {
		c := by[k]
		rows = append(rows, FigureRow{Label: k, Value: fmt.Sprintf("%d stored, %d failed (%s)", c.in, c.failed, pct(c.failed, c.in)),
			Tone: badIf(c.failed > 0)})
	}
	return Figure{Rows: rows, Note: "Objects received by C-STORE in the last 24 hours, by modality (0008,0060). Failed means not delivered on.",
		Empty: "No images received in 24 hours."}
}

func figDICOMRouting(fc *figureContext) Figure {
	chans := fc.channelsOfSource(config.SourceDICOM, config.SourceDICOMQuery)
	if len(chans) == 0 {
		return Figure{Empty: "No channel routes DICOM."}
	}
	msgs, why := fc.recent(24*time.Hour, msgstore.RecentFilter{Channels: chans})
	if why != "" {
		return Figure{Unavailable: why}
	}
	return Figure{Rows: durationRows(msgs, func(m msgstore.RecentMessage) bool { return !undelivered(m) }),
		Note:  "Time from receiving a study object to delivering it to every destination, last 24 hours.",
		Empty: "Nothing routed in 24 hours."}
}

func figDICOMQueries(fc *figureContext) Figure {
	chans := fc.channelsOfSource(config.SourceDICOMQuery)
	if len(chans) == 0 {
		return Figure{Empty: "No channel queries a worklist or archive (dicom_query)."}
	}
	if fc.rt == nil || fc.rt.Metrics == nil {
		return Figure{Unavailable: "metrics are not collected in this process"}
	}
	window := fc.rt.Metrics.Window()
	since := fc.now.Add(-window)
	sum := func(name string) map[string]float64 {
		out := map[string]float64{}
		for _, series := range fc.rt.Metrics.Query(name, since) {
			for _, p := range series.Points {
				out[series.Labels["channel"]] += p.Value
			}
		}
		return out
	}
	polls, failed := sum(metrics.DICOMQueryPolls), sum(metrics.DICOMQueryFailures)
	var rows []FigureRow
	for _, c := range chans {
		rows = append(rows, FigureRow{Label: c, Value: fmt.Sprintf("%.0f queries, %.0f failed", polls[c], failed[c]), Tone: badIf(failed[c] > 0)})
	}
	return Figure{Rows: rows, Note: "C-FIND queries against the worklist or archive since " + humanWindow(window) + " ago."}
}

// imagingSections are HL7 table 0074 diagnostic service sections that are imaging.
var imagingSections = map[string]bool{"RAD": true, "CT": true, "NMR": true, "NMS": true, "RUS": true, "VUS": true, "OUS": true,
	"CUS": true, "XRC": true, "RX": true, "MR": true, "US": true, "IMG": true, "MG": true}

func figReportsUndelivered(fc *figureContext) Figure {
	msgs, why := fc.recent(24*time.Hour, msgstore.RecentFilter{Types: []string{"ORU", "MDM"}, WithRaw: true})
	if why != "" {
		return Figure{Unavailable: why}
	}
	total, by := 0, map[string]int{}
	for _, m := range msgs {
		if !isImagingReport(m) {
			continue
		}
		total++
		if undelivered(m) {
			by[m.Channel]++
		}
	}
	var rows []FigureRow
	for _, c := range sortKeys(by, func(a, b string) bool { return by[a] > by[b] }) {
		rows = append(rows, FigureRow{Label: c, Value: fmt.Sprintf("%d not delivered", by[c]), Tone: "bad"})
	}
	empty := "Every imaging report in 24 hours was delivered."
	if total == 0 {
		empty = "No imaging reports (ORU with an imaging OBR-24, or MDM) in 24 hours."
	}
	return Figure{Rows: rows, Note: fmt.Sprintf("%d imaging reports in the last 24 hours; these did not reach every destination.", total), Empty: empty}
}

func isImagingReport(m msgstore.RecentMessage) bool {
	if strings.HasPrefix(m.Type, "MDM") {
		return true
	}
	msg, err := hl7.Parse(m.Raw)
	if err != nil {
		return false
	}
	for _, obr := range msg.Segments("OBR") {
		if imagingSections[strings.ToUpper(obr.Field(24).String())] {
			return true
		}
	}
	return false
}

func figLabDeliveryTime(fc *figureContext) Figure {
	msgs, why := fc.recent(24*time.Hour, msgstore.RecentFilter{Types: []string{"ORU"}, WithRaw: true})
	if why != "" {
		return Figure{Unavailable: why}
	}
	var lab []msgstore.RecentMessage
	for _, m := range msgs {
		if !isImagingReport(m) && !undelivered(m) {
			lab = append(lab, m)
		}
	}
	return Figure{Rows: durationRows(lab, func(msgstore.RecentMessage) bool { return true }),
		Note:  "Results (ORU, not imaging) in the last 24 hours: from receipt to the last destination's acknowledgement.",
		Empty: "No results delivered in 24 hours."}
}

// criticalFlags are OBX-8 abnormal flags that mean a critical value: HL7 table 0078 panic high and low, critically abnormal,
// and off-scale.
var criticalFlags = map[string]bool{"HH": true, "LL": true, "AA": true, ">": true, "<": true}

func figLabCritical(fc *figureContext) Figure {
	msgs, why := fc.recent(24*time.Hour, msgstore.RecentFilter{Types: []string{"ORU"}, WithRaw: true})
	if why != "" {
		return Figure{Unavailable: why}
	}
	critical, by := 0, map[string]int{}
	for _, m := range msgs {
		msg, err := hl7.Parse(m.Raw)
		if err != nil {
			continue
		}
		crit := false
		for _, obx := range msg.Segments("OBX") {
			for _, f := range obx.Field(8).Repeats() {
				crit = crit || criticalFlags[strings.ToUpper(strings.TrimSpace(f.String()))]
			}
		}
		if !crit {
			continue
		}
		critical++
		if undelivered(m) {
			by[m.Channel]++
		}
	}
	var rows []FigureRow
	for _, c := range sortKeys(by, func(a, b string) bool { return by[a] > by[b] }) {
		rows = append(rows, FigureRow{Label: c, Value: fmt.Sprintf("%d critical results not delivered", by[c]), Tone: "bad"})
	}
	empty := "Every critical result in 24 hours was delivered."
	if critical == 0 {
		empty = "No critical results (OBX-8 HH, LL, AA, > or <) in 24 hours."
	}
	return Figure{Rows: rows, Note: fmt.Sprintf("%d results with a critical flag in the last 24 hours.", critical), Empty: empty}
}

// --- X12 -----------------------------------------------------------------------------------------------------------

func x12Messages(fc *figureContext, window time.Duration, set string) ([]*x12.Message, string) {
	msgs, why := fc.recent(window, msgstore.RecentFilter{Types: []string{set}, WithRaw: true})
	if why != "" {
		return nil, why
	}
	var out []*x12.Message
	for _, m := range msgs {
		if x, err := x12.Parse(m.Raw); err == nil {
			out = append(out, x)
		}
	}
	return out, ""
}

func figX12Acks(fc *figureContext) Figure {
	acks, why := x12Messages(fc, 24*time.Hour, "999")
	if why != "" {
		return Figure{Unavailable: why}
	}
	counts := map[string]int{}
	total := 0
	for _, m := range acks {
		for _, ik5 := range m.Segments("IK5") {
			total++
			counts[ik5.Element(1).String()]++
		}
	}
	rejected := counts["R"] + counts["M"] + counts["W"] + counts["X"]
	rows := []FigureRow{
		{Label: "transaction sets acknowledged", Value: fmt.Sprint(total)},
		{Label: "accepted", Value: fmt.Sprint(counts["A"])},
		{Label: "accepted with errors", Value: fmt.Sprint(counts["E"]), Tone: warnIf(counts["E"] > 0)},
		{Label: "rejected", Value: fmt.Sprintf("%d (%s)", rejected, pct(rejected, total)), Tone: badIf(rejected > 0)},
	}
	if total == 0 {
		rows = nil
	}
	return Figure{Rows: rows, Note: "999 acknowledgements received in the last 24 hours (IK5).", Empty: "No 999 in 24 hours."}
}

// claimRejected is a 277CA claim status category that rejects the claim.
func claimRejected(category string) bool {
	switch category {
	case "A3", "A4", "A6", "A7", "A8":
		return true
	}
	return false
}

func figX12ClaimAcks(fc *figureContext) Figure {
	reports, why := x12Messages(fc, 24*time.Hour, "277")
	if why != "" {
		return Figure{Unavailable: why}
	}
	total, rejected, accepted := 0, 0, 0
	for _, m := range reports {
		r, err := m.ParseStatusReport()
		if err != nil {
			continue
		}
		for _, st := range r.Statuses {
			total++
			switch {
			case claimRejected(st.Category):
				rejected++
			case st.Category == "A1" || st.Category == "A2":
				accepted++
			}
		}
	}
	if total == 0 {
		return Figure{Empty: "No 277CA in 24 hours."}
	}
	return Figure{Rows: []FigureRow{
		{Label: "claims acknowledged", Value: fmt.Sprint(total)},
		{Label: "accepted (A1, A2)", Value: fmt.Sprint(accepted)},
		{Label: "rejected (A3, A4, A6, A7, A8)", Value: fmt.Sprintf("%d (%s)", rejected, pct(rejected, total)), Tone: badIf(rejected > 0)},
	}, Note: "Claim acknowledgements (277CA) received in the last 24 hours."}
}

func figX12RemittanceMatch(fc *figureContext) Figure {
	eras, why := x12Messages(fc, 7*24*time.Hour, "835")
	if why != "" {
		return Figure{Unavailable: why}
	}
	claims, why := x12Messages(fc, 120*24*time.Hour, "837")
	if why != "" {
		return Figure{Unavailable: why}
	}
	sent := map[string]bool{}
	for _, m := range claims {
		cs, _ := x12.ParseClaims(m)
		for _, c := range cs {
			sent[c.PatientAccountNumber] = true
		}
	}
	paid, matched := 0, 0
	for _, m := range eras {
		r, err := x12.ParseERA(m)
		if err != nil {
			continue
		}
		for _, c := range r.Claims {
			paid++
			if sent[c.PatientAccountNumber] {
				matched++
			}
		}
	}
	if paid == 0 {
		return Figure{Empty: "No 835 in 7 days."}
	}
	return Figure{Rows: []FigureRow{
		{Label: "claims paid or denied (835 CLP)", Value: fmt.Sprint(paid)},
		{Label: "matched to a claim sent (837 CLM01)", Value: fmt.Sprintf("%d (%s)", matched, pct(matched, paid)), Tone: toneAtLeast(matched, paid, 0.95)},
		{Label: "not matched", Value: fmt.Sprint(paid - matched), Tone: warnIf(paid > matched)},
	}, Note: "835s received in the last 7 days, matched by patient account number to 837s sent through this server in the last 120 days."}
}

func figX12RejectionReasons(fc *figureContext) Figure {
	reasons := map[string]int{}
	reports, why := x12Messages(fc, 7*24*time.Hour, "277")
	if why != "" {
		return Figure{Unavailable: why}
	}
	for _, m := range reports {
		if r, err := m.ParseStatusReport(); err == nil {
			for _, st := range r.Statuses {
				if claimRejected(st.Category) {
					reasons["277CA "+st.Category+":"+st.Code]++
				}
			}
		}
	}
	acks, _ := x12Messages(fc, 7*24*time.Hour, "999")
	for _, m := range acks {
		for _, ik3 := range m.Segments("IK3") {
			reasons["999 "+ik3.Element(1).String()+" segment error "+ik3.Element(4).String()]++
		}
		for _, ik4 := range m.Segments("IK4") {
			reasons["999 element error "+ik4.Element(3).String()]++
		}
	}
	keys := sortKeys(reasons, func(a, b string) bool { return reasons[a] > reasons[b] || reasons[a] == reasons[b] && a < b })
	if len(keys) > 8 {
		keys = keys[:8]
	}
	var rows []FigureRow
	for _, k := range keys {
		rows = append(rows, FigureRow{Label: k, Value: fmt.Sprint(reasons[k])})
	}
	return Figure{Rows: rows, Note: "The commonest reasons in the last 7 days: 277CA status category and code, 999 segment and element error codes.",
		Empty: "No rejections in 7 days."}
}

// --- privacy -------------------------------------------------------------------------------------------------------

// patientAccessActions are the audit actions that read patient data.
var patientAccessActions = []string{"message.read", "message.identity.search", "pas.read", "shl.create"}

func figPatientAccess(fc *figureContext) Figure {
	counts, err := fc.s.storeFor(fc.sess).AuditCounts(fc.r.Context(), fc.now.AddDate(0, 0, -7), patientAccessActions)
	if err != nil {
		return Figure{Unavailable: err.Error()}
	}
	names := map[string]string{"message.read": "messages opened", "message.identity.search": "patient searches",
		"pas.read": "prior authorizations read", "shl.create": "health links shared"}
	by := map[string][]string{}
	total := map[string]int{}
	for _, c := range counts {
		by[c.Username] = append(by[c.Username], fmt.Sprintf("%d %s", c.Count, names[c.Action]))
		total[c.Username] += c.Count
	}
	var rows []FigureRow
	for _, u := range sortKeys(by, func(a, b string) bool { return total[a] > total[b] || total[a] == total[b] && a < b }) {
		rows = append(rows, FigureRow{Label: u, Value: strings.Join(by[u], ", ")})
	}
	return Figure{Rows: rows, Note: "Who read patient data in the console in the last 7 days, from the audit log.",
		Empty: "Nobody read patient data in 7 days."}
}

func figTokenUse(fc *figureContext) Figure {
	tokens, err := fc.s.storeFor(fc.sess).ListAPITokens(fc.r.Context())
	if err != nil {
		return Figure{Unavailable: err.Error()}
	}
	var rows []FigureRow
	for _, t := range tokens {
		if t.RevokedAt != nil {
			continue
		}
		v, tone := "never used", "warn"
		if t.LastUsed != nil {
			ago := fc.now.Sub(*t.LastUsed)
			v, tone = "last used "+humanWindow(ago)+" ago", ""
			if ago > 90*24*time.Hour {
				tone = "warn"
				v += ": revoke it if nothing needs it"
			}
		}
		rows = append(rows, FigureRow{Label: t.Label + " (" + string(t.Role) + ")", Value: v, Tone: tone})
	}
	return Figure{Rows: rows, Note: "API tokens that can still be used. A token unused for 90 days is a credential nobody is watching.",
		Empty: "No API tokens."}
}

// --- helpers -------------------------------------------------------------------------------------------------------

func durationRows(msgs []msgstore.RecentMessage, keep func(msgstore.RecentMessage) bool) []FigureRow {
	by := map[string][]float64{}
	for _, m := range msgs {
		if keep(m) {
			by[m.Channel] = append(by[m.Channel], float64(m.DurationMS))
		}
	}
	var rows []FigureRow
	for _, c := range sortKeys(by, func(a, b string) bool { return a < b }) {
		v := by[c]
		sort.Float64s(v)
		p95 := v[int(math.Ceil(0.95*float64(len(v))))-1]
		rows = append(rows, FigureRow{Label: c, Value: fmt.Sprintf("median %s, 95%% within %s (%d)", msText(quantile(v, 0.5)), msText(p95), len(v))})
	}
	return rows
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	return sorted[max(0, i)]
}

func msText(v float64) string {
	switch {
	case v < 1000:
		return fmt.Sprintf("%.0f ms", v)
	case v < 120_000:
		return fmt.Sprintf("%.1f s", v/1000)
	default:
		return fmt.Sprintf("%.1f min", v/60_000)
	}
}

func hours(h float64) string {
	if h < 48 {
		return fmt.Sprintf("%.1f hours", h)
	}
	return fmt.Sprintf("%.1f days", h/24)
}

func humanWindow(d time.Duration) string {
	switch {
	case d < 2*time.Hour:
		return fmt.Sprintf("%.0f minutes", d.Minutes())
	case d < 48*time.Hour:
		return fmt.Sprintf("%.0f hours", d.Hours())
	default:
		return fmt.Sprintf("%.0f days", d.Hours()/24)
	}
}

func pct(n, of int) string {
	if of == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(of))
}

func badIf(b bool) string {
	if b {
		return "bad"
	}
	return ""
}

func warnIf(b bool) string {
	if b {
		return "warn"
	}
	return ""
}

// toneAtLeast is ok when n/of reaches the share, bad otherwise; neutral with nothing to judge.
func toneAtLeast(n, of int, share float64) string {
	if of == 0 {
		return ""
	}
	if float64(n)/float64(of) >= share {
		return "ok"
	}
	return "bad"
}

func sortKeys[V any](m map[string]V, less func(a, b string) bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
	return keys
}
