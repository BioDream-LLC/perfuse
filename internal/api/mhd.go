package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/xds"
)

// IHE MHD (Mobile access to Health Documents) over the XDS registry, at /mhd: ITI-65 Provide Document Bundle, ITI-66 Find
// Lists, ITI-67 Find Document References and ITI-68 Retrieve Document. A document provided over MHD is registered in the
// same registry as one provided over XDS.b, and found by either. PIXm (ITI-83, Patient/$ihe-pix) and PDQm (ITI-78, Patient
// search) answer from the FHIR store's Patients, at /mhd as well, so one base serves a mobile client.
//
// The same roles as XDS: the viewer role to find, read and look patients up; the operator role to provide.

func (s *Server) registerMHD(mux *recordingMux) {
	if s.XDS == nil {
		// A patient index is a PIX manager on its own, with or without a document registry.
		if s.PatientIndex != nil {
			mux.Handle("GET /mhd/Patient/$ihe-pix", s.require(store.RoleViewer, s.handlePIXm))
		}
		return
	}
	mux.Handle("POST /mhd", s.require(store.RoleOperator, s.handleMHDProvide))
	mux.Handle("POST /mhd/", s.require(store.RoleOperator, s.handleMHDProvide))
	mux.Handle("GET /mhd/metadata", s.require(store.RoleViewer, s.handleMHDCapability))
	mux.Handle("GET /mhd/DocumentReference", s.require(store.RoleViewer, s.handleMHDFind))
	mux.Handle("GET /mhd/DocumentReference/{id}", s.require(store.RoleViewer, s.handleMHDRead))
	mux.Handle("GET /mhd/List", s.require(store.RoleViewer, s.handleMHDLists))
	mux.Handle("GET /mhd/Binary/{uid}", s.require(store.RoleViewer, s.handleMHDBinary))
	mux.Handle("GET /mhd/Patient/$ihe-pix", s.require(store.RoleViewer, s.handlePIXm))
	mux.Handle("GET /mhd/Patient", s.require(store.RoleViewer, s.handlePDQm))
	mux.Handle("GET /mhd/Patient/{id}", s.require(store.RoleViewer, s.handlePDQmRead))
}

func (s *Server) mhdBase() string { return s.selfURL() + "/mhd" }

func fhirJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fhirFail answers with an OperationOutcome of one issue.
func fhirFail(w http.ResponseWriter, code int, issue, text string) {
	fhirJSON(w, code, map[string]any{"resourceType": "OperationOutcome", "issue": []any{map[string]any{"severity": "error", "code": issue,
		"diagnostics": text}}})
}

func (s *Server) mhdScoped(w http.ResponseWriter, sess *store.Session) bool {
	if sess.TenantID != store.DefaultTenant || len(sess.ChannelGroups) > 0 {
		fhirFail(w, http.StatusForbidden, "forbidden", "the document registry belongs to the whole server; an "+
			"account limited to a tenant or to channel groups cannot use it")
		return false
	}
	return true
}

func (s *Server) handleMHDCapability(w http.ResponseWriter, _ *http.Request, _ *store.Session) {
	res := func(t string, params ...string) map[string]any {
		var sp []any
		for _, p := range params {
			sp = append(sp, map[string]any{"name": p, "type": map[bool]string{true: "date", false: "token"}[p == "date" || p == "creation" || p == "period"]})
		}
		return map[string]any{"type": t, "interaction": []any{map[string]any{"code": "read"}, map[string]any{"code": "search-type"}}, "searchParam": sp}
	}
	fhirJSON(w, http.StatusOK, map[string]any{"resourceType": "CapabilityStatement", "status": "active", "kind": "instance",
		"date": time.Now().UTC().Format("2006-01-02"), "fhirVersion": "4.0.1", "format": []any{"application/fhir+json"},
		"instantiates": []any{"https://profiles.ihe.net/ITI/MHD/CapabilityStatement/IHE.MHD.DocumentRecipient",
			"https://profiles.ihe.net/ITI/MHD/CapabilityStatement/IHE.MHD.DocumentResponder",
			"https://profiles.ihe.net/ITI/PIXm/CapabilityStatement/IHE.PIXm.Manager",
			"https://profiles.ihe.net/ITI/PDQm/CapabilityStatement/IHE.PDQm.Supplier"},
		"implementation": map[string]any{"description": "Perfuse MHD, PIXm and PDQm over its XDS registry", "url": s.mhdBase()},
		"rest": []any{map[string]any{"mode": "server", "interaction": []any{map[string]any{"code": "transaction"}},
			"resource": []any{res("DocumentReference", "patient", "patient.identifier", "status", "category", "type", "creation", "date", "period", "format", "security-label"),
				res("List", "patient", "patient.identifier", "status", "code", "date"), res("Binary"),
				map[string]any{"type": "Patient", "interaction": []any{map[string]any{"code": "read"}, map[string]any{"code": "search-type"}},
					"operation": []any{map[string]any{"name": "ihe-pix", "definition": "https://profiles.ihe.net/ITI/PIXm/OperationDefinition/IHE.PIXm.pix"}}}}}}})
}

// patientCX resolves a Patient reference in the FHIR store to the identifier XDS registers it by: the first identifier whose
// system is urn:oid:.
func (s *Server) patientCX(ctx context.Context, ref string) string {
	if s.FHIRStore == nil || ref == "" {
		return ""
	}
	id := ref[strings.LastIndex(ref, "/")+1:]
	res, err := s.FHIRStore.Get(ctx, "Patient", id)
	if err != nil {
		return ""
	}
	p, ok := res.(*fhir.Patient)
	if !ok {
		return ""
	}
	for _, i := range p.Identifier {
		if strings.HasPrefix(i.System, "urn:oid:") && i.Value != "" {
			return i.Value + "^^^&" + strings.TrimPrefix(i.System, "urn:oid:") + "&ISO"
		}
	}
	return ""
}

// handleMHDProvide is ITI-65: the Bundle is registered in the XDS registry, and the answer is a transaction-response
// Bundle naming the DocumentReferences and the List as the registry now knows them.
func (s *Server) handleMHDProvide(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mhdScoped(w, sess) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, xds.MaxMessage))
	if err != nil {
		fhirFail(w, http.StatusRequestEntityTooLarge, "too-costly", "the Bundle is too large")
		return
	}
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		fhirFail(w, http.StatusBadRequest, "structure", "the body is not JSON: "+err.Error())
		return
	}
	// A Patient the Bundle carries itself (fullUrl urn:uuid:...) is resolved from the Bundle; otherwise from the FHIR store.
	inBundle := map[string]string{}
	for _, e := range sliceOf(bundle["entry"]) {
		em, _ := e.(map[string]any)
		res, _ := em["resource"].(map[string]any)
		if res["resourceType"] == "Patient" {
			for _, id := range sliceOf(res["identifier"]) {
				im, _ := id.(map[string]any)
				sys, _ := im["system"].(string)
				val, _ := im["value"].(string)
				if strings.HasPrefix(sys, "urn:oid:") && val != "" {
					inBundle[em["fullUrl"].(string)] = val + "^^^&" + strings.TrimPrefix(sys, "urn:oid:") + "&ISO"
					break
				}
			}
		}
	}
	resolve := func(ref string) string {
		if cx, ok := inBundle[ref]; ok {
			return cx
		}
		return s.patientCX(r.Context(), ref)
	}
	req, docs, err := xds.FromMHD(bundle, resolve)
	if err != nil {
		fhirFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	sub, ps := s.XDS.Register(r.Context(), req, docs)
	if sub == nil {
		var issues []any
		for _, p := range ps {
			issues = append(issues, map[string]any{"severity": "error", "code": "processing", "diagnostics": p.Code + ": " + p.Context,
				"details": map[string]any{"coding": []any{map[string]any{"system": "urn:oasis:names:tc:ebxml-regrep:ErrorCode", "code": p.Code}}}})
		}
		fhirJSON(w, http.StatusUnprocessableEntity, map[string]any{"resourceType": "OperationOutcome", "issue": issues})
		_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "mhd.provide", Detail: "refused", IP: clientIP(r)})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var entries []any
	entries = append(entries, map[string]any{"response": map[string]any{"status": "201 Created",
		"location": "List/" + strings.TrimPrefix(sub.SubmissionSet.ID, "urn:uuid:"), "lastModified": now}})
	for _, d := range sub.Documents {
		entries = append(entries, map[string]any{"response": map[string]any{"status": "201 Created",
			"location": "DocumentReference/" + strings.TrimPrefix(d.ID, "urn:uuid:"), "lastModified": now}})
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "mhd.provide",
		Target: sub.SubmissionSet.ExternalID(xds.IDSSPatientID), Detail: "ok", IP: clientIP(r)})
	fhirJSON(w, http.StatusOK, map[string]any{"resourceType": "Bundle", "type": "transaction-response", "entry": entries})
}

func sliceOf(v any) []any { x, _ := v.([]any); return x }

// mhdPatient is the patient a search names, as a CX: patient.identifier=urn:oid:X|value, or patient=Patient/id.
func (s *Server) mhdPatient(ctx context.Context, q url.Values) (string, bool) {
	if v := q.Get("patient.identifier"); v != "" {
		sys, val, ok := strings.Cut(v, "|")
		if !ok || !strings.HasPrefix(sys, "urn:oid:") {
			return "", false
		}
		return val + "^^^&" + strings.TrimPrefix(sys, "urn:oid:") + "&ISO", true
	}
	if v := q.Get("patient"); v != "" {
		cx := s.patientCX(ctx, v)
		return cx, cx != ""
	}
	return "", false
}

// handleMHDFind is ITI-67, a FindDocuments stored query in FHIR clothes.
func (s *Server) handleMHDFind(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mhdScoped(w, sess) {
		return
	}
	q := r.URL.Query()
	cx, ok := s.mhdPatient(r.Context(), q)
	if !ok {
		fhirFail(w, http.StatusBadRequest, "required", "ITI-67 needs patient or patient.identifier (urn:oid:...|value)")
		return
	}
	aq := xds.FindDocuments(cx, "LeafClass")
	statuses := q["status"]
	if len(statuses) > 0 {
		var st []string
		for _, v := range strings.Split(strings.Join(statuses, ","), ",") {
			switch strings.TrimSpace(v) {
			case "current":
				st = append(st, "'"+xds.StatusApproved+"'")
			case "superseded":
				st = append(st, "'"+xds.StatusDeprecated+"'")
			}
		}
		if len(st) > 0 {
			aq.Query.Slots[1].Values = []string{"(" + strings.Join(st, ",") + ")"}
		}
	}
	codes := func(param, slot string) {
		if v := q.Get(param); v != "" {
			var keys []string
			for _, t := range strings.Split(v, ",") {
				sys, code, ok := strings.Cut(t, "|")
				if !ok {
					continue
				}
				oid := strings.TrimPrefix(sys, "urn:oid:")
				if o, ok := map[string]string{"http://loinc.org": "2.16.840.1.113883.6.1", "http://snomed.info/sct": "2.16.840.1.113883.6.96",
					"http://terminology.hl7.org/CodeSystem/v3-Confidentiality": "2.16.840.1.113883.5.25"}[sys]; ok {
					oid = o
				}
				keys = append(keys, "'"+code+"^^^"+oid+"'")
			}
			if len(keys) > 0 {
				aq.Query.Slots = append(aq.Query.Slots, xds.Slot{Name: slot, Values: []string{"(" + strings.Join(keys, ",") + ")"}})
			}
		}
	}
	codes("category", "$XDSDocumentEntryClassCode")
	codes("type", "$XDSDocumentEntryTypeCode")
	codes("security-label", "$XDSDocumentEntryConfidentialityCode")
	codes("format", "$XDSDocumentEntryFormatCode")
	for _, v := range q["creation"] {
		if strings.HasPrefix(v, "ge") {
			aq.Query.Slots = append(aq.Query.Slots, xds.Slot{Name: "$XDSDocumentEntryCreationTimeFrom", Values: []string{fhirToDTM(v[2:])}})
		}
		if strings.HasPrefix(v, "lt") || strings.HasPrefix(v, "le") {
			aq.Query.Slots = append(aq.Query.Slots, xds.Slot{Name: "$XDSDocumentEntryCreationTimeTo", Values: []string{fhirToDTM(v[2:])}})
		}
	}
	res, ps := s.XDS.Query(r.Context(), aq)
	if res == nil {
		var issues []string
		for _, p := range ps {
			issues = append(issues, p.Code+": "+p.Context)
		}
		fhirFail(w, http.StatusBadRequest, "processing", strings.Join(issues, "; "))
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "mhd.find", Target: cx, IP: clientIP(r)})
	var entries []any
	for _, d := range res.Documents {
		dr := xds.ToDocumentReference(d, q.Get("patient"), s.mhdBase()+"/Binary/"+url.PathEscape(d.ExternalID(xds.IDDocUniqueID)))
		if dr["subject"].(map[string]any)["reference"] == "" {
			dr["subject"] = map[string]any{"identifier": map[string]any{"system": "urn:oid:" + cxAuthority(cx), "value": cxValue(cx)}}
		}
		entries = append(entries, map[string]any{"fullUrl": s.mhdBase() + "/DocumentReference/" + dr["id"].(string), "resource": dr,
			"search": map[string]any{"mode": "match"}})
	}
	if entries == nil {
		entries = []any{}
	}
	fhirJSON(w, http.StatusOK, map[string]any{"resourceType": "Bundle", "type": "searchset", "total": len(entries), "entry": entries})
}

func cxValue(cx string) string { v, _, _ := strings.Cut(cx, "^"); return v }
func cxAuthority(cx string) string {
	parts := strings.Split(cx, "&")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func fhirToDTM(v string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, v); err == nil {
			switch layout {
			case "2006-01-02":
				return t.Format("20060102")
			case "2006-01":
				return t.Format("200601")
			case "2006":
				return t.Format("2006")
			}
			return t.UTC().Format("20060102150405")
		}
	}
	return v
}

func (s *Server) handleMHDRead(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mhdScoped(w, sess) {
		return
	}
	id := r.PathValue("id")
	q := &xds.AdhocQueryRequest{}
	q.ResponseOpt.ReturnType = "LeafClass"
	q.Query.ID = xds.QGetDocuments
	q.Query.Slots = []xds.Slot{{Name: "$XDSDocumentEntryEntryUUID", Values: []string{"('urn:uuid:" + strings.ReplaceAll(id, "'", "") + "')"}}}
	res, _ := s.XDS.Query(r.Context(), q)
	if res == nil || len(res.Documents) == 0 {
		fhirFail(w, http.StatusNotFound, "not-found", "no DocumentReference "+id)
		return
	}
	d := res.Documents[0]
	cx := d.ExternalID(xds.IDDocPatientID)
	dr := xds.ToDocumentReference(d, "", s.mhdBase()+"/Binary/"+url.PathEscape(d.ExternalID(xds.IDDocUniqueID)))
	dr["subject"] = map[string]any{"identifier": map[string]any{"system": "urn:oid:" + cxAuthority(cx), "value": cxValue(cx)}}
	fhirJSON(w, http.StatusOK, dr)
}

// handleMHDLists is ITI-66, SubmissionSets as Lists.
func (s *Server) handleMHDLists(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mhdScoped(w, sess) {
		return
	}
	cx, ok := s.mhdPatient(r.Context(), r.URL.Query())
	if !ok {
		fhirFail(w, http.StatusBadRequest, "required", "ITI-66 needs patient or patient.identifier (urn:oid:...|value)")
		return
	}
	q := &xds.AdhocQueryRequest{}
	q.ResponseOpt.ReturnType = "LeafClass"
	q.Query.ID = xds.QFindSubmissionSets
	q.Query.Slots = []xds.Slot{{Name: "$XDSSubmissionSetPatientId", Values: []string{"'" + cx + "'"}},
		{Name: "$XDSSubmissionSetStatus", Values: []string{"('" + xds.StatusApproved + "')"}}}
	res, _ := s.XDS.Query(r.Context(), q)
	var entries []any
	for _, p := range res.Packages {
		list := map[string]any{"resourceType": "List", "id": strings.TrimPrefix(p.ID, "urn:uuid:"), "status": "current", "mode": "working",
			"meta":  map[string]any{"profile": []any{xds.MHDSubmissionSetProfile}},
			"code":  map[string]any{"coding": []any{map[string]any{"system": xds.MHDListType, "code": "submissionset"}}},
			"title": p.Name.Text(), "date": dtm(p.Slot1("submissionTime")),
			// MHD's entryUUID identifier is use official; the uniqueId is the other urn:ietf:rfc:3986 identifier, with no use.
			"identifier": []any{map[string]any{"use": "official", "system": "urn:ietf:rfc:3986", "value": p.ID},
				map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:oid:" + p.ExternalID(xds.IDSSUniqueID)}},
			"extension": []any{map[string]any{"url": xds.MHDSourceIDExt, "valueIdentifier": map[string]any{"value": "urn:oid:" + p.ExternalID(xds.IDSSSourceID)}}},
			"subject":   map[string]any{"identifier": map[string]any{"system": "urn:oid:" + cxAuthority(cx), "value": cxValue(cx)}}}
		entries = append(entries, map[string]any{"fullUrl": s.mhdBase() + "/List/" + list["id"].(string), "resource": list})
	}
	if entries == nil {
		entries = []any{}
	}
	fhirJSON(w, http.StatusOK, map[string]any{"resourceType": "Bundle", "type": "searchset", "total": len(entries), "entry": entries})
}

func dtm(v string) string {
	if len(v) < 8 {
		return v
	}
	out := v[:4] + "-" + v[4:6] + "-" + v[6:8]
	if len(v) >= 14 {
		out += "T" + v[8:10] + ":" + v[10:12] + ":" + v[12:14] + "Z"
	}
	return out
}

// handleMHDBinary is ITI-68: the document itself, as its own media type.
func (s *Server) handleMHDBinary(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mhdScoped(w, sess) {
		return
	}
	s.handleXDSDocument(w, r, sess)
}

// handlePIXm is ITI-83: every identifier the patient with this one has, from the FHIR store's Patients.
func (s *Server) handlePIXm(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	// With a patient index the cross-references are the index's: every identifier of every record linked to the same
	// person, and the enterprise identifier. That is what a PIX manager is for; the FHIR store only knows the identifiers
	// one Patient resource happens to carry.
	if s.PatientIndex != nil {
		if s.handlePIXmFromIndex(w, r, sess) {
			return
		}
	}
	if s.FHIRStore == nil {
		fhirFail(w, http.StatusNotImplemented, "not-supported", "PIXm answers from the FHIR store, which is off (serve -fhir)")
		return
	}
	src := r.URL.Query().Get("sourceIdentifier")
	sys, val, ok := strings.Cut(src, "|")
	if !ok || sys == "" || val == "" {
		fhirFail(w, http.StatusBadRequest, "required", "sourceIdentifier is system|value")
		return
	}
	q, err := fhirserver.ParseSearch("Patient", map[string][]string{"identifier": {src}})
	if err != nil {
		fhirFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	res, err := s.FHIRStore.Search(r.Context(), q)
	if err != nil {
		fhirFail(w, http.StatusInternalServerError, "exception", err.Error())
		return
	}
	if len(res.Resources) == 0 {
		// ITI-83: an identifier the manager does not know is a 404 with an OperationOutcome naming the identifier.
		fhirFail(w, http.StatusNotFound, "not-found", "sourceIdentifier Patient Identifier not found: "+src)
		return
	}
	targets := map[string]bool{}
	for _, t := range strings.Split(r.URL.Query().Get("targetSystem"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			targets[t] = true
		}
	}
	var params []any
	seen := map[string]bool{}
	for _, rr := range res.Resources {
		p, ok := rr.(*fhir.Patient)
		if !ok {
			continue
		}
		for _, id := range p.Identifier {
			key := id.System + "|" + id.Value
			if seen[key] || (id.System == sys && id.Value == val) || (len(targets) > 0 && !targets[id.System]) {
				continue
			}
			seen[key] = true
			params = append(params, map[string]any{"name": "targetIdentifier", "valueIdentifier": map[string]any{"use": "official",
				"system": id.System, "value": id.Value}})
		}
		params = append(params, map[string]any{"name": "targetId", "valueReference": map[string]any{"reference": s.mhdBase() + "/Patient/" + p.ID}})
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "pixm.query", Target: src, IP: clientIP(r)})
	if params == nil {
		params = []any{}
	}
	fhirJSON(w, http.StatusOK, map[string]any{"resourceType": "Parameters", "parameter": params})
}

// handlePDQm is ITI-78: the FHIR store's Patient search, on the parameters PDQm names.
func (s *Server) handlePDQm(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.FHIRStore == nil {
		fhirFail(w, http.StatusNotImplemented, "not-supported", "PDQm answers from the FHIR store, which is off (serve -fhir)")
		return
	}
	allowed := map[string]bool{"_id": true, "identifier": true, "family": true, "given": true, "name": true, "birthdate": true,
		"gender": true, "address": true, "address-city": true, "address-state": true, "address-postalcode": true, "telecom": true,
		"active": true, "_count": true}
	crit := map[string][]string{}
	for k, v := range r.URL.Query() {
		base := strings.SplitN(k, ":", 2)[0]
		if !allowed[base] {
			fhirFail(w, http.StatusBadRequest, "not-supported", "PDQm here searches by "+
				"_id, identifier, family, given, name, birthdate, gender, address and telecom, not "+k)
			return
		}
		if base == "_count" {
			continue
		}
		crit[k] = v
	}
	if len(crit) == 0 {
		fhirFail(w, http.StatusBadRequest, "required", "a PDQm query names at least one demographic")
		return
	}
	q, err := fhirserver.ParseSearch("Patient", crit)
	if err != nil {
		fhirFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	q.Count = 50
	res, err := s.FHIRStore.Search(r.Context(), q)
	if err != nil {
		fhirFail(w, http.StatusInternalServerError, "exception", err.Error())
		return
	}
	b := s.FHIRStore.SearchBundle(res, s.mhdBase(), "Patient", r.URL.RawQuery)
	raw, err := fhir.MarshalBundle(b, fhir.R4)
	if err != nil {
		fhirFail(w, http.StatusInternalServerError, "exception", err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "pdqm.query", Target: r.URL.RawQuery, IP: clientIP(r)})
	w.Header().Set("Content-Type", "application/fhir+json")
	_, _ = w.Write(raw)
}

func (s *Server) handlePDQmRead(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.FHIRStore == nil {
		fhirFail(w, http.StatusNotImplemented, "not-supported", "PDQm answers from the FHIR store, which is off (serve -fhir)")
		return
	}
	res, err := s.FHIRStore.Get(r.Context(), "Patient", r.PathValue("id"))
	if err != nil {
		fhirFail(w, http.StatusNotFound, "not-found", "no Patient "+r.PathValue("id"))
		return
	}
	raw, err := fhir.Marshal(res, fhir.R4)
	if err != nil {
		fhirFail(w, http.StatusInternalServerError, "exception", err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "pdqm.read", Target: r.PathValue("id"), IP: clientIP(r)})
	w.Header().Set("Content-Type", "application/fhir+json")
	_, _ = w.Write(raw)
}

// handlePIXmFromIndex answers ITI-83 from the patient index, and reports false when the index does not know the identifier,
// so the FHIR store is asked instead.
func (s *Server) handlePIXmFromIndex(w http.ResponseWriter, r *http.Request, sess *store.Session) bool {
	src := r.URL.Query().Get("sourceIdentifier")
	sys, val, ok := strings.Cut(src, "|")
	if !ok || sys == "" || val == "" {
		fhirFail(w, http.StatusBadRequest, "required", "sourceIdentifier is system|value")
		return true
	}
	t := mpiTenant(sess)
	p, err := s.PatientIndex.ByIdentifier(r.Context(), t, src)
	if err != nil {
		return false
	}
	set, _ := s.PatientIndex.Settings(r.Context(), t)
	targets := map[string]bool{}
	for _, x := range strings.Split(r.URL.Query().Get("targetSystem"), ",") {
		if x = strings.TrimSpace(x); x != "" {
			targets[x] = true
		}
	}
	params := []any{}
	for _, id := range p.Identifiers(set.EUIDSystem) {
		isys, ival, _ := strings.Cut(id, "|")
		if id == src || (len(targets) > 0 && !targets[isys]) {
			continue
		}
		params = append(params, map[string]any{"name": "targetIdentifier", "valueIdentifier": map[string]any{"use": "official",
			"system": isys, "value": ival}})
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "pixm.query", Target: src, Detail: "patient index", IP: clientIP(r)})
	fhirJSON(w, http.StatusOK, map[string]any{"resourceType": "Parameters", "parameter": params})
	return true
}
