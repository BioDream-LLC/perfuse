package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth providers with known token endpoints and scopes.
const (
	ProviderMicrosoft = "microsoft"
	ProviderGoogle    = "google"
	ProviderCustom    = "custom"
)

// OAuth gets access tokens for XOAUTH2 sign-in, by refresh token (a mailbox a person consented to) or by client credentials
// (Microsoft 365 app-only access, where an administrator granted the app access to one mailbox).
type OAuth struct {
	Provider string
	// Tenant is the Microsoft Entra tenant (an id or a domain); "organizations" when empty.
	Tenant       string
	TokenURL     string
	ClientID     string
	ClientSecret string
	// RefreshToken selects the refresh-token grant; empty selects client credentials.
	RefreshToken string
	// Scope overrides the provider's default.
	Scope string
	// Protocol is imap or pop3, which decides Microsoft's default scope.
	Protocol string
	Client   *http.Client

	mu      sync.Mutex
	access  string
	expires time.Time
}

// Endpoint is the token URL in use.
func (o *OAuth) Endpoint() string {
	if o.TokenURL != "" {
		return o.TokenURL
	}
	switch o.Provider {
	case ProviderMicrosoft:
		t := o.Tenant
		if t == "" {
			t = "organizations"
		}
		return "https://login.microsoftonline.com/" + url.PathEscape(t) + "/oauth2/v2.0/token"
	case ProviderGoogle:
		return "https://oauth2.googleapis.com/token"
	}
	return ""
}

// DefaultScope is what the provider wants for this protocol and grant.
func (o *OAuth) DefaultScope() string {
	if o.Scope != "" {
		return o.Scope
	}
	switch o.Provider {
	case ProviderMicrosoft:
		if o.RefreshToken == "" {
			return "https://outlook.office365.com/.default"
		}
		if strings.EqualFold(o.Protocol, "pop3") {
			return "https://outlook.office.com/POP.AccessAsUser.All offline_access"
		}
		return "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"
	case ProviderGoogle:
		return "https://mail.google.com/"
	}
	return ""
}

// Validate says what is missing.
func (o *OAuth) Validate() error {
	switch o.Provider {
	case ProviderMicrosoft, ProviderGoogle, ProviderCustom:
	default:
		return fmt.Errorf("oauth.provider %q is not known; use microsoft, google or custom", o.Provider)
	}
	if o.Provider == ProviderCustom && o.TokenURL == "" {
		return errors.New("oauth.token_url is required for a custom provider")
	}
	if o.TokenURL != "" {
		u, err := url.Parse(o.TokenURL)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
			return errors.New("oauth.token_url must be https (http only to this machine, for a test)")
		}
	}
	if o.ClientID == "" {
		return errors.New("oauth.client_id is required")
	}
	if o.RefreshToken == "" && o.ClientSecret == "" {
		return errors.New("oauth needs a refresh_token (a person's consent) or a client_secret (Microsoft 365 app-only access)")
	}
	if o.Provider == ProviderGoogle && o.RefreshToken == "" {
		return errors.New("Gmail needs oauth.refresh_token: Google offers no app-only access to a mailbox")
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Token returns a current access token, fetching a new one a minute before the old one expires.
func (o *OAuth) Token(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.access != "" && time.Until(o.expires) > time.Minute {
		return o.access, nil
	}
	form := url.Values{"client_id": {o.ClientID}}
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}
	if o.RefreshToken != "" {
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", o.RefreshToken)
	} else {
		form.Set("grant_type", "client_credentials")
	}
	if s := o.DefaultScope(); s != "" {
		form.Set("scope", s)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := o.Client
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		why := tr.Error
		if tr.Description != "" {
			why += ": " + tr.Description
		}
		if why == "" {
			why = resp.Status
		}
		return "", fmt.Errorf("the token endpoint refused: %s", clip(why))
	}
	o.access = tr.AccessToken
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	o.expires = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	// A provider may rotate the refresh token (Microsoft does); keep the new one for this process's life.
	if tr.RefreshToken != "" {
		o.RefreshToken = tr.RefreshToken
	}
	return o.access, nil
}
