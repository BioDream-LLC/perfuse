package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// SettingsFiles are the configuration files the interface may edit.
//
// Paths come from the flags the process was started with. The interface edits the same files rather than keeping a second
// copy in the database, which keeps configuration diffable on disk and avoids the question of which source wins.
//
// A file that was not configured has an empty path, and the interface offers to create one rather than silently having
// nowhere to write.
type SettingsFiles struct {
	// Alerts is the alert rules file, from -alerts.
	Alerts string

	// OIDC is the single sign-on configuration, from -oidc.
	OIDC string

	// LDAP is the directory configuration, from -ldap.
	LDAP string

	// SAML is the SAML 2.0 sign-in configuration, from -saml.
	SAML string

	// Peers is the fleet peer list, from -peers.
	Peers string
}

// StartupSettings are what the process was started with.
//
// Read-only, and shown rather than hidden. Somebody diagnosing a problem needs to know how the process was started, and
// "not shown anywhere" is how a wrong flag survives for months.
type StartupSettings struct {
	Addr           string   `json:"addr"`
	ChannelsDir    string   `json:"channelsDir"`
	DatabasePath   string   `json:"databasePath"`
	TLS            bool     `json:"tls"`
	MultiTenant    bool     `json:"multiTenant"`
	EngineRunning  bool     `json:"engineRunning"`
	FHIRServing    bool     `json:"fhirServing"`
	StoreMessages  bool     `json:"storeMessages"`
	StorePayloads  bool     `json:"storePayloads"`
	RetentionDays  int      `json:"retentionDays"`
	AllowMetadata  bool     `json:"allowMetadataEgress"`
	JSONLogs       bool     `json:"jsonLogs"`
	Version        string   `json:"version"`
	GoVersion      string   `json:"goVersion"`
	Platform       string   `json:"platform"`
	StartedAt      string   `json:"startedAt"`
	CommandLine    []string `json:"commandLine"`
	FleetLabel     string   `json:"fleetLabel,omitempty"`
	TraceEndpoint  string   `json:"traceEndpoint,omitempty"`
	AlertWebhook   bool     `json:"alertWebhook"`
	AlertSeverity  string   `json:"alertSeverity,omitempty"`
	AuthMethods    []string `json:"authMethods"`
	MetadataReason string   `json:"metadataReason,omitempty"`
}

// settingsFileResponse describes one editable file.
type settingsFileResponse struct {
	// Kind names the file: alerts, oidc, ldap or peers.
	Kind string `json:"kind"`

	// Path is where it lives, empty when none is configured.
	Path string `json:"path,omitempty"`

	// Configured reports whether the process was started with this file.
	Configured bool `json:"configured"`

	// Exists reports whether the file is on disk. A configured path that does not exist is a real state: the flag was
	// given and the file has not been written yet.
	Exists bool `json:"exists"`

	// Content is the file's text, with credentials redacted for a viewer.
	Content string `json:"content"`

	// Redacted reports whether anything was hidden, so the interface can say so rather than letting somebody save a
	// placeholder over a real secret.
	Redacted bool `json:"redacted"`

	// ModifiedAt is the file's timestamp.
	ModifiedAt string `json:"modifiedAt,omitempty"`

	// Bytes is its size.
	Bytes int64 `json:"bytes"`

	// RestartRequired says whether a change takes effect only on restart.
	RestartRequired bool `json:"restartRequired"`

	// Description explains what the file does, shown above the editor.
	Description string `json:"description"`
}

// settingsResponse is the whole settings page.
type settingsResponse struct {
	Files   []settingsFileResponse `json:"files"`
	Startup StartupSettings        `json:"startup"`

	// CanEdit reports whether this caller may write. The interface uses it to show a read-only editor rather than a
	// save button that fails.
	CanEdit bool `json:"canEdit"`
}

// settingKinds describes each editable file.
//
// A table rather than a switch in four places, so adding a file means one entry and the drift guard in settings_test.go
// can check that every configured path is reachable from the interface.
var settingKinds = []struct {
	kind            string
	description     string
	restartRequired bool
	path            func(SettingsFiles) string
}{
	{
		kind: "alerts",
		description: "Which conditions raise an alert, and how loudly. This is the file operators change most " +
			"often, especially in the first weeks of a deployment.",
		restartRequired: false,
		path:            func(f SettingsFiles) string { return f.Alerts },
	},
	{
		kind: "oidc",
		description: "Single sign-on through an OpenID Connect provider. The group-to-role mapping here decides " +
			"what somebody can do after they sign in; local accounts keep working when the provider is " +
			"unreachable.",
		restartRequired: true,
		path:            func(f SettingsFiles) string { return f.OIDC },
	},
	{
		kind: "ldap",
		description: "Sign-in against a directory such as Active Directory. The group-to-role mapping is the " +
			"part most often wrong, and a group named here that does not exist in the directory means " +
			"nobody can sign in.",
		restartRequired: true,
		path:            func(f SettingsFiles) string { return f.LDAP },
	},
	{
		kind:            "peers",
		description:     "Other Perfuse servers to show on the Fleet page.",
		restartRequired: true,
		path:            func(f SettingsFiles) string { return f.Peers },
	},
}

// handleSettings returns every editable file and what the process was started with.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	// Process-wide, so on a multi-tenant server this needs a platform account.
	//
	// There is one alerts file and one sign-on configuration for the whole process. A tenant administrator reading
	// them would see another customer's arrangements, and saving would change everybody's. Caught by attempting it.
	if !s.requirePlatform(w, r, sess, "this settings page") {
		return
	}

	out := settingsResponse{
		Startup: s.startupSettings(),
		CanEdit: sess.Role == store.RoleAdmin || sess.Role == store.RolePlatform,
	}

	for _, kind := range settingKinds {
		out.Files = append(out.Files, s.describeSettingFile(sess, kind.kind, kind.path(s.SettingsPaths),
			kind.description, kind.restartRequired))
	}

	s.ok(w, out)
}

// describeSettingFile reads one file, redacting for a caller who may not see credentials.
func (s *Server) describeSettingFile(sess *store.Session, kind, path, description string, restart bool) settingsFileResponse {
	out := settingsFileResponse{
		Kind:            kind,
		Path:            path,
		Configured:      strings.TrimSpace(path) != "",
		RestartRequired: restart,
		Description:     description,
	}

	if !out.Configured {
		return out
	}

	info, err := os.Stat(path)
	if err != nil {
		// A configured path with no file is a real state rather than an error: the flag was given and nothing has
		// been written yet. The interface offers to create it.
		return out
	}

	out.Exists = true
	out.Bytes = info.Size()
	out.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)

	raw, err := os.ReadFile(path)
	if err != nil {
		out.Content = "# this file could not be read: " + err.Error()
		return out
	}

	if mayReadSecrets(string(sess.Role)) {
		out.Content = string(raw)
		return out
	}

	// Redacted for a viewer, using the same code as channel definitions. These files hold a client secret and a
	// directory service account password, which is exactly what a read-only role must not be handed.
	redacted, err := redactSecretsInYAML(raw)
	if err != nil {
		out.Content = "# this file could not be parsed, so it is not shown to a role that may not read credentials"
		out.Redacted = true
		return out
	}

	out.Content = string(redacted)
	out.Redacted = string(redacted) != string(raw)

	return out
}

// startupSettings reports what the process was started with.
func (s *Server) startupSettings() StartupSettings {
	out := s.Startup

	if out.StartedAt == "" {
		out.StartedAt = s.startedAt().UTC().Format(time.RFC3339)
	}
	if out.GoVersion == "" {
		out.GoVersion = runtime.Version()
	}
	if out.Platform == "" {
		out.Platform = runtime.GOOS + "/" + runtime.GOARCH
	}

	var methods []string
	methods = append(methods, "password")
	if s.OIDC.Enabled() {
		methods = append(methods, "openid connect")
	}
	if s.ldapEnabled() {
		methods = append(methods, "directory")
	}
	sort.Strings(methods)
	out.AuthMethods = methods

	if !out.AllowMetadata {
		out.MetadataReason = "destinations pointed at cloud instance metadata addresses are refused; " +
			"this is set with -allow-metadata-egress and deliberately cannot be changed here, because a " +
			"channel author is the person it restrains"
	}

	return out
}

// settingsUpdateRequest is a proposed file change.
type settingsUpdateRequest struct {
	// Content is the new file text.
	Content string `json:"content"`

	// Kind names which file, matching the path in the URL rather than trusted from the body.
	Kind string `json:"kind,omitempty"`
}

// handleUpdateSettings writes one configuration file.
//
// Admin only, and validated before anything is written. A settings file that does not parse would take the feature it
// configures down at the next restart, which for the sign-on files means nobody can get in.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.requirePlatform(w, r, sess, "this settings file") {
		return
	}

	kind := r.PathValue("kind")

	entry, ok := settingKindByName(kind)
	if !ok {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("%q is not a settings file", kind))
		return
	}

	path := entry.path(s.SettingsPaths)
	if strings.TrimSpace(path) == "" {
		// Refused with the reason. Writing to a guessed location would put a file somewhere the process is not
		// reading, which looks like the setting being ignored.
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf(
			"this server was not started with a %s file, so there is nowhere to save one; "+
				"restart it with -%s <path>", kind, kind))
		return
	}

	var req settingsUpdateRequest
	if !s.decode(w, r, &req) {
		return
	}

	// A redaction placeholder is refused rather than written.
	//
	// Without this, somebody with a viewer's redacted copy open in one tab and admin rights in another could save
	// "**redacted**" over a real client secret and take single sign-on down, with the file looking plausible.
	if strings.Contains(req.Content, redactedPlaceholder) {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf(
			"this content still contains %s, which is what a redacted view shows instead of a credential; "+
				"saving it would replace the real value with that text", redactedPlaceholder))
		return
	}

	if err := validateSettingsContent(kind, []byte(req.Content)); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	if err := writeSettingsFile(path, []byte(req.Content)); err != nil {
		s.failErr(w, r, err)
		return
	}

	s.log().Info("a settings file was changed",
		"kind", kind, "path", path, "user", sess.Username, "bytes", len(req.Content))
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "settings." + kind,
		Target:   path,
		IP:       clientIP(r),
	})

	out := s.describeSettingFile(sess, kind, path, entry.description, entry.restartRequired)
	s.ok(w, out)
}

// settingKindByName finds a settings kind.
func settingKindByName(kind string) (struct {
	kind            string
	description     string
	restartRequired bool
	path            func(SettingsFiles) string
}, bool) {
	for _, entry := range settingKinds {
		if entry.kind == kind {
			return entry, true
		}
	}
	var zero struct {
		kind            string
		description     string
		restartRequired bool
		path            func(SettingsFiles) string
	}
	return zero, false
}

// writeSettingsFile replaces a file atomically.
//
// Via a temporary file and a rename, for the same reason channel files are: a crash or a full disk part way through a
// write would otherwise leave a configuration file that does not parse, and for the sign-on files that means nobody can
// sign in at the next restart.
func writeSettingsFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("the directory %s could not be created: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".perfuse-settings-*.tmp")
	if err != nil {
		return fmt.Errorf("a temporary file could not be created in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// 0o600 rather than 0o640. These files hold a client secret and a directory service account password, and unlike a
	// channel definition there is no case for another group reading them.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("the file could not be replaced: %w", err)
	}

	return nil
}

// errUnknownSettingsKind is returned for a kind with no validator.
var errUnknownSettingsKind = errors.New("this settings file has no validator, so it will not be written")
