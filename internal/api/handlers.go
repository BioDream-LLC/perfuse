package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/store"
)

// The front end sends and receives YAML, not a bespoke JSON shape for a channel.
// That keeps one definition of what a channel is: the graphical builder produces
// the same text a hand editor would, so there is no second schema to drift.

type channelPayload struct {
	// YAML is the channel definition.
	YAML string `json:"yaml"`
}

type channelResponse struct {
	Channel ChannelSummary `json:"channel"`
	YAML    string         `json:"yaml"`
	// Restarted says a running channel was restarted to apply the change; RestartError why that did not work.
	Restarted    bool   `json:"restarted,omitempty"`
	RestartError string `json:"restartError,omitempty"`
}

type channelListResponse struct {
	Channels []ChannelSummary  `json:"channels"`
	Broken   map[string]string `json:"broken,omitempty"`
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	valid, broken, err := channels.List()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	// A limited person sees their groups' channels. A file that will not load has no group anyone can read, so it is shown
	// to those who see every channel, not to a facility that may not own it.
	valid, broken = visibleSummaries(r, valid, broken)
	if valid == nil {
		valid = []ChannelSummary{}
	}
	s.ok(w, channelListResponse{Channels: valid, Broken: broken})
}

func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	c, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	raw, err := channels.RawYAML(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	shown, err := s.yamlFor(sess, raw)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	s.ok(w, channelResponse{Channel: summarise(c, c.Path()), YAML: string(shown)})
}

// yamlFor returns a channel definition with credentials redacted unless the caller may read them.
//
// One function for both the detail view and the download, because they served the same bytes and only one of them would
// have been remembered. A viewer could read the password for every downstream system - SFTP, databases, API tokens -
// from a role that exists to watch message flow.
func (s *Server) yamlFor(sess *store.Session, raw []byte) ([]byte, error) {
	if mayReadSecrets(string(sess.Role)) {
		return raw, nil
	}
	return redactSecretsInYAML(raw)
}

// handleGetChannelYAML serves the file for download, which is the share button.
func (s *Server) handleGetChannelYAML(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	raw, err := channels.RawYAML(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	filename, err := filenameFor(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	shown, err := s.yamlFor(sess, raw)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(shown)
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	var req channelPayload
	if !s.decode(w, r, &req) {
		return
	}

	c, err := channels.Validate([]byte(req.YAML))
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	// A successful validation returns the interpretation, not just "ok", so the
	// builder can show what the definition actually means: which paths it reads,
	// what it acknowledges, where it sends.
	s.ok(w, channelResponse{Channel: summarise(c, "(unsaved)"), YAML: req.YAML})
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	var req channelPayload
	if !s.decode(w, r, &req) {
		return
	}

	if !s.groupAllowed(w, r, sess, channels, []byte(req.YAML)) {
		return
	}
	if !s.underChannelLimit(w, r, sess, channels) {
		return
	}
	c, err := channels.Create([]byte(req.YAML))
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	s.log().Info("channel created", "channel", c.Name, "user", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "channel.create",
		Target: c.Name, IP: clientIP(r),
	})

	s.ok(w, channelResponse{Channel: summarise(c, c.Path()), YAML: req.YAML})
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	var req channelPayload
	if !s.decode(w, r, &req) {
		return
	}

	// Keep the previous text so the audit entry can say what actually changed.
	before, _ := channels.RawYAML(name)

	if !s.groupAllowed(w, r, sess, channels, []byte(req.YAML)) {
		return
	}
	c, err := channels.Update(name, []byte(req.YAML))
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	detail := "updated"
	if !strings.EqualFold(c.Name, name) {
		detail = "renamed from " + name
	} else if string(before) == req.YAML {
		detail = "saved with no changes"
	}

	s.log().Info("channel updated", "channel", c.Name, "user", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "channel.update",
		Target: c.Name, Detail: detail, IP: clientIP(r),
	})

	// A running channel is restarted so the change applies. Saving used to write the file and leave the old version running,
	// so a changed address or certificate took effect only at the next server restart, with nothing to say so: found by
	// changing a destination's CA file in the builder and watching delivery keep failing on the old one.
	resp := channelResponse{Channel: summarise(c, c.Path()), YAML: req.YAML}
	if rt := s.optionalRuntime(sess); rt != nil && rt.IsRunning(name) {
		if err := rt.Stop(r.Context(), name); err != nil {
			resp.RestartError = "saved, but the running version could not be stopped: " + err.Error()
		} else if err := rt.Start(c.Name, s.log()); err != nil {
			resp.RestartError = "saved and stopped, but it did not start again: " + err.Error()
		} else {
			resp.Restarted = true
			s.log().Info("channel restarted to apply the change", "channel", c.Name, "user", sess.Username)
		}
	}

	s.ok(w, resp)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	if err := channels.Delete(name); err != nil {
		s.failErr(w, r, err)
		return
	}

	s.log().Info("channel deleted", "channel", name, "user", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "channel.delete",
		Target: name, IP: clientIP(r),
	})

	s.ok(w, map[string]string{"status": "deleted"})
}

// ---------- users ----------

type createUserRequest struct {
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	Role          string   `json:"role"`
	ChannelGroups []string `json:"channelGroups,omitempty"`
	// GroupRoles gives the account a different role in some of its channel groups.
	GroupRoles map[string]string `json:"groupRoles,omitempty"`
}

type updateUserRequest struct {
	Role     *string `json:"role,omitempty"`
	Password *string `json:"password,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
	// ChannelGroups replaces the account's channel groups; an empty list removes the limit.
	ChannelGroups *[]string `json:"channelGroups,omitempty"`
	// GroupRoles replaces the account's per-group roles; an empty map gives it its own role in every group.
	GroupRoles *map[string]string `json:"groupRoles,omitempty"`
}

// groupRolesToGrant checks per-group roles for an account limited to these channel groups.
//
// Each group must be one of the account's: a role in a group the account cannot see would be a grant nobody could see either.
// A limited administrator may set them only in groups they administer, and never above their own role there, so delegated
// administration cannot mint a stronger account than the one doing it.
func (s *Server) groupRolesToGrant(w http.ResponseWriter, r *http.Request, sess *store.Session, groups []string,
	asked map[string]string) (map[string]store.Role, bool) {
	if len(asked) == 0 {
		return nil, true
	}
	if len(groups) == 0 {
		s.fail(w, r, http.StatusBadRequest, "a role per channel group needs the account limited to channel groups; "+
			"an account that sees every channel has one role")
		return nil, false
	}
	in := map[string]bool{}
	for _, g := range groups {
		in[g] = true
	}
	out := map[string]store.Role{}
	for g, rs := range asked {
		g = strings.TrimSpace(g)
		role := store.Role(strings.TrimSpace(rs))
		if g == "" {
			continue
		}
		if !in[g] {
			s.fail(w, r, http.StatusBadRequest, "channel group "+g+" is not one of the account's channel groups")
			return nil, false
		}
		if !role.Valid() || role == store.RolePlatform {
			s.fail(w, r, http.StatusBadRequest, roleChoices)
			return nil, false
		}
		if len(sess.ChannelGroups) > 0 && !sess.RoleIn(g).AtLeast(role) {
			s.fail(w, r, http.StatusForbidden, fmt.Sprintf("in channel group %s you are %s, so you cannot make anyone %s there",
				g, sess.RoleIn(g), role))
			return nil, false
		}
		out[g] = role
	}
	return out, true
}

// groupRolesDetail is the audit wording for per-group roles.
func groupRolesDetail(m map[string]store.Role) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for g, r := range m {
		parts = append(parts, g+" "+string(r))
	}
	sort.Strings(parts)
	return ", " + strings.Join(parts, ", ")
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	users, err := s.storeFor(sess).ListUsers(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if len(sess.ChannelGroups) > 0 {
		// A facility's administrator sees the people they look after: those inside their own groups.
		mine := users[:0]
		for _, u := range users {
			if manages(sess, u.ChannelGroups) {
				mine = append(mine, u)
			}
		}
		users = mine
	}
	if users == nil {
		users = []*store.User{}
	}
	s.ok(w, map[string]any{"users": users})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req createUserRequest
	if !s.decode(w, r, &req) {
		return
	}

	role := store.Role(req.Role)
	if !role.Valid() || role == store.RolePlatform {
		s.fail(w, r, http.StatusBadRequest, roleChoices)
		return
	}
	groups, ok := s.groupsToGrant(w, r, sess, req.ChannelGroups)
	if !ok {
		return
	}

	groupRoles, ok := s.groupRolesToGrant(w, r, sess, groups, req.GroupRoles)
	if !ok {
		return
	}

	u, err := s.storeFor(sess).CreateUser(r.Context(), req.Username, req.Password, role)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if len(groupRoles) > 0 {
		if err := s.storeFor(sess).SetGroupRoles(r.Context(), u.ID, groupRoles); err != nil {
			_ = s.storeFor(sess).DeleteUser(r.Context(), u.ID)
			s.failErr(w, r, err)
			return
		}
		u.GroupRoles = groupRoles
	}
	if len(groups) > 0 {
		// Removed rather than left unlimited if the limit cannot be recorded: an account meant for one facility must
		// never exist, even briefly, with every facility's channels.
		if err := s.storeFor(sess).SetChannelGroups(r.Context(), u.ID, groups); err != nil {
			_ = s.storeFor(sess).DeleteUser(r.Context(), u.ID)
			s.failErr(w, r, err)
			return
		}
		u.ChannelGroups = groups
	}

	s.log().Info("user created", "user", u.Username, "role", string(u.Role), "by", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "user.create",
		Target: u.Username, Detail: "role " + string(u.Role) + groupsDetail(u.ChannelGroups) + groupRolesDetail(u.GroupRoles),
		IP: clientIP(r),
	})

	s.ok(w, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid user id")
		return
	}

	var req updateUserRequest
	if !s.decode(w, r, &req) {
		return
	}

	target, err := s.storeFor(sess).GetUserByID(r.Context(), id)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if !manages(sess, target.ChannelGroups) {
		s.fail(w, r, http.StatusNotFound, "no such user")
		return
	}

	// Refuse the changes that lock the person making them out of the system.
	//
	// Every row in the user list has a role dropdown that saves the moment it changes. On your own row that
	// is one click from removing your own administrator rights - no confirmation, and the sections needed to
	// undo it are the first thing you lose. Recovery is editing the database by hand.
	//
	// Guarded here rather than only in the interface because the interface is not the only caller, and
	// because a rule about who may do what belongs on the server. Another administrator can still make this
	// change; what is refused is doing it to yourself, which is the case with no way back.
	if target.ID == sess.UserID {
		demotingSelf := req.Role != nil && store.Role(*req.Role) != target.Role
		disablingSelf := req.Disabled != nil && *req.Disabled
		if disablingSelf {
			s.fail(w, r, http.StatusConflict,
				"you cannot disable your own account; ask another administrator to do it")
			return
		}
		if demotingSelf && target.Role == store.RoleAdmin {
			s.fail(w, r, http.StatusConflict,
				"you cannot change your own role away from administrator; "+
					"ask another administrator to do it, or you will be locked out of the sections needed to undo it")
			return
		}
	}

	// Refuse the changes that lock everybody out of the system. Losing the last
	// administrator means editing the database by hand to recover.
	if target.Role == store.RoleAdmin && !target.Disabled {
		demoting := req.Role != nil && store.Role(*req.Role) != store.RoleAdmin
		disabling := req.Disabled != nil && *req.Disabled
		if demoting || disabling {
			n, err := s.storeFor(sess).CountAdmins(r.Context())
			if err != nil {
				s.failErr(w, r, err)
				return
			}
			if n <= 1 {
				s.fail(w, r, http.StatusConflict,
					"this is the only enabled administrator; promote someone else first")
				return
			}
		}
	}

	if req.ChannelGroups != nil {
		if target.ID == sess.UserID && len(sess.ChannelGroups) > 0 {
			s.fail(w, r, http.StatusConflict, "you cannot change your own channel groups; ask an administrator who sees every channel")
			return
		}
		groups, ok := s.groupsToGrant(w, r, sess, *req.ChannelGroups)
		if !ok {
			return
		}
		if err := s.storeFor(sess).SetChannelGroups(r.Context(), id, groups); err != nil {
			s.failErr(w, r, err)
			return
		}
		detail := "every channel"
		if len(groups) > 0 {
			detail = "channel groups " + strings.Join(groups, ", ")
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: sess.Username, Action: "user.channel_groups",
			Target: target.Username, Detail: "set to " + detail, IP: clientIP(r),
		})
	}

	if req.GroupRoles != nil {
		if target.ID == sess.UserID && len(sess.ChannelGroups) > 0 {
			s.fail(w, r, http.StatusConflict, "you cannot change your own roles; ask an administrator who sees every channel")
			return
		}
		groups := target.ChannelGroups
		if req.ChannelGroups != nil {
			current, err := s.storeFor(sess).GetUserByID(r.Context(), id)
			if err != nil {
				s.failErr(w, r, err)
				return
			}
			groups = current.ChannelGroups
		}
		groupRoles, ok := s.groupRolesToGrant(w, r, sess, groups, *req.GroupRoles)
		if !ok {
			return
		}
		if err := s.storeFor(sess).SetGroupRoles(r.Context(), id, groupRoles); err != nil {
			s.failErr(w, r, err)
			return
		}
		if err := s.storeFor(sess).DeleteUserSessions(r.Context(), id); err != nil {
			s.failErr(w, r, err)
			return
		}
		detail := "its own role in every group"
		if len(groupRoles) > 0 {
			detail = strings.TrimPrefix(groupRolesDetail(groupRoles), ", ")
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: sess.Username, Action: "user.group_roles",
			Target: target.Username, Detail: "set to " + detail, IP: clientIP(r),
		})
	} else if req.ChannelGroups != nil && len(target.GroupRoles) > 0 {
		// A group dropped from the account takes its role with it, so a later re-add does not bring back a grant nobody made again.
		current, err := s.storeFor(sess).GetUserByID(r.Context(), id)
		if err == nil {
			kept := map[string]store.Role{}
			for _, g := range current.ChannelGroups {
				if role, ok := target.GroupRoles[g]; ok {
					kept[g] = role
				}
			}
			_ = s.storeFor(sess).SetGroupRoles(r.Context(), id, kept)
		}
	}

	if req.Role != nil {
		role := store.Role(*req.Role)
		if !role.Valid() || role == store.RolePlatform {
			s.fail(w, r, http.StatusBadRequest, roleChoices)
			return
		}
		if err := s.storeFor(sess).SetRole(r.Context(), id, role); err != nil {
			s.failErr(w, r, err)
			return
		}
		// A role change must take effect now, not when the cookie expires.
		if err := s.storeFor(sess).DeleteUserSessions(r.Context(), id); err != nil {
			s.failErr(w, r, err)
			return
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: sess.Username, Action: "user.role",
			Target: target.Username, Detail: "set to " + *req.Role, IP: clientIP(r),
		})
	}

	if req.Password != nil {
		if err := s.storeFor(sess).SetPassword(r.Context(), id, *req.Password); err != nil {
			s.failErr(w, r, err)
			return
		}
		if err := s.storeFor(sess).DeleteUserSessions(r.Context(), id); err != nil {
			s.failErr(w, r, err)
			return
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: sess.Username, Action: "user.password",
			Target: target.Username, IP: clientIP(r),
		})
	}

	if req.Disabled != nil {
		if err := s.storeFor(sess).SetDisabled(r.Context(), id, *req.Disabled); err != nil {
			s.failErr(w, r, err)
			return
		}
		action := "user.enable"
		if *req.Disabled {
			action = "user.disable"
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: sess.Username, Action: action,
			Target: target.Username, IP: clientIP(r),
		})
	}

	updated, err := s.storeFor(sess).GetUserByID(r.Context(), id)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, updated)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid user id")
		return
	}

	target, err := s.storeFor(sess).GetUserByID(r.Context(), id)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	if !manages(sess, target.ChannelGroups) {
		s.fail(w, r, http.StatusNotFound, "no such user")
		return
	}
	if target.ID == sess.UserID {
		s.fail(w, r, http.StatusConflict, "you cannot delete your own account")
		return
	}
	if target.Role == store.RoleAdmin && !target.Disabled {
		n, err := s.storeFor(sess).CountAdmins(r.Context())
		if err != nil {
			s.failErr(w, r, err)
			return
		}
		if n <= 1 {
			s.fail(w, r, http.StatusConflict,
				"this is the only enabled administrator; promote someone else first")
			return
		}
	}

	if err := s.storeFor(sess).DeleteUser(r.Context(), id); err != nil {
		s.failErr(w, r, err)
		return
	}

	s.log().Info("user deleted", "user", target.Username, "by", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "user.delete",
		Target: target.Username, IP: clientIP(r),
	})

	s.ok(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	// Scoped to the caller's tenant, except for a platform account investigating across customers.
	//
	// This was s.Store.ListAudit, which returns every tenant's rows. A tenant administrator could read another
	// organisation's usernames and everything they had changed.
	var entries []store.AuditEntry
	var err error
	if crossTenant, ok := s.crossTenantStore(sess); ok {
		entries, err = crossTenant.ListAudit(r.Context(), limit)
	} else {
		entries, err = s.storeFor(sess).ListAudit(r.Context(), limit)
	}
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	s.ok(w, map[string]any{"entries": entries})
}
