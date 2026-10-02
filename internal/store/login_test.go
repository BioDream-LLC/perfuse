package store

import (
	"errors"
	"testing"

	"github.com/biodream-llc/perfuse/internal/tenant"
)

const goodPassword = "a sufficiently long password"

func TestLoginIsScopedToItsTenant(t *testing.T) {
	// The credential that matters. Two organisations can each have an "admin", so a
	// lookup by username alone would sign somebody into the wrong one.
	_, acme, beta, ctx := twoTenants(t)

	if _, err := acme.CreateUser(ctx, "admin", goodPassword, RoleAdmin); err != nil {
		t.Fatal(err)
	}

	// Acme's credentials must not work against beta.
	if _, _, err := beta.Authenticate(ctx, "admin", goodPassword, "ip", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("acme's password worked against beta: %v", err)
	}

	// And must work against acme.
	token, u, err := acme.Authenticate(ctx, "admin", goodPassword, "ip", "ua")
	if err != nil {
		t.Fatalf("acme's own login failed: %v", err)
	}
	if token == "" || u == nil {
		t.Fatal("no session was created")
	}
}

func TestAnUnknownTenantLooksExactlyLikeAWrongPassword(t *testing.T) {
	// Otherwise anybody can enumerate which organisations are hosted on an instance
	// by trying names at the login form. For a managed service provider the customer
	// list is commercially sensitive.
	st, ctx := tenantStore(t)
	if _, err := st.CreateTenant(ctx, &tenant.Tenant{ID: "real", Name: "Real"}); err != nil {
		t.Fatal(err)
	}
	real, err := st.Scope(ctx, "real")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := real.CreateUser(ctx, "someone", goodPassword, RoleViewer); err != nil {
		t.Fatal(err)
	}

	// Wrong password in a real tenant.
	_, _, wrongPass := real.Authenticate(ctx, "someone", "wrong password entirely", "ip", "ua")

	// Right password, tenant that does not exist.
	ghost := st.ScopeUnchecked("does-not-exist")
	_, _, noTenant := ghost.Authenticate(ctx, "someone", goodPassword, "ip", "ua")

	if !errors.Is(wrongPass, ErrInvalidCredentials) {
		t.Fatalf("wrong password gave %v", wrongPass)
	}
	if !errors.Is(noTenant, ErrInvalidCredentials) {
		t.Fatalf("unknown tenant gave %v, want the same error as a wrong password", noTenant)
	}
	if wrongPass.Error() != noTenant.Error() {
		t.Errorf("the two errors differ:\n  wrong password: %v\n  unknown tenant: %v", wrongPass, noTenant)
	}
}

func TestSessionCarriesTheTenant(t *testing.T) {
	// Every request after login is scoped from this, so if it were wrong or empty the
	// whole boundary would be decorative.
	st, acme, _, ctx := twoTenants(t)

	if _, err := acme.CreateUser(ctx, "user", goodPassword, RoleEditor); err != nil {
		t.Fatal(err)
	}
	token, _, err := acme.Authenticate(ctx, "user", goodPassword, "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}

	sess, err := st.Lookup(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if sess.TenantID != "acme" {
		t.Errorf("session tenant = %q, want acme", sess.TenantID)
	}
}

func TestDisablingATenantEndsItsSessionsImmediately(t *testing.T) {
	// Read on every lookup rather than at login, or somebody suspended for
	// non-payment keeps operating until their session happens to expire. The session
	// is deleted rather than refused, so it cannot start working again by itself.
	st, acme, _, ctx := twoTenants(t)

	if _, err := acme.CreateUser(ctx, "user", goodPassword, RoleEditor); err != nil {
		t.Fatal(err)
	}
	token, _, err := acme.Authenticate(ctx, "user", goodPassword, "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Lookup(ctx, token); err != nil {
		t.Fatalf("the session should work before suspension: %v", err)
	}

	if _, err := st.UpdateTenant(ctx, "acme", "Acme", true, "unpaid"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Lookup(ctx, token); err == nil {
		t.Error("a session survived its tenant being disabled")
	}

	// Re-enabling must not resurrect it: the row is gone.
	if _, err := st.UpdateTenant(ctx, "acme", "Acme", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Lookup(ctx, token); err == nil {
		t.Error("the session came back after the tenant was re-enabled; it should have been deleted")
	}
}

func TestScopedUserLookupRefusesAnotherTenantsUser(t *testing.T) {
	// User ids are global integers. Without the tenant clause, guessing a number
	// reads another organisation's account - the textbook insecure direct object
	// reference.
	_, acme, beta, ctx := twoTenants(t)

	victim, err := beta.CreateUser(ctx, "beta-user", goodPassword, RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := acme.User(ctx, victim.ID); err == nil {
		t.Error("acme read beta's user by id")
	}

	// And beta can read its own.
	if _, err := beta.User(ctx, victim.ID); err != nil {
		t.Errorf("beta could not read its own user: %v", err)
	}
}

func TestSingleTenantLoginIsUnchanged(t *testing.T) {
	// Every existing installation logs in through the unscoped method and has never
	// heard of tenancy.
	st, ctx := tenantStore(t)

	if _, err := st.CreateUser(ctx, "solo", goodPassword, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	token, _, err := st.Authenticate(ctx, "solo", goodPassword, "ip", "ua")
	if err != nil {
		t.Fatalf("single-tenant login failed: %v", err)
	}

	sess, err := st.Lookup(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if string(sess.TenantID) != DefaultTenant {
		t.Errorf("a single-tenant session reports tenant %q, want %q", sess.TenantID, DefaultTenant)
	}
}
