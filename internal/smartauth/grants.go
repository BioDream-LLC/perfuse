package smartauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"
)

// Grants keeps what the authorization server has handed out and must remember: authorization codes, refresh tokens,
// revoked access tokens, used client assertion ids and EHR launch ids. In memory by default; serve gives it the database
// (store.SMARTGrants), so a restart neither signs everyone out nor forgets a revocation.
//
// Keys reach it already hashed (hashKey): the database holds no code or token that could be presented.
type Grants interface {
	Put(ctx context.Context, kind, key string, data []byte, expires time.Time) error
	Get(ctx context.Context, kind, key string, now time.Time) ([]byte, bool, error)
	// Take reads and removes, atomically: a code or launch id works once.
	Take(ctx context.Context, kind, key string, now time.Time) ([]byte, bool, error)
	// Claim stores a key only if no live one is there, reporting whether it did: an assertion id is used once even when two
	// requests race with it.
	Claim(ctx context.Context, kind, key string, expires, now time.Time) (bool, error)
	Delete(ctx context.Context, kind, key string) error
	Prune(ctx context.Context, now time.Time) error
}

const (
	kindCode    = "code"
	kindRefresh = "refresh"
	kindRevoked = "revoked"
	kindJTI     = "assertion-jti"
	kindLaunch  = "launch"
)

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// MemoryGrants is Grants in memory, for tests and for a server with no database.
type MemoryGrants struct {
	mu sync.Mutex
	m  map[string]memoryGrant
}

type memoryGrant struct {
	data    []byte
	expires time.Time
}

func (g *MemoryGrants) Put(_ context.Context, kind, key string, data []byte, expires time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]memoryGrant{}
	}
	g.m[kind+"\x00"+key] = memoryGrant{data: append([]byte(nil), data...), expires: expires}
	return nil
}

func (g *MemoryGrants) Get(_ context.Context, kind, key string, now time.Time) ([]byte, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.m[kind+"\x00"+key]
	if !ok || !e.expires.After(now) {
		return nil, false, nil
	}
	return e.data, true, nil
}

func (g *MemoryGrants) Take(_ context.Context, kind, key string, now time.Time) ([]byte, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.m[kind+"\x00"+key]
	delete(g.m, kind+"\x00"+key)
	if !ok || !e.expires.After(now) {
		return nil, false, nil
	}
	return e.data, true, nil
}

func (g *MemoryGrants) Claim(_ context.Context, kind, key string, expires, now time.Time) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.m[kind+"\x00"+key]; ok && e.expires.After(now) {
		return false, nil
	}
	if g.m == nil {
		g.m = map[string]memoryGrant{}
	}
	g.m[kind+"\x00"+key] = memoryGrant{data: []byte("{}"), expires: expires}
	return true, nil
}

func (g *MemoryGrants) Delete(_ context.Context, kind, key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.m, kind+"\x00"+key)
	return nil
}

func (g *MemoryGrants) Prune(_ context.Context, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, e := range g.m {
		if !e.expires.After(now) {
			delete(g.m, k)
		}
	}
	return nil
}

func (s *Server) grants() Grants {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Grants == nil {
		s.Grants = &MemoryGrants{}
	}
	return s.Grants
}

// storedGrant is a grantRecord as it is kept: the client and user by name, so that one removed from its file since stops
// working, and the user's details for a person who signed in upstream and is in no file.
type storedGrant struct {
	Client    string    `json:"client"`
	Redirect  string    `json:"redirect,omitempty"`
	Challenge string    `json:"challenge,omitempty"`
	Nonce     string    `json:"nonce,omitempty"`
	Scopes    []string  `json:"scopes"`
	Patient   string    `json:"patient,omitempty"`
	Encounter string    `json:"encounter,omitempty"`
	User      string    `json:"user"`
	Upstream  *User     `json:"upstream,omitempty"`
	Expires   time.Time `json:"expires"`
}

func (s *Server) putGrant(ctx context.Context, kind, raw string, g *grantRecord) error {
	sg := storedGrant{Client: g.client.ID, Redirect: g.redirect, Challenge: g.challenge, Nonce: g.nonce, Scopes: g.scopes,
		Patient: g.patient, Encounter: g.encounter, User: g.user.Username, Expires: g.expires}
	data, err := json.Marshal(sg)
	if err != nil {
		return err
	}
	return s.grants().Put(ctx, kind, hashKey(raw), data, g.expires)
}

// grant reads a code (taking it) or a refresh token, and resolves its client and user as they are now.
func (s *Server) grant(ctx context.Context, kind, raw string, take bool) *grantRecord {
	if raw == "" {
		return nil
	}
	read := s.grants().Get
	if take {
		read = s.grants().Take
	}
	data, ok, err := read(ctx, kind, hashKey(raw), s.now())
	if err != nil || !ok {
		return nil
	}
	var sg storedGrant
	if json.Unmarshal(data, &sg) != nil {
		return nil
	}
	client := s.client(sg.Client)
	user := s.user(sg.User)
	if client == nil || user == nil {
		return nil
	}
	return &grantRecord{client: client, redirect: sg.Redirect, challenge: sg.Challenge, nonce: sg.Nonce, scopes: sg.Scopes,
		patient: sg.Patient, encounter: sg.Encounter, user: user, expires: sg.Expires}
}

// mark records that a key was seen (a revoked token, a used assertion id) until it no longer matters.
func (s *Server) mark(ctx context.Context, kind, raw string, until time.Time) error {
	return s.grants().Put(ctx, kind, hashKey(raw), []byte("{}"), until)
}

func (s *Server) marked(ctx context.Context, kind, raw string) bool {
	_, ok, err := s.grants().Get(ctx, kind, hashKey(raw), s.now())
	// A store that cannot answer is treated as having the mark: a revocation is never forgotten by an outage, and a replayed
	// assertion is never accepted because the database was slow.
	return ok || err != nil
}
