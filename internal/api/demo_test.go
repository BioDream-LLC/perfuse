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
