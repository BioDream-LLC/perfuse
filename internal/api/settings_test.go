package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// settingsHarness gives a harness real settings files to edit.
func settingsHarness(t *testing.T) (*harness, string) {
	t.Helper()

	h := newHarness(t)
	dir := t.TempDir()

	alertsPath := filepath.Join(dir, "alerts.yaml")
	if err := os.WriteFile(alertsPath, []byte("rules:\n  - kind: queue-depth\n    threshold: 100\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.server.SettingsPaths = SettingsFiles{Alerts: alertsPath}
	h.handler = h.server.Handler()

	return h, alertsPath
}

// TestSettingsListsEveryEditableFile covers the page's contents.
func TestSettingsListsEveryEditableFile(t *testing.T) {
	h, _ := settingsHarness(t)

	rec := h.do("admin", http.MethodGet, "/api/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var out settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, f := range out.Files {
		kinds = append(kinds, f.Kind)
	}
	sort.Strings(kinds)

	want := []string{"alerts", "ldap", "oidc", "peers"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("kinds are %v, want %v", kinds, want)
	}

	if !out.CanEdit {
		t.Error("an admin cannot edit settings")
	}
}

// TestAViewerCannotOpenSettings covers the role boundary.
//
// These files hold a client secret and a directory service account password. They are redacted for a lesser role anyway,
// but the safer default for a page whose entire purpose is showing configuration is that only an administrator opens it.
func TestAViewerCannotOpenSettings(t *testing.T) {
	h, _ := settingsHarness(t)

	if rec := h.do("viewer", http.MethodGet, "/api/settings", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer got %d for the settings page, want 403", rec.Code)
	}
	if rec := h.do("editor", http.MethodGet, "/api/settings", nil); rec.Code != http.StatusForbidden {
		t.Errorf("an editor got %d for the settings page, want 403", rec.Code)
	}
}

// TestSavingValidAlertRulesWorks covers the ordinary case.
func TestSavingValidAlertRulesWorks(t *testing.T) {
	h, path := settingsHarness(t)

	body := map[string]string{"content": "rules:\n  - kind: queue-depth\n    threshold: 250\n    severity: critical\n"}
	rec := h.do("admin", http.MethodPut, "/api/settings/alerts", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "250") {
		t.Errorf("the file was not written: %s", saved)
	}
}

// TestAnInvalidFileIsRefusedAndTheOldOneSurvives is the point of validating.
//
// A settings file that does not parse would take the feature it configures down at the next restart. For the sign-on files
// that means nobody can get in, which is the worst possible moment to find a typo.
func TestAnInvalidFileIsRefusedAndTheOldOneSurvives(t *testing.T) {
	h, path := settingsHarness(t)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]string{"content": "rules:\n  - kind: not-a-real-kind\n    threshold: 1\n"}
	rec := h.do("admin", http.MethodPut, "/api/settings/alerts", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an invalid file returned %d, want 400: %s", rec.Code, rec.Body.String())
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("the file on disk changed even though the save was refused")
	}
}

// TestTheRefusalDoesNotQuoteATemporaryPath covers the message.
//
// Validation writes a temporary file for the loaders that need one, and an earlier version quoted that path - so a mistake
// in the text on screen read as an internal error somewhere under /var/folders.
func TestTheRefusalDoesNotQuoteATemporaryPath(t *testing.T) {
	h, _ := settingsHarness(t)

	body := map[string]string{"content": "rules:\n  - kind: not-a-real-kind\n    threshold: 1\n"}
	rec := h.do("admin", http.MethodPut, "/api/settings/alerts", body)

	msg := rec.Body.String()
	for _, leak := range []string{"/var/folders", "candidate.yaml", "perfuse-settings-check"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the refusal quotes a temporary path (%q): %s", leak, msg)
		}
	}
}

// TestAnEmptyFileIsRefused covers switching a feature off by saving a blank page.
func TestAnEmptyFileIsRefused(t *testing.T) {
	h, _ := settingsHarness(t)

	for _, content := range []string{"", "   ", "\n\n"} {
		rec := h.do("admin", http.MethodPut, "/api/settings/alerts", map[string]string{"content": content})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("content %q returned %d, want 400", content, rec.Code)
		}
	}
}

// TestSavingARedactedPlaceholderIsRefused covers a way to destroy a credential.
//
// Somebody with a redacted copy open in one tab and admin rights in another could otherwise save the placeholder over a
// real client secret, taking single sign-on down with a file that looks entirely plausible.
func TestSavingARedactedPlaceholderIsRefused(t *testing.T) {
	h, _ := settingsHarness(t)

	content := "rules:\n  - kind: queue-depth\n    threshold: 100\n# secret: " + redactedPlaceholder + "\n"
	rec := h.do("admin", http.MethodPut, "/api/settings/alerts", map[string]string{"content": content})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a redaction placeholder was accepted: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), redactedPlaceholder) {
		t.Errorf("the refusal should name the placeholder: %s", rec.Body.String())
	}
}

// TestAFileThatWasNeverConfiguredCannotBeSaved covers writing to nowhere.
//
// Guessing a location would put a file somewhere the process is not reading, which looks exactly like the setting being
// ignored.
func TestAFileThatWasNeverConfiguredCannotBeSaved(t *testing.T) {
	h, _ := settingsHarness(t)

	rec := h.do("admin", http.MethodPut, "/api/settings/oidc",
		map[string]string{"content": "issuer: https://x.test\n"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "-oidc") {
		t.Errorf("the refusal should name the flag to restart with: %s", rec.Body.String())
	}
}

// TestAnUnknownSettingsKindIsRefused covers the route.
func TestAnUnknownSettingsKindIsRefused(t *testing.T) {
	h, _ := settingsHarness(t)

	rec := h.do("admin", http.MethodPut, "/api/settings/not-a-thing",
		map[string]string{"content": "anything"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// TestEverySettingsKindHasAValidator is the drift guard.
//
// A kind added to the table with no validator would be written unchecked, which removes the only thing separating this from
// a text editor with root on the server.
func TestEverySettingsKindHasAValidator(t *testing.T) {
	for _, entry := range settingKinds {
		// Content that is syntactically fine YAML and semantically wrong for every kind, so a real validator must
		// reject it and a missing one will not.
		err := validateSettingsContent(entry.kind, []byte("this_key_belongs_to_no_settings_file: true\n"))
		if err == nil {
			t.Errorf("%s accepted content that belongs to no settings file, so it has no real validator",
				entry.kind)
		}
		if err == errUnknownSettingsKind {
			t.Errorf("%s has no validator at all", entry.kind)
		}
	}
}

// TestSettingsChangesAreAudited covers the trail.
//
// Changing who can sign in is exactly the action that has to be attributable afterwards.
func TestSettingsChangesAreAudited(t *testing.T) {
	h, _ := settingsHarness(t)

	body := map[string]string{"content": "rules:\n  - kind: queue-depth\n    threshold: 300\n"}
	if rec := h.do("admin", http.MethodPut, "/api/settings/alerts", body); rec.Code != http.StatusOK {
		t.Fatalf("save failed: %s", rec.Body.String())
	}

	rec := h.do("admin", http.MethodGet, "/api/audit?limit=20", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit returned %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "settings.alerts") {
		t.Errorf("the change was not audited: %s", rec.Body.String())
	}
}

// TestStartupSettingsAreReported covers the read-only half.
//
// Startup flags cannot be changed from the interface, and showing them is the point: "not shown anywhere" is how a wrong
// flag survives for months.
func TestStartupSettingsAreReported(t *testing.T) {
	h, _ := settingsHarness(t)

	h.server.Startup = StartupSettings{
		Addr:          "127.0.0.1:8080",
		ChannelsDir:   "/var/perfuse/channels",
		RetentionDays: 30,
	}
	h.handler = h.server.Handler()

	rec := h.do("admin", http.MethodGet, "/api/settings", nil)
	var out settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if out.Startup.Addr != "127.0.0.1:8080" {
		t.Errorf("addr is %q", out.Startup.Addr)
	}
	if out.Startup.ChannelsDir != "/var/perfuse/channels" {
		t.Errorf("channels dir is %q", out.Startup.ChannelsDir)
	}
	if out.Startup.StartedAt == "" {
		t.Error("no start time was reported")
	}
	if len(out.Startup.AuthMethods) == 0 {
		t.Error("no sign-in methods were reported")
	}
	// The egress exemption is operator-only and the page should say why rather than simply omitting it.
	if out.Startup.MetadataReason == "" {
		t.Error("the egress policy is not explained")
	}
}

// TestTheSettingsFileIsWrittenPrivately covers permissions.
//
// These files hold a client secret and a directory service account password. Unlike a channel definition there is no case
// for another group reading them.
func TestTheSettingsFileIsWrittenPrivately(t *testing.T) {
	h, path := settingsHarness(t)

	body := map[string]string{"content": "rules:\n  - kind: queue-depth\n    threshold: 400\n"}
	if rec := h.do("admin", http.MethodPut, "/api/settings/alerts", body); rec.Code != http.StatusOK {
		t.Fatalf("save failed: %s", rec.Body.String())
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the file is mode %o; it should not be readable by group or others", perm)
	}
}
