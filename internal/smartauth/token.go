package smartauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

const jwtBearer = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// backendTokenLife is how long a client_credentials token lasts. SMART Backend Services recommends five minutes; a client
// asks again rather than holding a token that outlives a revoked registration.
const backendTokenLife = 5 * time.Minute

// Server is the authorization server.
type Server struct {
	// Issuer is this server's identifier, the iss of every token: the public URL of its base, such as https://host/auth.
	Issuer string
	// Audience is the FHIR base the tokens are for.
	Audience string
	Key      *Key
	Clients  Clients
	Now      func() time.Time

	// Users sign in to authorize apps; without any, only backend clients get tokens.
	Users Users
	// Patients lists patients a clinician may pick, matching a search text; PatientExists checks a pick.
	Patients      func(ctx context.Context, search string) ([]PatientChoice, error)
	PatientExists func(ctx context.Context, id string) bool

	mu       sync.Mutex
	used     map[string]time.Time // client assertion jti -> its expiry, to refuse a replay
	pending  map[string]*pending
	codes    map[string]*grantRecord
	refresh  map[string]*grantRecord
	launches map[string]launchContext
}

// AuthorizeURL is where apps send people to sign in, empty when nobody can.
func (s *Server) AuthorizeURL() string {
	if len(s.Users) == 0 {
		return ""
	}
	return strings.TrimRight(s.Issuer, "/") + "/authorize"
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// TokenURL is where clients ask for tokens; it is also the audience of their assertions.
func (s *Server) TokenURL() string { return strings.TrimRight(s.Issuer, "/") + "/token" }

// Handler serves /token and /jwks under the issuer's path.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", s.handleToken)
	if len(s.Users) > 0 {
		mux.HandleFunc("GET /authorize", s.handleAuthorize)
		mux.HandleFunc("POST /authorize", s.handleAuthorize)
		mux.HandleFunc("POST /signin", s.handleSignIn)
		mux.HandleFunc("POST /patient", s.handlePatient)
		mux.HandleFunc("POST /consent", s.handleConsent)
	}
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleOpenIDConfiguration)
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.Key.JWKS())
	})
	return mux
}

// tokenError answers as RFC 6749 section 5.2 says.
func tokenError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer error="`+code+`"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "the request is not a form: "+err.Error())
		return
	}
	switch gt := r.PostForm.Get("grant_type"); gt {
	case "client_credentials":
		s.clientCredentials(w, r)
	case "authorization_code":
		s.authorizationCode(w, r)
	case "refresh_token":
		s.refreshToken(w, r)
	case "":
		tokenError(w, http.StatusBadRequest, "invalid_request", "grant_type is missing")
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type", fmt.Sprintf("grant_type %q is not supported", gt))
	}
}

// clientCredentials is SMART Backend Services: the client proves itself with a JWT signed by a key it registered.
func (s *Server) clientCredentials(w http.ResponseWriter, r *http.Request) {
	client, err := s.assertedClient(r.Context(), r.PostForm)
	if err != nil {
		tokenError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	if client.Kind != KindBackend {
		tokenError(w, http.StatusBadRequest, "unauthorized_client", "client_credentials is for backend clients, and "+client.ID+" is "+client.Kind)
		return
	}
	requested := r.PostForm.Get("scope")
	if requested == "" {
		tokenError(w, http.StatusBadRequest, "invalid_scope", "scope is required")
		return
	}
	var granted []string
	for _, sc := range client.grant(requested) {
		// A backend token acts for no person and no patient, so only system scopes mean anything.
		if strings.HasPrefix(sc, "system/") {
			granted = append(granted, sc)
		}
	}
	if len(granted) == 0 {
		tokenError(w, http.StatusBadRequest, "invalid_scope", "none of the requested scopes is registered for "+client.ID)
		return
	}
	scope := strings.Join(granted, " ")
	tok, err := s.accessToken(client.ID, client.ID, scope, nil, backendTokenLife)
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error", "the token could not be signed")
		return
	}
	writeToken(w, map[string]any{"access_token": tok, "token_type": "bearer",
		"expires_in": int(backendTokenLife.Seconds()), "scope": scope})
}

func writeToken(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(body)
}

// accessToken signs a token for the FHIR endpoint. extra carries the launch context (patient, encounter, fhirUser).
func (s *Server) accessToken(sub, clientID, scope string, extra map[string]any, life time.Duration) (string, error) {
	now := s.now()
	claims := map[string]any{"iss": s.Issuer, "sub": sub, "aud": s.Audience, "client_id": clientID, "scope": scope,
		"iat": now.Unix(), "exp": now.Add(life).Unix(), "jti": randomID()}
	for k, v := range extra {
		claims[k] = v
	}
	return s.Key.Sign(claims)
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// assertedClient checks a client assertion (RFC 7523, as SMART profiles it): issued and subject both the client, for this token
// URL, short-lived, never seen before, and signed by one of the client's keys.
func (s *Server) assertedClient(ctx context.Context, form map[string][]string) (*Client, error) {
	get := func(k string) string {
		if v := form[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if get("client_assertion_type") != jwtBearer || get("client_assertion") == "" {
		return nil, fmt.Errorf("authenticate with client_assertion_type %s and a signed client_assertion", jwtBearer)
	}
	assertion := get("client_assertion")
	iss, err := unverifiedIssuer(assertion)
	if err != nil {
		return nil, err
	}
	client := s.Clients[iss]
	if client == nil || client.keys == nil {
		return nil, fmt.Errorf("no client %q with registered keys", iss)
	}
	if id := get("client_id"); id != "" && id != iss {
		return nil, fmt.Errorf("client_id %q is not the assertion's issuer %q", id, iss)
	}
	claims, err := oidc.Verify(ctx, client.keys, assertion, oidc.VerifyOptions{Issuer: iss, ClientID: s.TokenURL(),
		Algorithms: []string{"RS384", "ES384", "RS256", "ES256"}, System: true, Now: s.now})
	if err != nil {
		return nil, fmt.Errorf("the client assertion was not accepted: %w", err)
	}
	if claims.Subject != iss {
		return nil, fmt.Errorf("the client assertion's sub must be the client id")
	}
	if claims.ExpiresAt.After(s.now().Add(5 * time.Minute)) {
		return nil, fmt.Errorf("the client assertion expires more than five minutes from now")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used == nil {
		s.used = map[string]time.Time{}
	}
	for j, exp := range s.used {
		if s.now().After(exp.Add(time.Minute)) {
			delete(s.used, j)
		}
	}
	key := iss + " " + claims.JTI
	if _, seen := s.used[key]; seen {
		return nil, fmt.Errorf("the client assertion's jti was already used")
	}
	s.used[key] = claims.ExpiresAt
	return client, nil
}

// unverifiedIssuer reads iss without checking the signature, only to know whose keys to check it with.
func unverifiedIssuer(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("the client assertion is not a JWT")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("the client assertion is not a JWT")
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(body, &c); err != nil || c.Iss == "" {
		return "", fmt.Errorf("the client assertion names no issuer")
	}
	return c.Iss, nil
}

// handleOpenIDConfiguration is the OpenID Connect discovery document, which an app reads to check an ID token.
func (s *Server) handleOpenIDConfiguration(w http.ResponseWriter, r *http.Request) {
	doc := map[string]any{
		"issuer": s.Issuer, "jwks_uri": strings.TrimRight(s.Issuer, "/") + "/jwks", "token_endpoint": s.TokenURL(),
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post", "private_key_jwt"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "fhirUser", "profile", "launch", "launch/patient", "offline_access", "online_access"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "client_credentials"},
	}
	if a := s.AuthorizeURL(); a != "" {
		doc["authorization_endpoint"] = a
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
