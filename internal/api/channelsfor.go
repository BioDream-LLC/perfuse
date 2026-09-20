package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/store"
)

// Which channels a request is allowed to see.
//
// This is the most security-sensitive function in the package, because getting it wrong means one customer
// sees another customer's channels - and a channel file names hosts, ports, credentials and patient flows.
// In a system a consultancy runs for fifty clinics, that is the failure that ends the business.
//
// So the shape is deliberate: **there is no way to ask for "the channels" without supplying a session.** The
// tenant is not a parameter that can be defaulted, forgotten or passed as an empty string; it is read from
// the session, which was established by authentication. That is the same reasoning behind store.Scoped, and
// it is the reason this is a method taking a session rather than a field somebody can reach for.
//
// # Why it returns a bool rather than an error
//
// Following requireMessages: the failure has already been written to the response by the time this returns
// false, so a handler cannot accidentally continue with a nil repository. A handler that ignores an error
// return still compiles; one that ignores this reads obviously wrong.

// channelsFor returns the channel repository for the session's tenant.
//
// In single-tenant operation - which is nearly every installation - Repos is nil and this is the one shared
// repository. Nothing about the single-tenant path changes, which matters: multi-tenancy must not make the
// common case slower or more fragile.
func (s *Server) channelsFor(
	w http.ResponseWriter, r *http.Request, sess *store.Session,
) (*ChannelRepo, bool) {
	if s.Repos == nil {
		return s.Channels, true
	}

	// The platform role has no channels of its own. It administers tenants, and giving it a channel list
	// would mean deciding whose - so it is refused with an explanation rather than being shown an arbitrary
	// tenant's files or an empty list that looks like a broken installation.
	if sess.Role == store.RolePlatform && string(sess.TenantID) == "" {
		s.fail(w, r, http.StatusBadRequest,
			"a platform account is not inside a tenant, so it has no channels to list",
			"sign in as an administrator of a particular tenant, or use the tenants endpoints")
		return nil, false
	}

	repo, err := s.Repos.Repo(sess.TenantID)
	if err != nil {
		// Reported rather than falling back to the shared repository. A fallback here would be a silent
		// cross-tenant read, which is exactly the bug this function exists to make impossible.
		s.failErr(w, r, err)
		return nil, false
	}

	return repo, true
}
