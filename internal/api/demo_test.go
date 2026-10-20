package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

func TestTheSharedDemoAccountIsShownAndCannotBeTakenOver(t *testing.T) {
	h := newHarness(t)
	demo := &DemoAccount{Username: "demo", Password: "try-perfuse-demo"}
	if err := EnsureDemoAccount(t.Context(), h.store, demo); err != nil {
		t.Fatal(err)
	}
	h.server.Demo = demo

	// The sign-in page is told the account, because a visitor has no other way to learn it.
	rec := h.do("", http.MethodGet, "/api/auth/methods", nil)
	var methods struct {
		Demo *struct{ Username, Password string } `json:"demo"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &methods)
	if methods.Demo == nil || methods.Demo.Username != "demo" || methods.Demo.Password != "try-perfuse-demo" {
		t.Fatalf("auth methods did not carry the demo account: %s", rec.Body)
	}

	token, _, err := h.store.Authenticate(t.Context(), "demo", "try-perfuse-demo", "test", "test")
	if err != nil {
		t.Fatalf("signing in with the published password: %v", err)
	}
	h.tokens["demo"] = token

	if rec := h.do("demo", http.MethodGet, "/api/channels", nil); rec.Code != http.StatusOK {
		t.Errorf("the demo account looking at channels: %d", rec.Code)
	}
	// A viewer: nothing it can change.
	if rec := h.do("demo", http.MethodPost, "/api/channels", map[string]string{"yaml": "name: x"}); rec.Code != http.StatusForbidden {
		t.Errorf("the demo account creating a channel: %d, want 403", rec.Code)
	}
	// It looks at the administrators' screens too, and changes nothing on them.
	if rec := h.do("demo", http.MethodGet, "/api/users", nil); rec.Code != http.StatusOK {
		t.Errorf("the demo account reading users: %d, want 200", rec.Code)
	}
	if rec := h.do("demo", http.MethodGet, "/api/settings", nil); rec.Code != http.StatusOK {
		t.Errorf("the demo account reading settings: %d, want 200", rec.Code)
	}
	// The checks that change nothing work for it, as for an editor; one that would change something does not.
	if rec := h.do("demo", http.MethodPost, "/api/scripts/check", map[string]string{"language": "javascript", "source": "msg"}); rec.Code == http.StatusForbidden {
		t.Errorf("the demo account checking a script: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("demo", http.MethodPost, "/api/channels/validate", map[string]string{"yaml": "name: x"}); rec.Code == http.StatusForbidden {
		t.Errorf("the demo account validating a channel: %d", rec.Code)
	}
	if rec := h.do("demo", http.MethodPost, "/api/users", map[string]string{"username": "x", "password": "long enough password", "role": "admin"}); rec.Code != http.StatusForbidden {
		t.Errorf("the demo account adding a user: %d, want 403", rec.Code)
	}
	var me struct{ Demo bool }
	_ = json.Unmarshal(h.do("demo", http.MethodGet, "/api/me", nil).Body.Bytes(), &me)
	if !me.Demo {
		t.Error("/api/me does not say this is the demo account")
	}
	// An ordinary viewer still cannot.
	if _, err := h.store.CreateUser(t.Context(), "vera", "a viewer password", store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	vt, _, _ := h.store.Authenticate(t.Context(), "vera", "a viewer password", "test", "test")
	h.tokens["vera"] = vt
	if rec := h.do("vera", http.MethodGet, "/api/users", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer reading users: %d, want 403", rec.Code)
	}
	if rec := h.do("vera", http.MethodPost, "/api/scripts/check", map[string]string{"language": "javascript", "source": "msg"}); rec.Code != http.StatusForbidden {
		t.Errorf("an ordinary viewer checking a script: %d, want 403", rec.Code)
	}

	// The first visitor to change the password would lock out everyone else.
	if rec := h.do("demo", http.MethodPost, "/api/me/password",
		map[string]string{"current": "try-perfuse-demo", "new": "mine now, all mine"}); rec.Code != http.StatusForbidden {
		t.Errorf("the demo account changing its password: %d, want 403", rec.Code)
	}
	if _, _, err := h.store.Authenticate(t.Context(), "demo", "try-perfuse-demo", "test", "test"); err != nil {
		t.Errorf("the published password stopped working: %v", err)
	}

	// Whatever an administrator did to it, a restart puts it back.
	u, _ := h.store.GetUser(t.Context(), "demo")
	_ = h.store.SetRole(t.Context(), u.ID, store.RoleAdmin)
	_ = h.store.SetPassword(t.Context(), u.ID, "something else entirely")
	if err := EnsureDemoAccount(t.Context(), h.store, demo); err != nil {
		t.Fatal(err)
	}
	if u, _ = h.store.GetUser(t.Context(), "demo"); u.Role != store.RoleViewer {
		t.Errorf("after a restart the demo account is %s, want viewer", u.Role)
	}
	if _, _, err := h.store.Authenticate(t.Context(), "demo", "try-perfuse-demo", "test", "test"); err != nil {
		t.Errorf("after a restart the published password does not work: %v", err)
	}
}
