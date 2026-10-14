package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/biodream-llc/perfuse/internal/dicomweb"
	"github.com/biodream-llc/perfuse/internal/store"
)

// The imaging archive over DICOMweb, at /dicomweb.
//
// Reading needs the viewer role, as reading messages does, and storing the operator role. A session limited to some
// channel groups, or acting for a tenant, is refused: the archive belongs to the whole server, and narrowing it to the
// groups a person may see is not something an image carries.

func (s *Server) registerDICOMweb(mux *recordingMux) {
	mux.Handle("GET /api/dicomweb", s.require(store.RoleViewer, s.handleDICOMwebStatus))
	if s.DICOMweb == nil {
		return
	}
	h := &dicomweb.Handler{Archive: s.DICOMweb, Base: s.dicomwebBase(), Log: s.log()}
	inner := http.StripPrefix("/dicomweb", h)
	read := s.require(store.RoleViewer, func(w http.ResponseWriter, r *http.Request, sess *store.Session) {
		s.serveDICOMweb(w, r, sess, h, inner)
	})
	write := s.require(store.RoleOperator, func(w http.ResponseWriter, r *http.Request, sess *store.Session) {
		s.serveDICOMweb(w, r, sess, h, inner)
	})
	cors := func(next http.Handler) http.Handler {
		return authedHandlerLike(next, func(w http.ResponseWriter, r *http.Request) {
			s.dicomwebCORS(w, r)
			next.ServeHTTP(w, r)
		})
	}
	mux.Handle("GET /dicomweb/", cors(read))
	mux.Handle("POST /dicomweb/", cors(write))
	// A browser's preflight carries no credentials by definition, so it is answered without them, and only for the
	// origins listed; it reveals nothing beyond which methods exist.
	mux.HandleFunc("OPTIONS /dicomweb/", func(w http.ResponseWriter, r *http.Request) {
		if !s.dicomwebCORS(w, r) {
			w.WriteHeader(http.StatusForbidden)
		}
	})
}

func (s *Server) dicomwebBase() string {
	public := s.PublicURL
	if public == "" {
		public = s.SelfURL
	}
	return dicomweb.MountedBase(public, "/dicomweb")
}

// dicomwebCORS lets a viewer served from another origin (OHIF, most often) call the archive, for the origins the
// operator listed and no others. It answers a preflight itself and reports whether it did.
func (s *Server) dicomwebCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || !slices.Contains(s.DICOMwebCORS, origin) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Type, Content-Length, Warning")
	if r.Method != http.MethodOptions {
		return false
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Accept, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (s *Server) serveDICOMweb(w http.ResponseWriter, r *http.Request, sess *store.Session, h *dicomweb.Handler, inner http.Handler) {
	if sess.TenantID != store.DefaultTenant || len(sess.ChannelGroups) > 0 {
		s.fail(w, r, http.StatusForbidden, "the imaging archive belongs to the whole server; an account limited to a "+
			"tenant or to channel groups cannot read it")
		return
	}
	// Per request rather than once, so the audit entries name the person or token that made each call.
	scoped := *h
	scoped.Stored = func(ctx context.Context, st *dicomweb.Stored) {
		_ = s.Store.Audit(ctx, store.AuditEntry{Username: sess.Username, Action: "dicomweb.store",
			Target: st.StudyUID + "/" + st.SeriesUID + "/" + st.SOPUID, IP: clientIP(r)})
	}
	scoped.Read = func(ctx context.Context, what string) {
		_ = s.Store.Audit(ctx, store.AuditEntry{Username: sess.Username, Action: "dicomweb.read", Target: what, IP: clientIP(r)})
	}
	http.StripPrefix("/dicomweb", &scoped).ServeHTTP(w, r)
	_ = inner
}

func (s *Server) handleDICOMwebStatus(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.DICOMweb == nil {
		s.ok(w, map[string]any{"enabled": false,
			"hint": "Start the server with -dicomweb-archive DIR, or set Settings › Data › Imaging archive, to keep " +
				"images here and serve them over DICOMweb."})
		return
	}
	counts, err := s.DICOMweb.Count(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{
		"enabled": true, "base": s.dicomwebBase(), "studies": counts.Studies, "series": counts.Series,
		"instances": counts.Instances, "bytes": counts.Bytes, "corsOrigins": orEmpty(s.DICOMwebCORS),
		"readable": sess.TenantID == store.DefaultTenant && len(sess.ChannelGroups) == 0,
		"note": "QIDO-RS, WADO-RS and WADO-URI need the viewer role, STOW-RS the operator role; an API token works " +
			"as a bearer token. " + strings.TrimSpace(corsNote(s.DICOMwebCORS)),
	})
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func corsNote(origins []string) string {
	if len(origins) == 0 {
		return "No other origin may call it from a browser; a viewer served elsewhere needs its origin listed."
	}
	return ""
}

// authedHandlerLike wraps an authenticated handler without losing the role the route registry records.
func authedHandlerLike(inner http.Handler, f http.HandlerFunc) http.Handler {
	if a, ok := inner.(authedHandler); ok {
		return authedHandler{role: a.role, Handler: f}
	}
	return f
}
