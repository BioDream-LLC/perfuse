package fhirserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/authlimit"
)

// Authenticator decides whether a request may proceed.
//
// An interface rather than a concrete type, because a FHIR server is reached by three quite different callers: a person
// with a console session, another system holding a long-lived token, and a SMART app holding a scoped access token from
// an authorization server. They are not variations on one check.
type Authenticator interface {
	// Authenticate returns the caller's identity, or an error explaining the refusal.
	Authenticate(r *http.Request) (*Caller, error)

	// Describe names the scheme for the capability statement and the startup log. Never a credential.
	Describe() string
}

// Caller is an authenticated client.
type Caller struct {
	// Name identifies the caller in the audit log.
	Name string

	// Scopes are the SMART scopes granted, empty when the scheme does not use them.
	Scopes []string

	// Write reports whether this caller may change data.
	Write bool

	// AllScopes reports that scope checking does not apply, for a token that predates SMART.
	AllScopes bool

	// Patient is the launch context: the one patient this token may see, empty when unrestricted.
	//
	// Separate from the scopes because it answers a different question. Scopes say what kinds of resource may be
	// touched; this says whose. Enforcing the first without the second gives an app scoped to
	// patient/Observation.read every observation in the hospital.
	Patient string

	// Encounter is the launch context narrowing to one encounter, empty when unrestricted.
	//
	// A second and narrower restriction than Patient, not a replacement for it. An app launched from a specific visit
	// should see that visit, and a token carrying an encounter but no patient is not a licence to read every patient's
	// encounter of that id - so the two are enforced together and the patient check is never skipped because an encounter
	// was present.
	Encounter string
}

// ErrNoCredentials means the request carried none.
//
// Distinguished from a bad credential so the response can be a challenge rather than a flat refusal, and so the log can
// tell "somebody forgot the header" from "somebody is trying tokens".
var ErrNoCredentials = errors.New("this request carried no credentials")

// TokenLookup checks a bearer token and returns the caller's role.
//
// A function rather than the store type, so this package does not depend on the store - and so a test can supply one
// without a database.
type TokenLookup func(ctx context.Context, token string) (name string, role string, err error)

// BearerAuth authenticates with a bearer token held by Perfuse.
type BearerAuth struct {
	// Lookup checks the token.
	Lookup TokenLookup

	// Log records failures.
	Log *slog.Logger

	// attempts throttles guessing.
	attempts *authlimit.Limiter
	once     sync.Once
}

// Authenticate checks the Authorization header.
func (b *BearerAuth) Authenticate(r *http.Request) (*Caller, error) {
	b.once.Do(func() { b.attempts = authlimit.New() })

	token, err := bearerToken(r)
	if err != nil {
		return nil, err
	}

	ip := clientIP(r)
	if wait, limited := b.attempts.Blocked(ip); limited {
		// Throttled by source address. A FHIR server holds every patient record in reach of one token, so an
		// unlimited guessing rate is the difference between a token being hard to guess and being guessable
		// overnight.
		return nil, fmt.Errorf("too many failed attempts from this address; wait %s", wait.Round(time.Second))
	}

	name, role, err := b.Lookup(r.Context(), token)
	if err != nil {
		b.attempts.Failed(ip)
		if b.Log != nil {
			// The token is never logged, not even a prefix of it. A log is copied into tickets and pasted into
			// chat, and a prefix narrows a search enough to matter.
			b.Log.Warn("a FHIR request presented a token that was refused", "ip", ip)
		}
		return nil, errors.New("this token is not valid")
	}

	b.attempts.Succeeded(ip)

	return &Caller{
		Name: name,
		// Read for a viewer, write for anything above it. The same role names the console uses, so there is one
		// place where "what may this role do" is decided.
		Write:     role != "viewer",
		AllScopes: true,
	}, nil
}

// Describe names the scheme.
func (b *BearerAuth) Describe() string { return "a Perfuse API token as a bearer token" }

// bearerToken pulls a token out of the Authorization header.
func bearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", ErrNoCredentials
	}

	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		// Named specifically. Basic authentication here is a common mistake and the resulting "not valid" would
		// send somebody looking for a wrong password rather than a wrong scheme.
		return "", errors.New("the Authorization header is not a bearer token")
	}

	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", ErrNoCredentials
	}

	return token, nil
}

// StaticAuth authenticates against tokens given on the command line.
//
// For a server run without a database - conversion pipelines and test harnesses - where the alternative would be no
// authentication at all.
type StaticAuth struct {
	// Tokens maps a token to the name it is known by.
	Tokens map[string]string

	// ReadOnly refuses writes for every caller.
	ReadOnly bool

	// Log records failures.
	Log *slog.Logger

	attempts *authlimit.Limiter
	once     sync.Once
}

// Authenticate checks the token against the configured set.
func (s *StaticAuth) Authenticate(r *http.Request) (*Caller, error) {
	s.once.Do(func() { s.attempts = authlimit.New() })

	token, err := bearerToken(r)
	if err != nil {
		return nil, err
	}

	ip := clientIP(r)
	if wait, limited := s.attempts.Blocked(ip); limited {
		return nil, fmt.Errorf("too many failed attempts from this address; wait %s", wait.Round(time.Second))
	}

	// Compared in constant time against every configured token.
	//
	// A map lookup would be faster and would leak: the time to fail differs measurably between a token sharing a
	// long prefix with a real one and a token that does not. Iterating over all of them and accumulating the result
	// means the work does not depend on how close a guess was.
	name := ""
	found := false
	for candidate, label := range s.Tokens {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			name = label
			found = true
		}
	}

	if !found {
		s.attempts.Failed(ip)
		if s.Log != nil {
			s.Log.Warn("a FHIR request presented a token that was refused", "ip", ip)
		}
		return nil, errors.New("this token is not valid")
	}

	s.attempts.Succeeded(ip)

	return &Caller{Name: name, Write: !s.ReadOnly, AllScopes: true}, nil
}

// Describe names the scheme.
func (s *StaticAuth) Describe() string {
	if s.ReadOnly {
		return fmt.Sprintf("one of %d configured tokens, read only", len(s.Tokens))
	}
	return fmt.Sprintf("one of %d configured tokens", len(s.Tokens))
}

// OpenAuth allows every request.
//
// It exists so that running without authentication is a named thing that appears in a startup log and a capability
// statement, rather than the absence of a check. An unauthenticated FHIR server was the state of this program until
// tonight, and what made it dangerous was not that it was allowed but that nothing said so.
type OpenAuth struct{}

// Authenticate allows the request.
func (OpenAuth) Authenticate(*http.Request) (*Caller, error) {
	return &Caller{Name: "anonymous", Write: true, AllScopes: true}, nil
}

// Describe names the scheme.
func (OpenAuth) Describe() string { return "nothing; this server is open to anyone who can reach it" }

// clientIP is the caller's address, preferring a proxy header only when one is trusted.
//
// X-Forwarded-For is deliberately not read. Anybody can send it, so throttling by it means a guesser sets a different
// value on each request and is never throttled at all. A deployment behind a proxy needs the proxy's own rate limiting,
// and that tradeoff is written down rather than papered over with a header nobody can trust.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// requireAuth wraps a handler with authentication.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Auth == nil {
			// Refused rather than allowed. A nil authenticator is a wiring mistake, and the safe reading of a
			// mistake on this path is "nobody gets in" rather than "everybody does".
			s.authFailure(w, r, http.StatusInternalServerError,
				"security", "this server has no authenticator configured, so no request can be allowed")
			return
		}

		caller, err := s.Auth.Authenticate(r)
		if err != nil {
			status := http.StatusUnauthorized
			code := "login"
			if !errors.Is(err, ErrNoCredentials) {
				code = "security"
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="perfuse"`)
			s.authFailure(w, r, status, code, err.Error())
			return
		}

		// Writes are refused for a read-only caller before the request body is even parsed.
		if !caller.Write && isWrite(r.Method) {
			s.authFailure(w, r, http.StatusForbidden, "forbidden",
				"this token may read but not change data")
			return
		}

		// And the caller's scopes are checked against the resource type being addressed.
		//
		// This was the whole point of parsing SMART scopes and it was not done. Caller.Allows existed, was
		// correct, and was called from nowhere - so a token granted patient/Observation.read could read and
		// write every Patient on the server. The scopes were recorded, reported in the audit log, and enforced
		// nowhere, which is worse than not having them: an authorization server issuing a narrow scope, and an
		// operator reading a log that shows it, would both have been misled.
		//
		// Enforced here rather than in each handler for the reason the whole mux is wrapped: a route added later
		// is covered by default. Doing it per handler is how one gets forgotten, and the one that gets forgotten
		// is the one nobody tests.
		if rt := resourceTypeOf(r); rt != "" && !caller.Allows(rt, isWrite(r.Method)) {
			verb := "read"
			if isWrite(r.Method) {
				verb = "change"
			}
			// The refusal names the resource type and what was attempted, and does not list the scopes the
			// caller does hold. A client cannot fix its own grant, and enumerating what a token can reach
			// helps somebody probing with a stolen one more than it helps its owner.
			s.authFailure(w, r, http.StatusForbidden, "forbidden",
				fmt.Sprintf("this token is not scoped to %s %s resources", verb, rt))
			return
		}

		next.ServeHTTP(w, r.WithContext(withCaller(r.Context(), caller)))
	})
}

// resourceTypeOf returns the FHIR resource type a request addresses, or empty when it addresses none.
//
// Empty for /metadata, for the root, and for a bundle posted to the root - none of which is a single resource type. A
// transaction bundle is deliberately not scope-checked here: it can contain entries for several types, and checking the
// envelope would either refuse a legitimate bundle or - much worse - pass one whose entries were never checked at all.
// That is recorded as a caveat rather than papered over, because a scope check that appears to cover transactions and does
// not is the kind of gap somebody relies on.
//
// Read from the path rather than from a mux pattern value, because this runs before the mux has matched anything. The
// wrapping order is deliberate: authenticate first so an unauthenticated caller cannot learn which routes exist.
func resourceTypeOf(r *http.Request) string {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || path == "metadata" {
		return ""
	}

	if slash := strings.Index(path, "/"); slash >= 0 {
		path = path[:slash]
	}

	// A FHIR resource type is upper camel case. Anything else - an operation like $export, or a .well-known path -
	// is not a resource type, and treating it as one would invent a scope requirement for a name no authorization
	// server would ever grant.
	if path == "" || path[0] < 'A' || path[0] > 'Z' {
		return ""
	}

	return path
}

// isWrite reports whether a method changes data.
func isWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		// Everything else, including anything new. Defaulting an unrecognised method to read would mean a method
		// nobody thought about is treated as harmless.
		return true
	}
}

// callerKey carries the authenticated caller.
type callerKey struct{}

func withCaller(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the authenticated caller.
func CallerFrom(ctx context.Context) *Caller {
	c, _ := ctx.Value(callerKey{}).(*Caller)
	return c
}

// authFailure writes an OperationOutcome.
//
// FHIR's own error shape rather than a plain message, because a FHIR client parses this and a bare string leaves it
// reporting "unexpected response" instead of the reason.
func (s *Server) authFailure(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	if s.Log != nil {
		s.Log.Warn("a FHIR request was refused",
			"status", status, "reason", detail, "method", r.Method, "path", r.URL.Path, "ip", clientIP(r))
	}

	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"resourceType": "OperationOutcome",
		"issue": []map[string]any{{
			"severity":    "error",
			"code":        code,
			"diagnostics": detail,
		}},
	})
}

// SMART scope handling.

// ScopeGrant is what a scope allows on one resource type.
type ScopeGrant struct {
	Read  bool
	Write bool
}

// parseSMARTScopes turns a scope string into per-resource grants.
//
// SMART scopes look like patient/Observation.read, user/*.write, or the newer patient/Condition.rs form where the
// letters after the dot are chosen from cruds. Both spellings are accepted because version 1 and version 2 of the SMART
// App Launch specification differ here, and an authorization server may issue either.
func parseSMARTScopes(scopes []string) map[string]ScopeGrant {
	out := map[string]ScopeGrant{}

	for _, scope := range scopes {
		slash := strings.Index(scope, "/")
		if slash < 0 {
			// openid, profile, fhirUser, launch and offline_access have no resource part. Skipped rather than
			// refused: they are legitimate scopes that simply grant no FHIR access.
			continue
		}

		context := scope[:slash]
		switch context {
		case "patient", "user", "system":
		default:
			continue
		}

		rest := scope[slash+1:]
		dot := strings.LastIndex(rest, ".")
		if dot < 0 {
			continue
		}

		resource := rest[:dot]
		actions := rest[dot+1:]

		grant := out[resource]

		switch actions {
		case "read":
			grant.Read = true
		case "write":
			grant.Write = true
		case "*":
			grant.Read = true
			grant.Write = true
		default:
			// The version 2 letter form. c, u, d and the older write all imply changing data; r and s imply
			// reading it.
			for _, c := range actions {
				switch c {
				case 'r', 's':
					grant.Read = true
				case 'c', 'u', 'd':
					grant.Write = true
				}
			}
		}

		out[resource] = grant
	}

	return out
}

// Allows reports whether a caller may act on a resource type.
func (c *Caller) Allows(resourceType string, write bool) bool {
	if c == nil {
		return false
	}
	if c.AllScopes {
		return !write || c.Write
	}

	grants := parseSMARTScopes(c.Scopes)

	// The wildcard first, then the specific type. A caller with both patient/*.read and patient/Observation.write
	// should be able to do each of them.
	for _, key := range []string{"*", resourceType} {
		g, ok := grants[key]
		if !ok {
			continue
		}
		if write && g.Write {
			return true
		}
		if !write && g.Read {
			return true
		}
	}

	return false
}

// ScopeSummary lists the granted scopes in a stable order, for a log line.
func (c *Caller) ScopeSummary() string {
	if c == nil {
		return ""
	}
	if c.AllScopes {
		return "all"
	}
	out := append([]string(nil), c.Scopes...)
	// Sorted, because Go maps range randomly and an audit line that reorders between requests cannot be compared.
	sort.Strings(out)
	return strings.Join(out, " ")
}
