package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/store"
)

// The patient index: its persons, the review queue, links and merges by hand, and its settings.
//
// Reading it is reading patient demographics, so every read is written to the audit log as a PHI access, the way the
// identity search is; changing it needs an editor, and its settings an administrator.

func (s *Server) registerMPI(mux *recordingMux) {
	mux.Handle("GET /api/mpi", s.require(store.RoleViewer, s.handleMPISummary))
	mux.Handle("GET /api/mpi/persons/{euid}", s.require(store.RoleViewer, s.handleMPIPerson))
	mux.Handle("POST /api/mpi/find", s.require(store.RoleViewer, s.handleMPIFind))
	mux.Handle("GET /api/mpi/reviews", s.require(store.RoleEditor, s.handleMPIReviews))
	mux.Handle("POST /api/mpi/reviews/{id}", s.require(store.RoleEditor, s.handleMPIDecide))
	mux.Handle("POST /api/mpi/records", s.require(store.RoleEditor, s.handleMPIRegister))
	mux.Handle("POST /api/mpi/records/{id}/link", s.require(store.RoleEditor, s.handleMPILink))
	mux.Handle("POST /api/mpi/records/{id}/unlink", s.require(store.RoleEditor, s.handleMPIUnlink))
	mux.Handle("POST /api/mpi/merge", s.require(store.RoleEditor, s.handleMPIMerge))
	mux.Handle("PUT /api/mpi/settings", s.require(store.RoleAdmin, s.handleMPISettings))
	mux.Handle("POST /api/mpi/estimate", s.require(store.RoleAdmin, s.handleMPIEstimate))
}

func mpiTenant(sess *store.Session) string { return mpi.TenantOf(string(sess.TenantID)) }

func (s *Server) mpiReady(w http.ResponseWriter, r *http.Request) bool {
	if s.PatientIndex == nil {
		s.fail(w, r, http.StatusNotFound, "this server keeps no patient index; start it with -mpi, or turn on Settings → Data → Patient index")
		return false
	}
	return true
}

func (s *Server) mpiFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, mpi.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not found")
		return
	}
	if strings.HasPrefix(err.Error(), "mpi: ") {
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "mpi: "))
		return
	}
	s.failErr(w, r, err)
}

func (s *Server) mpiAudit(r *http.Request, sess *store.Session, action, target, detail string) {
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: action, Target: target, Detail: detail, IP: clientIP(r)})
}

type mpiSummary struct {
	Enabled  bool          `json:"enabled"`
	Counts   mpi.Counts    `json:"counts"`
	Persons  []*mpi.Person `json:"persons"`
	Settings mpi.Settings  `json:"settings"`
}

func (s *Server) handleMPISummary(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.PatientIndex == nil {
		s.ok(w, mpiSummary{Persons: []*mpi.Person{}, Settings: mpi.Settings{Trusted: []string{}}})
		return
	}
	t := mpiTenant(sess)
	c, err := s.PatientIndex.Count(r.Context(), t)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	q := r.URL.Query().Get("q")
	persons, err := s.PatientIndex.Recent(r.Context(), t, q, 50)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	set, err := s.PatientIndex.Settings(r.Context(), t)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if q != "" {
		s.mpiAudit(r, sess, "mpi.search", q, fmt.Sprintf("%d persons", len(persons)))
	}
	s.ok(w, mpiSummary{Enabled: true, Counts: c, Persons: persons, Settings: set})
}

type mpiPersonView struct {
	Person  *mpi.Person `json:"person"`
	Golden  mpi.Record  `json:"golden"`
	History []mpi.Event `json:"history"`
	IDs     []string    `json:"identifiers"`
}

func (s *Server) handleMPIPerson(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	t, euid := mpiTenant(sess), r.PathValue("euid")
	p, err := s.PatientIndex.Person(r.Context(), t, euid)
	if err != nil {
		s.mpiFail(w, r, err)
		return
	}
	h, err := s.PatientIndex.History(r.Context(), t, euid, 200)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	set, _ := s.PatientIndex.Settings(r.Context(), t)
	s.mpiAudit(r, sess, "mpi.read", euid, "")
	s.ok(w, mpiPersonView{Person: p, Golden: p.Golden(), History: h, IDs: p.Identifiers(set.EUIDSystem)})
}

func (s *Server) handleMPIFind(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	var q mpi.Record
	if !s.decode(w, r, &q) {
		return
	}
	t := mpiTenant(sess)
	set, err := s.PatientIndex.Settings(r.Context(), t)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	found, err := s.PatientIndex.Find(r.Context(), t, q, set, 20)
	if err != nil {
		s.mpiFail(w, r, err)
		return
	}
	if found == nil {
		found = []mpi.Match{}
	}
	s.mpiAudit(r, sess, "mpi.find", strings.TrimSpace(q.Family+" "+q.Given+" "+q.BirthDate), fmt.Sprintf("%d matches", len(found)))
	s.ok(w, map[string]any{"matches": found})
}

func (s *Server) handleMPIReviews(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	list, err := s.PatientIndex.Reviews(r.Context(), mpiTenant(sess), 200)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.mpiAudit(r, sess, "mpi.reviews", "", fmt.Sprintf("%d open", len(list)))
	s.ok(w, map[string]any{"reviews": list})
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (s *Server) handleMPIDecide(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the review id is a number")
		return
	}
	var req struct {
		Same *bool `json:"same"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Same == nil {
		s.fail(w, r, http.StatusBadRequest, "say whether the two are the same person: same true or false")
		return
	}
	if err := s.PatientIndex.Decide(r.Context(), mpiTenant(sess), sess.Username, id, *req.Same); err != nil {
		s.mpiFail(w, r, err)
		return
	}
	s.mpiAudit(r, sess, "mpi.review", strconv.FormatInt(id, 10), map[bool]string{true: "same person", false: "different people"}[*req.Same])
	s.ok(w, map[string]any{"review": id, "same": *req.Same})
}

func (s *Server) handleMPIRegister(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	var rec mpi.Record
	if !s.decode(w, r, &rec) {
		return
	}
	if rec.Source == "" {
		rec.Source = "console"
	}
	out, err := s.PatientIndex.RegisterWithSettings(r.Context(), mpiTenant(sess), sess.Username, rec)
	if err != nil {
		s.mpiFail(w, r, err)
		return
	}
	s.mpiAudit(r, sess, "mpi.register", rec.Key(), out.Action)
	s.ok(w, out)
}

func (s *Server) handleMPILink(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the record id is a number")
		return
	}
	var req struct{ EUID, Why string }
	if !s.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.EUID) == "" {
		s.fail(w, r, http.StatusBadRequest, "name the person (euid) to link the record to")
		return
	}
	if err := s.PatientIndex.Link(r.Context(), mpiTenant(sess), sess.Username, id, req.EUID, req.Why); err != nil {
		s.mpiFail(w, r, err)
		return
	}
	s.mpiAudit(r, sess, "mpi.link", strconv.FormatInt(id, 10), "to "+req.EUID)
	s.ok(w, map[string]any{"record": id, "euid": req.EUID})
}

func (s *Server) handleMPIUnlink(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the record id is a number")
		return
	}
	var req struct{ Why string }
	if !s.decode(w, r, &req) {
		return
	}
	euid, err := s.PatientIndex.Unlink(r.Context(), mpiTenant(sess), sess.Username, id, req.Why)
	if err != nil {
		s.mpiFail(w, r, err)
		return
	}
	s.mpiAudit(r, sess, "mpi.unlink", strconv.FormatInt(id, 10), "now "+euid)
	s.ok(w, map[string]any{"record": id, "euid": euid})
}

func (s *Server) handleMPIMerge(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	var req struct{ From, Into, Why string }
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.PatientIndex.Merge(r.Context(), mpiTenant(sess), sess.Username, req.From, req.Into, req.Why); err != nil {
		s.mpiFail(w, r, err)
		return
	}
	s.mpiAudit(r, sess, "mpi.merge", req.From, "into "+req.Into)
	s.ok(w, map[string]any{"euid": req.Into})
}

func (s *Server) handleMPISettings(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	var set mpi.Settings
	if !s.decode(w, r, &set) {
		return
	}
	if err := s.PatientIndex.SaveSettings(r.Context(), mpiTenant(sess), sess.Username, set); err != nil {
		s.mpiFail(w, r, err)
		return
	}
	saved, _ := s.PatientIndex.Settings(r.Context(), mpiTenant(sess))
	s.mpiAudit(r, sess, "mpi.settings", "", fmt.Sprintf("automatic %v at %.2f, review %.2f, trusted %v, %d site weights",
		saved.Automatic, saved.AutoLink, saved.Review, saved.Trusted, len(saved.Weights)))
	s.ok(w, saved)
}

// handleMPIEstimate estimates matching weights from the index's own records. It changes nothing: the administrator sees the
// estimate beside the weights in use and saves it, or not, through the settings.
func (s *Server) handleMPIEstimate(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mpiReady(w, r) {
		return
	}
	est, err := s.PatientIndex.EstimateFromRecords(r.Context(), mpiTenant(sess))
	if errors.Is(err, mpi.ErrTooFewPairs) {
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.mpiFail(w, r, err)
		return
	}
	s.mpiAudit(r, sess, "mpi.estimate", "", fmt.Sprintf("%d pairs, converged %v", est.Pairs, est.Converged))
	s.ok(w, map[string]any{"estimate": est, "defaults": mpi.DefaultWeights})
}
