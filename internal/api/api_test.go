package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/settings"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// PBKDF2 at production cost takes a few hundred milliseconds per hash by design,
// which under the race detector turns this suite into a two-minute wait. The
// tests here are about routing, permissions and file handling, not the cost of
// the hash; store's own tests cover that.
func TestMain(m *testing.M) {
	store.HashIterations = 4096
	m.Run()
}

const sampleYAML = `name: adt-inbound
source:
  type: mllp
  listen: ":6661"
filter: MSH-9.2 != "A28"
destinations:
  - name: archive
    type: file
    dir: ./archive
`

type harness struct {
	t       *testing.T
	server  *Server
	handler http.Handler
	dir     string
	store   *store.Store
	tokens  map[string]string // role name -> session cookie value
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	dir := t.TempDir()
	repo, err := NewChannelRepo(dir)
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// A real settings store, with a path inside the test directory.
	//
	// Without it /api/settings/schema returned 503 and every test that swept the read endpoints skipped it silently - which is how
	// a JSON null reached the settings screen and crashed it on a fresh installation. Nothing is written to this file unless a test
	// saves a setting, so the default state is exactly the state a new operator is in, which is the state that was broken.
	settingsRegistry, err := settings.NewRegistry(settings.Default())
	if err != nil {
		t.Fatal(err)
	}

	settingsStore, err := settings.NewStore(settingsRegistry, filepath.Join(dir, "perfuse-settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		Channels: repo,
		Store:    st,
		Settings: settingsStore,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h := &harness{
		t: t, server: srv, handler: srv.Handler(),
		dir: dir, store: st, tokens: map[string]string{},
	}

	// One account per role, so permission boundaries can be exercised.
	for _, role := range []store.Role{store.RoleViewer, store.RoleEditor, store.RoleAdmin} {
		name := string(role)
		if _, err := st.CreateUser(t.Context(), name, "a sufficiently long password", role); err != nil {
			t.Fatal(err)
		}
		token, _, err := st.Authenticate(t.Context(), name, "a sufficiently long password", "test", "test")
		if err != nil {
			t.Fatal(err)
		}
		h.tokens[name] = token
	}
	return h
}

// do issues a request as the given role. Pass "" for no session.
func (h *harness) do(role, method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if isStateChanging(method) {
		req.Header.Set("X-Perfuse-Request", "1")
	}
	if role != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokens[role]})
	}

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/api/channels", "/api/me", "/api/users", "/api/audit"} {
		rec := h.do("", http.MethodGet, path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", path, rec.Code)
		}
	}
}

func TestHealthNeedsNoSession(t *testing.T) {
	// A health check that requires a login is useless to a load balancer.
	h := newHarness(t)
	if rec := h.do("", http.MethodGet, "/api/health", nil); rec.Code != http.StatusOK {
		t.Errorf("health = %d, want 200", rec.Code)
	}
}

func TestLogin(t *testing.T) {
	h := newHarness(t)

	rec := h.do("", http.MethodPost, "/api/login",
		loginRequest{Username: "admin", Password: "a sufficiently long password"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie was set")
	}
	// An XSS bug must not be able to read the session.
	if !cookie.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Error("the session cookie is not SameSite=Lax")
	}

	me := decodeBody[meResponse](t, rec)
	if me.Username != "admin" || me.Role != "admin" {
		t.Errorf("login response = %+v", me)
	}
}

func TestLoginFailureIsUniform(t *testing.T) {
	// The response must not reveal whether an account exists.
	h := newHarness(t)

	wrongPass := h.do("", http.MethodPost, "/api/login",
		loginRequest{Username: "admin", Password: "wrong password entirely"})
	noSuchUser := h.do("", http.MethodPost, "/api/login",
		loginRequest{Username: "nobody", Password: "wrong password entirely"})

	if wrongPass.Code != http.StatusUnauthorized || noSuchUser.Code != http.StatusUnauthorized {
		t.Fatalf("codes = %d and %d, want 401 for both", wrongPass.Code, noSuchUser.Code)
	}
	if wrongPass.Body.String() != noSuchUser.Body.String() {
		t.Errorf("the two failures are distinguishable:\n %s %s",
			wrongPass.Body.String(), noSuchUser.Body.String())
	}
}

func TestFailedLoginIsAudited(t *testing.T) {
	// A brute force attempt is only visible if failures are recorded.
	h := newHarness(t)
	h.do("", http.MethodPost, "/api/login",
		loginRequest{Username: "admin", Password: "wrong password entirely"})

	entries, err := h.store.ListAudit(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "login.failed" {
			found = true
		}
	}
	if !found {
		t.Error("a failed login was not audited")
	}
}

func TestLogout(t *testing.T) {
	h := newHarness(t)

	if rec := h.do("admin", http.MethodPost, "/api/logout", nil); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d: %s", rec.Code, rec.Body.String())
	}
	// The session must be dead immediately.
	if rec := h.do("admin", http.MethodGet, "/api/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the session still worked after logging out: %d", rec.Code)
	}
}

func TestRolePermissions(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		role, method, path string
		body               any
		want               int
	}{
		// Viewers can read.
		{"viewer", http.MethodGet, "/api/channels", nil, http.StatusOK},
		{"viewer", http.MethodPost, "/api/validate", channelPayload{YAML: sampleYAML}, http.StatusOK},
		// Viewers cannot change channels.
		{"viewer", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}, http.StatusForbidden},
		// Editors can.
		{"editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}, http.StatusOK},
		// Editors cannot manage users.
		{"editor", http.MethodGet, "/api/users", nil, http.StatusForbidden},
		{"viewer", http.MethodGet, "/api/users", nil, http.StatusForbidden},
		// Admins can.
		{"admin", http.MethodGet, "/api/users", nil, http.StatusOK},
	}

	for _, c := range cases {
		rec := h.do(c.role, c.method, c.path, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s as %s = %d, want %d: %s",
				c.method, c.path, c.role, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestStateChangingRequestNeedsHeader(t *testing.T) {
	// The header is what a cross-site form cannot set, which is what makes the
	// SameSite cookie sufficient without a token round trip.
	h := newHarness(t)

	raw, _ := json.Marshal(channelPayload{YAML: sampleYAML})
	req := httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokens["editor"]})
	// Deliberately no X-Perfuse-Request header.

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a state-changing request without the header = %d, want 403", rec.Code)
	}
}

func TestChannelCrudWritesFiles(t *testing.T) {
	// The whole design rests on this: the API is an editor for files.
	h := newHarness(t)

	rec := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	path := filepath.Join(h.dir, "adt-inbound.yaml")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the channel was not written to disk: %v", err)
	}
	if string(onDisk) != sampleYAML {
		t.Errorf("the file does not match what was submitted:\n%s", onDisk)
	}

	// And it reads back.
	rec = h.do("viewer", http.MethodGet, "/api/channels/adt-inbound", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}
	got := decodeBody[channelResponse](t, rec)
	if got.Channel.Name != "adt-inbound" || got.YAML != sampleYAML {
		t.Errorf("get returned %+v", got)
	}
	if got.Channel.AckWhen != "on_delivery" {
		t.Errorf("ackWhen = %q, want the default on_delivery", got.Channel.AckWhen)
	}
	if strings.Join(got.Channel.Reads, " ") != "MSH-9.2" {
		t.Errorf("reads = %v, want the paths the filter uses", got.Channel.Reads)
	}
}

func TestHandEditedFileIsVisibleToTheAPI(t *testing.T) {
	// Advanced users edit files directly; the GUI must pick that up. If this
	// fails, the two paths have diverged and the design is broken.
	h := newHarness(t)

	if err := os.WriteFile(filepath.Join(h.dir, "by-hand.yaml"), []byte(sampleYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := h.do("viewer", http.MethodGet, "/api/channels", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	list := decodeBody[channelListResponse](t, rec)
	if len(list.Channels) != 1 || list.Channels[0].Name != "adt-inbound" {
		t.Errorf("a hand-written file was not listed: %+v", list)
	}
}

func TestBrokenFileIsReportedNotHidden(t *testing.T) {
	// A channel that will not load is exactly what an operator needs to see.
	h := newHarness(t)

	if err := os.WriteFile(filepath.Join(h.dir, "broken.yaml"),
		[]byte("name: broken\nsource:\n  type: mllp\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := h.do("viewer", http.MethodGet, "/api/channels", nil)
	list := decodeBody[channelListResponse](t, rec)

	if len(list.Broken) != 1 {
		t.Fatalf("broken files = %v, want one entry", list.Broken)
	}
	if _, ok := list.Broken["broken.yaml"]; !ok {
		t.Errorf("the broken file was not named: %v", list.Broken)
	}
}

func TestInvalidChannelIsRejectedWithEveryProblem(t *testing.T) {
	h := newHarness(t)

	bad := `name: ""
source:
  type: carrier-pigeon
  listen: nonsense
destinations: []
`
	rec := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: bad})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with an invalid definition = %d, want 400", rec.Code)
	}

	body := decodeBody[errorResponse](t, rec)
	if len(body.Problems) < 3 {
		t.Errorf("got %d problems, want every one at once: %v", len(body.Problems), body.Problems)
	}

	// And nothing was written.
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("an invalid channel left %d file(s) behind", len(entries))
	}
}

func TestValidateDoesNotSave(t *testing.T) {
	h := newHarness(t)

	rec := h.do("viewer", http.MethodPost, "/api/validate", channelPayload{YAML: sampleYAML})
	if rec.Code != http.StatusOK {
		t.Fatalf("validate = %d: %s", rec.Code, rec.Body.String())
	}

	entries, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("validate wrote %d file(s); it must not save", len(entries))
	}
}

func TestDuplicateChannelNameRejected(t *testing.T) {
	h := newHarness(t)

	if rec := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); rec.Code != http.StatusOK {
		t.Fatalf("first create = %d", rec.Code)
	}
	rec := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", rec.Code)
	}
}

func TestUpdateAndRename(t *testing.T) {
	h := newHarness(t)
	h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML})

	renamed := strings.Replace(sampleYAML, "adt-inbound", "adt-renamed", 1)
	rec := h.do("editor", http.MethodPut, "/api/channels/adt-inbound", channelPayload{YAML: renamed})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}

	// The filename should track the channel name, and the old file should be gone.
	if _, err := os.Stat(filepath.Join(h.dir, "adt-renamed.yaml")); err != nil {
		t.Errorf("the renamed file does not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "adt-inbound.yaml")); err == nil {
		t.Error("the old file was left behind after a rename")
	}
}

func TestDeleteChannel(t *testing.T) {
	h := newHarness(t)
	h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML})

	if rec := h.do("editor", http.MethodDelete, "/api/channels/adt-inbound", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(h.dir, "adt-inbound.yaml")); err == nil {
		t.Error("the file still exists after deletion")
	}
	if rec := h.do("viewer", http.MethodGet, "/api/channels/adt-inbound", nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
}

func TestExportDownloadsYAML(t *testing.T) {
	// This is the share button. It hands over the file itself, because the file
	// is the artifact.
	h := newHarness(t)
	h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML})

	rec := h.do("viewer", http.MethodGet, "/api/channels/adt-inbound/yaml", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	if got := rec.Body.String(); got != sampleYAML {
		t.Errorf("export body does not match the file:\n%s", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "adt-inbound.yaml") {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

func TestChannelNameCannotChooseAPath(t *testing.T) {
	// The name comes from a browser. It must not be able to write outside the
	// channel directory.
	h := newHarness(t)

	evil := strings.Replace(sampleYAML, "adt-inbound", "../../etc/passwd", 1)
	rec := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: evil})
	if rec.Code != http.StatusOK {
		// Rejecting it outright is also acceptable.
		return
	}

	entries, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "..") || strings.Contains(e.Name(), "/") {
			t.Errorf("the filename escaped the directory: %q", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("wrote %d files, want 1 inside the directory", len(entries))
	}
}

func TestChannelChangesAreAudited(t *testing.T) {
	h := newHarness(t)

	h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML})
	h.do("editor", http.MethodPut, "/api/channels/adt-inbound", channelPayload{YAML: sampleYAML})
	h.do("editor", http.MethodDelete, "/api/channels/adt-inbound", nil)

	entries, err := h.store.ListAudit(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"channel.create": false, "channel.update": false, "channel.delete": false}
	for _, e := range entries {
		if _, ok := want[e.Action]; ok {
			want[e.Action] = true
			if e.Username != "editor" {
				t.Errorf("%s was attributed to %q, want editor", e.Action, e.Username)
			}
			if e.Target != "adt-inbound" {
				t.Errorf("%s target = %q", e.Action, e.Target)
			}
		}
	}
	for action, found := range want {
		if !found {
			t.Errorf("%s was not audited", action)
		}
	}
}

func TestUnknownJSONFieldRejected(t *testing.T) {
	// Same reasoning as the YAML loader: a silently ignored key produces
	// something that looks configured and is not.
	h := newHarness(t)

	raw := []byte(`{"yaml":"name: x","yml":"typo"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokens["viewer"]})

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown field was accepted: %d", rec.Code)
	}
}

func TestUserManagement(t *testing.T) {
	h := newHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/users", createUserRequest{
		Username: "newperson", Password: "a sufficiently long password", Role: "editor",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create user = %d: %s", rec.Code, rec.Body.String())
	}
	created := decodeBody[store.User](t, rec)
	if created.Username != "newperson" || created.Role != store.RoleEditor {
		t.Errorf("created = %+v", created)
	}

	// The password hash must never appear in a response.
	if strings.Contains(rec.Body.String(), "pbkdf2") {
		t.Error("the response contains a password hash")
	}

	rec = h.do("admin", http.MethodPut, "/api/users/"+itoa(created.ID),
		updateUserRequest{Role: strptr("viewer")})
	if rec.Code != http.StatusOK {
		t.Fatalf("update user = %d: %s", rec.Code, rec.Body.String())
	}

	rec = h.do("admin", http.MethodDelete, "/api/users/"+itoa(created.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete user = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCannotRemoveTheLastAdministrator(t *testing.T) {
	// Losing the last admin means editing the database by hand to recover.
	h := newHarness(t)

	admin, err := h.store.GetUser(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}

	rec := h.do("admin", http.MethodPut, "/api/users/"+itoa(admin.ID),
		updateUserRequest{Role: strptr("viewer")})
	if rec.Code != http.StatusConflict {
		t.Errorf("demoting the only admin = %d, want 409", rec.Code)
	}

	rec = h.do("admin", http.MethodPut, "/api/users/"+itoa(admin.ID),
		updateUserRequest{Disabled: boolptr(true)})
	if rec.Code != http.StatusConflict {
		t.Errorf("disabling the only admin = %d, want 409", rec.Code)
	}

	rec = h.do("admin", http.MethodDelete, "/api/users/"+itoa(admin.ID), nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("deleting your own account = %d, want 409", rec.Code)
	}
}

func TestRoleChangeTakesEffectImmediately(t *testing.T) {
	// A demotion must not wait for a cookie to expire.
	h := newHarness(t)

	editor, err := h.store.GetUser(t.Context(), "editor")
	if err != nil {
		t.Fatal(err)
	}
	if rec := h.do("admin", http.MethodPut, "/api/users/"+itoa(editor.ID),
		updateUserRequest{Role: strptr("viewer")}); rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}

	// The old session is gone, so the editor has to sign in again.
	if rec := h.do("editor", http.MethodGet, "/api/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old session survived a role change: %d", rec.Code)
	}
}

func TestChangeOwnPasswordRequiresCurrent(t *testing.T) {
	h := newHarness(t)

	rec := h.do("viewer", http.MethodPost, "/api/me/password",
		changePasswordRequest{Current: "wrong password entirely", New: "a brand new long password"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("changing a password without the current one = %d, want 401", rec.Code)
	}

	rec = h.do("viewer", http.MethodPost, "/api/me/password",
		changePasswordRequest{Current: "a sufficiently long password", New: "a brand new long password"})
	if rec.Code != http.StatusOK {
		t.Fatalf("change password = %d: %s", rec.Code, rec.Body.String())
	}

	if _, _, err := h.store.Authenticate(t.Context(), "viewer", "a brand new long password", "", ""); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
}

func TestShortPasswordRejectedByAPI(t *testing.T) {
	h := newHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/users",
		createUserRequest{Username: "weak", Password: "short", Role: "viewer"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a short password = %d, want 400", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	rec := h.do("", http.MethodGet, "/api/health", nil)

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
}

func TestFilenameFor(t *testing.T) {
	cases := map[string]string{
		"adt-inbound":      "adt-inbound.yaml",
		"ADT Inbound":      "adt-inbound.yaml",
		"lab.results":      "lab-results.yaml",
		"../../etc/passwd": "etcpasswd.yaml",
		"weird!!!name":     "weirdname.yaml",
	}
	for in, want := range cases {
		got, err := filenameFor(in)
		if err != nil {
			t.Errorf("filenameFor(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("filenameFor(%q) = %q, want %q", in, got, want)
		}
	}

	if _, err := filenameFor("!!!"); err == nil {
		t.Error("a name with no usable characters was accepted")
	}
}

func itoa(n int64) string {
	return strings.TrimSpace(string(json.Number(formatInt(n))))
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }

// doAs issues a request as a user belonging to a particular tenant.
//
// Separate from do because the tenant has to come from a real session - the whole point of the isolation
// design is that a handler cannot be handed a tenant directly, so a test must not be able to either.
func (h *harness) doAs(
	t *testing.T, tenantID string, role store.Role, method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	username := "u-" + tenantID + "-" + string(role)
	const password = "a sufficiently long password"

	// Reused when this tenant and role have already been used in this test.
	//
	// Without this a second call fails with "already exists", which reads as an isolation failure rather than as a
	// helper being called twice - and a test that has to be arranged around the helper's limitations tends to be
	// arranged into not testing the thing.
	if _, ok := h.tokens[username]; ok {
		return h.do(username, method, path, body)
	}

	// The tenant has to exist before an account can belong to it.
	//
	// This was not needed until foreign keys were enabled for in-memory databases, which they now are - previously tests
	// enforced no constraints while production enforced them, so these harnesses were creating users in tenants that did
	// not exist and production would have refused. Creating it here rather than loosening the constraint, because the
	// constraint is right.
	if _, err := h.store.CreateTenant(t.Context(), &tenant.Tenant{
		ID: tenant.ID(tenantID), Name: tenantID,
	}); err != nil && !errors.Is(err, store.ErrTenantExists) && !errors.Is(err, store.ErrDuplicate) {
		t.Fatal(err)
	}

	scoped := h.store.ScopeUnchecked(tenant.ID(tenantID))
	if _, err := scoped.CreateUser(t.Context(), username, password, role); err != nil {
		t.Fatal(err)
	}

	// Authenticated through the same scope, because a username alone is ambiguous across tenants - two
	// organisations can each have an "admin" - and the store deliberately refuses to guess.
	token, _, err := scoped.Authenticate(t.Context(), username, password, "test", "test")
	if err != nil {
		t.Fatal(err)
	}

	h.tokens[username] = token
	return h.do(username, method, path, body)
}
