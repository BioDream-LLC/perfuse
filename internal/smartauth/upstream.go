package smartauth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

// Upstream is an OpenID Connect provider people may sign in with instead of a password: the organisation's own identity
// provider (Entra ID, Okta, Keycloak). The person must still be listed in the users file, linked by oidc_subject, because the
// users file is what says which FHIR resource they are; a provider account with no link is refused, never created.
type Upstream struct {
	// Label is the button's text: "Sign in with <Label>".
	Label        string
	ClientID     string
	ClientSecret string
	Scopes       []string
	Provider     *oidc.Provider
	Keys         *oidc.KeySet
	// HTTP is the client for the token exchange; nil for the default.
	HTTP *http.Client
}

// UpstreamFile is the -smart-oidc file.
type UpstreamFile struct {
	Issuer           string   `yaml:"issuer"`
	ClientID         string   `yaml:"client_id"`
	ClientSecretFile string   `yaml:"client_secret_file"`
	ClientSecretEnv  string   `yaml:"client_secret_env"`
	Label            string   `yaml:"label"`
	Scopes           []string `yaml:"scopes"`
}

// LoadUpstream reads the -smart-oidc file and discovers the provider. The secret comes from a file or an environment
// variable, never the file itself, which is often templated or kept in a repository.
func LoadUpstream(ctx context.Context, path string) (*Upstream, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f UpstreamFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Issuer == "" || f.ClientID == "" {
		return nil, fmt.Errorf("%s: issuer and client_id are required", path)
	}
	secret := ""
	switch {
	case f.ClientSecretFile != "":
		b, err := os.ReadFile(f.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("%s: client_secret_file: %w", path, err)
		}
		secret = strings.TrimSpace(string(b))
	case f.ClientSecretEnv != "":
		if secret = os.Getenv(f.ClientSecretEnv); secret == "" {
			return nil, fmt.Errorf("%s: the environment variable %s is empty", path, f.ClientSecretEnv)
		}
	}
	p, err := oidc.Discover(ctx, oidc.DefaultHTTPClient, f.Issuer)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Label == "" {
		f.Label = "your organisation"
	}
	if len(f.Scopes) == 0 {
		f.Scopes = []string{"openid"}
	}
	return &Upstream{Label: f.Label, ClientID: f.ClientID, ClientSecret: secret, Scopes: f.Scopes, Provider: p,
		Keys: oidc.NewKeySet(p.JWKSURL, oidc.DefaultHTTPClient)}, nil
}

// UpstreamRedirect is the redirect URI to register at the provider.
func (s *Server) UpstreamRedirect() string {
	return strings.TrimRight(s.Issuer, "/") + "/oidc/callback"
}

// handleUpstreamStart sends the person to the provider, remembering which authorization they came from.
func (s *Server) handleUpstreamStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		problem(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	_, p := s.pendingFor(r)
	if p == nil {
		problem(w, http.StatusBadRequest, "This sign-in has expired. Go back to the app and start again.")
		return
	}
	ar, err := oidc.NewAuthRequest(s.UpstreamRedirect())
	if err != nil {
		problem(w, http.StatusInternalServerError, "The sign-in could not be started.")
		return
	}
	s.mu.Lock()
	p.upstream = ar
	s.mu.Unlock()
	http.Redirect(w, r, ar.AuthURL(s.Upstream.Provider, s.Upstream.ClientID, s.Upstream.Scopes), http.StatusFound)
}

// handleUpstreamCallback finishes the provider's sign-in: the code is exchanged, the ID token verified against this
// attempt's nonce, and the person found by the token's subject.
func (s *Server) handleUpstreamCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	var (
		id string
		p  *pending
	)
	s.mu.Lock()
	for k, c := range s.pending {
		if c.upstream != nil && state != "" && c.upstream.State == state && s.now().Before(c.expires) {
			id, p = k, c
		}
	}
	if p != nil {
		// One callback per attempt: a replayed callback finds nothing.
		ar := p.upstream
		p.upstream = nil
		s.mu.Unlock()
		s.finishUpstream(w, r, id, p, ar)
		return
	}
	s.mu.Unlock()
	problem(w, http.StatusBadRequest, "This sign-in is unknown or has expired. Go back to the app and start again.")
}

func (s *Server) finishUpstream(w http.ResponseWriter, r *http.Request, id string, p *pending, ar *oidc.AuthRequest) {
	again := func(msg string) {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "signin", map[string]any{"Req": id, "App": appName(p.client), "Error": msg, "Upstream": s.upstreamLabel()})
	}
	if e := r.URL.Query().Get("error"); e != "" {
		again("The sign-in at " + s.Upstream.Label + " did not complete (" + e + ").")
		return
	}
	tok, err := oidc.Exchange(r.Context(), s.Upstream.HTTP, s.Upstream.Provider, s.Upstream.ClientID, s.Upstream.ClientSecret,
		r.URL.Query().Get("code"), ar)
	if err != nil || tok.IDToken == "" {
		again("The sign-in at " + s.Upstream.Label + " could not be completed.")
		return
	}
	claims, err := oidc.Verify(r.Context(), s.Upstream.Keys, tok.IDToken, oidc.VerifyOptions{Issuer: s.Upstream.Provider.Issuer,
		ClientID: s.Upstream.ClientID, Nonce: ar.Nonce, Now: s.Now})
	if err != nil {
		again("The identity provider's answer could not be verified.")
		return
	}
	u := s.userBySubject(claims.Subject)
	if u == nil {
		again("Your " + s.Upstream.Label + " account is not linked to anyone who may authorize apps here. Ask the administrator to link it.")
		return
	}
	s.mu.Lock()
	p.user = u
	s.mu.Unlock()
	s.next(w, r, id, p)
}

func (s *Server) upstreamLabel() string {
	if s.Upstream == nil {
		return ""
	}
	return s.Upstream.Label
}
