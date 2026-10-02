package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/settings"
	"github.com/biodream-llc/perfuse/internal/store"
)

// settingsHarness gives a server a real settings file.
func valuesHarness(t *testing.T) (*harness, *settings.Store) {
	t.Helper()

	h := newHarness(t)

	reg, err := settings.NewRegistry(settings.Default())
	if err != nil {
		t.Fatal(err)
	}
	set, err := settings.NewStore(reg, filepath.Join(t.TempDir(), "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	h.server.Settings = set
	h.handler = h.server.Handler()

	return h, set
}

// TestEveryLiveSettingIsActuallyApplied is the drift guard that matters most in this file.
//
// The registry tells the interface whether a change takes effect immediately. If it says immediately and nothing reads the new
// value, the interface says "saved" and means it while the server carries on with the old one - and nobody finds out until a
// restart makes it work, by which point the change looks unrelated.
//
// So a setting may only declare itself live if it appears in the list this server actually applies.
func TestEveryLiveSettingIsActuallyApplied(t *testing.T) {
	reg, err := settings.NewRegistry(settings.Default())
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range reg.All() {
		if s.Effect != settings.EffectLive {
			continue
		}
		if !liveSettings[s.Key] {
			t.Errorf("%s says it takes effect immediately, but nothing applies it. Either wire it into "+
				"applyLiveSettings and add it to liveSettings, or declare EffectRestart.", s.Key)
		}
	}

	// And the reverse, which catches a setting that was wired and then had its effect downgraded - leaving code that
	// pushes a value nothing claims is live.
	for key := range liveSettings {
		s, ok := reg.Lookup(key)
		if !ok {
			t.Errorf("liveSettings names %q, which is not a setting", key)

			continue
		}
		if s.Effect != settings.EffectLive {
			t.Errorf("liveSettings names %q, but it declares %s", key, s.Effect)
		}
	}
}

// TestNoSecretValueIsEverSentToTheInterface is the one leak in this area that would matter.
func TestNoSecretValueIsEverSentToTheInterface(t *testing.T) {
	h, set := valuesHarness(t)

	reg := set.Registry()

	// Give every secret a value distinctive enough to find in a response body.
	const canary = "canary-secret-value-9f3a"
	changes := map[string]any{}
	var secretKeys []string
	for _, s := range reg.All() {
		if s.Kind == settings.KindSecret {
			changes[s.Key] = canary
			secretKeys = append(secretKeys, s.Key)
		}
	}
	if len(changes) > 0 {
		if err := set.Set(changes); err != nil {
			t.Fatalf("setting the secrets failed: %v", err)
		}
	}

	rec := h.do("admin", http.MethodGet, "/api/settings/schema", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reading the schema failed: %d %s", rec.Code, rec.Body.String())
	}

	if strings.Contains(rec.Body.String(), canary) {
		t.Error("a secret value appears in the settings response")
	}

	// And each secret must still be reported as configured, or the interface cannot tell blank from set.
	var got settingsSchemaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range secretKeys {
		if !got.Secrets[key] {
			t.Errorf("%s has a value but is not reported as configured", key)
		}
		if _, present := got.Values[key]; present {
			t.Errorf("%s appears in values at all", key)
		}
	}
}

// TestTheSchemaCarriesEnoughToDrawTheForm covers the contract with the interface.
func TestTheSchemaCarriesEnoughToDrawTheForm(t *testing.T) {
	h, _ := valuesHarness(t)

	rec := h.do("admin", http.MethodGet, "/api/settings/schema", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var got settingsSchemaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Groups) < 4 {
		t.Fatalf("only %d groups came back", len(got.Groups))
	}
	if !got.Writable {
		t.Error("a server with a settings file reported itself unwritable")
	}
	if got.Path == "" {
		t.Error("the settings path was not reported, so the interface cannot say where values are kept")
	}

	var count int
	for _, g := range got.Groups {
		if g.Name == "" {
			t.Error("a group came back with no name")
		}
		if len(g.Subgroups) == 0 {
			t.Errorf("group %q has no subgroups", g.Name)
		}
		for _, sub := range g.Subgroups {
			if sub.Name == "" {
				t.Errorf("group %q has a subgroup with no name", g.Name)
			}
			for _, s := range sub.Settings {
				count++
				// Everything the interface needs to draw a control, checked through the JSON rather
				// than in Go - a field that does not serialise is a field the interface never sees,
				// and that failure is invisible from the server side.
				if s.Label == "" || s.Help == "" || s.Widget == "" || s.Kind == "" {
					t.Errorf("%s arrived incompletely: %+v", s.Key, s)
				}
				if s.Kind == settings.KindChoice && len(s.Choices) == 0 {
					t.Errorf("%s is a choice with no options in the response", s.Key)
				}
				if s.Widget == settings.WidgetSlider && (s.Min == nil || s.Max == nil) {
					t.Errorf("%s is a slider with no range in the response", s.Key)
				}
				// The current value has to be present for anything that is not a secret, or the
				// control renders empty and saving the form would blank it.
				if s.Kind != settings.KindSecret {
					if _, ok := got.Values[s.Key]; !ok {
						t.Errorf("%s has no value in the response", s.Key)
					}
				}
			}
		}
	}
	if count < 15 {
		t.Errorf("only %d settings came back", count)
	}
}

// TestSavingASettingTakesEffectAndIsAudited covers the round trip.
func TestSavingASettingTakesEffectAndIsAudited(t *testing.T) {
	h, set := valuesHarness(t)

	rec := h.do("admin", http.MethodPut, "/api/settings/values", map[string]any{
		"changes": map[string]any{"data.retentionDays": 90},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}

	if got := set.Int("data.retentionDays"); got != 90 {
		t.Errorf("the store says %d, want 90", got)
	}

	var body settingsValuesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Applied) != 1 || body.Applied[0] != "data.retentionDays" {
		t.Errorf("applied = %v", body.Applied)
	}
	// Retention is live, so nothing should be waiting on a restart.
	if len(body.RestartRequired) != 0 {
		t.Errorf("a live setting was reported as needing a restart: %v", body.RestartRequired)
	}

	// The change has to be in the audit trail. Deleting patient data on a schedule is a records decision, and
	// somebody will need to know who changed it.
	entries, err := h.store.ListAudit(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "settings.change" && strings.Contains(e.Detail, "retentionDays") {
			found = true
			if e.Username == "" {
				t.Error("the audit entry does not say who made the change")
			}
		}
	}
	if !found {
		t.Error("changing retention was not audited")
	}
}

// TestASettingNeedingARestartSaysSo covers the honesty requirement.
//
// Without this, somebody changes the passkey domain, sees it saved, believes it, and discovers otherwise at the next sign-in.
func TestASettingNeedingARestartSaysSo(t *testing.T) {
	h, _ := valuesHarness(t)

	rec := h.do("admin", http.MethodPut, "/api/settings/values", map[string]any{
		"changes": map[string]any{"signin.passkeyDomain": "perfuse.example.org"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}

	var body settingsValuesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.RestartRequired) != 1 || body.RestartRequired[0] != "signin.passkeyDomain" {
		t.Fatalf("restartRequired = %v, want the passkey domain", body.RestartRequired)
	}

	// And it must still be reported on the next load, not only at the moment of saving. Somebody who navigates away
	// and back has to still see that a restart is outstanding.
	rec = h.do("admin", http.MethodGet, "/api/settings/schema", nil)
	var schema settingsSchemaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.RestartRequired) != 1 {
		t.Errorf("the pending restart was forgotten between requests: %v", schema.RestartRequired)
	}
}

// TestAnInvalidSettingIsRefusedWithAReadableMessage covers what somebody sees when they get it wrong.
func TestAnInvalidSettingIsRefusedWithAReadableMessage(t *testing.T) {
	h, set := valuesHarness(t)

	rec := h.do("admin", http.MethodPut, "/api/settings/values", map[string]any{
		"changes": map[string]any{"signin.passkeyDomain": "https://perfuse.example.org"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// The message has to name the control and say what to do, not merely that something is invalid.
	body := strings.ToLower(rec.Body.String())
	if !strings.Contains(body, "https://") {
		t.Errorf("the message does not explain the mistake: %s", rec.Body.String())
	}

	// And nothing was saved.
	if set.IsSet("signin.passkeyDomain") {
		t.Error("a rejected value was stored anyway")
	}
}

// TestAnUnchangedValueIsNotReportedAsAChange covers audit honesty.
//
// An entry claiming somebody changed a setting they did not is worse than no entry, because it is the record that gets believed.
func TestAnUnchangedValueIsNotReportedAsAChange(t *testing.T) {
	h, set := valuesHarness(t)

	if err := set.Set(map[string]any{"data.retentionDays": 45}); err != nil {
		t.Fatal(err)
	}

	rec := h.do("admin", http.MethodPut, "/api/settings/values", map[string]any{
		"changes": map[string]any{"data.retentionDays": 45},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var body settingsValuesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Applied) != 0 {
		t.Errorf("resubmitting the same value was reported as a change: %v", body.Applied)
	}
	if len(body.Unchanged) != 1 {
		t.Errorf("unchanged = %v, want the one setting", body.Unchanged)
	}

	entries, err := h.store.ListAudit(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "settings.change" {
			t.Errorf("submitting an unchanged value was audited as a change: %s", e.Detail)
		}
	}
}

// TestASensitiveSettingIsAuditedWithoutItsValue covers what goes in the trail.
func TestASensitiveSettingIsAuditedWithoutItsValue(t *testing.T) {
	h, _ := valuesHarness(t)

	const hook = "https://hooks.example.org/T00000/B00000/verysecrettoken"
	rec := h.do("admin", http.MethodPut, "/api/settings/values", map[string]any{
		"changes": map[string]any{"alerts.webhook": hook},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}

	entries, err := h.store.ListAudit(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, e := range entries {
		if e.Action != "settings.change" {
			continue
		}
		found = true
		// A chat webhook URL is a bearer credential. The audit table is read more widely than the settings page,
		// so what changed belongs there and the value does not.
		if strings.Contains(e.Detail, "verysecrettoken") {
			t.Errorf("the webhook credential was written into the audit trail: %s", e.Detail)
		}
		if !strings.Contains(e.Detail, "alerts.webhook") {
			t.Errorf("the audit entry does not say which setting changed: %s", e.Detail)
		}
	}
	if !found {
		t.Error("changing the alert webhook was not audited at all")
	}
}

// TestAViewerCannotReadOrChangeSettings covers the role floor.
func TestAViewerCannotReadOrChangeSettings(t *testing.T) {
	h, _ := valuesHarness(t)

	for _, role := range []string{"viewer", "editor"} {
		if rec := h.do(role, http.MethodGet, "/api/settings/schema", nil); rec.Code != http.StatusForbidden {
			t.Errorf("a %s read the settings schema: %d", role, rec.Code)
		}
		rec := h.do(role, http.MethodPut, "/api/settings/values", map[string]any{
			"changes": map[string]any{"data.retentionDays": 1},
		})
		if rec.Code != http.StatusForbidden {
			t.Errorf("a %s changed a setting: %d", role, rec.Code)
		}
	}
}

// TestATenantAdminCannotChangeInstallationSettings covers the tenancy rule.
//
// These values belong to the whole installation. One tenant's administrator changing retention would change how long every other
// tenant's data is kept.
func TestATenantAdminCannotChangeInstallationSettings(t *testing.T) {
	h, _ := valuesHarness(t)
	h.enableTenants(t)
	h.handler = h.server.Handler()

	rec := h.doAs(t, "othertenant", store.RoleAdmin, http.MethodGet, "/api/settings/schema", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a tenant administrator read installation settings: %d %s", rec.Code, rec.Body.String())
	}

	rec = h.doAs(t, "othertenant", store.RoleAdmin, http.MethodPut, "/api/settings/values", map[string]any{
		"changes": map[string]any{"data.retentionDays": 1},
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("a tenant administrator changed installation settings: %d", rec.Code)
	}
}

// TestAnUnknownSettingIsRefused covers a stale interface.
func TestAnUnknownSettingIsRefused(t *testing.T) {
	h, _ := valuesHarness(t)

	rec := h.do("admin", http.MethodPut, "/api/settings/values", map[string]any{
		"changes": map[string]any{"data.retentionDayz": 5},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown setting was accepted: %d %s", rec.Code, rec.Body.String())
	}
}
