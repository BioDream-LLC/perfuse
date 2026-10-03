package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestProviderAccessCardCountsGroupLimitedTokens covers the note on the Provider Access card: an unlimited token can export
// every provider's attribution list, so the card says whether any token is limited.
func TestProviderAccessCardCountsGroupLimitedTokens(t *testing.T) {
	h := newHarness(t)

	note := func() string {
		t.Helper()
		rec := h.do("viewer", http.MethodGet, "/api/cms0057/status", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var out struct{ APIs []cms0057API }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, a := range out.APIs {
			if a.Name == "Provider Access API" {
				return a.Note
			}
		}
		t.Fatal("no Provider Access card")
		return ""
	}

	if n := note(); !strings.Contains(n, "No API token is limited") {
		t.Errorf("with no limited token: %q", n)
	}
	if rec := h.do("admin", http.MethodPost, "/api/tokens",
		map[string]any{"label": "riverside", "fhirGroups": []string{"riverside-attributed"}}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if n := note(); !strings.HasPrefix(n, "1 API token is limited") {
		t.Errorf("with one limited token: %q", n)
	}
}
