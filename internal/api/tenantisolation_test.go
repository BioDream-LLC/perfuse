package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// This file attempts cross-tenant reads rather than reasoning about them.
//
// It exists because the security audit reached a point where the worst remaining possibility was a tenant administrator
// reading another tenant's clinical data, and the store had already had one cross-tenant leak - stored messages were not
// scoped at all until it was found. A design that is correct in one place and not another is exactly what these attempts
// are for.
//
// Every test here uses doAs, which mints a real session in a real tenant, because the isolation design's whole premise is
// that a handler cannot be handed a tenant directly. A test that could would be testing something else.

// tenantHarness gives two tenants, each with a channel whose name is recognisable.
func tenantHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.enableTenants(t, "alpha", "beta")

	return h
}

// TestATenantCannotListAnotherTenantsChannels is the headline attempt.
func TestATenantCannotListAnotherTenantsChannels(t *testing.T) {
	h := tenantHarness(t)

	// Alpha creates a channel with a name nobody else should ever see.
	create := map[string]string{"yaml": "name: alpha-secret-feed\n" +
		"source:\n  type: mllp\n  listen: 127.0.0.1:19001\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/alpha\n"}

	rec := h.doAs(t, "alpha", store.RoleEditor, http.MethodPost, "/api/channels", create)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("alpha could not create a channel: %d %s", rec.Code, rec.Body.String())
	}

	// Beta lists channels. Its own list must not contain alpha's.
	rec = h.doAs(t, "beta", store.RoleAdmin, http.MethodGet, "/api/channels", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("beta could not list channels: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "alpha-secret-feed") {
		t.Errorf("beta can see alpha's channel:\n%s", rec.Body.String())
	}

	// And alpha still sees its own, so this is isolation rather than everything being broken.
	rec = h.doAs(t, "alpha", store.RoleAdmin, http.MethodGet, "/api/channels", nil)
	if !strings.Contains(rec.Body.String(), "alpha-secret-feed") {
		t.Errorf("alpha cannot see its own channel, so the test above proves nothing:\n%s", rec.Body.String())
	}
}

// TestATenantCannotFetchAnotherTenantsChannelByName covers a direct guess.
//
// Listing being scoped is not the same as fetching being scoped. Somebody who learns a channel name - from a shared
// support ticket, or by guessing a convention - would otherwise read its definition, which holds every credential the
// other organisation's interfaces use.
func TestATenantCannotFetchAnotherTenantsChannelByName(t *testing.T) {
	h := tenantHarness(t)

	create := map[string]string{"yaml": "name: alpha-private\n" +
		"source:\n  type: mllp\n  listen: 127.0.0.1:19002\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/alpha\n"}

	if rec := h.doAs(t, "alpha", store.RoleEditor, http.MethodPost, "/api/channels", create); rec.Code >= 400 {
		t.Fatalf("alpha could not create a channel: %d %s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{
		"/api/channels/alpha-private",
		"/api/channels/alpha-private/yaml",
	} {
		rec := h.doAs(t, "beta", store.RoleAdmin, http.MethodGet, path, nil)
		if rec.Code == http.StatusOK {
			t.Errorf("beta fetched %s and got 200:\n%s", path, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "19002") {
			t.Errorf("beta saw alpha's configuration at %s:\n%s", path, rec.Body.String())
		}
	}
}

// TestATenantCannotChangeAnotherTenantsChannel covers writes.
//
// A read leak is bad; a write would let one organisation redirect another's clinical traffic.
func TestATenantCannotChangeAnotherTenantsChannel(t *testing.T) {
	h := tenantHarness(t)

	create := map[string]string{"yaml": "name: alpha-writable\n" +
		"source:\n  type: mllp\n  listen: 127.0.0.1:19003\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/alpha\n"}

	if rec := h.doAs(t, "alpha", store.RoleEditor, http.MethodPost, "/api/channels", create); rec.Code >= 400 {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}

	hostile := map[string]string{"yaml": "name: alpha-writable\n" +
		"source:\n  type: mllp\n  listen: 127.0.0.1:19003\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/beta-stole-this\n"}

	if rec := h.doAs(t, "beta", store.RoleEditor, http.MethodPut, "/api/channels/alpha-writable",
		hostile); rec.Code == http.StatusOK {
		t.Errorf("beta rewrote alpha's channel: %s", rec.Body.String())
	}

	if rec := h.doAs(t, "beta", store.RoleEditor, http.MethodDelete, "/api/channels/alpha-writable",
		nil); rec.Code == http.StatusOK {
		t.Error("beta deleted alpha's channel")
	}

	// Alpha's channel is still there and still points where it did.
	rec := h.doAs(t, "alpha", store.RoleAdmin, http.MethodGet, "/api/channels/alpha-writable/yaml", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("alpha's channel is gone: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "beta-stole-this") {
		t.Error("alpha's channel was modified by beta")
	}
}

// TestATenantCannotStartOrStopAnotherTenantsChannel covers control rather than configuration.
func TestATenantCannotStartOrStopAnotherTenantsChannel(t *testing.T) {
	h := tenantHarness(t)

	create := map[string]string{"yaml": "name: alpha-running\n" +
		"source:\n  type: mllp\n  listen: 127.0.0.1:19004\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/alpha\n"}

	if rec := h.doAs(t, "alpha", store.RoleEditor, http.MethodPost, "/api/channels", create); rec.Code >= 400 {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}

	for _, action := range []string{"start", "stop"} {
		rec := h.doAs(t, "beta", store.RoleAdmin, http.MethodPost,
			"/api/channels/alpha-running/"+action, nil)
		if rec.Code == http.StatusOK {
			t.Errorf("beta could %s alpha's channel", action)
		}
	}
}

// TestATenantCannotSeeAnotherTenantsUsers covers who exists.
//
// A customer list is commercially sensitive for a managed service, and the names of another hospital's staff are not
// something a neighbouring customer should be able to enumerate.
func TestATenantCannotSeeAnotherTenantsUsers(t *testing.T) {
	h := tenantHarness(t)

	// Creating a user in alpha through doAs, which names it u-alpha-<role>.
	if rec := h.doAs(t, "alpha", store.RoleAdmin, http.MethodGet, "/api/users", nil); rec.Code != http.StatusOK {
		t.Fatalf("alpha could not list its own users: %d %s", rec.Code, rec.Body.String())
	}

	rec := h.doAs(t, "beta", store.RoleAdmin, http.MethodGet, "/api/users", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("beta could not list users: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "u-alpha-") {
		t.Errorf("beta can see alpha's users:\n%s", rec.Body.String())
	}
}

// TestATenantCannotSeeAnotherTenantsAudit covers the trail.
//
// An audit entry names a user and what they did, which is a description of another organisation's operations.
func TestATenantCannotSeeAnotherTenantsAudit(t *testing.T) {
	h := tenantHarness(t)

	create := map[string]string{"yaml": "name: alpha-audited\n" +
		"source:\n  type: mllp\n  listen: 127.0.0.1:19005\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/alpha\n"}
	if rec := h.doAs(t, "alpha", store.RoleEditor, http.MethodPost, "/api/channels", create); rec.Code >= 400 {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}

	rec := h.doAs(t, "beta", store.RoleAdmin, http.MethodGet, "/api/audit?limit=100", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("beta could not read its audit log: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "alpha-audited") {
		t.Errorf("beta can see alpha's activity:\n%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "u-alpha-") {
		t.Errorf("beta can see alpha's usernames in the audit log:\n%s", rec.Body.String())
	}
}

// TestATenantAdminCannotReachTheTenantEndpoints covers escalation.
//
// A customer's own administrator must not be able to create tenants or discover that the others exist. That is why those
// endpoints require the platform role rather than admin.
func TestATenantAdminCannotReachTheTenantEndpoints(t *testing.T) {
	h := tenantHarness(t)

	for _, attempt := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/tenants", nil},
		{http.MethodPost, "/api/tenants", map[string]string{"id": "gamma", "name": "Sneaky"}},
		{http.MethodPut, "/api/tenants/beta", map[string]string{"name": "Renamed"}},
		{http.MethodDelete, "/api/tenants/beta", nil},
	} {
		rec := h.doAs(t, "alpha", store.RoleAdmin, attempt.method, attempt.path, attempt.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a tenant admin returned %d, want 403: %s",
				attempt.method, attempt.path, rec.Code, rec.Body.String())
		}
	}
}

// TestATenantCannotReadTheSettingsPage covers the newest admin-only page.
//
// The settings files are process-wide - one alerts file, one sign-on configuration - so a tenant administrator reading
// them would see another organisation's arrangements, and editing them would change everybody's.
func TestATenantCannotReadTheSettingsPage(t *testing.T) {
	h := tenantHarness(t)

	rec := h.doAs(t, "alpha", store.RoleAdmin, http.MethodGet, "/api/settings", nil)
	if rec.Code == http.StatusOK {
		t.Errorf("a tenant administrator read the process-wide settings page:\n%s", rec.Body.String())
	}
}

// enableTenants turns a harness into a multi-tenant server with the named tenants.
//
// Follows the same shape as newMessageHarness, which set this up for message isolation. Written separately rather than
// reused because that one also builds a message store, and these tests are about channels, users and the audit log - a
// helper doing both would make it unclear which part any failure came from.
func (h *harness) enableTenants(t *testing.T, ids ...string) {
	t.Helper()

	repos, err := NewTenantRepos(h.dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range ids {
		if _, err := h.store.CreateTenant(t.Context(), &tenant.Tenant{ID: tenant.ID(id), Name: "Hospital " + id}); err != nil {
			t.Fatal(err)
		}
		// The repository is created eagerly, so a test failure is about isolation rather than about a directory
		// that did not exist yet.
		if _, err := repos.Repo(tenant.ID(id)); err != nil {
			t.Fatal(err)
		}
	}

	h.server.Repos = repos
	h.server.Runtimes = NewTenantRuntimes(repos, nil, nil, nil, h.server.Log)
	h.handler = h.server.Handler()
}

// TestATenantAdminSeesItsOwnUsersAndNotTheDefaultTenants covers the other direction of the same bug.
//
// The user handlers called the unscoped store, which defaults to the default tenant rather than to every tenant. So this
// was not a leak from one customer to another - it was worse in a quieter way: a tenant administrator was shown, and could
// manage, the default tenant's users, while their own were invisible.
//
// Found by the drift guard on unscoped store use rather than by the isolation attempts, which passed: alpha could not see
// beta's users because neither could see anybody's but the default tenant's.
func TestATenantAdminSeesItsOwnUsersAndNotTheDefaultTenants(t *testing.T) {
	h := tenantHarness(t)

	// The harness creates viewer, editor and admin in the default tenant. doAs creates u-alpha-admin in alpha.
	rec := h.doAs(t, "alpha", store.RoleAdmin, http.MethodGet, "/api/users", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("alpha's admin could not list users: %d %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	if !strings.Contains(body, "u-alpha-admin") {
		t.Errorf("alpha's admin cannot see its own tenant's users:\n%s", body)
	}

	// The default tenant's accounts must not appear. Being able to manage them means being able to delete an account
	// in an organisation this administrator has nothing to do with.
	for _, foreign := range []string{`"username":"admin"`, `"username":"editor"`, `"username":"viewer"`} {
		if strings.Contains(body, foreign) {
			t.Errorf("alpha's admin can see a default-tenant account (%s):\n%s", foreign, body)
		}
	}
}

// TestATenantCannotDeleteAnotherTenantsUserByID is the most serious attempt in this file.
//
// User identifiers are global integers, and the by-ID handlers looked users up with WHERE id = ? and no tenant filter. A
// tenant administrator counting upwards could therefore read, disable, demote or delete accounts in another organisation -
// including locking that organisation's own administrator out of their engine.
//
// Attempted rather than reasoned about, because "the store is tenant-scoped" was true of some methods and not others, and
// the difference is invisible at the call site.
func TestATenantCannotTouchAnotherTenantsUserByID(t *testing.T) {
	h := tenantHarness(t)

	// Beta gets two administrators, so there is a victim and the last-administrator check cannot be what refuses.
	//
	// This matters more than it looks. The first version of this test passed with tenant scoping removed, and the
	// refusal was 409 "this is the only enabled administrator" rather than a not-found - so it was asserting that a
	// safety check happened to fire, not that one tenant cannot reach another's accounts. A test that passes for the
	// wrong reason is worse than no test, because it stops anybody looking.
	betaScope := h.store.ScopeUnchecked(tenant.ID("beta"))
	if _, err := betaScope.CreateUser(t.Context(), "beta-spare-admin",
		"a sufficiently long password", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	victim, err := betaScope.CreateUser(t.Context(), "beta-doctor",
		"a sufficiently long password", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	id := itoa(victim.ID)

	// Alpha needs a second administrator too, or its own last-administrator check could refuse for its own reasons
	// before the tenant filter is ever consulted.
	if _, err := h.store.ScopeUnchecked(tenant.ID("alpha")).CreateUser(t.Context(), "alpha-spare-admin",
		"a sufficiently long password", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	// Alpha's administrator goes looking for it by identifier.
	rec := h.doAs(t, "alpha", store.RoleAdmin, http.MethodPut, "/api/users/"+id,
		map[string]any{"role": "viewer"})
	if rec.Code == http.StatusOK {
		t.Errorf("alpha demoted beta's administrator: %s", rec.Body.String())
	}
	// The refusal has to be a not-found. Any other code means something else stopped it and the tenant filter is
	// untested - which is how this test passed while the filter was removed.
	if rec.Code != http.StatusNotFound {
		t.Errorf("the refusal was %d rather than 404; something other than the tenant filter refused it, so "+
			"this test is not checking isolation: %s", rec.Code, rec.Body.String())
	}

	rec = h.doAs(t, "alpha", store.RoleAdmin, http.MethodPut, "/api/users/"+id,
		map[string]any{"disabled": true})
	if rec.Code == http.StatusOK {
		t.Errorf("alpha disabled beta's administrator: %s", rec.Body.String())
	}

	rec = h.doAs(t, "alpha", store.RoleAdmin, http.MethodDelete, "/api/users/"+id, nil)
	if rec.Code == http.StatusOK {
		t.Errorf("alpha deleted beta's administrator: %s", rec.Body.String())
	}

	// The account is untouched: still present, still an admin, still enabled.
	after, err := h.store.GetUserByID(t.Context(), victim.ID)
	if err != nil {
		t.Fatalf("beta's account is gone: %v", err)
	}
	if after.Role != store.RoleAdmin {
		t.Errorf("beta's account is now %s", after.Role)
	}
	if after.Disabled {
		t.Error("beta's account was disabled by another tenant")
	}
}

// TestAnAPITokenActsOnlyInItsOwnTenant covers machine callers.
//
// Tokens are how a fleet poll, a monitoring script and another Perfuse instance authenticate, so a token that acted across
// tenants would be a leak nobody was watching - there is no person at the other end to notice something odd.
//
// The token row carries a tenant and the session takes it, which is right. Attempted rather than read, because that was also
// true of the user handlers while they were reaching across tenants.
func TestAnAPITokenActsOnlyInItsOwnTenant(t *testing.T) {
	h := tenantHarness(t)

	// A channel belonging to alpha.
	create := map[string]string{"yaml": "name: alpha-token-test\n" +
		"source:\n  type: mllp\n  listen: 127.0.0.1:19010\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/alpha\n"}
	if rec := h.doAs(t, "alpha", store.RoleEditor, http.MethodPost, "/api/channels", create); rec.Code >= 400 {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}

	// A token issued inside beta.
	token, err := h.store.ScopeUnchecked(tenant.ID("beta")).CreateAPIToken(
		t.Context(), "beta-monitor", store.RoleAdmin, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Used directly as a bearer credential, which is how a machine presents it.
	req := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("beta's token could not list its own channels: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "alpha-token-test") {
		t.Errorf("beta's token can see alpha's channels:\n%s", rec.Body.String())
	}

	// And it cannot fetch alpha's channel by name either.
	req = httptest.NewRequest(http.MethodGet, "/api/channels/alpha-token-test/yaml", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Errorf("beta's token fetched alpha's channel definition:\n%s", rec.Body.String())
	}
}
