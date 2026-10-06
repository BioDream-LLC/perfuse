package smartauth

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

// Token introspection (RFC 7662, as SMART App Launch 2 uses it) and revocation (RFC 7009).

// verifyOwn checks an access token this server issued and has not revoked.
func (s *Server) verifyOwn(ctx context.Context, token string) (*oidc.Claims, bool) {
	keys, err := oidc.NewStaticKeySet(s.Key.JWKS())
	if err != nil {
		return nil, false
	}
	c, err := oidc.Verify(ctx, keys, token, oidc.VerifyOptions{Issuer: s.Issuer, ClientID: s.Audience,
		Algorithms: []string{"RS256"}, System: true, Now: s.now})
	if err != nil || s.Revoked(c.JTI) {
		return nil, false
	}
	return c, true
}

// Revoked reports whether an access token was revoked; the FHIR endpoint asks on every request.
func (s *Server) Revoked(jti string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.revoked[jti]
	return ok
}

func (s *Server) handleIntrospect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "the request is not a form")
		return
	}
	// Only a client that can authenticate may ask: introspection says whose records a token reaches.
	client, err := s.authenticatedClient(r)
	if err == nil && client.Kind == KindPublic {
		err = errClient("a public client cannot authenticate, so it cannot introspect tokens")
	}
	if err != nil {
		tokenError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	c, ok := s.verifyOwn(r.Context(), r.PostForm.Get("token"))
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		return
	}
	body := map[string]any{"active": true, "scope": c.Scope, "sub": c.Subject, "iss": c.Issuer, "aud": s.Audience,
		"exp": c.ExpiresAt.Unix(), "iat": c.IssuedAt.Unix(), "token_type": "Bearer"}
	var extra struct {
		ClientID string `json:"client_id"`
		FHIRUser string `json:"fhirUser"`
	}
	if raw, err := tokenBody(r.PostForm.Get("token")); err == nil {
		_ = json.Unmarshal(raw, &extra)
	}
	body["client_id"] = extra.ClientID
	if extra.FHIRUser != "" {
		body["fhirUser"] = extra.FHIRUser
	}
	if c.Patient != "" {
		body["patient"] = c.Patient
	}
	if c.Encounter != "" {
		body["encounter"] = c.Encounter
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "the request is not a form")
		return
	}
	client, err := s.authenticatedClient(r)
	if err != nil {
		tokenError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	token := r.PostForm.Get("token")
	s.mu.Lock()
	if g := s.refresh[token]; g != nil && g.client == client {
		delete(s.refresh, token)
	}
	s.mu.Unlock()
	if c, ok := s.verifyOwn(r.Context(), token); ok {
		var owner struct {
			ClientID string `json:"client_id"`
		}
		if raw, err := tokenBody(token); err == nil {
			_ = json.Unmarshal(raw, &owner)
		}
		if owner.ClientID == client.ID {
			s.mu.Lock()
			if s.revoked == nil {
				s.revoked = map[string]time.Time{}
			}
			s.revoked[c.JTI] = c.ExpiresAt
			s.mu.Unlock()
		}
	}
	// RFC 7009: success whether or not the token was valid, so revocation cannot be used to test tokens.
	w.WriteHeader(http.StatusOK)
}
