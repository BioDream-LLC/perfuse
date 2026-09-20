package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/tenant"
)

func tenantStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, context.Background()
}

func TestCreateAndReadTenant(t *testing.T) {
	st, ctx := tenantStore(t)

	created, err := st.CreateTenant(ctx, &tenant.Tenant{
		ID: "acme-health", Name: "Acme Health System", Notes: "pilot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedAt == "" {
		t.Error("CreatedAt was not set")
	}

	got, err := st.Tenant(ctx, "acme-health")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme Health System" || got.Notes != "pilot" || got.Disabled {
		t.Errorf("read back %+v", got)
	}
}

func TestCreateTenantRejectsAnInvalidID(t *testing.T) {
	// The validation is enforced at the store boundary too, not only in the API. A
	// path-traversing id that reached the database would name a directory.
	st, ctx := tenantStore(t)

	for _, id := range []tenant.ID{"../escape", "UPPER", "", "api"} {
		if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "x"}); err == nil {
			t.Errorf("CreateTenant accepted id %q", id)
		}
	}
}

func TestCreateDuplicateTenantIsReported(t *testing.T) {
	st, ctx := tenantStore(t)

	tn := &tenant.Tenant{ID: "acme", Name: "Acme"}
	if _, err := st.CreateTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateTenant(ctx, tn)
	if !errors.Is(err, ErrTenantExists) {
		t.Errorf("second create = %v, want ErrTenantExists", err)
	}
}

func TestMissingTenantIsReported(t *testing.T) {
	st, ctx := tenantStore(t)

	if _, err := st.Tenant(ctx, "nobody"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("Tenant() = %v, want ErrTenantNotFound", err)
	}
	if err := st.DeleteTenant(ctx, "nobody"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("DeleteTenant() = %v, want ErrTenantNotFound", err)
	}
	if _, err := st.UpdateTenant(ctx, "nobody", "x", false, ""); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("UpdateTenant() = %v, want ErrTenantNotFound", err)
	}
}

func TestListTenantsIsOrdered(t *testing.T) {
	// A stable order matters: an unstable one makes a paginated list repeat and skip
	// rows between requests.
	st, ctx := tenantStore(t)

	for _, id := range []tenant.ID{"zulu", "alpha", "mike"} {
		if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.ListTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Plus the default tenant created by the migration.
	var ids []string
	for _, tn := range got {
		ids = append(ids, string(tn.ID))
	}
	want := []string{"alpha", DefaultTenant, "mike", "zulu"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", ids, want)
	}
}

func TestUpdateTenantChangesWhatItShould(t *testing.T) {
	st, ctx := tenantStore(t)
	if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.UpdateTenant(ctx, "acme", "Acme Renamed", true, "unpaid")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme Renamed" || !got.Disabled || got.Notes != "unpaid" {
		t.Errorf("after update: %+v", got)
	}
	if got.ID != "acme" {
		t.Error("the id changed")
	}
}

func TestDisablingIsSeparateFromDeleting(t *testing.T) {
	// An MSP whose customer has not paid needs to stop the feeds, not destroy the
	// record of what was already delivered.
	st, ctx := tenantStore(t)
	if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTenant(ctx, "acme", "Acme", true, ""); err != nil {
		t.Fatal(err)
	}

	exists, enabled, err := st.TenantExists(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("a disabled tenant should still exist")
	}
	if enabled {
		t.Error("a disabled tenant reported as enabled")
	}
}

func TestTenantExistsAnswersBothQuestionsAtOnce(t *testing.T) {
	// Split into two calls, a caller can check existence and then act on a disabled
	// tenant. One call makes that mistake unavailable.
	st, ctx := tenantStore(t)

	exists, enabled, err := st.TenantExists(ctx, "never-created")
	if err != nil {
		t.Fatal(err)
	}
	if exists || enabled {
		t.Errorf("a missing tenant reported exists=%v enabled=%v", exists, enabled)
	}

	if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: "live", Name: "Live"}); err != nil {
		t.Fatal(err)
	}
	exists, enabled, err = st.TenantExists(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || !enabled {
		t.Errorf("a live tenant reported exists=%v enabled=%v", exists, enabled)
	}
}

func TestTheDefaultTenantCannotBeDeleted(t *testing.T) {
	// A single-tenant installation's rows all belong to it, and new ones would have
	// nowhere to go.
	st, ctx := tenantStore(t)

	err := st.DeleteTenant(ctx, DefaultTenant)
	if err == nil {
		t.Fatal("the default tenant was deleted")
	}
	if !strings.Contains(err.Error(), "disable it instead") {
		t.Errorf("the error should point at the alternative, got: %v", err)
	}
}

func TestThePlatformPseudoTenantCannotBeDeleted(t *testing.T) {
	st, ctx := tenantStore(t)
	if err := st.DeleteTenant(ctx, tenant.Platform); err == nil {
		t.Error("the platform pseudo-tenant was deleted")
	}
}

func TestDeletingATenantKeepsItsAuditTrail(t *testing.T) {
	// Deliberate. Deleting a customer must not destroy the record of what was
	// delivered on their behalf - that record is often the only evidence a result
	// reached a clinician, and retention obligations outlive the contract.
	st, ctx := tenantStore(t)
	if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: "leaving", Name: "Leaving"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO audit (at, username, action, tenant_id) VALUES (datetime('now'), 'someone', 'delivered', 'leaving')`); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteTenant(ctx, "leaving"); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit WHERE tenant_id = 'leaving'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("audit rows for a deleted tenant = %d, want 1 kept", n)
	}
}

func TestCountTenants(t *testing.T) {
	// Used to decide whether the interface mentions tenancy at all. A single-tenant
	// installation should not grow a tenant switcher it has no use for.
	st, ctx := tenantStore(t)

	got, err := st.CountTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("a fresh install has %d tenants, want 1 (the default)", got)
	}

	if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: "second", Name: "Second"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.CountTenants(ctx); got != 2 {
		t.Errorf("after adding one, count = %d, want 2", got)
	}
}
