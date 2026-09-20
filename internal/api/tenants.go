package api

import (
	"errors"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// Tenant administration.
//
// Every endpoint here needs the platform role, not admin. A customer's own
// administrator must not be able to create tenants or read the list of them - in a
// managed service, the customer list is commercially sensitive and the other
// customers' existence is not their business.

type tenantResponse struct {
	Tenants []*tenant.Tenant `json:"tenants"`
	// ChannelCount is per tenant, because "how many channels does this customer
	// have" is the first thing anybody wants from this list and fetching it per row
	// would be a request each.
	ChannelCounts map[string]int `json:"channelCounts"`
}

func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	tenants, err := s.Store.ListTenants(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	counts := map[string]int{}
	if s.Repos != nil {
		for _, t := range tenants {
			repo, err := s.Repos.Repo(t.ID)
			if err != nil {
				continue
			}
			valid, broken, err := repo.List()
			if err != nil {
				continue
			}
			// Broken files are counted. A channel that will not load is still a
			// channel somebody wrote and still needs attention; omitting it makes a
			// tenant look emptier than it is.
			counts[string(t.ID)] = len(valid) + len(broken)
		}
	}

	s.ok(w, tenantResponse{Tenants: tenants, ChannelCounts: counts})
}

type createTenantRequest struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Notes string `json:"notes,omitempty"`
}

func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req createTenantRequest
	if !s.decode(w, r, &req) {
		return
	}

	t := &tenant.Tenant{ID: tenant.ID(req.ID), Name: req.Name, Notes: req.Notes}
	created, err := s.Store.CreateTenant(r.Context(), t)
	if err != nil {
		if errors.Is(err, store.ErrTenantExists) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		// Validation failures are the sender's to fix, so 400 rather than 500. The
		// message explains the rule, because "invalid tenant id" alone sends
		// somebody guessing.
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// The channel directory is created now rather than on first use, so somebody can
	// put a file in it immediately and so a permissions problem surfaces here rather
	// than at the first message.
	if s.Repos != nil {
		if _, err := s.Repos.Repo(created.ID); err != nil {
			s.failErr(w, r, err)
			return
		}
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		ID: sess.UserID, Username: sess.Username,
		Action: "tenant.create", Target: string(created.ID), IP: clientIP(r),
	})
	s.ok(w, created)
}

type updateTenantRequest struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	Notes    string `json:"notes,omitempty"`
}

func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id := tenant.ID(r.PathValue("id"))

	var req updateTenantRequest
	if !s.decode(w, r, &req) {
		return
	}

	updated, err := s.Store.UpdateTenant(r.Context(), id, req.Name, req.Disabled, req.Notes)
	if err != nil {
		if errors.Is(err, store.ErrTenantNotFound) {
			s.fail(w, r, http.StatusNotFound, err.Error())
			return
		}
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	action := "tenant.update"
	if req.Disabled {
		// Recorded distinctly, because suspending a customer's feeds is the kind of
		// thing somebody later needs to prove happened and when.
		action = "tenant.disable"
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		ID: sess.UserID, Username: sess.Username,
		Action: action, Target: string(id), IP: clientIP(r),
	})
	s.ok(w, updated)
}

func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id := tenant.ID(r.PathValue("id"))

	if err := s.Store.DeleteTenant(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrTenantNotFound) {
			s.fail(w, r, http.StatusNotFound, err.Error())
			return
		}
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// The channel files are deliberately left on disk. A channel definition is
	// frequently the only record of how an interface was configured, and deleting a
	// customer should not silently destroy it. Only the cached handle goes.
	if s.Repos != nil {
		s.Repos.Forget(id)
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		ID: sess.UserID, Username: sess.Username,
		Action: "tenant.delete", Target: string(id), IP: clientIP(r),
		Detail: "channel files and message history were kept",
	})
	s.ok(w, map[string]string{"status": "deleted"})
}

// handleTenancyStatus reports whether this instance is running more than one tenant.
//
// The front end uses it to decide whether to show tenancy at all. An installation
// with one tenant should never meet the word - a tenant switcher with one entry is
// pure confusion, and most operators of this program will never have a second one.
func (s *Server) handleTenancyStatus(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	n, err := s.Store.CountTenants(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{
		"multiTenant": n > 1,
		"count":       n,
		"tenant":      string(sess.TenantID),
		"isPlatform":  sess.Role == store.RolePlatform,
	})
}
