package fhirserver

// The reviewer's view of PAS: the requests waiting for a person, each read whole, and the decision a reviewer makes. The web
// console's reviewer queue is built on these, and $decide uses the same decision rules, so a decision made in the console and
// one made over FHIR cannot differ.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PASCase is one prior authorization request as a reviewer sees it.
type PASCase struct {
	ID       string    `json:"id"`
	Trace    string    `json:"trace"`
	Member   string    `json:"member"`
	MemberID string    `json:"memberId"`
	Provider string    `json:"provider"`
	Pended   bool      `json:"pended"`
	Version  int       `json:"version"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	// Expedited is a request marked stat: CMS-0057 gives it 72 hours rather than 7 days. Due is when that runs out.
	Expedited bool          `json:"expedited"`
	Due       time.Time     `json:"due"`
	Items     []PASCaseItem `json:"items"`
	// Asked says what the response asks for ("LOINC 18776-5", "questionnaire <url>"); Attachments what has arrived.
	Asked       []string        `json:"asked,omitempty"`
	Attachments []PASAttachment `json:"attachments"`
	// Request and Response are the PAS Bundles, on a single case read only.
	Request  map[string]any `json:"request,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

// PASCaseItem is one service asked for and where it stands.
type PASCaseItem struct {
	Sequence int     `json:"sequence"`
	Service  string  `json:"service"`
	Quantity float64 `json:"quantity,omitempty"`
	Code     string  `json:"code"`
	Display  string  `json:"display"`
	Note     string  `json:"note,omitempty"`
}

// PASReview is a reviewer's decision.
type PASReview struct {
	// Decision is approve, deny or modify.
	Decision string `json:"decision"`
	// Items limits it to these item sequences; empty is every pended item.
	Items  []int  `json:"items,omitempty"`
	Reason string `json:"reason,omitempty"`
	// ReviewerNPI goes in the response's claimResponseReviewer.
	ReviewerNPI string `json:"reviewerNpi,omitempty"`
	// Quantity and Alternative are what a modify certifies: fewer units, or another service instead.
	Quantity    float64        `json:"quantity,omitempty"`
	Alternative map[string]any `json:"alternative,omitempty"`
}

// ErrBadReview is a decision that cannot be made as given; its message says why.
type ErrBadReview struct{ msg string }

func (e ErrBadReview) Error() string { return e.msg }

// reviewDecision turns a reviewer's decision into the item decision applyDecision makes.
func reviewDecision(rv PASReview) (itemDecision, error) {
	var d itemDecision
	switch rv.Decision {
	case "approve":
		d = pasApproved
	case "deny":
		d = pasDenied
	case "modify":
		if rv.Quantity <= 0 && rv.Alternative == nil {
			return d, ErrBadReview{"decision modify needs quantity (the units certified) or alternative (the service approved instead), or both"}
		}
		d = pasModified
		d.answer = PASAnswer{Decision: "approve", AllowedQuantity: rv.Quantity, Alternative: rv.Alternative}
	default:
		return d, ErrBadReview{"decision must be approve, deny or modify"}
	}
	if (rv.Quantity > 0 || rv.Alternative != nil) && rv.Decision != "modify" {
		return d, ErrBadReview{"quantity and alternative go with decision modify"}
	}
	if rv.Alternative != nil && str(rv.Alternative["code"]) == "" {
		return d, ErrBadReview{"alternative is the service approved instead, a Coding with a code"}
	}
	d.why = rv.Reason
	return d, nil
}

// Review records a reviewer's decision on a pended request, exactly as Claim/$decide does: a new version of the response, and a
// notification on the PAS topic.
func (s *Server) Review(ctx context.Context, id string, rv PASReview) (map[string]any, error) {
	if s.PAS == nil {
		return nil, errors.New("Da Vinci PAS is not enabled on this server (serve -pas)")
	}
	d, err := reviewDecision(rv)
	if err != nil {
		return nil, err
	}
	items := map[int]bool{}
	for _, n := range rv.Items {
		items[n] = true
	}
	return s.decidePAS(ctx, id, d, items, rv.ReviewerNPI)
}

// PASEnabled says whether this server answers PAS.
func (s *Server) PASEnabled() bool { return s != nil && s.PAS != nil }

// Cases lists prior authorization requests, the pended ones only when pendedOnly, the most urgent first: the soonest due.
func (s *Server) Cases(ctx context.Context, pendedOnly bool, limit int) ([]PASCase, error) {
	if err := s.pasReady(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT r.id, r.version, r.pended, r.created, r.updated, r.request, p.response FROM pas_requests r
		JOIN pas_responses p ON p.id = r.id AND p.version = r.version`
	if pendedOnly {
		q += ` WHERE r.pended = 1`
	}
	q += ` ORDER BY r.created DESC LIMIT ?`
	rows, err := s.Store.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PASCase
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts, err := s.attachmentCounts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Attachments = counts[out[i].ID]
		out[i].Request, out[i].Response = nil, nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pended != out[j].Pended {
			return out[i].Pended
		}
		if out[i].Pended {
			return out[i].Due.Before(out[j].Due)
		}
		return out[i].Updated.After(out[j].Updated)
	})
	return out, nil
}

// Case reads one request whole: both Bundles and the documents received.
func (s *Server) Case(ctx context.Context, id string) (*PASCase, error) {
	if err := s.pasReady(ctx); err != nil {
		return nil, err
	}
	row := s.Store.db.QueryRowContext(ctx, `SELECT r.id, r.version, r.pended, r.created, r.updated, r.request, p.response FROM pas_requests r
		JOIN pas_responses p ON p.id = r.id AND p.version = r.version WHERE r.id = ?`, id)
	c, err := scanCase(row)
	if err != nil {
		return nil, err
	}
	if c.Attachments, err = s.Attachments(ctx, id, true); err != nil {
		return nil, err
	}
	if c.Attachments == nil {
		c.Attachments = []PASAttachment{}
	}
	return c, nil
}

func (s *Server) attachmentCounts(ctx context.Context) (map[string][]PASAttachment, error) {
	rows, err := s.Store.db.QueryContext(ctx, `SELECT rowid, id, tracking, resource_type, code, final, received FROM pas_attachments ORDER BY received`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]PASAttachment{}
	for rows.Next() {
		var a PASAttachment
		var id string
		var final int
		var received int64
		if err := rows.Scan(&a.ID, &id, &a.Tracking, &a.ResourceType, &a.Code, &final, &received); err != nil {
			return nil, err
		}
		a.Final, a.Received = final == 1, time.UnixMilli(received).UTC()
		out[id] = append(out[id], a)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanCase(row rowScanner) (*PASCase, error) {
	var c PASCase
	var pended int
	var created, updated int64
	var reqRaw, respRaw string
	if err := row.Scan(&c.ID, &c.Version, &pended, &created, &updated, &reqRaw, &respRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	c.Pended = pended == 1
	c.Created, c.Updated = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	if err := json.Unmarshal([]byte(reqRaw), &c.Request); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(respRaw), &c.Response); err != nil {
		return nil, err
	}
	req, _ := readPASRequest(c.Request, true)
	if req != nil {
		resource := func(field string) map[string]any {
			return asMapAny(req.resolve(str(asMapAny(req.claim[field])["reference"]))["resource"])
		}
		patient := resource("patient")
		p := personFrom(patient)
		c.Member = strings.TrimSpace(titleCase(p.FirstName + " " + p.LastName))
		c.MemberID = firstIdentifier(patient, "")
		prov := personFrom(resource("provider"))
		c.Provider = strings.TrimSpace(titleCase(prov.FirstName + " " + prov.LastName))
		if npi := firstIdentifier(resource("provider"), "http://hl7.org/fhir/sid/us-npi"); npi != "" {
			c.Provider = strings.TrimSpace(c.Provider + " (NPI " + npi + ")")
		}
		for _, id := range asSliceAny(req.claim["identifier"]) {
			if v := str(asMapAny(id)["value"]); v != "" && c.Trace == "" {
				c.Trace = v
			}
		}
		c.Expedited = pasExpedited(req.claim)
	}
	c.Due = c.Created.Add(pasTimeframe(c.Expedited))
	notes := map[string]string{}
	var cr map[string]any
	for _, e := range asSliceAny(c.Response["entry"]) {
		res := asMapAny(asMapAny(e)["resource"])
		switch res["resourceType"] {
		case "ClaimResponse":
			cr = res
		case "Task":
			// What the Task asks for stays in the response after the decision, where the CommunicationRequests do not.
			for _, in := range asSliceAny(res["input"]) {
				im := asMapAny(in)
				for _, c2 := range asSliceAny(asMapAny(im["type"])["coding"]) {
					switch str(asMapAny(c2)["code"]) {
					case "attachments-needed":
						for _, v := range asSliceAny(asMapAny(im["valueCodeableConcept"])["coding"]) {
							c.Asked = append(c.Asked, "LOINC "+str(asMapAny(v)["code"]))
						}
					case "questionnaire-context":
						c.Asked = append(c.Asked, "questionnaire "+str(im["valueString"]))
					}
				}
			}
		}
	}
	for _, n := range asSliceAny(cr["processNote"]) {
		nm := asMapAny(n)
		notes[fmt.Sprint(nm["number"])] = str(nm["text"])
	}
	requested := map[string]map[string]any{}
	if req != nil {
		for _, it := range asSliceAny(req.claim["item"]) {
			requested[fmt.Sprint(asMapAny(it)["sequence"])] = asMapAny(it)
		}
	}
	for _, it := range asSliceAny(cr["item"]) {
		im := asMapAny(it)
		seq := fmt.Sprint(im["itemSequence"])
		item := PASCaseItem{}
		fmt.Sscan(seq, &item.Sequence)
		if asked := requested[seq]; asked != nil {
			item.Service = codingLabel(asMapAny(asked["productOrService"]))
			item.Quantity, _ = asMapAny(asked["quantity"])["value"].(float64)
		}
		item.Code, item.Display = reviewCode(im)
		for _, n := range asSliceAny(im["noteNumber"]) {
			item.Note = notes[fmt.Sprint(n)]
		}
		c.Items = append(c.Items, item)
	}
	return &c, nil
}

// reviewCode is an item's review action code and display.
func reviewCode(item map[string]any) (string, string) {
	for _, a := range asSliceAny(item["adjudication"]) {
		for _, e := range asSliceAny(asMapAny(a)["extension"]) {
			for _, x := range asSliceAny(asMapAny(e)["extension"]) {
				if xm := asMapAny(x); str(xm["url"]) == pasBase+"extension-reviewActionCode" {
					c := asMapAny(asSliceAny(asMapAny(xm["valueCodeableConcept"])["coding"])[0])
					return str(c["code"]), str(c["display"])
				}
			}
		}
	}
	return "", ""
}

func codingLabel(cc map[string]any) string {
	if t := str(cc["text"]); t != "" {
		return t
	}
	for _, c := range asSliceAny(cc["coding"]) {
		cm := asMapAny(c)
		if d := str(cm["display"]); d != "" {
			return d + " (" + str(cm["code"]) + ")"
		}
		if code := str(cm["code"]); code != "" {
			return code
		}
	}
	return ""
}

func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// pasExpedited says whether a request asks for an expedited decision: Claim.priority stat.
func pasExpedited(claim map[string]any) bool {
	for _, c := range asSliceAny(asMapAny(claim["priority"])["coding"]) {
		if str(asMapAny(c)["code"]) == "stat" {
			return true
		}
	}
	return false
}

// pasTimeframe is how long CMS-0057 gives a payer to decide: 72 hours expedited, 7 calendar days standard (Medicare Advantage,
// Medicaid and CHIP; QHP issuers on the federal exchanges keep their own timeframes).
func pasTimeframe(expedited bool) time.Duration {
	if expedited {
		return 72 * time.Hour
	}
	return 7 * 24 * time.Hour
}

// PASFigures are the prior authorization figures a payer's operations dashboard shows: how many requests met the CMS-0057
// timeframe, how many are overdue now, and how the services asked for were answered.
type PASFigures struct {
	Since time.Time `json:"since"`
	// Standard and Expedited count requests by where they stand against their timeframe.
	Standard  PASTimeliness `json:"standard"`
	Expedited PASTimeliness `json:"expedited"`
	// Decisions counts items by their current review action: approved, partial, modified, denied, pended, cancelled.
	Decisions map[string]int `json:"decisions"`
	Items     int            `json:"items"`
}

// PASTimeliness is one priority's requests against its timeframe.
type PASTimeliness struct {
	Requests      int `json:"requests"`
	DecidedInTime int `json:"decidedInTime"`
	DecidedLate   int `json:"decidedLate"`
	PendingInTime int `json:"pendingInTime"`
	Overdue       int `json:"overdue"`
	// MedianHours is the median time from receipt to the decision, over the decided requests; 0 when none was decided.
	MedianHours float64 `json:"medianHours"`
}

// Figures computes PASFigures over the requests received since a time.
func (s *Server) Figures(ctx context.Context, since time.Time) (*PASFigures, error) {
	if err := s.pasReady(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Store.db.QueryContext(ctx, `SELECT r.id, r.version, r.pended, r.created, r.updated, r.request, p.response FROM pas_requests r
		JOIN pas_responses p ON p.id = r.id AND p.version = r.version WHERE r.created >= ?`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	if s.PAS != nil {
		now = s.PAS.now()
	}
	out := &PASFigures{Since: since, Decisions: map[string]int{}}
	var stdHours, expHours []float64
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		t := &out.Standard
		if c.Expedited {
			t = &out.Expedited
		}
		t.Requests++
		switch {
		case c.Pended && now.After(c.Due):
			t.Overdue++
		case c.Pended:
			t.PendingInTime++
		case !c.Updated.After(c.Due):
			t.DecidedInTime++
		default:
			t.DecidedLate++
		}
		if !c.Pended {
			h := c.Updated.Sub(c.Created).Hours()
			if c.Expedited {
				expHours = append(expHours, h)
			} else {
				stdHours = append(stdHours, h)
			}
		}
		for _, it := range c.Items {
			out.Items++
			out.Decisions[decisionName(it.Code)]++
		}
	}
	out.Standard.MedianHours, out.Expedited.MedianHours = median(stdHours), median(expHours)
	return out, rows.Err()
}

func decisionName(code string) string {
	switch code {
	case "A1":
		return "approved"
	case "A2":
		return "partially approved"
	case "A6":
		return "modified"
	case "A3":
		return "denied"
	case "A4":
		return "pended"
	case "C":
		return "cancelled"
	}
	return "other"
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	m := len(v) / 2
	if len(v)%2 == 1 {
		return v[m]
	}
	return (v[m-1] + v[m]) / 2
}
