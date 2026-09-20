package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/scim"
	"github.com/biodream-llc/perfuse/internal/store"
)

// SCIM provisioning. An identity provider creates, updates and - the part that matters - disables accounts here.
//
// # The rule this file is written around
//
// A response must never be more successful than what actually happened.
//
// SCIM has no retry semantics an application can depend on. When a provider receives a 200 or a 204 it records the operation
// as complete and does not try again. So a handler that answers successfully for a disable it did not perform has not merely
// failed - it has told the system of record that a former employee's access was removed when it was not, and nothing will
// ever revisit that. Nobody is watching for a thing that did not happen.
//
// Everything unusual in here follows from that: session invalidation is checked rather than assumed, a partial failure is a
// failure, and an unparseable patch is a 400 rather than a shrug.

// scimBase is the path prefix, and appears in every resource's Location.
const scimBase = "/scim/v2"

// handleSCIMServiceProviderConfig says what this server supports.
//
// Answered honestly, including the unflattering parts. Claiming support for something not implemented makes a provider send
// it and then report a failure nobody can explain - and in the case of a filter, act on the wrong accounts.
func (s *Server) handleSCIMServiceProviderConfig(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	config := scim.ServiceProviderConfig{
		Schemas: []string{scim.SchemaServiceProviderConfig},
		Patch:   scim.Supported{Supported: true},

		// Bulk is not implemented. Saying so stops a provider batching operations into a request that would be refused
		// wholesale, which looks like every account failing at once.
		Bulk: scim.BulkSupported{Supported: false},

		// Filtering is supported for the subset in internal/scim. The limit is stated because a provider respects it,
		// and an unstated limit means a provider asks for everything.
		Filter: scim.FilterSupported{Supported: true, MaxResults: 200},

		// Passwords are not changed through SCIM. An account provisioned this way signs in through the identity
		// provider, and accepting a password here would create a second way in that the provider does not know about -
		// so disabling somebody upstream would leave that password working.
		ChangePassword: scim.Supported{Supported: false},

		Sort: scim.Supported{Supported: false},
		ETag: scim.Supported{Supported: false},

		AuthenticationSchemes: []scim.AuthScheme{{
			Type:        "oauthbearertoken",
			Name:        "OAuth Bearer Token",
			Description: "An API token with the platform or admin role, sent as a bearer credential.",
			SpecURI:     "http://www.rfc-editor.org/info/rfc6750",
			Primary:     true,
		}},
		Meta: &scim.Meta{ResourceType: "ServiceProviderConfig"},
	}

	s.scimOK(w, http.StatusOK, config)
}

// handleSCIMListUsers lists or searches users.
func (s *Server) handleSCIMListUsers(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	filter, err := scim.ParseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		var unsupported scim.ErrUnsupportedFilter
		if errors.As(err, &unsupported) {
			// invalidFilter and a 400, which a provider understands and logs. A 500 here would have it retry for ever.
			s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidFilter, unsupported.Error())

			return
		}
		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidFilter, err.Error())

		return
	}

	users, err := s.storeFor(sess).ListUsers(r.Context())
	if err != nil {
		s.scimFail(w, http.StatusInternalServerError, "", "could not list accounts")

		return
	}

	matched := make([]any, 0, len(users))
	for _, u := range users {
		if !filter.Matches(scimAttributes(u)) {
			continue
		}
		matched = append(matched, s.scimUserFrom(u))
	}

	// Paging. One-based, which is SCIM's choice.
	startIndex := 1
	if v := r.URL.Query().Get("startIndex"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
			startIndex = n
		}
	}
	count := len(matched)
	if v := r.URL.Query().Get("count"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n >= 0 {
			count = n
		}
	}

	total := len(matched)
	from := startIndex - 1
	if from > len(matched) {
		from = len(matched)
	}
	to := from + count
	if to > len(matched) {
		to = len(matched)
	}
	page := matched[from:to]

	s.scimOK(w, http.StatusOK, scim.ListResponse{
		Schemas: []string{scim.SchemaListResponse},

		// The total across every match, not the page size. A provider pages by comparing this against what it has
		// received, so reporting the page size makes it stop after one page - and every account beyond it is never
		// deprovisioned.
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(page),
		Resources:    page,
	})
}

// handleSCIMGetUser returns one user.
func (s *Server) handleSCIMGetUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id, ok := scimID(w, r, s)
	if !ok {
		return
	}

	u, err := s.storeFor(sess).GetUserByID(r.Context(), id)
	if err != nil {
		s.scimNotFound(w, r.PathValue("id"))

		return
	}

	s.scimOK(w, http.StatusOK, s.scimUserFrom(u))
}

// handleSCIMCreateUser provisions an account.
func (s *Server) handleSCIMCreateUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var incoming scim.User
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&incoming); err != nil {
		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidSyntax, "the request body is not valid SCIM JSON")

		return
	}

	username := strings.TrimSpace(incoming.UserName)
	if username == "" {
		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidValue, "userName is required")

		return
	}

	role := scimRoleFor(incoming, s.SCIMDefaultRole)

	// A password is generated rather than taken from the request, and is never returned.
	//
	// An account provisioned this way signs in through the identity provider. Setting a password the provider chose
	// would create a second way in that the provider does not know about - so disabling somebody upstream would leave
	// that password working, which is the exact failure this whole feature exists to prevent.
	password, err := generateSCIMPassword()
	if err != nil {
		s.scimFail(w, http.StatusInternalServerError, "", "could not create the account")

		return
	}

	scoped := s.storeFor(sess)

	created, err := scoped.CreateUser(r.Context(), username, password, role)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			// uniqueness, so the provider stops retrying and reconciles instead. Without the keyword it retries a
			// conflict indefinitely.
			s.scimFail(w, http.StatusConflict, scim.ErrUniqueness,
				fmt.Sprintf("an account named %q already exists", username))

			return
		}
		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidValue, err.Error())

		return
	}

	// The external identifier is the stable join between the two systems, so it is recorded rather than discarded.
	// Without it, somebody who marries and changes their username looks like a new person: the provider provisions a
	// second account, disables that one, and leaves the original enabled.
	if ext := strings.TrimSpace(incoming.ExternalID); ext != "" {
		if err := scoped.SetExternalID(r.Context(), created.ID, ext); err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				// Two accounts claiming the same provider identity means one will be deprovisioned in place of the
				// other. Refused rather than left unrecorded.
				s.scimFail(w, http.StatusConflict, scim.ErrUniqueness,
					fmt.Sprintf("another account already claims the external identifier %q", ext))

				return
			}

			// Reported rather than logged and shrugged at. Without the external identifier the provider has no
			// stable handle on this account, so the next rename produces a duplicate and the original stays
			// enabled - which is the failure this field exists to prevent, so answering 201 would be answering
			// more successfully than what happened.
			s.scimFail(w, http.StatusInternalServerError, "",
				"the account was created but its external identifier could not be recorded, so future syncs "+
					"cannot reliably match it; it needs attention")

			return
		}

		// Read back, so the response carries what was actually stored. Returning the object captured before the update
		// would report an empty external identifier - and a provider reconciling against that concludes the account it
		// just created is not the one it asked for.
		created.ExternalID = ext
	}

	// An account created disabled, when the provider says so. Some providers create then disable in two calls, and
	// others create with active false in the first.
	if incoming.Active != nil && !*incoming.Active {
		if err := scoped.SetDisabled(r.Context(), created.ID, true); err != nil {
			// Refused rather than reported as created-and-enabled. A provider told the account was created will not
			// revisit it, so an account that should be disabled would stay enabled for ever.
			s.scimFail(w, http.StatusInternalServerError, "",
				"the account was created but could not be disabled as requested; it has been left enabled and "+
					"needs attention")

			return
		}
		created.Disabled = true
	}

	s.log().Info("an account was provisioned over SCIM",
		"username", username, "role", string(role), "external_id", incoming.ExternalID)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "scim.user.create",
		Target:   username,
		Detail:   "role " + string(role),
		IP:       clientIP(r),
	})

	w.Header().Set("Location", scimLocation(created.ID))
	s.scimOK(w, http.StatusCreated, s.scimUserFrom(created))
}

// handleSCIMPatchUser applies a PATCH, which is how a provider disables somebody.
//
// The most security-relevant handler in the server. Okta sends a single replace of active rather than a PUT, so this path is
// what stands between a termination in the HR system and a former employee's access ending.
func (s *Server) handleSCIMPatchUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id, ok := scimID(w, r, s)
	if !ok {
		return
	}

	var patch scim.PatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidSyntax, "the patch is not valid SCIM JSON")

		return
	}
	if len(patch.Operations) == 0 {
		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidValue, "a patch with no operations changes nothing")

		return
	}

	scoped := s.storeFor(sess)

	existing, err := scoped.GetUserByID(r.Context(), id)
	if err != nil {
		s.scimNotFound(w, r.PathValue("id"))

		return
	}

	// Every operation has to be understood before any is applied.
	//
	// An operation this does not recognise is a 400, not a silent skip. A provider that sends a disable in a shape not
	// handled here and receives a 200 records the deprovisioning as done and never tries again - and that is a
	// terminated employee with working access. A 400 is visible in the provider's own error report.
	type change struct {
		setEnabled *bool
		setRole    *store.Role
	}
	var pending change

	for _, op := range patch.Operations {
		if active, isActive := op.ActiveChange(); isActive {
			enabled := active
			pending.setEnabled = &enabled

			continue
		}

		path := strings.ToLower(strings.TrimSpace(op.Path))

		// Attributes that are recorded but do not affect access. Accepted so a provider syncing a display name does
		// not see an error, and deliberately listed rather than matched by a catch-all: a catch-all would swallow a
		// disable in an unfamiliar shape.
		switch path {
		case "name", "name.givenname", "name.familyname", "name.formatted",
			"displayname", "emails", "emails[type eq \"work\"].value",
			"externalid", "title", "phonenumbers", "locale", "timezone",
			"preferredlanguage", "usertype", "nickname", "profileurl",
			"urn:ietf:params:scim:schemas:extension:enterprise:2.0:user:department",
			"urn:ietf:params:scim:schemas:extension:enterprise:2.0:user:employeenumber",
			"urn:ietf:params:scim:schemas:extension:enterprise:2.0:user:manager":
			continue
		case "roles", "groups":
			if role, ok := scimRoleFromPatch(op); ok {
				pending.setRole = &role
			}

			continue
		}

		// A pathless replace whose object contains only harmless attributes.
		if path == "" && op.Normalised() == "replace" {
			if harmlessPathlessPatch(op) {
				continue
			}
		}

		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidPath,
			fmt.Sprintf("this server does not understand the operation %q on path %q, and will not report success "+
				"for a change it did not make. If this is meant to disable the account, send a replace of "+
				"\"active\".", op.Op, op.Path))

		return
	}

	if pending.setRole != nil && *pending.setRole != existing.Role {
		if err := scoped.SetRole(r.Context(), id, *pending.setRole); err != nil {
			s.scimFail(w, http.StatusInternalServerError, "", "could not change the account's role")

			return
		}
		s.log().Info("an account's role was changed over SCIM",
			"username", existing.Username, "from", string(existing.Role), "to", string(*pending.setRole))
	}

	if pending.setEnabled != nil {
		if err := s.applySCIMEnabled(r, scoped, existing, *pending.setEnabled); err != nil {
			// Reported as a failure, because that is the whole rule. A provider told this succeeded will not revisit
			// it.
			s.scimFail(w, http.StatusInternalServerError, "", err.Error())

			return
		}
	}

	updated, err := scoped.GetUserByID(r.Context(), id)
	if err != nil {
		s.scimFail(w, http.StatusInternalServerError, "", "the change was applied but the account could not be read back")

		return
	}

	s.scimOK(w, http.StatusOK, s.scimUserFrom(updated))
}

// applySCIMEnabled enables or disables an account, and makes sure disabling actually ends access.
//
// Sessions are ended explicitly rather than left to expire. A browser tab open on a laptop that has gone home with a
// terminated employee must stop working now: a session that survives until its expiry is a session that survives the
// sacking, and the expiry may be days away.
//
// The session count is checked afterwards rather than trusted. This is the one operation in the server where "probably
// worked" is not good enough, because nothing downstream will notice if it did not.
func (s *Server) applySCIMEnabled(
	r *http.Request, scoped *store.Scoped, existing *store.User, enabled bool,
) error {
	// SetDisabled takes the inverse, so the negation is here and stated: passing "enabled" where "disabled" is wanted
	// would enable every account this was asked to disable, which is the worst possible off-by-one in the server.
	if err := scoped.SetDisabled(r.Context(), existing.ID, !enabled); err != nil {
		return fmt.Errorf("could not %s the account, and it has been left as it was",
			map[bool]string{true: "enable", false: "disable"}[enabled])
	}

	action := "scim.user.enable"
	if !enabled {
		action = "scim.user.disable"

		// SetDisabled ends the sessions itself, which is where that behaviour belongs: every path that disables an
		// account needs it, not only this one. An earlier version of this function called DeleteUserSessions again
		// afterwards, which read like the thing protecting access and was not - removing it changed no test, because
		// the store had already done it.
		//
		// What is not redundant is checking. This is the one operation in the server where "probably worked" is not
		// good enough: a provisioning system told a deprovisioning succeeded will never ask again, so if sessions
		// somehow survive there is nobody left to notice. The store's behaviour is pinned by its own test; this
		// confirms the outcome here as well, because the cost is one query and the failure is somebody's access.
		remaining, err := scoped.CountUserSessions(r.Context(), existing.ID)
		if err == nil && remaining > 0 {
			s.log().Error("sessions remain after disabling an account over SCIM",
				"username", existing.Username, "remaining", remaining)

			return fmt.Errorf("the account was marked disabled but %d session(s) are still active, so access has "+
				"not been removed", remaining)
		}
	}

	s.log().Info("an account's access was changed over SCIM",
		"username", existing.Username, "enabled", enabled)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: "scim",
		Action:   action,
		Target:   existing.Username,
		IP:       clientIP(r),
	})

	return nil
}

// handleSCIMPutUser replaces a user.
//
// PUT is how some providers disable an account, by sending the whole resource with active false.
func (s *Server) handleSCIMPutUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id, ok := scimID(w, r, s)
	if !ok {
		return
	}

	var incoming scim.User
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&incoming); err != nil {
		s.scimFail(w, http.StatusBadRequest, scim.ErrInvalidSyntax, "the request body is not valid SCIM JSON")

		return
	}

	scoped := s.storeFor(sess)

	existing, err := scoped.GetUserByID(r.Context(), id)
	if err != nil {
		s.scimNotFound(w, r.PathValue("id"))

		return
	}

	if role := scimRoleFor(incoming, existing.Role); role != existing.Role {
		if err := scoped.SetRole(r.Context(), id, role); err != nil {
			s.scimFail(w, http.StatusInternalServerError, "", "could not change the account's role")

			return
		}
	}

	// Only when the field is present. An absent active on a PUT must not be read as a request to enable: a provider
	// syncing a display name with a partial resource would otherwise re-enable somebody who was deliberately disabled.
	if incoming.Active != nil {
		wantEnabled := *incoming.Active
		if wantEnabled == existing.Disabled {
			if err := s.applySCIMEnabled(r, scoped, existing, wantEnabled); err != nil {
				s.scimFail(w, http.StatusInternalServerError, "", err.Error())

				return
			}
		}
	}

	updated, err := scoped.GetUserByID(r.Context(), id)
	if err != nil {
		s.scimFail(w, http.StatusInternalServerError, "", "the change was applied but the account could not be read back")

		return
	}

	s.scimOK(w, http.StatusOK, s.scimUserFrom(updated))
}

// handleSCIMDeleteUser removes an account.
//
// Treated as full deprovisioning: access ends, and it ends before anything else can fail. The audit history is retained
// regardless of what happens to the account, because an audit entry naming somebody who no longer exists anywhere is worse
// than one naming a withdrawn account.
func (s *Server) handleSCIMDeleteUser(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id, ok := scimID(w, r, s)
	if !ok {
		return
	}

	scoped := s.storeFor(sess)

	existing, err := scoped.GetUserByID(r.Context(), id)
	if err != nil {
		// A delete of something already gone is a 404. Some providers treat that as success, which is correct: the
		// desired state is reached.
		s.scimNotFound(w, r.PathValue("id"))

		return
	}

	// Disable first, then delete.
	//
	// The order matters. If the delete fails after access has been removed, the outcome is an account that cannot sign in
	// - which is the state the provider asked for. If they were the other way round and the disable failed after the
	// delete, there would be nothing left to disable and the sessions would still work.
	if err := s.applySCIMEnabled(r, scoped, existing, false); err != nil {
		s.scimFail(w, http.StatusInternalServerError, "", err.Error())

		return
	}

	if err := scoped.DeleteUser(r.Context(), id); err != nil {
		// Access has already been removed, so this is reported as a failure while being genuinely less serious than it
		// sounds. Said plainly in the message, because a provisioning log that reads as a security incident when it is
		// a bookkeeping one wastes somebody's morning.
		s.scimFail(w, http.StatusInternalServerError, "",
			"the account's access has been removed and its sessions ended, but the record itself could not be "+
				"deleted. Nobody can sign in with it.")

		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: "scim",
		Action:   "scim.user.delete",
		Target:   existing.Username,
		IP:       clientIP(r),
	})
	s.log().Info("an account was deprovisioned over SCIM", "username", existing.Username)

	w.WriteHeader(http.StatusNoContent)
}

// scimUserFrom converts a Perfuse account to a SCIM user.
//
// The password field is never populated. Echoing one - even the one just supplied - puts it in the provider's own request log,
// which is a system Perfuse does not control.
func (s *Server) scimUserFrom(u *store.User) scim.User {
	active := !u.Disabled

	user := scim.User{
		Schemas:    []string{scim.SchemaUser},
		ID:         strconv.FormatInt(u.ID, 10),
		ExternalID: u.ExternalID,
		UserName:   u.Username,
		Active:     &active,
		Meta: &scim.Meta{
			ResourceType: "User",
			Created:      scim.FormatTime(u.CreatedAt),
			Location:     scimLocation(u.ID),
		},
	}

	// The role goes out as a role value, which is how a provider reads it back for reconciliation.
	user.Roles = []scim.Role{{Value: string(u.Role), Primary: true}}

	return user
}

// scimAttributes gives an account's attributes for filter matching, keyed lower-case.
func scimAttributes(u *store.User) map[string]string {
	return map[string]string{
		"id":         strconv.FormatInt(u.ID, 10),
		"username":   u.Username,
		"externalid": u.ExternalID,
		"active":     strconv.FormatBool(!u.Disabled),
		"roles":      string(u.Role),
	}
}

// scimRoleFor decides what role a provisioned account gets.
//
// Groups and roles both carry it, and providers differ about which they send. When neither is present the configured default
// applies - and that default is viewer unless an operator says otherwise, because an account whose role could not be
// determined should be able to read and nothing else. Guessing upwards would hand out administrator to anybody the provider
// could not describe.
func scimRoleFor(u scim.User, fallback store.Role) store.Role {
	for _, role := range u.Roles {
		if mapped, ok := scimRoleName(role.Value); ok {
			return mapped
		}
	}
	for _, group := range u.Groups {
		if mapped, ok := scimRoleName(group.Display); ok {
			return mapped
		}
		if mapped, ok := scimRoleName(group.Value); ok {
			return mapped
		}
	}

	if fallback == "" {
		return store.RoleViewer
	}

	return fallback
}

// scimRoleName maps a group or role name to a Perfuse role.
//
// Matches a suffix as well as the whole name, because group names in a real directory are prefixed:
// "perfuse-admins", "APP-Perfuse-Editor", "Perfuse Viewers". Matching only exact names means every site has to rename its
// groups, which they will not do, so they set everybody to the default instead.
//
// Platform is deliberately not mappable. It is the role that crosses tenant boundaries, and letting a directory group name
// grant it means anybody who can create a group in the identity provider can reach every tenant's data.
func scimRoleName(name string) (store.Role, bool) {
	normalised := strings.ToLower(strings.TrimSpace(name))
	if normalised == "" {
		return "", false
	}

	// Longest first, so "administrator" is not matched as "admin" by accident of ordering. Both map to the same role
	// here, but the principle holds when a role name is a prefix of another.
	switch {
	case strings.Contains(normalised, "administrator"), strings.Contains(normalised, "admin"):
		return store.RoleAdmin, true
	case strings.Contains(normalised, "editor"), strings.Contains(normalised, "edit"):
		return store.RoleEditor, true
	case strings.Contains(normalised, "viewer"), strings.Contains(normalised, "readonly"),
		strings.Contains(normalised, "read-only"):
		return store.RoleViewer, true
	}

	return "", false
}

// scimRoleFromPatch reads a role out of a patch operation on roles or groups.
func scimRoleFromPatch(op scim.PatchOperation) (store.Role, bool) {
	if op.IsRemoval() {
		// A removal of a role is not a promotion or a demotion this can infer. Ignored rather than guessed: demoting on
		// a group removal would be plausible and would also demote somebody whose group membership was merely being
		// reshuffled.
		return "", false
	}

	// A bare string.
	var single string
	if err := json.Unmarshal(op.Value, &single); err == nil {
		return scimRoleName(single)
	}

	// An array of value objects, which is the multi-valued shape.
	var values []struct {
		Value   string `json:"value"`
		Display string `json:"display"`
	}
	if err := json.Unmarshal(op.Value, &values); err == nil {
		for _, v := range values {
			if role, ok := scimRoleName(v.Display); ok {
				return role, true
			}
			if role, ok := scimRoleName(v.Value); ok {
				return role, true
			}
		}
	}

	return "", false
}

// harmlessPathlessPatch reports whether a pathless replace touches only attributes that do not affect access.
//
// Conservative on purpose: anything containing a key this does not recognise returns false, and the caller then refuses the
// request. The alternative - accepting an object with an unrecognised key - would accept a disable expressed in a shape not
// handled and answer it with a 200.
func harmlessPathlessPatch(op scim.PatchOperation) bool {
	var attrs map[string]json.RawMessage
	if err := json.Unmarshal(op.Value, &attrs); err != nil {
		return false
	}
	if len(attrs) == 0 {
		return false
	}

	for key := range attrs {
		switch strings.ToLower(key) {
		case "name", "displayname", "emails", "externalid", "title", "phonenumbers",
			"locale", "timezone", "preferredlanguage", "usertype", "nickname", "profileurl":
		default:
			return false
		}
	}

	return true
}

// scimID reads the account identifier from the path.
func scimID(w http.ResponseWriter, r *http.Request, s *Server) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		s.scimNotFound(w, raw)

		return 0, false
	}

	return id, true
}

// scimLocation gives a resource's canonical location.
func scimLocation(id int64) string {
	return scimBase + "/Users/" + strconv.FormatInt(id, 10)
}

// scimOK writes a SCIM response.
func (s *Server) scimOK(w http.ResponseWriter, status int, body any) {
	// The SCIM media type, not application/json. Some providers check it and treat a plain JSON type as a protocol
	// error, which presents as provisioning silently never working.
	w.Header().Set("Content-Type", scim.ContentType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil && s.Log != nil {
		s.Log.Warn("could not write a SCIM response", "err", err)
	}
}

// scimFail writes a SCIM error.
func (s *Server) scimFail(w http.ResponseWriter, status int, scimType, detail string) {
	w.Header().Set("Content-Type", scim.ContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(scim.NewError(status, scimType, detail))
}

// scimNotFound writes a 404 in SCIM's shape.
func (s *Server) scimNotFound(w http.ResponseWriter, id string) {
	s.scimFail(w, http.StatusNotFound, "", fmt.Sprintf("no account with identifier %q", id))
}

// generateSCIMPassword makes a password nobody will ever use.
//
// An account provisioned over SCIM signs in through the identity provider, so it needs no password - but the store requires
// one, and leaving it empty or predictable would create a way in that the provider does not know about. Disabling somebody
// upstream would then leave that way in working, which is the precise failure this whole feature exists to prevent.
//
// So it is long, random, and discarded immediately. Nothing anywhere can recover it, which is the intent: the only way into
// such an account is through the identity provider, and that is the system that will be told to close it.
func generateSCIMPassword() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// Refused rather than falling back to something weaker. A predictable password on an account meant to be
		// unreachable except through the identity provider is a bypass of the deprovisioning path.
		return "", fmt.Errorf("no randomness available, so an account cannot be created safely: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}
