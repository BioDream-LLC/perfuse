package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/ldap"
	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/saml"
)

// harnessWithSignon points the harness at sign-on files inside the test directory.
//
// Both paths are always set, because "there is nowhere to save" and "the file does not exist yet" are different states needing different
// advice, and the tests below cover each deliberately rather than by accident of setup.
func harnessWithSignon(t *testing.T, oidcYAML, ldapYAML string) (*harness, string, string) {
	t.Helper()

	h := newHarness(t)
	oidcPath := filepath.Join(h.dir, "oidc.yaml")
	ldapPath := filepath.Join(h.dir, "ldap.yaml")

	if oidcYAML != "" {
		if err := os.WriteFile(oidcPath, []byte(oidcYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if ldapYAML != "" {
		if err := os.WriteFile(ldapPath, []byte(ldapYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	h.server.SettingsPaths.OIDC = oidcPath
	h.server.SettingsPaths.LDAP = ldapPath

	return h, oidcPath, ldapPath
}

const seedOIDC = `issuer: https://login.example.org
client_id: perfuse
client_secret: a-real-secret
redirect_url: https://perfuse.example.org/api/oidc/callback
label: Sign in with Example
create_users: true
roles:
  admin:
    - perfuse-admins
  viewer:
    - clinical-staff
`

const seedLDAP = `addr: dc01.example.org:636
tls: true
bind_dn: cn=perfuse,ou=services,dc=example,dc=org
bind_password: service-account-secret
user_base_dn: ou=people,dc=example,dc=org
username_attribute: uid
group_base_dn: ou=groups,dc=example,dc=org
group_filter: (&(objectClass=groupOfNames)(member=%d))
roles:
  perfuse-admins: admin
  clinical-staff: viewer
`

func readSignon(t *testing.T, h *harness) signonResponse {
	t.Helper()

	rec := h.do("admin", "GET", "/api/signon", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/signon returned %d: %s", rec.Code, rec.Body.String())
	}

	var got signonResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	return got
}

func TestSignonNeverSendsASecretToTheBrowser(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	rec := h.do("admin", "GET", "/api/signon", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	// The whole response body, not the decoded struct. A secret leaking through a field nobody remembered would not appear in a
	// field-by-field check, and the browser receives the body rather than the struct.
	body := rec.Body.String()
	for _, secret := range []string{"a-real-secret", "service-account-secret"} {
		if strings.Contains(body, secret) {
			t.Errorf("the response contains the secret %q", secret)
		}
	}

	got := readSignon(t, h)
	if !got.OIDC.Config.ClientSecret.Set {
		t.Error("a client secret is configured and the response says it is not set")
	}
	if !got.LDAP.Config.BindPassword.Set {
		t.Error("a bind password is configured and the response says it is not set")
	}
}

func TestSignonDescribesEveryFieldItOffers(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)
	got := readSignon(t, h)

	// The catalogue is what the form is built from. A field with no help is a box somebody has to guess at, and every field here has
	// a way of being wrong that produces no error at all.
	for _, set := range [][]signonField{got.OIDCFields, got.LDAPFields} {
		if len(set) == 0 {
			t.Fatal("a field set came back empty, so the form would have nothing to draw")
		}
		for _, f := range set {
			if f.Key == "" || f.Label == "" {
				t.Errorf("a field has no key or label: %+v", f)
			}
			if f.Help == "" {
				t.Errorf("%s has no help, so nothing explains what happens if it is wrong", f.Key)
			}
			switch f.Kind {
			case "text", "secret", "bool", "list", "duration", "roles":
			default:
				t.Errorf("%s has kind %q, which no control knows how to draw", f.Key, f.Kind)
			}
		}
	}
}

func TestSignonFieldsCoverTheWholeConfigurationFile(t *testing.T) {
	// Pairs the described fields against the YAML keys the loaders accept.
	//
	// This is the drift that matters. Both loaders refuse unknown keys, so a key that exists in the struct and not in this catalogue
	// is a setting that can only be reached by editing the file - which is the situation this whole screen exists to end. A field
	// described here that the loader does not accept is worse: the form would offer it, the save would write it, and the loader
	// would then refuse the entire file at next start.
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)
	got := readSignon(t, h)

	described := func(fields []signonField) map[string]bool {
		out := map[string]bool{}
		for _, f := range fields {
			out[f.Key] = true
		}

		return out
	}

	// Keys are taken from the yaml tags rather than written out again, so this cannot pass by agreeing with a stale copy.
	for _, tc := range []struct {
		what   string
		keys   []string
		fields map[string]bool
		skip   map[string]string
	}{
		{
			what:   "OpenID Connect",
			keys:   yamlKeys(t, oidc.FileConfig{}),
			fields: described(got.OIDCFields),
		},
		{
			what:   "the directory",
			keys:   yamlKeys(t, ldap.Config{}),
			fields: described(got.LDAPFields),
		},
		{
			// SAML was added here after a break went uncaught. Removing allow_unsolicited from the catalogue left every test
			// passing, because this case did not exist: a guard covering two of three mechanisms reports agreement it never
			// checked for the third, which is the same shape as the builder guard that was reading a glob instead of a list.
			what:   "SAML",
			keys:   yamlKeys(t, saml.FileConfig{}),
			fields: described(got.SAMLFields),
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			for _, key := range tc.keys {
				if !tc.fields[key] {
					t.Errorf("%s has a %s setting and the editor does not offer it, so it can only be set by editing the file", tc.what, key)
				}
			}
		})
	}
}

// yamlKeys reads the top-level yaml tag names off a struct by reflection.
//
// Reflection rather than marshalling a zero value, which was the first attempt and found three keys out of twenty: everything tagged
// omitempty is absent from an empty struct, and omitempty is exactly what optional settings carry. A guard that reads its subject
// incompletely reports agreement it never checked, so the count below refuses to run on too few keys.
func yamlKeys(t *testing.T, v any) []string {
	t.Helper()

	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("yamlKeys wants a struct, got %s", typ.Kind())
	}

	var out []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		out = append(out, name)
	}

	if len(out) < 5 {
		t.Fatalf("only found %d keys, so this test is not reading the struct properly", len(out))
	}

	return out
}

func TestSavingOIDCKeepsTheSecretWhenItWasNotRetyped(t *testing.T) {
	h, oidcPath, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	cfg := got.OIDC.Config
	cfg.Label = "Sign in with the new label"

	// No newSecret at all, which is what the form sends when nobody touched that box.
	rec := h.do("admin", "PUT", "/api/signon/oidc", map[string]any{"config": cfg})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	after, err := oidc.LoadFileWithoutDiscovery(oidcPath)
	if err != nil {
		t.Fatalf("the file written by the editor does not load: %v", err)
	}

	// The failure being prevented is the one the settings screen already had: saving a redacted placeholder over a real credential
	// takes sign-on down while leaving a file that reads perfectly plausibly.
	if after.ClientSecret != "a-real-secret" {
		t.Errorf("the client secret became %q after a save that never mentioned it", after.ClientSecret)
	}
	if after.Label != "Sign in with the new label" {
		t.Errorf("the label was not saved: %q", after.Label)
	}
}

func TestSavingOIDCCanReplaceTheSecret(t *testing.T) {
	h, oidcPath, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	replacement := "a-different-secret"

	rec := h.do("admin", "PUT", "/api/signon/oidc", map[string]any{"config": got.OIDC.Config, "newSecret": replacement})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	after, err := oidc.LoadFileWithoutDiscovery(oidcPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClientSecret != replacement {
		t.Errorf("the secret is %q, want the replacement", after.ClientSecret)
	}
}

func TestSignonRolesSurviveARoundTrip(t *testing.T) {
	// The group mapping crosses as one row per group and is stored as four lists, so it is converted in both directions - and a
	// mapping quietly losing a group is the failure that refuses somebody with no explanation available to them.
	h, oidcPath, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	if len(got.OIDC.Config.Roles) != 2 {
		t.Fatalf("read %d group mappings, want 2: %+v", len(got.OIDC.Config.Roles), got.OIDC.Config.Roles)
	}
	if got.OIDC.Config.Roles["perfuse-admins"] != "admin" || got.OIDC.Config.Roles["clinical-staff"] != "viewer" {
		t.Fatalf("the mapping was read wrongly: %+v", got.OIDC.Config.Roles)
	}

	if rec := h.do("admin", "PUT", "/api/signon/oidc", map[string]any{"config": got.OIDC.Config}); rec.Code != http.StatusOK {
		t.Fatalf("saving: got %d: %s", rec.Code, rec.Body.String())
	}

	after, err := oidc.LoadFileWithoutDiscovery(oidcPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Roles.Admin) != 1 || after.Roles.Admin[0] != "perfuse-admins" {
		t.Errorf("the admin mapping came out as %v", after.Roles.Admin)
	}
	if len(after.Roles.Viewer) != 1 || after.Roles.Viewer[0] != "clinical-staff" {
		t.Errorf("the viewer mapping came out as %v", after.Roles.Viewer)
	}
}

func TestSavingSignonRefusesAnInventedRole(t *testing.T) {
	h, oidcPath, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	cfg := got.OIDC.Config
	cfg.Roles["some-group"] = "superuser"

	rec := h.do("admin", "PUT", "/api/signon/oidc", map[string]any{"config": cfg})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for a role that does not exist: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "viewer") {
		t.Errorf("the refusal does not list the roles that do exist: %s", rec.Body.String())
	}

	// And the file must be untouched. A refused save that had already written would break sign-on to report a typo.
	after, err := oidc.LoadFileWithoutDiscovery(oidcPath)
	if err != nil {
		t.Fatalf("the file was damaged by a refused save: %v", err)
	}
	if len(after.Roles.Admin) != 1 {
		t.Errorf("the mapping changed during a refused save: %+v", after.Roles)
	}
}

func TestSavingSignonRefusesAConfigurationTheLoaderWouldReject(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	cfg := got.OIDC.Config
	cfg.Issuer = ""

	rec := h.do("admin", "PUT", "/api/signon/oidc", map[string]any{"config": cfg})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "issuer") {
		t.Errorf("the refusal does not name the missing field: %s", rec.Body.String())
	}

	// The message must not carry the temporary file the check ran against, which means nothing to anybody reading it.
	if strings.Contains(rec.Body.String(), "perfuse-oidc-check") {
		t.Errorf("the refusal exposes an internal temporary path: %s", rec.Body.String())
	}
}

func TestSavingTheDirectoryRefusesACleartextConnectionUnlessItIsChosen(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	cfg := got.LDAP.Config
	cfg.TLS = false
	cfg.StartTLS = false
	cfg.Insecure = false

	rec := h.do("admin", "PUT", "/api/signon/ldap", map[string]any{"config": cfg})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for an unencrypted directory connection: %s", rec.Code, rec.Body.String())
	}

	// The engine's own words, because they explain the choice rather than only refusing it - and passwords crossing a hospital
	// network in cleartext is the consequence.
	if !strings.Contains(rec.Body.String(), "cleartext") {
		t.Errorf("the refusal does not explain what would happen: %s", rec.Body.String())
	}
}

func TestSavingTheDirectoryAppliesTheSameDefaultsTheEngineWould(t *testing.T) {
	h, _, ldapPath := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	cfg := got.LDAP.Config
	cfg.UsernameAttribute = ""
	cfg.GroupNameAttribute = ""

	if rec := h.do("admin", "PUT", "/api/signon/ldap", map[string]any{"config": cfg}); rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	after, err := ldap.LoadConfig(ldapPath)
	if err != nil {
		t.Fatal(err)
	}

	// Written down rather than left empty, so what is on disk is what would run. An empty attribute that the engine fills at startup
	// means the file and the behaviour disagree, and the file is what somebody reads when they are trying to work out why.
	if after.UsernameAttribute != "uid" {
		t.Errorf("the username attribute is %q, want the default written out", after.UsernameAttribute)
	}
	if after.GroupNameAttribute != "cn" {
		t.Errorf("the group name attribute is %q, want the default written out", after.GroupNameAttribute)
	}
}

func TestSignonSaysWhetherEachMethodIsActuallyRunning(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)
	got := readSignon(t, h)

	// The harness has files and has loaded neither, which is exactly the state after an edit. Saying "saved" without saying "not yet
	// in effect" is how somebody waits hopefully for a fix that needs a restart.
	if got.OIDC.Active || got.LDAP.Active {
		t.Error("the response reports a sign-on method as active on a server that never loaded one")
	}
	if !got.OIDC.Exists || !got.LDAP.Exists {
		t.Error("both files exist and the response says otherwise")
	}
	if !got.OIDC.Configured || !got.LDAP.Configured {
		t.Error("both paths are set and the response says otherwise")
	}
}

func TestSignonReportsAFileThatDoesNotLoadRatherThanShowingItEmpty(t *testing.T) {
	h, _, _ := harnessWithSignon(t, "issuer: https://example.org\nnonsense_key: true\n", seedLDAP)

	got := readSignon(t, h)
	if got.OIDC.Problem == "" {
		t.Fatal("a file with an unknown key loaded without complaint, or its problem was not reported")
	}

	// Reported rather than shown as blank, because a blank form invites somebody to fill it in and overwrite a file whose only fault
	// is one bad line - and the bad line is usually the only thing they needed to see.
	if !strings.Contains(got.OIDC.Problem, "nonsense_key") {
		t.Errorf("the problem does not name the offending key: %s", got.OIDC.Problem)
	}
}

func TestSignonWarnsWhenThereIsNoWayBackIn(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)
	got := readSignon(t, h)

	// The harness creates one administrator. This number exists so the screen can warn before somebody makes a directory the only
	// way in: if federated sign-in is misconfigured and there is no local administrator, the way back is editing files on the server.
	if got.LocalAdmins < 1 {
		t.Errorf("counted %d local administrators on a harness that creates one", got.LocalAdmins)
	}
}

func TestOnlyAnAdministratorMayReadSignonSettings(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	// Reading is restricted too, unlike the alert rules. This configuration names the service account and the groups that grant
	// administrator rights, which is a map of how to attack the directory rather than operational state anybody needs.
	for _, role := range []string{"viewer", "editor"} {
		if rec := h.do(role, "GET", "/api/signon", nil); rec.Code != http.StatusForbidden {
			t.Errorf("a %s reading the sign-on settings got %d, want 403", role, rec.Code)
		}
		if rec := h.do(role, "PUT", "/api/signon/ldap", map[string]any{"config": map[string]any{}}); rec.Code != http.StatusForbidden {
			t.Errorf("a %s saving the directory settings got %d, want 403", role, rec.Code)
		}
	}
}

func TestSavingSignonWithoutAFileSaysHowToGetOne(t *testing.T) {
	h := newHarness(t)
	h.server.SettingsPaths.OIDC = ""
	h.server.SettingsPaths.LDAP = ""

	for path, flag := range map[string]string{"/api/signon/oidc": "-oidc", "/api/signon/ldap": "-ldap"} {
		rec := h.do("admin", "PUT", path, map[string]any{"config": map[string]any{}})
		if rec.Code != http.StatusConflict {
			t.Errorf("%s returned %d, want 409: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), flag) {
			t.Errorf("%s does not say to restart with %s: %s", path, flag, rec.Body.String())
		}
	}
}

func TestTestingTheProviderReportsWhatDiscoveryFound(t *testing.T) {
	// A local provider, so this runs everywhere rather than skipping. A test that skips is indistinguishable from one that passes,
	// which this suite has already been caught by once today.
	var issuer string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"issuer": %q,
			"authorization_endpoint": %q,
			"token_endpoint": %q,
			"jwks_uri": %q,
			"id_token_signing_alg_values_supported": ["RS256"]
		}`, issuer, issuer+"/auth", issuer+"/token", issuer+"/keys")
	}))
	defer provider.Close()
	issuer = provider.URL

	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	rec := h.do("admin", "POST", "/api/signon/oidc/test", map[string]any{"issuer": issuer})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var got signonTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("a reachable provider was reported as failing: %+v", got.Stages)
	}

	// The endpoints are reported, not just a verdict. "It works" is worth much less than the three URLs Perfuse will actually use,
	// because a provider serving a discovery document that points somewhere unexpected is a real and confusing failure.
	body := rec.Body.String()
	for _, want := range []string{"/auth", "/token", "/keys", "RS256"} {
		if !strings.Contains(body, want) {
			t.Errorf("the report does not mention %s: %s", want, body)
		}
	}
}

func TestTestingTheProviderReportsWhyItFailed(t *testing.T) {
	// A server that answers, and not with a discovery document. The interesting failures here are not "unreachable" - they are a
	// reachable host that is not a provider, which is what a typo in a URL usually produces.
	notAProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer notAProvider.Close()

	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	rec := h.do("admin", "POST", "/api/signon/oidc/test", map[string]any{"issuer": notAProvider.URL})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want the test itself to succeed and report a failure: %s", rec.Code, rec.Body.String())
	}

	var got signonTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatal("a server that is not a provider was reported as working")
	}
	if len(got.Stages) == 0 || got.Stages[0].Detail == "" {
		t.Fatalf("the failure was reported with no explanation: %+v", got.Stages)
	}
}

func TestTestingTheDirectoryReportsTheStageThatFailed(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	cfg := got.LDAP.Config
	// A port nothing is listening on, which is the ordinary first failure: a firewall, or a directory on a different port.
	cfg.Addr = "127.0.0.1:1"
	cfg.TLS = false
	cfg.Insecure = true
	cfg.Timeout = "2s"

	rec := h.do("admin", "POST", "/api/signon/ldap/test", map[string]any{"config": cfg, "username": "rturner"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var result signonTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("a directory that is not there was reported as working")
	}
	if len(result.Stages) == 0 {
		t.Fatal("no stages were reported, so nothing says what went wrong")
	}

	// The first stage names reaching the directory and says it failed. Reporting stages in order is the whole point: it is the
	// difference between "sign-in does not work" and knowing which single field to correct.
	first := result.Stages[0]
	if first.OK {
		t.Errorf("the first stage passed against a port nothing is listening on: %+v", first)
	}
	if !strings.Contains(strings.ToLower(first.Name), "reach") {
		t.Errorf("the first stage is %q, want the one about reaching the directory", first.Name)
	}
	if first.Detail == "" {
		t.Error("the failed stage carries no detail, so it says only that something is wrong")
	}
}

func TestTestingTheDirectoryChecksTheSettingsBeforeTheNetwork(t *testing.T) {
	h, _, _ := harnessWithSignon(t, seedOIDC, seedLDAP)

	got := readSignon(t, h)
	cfg := got.LDAP.Config
	cfg.TLS = true
	cfg.StartTLS = true

	rec := h.do("admin", "POST", "/api/signon/ldap/test", map[string]any{"config": cfg})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var result signonTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("a contradictory configuration was reported as working")
	}

	// Reported as a settings problem rather than a connection problem. Waiting for a timeout to say "these two options contradict
	// each other" wastes the operator's time and points them at the network.
	if !strings.Contains(result.Stages[0].Detail, "cannot both be set") {
		t.Errorf("the contradiction was not explained: %+v", result.Stages)
	}
}

func TestTestingTheDirectoryNeverAsksForAPassword(t *testing.T) {
	// Reads this file for a request field that would carry somebody's password into a test endpoint.
	//
	// Everything that goes silently wrong in a directory configuration goes wrong before a password is checked, so a probe needs
	// none - and a test endpoint that accepted one would be a password field on a screen that does not need it, which is how
	// credentials end up in a log or a browser's saved-form data.
	source, err := os.ReadFile("signon.go")
	if err != nil {
		t.Fatal(err)
	}

	body := string(source)
	start := strings.Index(body, "func (s *Server) handleTestLDAP")
	if start < 0 {
		t.Fatal("handleTestLDAP is not in signon.go any more, so this test is not reading what it thinks it is")
	}

	handler := body[start:]
	if end := strings.Index(handler[10:], "\nfunc "); end > 0 {
		handler = handler[:end+10]
	}

	for _, forbidden := range []string{"Password string", "password string"} {
		if strings.Contains(handler, forbidden) {
			t.Errorf("the directory test endpoint accepts a password field, which it does not need: %s", forbidden)
		}
	}
}

func TestEverySignonFieldNamesARealJSONProperty(t *testing.T) {
	// The guard for the bug this nearly shipped with.
	//
	// The browser indexes the configuration object by a name the server supplies. An earlier version derived that name in the browser
	// by converting the YAML key, which is wrong for exactly the fields where an initialism meets a word boundary: start_tls becomes
	// startTLS and not startTls, bind_dn becomes bindDN, unique_id_attribute becomes uniqueIDAttribute. Those four controls would have
	// read and written nothing at all, and no type checker can object because the object is indexed by a computed string.
	//
	// So the name is sent, and this pairs every name against the struct that actually crosses the wire. Both directions: a described
	// field that names no property is a dead control, and a property with no described field is a setting the form does not offer.
	for _, tc := range []struct {
		what   string
		fields []signonField
		target any
	}{
		{"OpenID Connect", oidcFields(), oidcConfigJSON{}},
		{"the directory", ldapFields(), ldapConfigJSON{}},
		{"SAML", samlFields(), samlConfigJSON{}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			properties := map[string]bool{}
			typ := reflect.TypeOf(tc.target)
			for i := 0; i < typ.NumField(); i++ {
				tag := typ.Field(i).Tag.Get("json")
				if tag == "" || tag == "-" {
					continue
				}
				properties[strings.Split(tag, ",")[0]] = true
			}

			described := map[string]bool{}
			for _, f := range tc.fields {
				if f.Field == "" {
					t.Errorf("%s does not say which property it edits, so the control would bind to nothing", f.Key)

					continue
				}
				described[f.Field] = true

				if !properties[f.Field] {
					t.Errorf("%s claims to edit %q and no such property is sent to the browser", f.Key, f.Field)
				}
			}

			for property := range properties {
				// caseSensitive is edited through the role mapping rather than as a field of its own, which is a deliberate
				// exception and the only one: it is a property of how groups are matched, not a setting somebody sets separately.
				if property == "caseSensitive" {
					continue
				}
				if !described[property] {
					t.Errorf("%s sends a %q property that no field edits, so it can only be changed by editing the file", tc.what, property)
				}
			}
		})
	}
}
