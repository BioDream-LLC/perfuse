package smartauth

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// restart is the same server configuration started again on the same database: a new Server, new memory, the same Grants.
func restart(f *fixture) *fixture {
	old := f.srv
	srv := &Server{Issuer: old.Issuer, Audience: old.Audience, Key: old.Key, Clients: old.Clients, Users: old.Users,
		Patients: old.Patients, PatientExists: old.PatientExists, Grants: old.Grants}
	return &fixture{srv: srv, clientKey: f.clientKey, h: srv.Handler()}
}

func TestCodesRefreshTokensAndRevocationsSurviveARestart(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := userFixture(t)
	f.srv.Grants = db.SMARTGrants()

	// A code issued before the restart is exchanged after it, once.
	b, req := signIn(t, f, "amy", "openid fhirUser launch/patient offline_access patient/*.rs")
	q := codeFrom(t, b.do(http.MethodPost, "/consent", url.Values{"req": {req}, "action": {"allow"}, "scope": {"patient/*.rs", "offline_access"}}))
	f = restart(f)
	code, tok := exchange(f, q.Get("code"), verifier)
	if code != 200 || tok["refresh_token"] == nil {
		t.Fatalf("the code did not survive the restart: %d %v", code, tok)
	}
	f = restart(f)
	if code, _ := exchange(f, q.Get("code"), verifier); code != 400 {
		t.Errorf("a code was exchanged twice across a restart: %d", code)
	}

	// The refresh token still works after another restart.
	refresh := tok["refresh_token"].(string)
	f = restart(f)
	if code, body := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"app"}}); code != 200 || body["patient"] != "p1" {
		t.Fatalf("refresh after restart: %d %v", code, body)
	}

	// A revocation is not forgotten by a restart.
	access := tok["access_token"].(string)
	c, ok := f.srv.verifyOwn(context.Background(), access)
	if !ok {
		t.Fatal("own token not verified")
	}
	if code, _ := f.post("/revoke", url.Values{"token": {access}, "client_id": {"app"}}, "", ""); code != 200 {
		t.Fatalf("revoke %d", code)
	}
	f.post("/revoke", url.Values{"token": {refresh}, "client_id": {"app"}}, "", "")
	f = restart(f)
	if !f.srv.Revoked(c.JTI) {
		t.Error("the revocation was forgotten on restart")
	}
	if code, _ := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"app"}}); code != 400 {
		t.Errorf("a revoked refresh token worked after a restart: %d", code)
	}

	// Nothing stored is a code or token that could be presented.
	rows, err := db.DB().Query(`SELECT key, data FROM smart_grants`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, d string
		_ = rows.Scan(&k, &d)
		for _, secret := range []string{q.Get("code"), refresh, access, c.JTI} {
			if strings.Contains(k+d, secret) {
				t.Errorf("a grant row holds a usable secret: %s", k)
			}
		}
	}
}

func TestAClientAssertionIsUsedOnceAcrossARestart(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := newFixture(t)
	f.srv.Grants = db.SMARTGrants()
	a := f.assertion(t, nil)
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"system/*.rs"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}, "client_assertion": {a}}
	if code, body := f.token(form); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	f = restart(f)
	if code, _ := f.token(form); code == 200 {
		t.Error("an assertion was replayed after a restart")
	}
}

func TestExpiredGrantsArePruned(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	g := db.SMARTGrants()
	ctx := context.Background()
	now := time.Now()
	_ = g.Put(ctx, "code", "a", []byte("{}"), now.Add(-time.Second))
	_ = g.Put(ctx, "code", "b", []byte("{}"), now.Add(time.Minute))
	if _, ok, _ := g.Take(ctx, "code", "a", now); ok {
		t.Error("an expired code was taken")
	}
	if fresh, _ := g.Claim(ctx, "jti", "x", now.Add(time.Minute), now); !fresh {
		t.Error("first claim refused")
	}
	if fresh, _ := g.Claim(ctx, "jti", "x", now.Add(time.Minute), now); fresh {
		t.Error("second claim accepted")
	}
	_ = g.Prune(ctx, now.Add(2*time.Minute))
	var n int
	_ = db.DB().QueryRow(`SELECT COUNT(*) FROM smart_grants`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows left after pruning", n)
	}
}
