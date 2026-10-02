package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/saml"
	"github.com/biodream-llc/perfuse/internal/store"
)

// SAMLConfig is how SAML sign-in is configured.
//
// Deliberately shaped like OIDCConfig, including reusing oidc.RoleMapping. The two protocols disagree about almost everything on
// the wire and agree completely about what an administrator has to decide: which directory, who is allowed in, which groups grant
// which role, and whether a first sign-in may create an account. Two different shapes for that would mean two screens to learn and
// two places for the same misconfiguration to hide.
type SAMLConfig struct {
	// EntityID identifies Perfuse to the identity provider, and is the audience the assertion must be addressed to.
	EntityID string

	// ACSURL is where the identity provider posts its response. It has to match what is registered there exactly.
	ACSURL string

	// IdPSSOURL is where the browser is sent to sign in.
	IdPSSOURL string

	// IdPCertPEM is the certificate that signs responses, and the only one trusted. A certificate carried inside a response is
	// never used: see internal/saml/embeddedcert_test.go for what that would cost.
	IdPCertPEM string

	// GroupsAttribute names the assertion attribute carrying group membership.
	//
	// There is no standard for this and no useful default. Entra sends "groups", Okta usually sends whatever the administrator
	// named in the app configuration, ADFS sends a claim URI. Guessing would produce a sign-in that works and grants nobody
	// anything, which reads as a broken product rather than as a missing setting.
	GroupsAttribute string

	// Roles maps the provider's groups to Perfuse roles.
	Roles *oidc.RoleMapping

	// CreateUsers allows a first sign-in to create an account. False is the safe default, as with OIDC.
	CreateUsers bool

	// AllowUnsolicited accepts a response that answers no request this server sent, which is how a portal tile works. Off by
	// default: it is also what lets a captured response be posted into somebody else's browser.
	AllowUnsolicited bool

	// Label is what the sign-in button says.
	Label string

	// SP is built at startup so a bad certificate is reported then rather than at somebody's first sign-in.
	SP *saml.ServiceProvider
}

// Enabled reports whether SAML sign-in is configured.
func (c *SAMLConfig) Enabled() bool {
	return c != nil && c.SP != nil && c.IdPSSOURL != "" && c.IdPCertPEM != ""
}

// ButtonLabel is what the sign-in button says.
func (c *SAMLConfig) ButtonLabel() string {
	if c == nil {
		return "Sign in with SAML"
	}

	if strings.TrimSpace(c.Label) != "" {
		return c.Label
	}

	return "Sign in with SAML"
}

// samlRequests holds the ids of AuthnRequests waiting for an answer.
//
// In memory and not in the database, for the same reasons as the OIDC attempts beside it: an outstanding request is worthless after
// a few minutes and meaningless on another server, so writing it down would create a table whose rows exist only to be forgotten.
//
// The consequence is worth stating plainly rather than discovering: behind two Perfuse servers without sticky sessions, a browser
// that starts a login on one and comes back to the other is refused, because the second has never heard of the request. That is the
// correct failure - the alternative is accepting a response nobody asked for - but it is a deployment note, not a bug to file.
type samlRequests struct {
	mu  sync.Mutex
	ids map[string]samlPending
}

type samlPending struct {
	started  time.Time
	returnTo string
}

const (
	// samlRequestLifetime is how long a login may take. Long enough for a password, a second factor and a fumbled one-time code;
	// short enough that an abandoned attempt is not still open an hour later.
	samlRequestLifetime = 10 * time.Minute

	// samlMaxOutstanding caps the map so that an unauthenticated endpoint cannot be used to grow memory without limit. Reaching it
	// means something is generating requests rather than people signing in.
	samlMaxOutstanding = 1000
)

func newSAMLRequests() *samlRequests {
	return &samlRequests{ids: make(map[string]samlPending)}
}

func (a *samlRequests) put(id string, p samlPending) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.expireLocked()

	if len(a.ids) >= samlMaxOutstanding {
		return false
	}

	a.ids[id] = p

	return true
}

// take consumes an id, reporting whether it was outstanding.
//
// Consuming is the point rather than a tidy-up. A response answers one login attempt, so the second presentation of the same
// response finds nothing and is refused.
func (a *samlRequests) take(id string) (samlPending, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.expireLocked()

	p, ok := a.ids[id]
	if !ok {
		return samlPending{}, false
	}

	delete(a.ids, id)

	return p, true
}

func (a *samlRequests) expireLocked() {
	cutoff := time.Now().Add(-samlRequestLifetime)
	for id, p := range a.ids {
		if p.started.Before(cutoff) {
			delete(a.ids, id)
		}
	}
}

func (s *Server) ensureSAMLRequests() {
	s.samlOnce.Do(func() {
		if s.samlPending == nil {
			s.samlPending = newSAMLRequests()
		}
	})
}

// handleSAMLStart sends the browser to the identity provider.
func (s *Server) handleSAMLStart(w http.ResponseWriter, r *http.Request) {
	if !s.SAML.Enabled() {
		s.signInFailed(w, r, "SAML sign-in is not configured on this server")

		return
	}

	s.ensureSAMLRequests()

	returnTo := safeReturnTo(r.URL.Query().Get("returnTo"))

	redirectURL, requestID, err := s.SAML.SP.BuildAuthnRequest(returnTo)
	if err != nil {
		s.log().Error("could not build a SAML request", "err", err)
		s.signInFailed(w, r, "the sign-in request could not be built")

		return
	}

	if !s.samlPending.put(requestID, samlPending{started: time.Now(), returnTo: returnTo}) {
		// Refused rather than allowed through without tracking, which would mean accepting a response nobody asked for.
		s.log().Error("too many SAML sign-ins are already in progress", "limit", samlMaxOutstanding)
		s.signInFailed(w, r, "too many sign-ins are in progress; try again in a few minutes")

		return
	}

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// handleSAMLACS consumes the identity provider's response and signs somebody in.
//
// A POST from a form the identity provider rendered in the user's browser, so it carries no Perfuse header and cannot: the browser
// is following an instruction from another origin. That is what the signature, the audience, the destination, the conditions, the
// assertion-id cache and the InResponseTo check are all for. This endpoint trusts the document only after all of them.
func (s *Server) handleSAMLACS(w http.ResponseWriter, r *http.Request) {
	if !s.SAML.Enabled() {
		s.signInFailed(w, r, "SAML sign-in is not configured on this server")

		return
	}

	s.ensureSAMLRequests()

	if err := r.ParseForm(); err != nil {
		s.signInFailed(w, r, "the response from the identity provider could not be read")

		return
	}

	encoded := r.PostFormValue("SAMLResponse")
	if encoded == "" {
		s.signInFailed(w, r, "the identity provider sent no response")

		return
	}

	// returnTo is recovered from the request we sent, not from RelayState, even though the provider echoes RelayState back.
	// RelayState arrives from outside and would be an open redirect; the request record is ours.
	returnTo := "/"

	var matched samlPending

	assertion, err := s.SAML.SP.ParseResponseFor(encoded, func(id string) bool {
		p, ok := s.samlPending.take(id)
		if ok {
			matched = p
		}

		return ok
	})
	if err != nil {
		// Logged in full, summarised on screen. A verification failure is a misconfiguration or an attack, and in either case the
		// detail belongs where an operator can read it rather than where a stranger can.
		s.log().Error("a SAML response failed verification", "err", err, "ip", clientIP(r))
		s.signInFailed(w, r, "the identity provider's response could not be verified")

		return
	}

	if matched.returnTo != "" {
		returnTo = matched.returnTo
	}

	groups := s.samlGroups(assertion)

	role := s.SAML.Roles.RoleFor(groups)
	if role == oidc.RoleNone {
		// The groups are logged because "no role" is almost always a mapping that does not match what the directory sends, and the
		// only way to fix that is to see what it sent. The attribute names are logged too: the commonest cause is reading the
		// wrong attribute, which looks identical to somebody being in no groups.
		s.log().Warn("a SAML sign-in matched no role",
			"nameID", assertion.NameID, "groupsAttribute", s.SAML.GroupsAttribute,
			"groups", groups, "attributesPresent", samlAttributeNames(assertion),
			"mapping", s.SAML.Roles.Describe())
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: assertion.NameID, Action: "login.refused.norole", IP: clientIP(r),
		})
		s.signInFailed(w, r, "your account is not a member of any group that grants access here")

		return
	}

	identity := store.ExternalIdentity{
		// The SSO URL stands in for an issuer. A SAML issuer is a string the provider chooses and an administrator may not know,
		// whereas the URL is what they configured here, so it is both stable and recognisable in an audit log.
		Issuer:  s.SAML.IdPSSOURL,
		Subject: assertion.NameID,
		Email: samlFirstAttr(assertion, "email", "mail", "emailAddress",
			"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"),
		DisplayName: samlFirstAttr(assertion, "displayName", "name", "cn",
			"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name"),
		Username: samlUsername(assertion),
		Role:     store.Role(role),
		// Stated rather than derived. A SAML issuer is an https URL and so is an OIDC one, so the store cannot tell them apart and recorded
		// every SAML account as OIDC until this was passed.
		Source: store.AuthSAML,
	}

	token, user, created, err := s.Store.SignInExternal(r.Context(), identity, s.SAML.CreateUsers,
		clientIP(r), r.UserAgent())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNoExternalMatch):
			s.log().Warn("a SAML sign-in had no account and account creation is off", "nameID", assertion.NameID)
			s.signInFailed(w, r, "you do not have an account here yet, and this server does not create them "+
				"automatically; ask an administrator to add you")
		case errors.Is(err, store.ErrLocalAccountExists):
			s.log().Error("a SAML sign-in collided with a local account", "err", err)
			s.signInFailed(w, r, "an account with that name already exists here and is not linked to your directory "+
				"identity; an administrator will need to resolve it")
		case errors.Is(err, store.ErrDisabled):
			s.signInFailed(w, r, "this account is disabled")
		default:
			s.log().Error("a SAML sign-in failed", "err", err)
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

	s.log().Info("SAML login",
		"user", user.Username, "role", string(user.Role), "idp", s.SAML.IdPSSOURL,
		"created", created, "ip", clientIP(r))

	action := "login.saml"
	if created {
		action = "login.saml.created"
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: user.Username, Action: action, IP: clientIP(r),
	})

	http.Redirect(w, r, returnTo, http.StatusFound)
}

// samlGroups reads group membership from the configured attribute.
func (s *Server) samlGroups(a *saml.Assertion) []string {
	if s.SAML.GroupsAttribute == "" {
		return nil
	}

	// Matched case-insensitively and by trailing segment, because a claim arrives as a URI often enough that requiring the full
	// string would make the setting a transcription exercise. "groups" therefore also finds the Microsoft claim URI ending in
	// /groups, which is what somebody typing "groups" meant.
	want := strings.ToLower(s.SAML.GroupsAttribute)

	for name, values := range a.Attributes {
		lower := strings.ToLower(name)
		if lower == want || strings.HasSuffix(lower, "/"+want) {
			return values
		}
	}

	return nil
}

// samlFirstAttr returns the first value found under any of the given attribute names.
func samlFirstAttr(a *saml.Assertion, names ...string) string {
	for _, want := range names {
		lower := strings.ToLower(want)
		for name, values := range a.Attributes {
			got := strings.ToLower(name)
			if (got == lower || strings.HasSuffix(got, "/"+lower)) && len(values) > 0 && values[0] != "" {
				return values[0]
			}
		}
	}

	return ""
}

// samlAttributeNames lists what the assertion actually carried, sorted.
//
// Logged when a sign-in matches no role. The commonest cause is reading the wrong attribute, and that is indistinguishable from
// somebody being in no groups unless the log says which attributes were present.
func samlAttributeNames(a *saml.Assertion) []string {
	names := make([]string, 0, len(a.Attributes))
	for name := range a.Attributes {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// samlUsername picks the name the account is known by.
//
// An email local part where there is one, because a NameID is often a long opaque string or a Windows-style domain\user, and
// neither is a name anybody wants to see in an audit log next to a channel they changed.
func samlUsername(a *saml.Assertion) string {
	if email := samlFirstAttr(a, "email", "mail", "emailAddress",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"); email != "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			return strings.ToLower(email[:at])
		}
	}

	// Then the user principal name, which is where Entra puts a usable identifier.
	//
	// Entra sends no email claim at all. Its assertion carries displayname, identityprovider, objectidentifier, tenantid and name, and the
	// name claim holds the user principal name - an address-shaped string. Without this, an Entra account is created under the NameID, which
	// Entra makes an opaque persistent identifier by default: a real sign-in produced the account name
	// 99d-dljf9omwoabcgx3kpkzm22vvzapha5ktwctmnc4, which is what then appears beside every channel that account changes.
	//
	// Only used when it contains an @, because the name claim is a display name at some providers and "Perfuse Entra Test" is not a username.
	if upn := samlFirstAttr(a, "upn", "userPrincipalName",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn", "name"); upn != "" {
		if at := strings.IndexByte(upn, '@'); at > 0 {
			return strings.ToLower(upn[:at])
		}
	}

	name := a.NameID
	if at := strings.IndexByte(name, '@'); at > 0 {
		return strings.ToLower(name[:at])
	}

	// A domain-qualified Windows name keeps only the account part, for the same reason.
	if slash := strings.LastIndexByte(name, '\\'); slash >= 0 && slash+1 < len(name) {
		return strings.ToLower(name[slash+1:])
	}

	return strings.ToLower(name)
}

// safeReturnTo keeps a redirect target inside this site.
//
// Only a rooted path with no scheme and no host. Anything else becomes the root, because a sign-in endpoint that will forward a
// browser to an arbitrary URL after authenticating is an open redirect, and an open redirect on a login page is how a convincing
// credential-phishing link is built.
func safeReturnTo(candidate string) string {
	if candidate == "" || !strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, "//") {
		return "/"
	}

	if strings.ContainsAny(candidate, "\\\r\n") {
		return "/"
	}

	return candidate
}
