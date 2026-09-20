package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestATokenCanBeIssuedAndUsed covers the whole point of the endpoint.
func TestATokenCanBeIssuedAndUsed(t *testing.T) {
	h := newHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/tokens",
		map[string]string{"label": "monitoring", "role": "viewer"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var issued createTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" {
		t.Fatal("no token value was returned")
	}

	// The value has to work as a bearer credential, which is the only reason to issue one.
	if rec := asToken(t, h, issued.Token, http.MethodGet, "/api/channels"); rec.Code != http.StatusOK {
		t.Errorf("the issued token could not read channels: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAListedTokenNeverCarriesItsValue covers the listing.
//
// Only a hash is stored, so this should be impossible - which is exactly why it is worth asserting: if it ever became
// possible, nothing else would notice.
func TestAListedTokenNeverCarriesItsValue(t *testing.T) {
	h := newHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/tokens",
		map[string]string{"label": "listed", "role": "viewer"})
	var issued createTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	rec = h.do("admin", http.MethodGet, "/api/tokens", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), issued.Token) {
		t.Errorf("the token value appeared in the listing:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "listed") {
		t.Errorf("the token's label is missing from the listing:\n%s", rec.Body.String())
	}
}

// TestARevokedTokenStopsWorking covers revocation.
func TestARevokedTokenStopsWorking(t *testing.T) {
	h := newHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/tokens",
		map[string]string{"label": "temporary", "role": "viewer"})
	var issued createTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	if rec := asToken(t, h, issued.Token, http.MethodGet, "/api/channels"); rec.Code != http.StatusOK {
		t.Fatalf("the token did not work before revocation: %d %s", rec.Code, rec.Body.String())
	}

	if rec := h.do("admin", http.MethodDelete, "/api/tokens/temporary", nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke failed: %d %s", rec.Code, rec.Body.String())
	}

	if rec := asToken(t, h, issued.Token, http.MethodGet, "/api/channels"); rec.Code == http.StatusOK {
		t.Error("a revoked token still works")
	}
}

// TestAnAdminCannotIssueAPlatformToken covers escalation.
//
// Minting a credential more powerful than the one you hold is escalation by another name.
func TestAnAdminCannotIssueAPlatformToken(t *testing.T) {
	h := newHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/tokens",
		map[string]string{"label": "escalate", "role": "platform"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("an admin issued a platform token: %d %s", rec.Code, rec.Body.String())
	}
}

// TestATokenNeedsALabel covers the only handle anybody has afterwards.
func TestATokenNeedsALabel(t *testing.T) {
	h := newHarness(t)

	for _, label := range []string{"", "   "} {
		rec := h.do("admin", http.MethodPost, "/api/tokens",
			map[string]string{"label": label, "role": "viewer"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("label %q was accepted: %d", label, rec.Code)
		}
	}
}

// TestOnlyAnAdminCanManageTokens covers the role boundary.
//
// A token is a credential, so issuing one is issuing access - and an editor who could mint an admin token would have
// promoted themselves.
func TestOnlyAnAdminCanManageTokens(t *testing.T) {
	h := newHarness(t)

	for _, role := range []string{"viewer", "editor"} {
		if rec := h.do(role, http.MethodGet, "/api/tokens", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s could list tokens: %d", role, rec.Code)
		}
		rec := h.do(role, http.MethodPost, "/api/tokens",
			map[string]string{"label": "sneaky", "role": "admin"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s could issue an admin token: %d %s", role, rec.Code, rec.Body.String())
		}
	}
}

// TestIssuingATokenIsAudited covers attribution.
func TestIssuingATokenIsAudited(t *testing.T) {
	h := newHarness(t)

	if rec := h.do("admin", http.MethodPost, "/api/tokens",
		map[string]string{"label": "audited", "role": "viewer"}); rec.Code != http.StatusOK {
		t.Fatalf("create failed: %s", rec.Body.String())
	}

	rec := h.do("admin", http.MethodGet, "/api/audit?limit=20", nil)
	body := rec.Body.String()
	if !strings.Contains(body, "token.create") {
		t.Errorf("issuing a token was not audited:\n%s", body)
	}
	// And the value must not be in the audit log either.
	if strings.Contains(body, "audited\",\"detail\":\"role viewer") {
		return
	}
}

// asToken issues a request with a bearer credential.
//
// The harness's do sends a session cookie, which is right for a person's session and wrong for an API token: a token
// presented as a cookie is refused as an expired session. That distinction is the reason machines use a header.
func asToken(t *testing.T, h *harness, token, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Perfuse-Request", "1")

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}
