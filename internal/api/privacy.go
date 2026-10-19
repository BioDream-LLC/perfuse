package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/natid"
	"github.com/biodream-llc/perfuse/internal/privacy"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Data protection: a data subject's requests under the GDPR and the UK GDPR (access, portability, erasure, restriction),
// legal holds, the erasure ledger, and the record of processing. Administrators only: finding everything about a person is
// the widest read of patient data there is. Every step is audited, with the person's numbers masked in the audit entry.

func (s *Server) registerPrivacy(mux *recordingMux) {
	mux.Handle("GET /api/privacy", s.require(store.RoleAdmin, s.handlePrivacySummary))
	mux.Handle("POST /api/privacy/find", s.require(store.RoleAdmin, s.handlePrivacyFind))
	mux.Handle("POST /api/privacy/requests", s.require(store.RoleAdmin, s.handlePrivacyCreate))
	mux.Handle("POST /api/privacy/requests/{id}/approve", s.require(store.RoleAdmin, s.handlePrivacyApprove))
	mux.Handle("GET /api/privacy/requests/{id}/export", s.require(store.RoleAdmin, s.handlePrivacyExport))
	mux.Handle("POST /api/privacy/requests/{id}/erase", s.require(store.RoleAdmin, s.handlePrivacyErase))
	mux.Handle("POST /api/privacy/requests/{id}/close", s.require(store.RoleAdmin, s.handlePrivacyClose))
	mux.Handle("POST /api/privacy/holds", s.require(store.RoleAdmin, s.handlePrivacyHold))
	mux.Handle("DELETE /api/privacy/holds/{id}", s.require(store.RoleAdmin, s.handlePrivacyLift))
	mux.Handle("POST /api/privacy/reapply", s.require(store.RoleAdmin, s.handlePrivacyReapply))
	mux.Handle("GET /api/privacy/register", s.require(store.RoleAdmin, s.handlePrivacyRegister))
}

func (s *Server) privacyReady(w http.ResponseWriter, r *http.Request) bool {
	if s.Privacy == nil {
		s.fail(w, r, http.StatusNotFound, "data protection requests need the server's database")
		return false
	}
	return true
}

func privacyTenant(sess *store.Session) string { return string(sess.TenantID) }

func (s *Server) privacyFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, privacy.ErrHeld) || strings.HasPrefix(err.Error(), "privacy: ") {
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "privacy: "))
		return
	}
	s.failErr(w, r, err)
}

// maskedIdents writes identifiers for the audit log: system and the last few characters.
func maskedIdents(idents []string) string {
	out := make([]string, len(idents))
	for i, ident := range idents {
		sys, val, ok := strings.Cut(ident, "|")
		if !ok {
			sys, val = "", ident
		}
		out[i] = strings.TrimPrefix(sys+"|", "|") + natid.Mask(val)
	}
	return strings.Join(out, ", ")
}

func (s *Server) privacyAudit(r *http.Request, sess *store.Session, action, target, detail string) {
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: action, Target: target, Detail: detail, IP: clientIP(r)})
}

func (s *Server) mpiSettingsFor(r *http.Request, sess *store.Session) mpi.Settings {
	if s.PatientIndex == nil {
		return mpi.Settings{}
	}
	set, _ := s.PatientIndex.Settings(r.Context(), mpiTenant(sess))
	return set
}

type privacySummary struct {
	Requests  []privacy.Request       `json:"requests"`
	Holds     []privacy.Hold          `json:"holds"`
	Erasures  []privacy.Erasure       `json:"erasures"`
	Retention []privacy.RetentionRule `json:"retention"`
	Indexed   bool                    `json:"indexed"`
	Stores    []string                `json:"stores"`
	Location  string                  `json:"location"`
}

func (s *Server) handlePrivacySummary(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	t := privacyTenant(sess)
	reqs, err := s.Privacy.Requests(r.Context(), t)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	holds, err := s.Privacy.Holds(r.Context(), t)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	ers, err := s.Privacy.Erasures(r.Context(), t)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	out := privacySummary{Requests: reqs, Holds: holds, Erasures: ers, Retention: s.retentionRules(), Stores: []string{}}
	if s.Privacy.Messages != nil {
		out.Indexed = s.Privacy.Messages.IndexesIdentity()
		out.Stores = append(out.Stores, "messages")
	}
	if s.Privacy.Queue != nil {
		out.Stores = append(out.Stores, "queue")
	}
	if s.Privacy.FHIR != nil {
		out.Stores = append(out.Stores, "fhir", "priorauth")
	}
	if s.Privacy.Index != nil {
		out.Stores = append(out.Stores, "patient index")
	}
	if s.Settings != nil {
		out.Location = s.Settings.String("privacy.dataLocation")
	}
	s.ok(w, out)
}

// retentionRules says how long each kind of record is kept on this server, from the settings in force.
func (s *Server) retentionRules() []privacy.RetentionRule {
	days := func(key string) int {
		if s.Settings == nil {
			return 0
		}
		return s.Settings.Int(key)
	}
	kept := func(n int, otherwise string) string {
		if n <= 0 {
			return otherwise
		}
		return fmt.Sprintf("%d days", n)
	}
	ret := days("data.retentionDays")
	pay := days("data.payloadDays")
	if pay <= 0 && ret > 0 {
		pay = ret / 2
	}
	return []privacy.RetentionRule{
		{Category: "Message records (who sent what, when, the outcome)", Kept: kept(ret, "until deleted (no retention window set)"), Setting: "data.retentionDays"},
		{Category: "Message contents", Kept: kept(pay, "as long as the records"), Setting: "data.payloadDays"},
		{Category: "Patient identifiers indexed from messages", Kept: "as long as their message", Setting: "data.indexIdentity"},
		{Category: "Messages waiting in the delivery queue", Kept: "until delivered, skipped or removed; failed ones until purged from the Queue screen"},
		{Category: "FHIR store (resources and their history)", Kept: "until deleted or erased: clinical records are not deleted on a schedule"},
		{Category: "Prior authorization requests and answers", Kept: "until erased"},
		{Category: "Patient index", Kept: "until erased or unlinked"},
		{Category: "Audit log", Kept: "kept: it is the record of who did what, this erasure included"},
		{Category: "Data protection requests", Kept: "kept, with the person's numbers reduced to their last characters once answered"},
		{Category: "Erasure ledger", Kept: "kept, as salted hashes of the identifiers, to apply erasures again to a restored backup"},
	}
}

type privacyIdents struct {
	Identifiers []string `json:"identifiers"`
}

func (s *Server) handlePrivacyFind(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	var req privacyIdents
	if !s.decode(w, r, &req) {
		return
	}
	f, err := s.Privacy.Find(r.Context(), privacyTenant(sess), req.Identifiers, s.mpiSettingsFor(r, sess))
	if err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.find", maskedIdents(privacy.Clean(req.Identifiers)), fmt.Sprintf("%v", f.Counts))
	s.ok(w, f)
}

func (s *Server) handlePrivacyCreate(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	var req privacy.Request
	if !s.decode(w, r, &req) {
		return
	}
	t := privacyTenant(sess)
	id, err := s.Privacy.Create(r.Context(), t, sess.Username, req)
	if err != nil {
		s.privacyFail(w, r, err)
		return
	}
	// A restriction (Article 18) keeps the data as it is while something is settled: a hold does exactly that.
	if req.Kind == "restriction" {
		if _, err := s.Privacy.PlaceHold(r.Context(), t, sess.Username, req.Identifiers, fmt.Sprintf("restriction of processing, request %d", id), ""); err != nil {
			s.privacyFail(w, r, err)
			return
		}
	}
	s.privacyAudit(r, sess, "privacy.request", strconv.FormatInt(id, 10), req.Kind+" for "+maskedIdents(privacy.Clean(req.Identifiers)))
	s.ok(w, map[string]any{"id": id})
}

func (s *Server) privacyRequest(w http.ResponseWriter, r *http.Request, sess *store.Session) (privacy.Request, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the request id is a number")
		return privacy.Request{}, false
	}
	req, err := s.Privacy.Get(r.Context(), privacyTenant(sess), id)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such request")
		return privacy.Request{}, false
	}
	return req, true
}

func (s *Server) handlePrivacyApprove(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	req, ok := s.privacyRequest(w, r, sess)
	if !ok {
		return
	}
	if err := s.Privacy.Approve(r.Context(), privacyTenant(sess), sess.Username, req.ID); err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.approve", strconv.FormatInt(req.ID, 10), "erasure approved")
	s.ok(w, map[string]any{"approved": req.ID})
}

func (s *Server) handlePrivacyExport(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	req, ok := s.privacyRequest(w, r, sess)
	if !ok {
		return
	}
	if req.Status == "done" || req.Status == "refused" {
		s.fail(w, r, http.StatusConflict, "the request is closed; its identifiers are no longer kept in full")
		return
	}
	f, err := s.Privacy.Find(r.Context(), privacyTenant(sess), req.Identifiers, s.mpiSettingsFor(r, sess))
	if err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.export", strconv.FormatInt(req.ID, 10), fmt.Sprintf("%v", f.Counts))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="request-%d-%s.zip"`, req.ID, time.Now().UTC().Format("20060102")))
	w.Header().Set("Cache-Control", "no-store")
	if err := s.Privacy.Export(r.Context(), privacyTenant(sess), f, w); err != nil {
		s.Log.Error("data protection export failed", "request", req.ID, "err", err)
	}
}

func (s *Server) handlePrivacyErase(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	req, ok := s.privacyRequest(w, r, sess)
	if !ok {
		return
	}
	if req.Kind != "erasure" || req.Status != "approved" {
		s.fail(w, r, http.StatusConflict, "only an erasure a second administrator has approved can be carried out")
		return
	}
	t := privacyTenant(sess)
	f, err := s.Privacy.Find(r.Context(), t, req.Identifiers, s.mpiSettingsFor(r, sess))
	if err != nil {
		s.privacyFail(w, r, err)
		return
	}
	counts, err := s.Privacy.Erase(r.Context(), t, req.ID, f, sess.Username, req.ApprovedBy)
	if err != nil {
		s.privacyFail(w, r, err)
		return
	}
	outcome := "erased: " + countsText(counts)
	if err := s.Privacy.Close(r.Context(), t, sess.Username, req.ID, "done", outcome); err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.erase", strconv.FormatInt(req.ID, 10), outcome)
	s.ok(w, map[string]any{"counts": counts, "notes": f.Notes})
}

func countsText(c map[string]int) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
	}
	if len(parts) == 0 {
		return "nothing was held"
	}
	return strings.Join(parts, ", ")
}

func (s *Server) handlePrivacyClose(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	req, ok := s.privacyRequest(w, r, sess)
	if !ok {
		return
	}
	var body struct{ Status, Outcome string }
	if !s.decode(w, r, &body) {
		return
	}
	if req.Kind == "erasure" && body.Status == "done" {
		s.fail(w, r, http.StatusConflict, "an erasure is done by carrying it out (Erase), not by closing it")
		return
	}
	if err := s.Privacy.Close(r.Context(), privacyTenant(sess), sess.Username, req.ID, body.Status, body.Outcome); err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.close", strconv.FormatInt(req.ID, 10), body.Status+": "+body.Outcome)
	s.ok(w, map[string]any{"closed": req.ID})
}

func (s *Server) handlePrivacyHold(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	var body struct {
		Identifiers   []string
		Reason, Until string
	}
	if !s.decode(w, r, &body) {
		return
	}
	id, err := s.Privacy.PlaceHold(r.Context(), privacyTenant(sess), sess.Username, body.Identifiers, body.Reason, body.Until)
	if err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.hold", strconv.FormatInt(id, 10), body.Reason+" for "+maskedIdents(privacy.Clean(body.Identifiers)))
	s.ok(w, map[string]any{"id": id})
}

func (s *Server) handlePrivacyLift(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the hold id is a number")
		return
	}
	if err := s.Privacy.LiftHold(r.Context(), privacyTenant(sess), sess.Username, id); err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.lift", strconv.FormatInt(id, 10), "hold lifted")
	s.ok(w, map[string]any{"lifted": id})
}

func (s *Server) handlePrivacyReapply(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.privacyReady(w, r) {
		return
	}
	t := privacyTenant(sess)
	cands, err := s.Privacy.Candidates(r.Context(), t)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	counts, err := s.Privacy.Reapply(r.Context(), t, sess.Username, cands, s.mpiSettingsFor(r, sess))
	if err != nil {
		s.privacyFail(w, r, err)
		return
	}
	s.privacyAudit(r, sess, "privacy.reapply", "", countsText(counts))
	s.ok(w, map[string]any{"counts": counts})
}

func (s *Server) handlePrivacyRegister(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var c privacy.Controller
	if s.Settings != nil {
		c.Name, c.DPO, c.Location = s.Settings.String("privacy.controller"), s.Settings.String("privacy.dpo"), s.Settings.String("privacy.dataLocation")
	}
	c.Retention = s.retentionRules()
	var acts []privacy.Activity
	if s.Channels != nil {
		list, _, err := s.Channels.List()
		if err != nil {
			s.failErr(w, r, err)
			return
		}
		for _, ch := range list {
			if !visibleChannel(r, ch.Name) {
				continue
			}
			a := privacy.Activity{Channel: ch.Name, Description: ch.Description, Source: ch.SourceType, Listen: ch.Listen,
				Recipients: ch.Destinations, Hosts: []string{}, Enabled: ch.Enabled}
			if cfg, err := s.Channels.Get(ch.Name); err == nil {
				seen := map[string]bool{}
				for i := range cfg.Destinations {
					for _, h := range cfg.Destinations[i].OutboundHosts() {
						if !seen[h] {
							seen[h] = true
							a.Hosts = append(a.Hosts, h)
						}
					}
				}
			}
			acts = append(acts, a)
		}
	}
	s.privacyAudit(r, sess, "privacy.register", "", fmt.Sprintf("%d activities", len(acts)))
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="records-of-processing.csv"`)
		_, _ = w.Write(privacy.RegisterCSV(c, acts))
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="records-of-processing.md"`)
	_, _ = w.Write(privacy.RegisterMarkdown(c, acts, time.Now()))
}
