package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Choosing the FHIR endpoint's authentication for the combined server.
//
// Its own file because the decision has three inputs and one of them is a security default. Inlined in serve.go it would
// have been a switch buried in three hundred lines of wiring, which is roughly how the previous state came about: the
// authenticator was simply never set, so every request answered 500 while the startup log described an open endpoint.

// fhirAuthOptions is what the choice depends on.
type fhirAuthOptions struct {
	SMARTIssuer   string
	SMARTAudience string

	// BaseURL is this server's FHIR base, used as the SMART audience when none is given.
	BaseURL string

	Open     bool
	ReadOnly bool

	Store *store.Store
	Log   *slog.Logger
}

// fhirAuthenticator picks the scheme.
//
// The default is Perfuse API tokens, which is the useful default rather than the cautious one: this server already has a
// token store, an interface for issuing tokens, and a role on each. Refusing to start without an explicit choice - as the
// standalone fhir command does - makes sense there, where there may be no database at all, and would be an obstacle here
// for no gain.
//
// Open is possible and loud. Refusing outright would push somebody towards a worse workaround, which is usually a reverse
// proxy configured once and forgotten.
func fhirAuthenticator(opts fhirAuthOptions) (fhirserver.Authenticator, error) {
	if opts.Open && opts.SMARTIssuer != "" {
		return nil, fmt.Errorf("-fhir-open and -smart-issuer contradict each other: one accepts everybody " +
			"and the other checks tokens, and there is no sensible way to do both")
	}

	if opts.Open {
		return fhirserver.OpenAuth{}, nil
	}

	if opts.SMARTIssuer != "" {
		audience := opts.SMARTAudience
		if audience == "" {
			// Defaulted to this server's own FHIR base URL, which is what a correctly configured
			// authorization server puts in aud - it is the address the app asked for a token for.
			//
			// Defaulted rather than required because getting it wrong in the other direction is worse: an
			// operator who cannot make it work is tempted towards -fhir-open. The value is logged so a
			// mismatch is visible, and NewSMARTAuth still refuses an empty one.
			audience = opts.BaseURL
			opts.Log.Info("no -smart-audience given, so SMART tokens must be issued for this server's own "+
				"FHIR base URL", "audience", audience)
		}

		auth, err := fhirserver.NewSMARTAuth(fhirserver.SMARTConfig{
			Issuer:   opts.SMARTIssuer,
			Audience: audience,
		})
		if err != nil {
			return nil, err
		}

		return auth, nil
	}

	if opts.Store == nil {
		return nil, fmt.Errorf("the FHIR endpoint needs either a database for its API tokens, a SMART " +
			"issuer, or -fhir-open")
	}

	return &fhirserver.BearerAuth{
		Lookup: func(ctx context.Context, token string) (string, string, error) {
			sess, err := opts.Store.LookupAPIToken(ctx, token)
			if err != nil {
				return "", "", err
			}

			return sess.Username, string(sess.Role), nil
		},
		Log: opts.Log,
	}, nil
}

// smartConfigured reports whether enough was given to advertise SMART endpoints.
//
// Used only for a startup message. Kept as a named function rather than an inline condition so the message and the
// discovery document cannot disagree about what "configured" means.
func smartConfigured(authorize, token string) bool {
	return strings.TrimSpace(authorize) != "" && strings.TrimSpace(token) != ""
}
