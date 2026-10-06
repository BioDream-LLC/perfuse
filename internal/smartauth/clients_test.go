package smartauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadClientsText(t *testing.T, text string) (Clients, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clients.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadClients(p)
}

func TestTheExampleClientsFileLoads(t *testing.T) {
	c, err := LoadClients("../../examples/smart/clients.yaml")
	if err != nil || c["payer-dtr-client"] == nil {
		t.Fatalf("%v %v", err, c)
	}
}

func TestAClientsFileThatWouldFailLaterIsRefusedNow(t *testing.T) {
	for want, text := range map[string]string{
		"needs its public keys":  "clients: [{id: a, kind: backend, scopes: [system/*.rs]}]",
		"cannot keep a secret":   "clients: [{id: a, kind: public, secret_hash: x, scopes: [launch], redirect_uris: [https://a.example/cb]}]",
		"redirect_uris":          "clients: [{id: a, kind: public, scopes: [launch]}]",
		"used twice":             "clients: [{id: a, kind: backend, jwks_uri: https://a.example/j, scopes: [x]}, {id: a, kind: backend, jwks_uri: https://a.example/j, scopes: [x]}]",
		"must be https":          "clients: [{id: a, kind: backend, jwks_uri: http://a.example/j, scopes: [x]}]",
		"field secret not found": "clients: [{id: a, kind: confidential-symmetric, secret: plain, scopes: [x], redirect_uris: [https://a.example/cb]}]",
	} {
		if _, err := loadClientsText(t, text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}
