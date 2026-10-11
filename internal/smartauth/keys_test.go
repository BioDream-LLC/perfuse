package smartauth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
)

func TestAKeyIsCreatedOnceAndItsTokensVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smart.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); unixModes && info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", info.Mode().Perm())
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil || k2.ID != k1.ID {
		t.Fatalf("reloaded key %v %v, want the same key", err, k2)
	}
	now := time.Now()
	tok, err := k1.Sign(map[string]any{"iss": "https://perfuse.example/auth", "aud": "https://perfuse.example/fhir",
		"sub": "client-1", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "jti": "j1", "scope": "system/*.rs"})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := oidc.NewStaticKeySet(k2.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	c, err := oidc.Verify(context.Background(), keys, tok, oidc.VerifyOptions{Issuer: "https://perfuse.example/auth",
		ClientID: "https://perfuse.example/fhir", Algorithms: []string{"RS256"}, System: true})
	if err != nil || c.Scope != "system/*.rs" {
		t.Fatalf("verify: %v %+v", err, c)
	}
}

func TestAKeyOthersCanReadIsRefused(t *testing.T) {
	if !unixModes {
		t.Skip("Windows has no Unix modes; the key's privacy there is its directory's ACL")
	}
	path := filepath.Join(t.TempDir(), "smart.key")
	if _, err := LoadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o644)
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("a world-readable signing key was accepted")
	}
}
