package fhirserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

// SMART on FHIR authentication.
//
// A SMART app does not hold a Perfuse credential. It asks an authorization server - the hospital's identity provider, or
// its EHR vendor's - for an access token, and presents that. So this server's job is to verify a token it did not issue
// and honour the scopes inside it.
//
// The verification reuses internal/oidc rather than reimplementing JWT checking. That was not laziness: the two dangerous
// mistakes here are trusting the algorithm named in the token and trusting a key the token points at, and that package
// already refuses both - the algorithm comes from a list decided in advance, and the key comes from the JWKS of the
// configured issuer.

// SMARTConfig is what a deployment must state to accept SMART tokens.
type SMARTConfig struct {
	// Issuer is the authorization server. A token claiming any other issuer is refused.
	//
	// Compared exactly. A prefix or suffix comparison would accept an issuer somebody else controls, which is the
	// same mistake the passkey origin check exists to avoid.
	Issuer string

	// Audience is what a token must be issued for, normally this server's FHIR base URL.
	//
	// Required, not optional, and this is the check most often left out. Without it any token from the same
	// authorization server is accepted here - including one a patient's own app obtained for a completely different
	// service, and including one issued to a client that was never authorised to touch clinical data. The signature
	// would be perfectly valid in every case.
	Audience string

	// JWKSURL is where the signing keys are published.
	//
	// Discovered from the issuer's metadata when empty, which is the normal case: an authorization server rotates
	// keys and publishing the location is how it tells anyone.
	JWKSURL string

	// Algorithms are the signing algorithms accepted. Empty accepts every implemented one.
	Algorithms []string

	// Leeway tolerates clock difference with the authorization server.
	Leeway time.Duration

	// HTTPClient fetches metadata and keys.
	HTTPClient *http.Client

	// Now overrides the clock, for tests.
	Now func() time.Time
}

// SMARTAuth authenticates a SMART on FHIR access token.
type SMARTAuth struct {
	cfg  SMARTConfig
	keys *oidc.KeySet

	// discover runs once, so a server that starts before its authorization server does not fail permanently.
	//
	// The alternative - resolving metadata at construction - means Perfuse cannot start if the identity provider is
	// briefly unavailable, which on a hospital network during a maintenance window is a normal Tuesday.
	discoverOnce sync.Once
	discoverErr  error
	resolved     string
}

// NewSMARTAuth prepares SMART authentication.
//
// Refuses a configuration that cannot be safe rather than accepting it and hoping. Both missing values here are ones a
// deployment would not notice: without an issuer there is nothing to check a token against, and without an audience every
// token from that issuer is accepted no matter who it was issued to.
func NewSMARTAuth(cfg SMARTConfig) (*SMARTAuth, error) {
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, fmt.Errorf("a SMART issuer is required; it is the authorization server whose tokens this " +
			"server will accept")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, fmt.Errorf("a SMART audience is required, normally this server's FHIR base URL; without it "+
			"any token from %s would be accepted here, including one issued to a different service "+
			"entirely", cfg.Issuer)
	}
	if !strings.HasPrefix(cfg.Issuer, "https://") && !strings.HasPrefix(cfg.Issuer, "http://") {
		return nil, fmt.Errorf("the SMART issuer %q is not a URL", cfg.Issuer)
	}

	return &SMARTAuth{cfg: cfg}, nil
}

// Describe names the scheme, never a credential.
func (a *SMARTAuth) Describe() string {
	return "a SMART on FHIR access token issued by " + a.cfg.Issuer
}

// Authenticate verifies the bearer token and returns what it grants.
func (a *SMARTAuth) Authenticate(r *http.Request) (*Caller, error) {
	token, err := bearerToken(r)
	if err != nil {
		return nil, err
	}

	keys, err := a.keySet(r.Context())
	if err != nil {
		return nil, err
	}

	claims, err := oidc.Verify(r.Context(), keys, token, oidc.VerifyOptions{
		Issuer: a.cfg.Issuer,
		// The audience, passed as ClientID because that is the field that checks aud. Naming it ClientID is an
		// artefact of that package being written for sign-in; the check is the one that matters here.
		ClientID:   a.cfg.Audience,
		Algorithms: a.cfg.Algorithms,
		Leeway:     a.cfg.Leeway,
		Now:        a.cfg.Now,
		// No nonce. A nonce ties an ID token to one sign-in attempt and has no meaning on an access token
		// presented to a resource server - requiring one would refuse every real SMART token.
	})
	if err != nil {
		return nil, fmt.Errorf("this SMART token was not accepted: %w", err)
	}

	scopes := strings.Fields(claims.Scope)

	// A token with no scopes at all reaches nothing, and is refused here rather than admitted as a caller who can
	// do nothing. The distinction matters in the log: "refused, no scopes" is a configuration problem at the
	// authorization server, and a stream of 403s from an admitted caller looks like an application bug.
	if len(scopes) == 0 {
		return nil, fmt.Errorf("this SMART token grants no scopes, so it permits nothing; the authorization " +
			"server issued it without a scope claim")
	}

	// Name comes from the subject, which is the authorization server's stable identifier. Deliberately not an email
	// or a username: an email is reassigned when somebody leaves, and keying an audit trail on it means their
	// replacement inherits their history.
	name := claims.Subject
	if name == "" {
		name = "smart-client"
	}

	// Write reflects what the token grants, and nothing else.
	//
	// Deliberately not narrowed by this server's read-only setting here. That was the first version and it was a
	// stale copy waiting to happen: read-only became live-editable, so a value read once at construction would have
	// gone out of date the moment somebody changed it in the interface, and the authenticator would have reported a
	// caller as writable on a server that was not.
	//
	// The server refuses writes itself, per request, through isReadOnly - and answers 405 rather than 403, because a
	// read-only server does not offer the interaction at all rather than withholding it from this caller.
	grants := parseSMARTScopes(scopes)
	write := false
	for _, g := range grants {
		if g.Write {
			write = true

			break
		}
	}

	return &Caller{
		Name:   name,
		Scopes: scopes,
		Write:  write,
		// The launch context, taken from the verified claims rather than from a header or a query parameter.
		// Anywhere else and the app would be choosing which patient it is limited to.
		Patient:   strings.TrimSpace(claims.Patient),
		Encounter: strings.TrimSpace(claims.Encounter),
	}, nil
}

// keySet resolves the JWKS location once and caches the key set.
func (a *SMARTAuth) keySet(ctx context.Context) (*oidc.KeySet, error) {
	a.discoverOnce.Do(func() {
		if a.cfg.JWKSURL != "" {
			a.resolved = a.cfg.JWKSURL

			return
		}
		a.resolved, a.discoverErr = a.discoverJWKS(ctx)
	})

	if a.discoverErr != nil {
		// The failure is not cached as permanent for the caller's benefit: a server that started while its
		// authorization server was down would otherwise refuse every request until somebody restarted it. The
		// once is reset so the next request tries again.
		a.discoverOnce = sync.Once{}

		return nil, fmt.Errorf("the signing keys for %s could not be found: %w", a.cfg.Issuer, a.discoverErr)
	}

	if a.keys == nil {
		a.keys = oidc.NewKeySet(a.resolved, a.cfg.HTTPClient)
	}

	return a.keys, nil
}

// discoverJWKS reads the authorization server's metadata to find where its keys are published.
//
// Tries the SMART-specific document first and the OpenID one second. Both are in use: an EHR vendor commonly publishes
// smart-configuration and nothing else, while a general-purpose identity provider publishes openid-configuration. Trying
// only one strands half the deployments.
func (a *SMARTAuth) discoverJWKS(ctx context.Context) (string, error) {
	client := a.cfg.HTTPClient
	if client == nil {
		client = oidc.DefaultHTTPClient
	}

	base := strings.TrimSuffix(a.cfg.Issuer, "/")
	var lastErr error

	for _, path := range []string{"/.well-known/smart-configuration", "/.well-known/openid-configuration"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err

			continue
		}

		var doc struct {
			JWKSURI string `json:"jwks_uri"`
		}
		err = json.NewDecoder(resp.Body).Decode(&doc)
		resp.Body.Close()

		if err != nil || resp.StatusCode != http.StatusOK || doc.JWKSURI == "" {
			lastErr = fmt.Errorf("%s returned no jwks_uri", base+path)

			continue
		}

		return doc.JWKSURI, nil
	}

	return "", lastErr
}

// The discovery document this server publishes.
//
// A SMART app reads this before it has any credential, to find out where to authenticate. So it is served without
// authentication - which is the one deliberate exception to wrapping everything, and it is safe for a reason worth stating:
// it contains the addresses of an authorization server that is already public, and nothing about what this server holds.
//
// That is the difference between this and /metadata. The capability statement lists every resource type and search
// parameter available, which tells an unauthenticated caller exactly what is worth asking for, so it stays behind
// authentication. This document tells them only where to go and ask properly.

// smartConfiguration is the .well-known/smart-configuration body.
//
// Field names and shapes are from the SMART App Launch specification. Only what this server can honestly claim is
// included: advertising a capability that is not implemented is how an app fails at a hospital rather than in testing.
type smartConfiguration struct {
	Issuer                string   `json:"issuer,omitempty"`
	JWKSURI               string   `json:"jwks_uri,omitempty"`
	AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string   `json:"token_endpoint,omitempty"`
	Capabilities          []string `json:"capabilities"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported,omitempty"`
}

// SMARTDiscovery holds what this server advertises to SMART apps.
//
// Separate from SMARTConfig because the two answer different questions. SMARTConfig is what this server will accept, and
// getting it wrong lets the wrong token in. This is what this server tells apps, and getting it wrong sends them to the
// wrong place - annoying rather than dangerous, and a reason not to let one struct do both.
type SMARTDiscovery struct {
	// Issuer, AuthorizationEndpoint and TokenEndpoint belong to the authorization server, not to Perfuse.
	//
	// Perfuse is the resource server in a SMART launch. It holds the clinical data and honours tokens; it does not
	// issue them. Advertising its own address here would send every app to an endpoint that does not exist.
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSURI               string
}

// HasEndpoints reports whether enough is configured to publish anything useful.
func (d SMARTDiscovery) HasEndpoints() bool {
	return strings.TrimSpace(d.AuthorizationEndpoint) != "" && strings.TrimSpace(d.TokenEndpoint) != ""
}

// handleSMARTConfiguration serves .well-known/smart-configuration.
func (s *Server) handleSMARTConfiguration(w http.ResponseWriter, r *http.Request) {
	d := s.SMART

	// A 404 rather than an empty document when nothing is configured. An app reading a document that lists no
	// endpoints cannot tell "this server does not do SMART" from "this server does SMART and is misconfigured", and
	// the two need different people to fix them.
	if !d.HasEndpoints() {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server is not configured for SMART on FHIR; no authorization server has been set")

		return
	}

	// Only capabilities this server actually has.
	//
	// context-standalone-patient is claimed now that the patient claim is read and enforced on every read, write,
	// delete, search and bundle entry. It was deliberately absent before: telling an app it can rely on a context
	// that is not enforced is worse than saying nothing, because the app scopes itself to one patient and receives
	// everyone.
	//
	// Still absent, and still deliberately: launch-ehr and context-ehr-patient, which require the EHR launch
	// sequence - an app is handed a launch token and exchanges it for context. Perfuse honours a context that is
	// already in a token; it does not participate in producing one.
	//
	// context-standalone-encounter is claimed now that the encounter claim is read and enforced on reads, searches and
	// writes. Still absent are the EHR launch capabilities - context-ehr-patient and context-ehr-encounter - because
	// those describe an authorization server participating in a launch sequence, which this is not: it verifies a token
	// somebody else issued. Claiming them would make an EHR attempt a launch against an endpoint that cannot answer.
	caps := []string{
		"client-public",
		"client-confidential-symmetric",
		"context-standalone-patient",
		"context-standalone-encounter",
		"launch-standalone",
		"permission-patient",
		"permission-v1",
		"permission-v2",
		"sso-openid-connect",
	}

	body := smartConfiguration{
		Issuer:                d.Issuer,
		JWKSURI:               d.JWKSURI,
		AuthorizationEndpoint: d.AuthorizationEndpoint,
		TokenEndpoint:         d.TokenEndpoint,
		Capabilities:          caps,
		// Both spellings, because version 1 and version 2 of the specification differ and an authorization
		// server may ask for either. Wildcards rather than a per-resource list: the resource types this server
		// holds are in the capability statement, which is behind authentication on purpose.
		ScopesSupported: []string{
			"openid", "fhirUser", "offline_access",
			"patient/*.read", "user/*.read", "system/*.read",
			"patient/*.rs", "user/*.rs", "system/*.rs",
		},
		// S256 only. The plain method exists in the specification and offers no protection worth having, so it
		// is not advertised - an app that would have used plain will use S256 instead.
		CodeChallengeMethods: []string{"S256"},
	}

	w.Header().Set("Content-Type", "application/json")
	// Cacheable, because every app start reads it and it changes when somebody changes configuration rather than
	// per request. Five minutes so a correction propagates within a coffee break.
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(body)
}
