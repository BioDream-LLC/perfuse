package store

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/tenant"
)

// These are the tests that matter. Everything else in this package failing is a bug;
// these failing is a reportable breach.

func twoTenants(t *testing.T) (*Store, *Scoped, *Scoped, context.Context) {
	t.Helper()
	st, ctx := tenantStore(t)

	for _, id := range []tenant.ID{"acme", "beta"} {
		if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	acme, err := st.Scope(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.Scope(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	return st, acme, beta, ctx
}

func TestATenantCannotSeeAnothersUsers(t *testing.T) {
	_, acme, beta, ctx := twoTenants(t)

	if _, err := acme.CreateUser(ctx, "acme-admin", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := beta.CreateUser(ctx, "beta-admin", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatal(err)
	}

	got, err := acme.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("acme sees %d users, want 1", len(got))
	}
	if got[0].Username != "acme-admin" {
		t.Errorf("acme sees %q", got[0].Username)
	}

	got, err = beta.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Username != "beta-admin" {
		t.Errorf("beta sees %+v", got)
	}
}

func TestBothTenantsCanHaveAnAdminAccount(t *testing.T) {
	// Every organisation names its first account "admin". Under a globally unique
	// username this is the first thing that breaks, on day one.
	_, acme, beta, ctx := twoTenants(t)

	if _, err := acme.CreateUser(ctx, "admin", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatalf("acme admin: %v", err)
	}
	if _, err := beta.CreateUser(ctx, "admin", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatalf("beta admin: %v", err)
	}
}

func TestDuplicateUsernameWithinATenantIsStillRefused(t *testing.T) {
	_, acme, _, ctx := twoTenants(t)

	if _, err := acme.CreateUser(ctx, "dup", "a sufficiently long password", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.CreateUser(ctx, "dup", "a sufficiently long password", RoleViewer); err == nil {
		t.Error("a duplicate username within one tenant was accepted")
	}
}

func TestATenantCannotSeeAnothersAuditTrail(t *testing.T) {
	_, acme, beta, ctx := twoTenants(t)

	if err := acme.Audit(ctx, AuditEntry{Username: "acme-user", Action: "channel.start"}); err != nil {
		t.Fatal(err)
	}
	if err := beta.Audit(ctx, AuditEntry{Username: "beta-user", Action: "channel.stop"}); err != nil {
		t.Fatal(err)
	}

	got, err := acme.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("acme sees %d audit rows, want 1", len(got))
	}
	if got[0].Username != "acme-user" {
		t.Errorf("acme sees an audit row for %q", got[0].Username)
	}
}

func TestAPlatformAdministratorCanSeeEverything(t *testing.T) {
	// The one deliberate crossing. Investigating an incident across customers is a
	// legitimate need, and it goes through the unscoped method so the crossing is
	// visible at the call site rather than implied.
	st, acme, beta, ctx := twoTenants(t)

	if err := acme.Audit(ctx, AuditEntry{Username: "a", Action: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := beta.Audit(ctx, AuditEntry{Username: "b", Action: "y"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("the unscoped audit log returned %d rows, want 2", len(got))
	}
}

func TestScopeRefusesAMissingTenant(t *testing.T) {
	// The id usually arrives from a session or a URL. A handle for a tenant that was
	// deleted five minutes ago would read and write rows belonging to nobody.
	st, ctx := tenantStore(t)

	if _, err := st.Scope(ctx, "never-existed"); err == nil {
		t.Error("Scope accepted a tenant that does not exist")
	}
}

func TestScopeRefusesADisabledTenant(t *testing.T) {
	st, ctx := tenantStore(t)
	if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: "paused", Name: "Paused", Disabled: true}); err != nil {
		t.Fatal(err)
	}

	_, err := st.Scope(ctx, "paused")
	if err == nil {
		t.Fatal("Scope accepted a disabled tenant")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("the error should say why, got: %v", err)
	}
}

func TestScopeRefusesAnUnsafeID(t *testing.T) {
	// Belt and braces. The id is validated at creation, but Scope is reached from a
	// URL and must not trust it either.
	st, ctx := tenantStore(t)

	for _, id := range []tenant.ID{"../escape", "UPPER", "", "a/b"} {
		if _, err := st.Scope(ctx, id); err == nil {
			t.Errorf("Scope accepted unsafe id %q", id)
		}
	}
}

func TestScopeRefusesThePlatformPseudoTenant(t *testing.T) {
	// The platform owns no data. Scoping to it would produce a handle that reads and
	// writes rows tagged "_platform", which no query for real data would ever find.
	st, ctx := tenantStore(t)

	_, err := st.Scope(ctx, tenant.Platform)
	if err == nil {
		t.Fatal("Scope accepted the platform pseudo-tenant")
	}
	if !strings.Contains(err.Error(), "owns no data") {
		t.Errorf("the error should explain, got: %v", err)
	}
}

func TestScopedHandleReportsItsTenant(t *testing.T) {
	_, acme, _, _ := twoTenants(t)
	if acme.TenantID() != "acme" {
		t.Errorf("TenantID() = %q", acme.TenantID())
	}
}

func TestUnscopedIsExplicit(t *testing.T) {
	// Reachable, because a platform administrator needs it. A method rather than a
	// field, so a reviewer can see at the call site that isolation is being stepped
	// around.
	st, acme, _, _ := twoTenants(t)
	if acme.Unscoped() != st {
		t.Error("Unscoped did not return the underlying store")
	}
}

func TestSingleTenantCodeStillWorks(t *testing.T) {
	// Every existing caller uses the unscoped methods and never mentions tenancy. A
	// single-tenant installation genuinely has one tenant called main, so that is
	// correct behaviour rather than a gap.
	st, ctx := tenantStore(t)

	if _, err := st.CreateUser(ctx, "solo", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Username != "solo" {
		t.Fatalf("unscoped round trip returned %+v", got)
	}

	// And that user belongs to the default tenant, so a later upgrade to
	// multi-tenancy finds it rather than losing it.
	var tid string
	if err := st.db.QueryRowContext(ctx,
		`SELECT tenant_id FROM users WHERE username = 'solo'`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if tid != DefaultTenant {
		t.Errorf("an unscoped user landed in tenant %q, want %q", tid, DefaultTenant)
	}
}
