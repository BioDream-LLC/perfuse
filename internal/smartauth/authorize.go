package smartauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// The SMART App Launch authorization code flow: an app sends the person here, they sign in, choose a patient when they are a
// clinician and the app asked for one, and approve the scopes; the app gets a code it exchanges, with its PKCE verifier, for
// tokens. Every step is a plain HTML form, so it works without JavaScript and with a screen reader.

const (
	pendingLife       = 10 * time.Minute
	codeLife          = time.Minute
	userTokenLife     = time.Hour
	maxSignInFailures = 5
)

// PatientChoice is one patient a clinician can pick.
type PatientChoice struct {
	ID, Name, BirthDate string
}

// pending is an authorization in progress, between the app's request and the code.
type pending struct {
	client                            *Client
	redirect, state, nonce, challenge string
	scopes                            []string
	launchPatient, launchEncounter    string
	user                              *User
	patient                           string
	failures                          int
	expires                           time.Time
}

// grantRecord is what a code or refresh token stands for.
type grantRecord struct {
	client                     *Client
	redirect, challenge, nonce string
	scopes                     []string
	patient, encounter         string
	user                       *User
	expires                    time.Time
}

func newID() string {
	return randomID() + randomID()
}

func secureHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https: http:; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// problem is a page for an error that cannot be sent back to the app, because the app or its redirect URI is not trusted.
func problem(w http.ResponseWriter, status int, message string) {
	secureHeaders(w)
	w.WriteHeader(status)
	_ = pages.ExecuteTemplate(w, "problem", message)
}

// redirectError sends an OAuth error back to a redirect URI already checked against the client's registration.
func redirectError(w http.ResponseWriter, r *http.Request, redirect, state, code, description string) {
	u, _ := url.Parse(redirect)
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", description)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			problem(w, http.StatusBadRequest, "The request could not be read.")
			return
		}
		q = r.PostForm
	}
	client := s.Clients[q.Get("client_id")]
	if client == nil || client.Kind == KindBackend {
		problem(w, http.StatusBadRequest, "This app is not registered to sign people in here.")
		return
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" && len(client.RedirectURIs) == 1 {
		redirect = client.RedirectURIs[0]
	}
	if !slices.Contains(client.RedirectURIs, redirect) {
		// Never redirect to an address the app did not register: that is how a code is sent to an attacker.
		problem(w, http.StatusBadRequest, "The app asked to return to an address it has not registered.")
		return
	}
	state := q.Get("state")
	fail := func(code, description string) { redirectError(w, r, redirect, state, code, description) }
	switch {
	case q.Get("response_type") != "code":
		fail("unsupported_response_type", "response_type must be code")
		return
	case strings.TrimRight(q.Get("aud"), "/") != strings.TrimRight(s.Audience, "/"):
		// SMART: aud names the FHIR server the token is for, so a token cannot be phished for another server.
		fail("invalid_request", "aud must be this server's FHIR base, "+s.Audience)
		return
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		fail("invalid_request", "PKCE is required: code_challenge with code_challenge_method S256")
		return
	}
	p := &pending{client: client, redirect: redirect, state: state, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
		expires: s.now().Add(pendingLife)}
	p.scopes = client.grant(q.Get("scope"))
	if launch := q.Get("launch"); launch != "" {
		ctx, ok := s.takeLaunch(launch)
		if !ok {
			fail("invalid_request", "the launch is unknown or has expired; launch the app again")
			return
		}
		p.launchPatient, p.launchEncounter = ctx.patient, ctx.encounter
	} else {
		p.scopes = slices.DeleteFunc(p.scopes, func(sc string) bool { return sc == "launch" })
	}
	if len(p.scopes) == 0 {
		fail("invalid_scope", "none of the requested scopes is registered for this app")
		return
	}
	id := newID()
	s.mu.Lock()
	s.pruneLocked()
	if s.pending == nil {
		s.pending = map[string]*pending{}
	}
	s.pending[id] = p
	s.mu.Unlock()
	s.render(w, "signin", map[string]any{"Req": id, "App": appName(client)})
}

func appName(c *Client) string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	secureHeaders(w)
	_ = pages.ExecuteTemplate(w, page, data)
}

// pendingFor finds the authorization a form belongs to.
func (s *Server) pendingFor(r *http.Request) (string, *pending) {
	id := r.PostForm.Get("req")
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	if p == nil || s.now().After(p.expires) {
		delete(s.pending, id)
		return id, nil
	}
	return id, p
}

func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		problem(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	id, p := s.pendingFor(r)
	if p == nil {
		problem(w, http.StatusBadRequest, "This sign-in has expired. Go back to the app and start again.")
		return
	}
	u := s.Users[r.PostForm.Get("username")]
	ok := false
	if u != nil {
		ok, _ = store.VerifyPassword(u.PasswordHash, r.PostForm.Get("password"))
	} else {
		// The same work for an unknown name, so timing does not say which names exist.
		_, _ = store.VerifyPassword(dummyHash, r.PostForm.Get("password"))
	}
	if !ok {
		s.mu.Lock()
		p.failures++
		tooMany := p.failures >= maxSignInFailures
		if tooMany {
			delete(s.pending, id)
		}
		s.mu.Unlock()
		if tooMany {
			redirectError(w, r, p.redirect, p.state, "access_denied", "too many failed sign-in attempts")
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "signin", map[string]any{"Req": id, "App": appName(p.client), "Error": "That username and password did not match."})
		return
	}
	s.mu.Lock()
	p.user = u
	s.mu.Unlock()
	s.next(w, r, id, p)
}

var dummyHash, _ = store.HashPassword("not a real password")

// next shows the patient picker when a clinician must choose a patient, and the consent page otherwise.
func (s *Server) next(w http.ResponseWriter, r *http.Request, id string, p *pending) {
	if own := p.user.patientID(); own != "" {
		p.patient = own
	} else if p.launchPatient != "" {
		p.patient = p.launchPatient
	}
	if p.patient == "" && slices.Contains(p.scopes, "launch/patient") {
		var choices []PatientChoice
		if s.Patients != nil {
			choices, _ = s.Patients(r.Context(), r.PostForm.Get("search"))
		}
		s.render(w, "patient", map[string]any{"Req": id, "App": appName(p.client), "Patients": choices,
			"Search": r.PostForm.Get("search")})
		return
	}
	s.render(w, "consent", map[string]any{"Req": id, "App": appName(p.client), "Scopes": describeScopes(p.scopes),
		"Patient": p.patient, "User": p.user.Name})
}

func (s *Server) handlePatient(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		problem(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	id, p := s.pendingFor(r)
	if p == nil || p.user == nil || p.user.patientID() != "" {
		problem(w, http.StatusBadRequest, "This sign-in has expired. Go back to the app and start again.")
		return
	}
	if r.PostForm.Has("search") && !r.PostForm.Has("patient") {
		s.next(w, r, id, p)
		return
	}
	chosen := r.PostForm.Get("patient")
	if chosen == "" || s.PatientExists == nil || !s.PatientExists(r.Context(), chosen) {
		s.next(w, r, id, p)
		return
	}
	s.mu.Lock()
	p.patient = chosen
	s.mu.Unlock()
	s.next(w, r, id, p)
}

func (s *Server) handleConsent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		problem(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	id, p := s.pendingFor(r)
	if p == nil || p.user == nil {
		problem(w, http.StatusBadRequest, "This sign-in has expired. Go back to the app and start again.")
		return
	}
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
	if r.PostForm.Get("action") != "allow" {
		redirectError(w, r, p.redirect, p.state, "access_denied", "the person did not allow access")
		return
	}
	// Only scopes that were asked for and are still ticked: unticking one is how a person limits an app.
	var scopes []string
	for _, sc := range r.PostForm["scope"] {
		if slices.Contains(p.scopes, sc) && !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	// openid, fhirUser and the launch scopes say who and whose, not what; they are not offered as choices.
	for _, sc := range p.scopes {
		if !isDataScope(sc) && !slices.Contains(scopes, sc) && sc != "offline_access" && sc != "online_access" {
			scopes = append(scopes, sc)
		}
	}
	if p.user.patientID() != "" {
		// A member's user/ scopes mean their own record, which is what patient/ scopes say to the FHIR endpoint; left as
		// user/ they would reach whatever the endpoint lets a user see.
		for i, sc := range scopes {
			if rest, ok := strings.CutPrefix(sc, "user/"); ok {
				scopes[i] = "patient/" + rest
			}
		}
		scopes = slices.Compact(scopes)
	}
	if p.patient == "" {
		// patient/ scopes with no patient would read every patient's records.
		scopes = slices.DeleteFunc(scopes, func(sc string) bool { return strings.HasPrefix(sc, "patient/") || sc == "launch/patient" })
	}
	g := &grantRecord{client: p.client, redirect: p.redirect, challenge: p.challenge, nonce: p.nonce, scopes: scopes,
		patient: p.patient, encounter: p.launchEncounter, user: p.user, expires: s.now().Add(codeLife)}
	code := newID()
	s.mu.Lock()
	if s.codes == nil {
		s.codes = map[string]*grantRecord{}
	}
	s.codes[code] = g
	s.mu.Unlock()
	u, _ := url.Parse(p.redirect)
	q := u.Query()
	q.Set("code", code)
	if p.state != "" {
		q.Set("state", p.state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func isDataScope(sc string) bool {
	return strings.HasPrefix(sc, "patient/") || strings.HasPrefix(sc, "user/") || sc == "offline_access" || sc == "online_access"
}

type scopeChoice struct{ Scope, Text string }

// describeScopes words each choosable scope for the person approving it.
func describeScopes(scopes []string) []scopeChoice {
	var out []scopeChoice
	for _, sc := range scopes {
		if !isDataScope(sc) {
			continue
		}
		text := sc
		switch {
		case sc == "offline_access":
			text = "Keep access after you close the app, until you revoke it"
		case sc == "online_access":
			text = "Keep access while you are using the app"
		default:
			who, rest, _ := strings.Cut(sc, "/")
			res, perm, _ := strings.Cut(rest, ".")
			if q := strings.IndexByte(perm, '?'); q >= 0 {
				res += " (" + perm[q+1:] + ")"
				perm = perm[:q]
			}
			if res == "*" || strings.HasPrefix(res, "* ") {
				res = "all records" + strings.TrimPrefix(res, "*")
			}
			verb := "Read"
			if strings.ContainsAny(perm, "cud") || perm == "write" || perm == "*" {
				verb = "Read and change"
				if !strings.ContainsAny(perm, "rs") && perm != "*" {
					verb = "Change"
				}
			}
			whose := "the patient's"
			if who == "user" {
				whose = "any records you can see:"
			}
			text = verb + " " + whose + " " + res
		}
		out = append(out, scopeChoice{Scope: sc, Text: text})
	}
	return out
}

// authorizationCode exchanges a code, checking the client, the redirect URI and the PKCE verifier.
func (s *Server) authorizationCode(w http.ResponseWriter, r *http.Request) {
	client, err := s.authenticatedClient(r)
	if err != nil {
		tokenError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	code := r.PostForm.Get("code")
	s.mu.Lock()
	g := s.codes[code]
	delete(s.codes, code) // single use, whatever happens next
	s.mu.Unlock()
	switch {
	case g == nil || s.now().After(g.expires):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the code is unknown, used or expired")
		return
	case g.client != client:
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
		return
	case r.PostForm.Get("redirect_uri") != "" && r.PostForm.Get("redirect_uri") != g.redirect:
		tokenError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri is not the one the code was issued for")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(g.challenge)) != 1 {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the code_verifier does not match the code_challenge")
		return
	}
	s.issueUserTokens(w, g, "")
}

// issueUserTokens answers with an access token, and an ID token and refresh token when their scopes were granted.
func (s *Server) issueUserTokens(w http.ResponseWriter, g *grantRecord, refresh string) {
	fhirUser := strings.TrimRight(s.Audience, "/") + "/" + g.user.FHIRUser
	extra := map[string]any{"fhirUser": fhirUser}
	if g.patient != "" {
		extra["patient"] = g.patient
	}
	if g.encounter != "" {
		extra["encounter"] = g.encounter
	}
	scope := strings.Join(g.scopes, " ")
	tok, err := s.accessToken(g.user.Username, g.client.ID, scope, extra, userTokenLife)
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error", "the token could not be signed")
		return
	}
	body := map[string]any{"access_token": tok, "token_type": "bearer", "expires_in": int(userTokenLife.Seconds()), "scope": scope}
	if g.patient != "" {
		body["patient"] = g.patient
		body["need_patient_banner"] = true
	}
	if g.encounter != "" {
		body["encounter"] = g.encounter
	}
	if slices.Contains(g.scopes, "openid") {
		now := s.now()
		claims := map[string]any{"iss": s.Issuer, "sub": g.user.Username, "aud": g.client.ID, "iat": now.Unix(),
			"exp": now.Add(userTokenLife).Unix()}
		if slices.Contains(g.scopes, "fhirUser") {
			claims["fhirUser"] = fhirUser
		}
		if g.nonce != "" {
			claims["nonce"] = g.nonce
		}
		if g.user.Name != "" && slices.Contains(g.scopes, "profile") {
			claims["name"] = g.user.Name
		}
		idt, err := s.Key.Sign(claims)
		if err != nil {
			tokenError(w, http.StatusInternalServerError, "server_error", "the ID token could not be signed")
			return
		}
		body["id_token"] = idt
	}
	if refresh == "" && (slices.Contains(g.scopes, "offline_access") || slices.Contains(g.scopes, "online_access")) {
		refresh = newID()
		life := 90 * 24 * time.Hour
		if !slices.Contains(g.scopes, "offline_access") {
			life = 12 * time.Hour
		}
		r := *g
		r.expires = s.now().Add(life)
		s.mu.Lock()
		if s.refresh == nil {
			s.refresh = map[string]*grantRecord{}
		}
		s.refresh[refresh] = &r
		s.mu.Unlock()
	}
	if refresh != "" {
		body["refresh_token"] = refresh
	}
	writeToken(w, body)
}

// refreshToken issues a new access token for a refresh token, narrowed when the client asks for fewer scopes.
func (s *Server) refreshToken(w http.ResponseWriter, r *http.Request) {
	client, err := s.authenticatedClient(r)
	if err != nil {
		tokenError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	rt := r.PostForm.Get("refresh_token")
	s.mu.Lock()
	g := s.refresh[rt]
	s.mu.Unlock()
	if g == nil || s.now().After(g.expires) || g.client != client {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is unknown, expired or another client's")
		return
	}
	issued := *g
	if want := strings.Fields(r.PostForm.Get("scope")); len(want) > 0 {
		var narrowed []string
		for _, sc := range want {
			if !slices.Contains(g.scopes, sc) {
				tokenError(w, http.StatusBadRequest, "invalid_scope", sc+" was not granted, so it cannot be refreshed")
				return
			}
			narrowed = append(narrowed, sc)
		}
		issued.scopes = narrowed
	}
	s.issueUserTokens(w, &issued, rt)
}

// authenticatedClient authenticates the client of an authorization code or refresh request as its kind requires: a public
// client by its id alone (PKCE or the refresh token is the proof), a symmetric one by its secret, an asymmetric one by assertion.
func (s *Server) authenticatedClient(r *http.Request) (*Client, error) {
	if r.PostForm.Get("client_assertion") != "" {
		c, err := s.assertedClient(r.Context(), r.PostForm)
		if err != nil {
			return nil, err
		}
		if c.Kind != KindAsymmetric {
			return nil, errClient("this client does not authenticate with a signed assertion")
		}
		return c, nil
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c := s.Clients[id]
	if c == nil {
		return nil, errClient("unknown client")
	}
	switch c.Kind {
	case KindPublic:
		if secret != "" {
			return nil, errClient("a public client has no secret")
		}
		return c, nil
	case KindSymmetric:
		if ok, _ := store.VerifyPassword(c.SecretHash, secret); !ok {
			return nil, errClient("the client secret is wrong")
		}
		return c, nil
	default:
		return nil, errClient("this client authenticates with a signed assertion")
	}
}

type errClient string

func (e errClient) Error() string { return string(e) }

// pruneLocked drops expired authorizations, codes and refresh tokens. The caller holds s.mu.
func (s *Server) pruneLocked() {
	now := s.now()
	for k, p := range s.pending {
		if now.After(p.expires) {
			delete(s.pending, k)
		}
	}
	for k, g := range s.codes {
		if now.After(g.expires) {
			delete(s.codes, k)
		}
	}
	for k, g := range s.refresh {
		if now.After(g.expires) {
			delete(s.refresh, k)
		}
	}
	for k, exp := range s.revoked {
		if now.After(exp) {
			delete(s.revoked, k)
		}
	}
}

type launchContext struct {
	patient, encounter string
	expires            time.Time
}

// takeLaunch resolves an EHR launch id, once.
func (s *Server) takeLaunch(id string) (launchContext, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.launches[id]
	delete(s.launches, id)
	if !ok || s.now().After(l.expires) {
		return launchContext{}, false
	}
	return l, true
}
