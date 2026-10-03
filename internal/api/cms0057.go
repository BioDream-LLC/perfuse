package api

import (
	"encoding/json"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/crd"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms0057"
	"github.com/biodream-llc/perfuse/internal/store"
)

// CMS-0057 for payers: the readiness of the four APIs the rule requires on this instance, and the three conversions behind
// them. The conversions are inspectors in the same sense as the rest of /api/x12 - they transform what is pasted, store
// nothing and send nothing - so viewer is the floor.

// CMS0057Status is what the serve command knows about the FHIR endpoint, which decides whether each API can answer.
type CMS0057Status struct {
	FHIR       bool   `json:"fhir"`
	BaseURL    string `json:"baseUrl"`
	SMART      bool   `json:"smart"`
	BulkExport bool   `json:"bulkExport"`
	PayerAPIs  bool   `json:"payerApis"`
	Consent    bool   `json:"consentRequired"`
	ReadOnly   bool   `json:"readOnly"`
}

type cms0057API struct {
	Name      string   `json:"name"`
	Rule      string   `json:"rule"`
	Deadline  string   `json:"deadline"`
	Ready     bool     `json:"ready"`
	Endpoints []string `json:"endpoints"`
	Guides    []string `json:"guides"`
	Missing   []string `json:"missing"`
	// Note is advice that does not decide readiness: something a deployment needs that this instance cannot check for.
	Note string `json:"note,omitempty"`
}

func (s *Server) handleCMS0057Status(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	st := s.CMS0057
	base := strings.TrimRight(st.BaseURL, "/")
	need := func(cond bool, what string, list *[]string) {
		if !cond {
			*list = append(*list, what)
		}
	}

	patient := cms0057API{Name: "Patient Access API", Rule: "42 CFR 422.119, 431.60, 457.730, 45 CFR 156.221",
		Deadline:  "Prior authorization data from 1 January 2027",
		Endpoints: []string{base + "/ExplanationOfBenefit", base + "/Coverage", base + "/Patient", base + "/.well-known/smart-configuration"},
		Guides:    []string{"CARIN Blue Button " + cms0057.CARINVersion, "PDex " + cms0057.PDexVersion, "US Core", "SMART App Launch"}}
	need(st.FHIR, "the FHIR endpoint (-fhir)", &patient.Missing)
	need(st.SMART, "SMART on FHIR authorization (-smart-issuer), so members sign in through an app of their choosing", &patient.Missing)

	provider := cms0057API{Name: "Provider Access API", Rule: "42 CFR 422.121(a), 431.61(a), 457.731(a), 45 CFR 156.222(a)",
		Deadline:  "1 January 2027",
		Endpoints: []string{base + "/Group/{id}/$davinci-data-export"},
		Guides:    []string{"PDex " + cms0057.PDexVersion, "Da Vinci ATR " + cms0057.ATRVersion, "Bulk Data"}}
	need(st.FHIR, "the FHIR endpoint (-fhir)", &provider.Missing)
	need(st.PayerAPIs, "the payer operations (-fhir-payer-apis)", &provider.Missing)
	need(st.BulkExport, "bulk export (-fhir-bulk-export)", &provider.Missing)
	provider.Note = s.providerTokenNote(r, sess)

	p2p := cms0057API{Name: "Payer-to-Payer API", Rule: "42 CFR 422.121(b), 431.61(b), 457.731(b), 45 CFR 156.222(b)",
		Deadline:  "1 January 2027",
		Endpoints: []string{base + "/Patient/$member-match", base + "/Group/{id}/$davinci-data-export?exportType=hl7.fhir.us.davinci-pdex%23payertopayer"},
		Guides:    []string{"HRex " + cms0057.HRexVersion + " $member-match", "PDex " + cms0057.PDexVersion, "Bulk Data"}}
	need(st.FHIR, "the FHIR endpoint (-fhir)", &p2p.Missing)
	need(st.PayerAPIs, "the payer operations (-fhir-payer-apis)", &p2p.Missing)
	need(st.Consent, "member consent checking (remove -fhir-member-match-without-consent)", &p2p.Missing)

	pa := cms0057API{Name: "Prior Authorization API", Rule: "42 CFR 422.122, 431.80, 457.732, 45 CFR 156.223",
		Deadline:  "1 January 2027; decision timeframes and public metrics from 1 January 2026",
		Endpoints: []string{base + "/ClaimResponse", base + "/Claim"},
		Guides: []string{"Da Vinci PAS " + cms0057.PASVersion + " with X12 278 translation", "Da Vinci CRD " + crd.Version +
			" over CDS Hooks", "Da Vinci DTR 2.1.0 $questionnaire-package"}}
	pa.Endpoints = append(pa.Endpoints, strings.TrimSuffix(base, "/fhir")+"/cds-services", base+"/Questionnaire/$questionnaire-package")
	need(st.FHIR, "the FHIR endpoint (-fhir)", &pa.Missing)
	need(s.CRD != nil, "coverage requirements rules for CRD (-crd-rules)", &pa.Missing)
	need(!st.ReadOnly, "a writable FHIR endpoint, so PAS requests can be received", &pa.Missing)

	apis := []cms0057API{patient, provider, p2p, pa}
	for i := range apis {
		apis[i].Ready = len(apis[i].Missing) == 0
		if apis[i].Missing == nil {
			apis[i].Missing = []string{}
		}
	}
	s.ok(w, map[string]any{"apis": apis, "status": st})
}

type carinRequest struct {
	Claims           string `json:"claims"`
	Remittance       string `json:"remittance"`
	IdentifierSystem string `json:"identifierSystem"`
	NetworkStatus    string `json:"networkStatus"`
}

func (s *Server) handleCARIN(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req carinRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.NetworkStatus != "" && req.NetworkStatus != "innetwork" && req.NetworkStatus != "outofnetwork" {
		s.fail(w, r, http.StatusBadRequest, "network status must be innetwork or outofnetwork")
		return
	}
	results, skipped, err := cms0057.ConvertClaims([]byte(strings.TrimSpace(req.Claims)), []byte(strings.TrimSpace(req.Remittance)),
		cms0057.CARINOptions{IdentifierSystem: strings.TrimSpace(req.IdentifierSystem), NetworkStatus: req.NetworkStatus})
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if skipped == nil {
		skipped = []string{}
	}
	for i := range results {
		if results[i].Notes == nil {
			results[i].Notes = []string{}
		}
	}
	s.ok(w, map[string]any{"results": results, "skipped": skipped})
}

type priorAuthRequest struct {
	Response string `json:"response"`
	Claim    string `json:"claim"`
}

func (s *Server) handlePDexPriorAuth(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req priorAuthRequest
	if !s.decode(w, r, &req) {
		return
	}
	res, err := cms0057.PriorAuthFromJSON([]byte(req.Response), []byte(strings.TrimSpace(req.Claim)), time.Now())
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if res.Notes == nil {
		res.Notes = []string{}
	}
	s.ok(w, res)
}

type metricsRequest struct {
	Decisions      string `json:"decisions"`
	Services       string `json:"services"`
	Year           int    `json:"year"`
	Organization   string `json:"organization"`
	Contact        string `json:"contact"`
	StandardDays   int    `json:"standardDays"`
	ExpeditedHours int    `json:"expeditedHours"`
}

func (s *Server) handlePAMetrics(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req metricsRequest
	if !s.decode(w, r, &req) {
		return
	}
	decisions, err := cms0057.ParseDecisionsCSV(strings.NewReader(req.Decisions))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	opt := cms0057.MetricsOptions{Year: req.Year, Organization: strings.TrimSpace(req.Organization), Contact: strings.TrimSpace(req.Contact)}
	if req.StandardDays > 0 {
		opt.StandardDeadline = time.Duration(req.StandardDays) * 24 * time.Hour
	}
	if req.ExpeditedHours > 0 {
		opt.ExpeditedDeadline = time.Duration(req.ExpeditedHours) * time.Hour
	}
	if strings.TrimSpace(req.Services) != "" {
		if opt.Services, err = cms0057.ParseServicesCSV(strings.NewReader(req.Services)); err != nil {
			s.fail(w, r, http.StatusBadRequest, "the services list: "+err.Error())
			return
		}
	}
	report, err := cms0057.BuildMetrics(decisions, opt)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if report.DataQuality == nil {
		report.DataQuality = []string{}
	}
	page, err := cms0057.MetricsHTML(report)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	raw, _ := json.Marshal(report)
	var view map[string]any
	_ = json.Unmarshal(raw, &view)
	s.ok(w, map[string]any{"report": view, "html": string(page), "csv": string(cms0057.MetricsCSV(report))})
}

// providerTokenNote says whether any provider can be given only its own attribution list.
//
// Not a readiness condition: a SMART issuer may be how providers authenticate, and that is outside what this instance can see. But an
// unlimited token can export every Group, so the card says how many tokens are limited rather than leaving it to be found out.
func (s *Server) providerTokenNote(r *http.Request, sess *store.Session) string {
	tokens, err := s.storeFor(sess).ListAPITokens(r.Context())
	if err != nil {
		return ""
	}
	limited := 0
	for _, t := range tokens {
		if !t.Revoked() && len(t.FHIRGroups) > 0 {
			limited++
		}
	}
	switch limited {
	case 0:
		return "No API token is limited to FHIR Groups yet. An unlimited token can export every provider's attribution list: " +
			"issue each provider one limited to its own Groups, under Users → Machine credentials."
	case 1:
		return "1 API token is limited to FHIR Groups, for a provider's attribution list."
	default:
		return fmt.Sprintf("%d API tokens are limited to FHIR Groups, for providers' attribution lists.", limited)
	}
}
