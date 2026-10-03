package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The client-credentials preset against Keycloak, a real authorisation server: a confidential client is created through Keycloak's
// admin API, and the token Perfuse fetches is checked to be Keycloak's, issued to that client. Skipped without Keycloak.
func TestClientCredentialsAgainstKeycloak(t *testing.T) {
	base := "http://127.0.0.1:8080"
	res, err := http.Get(base + "/realms/master")
	if err != nil {
		t.Skip("no Keycloak on 127.0.0.1:8080; start it with ./scripts/interop-up.sh")
	}
	_ = res.Body.Close()

	admin := func() string {
		r, err := http.PostForm(base+"/realms/master/protocol/openid-connect/token", url.Values{"grant_type": {"password"},
			"client_id": {"admin-cli"}, "username": {"admin"}, "password": {"admin"}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		var tr struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&tr)
		return tr.AccessToken
	}()
	clientID := fmt.Sprintf("perfuse-fhir-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"clientId":%q,"enabled":true,"publicClient":false,"serviceAccountsEnabled":true,"secret":"perfuse-secret",
		"standardFlowEnabled":false}`, clientID)
	req, _ := http.NewRequest(http.MethodPost, base+"/admin/realms/master/clients", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil || r.StatusCode != http.StatusCreated {
		t.Fatalf("creating the client: %v %v", err, r.Status)
	}
	_ = r.Body.Close()

	a := newFHIRAuthorizer(&config.FHIRAuth{Type: "client_credentials", TokenURL: base + "/realms/master/protocol/openid-connect/token",
		ClientID: clientID, ClientSecret: "perfuse-secret"}, "https://fhir.example", &http.Client{Timeout: 10 * time.Second})
	out, _ := http.NewRequest(http.MethodPost, "https://fhir.example/", nil)
	if err := a.authorize(context.Background(), out, nil); err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimPrefix(out.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", tok)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss string `json:"iss"`
		Azp string `json:"azp"`
	}
	_ = json.Unmarshal(claims, &c)
	if c.Iss != base+"/realms/master" || c.Azp != clientID {
		t.Errorf("the token is not Keycloak's for this client: %s", claims)
	}

	wrong := newFHIRAuthorizer(&config.FHIRAuth{Type: "client_credentials", TokenURL: base + "/realms/master/protocol/openid-connect/token",
		ClientID: clientID, ClientSecret: "not-it"}, "https://fhir.example", &http.Client{Timeout: 10 * time.Second})
	if err := wrong.authorize(context.Background(), out, nil); err == nil || !strings.Contains(err.Error(), "unauthorized_client") &&
		!strings.Contains(err.Error(), "invalid_client") {
		t.Errorf("a wrong secret: %v", err)
	}
}
