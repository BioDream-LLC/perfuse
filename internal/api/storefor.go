package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/store"
)

// storeFor returns a store scoped to the session's tenant.
//
// This exists because the audit log was not scoped. A tenant administrator reading /api/audit saw every tenant's
// activity: their usernames, what they changed and when. Found by attempting it rather than by reading the code, and the
// store had the correct scoped accessor all along - the handler simply called the unscoped one.
//
// That is the whole problem. Nothing forced a handler to scope, so scoping was a thing each handler had to remember, and
// one did not. Channels and runtimes already had mandatory accessors with drift guards for exactly this reason; the store
// did not, and the gap was where the leak was.
//
// In single-tenant operation - nearly every installation - this returns a scope over the default tenant, so the path is
// the same one and there is no separate code to rot.
func (s *Server) storeFor(sess *store.Session) *store.Scoped {
	if sess == nil {
		// Scoped to the default tenant rather than unscoped. A nil session reaching here is a wiring mistake, and
		// the safe reading of a mistake is the narrower one.
		return s.Store.ScopeUnchecked(store.DefaultTenant)
	}

	if sess.TenantID == "" {
		return s.Store.ScopeUnchecked(store.DefaultTenant)
	}

	return s.Store.ScopeUnchecked(sess.TenantID)
}

// crossTenantStore returns the unscoped store for a caller entitled to see every tenant.
//
// Only the platform role, and only where seeing across customers is the point - investigating an incident that spans
// them. Named so that its use is conspicuous in a diff: an unscoped read is not something to reach for casually, and
// "crossTenant" in a handler is a question a reviewer should ask about.
//
// Refuses by returning false rather than silently narrowing, because a platform administrator who expected every tenant
// and quietly got one would draw the wrong conclusion from what they saw.
func (s *Server) crossTenantStore(sess *store.Session) (*store.Store, bool) {
	if sess == nil || sess.Role != store.RolePlatform {
		return nil, false
	}
	return s.Store, true
}

// requirePlatform refuses a request that is not from a platform account, explaining why.
//
// For settings and other process-wide state. A tenant administrator is an administrator of their own organisation, and
// process-wide configuration is not theirs: reading it shows another customer's arrangements, and changing it changes
// everybody's.
func (s *Server) requirePlatform(w http.ResponseWriter, r *http.Request, sess *store.Session, what string) bool {
	if s.Repos == nil {
		// Single-tenant. There is no other customer to protect, and requiring a platform account would mean an
		// administrator of a single-tenant installation could not reach their own settings.
		return true
	}

	if sess.Role == store.RolePlatform {
		return true
	}

	s.fail(w, r, http.StatusForbidden,
		what+" is shared by every tenant on this server, so it needs a platform account rather than an "+
			"administrator of one tenant")
	return false
}
