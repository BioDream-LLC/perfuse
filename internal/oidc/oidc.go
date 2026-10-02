// Package oidc verifies OpenID Connect identities.
//
// Written against the standard library rather than a JWT dependency. Authentication is the one place where an unaudited
// dependency is least acceptable, signature verification for the algorithms that matter is about a hundred lines of
// crypto/rsa and crypto/ecdsa, and Perfuse already carries enough that adding more needs a reason rather than a habit.
//
// The scope is deliberately narrow: verify that an identity provider vouched for a person, and report what it said about
// them. Everything else - what they may do, how long the session lasts - stays with Perfuse, because an identity provider
// knows who somebody is and not what they are allowed to do in an interface engine.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider is a discovered identity provider.
type Provider struct {
	Issuer      string
	AuthURL     string
	TokenURL    string
	JWKSURL     string
	UserInfoURL string

	// AlgorithmsSupported is what the provider says it will sign with.
	//
	// Recorded so a provider offering only algorithms this does not implement is reported at configuration time rather
	// than at somebody's first sign-in attempt.
	AlgorithmsSupported []string
}

// discovery is the subset of the discovery document that matters.
//
// A subset deliberately. The document carries a great deal that this does not use, and decoding only what is needed means a
// provider adding a field cannot break sign-in.
type discovery struct {
	Issuer      string   `json:"issuer"`
	AuthURL     string   `json:"authorization_endpoint"`
	TokenURL    string   `json:"token_endpoint"`
	JWKSURL     string   `json:"jwks_uri"`
	UserInfoURL string   `json:"userinfo_endpoint"`
	Algorithms  []string `json:"id_token_signing_alg_values_supported"`
}

// Discover reads a provider's configuration from its issuer URL.
//
// The issuer is checked against the document's own issuer claim, which is not a formality: an attacker who can persuade a
// site to point at their discovery document but cannot forge the issuer inside it is stopped here, and the standard requires
// the check for that reason.
func Discover(ctx context.Context, client *http.Client, issuer string) (*Provider, error) {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return nil, fmt.Errorf("oidc: no issuer was configured")
	}

	parsed, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: the issuer %q is not a URL: %w", issuer, err)
	}
	if parsed.Scheme != "https" && !isLoopback(parsed.Host) {
		// Refused rather than warned. An identity provider reached over plain HTTP can be impersonated by anything on the
		// path, and the tokens it issues are the only thing standing between a stranger and every channel on the server.
		// Loopback is allowed because that is how this gets tested.
		return nil, fmt.Errorf("oidc: the issuer %q is not https; an identity provider over plain http can be "+
			"impersonated by anything between here and it, and its tokens are what guard every channel", issuer)
	}

	endpoint := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: could not reach the identity provider at %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: the identity provider answered %s at %s; check the issuer URL is the one it "+
			"publishes rather than its sign-in page", resp.Status, endpoint)
	}

	// Bounded. A discovery document is a few kilobytes, and reading an unbounded body from something not yet trusted is
	// how a configuration mistake becomes an out-of-memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var doc discovery
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("oidc: the discovery document at %s could not be read: %w", endpoint, err)
	}

	if strings.TrimSuffix(doc.Issuer, "/") != issuer {
		return nil, fmt.Errorf("oidc: the document at %s says its issuer is %q, not %q; tokens are checked against the "+
			"issuer, so this mismatch would reject every sign-in", endpoint, doc.Issuer, issuer)
	}
	if doc.AuthURL == "" || doc.TokenURL == "" || doc.JWKSURL == "" {
		return nil, fmt.Errorf("oidc: the discovery document at %s is missing an endpoint this needs "+
			"(authorization, token or keys)", endpoint)
	}

	return &Provider{
		Issuer:              doc.Issuer,
		AuthURL:             doc.AuthURL,
		TokenURL:            doc.TokenURL,
		JWKSURL:             doc.JWKSURL,
		UserInfoURL:         doc.UserInfoURL,
		AlgorithmsSupported: doc.Algorithms,
	}, nil
}

// SupportsAnyOf reports whether the provider will sign with an algorithm this understands.
func (p *Provider) SupportsAnyOf(algorithms []string) bool {
	if len(p.AlgorithmsSupported) == 0 {
		// Silence is not refusal. RS256 is mandatory for a conforming provider, so a document that omits the list is
		// assumed to do the mandatory thing rather than assumed to be broken.
		return true
	}
	for _, theirs := range p.AlgorithmsSupported {
		for _, ours := range algorithms {
			if theirs == ours {
				return true
			}
		}
	}
	return false
}

// isLoopback reports whether a host is local, which is the only case where plain HTTP is allowed.
func isLoopback(host string) bool {
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}

func splitHostPort(hostport string) (string, string, error) {
	if i := strings.LastIndex(hostport, ":"); i >= 0 && !strings.Contains(hostport[i:], "]") {
		return hostport[:i], hostport[i+1:], nil
	}
	return hostport, "", fmt.Errorf("no port")
}

// DefaultHTTPClient is the client used when none is supplied.
//
// A short timeout on purpose. Every request here happens while a person waits at a sign-in screen, and an identity provider
// that has stopped answering should produce an error they can report rather than a page that hangs.
var DefaultHTTPClient = &http.Client{Timeout: 10 * time.Second}
