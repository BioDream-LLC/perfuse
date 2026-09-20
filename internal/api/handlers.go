package api

import (
	"net/http"
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

	s.ok(w, channelResponse{Channel: summarise(c, c.Path()), YAML: req.YAML})
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
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type updateUserRequest struct {
	Role     *string `json:"role,omitempty"`
	Password *string `json:"password,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	users, err := s.storeFor(sess).ListUsers(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
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
	if !role.Valid() {
		s.fail(w, r, http.StatusBadRequest, "role must be viewer, editor or admin")
		return
	}

	u, err := s.storeFor(sess).CreateUser(r.Context(), req.Username, req.Password, role)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	s.log().Info("user created", "user", u.Username, "role", string(u.Role), "by", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "user.create",
		Target: u.Username, Detail: "role " + string(u.Role), IP: clientIP(r),
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

	if req.Role != nil {
		role := store.Role(*req.Role)
		if !role.Valid() {
			s.fail(w, r, http.StatusBadRequest, "role must be viewer, editor or admin")
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
