package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsv4"
	"github.com/biodream-llc/perfuse/internal/config"
)

// Authorising requests to hosted FHIR services: AWS HealthLake by signature, Azure Health Data Services and any OAuth server by a
// client-credentials token, cached until a minute before it expires.

type fhirAuthorizer struct {
	cfg    config.FHIRAuth
	client *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newFHIRAuthorizer(a *config.FHIRAuth, fhirURL string, client *http.Client) *fhirAuthorizer {
	if a == nil {
		return nil
	}
	return &fhirAuthorizer{cfg: a.Resolved(fhirURL), client: client}
}

// authorize adds whatever the service wants to req, whose body is body.
func (a *fhirAuthorizer) authorize(ctx context.Context, req *http.Request, body []byte) error {
	if a == nil {
		return nil
	}
	if a.cfg.Type == "aws" {
		req.Host = req.URL.Host
		awsv4.Sign(req, body, awsv4.Credentials{AccessKeyID: a.cfg.AccessKeyID, SecretAccessKey: a.cfg.SecretAccessKey,
			SessionToken: a.cfg.SessionToken}, a.cfg.Region, "healthlake", time.Now())
		return nil
	}
	tok, err := a.bearer(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

func (a *fhirAuthorizer) bearer(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.expires) {
		return a.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {a.cfg.ClientID}, "client_secret": {a.cfg.ClientSecret}}
	if a.cfg.Scope != "" {
		form.Set("scope", a.cfg.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("the token endpoint could not be reached: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(data, &tr)
	if res.StatusCode != http.StatusOK || tr.AccessToken == "" {
		// The description, never the request: the request holds the client secret.
		return "", fmt.Errorf("the token endpoint refused the client credentials (%s): %s %s", res.Status, tr.Error,
			strings.SplitN(tr.Description, "\n", 2)[0])
	}
	life := time.Duration(tr.ExpiresIn) * time.Second
	if life <= 0 {
		life = 5 * time.Minute
	}
	a.token, a.expires = tr.AccessToken, time.Now().Add(life-time.Minute)
	return a.token, nil
}
