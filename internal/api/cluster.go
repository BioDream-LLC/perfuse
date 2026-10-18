package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cluster"
	"github.com/biodream-llc/perfuse/internal/store"
)

// ClusterControl is what the console and API need from a server in a cluster: its view of the cluster, where a channel
// runs, and the controls (start and stop anywhere, step down, move).
type ClusterControl interface {
	Status(ctx context.Context) (cluster.Status, error)
	Runs(tenant, channel string) bool
	SetDesired(ctx context.Context, tenant, channel string, running bool, by string) error
	StepDown(ctx context.Context, d time.Duration, by string) error
	Move(ctx context.Context, tenant, channel, toNode, by string) error
	IsLeader() bool
	// RunningOn names the server running a channel, "" when none does.
	RunningOn(ctx context.Context, tenant, channel string) string
}

func (s *Server) registerCluster(mux *recordingMux) {
	mux.Handle("GET /api/cluster", s.require(store.RoleViewer, s.handleClusterStatus))
	mux.Handle("POST /api/cluster/step-down", s.require(store.RoleAdmin, s.handleClusterStepDown))
	mux.Handle("POST /api/cluster/channels/{name}/move", s.require(store.RoleAdmin, s.handleClusterMove))
}

type clusterStatus struct {
	Enabled bool `json:"enabled"`
	cluster.Status
}

func (s *Server) handleClusterStatus(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Cluster == nil {
		s.ok(w, clusterStatus{Enabled: false})
		return
	}
	st, err := s.Cluster.Status(r.Context())
	if err != nil {
		st.Error = err.Error()
	}
	s.ok(w, clusterStatus{Enabled: true, Status: st})
}

func (s *Server) handleClusterStepDown(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Cluster == nil {
		s.fail(w, r, http.StatusConflict, "this server is not in a cluster")
		return
	}
	if !s.Cluster.IsLeader() {
		s.fail(w, r, http.StatusConflict, "this server is not the leader; step down on the leader, or let it be")
		return
	}
	var req struct {
		Minutes int `json:"minutes"`
	}
	if r.ContentLength > 0 && !s.decode(w, r, &req) {
		return
	}
	if err := s.Cluster.StepDown(r.Context(), time.Duration(req.Minutes)*time.Minute, sess.Username); err != nil {
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "cluster.step_down", IP: clientIP(r)})
	s.ok(w, map[string]string{"status": "stepped down"})
}

func (s *Server) handleClusterMove(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Cluster == nil {
		s.fail(w, r, http.StatusConflict, "this server is not in a cluster")
		return
	}
	var req struct {
		Node string `json:"node"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}
	if _, err := channels.Get(name); err != nil {
		s.failErr(w, r, err)
		return
	}
	if err := s.Cluster.Move(r.Context(), string(sess.TenantID), name, strings.TrimSpace(req.Node), sess.Username); err != nil {
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "cluster.move", Target: name,
		Detail: "to " + req.Node, IP: clientIP(r)})
	s.ok(w, map[string]string{"status": "moving", "node": req.Node})
}

// clusterStartStop records a start or stop for the whole cluster, applies it here when this server runs the channel, and
// otherwise says where it will be applied. It reports whether it handled the request.
func (s *Server) clusterStartStop(w http.ResponseWriter, r *http.Request, sess *store.Session, rt *Runtime, name string, start bool) bool {
	if s.Cluster == nil {
		return false
	}
	tenant := string(sess.TenantID)
	if err := s.Cluster.SetDesired(r.Context(), tenant, name, start, sess.Username); err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the cluster's store did not take the change: "+err.Error())
		return true
	}
	action, status := "channel.stop", "stopped"
	if start {
		action, status = "channel.start", "started"
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: action, Target: name, IP: clientIP(r)})
	if s.Cluster.Runs(tenant, name) {
		var err error
		if start {
			if !rt.IsRunning(name) {
				err = rt.Start(name, s.log())
			}
		} else if rt.IsRunning(name) {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			err = rt.Stop(ctx, name)
		}
		if err != nil {
			s.fail(w, r, http.StatusConflict, err.Error())
			return true
		}
		s.ok(w, map[string]string{"status": status, "node": "this server"})
		return true
	}
	where := s.Cluster.RunningOn(r.Context(), tenant, name)
	msg := "recorded; the server that runs it applies it within a few seconds"
	if where == "" {
		msg = "recorded; no server runs it at the moment, and the next to take it will apply it"
	}
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested", "node": where, "message": msg})
	return true
}
