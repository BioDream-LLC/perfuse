package smartauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

// Client kinds, as SMART App Launch names them.
const (
	// KindBackend is SMART Backend Services: no person, a signed JWT assertion, client_credentials.
	KindBackend = "backend"
	// KindPublic is an app that cannot keep a secret (in a browser or on a phone): PKCE and nothing else.
	KindPublic = "public"
	// KindSymmetric is a confidential app authenticating with a client secret.
	KindSymmetric = "confidential-symmetric"
	// KindAsymmetric is a confidential app authenticating with a signed JWT assertion.
	KindAsymmetric = "confidential-asymmetric"
)

// Client is one registered app.
type Client struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
	// Scopes are the most this client may be granted, matched exactly; one ending in * covers every scope beginning with
	// what comes before it ("system/*" covers every system scope).
	Scopes []string `yaml:"scopes"`
	// JWKSURI or JWKS hold the client's public keys, for backend and asymmetric clients.
	JWKSURI string         `yaml:"jwks_uri"`
	JWKS    map[string]any `yaml:"jwks"`
	// SecretHash is HashPassword's output for a symmetric client's secret; the secret itself is never stored.
	SecretHash   string   `yaml:"secret_hash"`
	RedirectURIs []string `yaml:"redirect_uris"`
	LaunchURL    string   `yaml:"launch_url"`

	keys *oidc.KeySet
}

// Clients is the registry, keyed by id.
type Clients map[string]*Client

// LoadClients reads the clients file, refusing what would fail later at a token request: a backend client with no keys, a
// public client with a secret, a redirect URI that is not absolute.
func LoadClients(path string) (Clients, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Clients []*Client `yaml:"clients"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := Clients{}
	for i, c := range doc.Clients {
		where := fmt.Sprintf("%s: client %d (%s)", path, i+1, c.ID)
		if c.ID == "" {
			return nil, fmt.Errorf("%s has no id", where)
		}
		if out[c.ID] != nil {
			return nil, fmt.Errorf("%s: the id is used twice", where)
		}
		if len(c.Scopes) == 0 {
			return nil, fmt.Errorf("%s: list the scopes it may be granted", where)
		}
		switch c.Kind {
		case KindBackend, KindAsymmetric:
			if (c.JWKSURI == "") == (c.JWKS == nil) {
				return nil, fmt.Errorf("%s: a %s client needs its public keys, as jwks_uri or jwks (one of them)", where, c.Kind)
			}
			if c.JWKS != nil {
				doc, _ := json.Marshal(c.JWKS)
				if c.keys, err = oidc.NewStaticKeySet(doc); err != nil {
					return nil, fmt.Errorf("%s: jwks: %w", where, err)
				}
			} else {
				if u, err := url.Parse(c.JWKSURI); err != nil || u.Scheme != "https" && !strings.HasPrefix(c.JWKSURI, "http://localhost") &&
					!strings.HasPrefix(c.JWKSURI, "http://127.0.0.1") {
					return nil, fmt.Errorf("%s: jwks_uri must be https", where)
				}
				c.keys = oidc.NewKeySet(c.JWKSURI, nil)
			}
		case KindSymmetric:
			if c.SecretHash == "" {
				return nil, fmt.Errorf("%s: a confidential-symmetric client needs secret_hash, the hash of its secret", where)
			}
		case KindPublic:
			if c.SecretHash != "" || c.JWKSURI != "" || c.JWKS != nil {
				return nil, fmt.Errorf("%s: a public client cannot keep a secret, so it has no secret or keys", where)
			}
		default:
			return nil, fmt.Errorf("%s: kind is backend, public, confidential-symmetric or confidential-asymmetric, not %q", where, c.Kind)
		}
		if c.Kind != KindBackend && len(c.RedirectURIs) == 0 {
			return nil, fmt.Errorf("%s: an app that signs people in needs redirect_uris", where)
		}
		for _, r := range c.RedirectURIs {
			if u, err := url.Parse(r); err != nil || !u.IsAbs() || u.Fragment != "" {
				return nil, fmt.Errorf("%s: redirect URI %q must be absolute, with no fragment", where, r)
			}
		}
		out[c.ID] = c
	}
	return out, nil
}

// allows reports whether the client may be granted a scope.
func (c *Client) allows(scope string) bool {
	for _, s := range c.Scopes {
		if s == scope || strings.HasSuffix(s, "*") && strings.HasPrefix(scope, strings.TrimSuffix(s, "*")) {
			return true
		}
	}
	return false
}

// grant is what of the requested scopes the client may have, in the order asked.
func (c *Client) grant(requested string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range strings.Fields(requested) {
		if !seen[s] && c.allows(s) {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
