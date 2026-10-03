package api

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/dsdr"
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
	// Sign applies an HL7 DSDR electronic signature to a C-CDA before it goes in - the signature standard CMS-0053 adopts for
	// claims attachments.
	Sign *attachmentSign `json:"sign"`
}

type attachmentSign struct {
	Role        string `json:"role"`        // NUCC taxonomy code
	RoleDisplay string `json:"roleDisplay"` // its name
	Purpose     string `json:"purpose"`     // ASTM E1762 code from the guide's Appendix E
	As          string `json:"as"`          // legalAuthenticator, authenticator, or empty to choose
	SignerName  string `json:"signerName"`
	NPI         string `json:"npi"`
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
	// Signatures are the DSDR signatures on a C-CDA, each checked.
	Signatures []signatureView `json:"signatures"`
}

type signatureView struct {
	dsdr.Report
	Sound bool `json:"sound"`
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

func viewAttachment(m *x12.Message, roots *x509.CertPool) (*attachmentView, error) {
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
		sigs := []signatureView{}
		if bytes.Contains(doc, []byte("signatureText")) {
			if reports, err := dsdr.Verify(doc, dsdr.VerifyOptions{Roots: roots}); err == nil {
				for _, r := range reports {
					sigs = append(sigs, signatureView{Report: r, Sound: r.Sound()})
				}
			}
		}
		v.Documents = append(v.Documents, attachmentDocumentView{
			TraceType: d.TraceType, TraceNumber: d.TraceNumber, Category: d.Category, Transmission: d.Transmission,
			Filter: d.Filter, ContentType: d.ContentType, Filename: d.Filename, Size: len(doc),
			DocumentBase64: base64.StdEncoding.EncodeToString(doc), Notes: notes, Signatures: sigs,
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

	var signed *dsdr.Result
	if req.Sign != nil {
		// Signing commits the organisation's key, which is why /api/document/sign is an admin action; building a 275 is not.
		if !sess.Role.AtLeast(store.RoleAdmin) {
			s.fail(w, r, http.StatusForbidden, "signing with this server's key needs the admin role; build the 275 unsigned, or ask an admin")
			return
		}
		chain, key, err := s.signingChain()
		if err != nil {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		signed, err = dsdr.Sign(req.Document, dsdr.Options{Key: key, Chain: chain, Role: req.Sign.Role, RoleDisplay: req.Sign.RoleDisplay,
			Purpose: req.Sign.Purpose, As: req.Sign.As, SignerName: req.Sign.SignerName, NPI: req.Sign.NPI, TSAURL: s.TSAURL})
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		req.Document = signed.Document
		if req.ContentType == "" {
			req.ContentType = "text/xml"
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "attachment.sign", IP: clientIP(r),
			Detail: fmt.Sprintf("DSDR %s signature, XAdES-%s, as %s", signed.Participant, signed.Level, chain[0].Subject.CommonName)})
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
	view, err := viewAttachment(m, nil)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	basis := "Built from the published CMS esMD and UnitedHealthcare companion guides for 006020X314, not validated against the X12 TR3. A trading partner's companion guide governs."
	if signed == nil {
		basis += " No electronic signature is applied."
	}
	out := map[string]any{
		"x12":       string(raw),
		"bytes":     len(raw),
		"readBack":  view,
		"basis":     basis,
		"roundTrip": len(view.Documents) == 1 && view.Documents[0].DocumentBase64 == base64.StdEncoding.EncodeToString(req.Document),
	}
	if signed != nil {
		out["signature"] = signed
	}
	s.ok(w, out)
}

type x12Body struct {
	X12 string `json:"x12"`
	// TrustPEM is the CA certificates a recipient trusts for signatures on the documents, optional.
	TrustPEM string `json:"trustPem"`
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
	var roots *x509.CertPool
	if strings.TrimSpace(req.TrustPEM) != "" {
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(req.TrustPEM)) {
			s.fail(w, r, http.StatusBadRequest, "no certificates could be read from the trust store; paste PEM certificates")
			return
		}
	}
	view, err := viewAttachment(m, roots)
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
