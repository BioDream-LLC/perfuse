// Package gcp talks to Google Cloud Pub/Sub and Cloud Storage over their REST APIs, with Google's OAuth 2.0 done here: a
// service account key signs a JWT (RS256, crypto/rsa) that is traded for an access token, or, on Google Cloud itself, the
// metadata server hands one out. No Google SDK.
//
// Emulators (the Pub/Sub emulator, fake-gcs-server) take no credentials: set Endpoint and leave the credentials empty.
package gcp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Scopes the clients ask for.
const (
	ScopePubSub  = "https://www.googleapis.com/auth/pubsub"
	ScopeStorage = "https://www.googleapis.com/auth/devstorage.read_write"
)

// metadataTokenURL is where a workload on Google Cloud gets its attached service account's token.
const metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"

// ServiceAccountKey is the JSON key file Google issues for a service account (the fields used here).
type ServiceAccountKey struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	TokenURI     string `json:"token_uri"`
}

// Credentials yields access tokens. The zero value is anonymous, for an emulator.
type Credentials struct {
	key      *ServiceAccountKey
	rsa      *rsa.PrivateKey
	metadata bool
	scope    string
	client   *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// FromKeyJSON reads a service account key.
func FromKeyJSON(data []byte, scope string) (*Credentials, error) {
	var k ServiceAccountKey
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("gcp: the service account key is not valid JSON: %w", err)
	}
	if k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, errors.New("gcp: the key is not a service account key (type service_account, client_email and private_key)")
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return nil, errors.New("gcp: the service account's private_key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if pk1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes); err1 == nil {
			parsed = pk1
		} else {
			return nil, fmt.Errorf("gcp: reading the service account's private key: %w", err)
		}
	}
	rk, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("gcp: the service account's private key is not RSA")
	}
	return &Credentials{key: &k, rsa: rk, scope: scope, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

// FromMetadata uses the service account attached to the Compute Engine VM, GKE pod or Cloud Run service this runs on.
func FromMetadata() *Credentials {
	return &Credentials{metadata: true, client: &http.Client{Timeout: 10 * time.Second}}
}

// Anonymous reports whether these credentials send no token, as for an emulator.
func (c *Credentials) Anonymous() bool { return c == nil || (c.key == nil && !c.metadata) }

// Token returns a valid access token, fetching a new one a minute before the old one expires.
func (c *Credentials) Token(ctx context.Context) (string, error) {
	if c.Anonymous() {
		return "", nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires.Add(-time.Minute)) {
		return c.token, nil
	}
	var req *http.Request
	var err error
	if c.metadata {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, metadataTokenURL, nil)
		if err == nil {
			req.Header.Set("Metadata-Flavor", "Google")
		}
	} else {
		var assertion string
		assertion, err = c.assertion(time.Now())
		if err != nil {
			return "", err
		}
		form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.key.TokenURI, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gcp: getting an access token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gcp: the token endpoint answered %d: %s", resp.StatusCode, firstLine(body))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", errors.New("gcp: the token endpoint's answer held no access_token")
	}
	c.token, c.expires = tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second)
	return c.token, nil
}

// assertion is the signed JWT a service account trades for a token.
func (c *Credentials) assertion(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": c.key.PrivateKeyID})
	claims, _ := json.Marshal(map[string]any{"iss": c.key.ClientEmail, "scope": c.scope, "aud": c.key.TokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.rsa, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("gcp: signing the token request: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// APIError is an error answer from a Google API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("gcp: %d %s", e.Status, e.Message) }

// do sends one request with the token, and decodes a JSON answer into out when out is not nil.
func do(ctx context.Context, hc *http.Client, creds *Credentials, method, u, contentType string, body []byte, out any) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	tok, err := creds.Token(ctx)
	if err != nil {
		return nil, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gcp: %s %s: %w", method, redact(u), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := firstLine(data)
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("gcp: reading the answer to %s %s: %w", method, redact(u), err)
		}
	}
	return data, nil
}

func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
