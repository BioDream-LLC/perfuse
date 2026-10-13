package api

import (
	"context"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/chanscope"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Channel groups inside a tenant: a person limited to some groups sees, counts and acts on those channels only.
//
// This is the Rhapsody "locker" arrangement: one hospital group, one server, each facility's analysts looking after their
// own feeds and a central team with no limit looking after all of them. Tenants already separate organisations; this
// separates the people within one.
//
// It is applied once per request, in require, rather than by each handler remembering to. The visible channel names go on
// the request's context, where the message store narrows every count, list, chart and search to them, and the handlers
// that list things the message store does not hold (channel files, the running engine, the queue, alerts, metrics) filter
// through visibleChannel. A request naming a channel outside the person's groups in its path is answered 404 before any
// handler runs, so whether a channel exists is not disclosed either.

type visibleKey struct{}

// channelVisibility is the set of channels a limited session may see. nil means no limit.
type channelVisibility map[string]bool

// visibleFrom returns the request's visible channels, or nil for no limit.
func visibleFrom(ctx context.Context) channelVisibility {
	v, _ := ctx.Value(visibleKey{}).(channelVisibility)
	return v
}

// visibleChannel reports whether the request may see this channel. Always true for a session with no limit.
func visibleChannel(r *http.Request, name string) bool {
	v := visibleFrom(r.Context())
	return v == nil || v[name]
}

// scopeChannels works out a limited session's channels and puts them on the request. It returns false, having answered,
// when the request names a channel the session may not see.
func (s *Server) scopeChannels(w http.ResponseWriter, r *http.Request, sess *store.Session, minimum store.Role) (*http.Request, bool) {
	if len(sess.ChannelGroups) == 0 {
		return r, true
	}
	if serverWideRoute(r.Pattern, minimum) {
		s.fail(w, r, http.StatusForbidden,
			"this changes the whole server, so it is for people who see every channel; you see "+
				strings.Join(sess.ChannelGroups, ", "),
			"ask an administrator who looks after the whole server")
		return r, false
	}

	vis := channelVisibility{}
	repo := s.Channels
	if s.Repos != nil {
		var err error
		if repo, err = s.Repos.Repo(sess.TenantID); err != nil {
			s.failErr(w, r, err)
			return r, false
		}
	}
	// below holds channels the session sees but whose group gives it less than this route needs: somebody who edits the lab's
	// channels and views the admissions ones sees both on a list, and changes only the first.
	below := map[string]string{}
	if repo != nil {
		valid, _, err := repo.List()
		if err != nil {
			s.failErr(w, r, err)
			return r, false
		}
		for _, c := range valid {
			if !sess.SeesChannelGroup(c.Group) {
				continue
			}
			if sess.RoleIn(c.Group).AtLeast(minimum) {
				vis[c.Name] = true
			} else {
				below[c.Name] = c.Group
			}
		}
	}

	names := make([]string, 0, len(vis))
	for n := range vis {
		names = append(names, n)
	}
	ctx := context.WithValue(r.Context(), visibleKey{}, vis)
	ctx = chanscope.With(ctx, names)
	r = r.WithContext(ctx)

	// A channel named in the path. Answered as not found rather than forbidden: a facility's analyst is not told the
	// names of another facility's feeds by probing for them.
	if name := r.PathValue("name"); name != "" && strings.HasPrefix(r.URL.Path, "/api/channels/") && !vis[name] {
		if group, ok := below[name]; ok {
			// Seen, so its existence is no secret: say why rather than pretend it is not there.
			s.fail(w, r, http.StatusForbidden, fmt.Sprintf("this action needs the %s role, and in channel group %s you are %s",
				minimum, group, sess.RoleIn(group)))
			return r, false
		}
		s.fail(w, r, http.StatusNotFound, "no channel called "+name)
		return r, false
	}
	return r, true
}

// mayFileUnder reports whether a session may put a channel in this group: a limited person keeps their channels inside
// their groups, so a save cannot move a feed out of their sight or into another facility's.
func (s *Server) mayFileUnder(w http.ResponseWriter, r *http.Request, sess *store.Session, group string) bool {
	if sess.SeesChannelGroup(group) && sess.RoleIn(group).AtLeast(store.RoleEditor) {
		return true
	}
	if sess.SeesChannelGroup(group) {
		s.fail(w, r, http.StatusForbidden, fmt.Sprintf("in channel group %s you are %s, and saving a channel there needs editor",
			group, sess.RoleIn(group)))
		return false
	}
	s.fail(w, r, http.StatusForbidden,
		"you can only keep channels in your own groups ("+strings.Join(sess.ChannelGroups, ", ")+")",
		"set the channel's group to one of them, or ask an administrator")
	return false
}

// groupAllowed checks the group a channel file would be saved under, for a limited session. An unparseable file is let
// through, so the save reports what is wrong with it rather than a permission problem it does not have.
func (s *Server) groupAllowed(w http.ResponseWriter, r *http.Request, sess *store.Session, channels *ChannelRepo, raw []byte) bool {
	if len(sess.ChannelGroups) == 0 {
		return true
	}
	c, err := channels.Validate(raw)
	if err != nil || c == nil {
		return true
	}
	return s.mayFileUnder(w, r, sess, c.Group)
}

// seeFunc is visibleChannel as a function for a request, or nil when the session sees every channel.
func seeFunc(r *http.Request) func(string) bool {
	v := visibleFrom(r.Context())
	if v == nil {
		return nil
	}
	return func(name string) bool { return v[name] }
}

// alertsVisible keeps the alerts about a limited session's channels. An alert about no channel - the server's disk, a
// certificate - is for whoever looks after the server, who sees every channel, so a limited session does not see it.
func alertsVisible(r *http.Request, list []alerts.Alert) []alerts.Alert {
	if visibleFrom(r.Context()) == nil {
		return list
	}
	out := []alerts.Alert{}
	for _, a := range list {
		if a.Channel != "" && visibleChannel(r, a.Channel) {
			out = append(out, a)
		}
	}
	return out
}

// alertKeyVisible reports whether a firing alert, named by its key, is one the request may see.
func alertKeyVisible(r *http.Request, firing []alerts.Alert, key string) bool {
	if visibleFrom(r.Context()) == nil {
		return true
	}
	for _, a := range alertsVisible(r, firing) {
		if a.Key == key {
			return true
		}
	}
	return false
}

// seriesVisible keeps the metric series about a limited session's channels. A series with no channel label counts the
// whole server - every facility's traffic together - so a limited session does not see it.
func seriesVisible(r *http.Request, list []metrics.Series) []metrics.Series {
	if visibleFrom(r.Context()) == nil {
		return list
	}
	out := []metrics.Series{}
	for _, m := range list {
		if c := m.Labels["channel"]; c != "" && visibleChannel(r, c) {
			out = append(out, m)
		}
	}
	return out
}

// visibleSummaries keeps the channels a limited session may see and drops the files that would not load, whose group
// nobody can read. Unchanged for a session with no limit.
func visibleSummaries(r *http.Request, list []ChannelSummary, broken map[string]string) ([]ChannelSummary, map[string]string) {
	if visibleFrom(r.Context()) == nil {
		return list, broken
	}
	out := []ChannelSummary{}
	for _, c := range list {
		if visibleChannel(r, c.Name) {
			out = append(out, c)
		}
	}
	if broken != nil {
		broken = map[string]string{}
	}
	return out, broken
}

// delegatedAdminRoutes are the administrator actions a person limited to some channel groups may still take: looking after
// their own people and tokens (within their groups), starting and stopping their channels, and clearing their queues.
// That is the delegated administration of a Rhapsody locker. Everything else an administrator can do changes the whole
// server - sign-on, settings, restart, branding, fleet, alert rules, the SMART server - and is refused.
var delegatedAdminRoutes = map[string]bool{
	"GET /api/users": true, "POST /api/users": true, "PUT /api/users/{id}": true, "DELETE /api/users/{id}": true,
	"GET /api/tokens": true, "POST /api/tokens": true, "DELETE /api/tokens/{label}": true,
	"POST /api/channels/{name}/start": true, "POST /api/channels/{name}/stop": true,
	"POST /api/queue/skip": true, "POST /api/queue/drain": true, "POST /api/queue/remove": true,
}

// sharedWriteRoutes are writes below administrator that change something every facility shares: a code table, a script
// library, prior authorization decisions. A limited person reads them but does not change them for everybody else.
var sharedWriteRoutes = map[string]bool{
	"PUT /api/codesets/table": true, "POST /api/mirth/libraries": true, "POST /api/pas/cases/{id}/decide": true,
}

// serverWideRoute reports whether a route, needing this role, changes the whole server.
func serverWideRoute(pattern string, minimum store.Role) bool {
	if sharedWriteRoutes[pattern] {
		return true
	}
	return minimum.AtLeast(store.RoleAdmin) && !delegatedAdminRoutes[pattern]
}

// roleChoices is the answer to a role nobody has heard of.
const roleChoices = "role must be monitor, viewer, operator, editor or admin"

// manages reports whether a session may look after an account with these channel groups. Somebody who sees every channel
// looks after everybody. Somebody limited to some groups looks after accounts limited to some of the same groups, never an
// unlimited account - otherwise a facility's administrator could reset the central team's passwords.
func manages(sess *store.Session, groups []string) bool {
	if len(sess.ChannelGroups) == 0 {
		return true
	}
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		// Administrator in that group, not merely able to see it: an administrator of the lab's channels who views the
		// admissions ones looks after the lab's people only.
		if !sess.SeesChannelGroup(g) || !sess.RoleIn(g).AtLeast(store.RoleAdmin) {
			return false
		}
	}
	return true
}

// groupsToGrant checks the channel groups a session would give an account or a token. A limited session can only hand on
// its own groups, and a request that names none from a limited session gets the session's own: a facility's administrator
// creating a colleague creates one for their facility, not one who sees everything.
func (s *Server) groupsToGrant(w http.ResponseWriter, r *http.Request, sess *store.Session, asked []string) ([]string, bool) {
	var groups []string
	for _, g := range asked {
		if g = strings.TrimSpace(g); g != "" {
			if strings.Contains(g, ",") {
				s.fail(w, r, http.StatusBadRequest, "a channel group name cannot contain a comma: "+g)
				return nil, false
			}
			groups = append(groups, g)
		}
	}
	if len(sess.ChannelGroups) == 0 {
		return groups, true
	}
	mine := adminGroups(sess)
	if len(groups) == 0 {
		return mine, true
	}
	if !manages(sess, groups) {
		s.fail(w, r, http.StatusForbidden,
			"you can only give the channel groups you administer yourself ("+strings.Join(mine, ", ")+")")
		return nil, false
	}
	return groups, true
}

// adminGroups lists the channel groups a limited session administers.
func adminGroups(sess *store.Session) []string {
	var out []string
	for _, g := range sess.ChannelGroups {
		if sess.RoleIn(g).AtLeast(store.RoleAdmin) {
			out = append(out, g)
		}
	}
	return out
}

// groupsDetail is the audit wording for a set of channel groups.
func groupsDetail(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return ", channel groups " + strings.Join(groups, ", ")
}
