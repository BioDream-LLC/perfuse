package epa

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/formulary"
)

// Closed reason codes (NCPDP ECL), as the Surescripts companion guide's business flows use them.
const (
	ClosedNotRequired  = "CC" // Prior Authorization not required for patient/medication
	ClosedNoPatient    = "CD" // Cannot find matching patient
	ClosedNotEligible  = "CE" // Patient not eligible
	ClosedDuplicate    = "CG" // Prior Authorization duplicate/in process
	ClosedNotCovered   = "FM" // Product not covered by this plan; Prior Authorization not available
	ClosedWithinLimits = "FN" // Prescription within prescribing limits; Prior Authorization not required
	ClosedActiveOnFile = "FQ" // Active PA on file
	ClosedBadProduct   = "FR" // Submitted Product Code is not valid
	ClosedOther        = "BY" // Other, with a note
)

// Service is the payer's ePA endpoint.
type Service struct {
	Formulary *formulary.File
	DB        *sql.DB
	// PayerID is this payer's identifier on the network, the From of every response.
	PayerID string
	// Submit decides a PAS request Bundle (fhirserver.Server.SubmitPAS). Nil refuses PARequests: there is nothing to decide with.
	Submit func(ctx context.Context, bundle map[string]any) (map[string]any, string, []string, error)
	// Review records a decision on a PAS request (fhirserver.Server.Review), used to cancel one.
	Review func(ctx context.Context, id, decision, reason string) error
	// Questionnaire finds a FHIR Questionnaire by canonical URL, or nil.
	Questionnaire func(ctx context.Context, canonical string) map[string]any
	// Member finds a member's plan id in the FHIR store, or "".
	Member func(id string) string
	// Send delivers an unsolicited response (a decision made after the request was answered) to the prescriber's system.
	// Nil keeps it on the case for the screen, marked not sent.
	Send func(ctx context.Context, reply []byte) error
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	Log *slog.Logger

	once sync.Once
	err  error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) ready(ctx context.Context) error {
	s.once.Do(func() {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS epa_cases (
				case_id TEXT PRIMARY KEY,
				reference_id TEXT NOT NULL,
				member TEXT NOT NULL,
				plan TEXT NOT NULL,
				rxnorm TEXT NOT NULL,
				ndc TEXT NOT NULL,
				drug TEXT NOT NULL,
				prescriber TEXT NOT NULL,
				state TEXT NOT NULL,
				pas_id TEXT NOT NULL DEFAULT '',
				outcome TEXT NOT NULL DEFAULT '',
				reason TEXT NOT NULL DEFAULT '',
				auth_number TEXT NOT NULL DEFAULT '',
				auth_end TEXT NOT NULL DEFAULT '',
				appeal_pas_id TEXT NOT NULL DEFAULT '',
				appeal_outcome TEXT NOT NULL DEFAULT '',
				appealed INTEGER NOT NULL DEFAULT 0,
				expedited INTEGER NOT NULL DEFAULT 0,
				created INTEGER NOT NULL,
				submitted INTEGER NOT NULL DEFAULT 0,
				decided INTEGER NOT NULL DEFAULT 0,
				questions TEXT NOT NULL DEFAULT '',
				request TEXT NOT NULL,
				reply TEXT NOT NULL DEFAULT '',
				reply_sent INTEGER NOT NULL DEFAULT 0,
				reply_error TEXT NOT NULL DEFAULT '')`,
			`CREATE INDEX IF NOT EXISTS epa_cases_pas ON epa_cases (pas_id)`,
			`CREATE INDEX IF NOT EXISTS epa_cases_appeal ON epa_cases (appeal_pas_id)`,
			`CREATE INDEX IF NOT EXISTS epa_cases_member ON epa_cases (member, rxnorm)`,
		} {
			if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
				s.err = fmt.Errorf("preparing the ePA tables: %w", err)
				return
			}
		}
	})
	return s.err
}

// Case is one prior authorization case, for the screen.
type Case struct {
	CaseID        string     `json:"caseId"`
	ReferenceID   string     `json:"referenceId"`
	Member        string     `json:"member"`
	Plan          string     `json:"plan"`
	RxNorm        string     `json:"rxnorm"`
	NDC           string     `json:"ndc"`
	Drug          string     `json:"drug"`
	Prescriber    string     `json:"prescriber"`
	State         string     `json:"state"`
	PASID         string     `json:"pasId,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	AuthNumber    string     `json:"authNumber,omitempty"`
	AuthEnd       string     `json:"authEnd,omitempty"`
	Appealed      bool       `json:"appealed"`
	AppealOutcome string     `json:"appealOutcome,omitempty"`
	Expedited     bool       `json:"expedited"`
	Created       time.Time  `json:"created"`
	Submitted     *time.Time `json:"submitted,omitempty"`
	Decided       *time.Time `json:"decided,omitempty"`
	Answers       []Answered `json:"answers,omitempty"`
	ReplySent     bool       `json:"replySent"`
	ReplyError    string     `json:"replyError,omitempty"`
	Reply         string     `json:"reply,omitempty"`
}

const caseColumns = `case_id, reference_id, member, plan, rxnorm, ndc, drug, prescriber, state, pas_id, outcome, reason, auth_number,
	auth_end, appealed, appeal_outcome, expedited, created, submitted, decided, questions, reply, reply_sent, reply_error`

func scan(row interface{ Scan(...any) error }) (*Case, error) {
	var c Case
	var appealed, expedited, sent int
	var created, submitted, decided int64
	var questions string
	if err := row.Scan(&c.CaseID, &c.ReferenceID, &c.Member, &c.Plan, &c.RxNorm, &c.NDC, &c.Drug, &c.Prescriber, &c.State, &c.PASID,
		&c.Outcome, &c.Reason, &c.AuthNumber, &c.AuthEnd, &appealed, &c.AppealOutcome, &expedited, &created, &submitted, &decided,
		&questions, &c.Reply, &sent, &c.ReplyError); err != nil {
		return nil, err
	}
	c.Appealed, c.Expedited, c.ReplySent = appealed == 1, expedited == 1, sent == 1
	c.Created = time.UnixMilli(created).UTC()
	if submitted > 0 {
		t := time.UnixMilli(submitted).UTC()
		c.Submitted = &t
	}
	if decided > 0 {
		t := time.UnixMilli(decided).UTC()
		c.Decided = &t
	}
	if questions != "" {
		var qs QuestionSet
		if json.Unmarshal([]byte(questions), &qs) == nil {
			c.Answers = qs.Read()
		}
	}
	return &c, nil
}

// Cases lists cases, newest first.
func (s *Service) Cases(ctx context.Context, limit int) ([]Case, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+caseColumns+` FROM epa_cases ORDER BY created DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Case
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		c.Reply = ""
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Get returns one case, with its last response.
func (s *Service) Get(ctx context.Context, id string) (*Case, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	return scan(s.DB.QueryRowContext(ctx, `SELECT `+caseColumns+` FROM epa_cases WHERE case_id = ?`, id))
}

// ErrNotEPA is a message that parses as XML but is no ePA transaction this payer answers.
var ErrNotEPA = errors.New("not an electronic prior authorization request")

// Handle answers one inbound ePA message with its response.
func (s *Service) Handle(ctx context.Context, raw []byte) ([]byte, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	kind, pa, _ := m.Kind()
	switch kind {
	case InitiationRequest:
		return s.initiate(ctx, m, pa, raw)
	case Request:
		return s.request(ctx, m, pa, raw)
	case AppealRequest:
		return s.appeal(ctx, m, pa, raw)
	case CancelRequest:
		return s.cancel(ctx, m, pa)
	}
	return nil, fmt.Errorf("%w: a payer receives requests, and this is a %s", ErrNotEPA, kind)
}

func newCaseID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "PF" + strings.ToUpper(hex.EncodeToString(b))
}

// drug finds the requested drug on the member's formulary.
func (s *Service) drug(pa *PA) (*formulary.Plan, *formulary.Drug, string, string) {
	member, group := pa.Member()
	plan, ok := s.Formulary.PlanFor(member, group, s.Member)
	if !ok {
		return nil, nil, ClosedNoPatient, "No member with this ID is on any plan this payer administers."
	}
	fm, _ := s.Formulary.Formulary(plan.Formulary)
	code := pa.Medication.Product.DrugCoded
	var d *formulary.Drug
	var err error
	switch {
	case strings.TrimSpace(code.NDC) != "":
		d, err = fm.Find(formulary.NDC, code.NDC)
	case code.ProductCode.Qualifier == "ND" && code.ProductCode.Code != "":
		d, err = fm.Find(formulary.NDC, code.ProductCode.Code)
	case code.ProductCode.Code != "":
		// An RxNorm code (qualifier SBD or SCD in 2023011), or no qualifier.
		d, err = fm.Find(formulary.RxNorm, code.ProductCode.Code)
	default:
		return plan, nil, ClosedBadProduct, "The request names no drug by NDC or RxNorm."
	}
	switch {
	case errors.Is(err, formulary.ErrNotListed):
		return plan, nil, ClosedNotCovered, "This drug is not on the " + fm.Name + "; a prior authorization cannot make it covered. " +
			"Ask for a formulary exception, or prescribe a covered alternative."
	case err != nil:
		return plan, nil, ClosedBadProduct, err.Error()
	case d.NotCovered:
		note := "This drug is excluded from the " + fm.Name + "."
		if d.Notes != "" {
			note += " " + d.Notes
		}
		return plan, nil, ClosedNotCovered, note
	}
	return plan, d, "", ""
}

func (s *Service) onFile(ctx context.Context, member, rxnorm string) (string, bool) {
	var id, end string
	err := s.DB.QueryRowContext(ctx, `SELECT case_id, auth_end FROM epa_cases WHERE member = ? AND rxnorm = ? AND outcome = 'approved'
		ORDER BY decided DESC LIMIT 1`, member, rxnorm).Scan(&id, &end)
	if err != nil || end == "" || end < s.now().Format("2006-01-02") {
		return "", false
	}
	return id, true
}

// PAOnFile says whether a member has an approved, unexpired prior authorization for a drug: what Real-Time Prescription
// Benefit reports as AB8.
func (s *Service) PAOnFile(member, rxnorm string) bool {
	if s.ready(context.Background()) != nil {
		return false
	}
	_, ok := s.onFile(context.Background(), member, rxnorm)
	return ok
}

func (s *Service) initiate(ctx context.Context, m *Message, pa *PA, raw []byte) ([]byte, error) {
	plan, d, reason, note := s.drug(pa)
	if reason != "" {
		return s.reply(m, pa, InitiationResponse, &ResponseDetail{}, closed("", reason, note)), nil
	}
	member, _ := pa.Member()
	if id, ok := s.onFile(ctx, member, d.RxNorm); ok {
		return s.reply(m, pa, InitiationResponse, &ResponseDetail{}, closed("", ClosedActiveOnFile,
			"An approved prior authorization for this drug is on file (case "+id+").")), nil
	}
	quantity, _ := strconv.ParseFloat(strings.TrimSpace(pa.Medication.Quantity.Value), 64)
	days, _ := strconv.Atoi(strings.TrimSpace(pa.Medication.DaysSupply))
	over, why := d.ExceedsLimit(quantity, days)
	if !d.PriorAuthorization && !d.StepTherapy && !over {
		code, note := ClosedNotRequired, "No prior authorization is required for this drug on this plan."
		if d.QuantityLimit != nil {
			code, note = ClosedWithinLimits, "No prior authorization is required: the prescription is within the plan's quantity limit."
		}
		return s.reply(m, pa, InitiationResponse, &ResponseDetail{}, closed("", code, note)), nil
	}
	if dup := s.openCase(ctx, member, d.RxNorm); dup != "" {
		return s.reply(m, pa, InitiationResponse, &ResponseDetail{}, closed(dup, ClosedDuplicate,
			"A prior authorization for this drug is already in process (case "+dup+").")), nil
	}
	caseID := newCaseID()
	qs := s.questions(ctx, m, d, caseID)
	if over && !d.PriorAuthorization && !d.StepTherapy {
		qs.Header.Description = strings.TrimSpace("The request is over the plan's limit: " + why + ". " + qs.Header.Description)
	}
	q, _ := json.Marshal(qs)
	now := s.now()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO epa_cases (case_id, reference_id, member, plan, rxnorm, ndc, drug, prescriber, state,
		created, questions, request) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?)`, caseID, pa.PAReferenceID, member, plan.ID, d.RxNorm,
		pa.Medication.Product.DrugCoded.NDC, d.Display, pa.Prescriber.NonVeterinarian.NPI, now.UnixMilli(), string(q), string(raw)); err != nil {
		return nil, err
	}
	open := &Open{PACaseID: caseID, DeadlineForReply: &DateTime{now.AddDate(0, 0, 30).Format("2006-01-02T15:04:05")}, QuestionSet: &qs}
	out := s.reply(m, pa, InitiationResponse, &ResponseDetail{}, open)
	s.saveReply(ctx, caseID, out, true, "")
	return out, nil
}

func (s *Service) openCase(ctx context.Context, member, rxnorm string) string {
	var id string
	_ = s.DB.QueryRowContext(ctx, `SELECT case_id FROM epa_cases WHERE member = ? AND rxnorm = ? AND state IN ('open', 'submitted')
		ORDER BY created DESC LIMIT 1`, member, rxnorm).Scan(&id)
	return id
}

func (s *Service) questions(ctx context.Context, m *Message, d *formulary.Drug, caseID string) QuestionSet {
	if d.Questionnaire != "" && s.Questionnaire != nil {
		if q := s.Questionnaire(ctx, d.Questionnaire); q != nil {
			return FromQuestionnaire(q, d.Questionnaire, m.boolean)
		}
		s.log().Warn("the drug's prior authorization questionnaire is not in the FHIR store; asking the general questions",
			"rxnorm", d.RxNorm, "questionnaire", d.Questionnaire)
	}
	return Generic("perfuse-epa-general", d.Display, m.boolean)
}

func (s *Service) request(ctx context.Context, m *Message, pa *PA, raw []byte) ([]byte, error) {
	if s.Submit == nil {
		return nil, errors.New("electronic prior authorization needs Da Vinci PAS on this server to decide requests (serve -pas)")
	}
	caseID := pa.CaseID()
	var c *Case
	if caseID != "" {
		var err error
		if c, err = s.Get(ctx, caseID); errors.Is(err, sql.ErrNoRows) {
			return s.reply(m, pa, Response, &ResponseDetail{}, closed(caseID, ClosedOther, "No case "+caseID+" is open with this payer.")), nil
		} else if err != nil {
			return nil, err
		}
		if c.State != "open" {
			return s.reply(m, pa, Response, &ResponseDetail{}, closed(caseID, ClosedOther,
				"Case "+caseID+" is "+c.State+"; send an appeal to ask again.")), nil
		}
	}
	plan, d, reason, note := s.drug(pa)
	if reason != "" {
		return s.reply(m, pa, Response, &ResponseDetail{}, closed(caseID, reason, note)), nil
	}
	member, _ := pa.Member()
	now := s.now()
	if c == nil {
		// Unsolicited: a PARequest with no initiation first, as a renewal is.
		caseID = newCaseID()
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO epa_cases (case_id, reference_id, member, plan, rxnorm, ndc, drug, prescriber, state,
			created, request) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`, caseID, pa.PAReferenceID, member, plan.ID, d.RxNorm,
			pa.Medication.Product.DrugCoded.NDC, d.Display, pa.Prescriber.NonVeterinarian.NPI, now.UnixMilli(), string(raw)); err != nil {
			return nil, err
		}
	}
	answers := pa.Answers()
	bundle := toPAS(pa, plan, d, caseID, answers, false, now)
	resp, pasID, problems, err := s.Submit(ctx, bundle)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the request could not be made into a PAS request: %s", strings.Join(problems, "; "))
	}
	q, _ := json.Marshal(answers)
	if _, err := s.DB.ExecContext(ctx, `UPDATE epa_cases SET state = 'submitted', pas_id = ?, submitted = ?, questions = ?,
		request = ?, expedited = ? WHERE case_id = ?`, pasID, now.UnixMilli(), string(q), string(raw), boolInt(expedited(pa)), caseID); err != nil {
		return nil, err
	}
	out, err := s.decided(ctx, m, pa, caseID, resp, Response, false)
	if err != nil {
		return nil, err
	}
	s.saveReply(ctx, caseID, out, true, "")
	return out, nil
}

func expedited(pa *PA) bool {
	if pa.Request != nil && strings.Contains(strings.ToLower(pa.Request.PANote), "urgent") {
		return true
	}
	return false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// decided records a PAS answer on the case and writes the response that says it.
func (s *Service) decided(ctx context.Context, m *Message, pa *PA, caseID string, resp map[string]any, kind Kind, appeal bool) ([]byte, error) {
	o := outcomeOf(resp)
	now := s.now()
	detail := &ResponseDetail{}
	var status any
	switch o.code {
	case "approved":
		end := o.end
		if end == "" {
			end = now.AddDate(1, 0, 0).Format("2006-01-02")
		}
		status = &Approved{PACaseID: caseID, AuthorizationNumber: o.auth, PANote: o.why,
			AuthorizationPeriod: &Period{DateTime{dayStart(firstNonEmpty(o.start, now.Format("2006-01-02")))}, DateTime{dayEnd(end)}},
			Appeal:              &AppealInf{m.boolean(false)}}
		o.end = end
	case "denied":
		why := o.why
		if why == "" {
			why = "The request does not meet the plan's criteria for this drug."
		}
		status = &Denied{PACaseID: caseID, DenialReason: why, Appeal: &AppealInf{m.boolean(true)}}
	case "cancelled":
		status = closed(caseID, ClosedOther, "The request was cancelled.")
	default:
		status = &Open{PACaseID: caseID, PANote: "The request has been received and is under review. A decision will be sent when it is made." +
			map[bool]string{true: " " + o.why, false: ""}[o.why != ""]}
	}
	if o.code != "pended" {
		col := "outcome"
		if appeal {
			col = "appeal_outcome"
		}
		state := o.code
		if _, err := s.DB.ExecContext(ctx, `UPDATE epa_cases SET state = ?, `+col+` = ?, reason = ?, auth_number = ?, auth_end = ?, decided = ?
			WHERE case_id = ?`, state, o.code, o.why, o.auth, o.end, now.UnixMilli(), caseID); err != nil {
			return nil, err
		}
	}
	return s.reply(m, pa, kind, detail, status), nil
}

type outcome struct{ code, why, auth, start, end string }

// outcomeOf reads the decision out of a PAS response Bundle: every item approved (fully, in part or modified) is an
// approval, any denied is a denial, any pended keeps it open.
func outcomeOf(bundle map[string]any) outcome {
	var o outcome
	for _, e := range list(bundle["entry"]) {
		cr, _ := e.(map[string]any)["resource"].(map[string]any)
		if str(cr["resourceType"]) != "ClaimResponse" {
			continue
		}
		o.auth = str(cr["preAuthRef"])
		if p, ok := cr["preAuthPeriod"].(map[string]any); ok {
			o.start, o.end = str(p["start"]), str(p["end"])
		}
		notes := map[string]string{}
		for _, n := range list(cr["processNote"]) {
			nm, _ := n.(map[string]any)
			notes[fmt.Sprint(nm["number"])] = str(nm["text"])
		}
		codes := map[string]bool{}
		var whys []string
		for _, it := range list(cr["item"]) {
			im, _ := it.(map[string]any)
			for _, adj := range list(im["adjudication"]) {
				for _, ext := range list(adj.(map[string]any)["extension"]) {
					for _, x := range list(ext.(map[string]any)["extension"]) {
						xm, _ := x.(map[string]any)
						if !strings.HasSuffix(str(xm["url"]), "extension-reviewActionCode") {
							continue
						}
						for _, c := range list(xm["valueCodeableConcept"].(map[string]any)["coding"]) {
							codes[str(c.(map[string]any)["code"])] = true
						}
					}
				}
			}
			for _, n := range list(im["noteNumber"]) {
				if t := notes[fmt.Sprint(n)]; t != "" {
					whys = append(whys, t)
				}
			}
		}
		o.why = strings.Join(whys, " ")
		switch {
		case codes["A4"]:
			o.code = "pended"
		case codes["A3"]:
			o.code = "denied"
		case codes["C"]:
			o.code = "cancelled"
		case codes["A1"] || codes["A2"] || codes["A6"]:
			o.code = "approved"
		default:
			o.code = "pended"
		}
	}
	if o.code == "" {
		o.code = "pended"
	}
	return o
}

func dayStart(d string) string {
	if len(d) == 10 {
		return d + "T00:00:00"
	}
	return d
}

func dayEnd(d string) string {
	if len(d) == 10 {
		return d + "T23:59:59"
	}
	return d
}

func closed(caseID, code, note string) *Closed {
	return &Closed{PACaseID: caseID, ReasonCode: code, PANote: note}
}

func (s *Service) appeal(ctx context.Context, m *Message, pa *PA, raw []byte) ([]byte, error) {
	if s.Submit == nil {
		return nil, errors.New("electronic prior authorization needs Da Vinci PAS on this server to decide requests (serve -pas)")
	}
	caseID := pa.CaseID()
	c, err := s.Get(ctx, caseID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.reply(m, pa, AppealResponse, &ResponseDetail{}, closed(caseID, ClosedOther, "No case "+caseID+" is known to this payer.")), nil
	} else if err != nil {
		return nil, err
	}
	if c.Outcome != "denied" || c.Appealed && c.AppealOutcome == "" {
		return s.reply(m, pa, AppealResponse, &ResponseDetail{}, closed(caseID, ClosedOther,
			"Case "+caseID+" is "+c.State+"; only a denied request can be appealed, once at a time.")), nil
	}
	note := ""
	if pa.Request != nil {
		note = strings.TrimSpace(pa.Request.PANote)
	}
	if note == "" {
		return s.reply(m, pa, AppealResponse, &ResponseDetail{}, &Open{PACaseID: caseID,
			PANote: "Please say why the decision should change: an appeal is decided on the reason given."}), nil
	}
	orig, err := Parse([]byte(s.request0(ctx, caseID)))
	if err != nil {
		return nil, err
	}
	_, opa, _ := orig.Kind()
	plan, _ := s.Formulary.Plan(c.Plan)
	fm, _ := s.Formulary.Formulary(plan.Formulary)
	d, err := fm.Find(formulary.RxNorm, c.RxNorm)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var answers *QuestionSet
	var q string
	_ = s.DB.QueryRowContext(ctx, `SELECT questions FROM epa_cases WHERE case_id = ?`, caseID).Scan(&q)
	if q != "" {
		answers = new(QuestionSet)
		_ = json.Unmarshal([]byte(q), answers)
	}
	bundle := toPAS(opa, plan, d, caseID, answers, true, now)
	addAppealNote(bundle, note, c.PASID)
	resp, pasID, problems, err := s.Submit(ctx, bundle)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the appeal could not be made into a PAS request: %s", strings.Join(problems, "; "))
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE epa_cases SET appealed = 1, appeal_pas_id = ?, appeal_outcome = '', state = 'appealed'
		WHERE case_id = ?`, pasID, caseID); err != nil {
		return nil, err
	}
	out, err := s.decided(ctx, m, pa, caseID, resp, AppealResponse, true)
	if err != nil {
		return nil, err
	}
	s.saveReply(ctx, caseID, out, true, "")
	return out, nil
}

func (s *Service) request0(ctx context.Context, caseID string) string {
	var raw string
	_ = s.DB.QueryRowContext(ctx, `SELECT request FROM epa_cases WHERE case_id = ?`, caseID).Scan(&raw)
	return raw
}

func (s *Service) cancel(ctx context.Context, m *Message, pa *PA) ([]byte, error) {
	caseID := pa.CaseID()
	c, err := s.Get(ctx, caseID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s.reply(m, pa, CancelResponse, &ResponseDetail{}, &Denied{PACaseID: caseID, DenialReason: "No case " + caseID + " is known to this payer."}), nil
	case err != nil:
		return nil, err
	case c.State != "open" && c.State != "submitted":
		return s.reply(m, pa, CancelResponse, &ResponseDetail{}, &Denied{PACaseID: caseID,
			DenialReason: "Case " + caseID + " is already " + c.State + " and cannot be cancelled."}), nil
	}
	reason := "Cancelled by the prescriber."
	if pa.Request != nil && pa.Request.CancelReasonCode != "" {
		reason += " Reason code " + pa.Request.CancelReasonCode + "."
	}
	if c.PASID != "" && s.Review != nil {
		if err := s.Review(ctx, c.PASID, "cancel", reason); err != nil {
			return nil, fmt.Errorf("cancelling the PAS request: %w", err)
		}
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE epa_cases SET state = 'cancelled', outcome = 'cancelled', reason = ?, decided = ? WHERE case_id = ?`,
		reason, s.now().UnixMilli(), caseID); err != nil {
		return nil, err
	}
	return s.reply(m, pa, CancelResponse, &ResponseDetail{}, &Approved{PACaseID: caseID}), nil
}

// Decided is told of a reviewer's decision on a PAS request; when it is one of ours, the prescriber is sent an unsolicited
// PAResponse (or PAAppealResponse) saying so.
func (s *Service) Decided(ctx context.Context, pasID string, response map[string]any) {
	if s.ready(ctx) != nil {
		return
	}
	var caseID string
	appeal := false
	if err := s.DB.QueryRowContext(ctx, `SELECT case_id FROM epa_cases WHERE pas_id = ?`, pasID).Scan(&caseID); err != nil {
		if err := s.DB.QueryRowContext(ctx, `SELECT case_id FROM epa_cases WHERE appeal_pas_id = ?`, pasID).Scan(&caseID); err != nil {
			return
		}
		appeal = true
	}
	m, err := Parse([]byte(s.request0(ctx, caseID)))
	if err != nil {
		s.log().Error("an ePA case's request cannot be read back", "case", caseID, "error", err)
		return
	}
	_, pa, _ := m.Kind()
	kind := Response
	if appeal {
		kind = AppealResponse
	}
	out, err := s.decided(ctx, m, pa, caseID, response, kind, appeal)
	if err != nil {
		s.log().Error("recording the decision on an ePA case", "case", caseID, "error", err)
		return
	}
	if s.Send == nil {
		s.saveReply(ctx, caseID, out, false, "no reply route is set (-epa-reply-channel), so the prescriber has not been told")
		return
	}
	if err := s.Send(ctx, out); err != nil {
		s.log().Warn("sending an ePA decision to the prescriber failed", "case", caseID, "error", err)
		s.saveReply(ctx, caseID, out, false, err.Error())
		return
	}
	s.saveReply(ctx, caseID, out, true, "")
}

func (s *Service) saveReply(ctx context.Context, caseID string, out []byte, sent bool, why string) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE epa_cases SET reply = ?, reply_sent = ?, reply_error = ? WHERE case_id = ?`,
		string(out), boolInt(sent), why, caseID)
}

// Resend sends a case's last response again, for a decision whose delivery failed.
func (s *Service) Resend(ctx context.Context, caseID string) error {
	c, err := s.Get(ctx, caseID)
	if err != nil {
		return err
	}
	if c.Reply == "" {
		return errors.New("this case has no response to send")
	}
	if s.Send == nil {
		return errors.New("no reply route is set (-epa-reply-channel)")
	}
	if err := s.Send(ctx, []byte(c.Reply)); err != nil {
		s.saveReply(ctx, caseID, []byte(c.Reply), false, err.Error())
		return err
	}
	s.saveReply(ctx, caseID, []byte(c.Reply), true, "")
	return nil
}

// Metrics are CMS-0062-P's proposed prior authorization figures for drugs, over the cases decided since a time.
type Metrics struct {
	Since            time.Time `json:"since"`
	Requests         int       `json:"requests"`
	Approved         int       `json:"approved"`
	Denied           int       `json:"denied"`
	Pending          int       `json:"pending"`
	Appealed         int       `json:"appealed"`
	ApprovedOnAppeal int       `json:"approvedOnAppeal"`
	DeniedOnAppeal   int       `json:"deniedOnAppeal"`
	// MeanHours and MedianHours are from the request (the PARequest) to the decision.
	MeanHours   float64 `json:"meanHours"`
	MedianHours float64 `json:"medianHours"`
	// Late counts decisions after the proposed timeframe (72 hours standard, 24 expedited).
	Late int `json:"late"`
}

// Metrics computes the drug figures.
func (s *Service) Metrics(ctx context.Context, since time.Time) (*Metrics, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT outcome, appealed, appeal_outcome, expedited, submitted, decided FROM epa_cases
		WHERE submitted >= ?`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := &Metrics{Since: since}
	var hours []float64
	for rows.Next() {
		var outcome, appealOutcome string
		var appealed, exp int
		var submitted, decided int64
		if err := rows.Scan(&outcome, &appealed, &appealOutcome, &exp, &submitted, &decided); err != nil {
			return nil, err
		}
		if outcome == "cancelled" {
			continue
		}
		m.Requests++
		switch outcome {
		case "approved":
			m.Approved++
		case "denied":
			m.Denied++
		default:
			m.Pending++
		}
		if appealed == 1 {
			m.Appealed++
			switch appealOutcome {
			case "approved":
				m.ApprovedOnAppeal++
			case "denied":
				m.DeniedOnAppeal++
			}
		}
		if decided > 0 && outcome != "" {
			h := float64(decided-submitted) / 3.6e6
			hours = append(hours, h)
			limit := 72.0
			if exp == 1 {
				limit = 24
			}
			if h > limit {
				m.Late++
			}
		}
	}
	if len(hours) > 0 {
		sum := 0.0
		for _, h := range hours {
			sum += h
		}
		m.MeanHours = sum / float64(len(hours))
		sorted := append([]float64(nil), hours...)
		for i := 1; i < len(sorted); i++ {
			for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
				sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
			}
		}
		if n := len(sorted); n%2 == 1 {
			m.MedianHours = sorted[n/2]
		} else {
			m.MedianHours = (sorted[n/2-1] + sorted[n/2]) / 2
		}
	}
	return m, rows.Err()
}

var _ = xml.Name{}
