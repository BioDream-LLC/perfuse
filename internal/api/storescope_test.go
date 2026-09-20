package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestNoHandlerReadsTheUnscopedStore is a drift guard, and it exists because of a leak.
//
// The audit handler called s.Store.ListAudit, which returns every tenant's rows. A tenant administrator could read another
// organisation's usernames and everything they had changed. The store had the correct scoped accessor the whole time - the
// handler simply did not use it, and nothing made it.
//
// That is the pattern worth guarding rather than the single line. Channels and runtimes already had mandatory accessors
// with drift guards for exactly this reason; the store did not, so scoping was something each handler had to remember, and
// one did not remember.
//
// The guard is textual because the property is textual: a handler should reach the store through storeFor or, when crossing
// tenants is deliberate, through crossTenantStore - which is named so that it stands out in a diff.
func TestNoHandlerReadsTheUnscopedStore(t *testing.T) {
	// Methods that read or write per-tenant data. Calling these on the unscoped store returns or changes every
	// tenant's, which is the leak.
	//
	// Deliberately a list of what is dangerous rather than of what is allowed. A new method on the store is far more
	// likely to be per-tenant than not, so the failure mode of an incomplete list should be a false pass on something
	// obscure rather than a false failure on everything.
	// Matched as prefixes rather than exact names, because the list missed GetUserByID: it said "GetUser" and the
	// method is GetUserByID, so an exact comparison found nothing and the guard passed while the leak was present.
	// A prefix catches the family, and the family is what is dangerous.
	dangerous := []string{
		"ListAudit",
		"ListUsers",
		"CreateUser",
		"UpdateUser",
		"DeleteUser",
		"GetUser",
		"SetRole",
		"SetDisabled",
		"SetPassword",
		"CountAdmins",
		"Authenticate",
		"ListAPIToken",
		"CreateAPIToken",
		"RevokeAPIToken",
	}

	// Files exempt with a reason, each one an explicit decision.
	exempt := map[string]string{
		"storefor.go": "defines the accessors",
		"server.go": "handleLogin authenticates before a session exists, so there is no tenant to scope to yet - " +
			"the store resolves it from the credentials",
		"oidc.go": "federated sign-in happens before a session exists, for the same reason",
		"ldap.go": "directory sign-in happens before a session exists, for the same reason",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	// Matches a call on the unscoped store, such as s.Store.ListAudit(
	pattern := regexp.MustCompile(`s\.Store\.([A-Z][A-Za-z0-9]*)\(`)

	var problems []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, ok := exempt[name]; ok {
			continue
		}

		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}

		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			method := match[1]
			for _, bad := range dangerous {
				if strings.HasPrefix(method, bad) {
					problems = append(problems,
						name+" calls s.Store."+method+" directly")
				}
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("these handlers read or write per-tenant data through the unscoped store:\n  %s\n\n"+
			"Use s.storeFor(sess), which scopes to the caller's tenant, or s.crossTenantStore(sess) when "+
			"seeing every tenant is deliberate and the caller is a platform account. The audit log leaked "+
			"across tenants for exactly this reason.\n\nIf a file legitimately runs before a session exists, "+
			"add it to the exempt list in this test with the reason.",
			strings.Join(problems, "\n  "))
	}
}

// TestTheExemptionsStillExist stops the exempt list going stale.
//
// A file removed or renamed would leave an exemption covering nothing, and the next file to take that name would inherit
// it silently.
func TestTheExemptionsStillExist(t *testing.T) {
	for _, name := range []string{"storefor.go", "server.go", "oidc.go", "ldap.go"} {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s is exempt from the unscoped-store guard and does not exist: %v", name, err)
		}
	}
}
