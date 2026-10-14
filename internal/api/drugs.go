package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/epa"
	"github.com/biodream-llc/perfuse/internal/formulary"
	"github.com/biodream-llc/perfuse/internal/rtpb"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Drugs: the formulary, NCPDP Real-Time Prescription Benefit and NCPDP SCRIPT electronic prior authorization.
//
// The network endpoints (/ncpdp/rtpb and /ncpdp/script) take XML from a prescribing network or an EHR with an API token
// of the operator role, and are audited. The formulary itself is public, as CMS requires of a payer's drug formulary, at
// /formulary/fhir (mounted by serve), which holds the formulary and nothing else.

// Drugs is what the drug screens and endpoints need. Nil Formulary leaves them off.
type Drugs struct {
	Formulary *formulary.File
	// FormularyBase is the public Da Vinci US Drug Formulary endpoint's address.
	FormularyBase string
	RTPB          rtpb.Options
	// EPA answers SCRIPT ePA; its Submit is nil when PAS is off, which leaves initiation working and PARequests refused.
	EPA *epa.Service
	// ReplyChannel names the channel unsolicited ePA responses are routed to, or "".
	ReplyChannel string
}

const maxNCPDP = 32 << 20

func (s *Server) registerDrugs(mux *recordingMux) {
	mux.Handle("GET /api/formulary", s.require(store.RoleViewer, s.handleFormulary))
	mux.Handle("POST /api/formulary/rtpb", s.require(store.RoleViewer, s.handleRTPBTry))
	mux.Handle("GET /api/epa/cases", s.require(store.RoleViewer, s.handleEPACases))
	mux.Handle("GET /api/epa/cases/{id}", s.require(store.RoleViewer, s.handleEPACase))
	mux.Handle("POST /api/epa/cases/{id}/resend", s.require(store.RoleOperator, s.handleEPAResend))
	mux.Handle("POST /api/epa/try", s.require(store.RoleEditor, s.handleEPATry))
	mux.Handle("GET /api/epa/metrics", s.require(store.RoleViewer, s.handleEPAMetrics))
	mux.Handle("POST /ncpdp/rtpb", s.require(store.RoleOperator, s.handleRTPB))
	mux.Handle("POST /ncpdp/script", s.require(store.RoleOperator, s.handleSCRIPT))
}

func (s *Server) drugsOff(w http.ResponseWriter, r *http.Request) bool {
	if s.Drugs == nil || s.Drugs.Formulary == nil {
		s.fail(w, r, http.StatusNotFound, "no formulary is loaded on this server (serve -formulary FILE)")
		return true
	}
	return false
}

func (s *Server) handleFormulary(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Drugs == nil || s.Drugs.Formulary == nil {
		s.ok(w, map[string]any{"enabled": false, "hint": "Start the server with -formulary FILE (Settings › Payer › Drug formulary) " +
			"to publish a drug formulary over the Da Vinci US Drug Formulary API and answer Real-Time Prescription Benefit and " +
			"NCPDP SCRIPT electronic prior authorization from it. examples/formulary/formulary.yaml is a sample."})
		return
	}
	f := s.Drugs.Formulary
	type drugRow struct {
		Formulary    string   `json:"formulary"`
		RxNorm       string   `json:"rxnorm"`
		Display      string   `json:"display"`
		NDCs         []string `json:"ndc"`
		Tier         string   `json:"tier"`
		Requirements []string `json:"requirements"`
		NotCovered   bool     `json:"notCovered"`
		PA           bool     `json:"priorAuthorization"`
		StepTherapy  bool     `json:"stepTherapy"`
		Quantity     bool     `json:"quantityLimit"`
		Questions    string   `json:"questionnaire,omitempty"`
		Alternatives []string `json:"alternatives,omitempty"`
		Notes        string   `json:"notes,omitempty"`
	}
	var drugs []drugRow
	for _, fm := range f.Formularies {
		for _, d := range fm.Drugs {
			drugs = append(drugs, drugRow{Formulary: fm.ID, RxNorm: d.RxNorm, Display: d.Display, NDCs: orEmpty(d.NDCs), Tier: d.Tier,
				Requirements: orEmpty(d.Requirements()), NotCovered: d.NotCovered, PA: d.PriorAuthorization, StepTherapy: d.StepTherapy,
				Quantity: d.QuantityLimit != nil, Questions: d.Questionnaire, Alternatives: d.Alternatives, Notes: d.Notes})
		}
	}
	epaOn := s.Drugs.EPA != nil
	s.ok(w, map[string]any{
		"enabled": true, "summary": f.Summarize(), "drugs": drugs, "formularyBase": s.Drugs.FormularyBase,
		"rtpb": s.selfURL() + "/ncpdp/rtpb", "script": s.selfURL() + "/ncpdp/script",
		"epa": epaOn, "epaDecides": epaOn && s.Drugs.EPA.Submit != nil, "replyChannel": s.Drugs.ReplyChannel,
		"payerId": f.Payer.ID,
	})
}

func (s *Server) selfURL() string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	return strings.TrimRight(s.SelfURL, "/")
}

// handleRTPBTry prices a drug from the screen, building the request a prescriber's system would send.
func (s *Server) handleRTPBTry(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.drugsOff(w, r) {
		return
	}
	var in struct {
		Member   string  `json:"member"`
		Group    string  `json:"group"`
		NDC      string  `json:"ndc"`
		Quantity float64 `json:"quantity"`
		Days     int     `json:"days"`
		Mail     bool    `json:"mail"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	body := &rtpb.RequestBody{}
	body.BenefitsCoordination.PBMMemberID, body.BenefitsCoordination.GroupID = in.Member, in.Group
	body.RequestedProduct.Product.DrugCoded.NDC = in.NDC
	body.RequestedProduct.Quantity = &rtpb.Quantity{Value: strconv.FormatFloat(in.Quantity, 'f', -1, 64), CodeListQualifier: "38"}
	body.RequestedProduct.DaysSupply = strconv.Itoa(in.Days)
	body.Pharmacy = &rtpb.Pharmacy{BusinessName: "PRICE CHECK"}
	if in.Mail {
		body.Pharmacy.PharmacyType = "M"
	}
	a := s.Drugs.RTPB.Answer(s.Drugs.Formulary, body)
	req := &rtpb.Request{Header: rtpb.Header{MessageID: "CONSOLE-" + strconv.FormatInt(time.Now().UnixMilli(), 36),
		From: rtpb.Party{Primary: rtpb.ID{Identification: "PERFUSE-CONSOLE", Qualifier: "D"}}}}
	req.Body.Request = body
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "rtpb.try", Target: in.Member + " " + in.NDC, IP: clientIP(r)})
	s.ok(w, map[string]any{"answer": a, "xml": string(rtpb.Respond(req, a, s.Drugs.Formulary.Payer.ID, time.Now()))})
}

func readXML(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNCPDP))
	if err != nil {
		http.Error(w, "the request body could not be read: "+err.Error(), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return b, true
}

func writeXML(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) handleRTPB(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.drugsOff(w, r) {
		return
	}
	raw, ok := readXML(w, r)
	if !ok {
		return
	}
	req, err := rtpb.Parse(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a := s.Drugs.RTPB.Answer(s.Drugs.Formulary, req.Body.Request)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "rtpb.request",
		Target: req.Body.Request.BenefitsCoordination.PBMMemberID + " " + req.Body.Request.RequestedProduct.Product.DrugCoded.NDC, IP: clientIP(r)})
	writeXML(w, rtpb.Respond(req, a, s.Drugs.Formulary.Payer.ID, time.Now()))
}

func (s *Server) handleSCRIPT(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.drugsOff(w, r) || s.epaOff(w, r) {
		return
	}
	raw, ok := readXML(w, r)
	if !ok {
		return
	}
	out, err := s.answerSCRIPT(r.Context(), raw, sess, clientIP(r))
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, epa.ErrNotEPA) && !strings.Contains(err.Error(), "not a SCRIPT") && !strings.Contains(err.Error(), "transaction") &&
			!strings.Contains(err.Error(), "MessageID") && !strings.Contains(err.Error(), "root element") {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeXML(w, out)
}

func (s *Server) epaOff(w http.ResponseWriter, r *http.Request) bool {
	if s.Drugs.EPA == nil {
		s.fail(w, r, http.StatusNotFound, "electronic prior authorization needs the FHIR endpoint (serve -fhir)")
		return true
	}
	return false
}

func (s *Server) answerSCRIPT(ctx context.Context, raw []byte, sess *store.Session, ip string) ([]byte, error) {
	out, err := s.Drugs.EPA.Handle(ctx, raw)
	target := ""
	if m, perr := epa.Parse(raw); perr == nil {
		kind, pa, _ := m.Kind()
		member, _ := pa.Member()
		target = string(kind) + " " + member + " " + pa.Medication.Product.DrugCoded.NDC
	}
	action := "epa.request"
	if err != nil {
		action = "epa.refused"
	}
	_ = s.Store.Audit(ctx, store.AuditEntry{Username: sess.Username, Action: action, Target: strings.TrimSpace(target), IP: ip})
	return out, err
}

// handleEPATry sends a SCRIPT message from the screen, exactly as the network endpoint would receive it.
func (s *Server) handleEPATry(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.drugsOff(w, r) || s.epaOff(w, r) {
		return
	}
	var in struct {
		XML string `json:"xml"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.answerSCRIPT(r.Context(), []byte(in.XML), sess, clientIP(r))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.ok(w, map[string]any{"xml": string(out)})
}

func (s *Server) handleEPACases(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Drugs == nil || s.Drugs.EPA == nil {
		s.ok(w, map[string]any{"cases": []epa.Case{}})
		return
	}
	cases, err := s.Drugs.EPA.Cases(r.Context(), 200)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if cases == nil {
		cases = []epa.Case{}
	}
	s.ok(w, map[string]any{"cases": cases})
}

func (s *Server) handleEPACase(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.drugsOff(w, r) || s.epaOff(w, r) {
		return
	}
	c, err := s.Drugs.EPA.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such case")
		return
	}
	s.ok(w, c)
}

func (s *Server) handleEPAResend(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.drugsOff(w, r) || s.epaOff(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.Drugs.EPA.Resend(r.Context(), id); err != nil {
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "epa.resend", Target: id, IP: clientIP(r)})
	s.ok(w, map[string]any{"sent": true})
}

func (s *Server) handleEPAMetrics(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Drugs == nil || s.Drugs.EPA == nil {
		s.ok(w, map[string]any{"enabled": false})
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 3660 {
		days = 365
	}
	m, err := s.Drugs.EPA.Metrics(r.Context(), time.Now().AddDate(0, 0, -days))
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{"enabled": true, "metrics": m, "days": days})
}
