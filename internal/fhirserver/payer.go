package fhirserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms0057"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The CMS-0057 payer operations: HRex $member-match for the Payer-to-Payer API, and the Da Vinci ATR $davinci-data-export
// (with the standard Group $export beside it) for the Provider Access and Payer-to-Payer bulk APIs.
//
// The Patient Access API needs no operation of its own: it is this server's ordinary SMART-protected read and search, with
// CARIN Blue Button and PDex resources in the store.

// PayerAPIs turns the payer operations on. Nil leaves them off, and each endpoint says so rather than answering a 404 that
// looks like a typo.
type PayerAPIs struct {
	// RequireConsent makes $member-match refuse a request without an active, permitting Consent. On by default in
	// serve, because the Payer-to-Payer API is opt-in for the member.
	RequireConsent bool

	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time
}

func (p *PayerAPIs) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}

	return time.Now()
}

// The export types PDex and ATR define. Each decides what is excluded on the way out.
const (
	exportTypeATR            = "hl7.fhir.us.davinci-atr"
	exportTypeProviderDelta  = "hl7.fhir.us.davinci-pdex#provider-delta"
	exportTypeProviderDownld = "hl7.fhir.us.davinci-pdex#provider-download"
	exportTypePayerToPayer   = "hl7.fhir.us.davinci-pdex#payertopayer"
)

func (s *Server) registerPayerAPIs(mux *http.ServeMux) {
	mux.HandleFunc("POST /Patient/$member-match", s.handleMemberMatch)
	mux.HandleFunc("GET /Group/{id}/$davinci-data-export", s.handleGroupExport)
	mux.HandleFunc("POST /Group/{id}/$davinci-data-export", s.handleGroupExport)
	mux.HandleFunc("GET /Group/{id}/$export", s.handleGroupExport)
}

func (s *Server) payerOff(w http.ResponseWriter, r *http.Request) bool {
	if s.Payer != nil {
		return false
	}
	s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
		"the CMS-0057 payer operations are not enabled on this server; start it with -fhir-payer-apis")

	return true
}

// handleMemberMatch answers POST /Patient/$member-match.
func (s *Server) handleMemberMatch(w http.ResponseWriter, r *http.Request) {
	if s.payerOff(w, r) {
		return
	}
	caller := CallerFrom(r.Context())
	if callerLimitedToOnePatient(caller) {
		// A member's own app has no business asking which of this payer's members someone else is.
		s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden",
			"$member-match is a payer-to-payer operation and cannot be called with a token limited to one patient")
		return
	}
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var params map[string]any
	if err := json.Unmarshal(body, &params); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body is not JSON: "+err.Error())
		return
	}
	req, err := cms0057.ParseMatchRequest(params)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "required", err.Error())
		return
	}
	if s.Payer.RequireConsent {
		if err := cms0057.CheckConsent(req.Consent, s.Payer.now()); err != nil {
			// 422 is what HRex specifies for a request the server understood and will not satisfy.
			s.writeOutcome(w, r, http.StatusUnprocessableEntity, fhir.SeverityError, "business-rule", err.Error())
			return
		}
	}

	candidates, err := s.matchCandidates(r.Context(), req)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	found, why := cms0057.Match(req, candidates)
	s.Log.Info("member-match", "caller", callerName(caller), "candidates", len(candidates), "matched", found != nil)
	if found == nil {
		s.writeOutcome(w, r, http.StatusUnprocessableEntity, fhir.SeverityError, "business-rule", why)
		return
	}
	patientID, _ := found.Patient["id"].(string)
	s.writeJSON(w, http.StatusOK, cms0057.MatchResponse(found, patientID))
}

func callerName(c *Caller) string {
	if c == nil {
		return ""
	}

	return c.Name
}

// matchCandidates finds the coverages the request's identifiers point at, with the patient each one covers.
func (s *Server) matchCandidates(ctx context.Context, req *cms0057.MatchRequest) ([]cms0057.MatchCandidate, error) {
	sub, idents := req.MatchKeys()
	seen := map[string]bool{}
	var out []cms0057.MatchCandidate
	look := func(param, value string) error {
		if value == "" {
			return nil
		}
		q, err := ParseSearch("Coverage", map[string][]string{param: {value}, "_count": {"50"}})
		if err != nil {
			return err
		}
		res, err := s.Store.Search(ctx, q)
		if err != nil {
			return err
		}
		for _, c := range res.Resources {
			if seen[c.ResourceID()] {
				continue
			}
			seen[c.ResourceID()] = true
			cov, err := asTree(c)
			if err != nil {
				return err
			}
			ref, _ := asMapAny(cov["beneficiary"])["reference"].(string)
			pid, ok := strings.CutPrefix(ref, "Patient/")
			if !ok {
				continue
			}
			p, err := s.Store.Get(ctx, "Patient", pid)
			if err != nil || p == nil {
				continue
			}
			pt, err := asTree(p)
			if err != nil {
				return err
			}
			out = append(out, cms0057.MatchCandidate{Coverage: cov, Patient: pt})
		}
		return nil
	}
	if err := look("subscriber-id", sub); err != nil {
		return nil, err
	}
	for _, id := range idents {
		if err := look("identifier", id); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// handleGroupExport answers $davinci-data-export and $export on a Group.
func (s *Server) handleGroupExport(w http.ResponseWriter, r *http.Request) {
	if s.payerOff(w, r) {
		return
	}
	if s.Export == nil {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			"a Group export is a bulk export, and bulk export is not enabled on this server; start it with -fhir-bulk-export")
		return
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Prefer")), "respond-async") {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid",
			"a Group export is asynchronous, so this needs the header Prefer: respond-async")
		return
	}
	caller := CallerFrom(r.Context())
	if callerLimitedToOnePatient(caller) {
		s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden",
			"a Group export covers many members and cannot be called with a token limited to one patient")
		return
	}

	id := r.PathValue("id")
	g, err := s.Store.Get(r.Context(), "Group", id)
	if err != nil || g == nil {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found", fmt.Sprintf("Group/%s does not exist", id))
		return
	}
	if !callerMayUseGroup(caller, id) {
		// Not found rather than forbidden: a provider's token should not learn which other attribution lists exist.
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found", fmt.Sprintf("Group/%s does not exist", id))
		return
	}
	if !permitsResource(caller, g) {
		s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden", "this token may not read that Group")
		return
	}
	group, err := asTree(g)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	params, err := exportParams(r)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid", err.Error())
		return
	}

	members := cms0057.GroupMembers(group, s.Payer.now())
	if len(params.patients) > 0 {
		// The patient parameter narrows to members of the Group; it cannot add someone who is not one.
		in := map[string]bool{}
		for _, m := range members {
			in[m] = true
		}
		var narrowed []string
		for _, p := range params.patients {
			if !in[p] {
				s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid",
					fmt.Sprintf("Patient/%s is not a current member of Group/%s", p, id))
				return
			}
			narrowed = append(narrowed, p)
		}
		members = narrowed
	}

	audience := cms0057.ProviderAccess
	switch params.exportType {
	case exportTypePayerToPayer:
		audience = cms0057.PayerToPayer
	case "", exportTypeATR, exportTypeProviderDelta, exportTypeProviderDownld:
	default:
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "not-supported",
			fmt.Sprintf("exportType %q is not one this server knows; use %s, %s or %s", params.exportType,
				exportTypeProviderDelta, exportTypeProviderDownld, exportTypePayerToPayer))
		return
	}

	// Members who opted out of provider access are left out of a provider export entirely.
	optedOut := 0
	if audience == cms0057.ProviderAccess {
		kept, n, err := s.withoutOptOuts(r.Context(), members)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		members, optedOut = kept, n
	}
	if len(members) == 0 {
		s.writeOutcome(w, r, http.StatusUnprocessableEntity, fhir.SeverityError, "business-rule",
			fmt.Sprintf("Group/%s has no current members whose data may be exported (%d opted out)", id, optedOut))
		return
	}

	req := ExportRequest{
		RequestURL: r.URL.String(),
		Types:      params.types,
		Since:      params.since,
		PatientIDs: members,
		Filter:     func(m map[string]any) map[string]any { return cms0057.Redact(m, audience) },
	}
	job, err := s.Export.Start(r.Context(), req)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid", err.Error())
		return
	}
	s.Log.Info("group export", "group", id, "members", len(members), "opted_out", optedOut,
		"audience", string(audience), "caller", callerName(caller))

	status := fmt.Sprintf("%s/_export/%s/status", strings.TrimRight(s.BaseURL, "/"), job.ID)
	w.Header().Set("Content-Location", status)
	w.WriteHeader(http.StatusAccepted)
}

type groupExportParams struct {
	types      []string
	since      string
	exportType string
	patients   []string
}

// exportParams reads the operation's parameters from the query string, or from a Parameters body on POST.
func exportParams(r *http.Request) (*groupExportParams, error) {
	p := &groupExportParams{}
	add := func(name, value string) error {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		switch name {
		case "_type":
			for _, t := range strings.Split(value, ",") {
				if t = strings.TrimSpace(t); t != "" {
					p.types = append(p.types, t)
				}
			}
		case "_since":
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return fmt.Errorf("_since must be an instant such as 2026-01-01T00:00:00Z, and %q is not", value)
			}
			p.since = value
		case "exportType":
			p.exportType = value
		case "patient":
			id, ok := strings.CutPrefix(value, "Patient/")
			if !ok {
				return fmt.Errorf("patient must be a Patient reference, and %q is not", value)
			}
			p.patients = append(p.patients, id)
		case "_until", "_typeFilter":
			return fmt.Errorf("%s is not supported by this server's Group export", name)
		}
		return nil
	}
	for name, values := range r.URL.Query() {
		for _, v := range values {
			if err := add(name, v); err != nil {
				return nil, err
			}
		}
	}
	if r.Method == http.MethodPost && r.ContentLength != 0 {
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody)).Decode(&body); err != nil {
			return nil, fmt.Errorf("the body is not a Parameters resource: %w", err)
		}
		for _, raw := range asSliceAny(body["parameter"]) {
			m := asMapAny(raw)
			name, _ := m["name"].(string)
			for _, k := range []string{"valueString", "valueCanonical", "valueInstant", "valueUri"} {
				if v, ok := m[k].(string); ok {
					if err := add(name, v); err != nil {
						return nil, err
					}
				}
			}
			if ref, ok := asMapAny(m["valueReference"])["reference"].(string); ok {
				if err := add(name, ref); err != nil {
					return nil, err
				}
			}
		}
	}

	return p, nil
}

// withoutOptOuts removes members holding an active Provider Access opt-out Consent.
func (s *Server) withoutOptOuts(ctx context.Context, members []string) ([]string, int, error) {
	now := s.Payer.now()
	out := make([]string, 0, len(members))
	removed := 0
	for _, m := range members {
		q, err := ParseSearch("Consent", map[string][]string{"patient": {m}, "_count": {"100"}})
		if err != nil {
			return nil, 0, err
		}
		res, err := s.Store.Search(ctx, q)
		if err != nil {
			return nil, 0, err
		}
		opted := false
		for _, c := range res.Resources {
			tree, err := asTree(c)
			if err != nil {
				return nil, 0, err
			}
			if cms0057.ProviderOptedOut(tree, now) {
				opted = true
				break
			}
		}
		if opted {
			removed++
			continue
		}
		out = append(out, m)
	}

	return out, removed, nil
}

func asTree(r fhir.Resource) (map[string]any, error) {
	raw, err := fhir.Marshal(r, fhir.CanonicalVersion)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	return m, nil
}

func asMapAny(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}

	return m
}

func asSliceAny(v any) []any {
	s, _ := v.([]any)

	return s
}

// callerMayUseGroup applies a token's Group limit. A caller with no limit may use any Group.
func callerMayUseGroup(c *Caller, id string) bool {
	if c == nil || len(c.Groups) == 0 {
		return true
	}
	for _, g := range c.Groups {
		if g == id {
			return true
		}
	}

	return false
}

// limitToGroups confines a Group-limited caller to the Provider Access surface: reading its own Groups, exporting them, and
// fetching what the export produced.
//
// Everything else is refused, not narrowed. A provider token holds no patient context, so an ordinary search would be a search
// over every member the payer has; narrowing each query to the Groups' members would be a second access-control system to get
// right, and the rule asks for bulk access only.
func (s *Server) limitToGroups(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := CallerFrom(r.Context())
		if c == nil || len(c.Groups) == 0 || groupLimitedPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden",
			"this token is limited to its provider's Groups, and may only read them and run $davinci-data-export or $export on them")
	})
}

func groupLimitedPath(r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "metadata":
		return true
	case len(parts) >= 2 && parts[0] == "_export":
		// Poll, cancel and download. Job ids are unguessable and returned only to whoever started the job.
		return true
	case len(parts) == 2 && parts[0] == "Group" && r.Method == http.MethodGet:
		return callerMayUseGroup(CallerFrom(r.Context()), parts[1])
	case len(parts) == 3 && parts[0] == "Group" && (parts[2] == "$davinci-data-export" || parts[2] == "$export"):
		// The handler applies the Group limit itself, answering not-found for someone else's Group.
		return true
	}

	return false
}
