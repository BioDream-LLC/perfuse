package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/store"
)

// OIDCConfig is how federated sign-in is configured.
type OIDCConfig struct {
	// Issuer is the identity provider's issuer URL.
	Issuer string

	// ClientID and ClientSecret identify Perfuse to the provider.
	ClientID     string
	ClientSecret string

	// RedirectURL is where the provider sends the browser back. Must match what is registered with the provider exactly.
	RedirectURL string

	// Scopes are requested at sign-in. Empty asks for openid, profile and email.
	Scopes []string

	// Roles maps the provider's groups to Perfuse roles.
	Roles *oidc.RoleMapping

	// CreateUsers allows a first-time sign-in to create an account. Defaults to false.
	//
	// False is the safe default and the useful one to explain: with it off, somebody has to exist in Perfuse before they
	// can sign in, so the directory decides who they are and Perfuse decides who is allowed in. With it on, anybody the
	// directory vouches for and whose groups map to a role gets an account - which is what most sites want, and is a
	// decision they should make deliberately.
	CreateUsers bool

	// Label is what the sign-in button says. Defaults to naming the issuer's host.
	Label string

	// Provider is the discovered provider, filled in at startup.
	Provider *oidc.Provider

	// Keys are the provider's signing keys.
	Keys *oidc.KeySet
}

// Enabled reports whether federated sign-in is configured.
func (c *OIDCConfig) Enabled() bool {
	return c != nil && c.Provider != nil && strings.TrimSpace(c.ClientID) != ""
}

// ButtonLabel is what the sign-in button should say.
//
// The issuer's host when nothing is configured, because "sign in with 127.0.0.1" is unhelpful but "sign in with SSO" is worse -
// a site with two providers would show the same word twice.
func (c *OIDCConfig) ButtonLabel() string {
	if c == nil {
		return "single sign-on"
	}
	if l := strings.TrimSpace(c.Label); l != "" {
		return l
	}
	if c.Provider != nil {
		if u, err := url.Parse(c.Provider.Issuer); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return "single sign-on"
}

// pendingAuth is one sign-in attempt waiting for its callback.
type pendingAuth struct {
	request *oidc.AuthRequest
	started time.Time
	// returnTo is where to send the browser afterwards.
	returnTo string
}

// authAttempts holds sign-in attempts between the redirect out and the callback back.
//
// In memory rather than the database, deliberately. An attempt is worthless after a few minutes, it is meaningless on another
// server, and writing it down would mean a table that has to be pruned to hold values whose entire purpose is to be forgotten.
// The consequence is that restarting mid-sign-in makes somebody click the button again, which is the correct trade.
type authAttempts struct {
	mu sync.Mutex
	m  map[string]pendingAuth
}

// authAttemptLifetime is how long a sign-in attempt stays valid.
//
// Five minutes. Long enough for somebody to find their password manager and complete two-factor, short enough that a state
// value captured from a browser's history is useless by the time anybody looks.
const authAttemptLifetime = 5 * time.Minute

// maxPendingAuths bounds how many attempts are held.
//
// A bound rather than none, because starting a sign-in is unauthenticated by necessity - anybody can ask for the redirect - and
// an unbounded map behind an unauthenticated endpoint is a way to exhaust memory.
const maxPendingAuths = 1000

func newAuthAttempts() *authAttempts {
	return &authAttempts{m: map[string]pendingAuth{}}
}

// ensureAuthAttempts prepares the attempt store on first use.
//
// Lazily rather than in a constructor, because the server is built as a struct literal in several places and a nil map here
// would be a panic on the first sign-in rather than at startup.
func (s *Server) ensureAuthAttempts() {
	s.authAttemptsOnce.Do(func() {
		if s.authAttempts == nil {
			s.authAttempts = newAuthAttempts()
		}
	})
}

func (a *authAttempts) put(state string, p pendingAuth) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Expired entries are cleared on write rather than by a timer. The only thing that grows this map is somebody starting
	// a sign-in, so that is the only moment it needs tidying.
	for k, v := range a.m {
		if time.Since(v.started) > authAttemptLifetime {
			delete(a.m, k)
		}
	}

	if len(a.m) >= maxPendingAuths {
		return false
	}

	a.m[state] = p
	return true
}

// take removes and returns an attempt. Single use: an authorization code may only be redeemed once, and allowing the state to
// be reused would permit a captured callback URL to be replayed.
func (a *authAttempts) take(state string) (pendingAuth, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	p, ok := a.m[state]
	if !ok {
		return pendingAuth{}, false
	}
	delete(a.m, state)

	if time.Since(p.started) > authAttemptLifetime {
		return pendingAuth{}, false
	}

	return p, true
}

// handleOIDCStart begins a federated sign-in by redirecting to the provider.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	s.ensureAuthAttempts()

	cfg := s.OIDC
	if !cfg.Enabled() {
		s.fail(w, r, http.StatusNotFound, "federated sign-in is not configured on this server")
		return
	}

	req, err := oidc.NewAuthRequest(cfg.RedirectURL)
	if err != nil {
		s.log().Error("could not start a federated sign-in", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "could not start sign-in")
		return
	}

	// Only a path, and only one beginning with a single slash. A full URL here would be an open redirect: an attacker sends
	// somebody a Perfuse sign-in link that lands them on a copy of the interface, and the sign-in itself looks entirely
	// normal. Two slashes are excluded because "//host" is a protocol-relative URL to somewhere else.
	// Shared with SAML rather than repeated, and the shared version is the stricter of the two: it also refuses a leading "/\",
	// which some browsers read as protocol-relative, and anything carrying a carriage return or newline.
	returnTo := safeReturnTo(r.URL.Query().Get("returnTo"))

	if !s.authAttempts.put(req.State, pendingAuth{request: req, started: time.Now(), returnTo: returnTo}) {
		// Said plainly rather than as a generic error. Hitting this means either an attack or a misconfigured provider that
		// never completes the flow, and both need somebody to look.
		s.log().Warn("too many sign-in attempts are waiting for a callback", "held", maxPendingAuths)
		s.fail(w, r, http.StatusServiceUnavailable, "too many sign-in attempts are in progress; try again shortly")
		return
	}

	http.Redirect(w, r, req.AuthURL(cfg.Provider, cfg.ClientID, cfg.Scopes), http.StatusFound)
}

// handleOIDCCallback completes a federated sign-in.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	s.ensureAuthAttempts()

	cfg := s.OIDC
	if !cfg.Enabled() {
		s.fail(w, r, http.StatusNotFound, "federated sign-in is not configured on this server")
		return
	}

	query := r.URL.Query()

	// The provider's own error first. A person who declined consent should be told that, not shown a failure that looks
	// like the server is broken.
	if errParam := query.Get("error"); errParam != "" {
		detail := errParam
		if d := query.Get("error_description"); d != "" {
			detail += ": " + d
		}
		s.log().Warn("the identity provider refused a sign-in", "detail", detail, "ip", clientIP(r))
		s.signInFailed(w, r, "the identity provider refused the sign-in")
		return
	}

	state := query.Get("state")
	if state == "" {
		s.signInFailed(w, r, "this sign-in callback has no state value")
		return
	}

	attempt, ok := s.authAttempts.take(state)
	if !ok {
		// The same message for an unknown state and an expired one, because from here they are indistinguishable and the
		// difference would tell an attacker whether they had guessed a real one.
		s.log().Warn("a sign-in callback did not match any attempt", "ip", clientIP(r))
		s.signInFailed(w, r, "this sign-in attempt has expired or was not started here; please try again")
		return
	}

	code := query.Get("code")
	if code == "" {
		s.signInFailed(w, r, "the identity provider returned no authorization code")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	tokens, err := oidc.Exchange(ctx, oidc.DefaultHTTPClient, cfg.Provider,
		cfg.ClientID, cfg.ClientSecret, code, attempt.request)
	if err != nil {
		s.log().Error("a federated sign-in could not be completed", "err", err, "ip", clientIP(r))
		s.signInFailed(w, r, "the sign-in could not be completed")
		return
	}

	claims, err := oidc.Verify(ctx, cfg.Keys, tokens.IDToken, oidc.VerifyOptions{
		Issuer:   cfg.Provider.Issuer,
		ClientID: cfg.ClientID,
		Nonce:    attempt.request.Nonce,
	})
	if err != nil {
		// Logged in full and shown in summary. A verification failure is either a misconfiguration or an attack, and the
		// details belong in the log where an operator can see them rather than on a page where a stranger can.
		s.log().Error("an identity token failed verification", "err", err, "ip", clientIP(r))
		s.signInFailed(w, r, "the identity provider's response could not be verified")
		return
	}

	role := cfg.Roles.RoleFor(claims.Groups)
	if role == oidc.RoleNone {
		// Named in the log with the groups, because "no role" is almost always a mapping that does not match what the
		// directory actually sends - and the only way to fix it is to know what it sent.
		s.log().Warn("a federated sign-in matched no role",
			"subject", claims.Subject, "groups", claims.Groups, "mapping", cfg.Roles.Describe())
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: claims.Email, Action: "login.refused.norole", IP: clientIP(r),
		})
		s.signInFailed(w, r, "your account is not a member of any group that grants access here")
		return
	}

	identity := store.ExternalIdentity{
		Issuer:      claims.Issuer,
		Subject:     claims.Subject,
		Email:       claims.Email,
		DisplayName: claims.Name,
		Username:    federatedUsername(claims),
		Role:        store.Role(role),
	}

	token, user, created, err := s.Store.SignInExternal(r.Context(), identity, cfg.CreateUsers,
		clientIP(r), r.UserAgent())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNoExternalMatch):
			s.log().Warn("a federated sign-in had no account and account creation is off",
				"subject", claims.Subject, "email", claims.Email)
			s.signInFailed(w, r, "you do not have an account here yet, and this server does not create them "+
				"automatically; ask an administrator to add you")
		case errors.Is(err, store.ErrLocalAccountExists):
			s.log().Error("a federated sign-in collided with a local account", "err", err)
			s.signInFailed(w, r, "an account with that name already exists here and is not linked to your directory "+
				"identity; an administrator will need to resolve it")
		case errors.Is(err, store.ErrDisabled):
			s.signInFailed(w, r, "this account is disabled")
		default:
			s.log().Error("a federated sign-in failed", "err", err)
			s.signInFailed(w, r, "the sign-in could not be completed")
		}
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(s.Store.SessionLifetime()),
	})

	s.log().Info("federated login",
		"user", user.Username, "role", string(user.Role), "issuer", claims.Issuer,
		"created", created, "ip", clientIP(r))

	action := "login.federated"
	if created {
		// A distinct action, because an account appearing is the event somebody reviewing the audit log cares about.
		action = "login.federated.created"
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: user.Username, Action: action, IP: clientIP(r),
	})

	http.Redirect(w, r, attempt.returnTo, http.StatusFound)
}

// signInFailed sends the browser back to the sign-in page with a reason.
//
// A redirect rather than a JSON error, because this endpoint is reached by a browser following the provider's redirect and
// nothing is there to read JSON. The reason is a query parameter the sign-in page displays.
func (s *Server) signInFailed(w http.ResponseWriter, r *http.Request, reason string) {
	target := "/?signInError=" + url.QueryEscape(reason)
	http.Redirect(w, r, target, http.StatusFound)
}

// federatedUsername picks the name to give a new federated account.
//
// Preferred username, then email, then subject. Display only - identity is the subject - but it is what appears in the audit
// log, and a log naming a UUID is a log nobody can read.
func federatedUsername(claims *oidc.Claims) string {
	if u := strings.TrimSpace(claims.PreferredUsername); u != "" {
		return u
	}
	if e := strings.TrimSpace(claims.Email); e != "" {
		return e
	}
	return claims.Subject
}

// authMethodsResponse tells the sign-in page what is available.
//
// Unauthenticated by necessity: it is read before anybody has signed in. It carries no secret - the client id is public by
// design in this flow, and the label and issuer host are on the button anyway - but the client secret is deliberately absent,
// because an endpoint that grew one later is exactly the sort of accident this comment exists to prevent.
type authMethodsResponse struct {
	// Password says the local sign-in form should be shown.
	Password bool `json:"password"`

	// OIDC describes the federated option, absent when none is configured.
	OIDC *authMethodOIDC `json:"oidc,omitempty"`

	// SAML describes the SAML option, absent when none is configured.
	//
	// The same shape as OIDC because the button is the same button: a label and somewhere to send the browser. Both are omitted
	// rather than sent false, so a page cannot render a button that answers 404 - which is worse than no button, because somebody
	// clicks it, nothing happens, and they conclude the product is broken.
	SAML *authMethodOIDC `json:"saml,omitempty"`

	// Passkeys says a passkey sign-in is available.
	//
	// A boolean rather than the relying party identifier. The identifier is not a secret - it is in every credential and
	// on the page's own domain - but sending it here would put a configuration value on an unauthenticated endpoint for no
	// reason, and the browser gets it from the sign-in options anyway.
	Passkeys bool `json:"passkeys"`

	// Directory names the LDAP directory, absent when none is configured.
	//
	// No separate button: a directory sign-in uses the same username and password form. What this changes is what the
	// form says, so somebody knows to type their hospital credentials rather than looking for an account nobody
	// created for them.
	Directory *authMethodDirectory `json:"directory,omitempty"`
}

type authMethodDirectory struct {
	Label string `json:"label"`
}

type authMethodOIDC struct {
	Label   string `json:"label"`
	StartAt string `json:"startAt"`
}

// handleAuthMethods reports how somebody may sign in.
func (s *Server) handleAuthMethods(w http.ResponseWriter, r *http.Request) {
	out := authMethodsResponse{
		// Always true. Local accounts are the break-glass path for an on-premises engine: when the identity provider is
		// unreachable somebody still has to get in, and hiding the form would mean the only way in is the one that is down.
		Password: true,

		// Offered only when configured, since the endpoints are not registered otherwise. A button that answers 404 is
		// worse than no button: somebody clicks it, nothing happens, and they conclude the product is broken.
		Passkeys: s.PasskeyRPID != "",
	}

	if s.OIDC.Enabled() {
		out.OIDC = &authMethodOIDC{Label: s.OIDC.ButtonLabel(), StartAt: "/auth/oidc/start"}
	}

	if s.ldapEnabled() {
		out.Directory = &authMethodDirectory{Label: s.ldapButtonLabel()}
	}

	if s.SAML.Enabled() {
		out.SAML = &authMethodOIDC{Label: s.SAML.ButtonLabel(), StartAt: "/auth/saml/start"}
	}

	s.ok(w, out)
}
