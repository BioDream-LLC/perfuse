package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// platformHarness adds a platform-role account to the standard harness.
func platformHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)

	name := string(store.RolePlatform)
	if _, err := h.store.CreateUser(t.Context(), name, "a sufficiently long password", store.RolePlatform); err != nil {
		t.Fatal(err)
	}
	token, _, err := h.store.Authenticate(t.Context(), name, "a sufficiently long password", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	h.tokens[name] = token
	return h
}

func TestOnlyThePlatformRoleCanAdministerTenants(t *testing.T) {
	// The boundary that makes multi-tenancy safe to sell. If a customer's own
	// administrator could list tenants, they would learn who else is hosted here -
	// and in a managed service the customer list is commercially sensitive.
	h := platformHarness(t)

	for _, role := range []string{"viewer", "editor", "admin"} {
		if rec := h.do(role, http.MethodGet, "/api/tenants", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s listing tenants = %d, want 403", role, rec.Code)
		}
		body := map[string]string{"id": "sneaky", "name": "Sneaky"}
		if rec := h.do(role, http.MethodPost, "/api/tenants", body); rec.Code != http.StatusForbidden {
			t.Errorf("%s creating a tenant = %d, want 403", role, rec.Code)
		}
		if rec := h.do(role, http.MethodDelete, "/api/tenants/main", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s deleting a tenant = %d, want 403", role, rec.Code)
		}
	}

	if rec := h.do("platform", http.MethodGet, "/api/tenants", nil); rec.Code != http.StatusOK {
		t.Errorf("platform listing tenants = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestAnAdminCannotEscalateByCreatingATenant(t *testing.T) {
	// Worth asserting separately from the permission check: an admin who could create
	// a tenant could create one and put themselves in it, which is a route to the
	// platform role by the back door.
	h := platformHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/tenants",
		map[string]string{"id": "escalation", "name": "Escalation"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an admin created a tenant: %d", rec.Code)
	}

	// And it really was not created.
	list := h.do("platform", http.MethodGet, "/api/tenants", nil)
	if list.Code != http.StatusOK {
		t.Fatal(list.Body.String())
	}
	var got tenantResponse
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, tn := range got.Tenants {
		if string(tn.ID) == "escalation" {
			t.Error("the tenant was created despite the 403")
		}
	}
}

func TestCreateTenantValidatesTheID(t *testing.T) {
	// 400 rather than 500, with the rule explained. "Invalid tenant id" alone sends
	// somebody guessing.
	h := platformHarness(t)

	for _, id := range []string{"../escape", "UPPER", "", "api", "acme--west"} {
		rec := h.do("platform", http.MethodPost, "/api/tenants",
			map[string]string{"id": id, "name": "X"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("creating tenant %q = %d, want 400", id, rec.Code)
		}
		if rec.Body.Len() < 20 {
			t.Errorf("the error for %q is too terse to act on: %s", id, rec.Body.String())
		}
	}
}

func TestCreateTenantRoundTrip(t *testing.T) {
	h := platformHarness(t)

	rec := h.do("platform", http.MethodPost, "/api/tenants",
		map[string]string{"id": "acme", "name": "Acme Health", "notes": "pilot"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	list := h.do("platform", http.MethodGet, "/api/tenants", nil)
	var got tenantResponse
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, tn := range got.Tenants {
		if string(tn.ID) == "acme" {
			found = true
			if tn.Name != "Acme Health" || tn.Notes != "pilot" {
				t.Errorf("round trip lost fields: %+v", tn)
			}
		}
	}
	if !found {
		t.Error("the created tenant was not listed")
	}
}

func TestDuplicateTenantIsAConflict(t *testing.T) {
	h := platformHarness(t)
	body := map[string]string{"id": "acme", "name": "Acme"}

	if rec := h.do("platform", http.MethodPost, "/api/tenants", body); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := h.do("platform", http.MethodPost, "/api/tenants", body); rec.Code != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", rec.Code)
	}
}

func TestDeletingAMissingTenantIs404(t *testing.T) {
	h := platformHarness(t)
	if rec := h.do("platform", http.MethodDelete, "/api/tenants/not-there", nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting a missing tenant = %d, want 404", rec.Code)
	}
}

func TestTheDefaultTenantCannotBeDeletedThroughTheAPI(t *testing.T) {
	// Refused with 400 and an explanation, not 500. A single-tenant installation's
	// rows all belong to it.
	h := platformHarness(t)

	rec := h.do("platform", http.MethodDelete, "/api/tenants/main", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting the default tenant = %d, want 400", rec.Code)
	}
	if !contains(rec.Body.String(), "disable") {
		t.Errorf("the error should point at disabling instead: %s", rec.Body.String())
	}
}

func TestTenancyStatusIsVisibleToAnyViewer(t *testing.T) {
	// The front end uses this to decide whether to mention tenancy at all. It has to
	// be readable by an ordinary user, and it deliberately reveals only a count -
	// never the names.
	h := platformHarness(t)

	rec := h.do("viewer", http.MethodGet, "/api/tenancy", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tenancy status for a viewer = %d", rec.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["multiTenant"] != false {
		t.Errorf("a single-tenant install reports multiTenant=%v", got["multiTenant"])
	}
	if got["isPlatform"] != false {
		t.Error("a viewer was reported as platform")
	}
	// No tenant names, so an ordinary user cannot learn who else is hosted here.
	if _, leaked := got["tenants"]; leaked {
		t.Error("the tenancy status endpoint leaked the tenant list")
	}
}

func TestTenancyStatusBecomesMultiTenant(t *testing.T) {
	h := platformHarness(t)

	if rec := h.do("platform", http.MethodPost, "/api/tenants",
		map[string]string{"id": "second", "name": "Second"}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}

	rec := h.do("viewer", http.MethodGet, "/api/tenancy", nil)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["multiTenant"] != true {
		t.Errorf("with two tenants, multiTenant = %v", got["multiTenant"])
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
