package vpn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AzureSource reads an Azure VPN Gateway connection (Microsoft.Network/connections) from Azure Resource Manager, with an
// Entra ID app's client credentials. The app needs Reader on the connection.
type AzureSource struct {
	Subscription  string `yaml:"subscription"`
	ResourceGroup string `yaml:"resource_group"`
	Connection    string `yaml:"connection"`
	TenantID      string `yaml:"tenant_id"`
	ClientID      string `yaml:"client_id"`
	// ClientSecretEnv names the environment variable holding the app's secret.
	ClientSecretEnv string `yaml:"client_secret_env"`
	// ManagementURL and TokenURL override https://management.azure.com and Entra's token endpoint, for a sovereign cloud or a
	// test.
	ManagementURL string `yaml:"management_url,omitempty"`
	TokenURL      string `yaml:"token_url,omitempty"`
}

type azureConnection struct {
	Properties struct {
		Status        string `json:"connectionStatus"`
		Protocol      string `json:"connectionProtocol"`
		Provisioning  string `json:"provisioningState"`
		TunnelsStatus []struct {
			Tunnel      string `json:"tunnel"`
			Status      string `json:"connectionStatus"`
			Established string `json:"lastConnectionEstablishedUtcTime"`
		} `json:"tunnelConnectionStatus"`
		Policies []struct {
			SALife   int    `json:"saLifeTimeSeconds"`
			IPsecEnc string `json:"ipsecEncryption"`
			IPsecInt string `json:"ipsecIntegrity"`
			IKEEnc   string `json:"ikeEncryption"`
			IKEInt   string `json:"ikeIntegrity"`
			DHGroup  string `json:"dhGroup"`
			PFSGroup string `json:"pfsGroup"`
		} `json:"ipsecPolicies"`
	} `json:"properties"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var azureTokens struct {
	sync.Mutex
	m map[string]azureToken
}

type azureToken struct {
	value   string
	expires time.Time
}

func (m *Monitor) azureToken(ctx context.Context, a *AzureSource) (string, error) {
	key := a.TenantID + " " + a.ClientID
	azureTokens.Lock()
	if t, ok := azureTokens.m[key]; ok && m.now().Before(t.expires) {
		azureTokens.Unlock()
		return t.value, nil
	}
	azureTokens.Unlock()
	secret := os.Getenv(a.ClientSecretEnv)
	var missing []string
	for _, f := range []struct{ name, v string }{{"tenant_id", a.TenantID}, {"client_id", a.ClientID}, {"client_secret_env", a.ClientSecretEnv}} {
		if f.v == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("no Entra ID credentials: set %s in the -vpn file (an app with Reader on the connection)", strings.Join(missing, ", "))
	}
	if secret == "" {
		// The usual case once the file is right: the service was started without the variable in its environment.
		return "", fmt.Errorf("no Entra ID client secret: the environment variable %s, named by client_secret_env, is not set", a.ClientSecretEnv)
	}
	tokenURL := a.TokenURL
	if tokenURL == "" {
		tokenURL = "https://login.microsoftonline.com/" + url.PathEscape(a.TenantID) + "/oauth2/v2.0/token"
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {a.ClientID}, "client_secret": {secret},
		"scope": {strings.TrimRight(a.management(), "/") + "/.default"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, status, err := m.fetch(req)
	if err != nil {
		return "", fmt.Errorf("Entra ID could not be reached: %w", err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tok)
	if status != http.StatusOK || tok.AccessToken == "" {
		msg := tok.Error
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		return "", fmt.Errorf("Entra ID refused the app's credentials (%d): %s", status, strings.TrimSpace(msg))
	}
	life := 3000
	switch v := tok.ExpiresIn.(type) {
	case float64:
		life = int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			life = n
		}
	}
	azureTokens.Lock()
	if azureTokens.m == nil {
		azureTokens.m = map[string]azureToken{}
	}
	azureTokens.m[key] = azureToken{value: tok.AccessToken, expires: m.now().Add(time.Duration(life-120) * time.Second)}
	azureTokens.Unlock()
	return tok.AccessToken, nil
}

func (a *AzureSource) management() string {
	if a.ManagementURL != "" {
		return a.ManagementURL
	}
	return "https://management.azure.com"
}

func (m *Monitor) readAzure(ctx context.Context, t *Tunnel, st *Status) error {
	a := t.Azure
	token, err := m.azureToken(ctx, a)
	if err != nil {
		return err
	}
	u := strings.TrimRight(a.management(), "/") + "/subscriptions/" + url.PathEscape(a.Subscription) + "/resourceGroups/" +
		url.PathEscape(a.ResourceGroup) + "/providers/Microsoft.Network/connections/" + url.PathEscape(a.Connection) + "?api-version=2023-09-01"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	body, status, err := m.fetch(req)
	if err != nil {
		return fmt.Errorf("Azure could not be reached: %w", err)
	}
	var c azureConnection
	if err := json.Unmarshal(body, &c); err != nil {
		return fmt.Errorf("Azure's answer could not be read (%d)", status)
	}
	if status != http.StatusOK {
		if c.Error != nil {
			return fmt.Errorf("Azure refused: %s: %s", c.Error.Code, c.Error.Message)
		}
		return fmt.Errorf("Azure answered %d", status)
	}
	p := c.Properties
	for _, tun := range p.TunnelsStatus {
		since, _ := time.Parse(time.RFC3339, tun.Established)
		st.Endpoints = append(st.Endpoints, Endpoint{Address: tun.Tunnel, Up: tun.Status == "Connected", Since: since, Message: tun.Status})
	}
	if len(st.Endpoints) == 0 {
		// A gateway that is not active-active reports one status for the connection.
		st.Endpoints = []Endpoint{{Address: a.Connection, Up: p.Status == "Connected", Message: p.Status}}
	}
	if p.Status != "" && p.Status != "Connected" && len(p.TunnelsStatus) == 0 {
		st.State, st.Detail = Down, "The connection is "+p.Status+"."
	}
	if p.Protocol != "" {
		st.Reported.IKEVersions = []string{p.Protocol}
	}
	if len(p.Policies) > 0 {
		pol := p.Policies[0]
		st.Reported.Phase1Encryption, st.Reported.Phase1Integrity = []string{pol.IKEEnc}, []string{pol.IKEInt}
		st.Reported.Phase1DHGroups = []string{pol.DHGroup}
		st.Reported.Phase2Encryption, st.Reported.Phase2Integrity = []string{pol.IPsecEnc}, []string{pol.IPsecInt}
		if pol.PFSGroup != "" && pol.PFSGroup != "None" {
			st.Reported.Phase2DHGroups = []string{pol.PFSGroup}
		}
		st.Reported.Phase2Lifetime = pol.SALife
	}
	return nil
}
