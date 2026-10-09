package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/smartauth"
)

func smartHarness(t *testing.T) (*harness, string, string) {
	t.Helper()
	h := newHarness(t)
	dir := t.TempDir()
	clients, users := filepath.Join(dir, "clients.yaml"), filepath.Join(dir, "users.yaml")
	_ = os.WriteFile(clients, []byte("clients:\n  - id: app\n    kind: public\n    redirect_uris: [\"https://app.example/cb\"]\n    scopes: [openid]\n"), 0o600)
	_ = os.WriteFile(users, []byte("users: []\n"), 0o600)
	c, err := smartauth.LoadClients(clients)
	if err != nil {
		t.Fatal(err)
	}
	u, err := smartauth.LoadUsers(users)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := smartauth.LoadOrCreateKey(filepath.Join(dir, "k"))
	h.server.SMART = &smartauth.Server{Issuer: "https://example.test/auth", Audience: "https://example.test/fhir", Key: key, Clients: c, Users: u}
	h.server.SMARTClientsFile, h.server.SMARTUsersFile = clients, users
	h.handler = h.server.Handler()
	return h, clients, users
}

func TestSMARTAppsAndPeopleAreEditedInTheConsole(t *testing.T) {
	h, clientsFile, usersFile := smartHarness(t)

	if rec := h.do("editor", http.MethodGet, "/api/smart", nil); rec.Code != http.StatusForbidden {
		t.Errorf("an editor read the SMART directory: %d", rec.Code)
	}

	// A confidential app with no secret given gets one, shown once.
	rec := h.do("admin", http.MethodPut, "/api/smart/clients/portal", map[string]any{"name": "Portal", "kind": "confidential-symmetric",
		"scopes": []string{"openid", "user/*.rs"}, "redirectUris": []string{"https://portal.example/cb"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var made struct{ Secret string }
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	if len(made.Secret) < 30 {
		t.Errorf("no secret generated: %s", rec.Body)
	}
	if c := h.server.SMART.ClientList(); len(c) != 2 || c[1].ID != "portal" || c[1].SecretHash == "" {
		t.Errorf("the server's clients were not replaced: %+v", c)
	}
	raw, _ := os.ReadFile(clientsFile)
	if !strings.Contains(string(raw), "id: portal") || strings.Contains(string(raw), made.Secret) {
		t.Errorf("clients file: %s", raw)
	}

	// A mistake is refused, and the file is untouched.
	rec = h.do("admin", http.MethodPut, "/api/smart/clients/bad", map[string]any{"kind": "public", "scopes": []string{"openid"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "redirect_uris") {
		t.Errorf("a public app with no redirect URI: %d %s", rec.Code, rec.Body)
	}
	if again, _ := os.ReadFile(clientsFile); string(again) != string(raw) {
		t.Error("a refused save changed the file")
	}

	// People: a password is set, never read back; an upstream link alone is enough.
	if rec := h.do("admin", http.MethodPut, "/api/smart/users/amy", map[string]any{"name": "Amy", "fhirUser": "Patient/p1",
		"password": "a long enough password"}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := h.do("admin", http.MethodPut, "/api/smart/users/carol", map[string]any{"fhirUser": "Practitioner/c1", "oidcSubject": "sub-1"}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := h.do("admin", http.MethodPut, "/api/smart/users/dan", map[string]any{"fhirUser": "Practitioner/d1"}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a person with no way to sign in: %d", rec.Code)
	}
	// Renaming nothing else keeps amy's password.
	h.do("admin", http.MethodPut, "/api/smart/users/amy", map[string]any{"name": "Amy M", "fhirUser": "Patient/p1"})
	rec = h.do("admin", http.MethodGet, "/api/smart", nil)
	if b := rec.Body.String(); !strings.Contains(b, `"username":"amy","name":"Amy M","fhirUser":"Patient/p1","hasPassword":true`) ||
		strings.Contains(b, "pbkdf2") || !strings.Contains(b, `"oidcSubject":"sub-1"`) {
		t.Errorf("directory: %s", b)
	}
	if u, err := smartauth.LoadUsers(usersFile); err != nil || len(u) != 2 {
		t.Errorf("users file: %v %v", u, err)
	}
	if rec := h.do("admin", http.MethodDelete, "/api/smart/users/carol", nil); rec.Code != http.StatusOK || len(h.server.SMART.UserList()) != 1 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := h.do("admin", http.MethodDelete, "/api/smart/clients/portal", nil); rec.Code != http.StatusOK || len(h.server.SMART.ClientList()) != 1 {
		t.Errorf("delete client: %d", rec.Code)
	}
}

func TestTheSMARTDirectorySaysWhenThereIsNoAuthorizationServer(t *testing.T) {
	h := newHarness(t)
	rec := h.do("admin", http.MethodGet, "/api/smart", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
