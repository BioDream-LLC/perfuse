package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// AuthRequest is the state of one sign-in attempt.
//
// Held server-side rather than round-tripped through the browser. The verifier and nonce are what prove the callback belongs
// to this attempt, and a browser that carries them can be persuaded to carry an attacker's instead.
type AuthRequest struct {
	// State is the opaque value echoed back, which is what defends against a forged callback.
	State string

	// Nonce ties the resulting token to this attempt.
	Nonce string

	// Verifier is the PKCE code verifier.
	Verifier string

	// RedirectURI is where the provider sends the browser back.
	RedirectURI string
}

// NewAuthRequest generates a sign-in attempt.
//
// PKCE is used unconditionally, including with a client secret. It was designed for clients that cannot keep a secret and is
// now recommended for all of them, because it also defends against an authorization code stolen in transit - a redirect
// through a browser passes through more hands than the secret does.
func NewAuthRequest(redirectURI string) (*AuthRequest, error) {
	state, err := randomString(32)
	if err != nil {
		return nil, err
	}
	nonce, err := randomString(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomString(64)
	if err != nil {
		return nil, err
	}

	return &AuthRequest{State: state, Nonce: nonce, Verifier: verifier, RedirectURI: redirectURI}, nil
}

// Challenge is the S256 code challenge for the verifier.
func (a *AuthRequest) Challenge() string {
	sum := sha256.Sum256([]byte(a.Verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthURL builds the URL to send the browser to.
func (a *AuthRequest) AuthURL(p *Provider, clientID string, scopes []string) string {
	if len(scopes) == 0 {
		// openid is required; profile and email are what make a person's name and address available for display and for
		// matching an existing account. groups is not requested by default because providers differ on whether it is a
		// scope or always present, and asking for one a provider does not know is an error at sign-in.
		scopes = []string{"openid", "profile", "email"}
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", a.RedirectURI)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", a.State)
	q.Set("nonce", a.Nonce)
	q.Set("code_challenge", a.Challenge())
	q.Set("code_challenge_method", "S256")

	separator := "?"
	if strings.Contains(p.AuthURL, "?") {
		separator = "&"
	}

	return p.AuthURL + separator + q.Encode()
}

// TokenResponse is what the token endpoint returned.
type TokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`

	// RefreshToken is deliberately ignored beyond being decoded.
	//
	// Perfuse issues its own session and does not act on the person's behalf against anything else, so holding a refresh
	// token would mean storing a long-lived credential for no purpose it can serve.
	RefreshToken string `json:"refresh_token"`
}

// Exchange trades an authorization code for tokens.
func Exchange(ctx context.Context, client *http.Client, p *Provider, clientID, clientSecret, code string,
	req *AuthRequest) (*TokenResponse, error) {

	if client == nil {
		client = DefaultHTTPClient
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", req.RedirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", req.Verifier)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")

	if clientSecret != "" {
		// Basic authentication rather than a form field. Both are allowed and providers vary in which they accept, but
		// basic is the one the specification says a provider must support, so it is the one that always works.
		httpReq.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("oidc: could not reach the token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		// The provider's own error, which is the useful part: "invalid_client" means the secret is wrong and
		// "invalid_grant" means the code was already used, and those send somebody to entirely different places.
		var oauthErr struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &oauthErr) == nil && oauthErr.Error != "" {
			detail := oauthErr.Error
			if oauthErr.Description != "" {
				detail += ": " + oauthErr.Description
			}
			return nil, fmt.Errorf("oidc: the identity provider refused the code exchange (%s)", detail)
		}
		return nil, fmt.Errorf("oidc: the token endpoint answered %s", resp.Status)
	}

	var out TokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("oidc: the token response could not be read: %w", err)
	}
	if out.IDToken == "" {
		return nil, fmt.Errorf("oidc: the identity provider returned no ID token, so there is nothing that says who " +
			"this is; check that the openid scope is being requested")
	}

	return &out, nil
}

// randomString returns n bytes of randomness, base64url encoded.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oidc: no randomness available: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
