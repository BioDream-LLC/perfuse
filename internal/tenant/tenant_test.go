package tenant

import (
	"strings"
	"testing"
)

func TestValidIDs(t *testing.T) {
	for _, id := range []ID{
		"acme",
		"acme-health",
		"a",
		"a1",
		"st-josephs",
		"hospital-2",
		ID("x" + strings.Repeat("y", maxIDLength-1)),
	} {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
}

func TestPathTraversalIsRefused(t *testing.T) {
	// The reason the character rules exist. A tenant id becomes a directory name,
	// so an id that can contain a dot or a slash is an id that can read another
	// tenant's channels - or /etc.
	for _, id := range []ID{
		"..",
		"../other",
		"acme/../beta",
		"a/b",
		`a\b`,
		"./acme",
		"acme.",
		".acme",
		"acme..beta",
		"/absolute",
		"~root",
		"a\x00b",
		"a b",
		"a\tb",
		"a\nb",
	} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) was accepted; it must not be usable as a directory name", id)
		}
	}
}

func TestUpperCaseIsRefusedRatherThanFolded(t *testing.T) {
	// On a case-insensitive filesystem, Acme and acme are one directory while the
	// database treats them as two rows. That discrepancy is a cross-tenant data
	// leak on any Mac or Windows host, so it is refused rather than normalised.
	for _, id := range []ID{"Acme", "ACME", "acmeHealth", "St-Josephs"} {
		err := ValidateID(id)
		if err == nil {
			t.Fatalf("ValidateID(%q) was accepted", id)
		}
		if !strings.Contains(err.Error(), "lower case") {
			t.Errorf("ValidateID(%q) should explain the case rule, got: %v", id, err)
		}
	}
}

func TestReservedNamesAreRefused(t *testing.T) {
	// Each of these would collide with a built-in route or directory, and the
	// collision would present as a tenant that intermittently does not exist.
	for _, id := range []ID{"api", "static", "metrics", "livez", "readyz", "admin", "platform", "_platform", "tenants", "default"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("reserved name %q was accepted", id)
		}
	}
}

func TestEmptyAndOverlongIDsAreRefused(t *testing.T) {
	if err := ValidateID(""); err == nil {
		t.Error("an empty id was accepted")
	}
	long := ID(strings.Repeat("a", maxIDLength+1))
	if err := ValidateID(long); err == nil {
		t.Error("an overlong id was accepted")
	}
}

func TestHyphenPlacementRules(t *testing.T) {
	for _, id := range []ID{"-acme", "acme-", "-", "--"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) was accepted", id)
		}
	}
}

func TestDoubleHyphenIsRefusedForLegibility(t *testing.T) {
	// Not a safety rule. acme--west and acme-west differ by a character nobody
	// notices, and confusing two tenants is the worst mistake available here.
	err := ValidateID("acme--west")
	if err == nil {
		t.Fatal("a double hyphen was accepted")
	}
	if !strings.Contains(err.Error(), "confuse") {
		t.Errorf("the error should explain why, got: %v", err)
	}
}

func TestPlatformCannotBeCreatedAsATenant(t *testing.T) {
	// The platform pseudo-tenant exists so that "who may create tenants" is not
	// answered by "any admin of any tenant". Letting somebody create it as a real
	// tenant would hand them every other customer's data.
	tn := &Tenant{ID: Platform, Name: "Platform"}
	if err := tn.Validate(); err == nil {
		t.Error("the platform pseudo-tenant was accepted as a real tenant")
	}
	if !IsPlatform(Platform) {
		t.Error("IsPlatform did not recognise the platform id")
	}
	if IsPlatform("acme") {
		t.Error("IsPlatform matched a real tenant")
	}
}

func TestTenantNeedsADisplayName(t *testing.T) {
	// An id alone is not enough for somebody reading a list of forty.
	for _, name := range []string{"", "   ", "\t"} {
		tn := &Tenant{ID: "acme", Name: name}
		if err := tn.Validate(); err == nil {
			t.Errorf("a tenant with name %q was accepted", name)
		}
	}
}

func TestTenantValidates(t *testing.T) {
	tn := &Tenant{ID: "acme-health", Name: "Acme Health System"}
	if err := tn.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestOverlongDisplayNameIsRefused(t *testing.T) {
	tn := &Tenant{ID: "acme", Name: strings.Repeat("x", 201)}
	if err := tn.Validate(); err == nil {
		t.Error("an overlong display name was accepted")
	}
}
