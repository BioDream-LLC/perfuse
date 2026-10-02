package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/pas278"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/x12"
)

// Claims attachments and prior authorisation.
//
// Inspectors in the same sense as /api/inspect: they transform what the caller supplies and store nothing, so viewer is the
// floor. Building a 275 sends nothing anywhere - the interchange comes back in the response for the caller to deliver through a
// channel, which is where the trading partner, the credentials and the audit trail are.

type attachmentBuildRequest struct {
	x12.AttachmentRequest
	// DocumentBase64 carries a binary document; DocumentText a text one such as a C-CDA. Exactly one.
	DocumentBase64 string `json:"documentBase64"`
	DocumentText   string `json:"documentText"`
}

type attachmentDocumentView struct {
	TraceType      string   `json:"traceType"`
	TraceNumber    string   `json:"traceNumber"`
	Category       string   `json:"category"`
	Transmission   string   `json:"transmission"`
	Filter         string   `json:"filter"`
	ContentType    string   `json:"contentType"`
	Filename       string   `json:"filename"`
	Size           int      `json:"size"`
	DocumentBase64 string   `json:"documentBase64"`
	Notes          []string `json:"notes"`
}

type attachmentView struct {
	Version         string                   `json:"version"`
	Purpose         string                   `json:"purpose"`
	Reference       string                   `json:"reference"`
	Payer           x12.Party                `json:"payer"`
	Provider        x12.Party                `json:"provider"`
	Submitter       x12.Party                `json:"submitter"`
	Patient         x12.Party                `json:"patient"`
	ProviderClaimID string                   `json:"providerClaimId"`
	Documents       []attachmentDocumentView `json:"documents"`
}

func viewAttachment(m *x12.Message) (*attachmentView, error) {
	a, err := m.ParseAttachment()
	if err != nil {
		return nil, err
	}
	v := &attachmentView{
		Version: m.Version(), Purpose: a.Purpose, Reference: a.TransactionID,
		Payer: a.Payer, Provider: a.Provider, Submitter: a.Submitter, Patient: a.Patient,
		ProviderClaimID: a.ProviderClaimID, Documents: []attachmentDocumentView{},
	}
	for _, d := range a.Documents {
		doc := d.Document
		if doc == nil {
			doc = d.Payload // the 5010 form, where BIN carries the document itself
		}
		notes := d.Notes
		if notes == nil {
			notes = []string{}
		}
		v.Documents = append(v.Documents, attachmentDocumentView{
			TraceType: d.TraceType, TraceNumber: d.TraceNumber, Category: d.Category, Transmission: d.Transmission,
			Filter: d.Filter, ContentType: d.ContentType, Filename: d.Filename, Size: len(doc),
			DocumentBase64: base64.StdEncoding.EncodeToString(doc), Notes: notes,
		})
	}
	return v, nil
}

func (s *Server) handleBuildAttachment(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req attachmentBuildRequest
	if !s.decode(w, r, &req) {
		return
	}
	switch {
	case req.DocumentBase64 != "" && req.DocumentText != "":
		s.fail(w, r, http.StatusBadRequest, "send the document as documentBase64 or documentText, not both")
		return
	case req.DocumentBase64 != "":
		doc, err := base64.StdEncoding.DecodeString(req.DocumentBase64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "documentBase64 is not base64: "+err.Error())
			return
		}
		req.Document = doc
	default:
		req.Document = []byte(req.DocumentText)
	}

	raw, err := x12.BuildAttachment6020(req.AttachmentRequest)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Read back before returning. An interchange this server cannot parse is not one to hand anybody, and the parsed view is what
	// the page shows as proof that the document inside is the one that went in.
	m, err := x12.Parse(raw)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	view, err := viewAttachment(m)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{
		"x12":       string(raw),
		"bytes":     len(raw),
		"readBack":  view,
		"basis":     "Built from the published CMS esMD and UnitedHealthcare companion guides for 006020X314, not validated against the X12 TR3. A trading partner's companion guide governs, and no electronic signature is applied.",
		"roundTrip": len(view.Documents) == 1 && view.Documents[0].DocumentBase64 == base64.StdEncoding.EncodeToString(req.Document),
	})
}

type x12Body struct {
	X12 string `json:"x12"`
}

func (s *Server) handleReadAttachment(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req x12Body
	if !s.decode(w, r, &req) {
		return
	}
	m, err := x12.Parse([]byte(strings.TrimSpace(req.X12)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "not a readable X12 interchange: "+err.Error())
		return
	}
	if !m.IsAttachment() {
		s.fail(w, r, http.StatusBadRequest, "this interchange carries no 275; it contains "+strings.Join(m.TransactionSets(), ", "))
		return
	}
	view, err := viewAttachment(m)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.ok(w, view)
}

func (s *Server) handlePriorAuthResponse(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req x12Body
	if !s.decode(w, r, &req) {
		return
	}
	m, err := x12.Parse([]byte(strings.TrimSpace(req.X12)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "not a readable X12 interchange: "+err.Error())
		return
	}
	if !m.IsServiceReview() {
		s.fail(w, r, http.StatusBadRequest, "this interchange carries no 278; it contains "+strings.Join(m.TransactionSets(), ", "))
		return
	}
	review, err := m.ParseServiceReview()
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	result := pas278.FromReview(review)
	cr := pas278.ToClaimResponse(result)
	pretty, err := json.MarshalIndent(cr, "", "  ")
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	decisions := make([]map[string]any, 0, len(result.Authorisations))
	for _, a := range result.Authorisations {
		decision, lost := pas278.Narrow(a.Decision)
		decisions = append(decisions, map[string]any{
			"decision": string(a.Decision), "narrowed": decision, "lostInNarrowing": lost,
			"number": a.Number, "reasonCode": a.ReasonCode, "service": a.ServiceCode, "trace": a.TraceNumber,
		})
	}
	notes := result.Notes
	if notes == nil {
		notes = []string{}
	}
	s.ok(w, map[string]any{
		"claimResponse": json.RawMessage(pretty),
		"summary":       pas278.SummariseDecisions(result),
		"decisions":     decisions,
		"notes":         notes,
		"profile":       "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claimresponse (PAS 2.2.1)",
	})
}
