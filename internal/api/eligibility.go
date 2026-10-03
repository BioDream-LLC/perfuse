package api

import (
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/x12"
)

// Eligibility, claim status and enrolment, as inspectors: they build or read what the caller supplies and store nothing, so viewer is
// the floor, like the attachment and prior authorisation inspectors beside them. Sending a 270 is a channel's job, where the trading
// partner, the credentials and the audit trail are.

func (s *Server) handleBuildEligibility(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req x12.EligibilityRequest
	if !s.decode(w, r, &req) {
		return
	}
	raw, err := x12.BuildEligibilityInquiry(req)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.builtX12(w, r, raw)
}

func (s *Server) handleBuildClaimStatus(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req x12.ClaimStatusRequest
	if !s.decode(w, r, &req) {
		return
	}
	raw, err := x12.BuildClaimStatusRequest(req)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.builtX12(w, r, raw)
}

// builtX12 reads a built interchange back before returning it: one this server cannot parse is not one to hand anybody.
func (s *Server) builtX12(w http.ResponseWriter, r *http.Request, raw []byte) {
	m, err := x12.Parse(raw)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	problems := m.Validate().Problems
	if problems == nil {
		problems = []x12.Problem{}
	}
	s.ok(w, map[string]any{"x12": string(raw), "segments": m.SegmentCount(), "envelopeProblems": problems,
		"basis": "Built to the HIPAA 5010 implementation guide structure (005010X279A1, 005010X212). A trading partner's companion " +
			"guide governs, and this has not been accepted by a real payer."})
}

func (s *Server) handleReadEligibility(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	m, ok := s.readX12Body(w, r)
	if !ok {
		return
	}
	e, err := m.ReadEligibility()
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.ok(w, map[string]any{"eligibility": e, "core": e.CheckCORE(),
		"coreBasis": "Checked against this program's reading of the CAQH CORE Eligibility & Benefits data content rule. It is not " +
			"CORE certification, which CAQH's authorised testing vendors carry out."})
}

func (s *Server) handleReadEnrollment(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	m, ok := s.readX12Body(w, r)
	if !ok {
		return
	}
	e, err := m.ReadEnrollment()
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.ok(w, e)
}

func (s *Server) readX12Body(w http.ResponseWriter, r *http.Request) (*x12.Message, bool) {
	var req x12Body
	if !s.decode(w, r, &req) {
		return nil, false
	}
	m, err := x12.Parse([]byte(strings.TrimSpace(req.X12)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "not a readable X12 interchange: "+err.Error())
		return nil, false
	}
	return m, true
}
