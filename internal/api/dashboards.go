package api

import (
	"encoding/json"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/dashboards"
	"github.com/biodream-llc/perfuse/internal/store"
)

// dashboardView is what a person saved: which dashboard, which of its tiles in what order, and a channel filter.
type dashboardView struct {
	Dashboard string   `json:"dashboard"`
	Tiles     []string `json:"tiles,omitempty"`
	Channel   string   `json:"channel,omitempty"`
}

// handleDashboards lists the dashboards and tiles, and says which one this person opens on and why.
func (s *Server) handleDashboards(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	groups, _ := s.Store.DirectoryGroups(r.Context(), sess.UserID)
	assigned := s.Dashboards.For(string(sess.Role), groups)
	why := "the default"
	if s.Dashboards != nil {
		for _, g := range groups {
			if s.Dashboards.Groups[g] == assigned {
				why = "your directory group " + g
				break
			}
		}
		if why == "the default" && s.Dashboards.Roles[string(sess.Role)] == assigned {
			why = "your role, " + string(sess.Role)
		}
	}
	var saved *dashboardView
	if raw, err := s.Store.DashboardView(r.Context(), sess.UserID); err == nil && raw != "" {
		var v dashboardView
		if json.Unmarshal([]byte(raw), &v) == nil {
			saved = &v
		}
	}
	s.ok(w, map[string]any{
		"dashboards": dashboards.Dashboards,
		"tiles":      dashboards.Tiles,
		"assigned":   assigned,
		"assignedBy": why,
		"saved":      saved,
	})
}

// handleSaveDashboardView stores this person's view. An empty body (no dashboard) clears it.
func (s *Server) handleSaveDashboardView(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var v dashboardView
	if !s.decode(w, r, &v) {
		return
	}
	if v.Dashboard == "" {
		if err := s.Store.SaveDashboardView(r.Context(), sess.UserID, ""); err != nil {
			s.failErr(w, r, err)
			return
		}
		s.ok(w, map[string]string{"status": "cleared"})
		return
	}
	if _, ok := dashboards.ByID(v.Dashboard); !ok {
		s.fail(w, r, http.StatusBadRequest, "there is no dashboard called "+v.Dashboard)
		return
	}
	for _, t := range v.Tiles {
		if _, ok := dashboards.TileByID(t); !ok {
			s.fail(w, r, http.StatusBadRequest, "there is no tile called "+t)
			return
		}
	}
	raw, _ := json.Marshal(v)
	if err := s.Store.SaveDashboardView(r.Context(), sess.UserID, string(raw)); err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, v)
}

// handleGrafanaExport returns a dashboard as Grafana JSON, with the tiles given (?tiles=a,b) or all of its own.
func (s *Server) handleGrafanaExport(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	d, ok := dashboards.ByID(r.PathValue("id"))
	if !ok {
		s.fail(w, r, http.StatusNotFound, "there is no dashboard called "+r.PathValue("id"))
		return
	}
	var tiles []string
	if v := r.URL.Query().Get("tiles"); v != "" {
		for _, t := range splitComma(v) {
			tiles = append(tiles, t)
		}
	}
	w.Header().Set("Content-Disposition", `attachment; filename="perfuse-`+d.ID+`.grafana.json"`)
	s.ok(w, dashboards.Grafana(d, tiles))
}

func splitComma(v string) []string {
	var out []string
	cur := ""
	for _, r := range v {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
