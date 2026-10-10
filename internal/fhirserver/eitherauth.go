package fhirserver

import (
	"net/http"
	"strings"
)

// EitherAuth accepts SMART access tokens, and Perfuse API tokens as well while Allow says so.
//
// With SMART configured the FHIR endpoint normally takes SMART tokens only: an API token carries a role and no scopes, so a payer
// who switched SMART on to limit every caller to its scopes would otherwise still have an unscoped way in. Some deployments need
// both anyway - an internal claims feed holding a console token, beside member apps on SMART - so it is a switch, off by default
// and read on every request, so turning it off takes effect on the next call rather than the next restart.
//
// The two are told apart by shape: a SMART access token is a JWT, three dot-separated parts, and a Perfuse API token never contains
// a dot. A JWT is never offered to the API token lookup, and an API token is never offered to the SMART check.
type EitherAuth struct {
	SMART Authenticator
	API   Authenticator
	// Allow reports whether API tokens are accepted beside SMART ones. Nil means never.
	Allow func() bool
}

func (a *EitherAuth) Authenticate(r *http.Request) (*Caller, error) {
	tok, err := bearerToken(r)
	if err == nil && strings.Count(tok, ".") != 2 && a.Allow != nil && a.Allow() {
		return a.API.Authenticate(r)
	}
	return a.SMART.Authenticate(r)
}

func (a *EitherAuth) Describe() string {
	if a.Allow != nil && a.Allow() {
		return a.SMART.Describe() + ", and Perfuse API tokens"
	}
	return a.SMART.Describe()
}
