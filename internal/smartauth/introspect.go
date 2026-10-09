package smartauth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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
	return s.marked(context.Background(), kindRevoked, jti)
}

func (s *Server) handleIntrospect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "the request is not a form")
		return
	}
	// Only a caller that proves itself may ask, since introspection says whose records a token reaches: a client that can
	// authenticate, or one holding an active access token from this server (SMART 2 allows either). A public client's id
	// alone is not proof.
	var err error
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if _, active := s.verifyOwn(r.Context(), strings.TrimSpace(bearer)); !active {
			err = errClient("the bearer token is not an active access token from this server")
		}
	} else {
		var client *Client
		client, err = s.authenticatedClient(r)
		if err == nil && client.Kind == KindPublic {
			err = errClient("a public client cannot authenticate, so it cannot introspect tokens; present an access token instead")
		}
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
	if g := s.grant(r.Context(), kindRefresh, token, false); g != nil && g.client == client {
		_ = s.grants().Delete(r.Context(), kindRefresh, hashKey(token))
	}
	if c, ok := s.verifyOwn(r.Context(), token); ok {
		var owner struct {
			ClientID string `json:"client_id"`
		}
		if raw, err := tokenBody(token); err == nil {
			_ = json.Unmarshal(raw, &owner)
		}
		if owner.ClientID == client.ID {
			if err := s.mark(r.Context(), kindRevoked, c.JTI, c.ExpiresAt); err != nil {
				// Saying it worked when it did not would leave a token the caller believes is dead working for an hour.
				tokenError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "the revocation could not be recorded")
				return
			}
		}
	}
	// RFC 7009: success whether or not the token was valid, so revocation cannot be used to test tokens.
	w.WriteHeader(http.StatusOK)
}
