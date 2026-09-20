package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// API tokens are how a machine authenticates: a fleet poll, a monitoring script, another Perfuse instance, and - since the
// FHIR server gained authentication - a FHIR client.
//
// They could only be created from the command line, which meant reaching a shell on the server to let a monitoring system
// read a dashboard. That is worse than inconvenient: it pushes people towards sharing a person's password with a script,
// which is the thing tokens exist to avoid.

// apiTokenResponse describes one token. Never its value.
type apiTokenResponse struct {
	Label     string `json:"label"`
	Role      string `json:"role"`
	CreatedBy string `json:"createdBy,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	LastUsed  string `json:"lastUsed,omitempty"`
	Revoked   bool   `json:"revoked"`
	RevokedAt string `json:"revokedAt,omitempty"`
}

// handleListAPITokens lists tokens without their values.
//
// Only a hash is stored, so a value cannot be shown again even deliberately. Revoked tokens are listed rather than hidden,
// because the audit log names them and an entry pointing at something invisible is worse than a row marked withdrawn.
func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	tokens, err := s.storeFor(sess).ListAPITokens(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	out := make([]apiTokenResponse, 0, len(tokens))
	for _, t := range tokens {
		entry := apiTokenResponse{
			Label:     t.Label,
			Role:      string(t.Role),
			CreatedBy: t.CreatedBy,
			Revoked:   t.Revoked(),
		}
		if !t.CreatedAt.IsZero() {
			entry.CreatedAt = t.CreatedAt.UTC().Format(time.RFC3339)
		}
		if t.LastUsed != nil && !t.LastUsed.IsZero() {
			entry.LastUsed = t.LastUsed.UTC().Format(time.RFC3339)
		}
		if t.RevokedAt != nil && !t.RevokedAt.IsZero() {
			entry.RevokedAt = t.RevokedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}

	s.ok(w, map[string]any{"tokens": out})
}

// createTokenRequest asks for a new token.
type createTokenRequest struct {
	Label string `json:"label"`
	Role  string `json:"role"`
}

// createTokenResponse carries the one and only sight of the value.
type createTokenResponse struct {
	Label string `json:"label"`
	Role  string `json:"role"`

	// Token is the value, returned exactly once because only its hash is kept.
	Token string `json:"token"`

	// Note tells the interface to say so, rather than leaving the interface to remember.
	Note string `json:"note"`
}

// handleCreateAPIToken issues a token.
func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req createTokenRequest
	if !s.decode(w, r, &req) {
		return
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		// Required, because the label is the only handle anybody has on the token afterwards. An unlabelled token is
		// one nobody can decide whether to revoke.
		s.fail(w, r, http.StatusBadRequest,
			"a label is required; it is the only way to tell afterwards what this token is for")
		return
	}

	role := store.Role(strings.TrimSpace(req.Role))
	if role == "" {
		role = store.RoleViewer
	}
	if !role.Valid() {
		s.fail(w, r, http.StatusBadRequest, "role must be viewer, editor, admin or platform")
		return
	}

	// Refused rather than allowed. An admin issuing a platform token would be minting a credential more powerful than
	// the one they hold, which is escalation by another name.
	if role == store.RolePlatform && sess.Role != store.RolePlatform {
		s.fail(w, r, http.StatusForbidden,
			"only a platform account can issue a platform token; issuing one more powerful than your own "+
				"account would be an escalation")
		return
	}

	token, err := s.storeFor(sess).CreateAPIToken(r.Context(), label, role, sess.Username)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	s.log().Info("an API token was created",
		"label", label, "role", string(role), "by", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "token.create",
		Target:   label,
		Detail:   "role " + string(role),
		IP:       clientIP(r),
	})

	s.ok(w, createTokenResponse{
		Label: label,
		Role:  string(role),
		Token: token,
		Note: "Copy this now. Only its hash is stored, so it cannot be shown again - " +
			"a replacement means issuing a new token and revoking this one.",
	})
}

// handleRevokeAPIToken withdraws a token.
//
// Revoked rather than deleted, so the audit log's reference to it still resolves to something.
func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	label := r.PathValue("label")
	if strings.TrimSpace(label) == "" {
		s.fail(w, r, http.StatusBadRequest, "which token?")
		return
	}

	if err := s.storeFor(sess).RevokeAPIToken(r.Context(), label); err != nil {
		s.failErr(w, r, err)
		return
	}

	s.log().Info("an API token was revoked", "label", label, "by", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "token.revoke",
		Target:   label,
		IP:       clientIP(r),
	})

	s.ok(w, map[string]string{"status": "revoked"})
}
