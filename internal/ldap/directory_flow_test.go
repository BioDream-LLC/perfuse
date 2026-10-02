package ldap

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// testConfig is a configuration for the seeded test directory.
//
// groupOfNames style: the group lists its members, which is how OpenLDAP is usually built. The Active Directory shape -
// memberOf on the person - is covered separately below, because reading only one of the two finds no groups on the other
// kind of directory and refuses everybody.
func testConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{
		Addr:               addr(t),
		Insecure:           true,
		BindDN:             testAdminDN,
		BindPassword:       testAdminPW,
		UserBaseDN:         testPeople,
		UsernameAttribute:  "uid",
		NameAttribute:      "cn",
		EmailAttribute:     "mail",
		GroupBaseDN:        "ou=groups," + testBase,
		GroupFilter:        "(&(objectClass=groupOfNames)(member=%d))",
		GroupNameAttribute: "cn",
		Roles: map[string]string{
			"perfuse-admins":  "admin",
			"perfuse-viewers": "viewer",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the test configuration is not valid: %v", err)
	}
	return cfg
}

func testDirectory(t *testing.T) *Directory {
	t.Helper()
	d, err := NewDirectory(testConfig(t), nil)
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}
	return d
}

// TestAFullSignIn covers search, password check and group read together.
func TestAFullSignIn(t *testing.T) {
	d := testDirectory(t)

	id, err := d.Authenticate(context.Background(), "rturner", testUserPW)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if id.DN != testUserDN {
		t.Errorf("DN is %q, want %q", id.DN, testUserDN)
	}
	if id.DisplayName != "Rosalind Turner" {
		t.Errorf("display name is %q, want Rosalind Turner", id.DisplayName)
	}
	if id.Email != "rturner@perfuse.test" {
		t.Errorf("email is %q, want rturner@perfuse.test", id.Email)
	}

	if len(id.Groups) != 1 || id.Groups[0] != "perfuse-admins" {
		t.Fatalf("groups are %v, want exactly perfuse-admins", id.Groups)
	}

	role, ok := d.cfg.RoleFor(id.Groups)
	if !ok || role != "admin" {
		t.Errorf("role is %q (mapped %v), want admin", role, ok)
	}
}

// TestTwoPeopleGetDifferentRoles is the test that a single-user check cannot replace.
//
// One person mapping to the right role proves nothing about whether the group read is per-person: a group filter that
// ignored its member condition would return every group and hand this same answer to everybody. Two people with
// different memberships is the smallest case that can tell the difference - the same reason the DICOM archive was loaded
// with two studies for one patient.
func TestTwoPeopleGetDifferentRoles(t *testing.T) {
	d := testDirectory(t)

	admin, err := d.Authenticate(context.Background(), "rturner", testUserPW)
	if err != nil {
		t.Fatalf("Authenticate as the admin: %v", err)
	}
	viewer, err := d.Authenticate(context.Background(), "dwhite", "viewer-pass")
	if err != nil {
		t.Fatalf("Authenticate as the viewer: %v", err)
	}

	adminRole, _ := d.cfg.RoleFor(admin.Groups)
	viewerRole, _ := d.cfg.RoleFor(viewer.Groups)

	if adminRole != "admin" {
		t.Errorf("the first person got %q, want admin", adminRole)
	}
	if viewerRole != "viewer" {
		t.Errorf("the second person got %q, want viewer", viewerRole)
	}
	if adminRole == viewerRole {
		t.Error("both people got the same role, so the group read is not per-person")
	}
}

// TestTheWrongPasswordIsRefusedThroughTheWholeFlow covers the path a login form takes.
func TestTheWrongPasswordIsRefusedThroughTheWholeFlow(t *testing.T) {
	d := testDirectory(t)

	_, err := d.Authenticate(context.Background(), "rturner", "not-the-password")
	if err == nil {
		t.Fatal("the wrong password was accepted")
	}

	var ldapErr *Error
	if !errors.As(err, &ldapErr) || !ldapErr.IsInvalidCredentials() {
		t.Errorf("a wrong password should be reportable as invalid credentials, got: %v", err)
	}
}

// TestAnEmptyPasswordIsRefusedByTheFlowToo covers the second of the two refusals.
//
// Bind refuses this as well, and deliberately so: this is the path a login form reaches, and the consequence of it
// getting through is somebody signing in without a password. One of the two checks would be enough right now, and the
// second costs nothing and survives a refactor that moves the first.
func TestAnEmptyPasswordIsRefusedByTheFlowToo(t *testing.T) {
	d := testDirectory(t)

	_, err := d.Authenticate(context.Background(), "rturner", "")
	if err == nil {
		t.Fatal("an empty password was accepted by the sign-in flow")
	}
	if !strings.Contains(err.Error(), "anonymous bind") {
		t.Errorf("the refusal should explain what an empty password means, got: %v", err)
	}
}

// TestAnUnknownPersonLooksLikeAWrongPassword covers account enumeration.
//
// Telling an unauthenticated caller that an account does not exist is how somebody finds out who works at a hospital.
func TestAnUnknownPersonLooksLikeAWrongPassword(t *testing.T) {
	d := testDirectory(t)

	_, err := d.Authenticate(context.Background(), "nobody-here", "any-password")
	if err == nil {
		t.Fatal("an unknown username was accepted")
	}

	var ldapErr *Error
	if !errors.As(err, &ldapErr) || !ldapErr.IsInvalidCredentials() {
		t.Errorf("an unknown account should be indistinguishable from a wrong password, got: %v", err)
	}
}

// TestNoMappedGroupMeansRefused covers the decision not to default to viewer.
func TestNoMappedGroupMeansRefused(t *testing.T) {
	cfg := testConfig(t)
	// A mapping that names a group nobody is in.
	cfg.Roles = map[string]string{"some-other-group": "admin"}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.Authenticate(context.Background(), "rturner", testUserPW)
	if err != nil {
		t.Fatalf("the password is correct, so authentication should succeed: %v", err)
	}

	role, ok := cfg.RoleFor(id.Groups)
	if ok {
		t.Errorf("an unmapped group produced the role %q; it should produce none, and the caller refuses", role)
	}
}

// TestTheStrongestRoleWins covers a person in two mapped groups.
func TestTheStrongestRoleWins(t *testing.T) {
	cfg := testConfig(t)
	cfg.Roles = map[string]string{
		"perfuse-admins":  "admin",
		"perfuse-viewers": "viewer",
	}

	// Order reversed on purpose. Taking the first match would give a different answer for the same person depending
	// on how the directory sorted their groups, and an answer to "what can this person do" that cannot be
	// reproduced is worse than one that is wrong.
	if role, ok := cfg.RoleFor([]string{"perfuse-viewers", "perfuse-admins"}); !ok || role != "admin" {
		t.Errorf("viewer then admin gave %q, want admin", role)
	}
	if role, ok := cfg.RoleFor([]string{"perfuse-admins", "perfuse-viewers"}); !ok || role != "admin" {
		t.Errorf("admin then viewer gave %q, want admin", role)
	}
}

// TestGroupNamesAreMatchedCaseInsensitively covers directories that capitalise differently.
func TestGroupNamesAreMatchedCaseInsensitively(t *testing.T) {
	cfg := testConfig(t)
	cfg.Roles = map[string]string{"PERFUSE-ADMINS": "admin"}

	if role, ok := cfg.RoleFor([]string{"perfuse-admins"}); !ok || role != "admin" {
		t.Errorf("a differently capitalised group gave %q; directories return whatever case a group was "+
			"created with", role)
	}
}

// TestAnAmbiguousUsernameIsRefused covers a search that matches more than one person.
//
// A user filter on cn rather than uid will match several real people in any directory with two Smiths in it. Picking one
// would check the typed password against whichever the directory returned first.
func TestAnAmbiguousUsernameIsRefused(t *testing.T) {
	cfg := testConfig(t)
	// objectClass matches every person in the base, standing in for a badly chosen username attribute.
	cfg.UsernameAttribute = "objectClass"

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.Authenticate(context.Background(), "inetOrgPerson", "any-password")
	if err == nil {
		t.Fatal("an ambiguous username was accepted")
	}
	if !strings.Contains(err.Error(), "match the username") {
		t.Errorf("the refusal should say the username matches several accounts, got: %v", err)
	}
}

// TestGroupNameFromDN covers turning a memberOf value into a name.
func TestGroupNameFromDN(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"CN=Perfuse Admins,OU=Groups,DC=hospital,DC=local", "Perfuse Admins"},
		{"cn=perfuse-admins,ou=groups,dc=perfuse,dc=test", "perfuse-admins"},
		// An escaped comma inside a group name, which unescapes to a real comma: the group is called
		// "Radiology, Nuclear". Splitting on the first comma regardless would give "Radiology" and quietly map
		// a different group, or none at all.
		{"CN=Radiology\\, Nuclear,OU=Groups,DC=x", "Radiology, Nuclear"},
		{"", ""},
	}

	for _, c := range cases {
		if got := groupNameFromDN(c.in); got != c.want {
			t.Errorf("groupNameFromDN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestConfigurationRefusals covers the settings that would fail silently at run time.
func TestConfigurationRefusals(t *testing.T) {
	base := func() *Config {
		return &Config{
			Addr:              "127.0.0.1:389",
			Insecure:          true,
			UserBaseDN:        "dc=x",
			MemberOfAttribute: "memberOf",
			Roles:             map[string]string{"g": "admin"},
		}
	}

	cases := []struct {
		name   string
		change func(*Config)
		expect string
	}{
		{"no address", func(c *Config) { c.Addr = "" }, "addr is required"},
		{"no user base", func(c *Config) { c.UserBaseDN = "" }, "user_base_dn is required"},
		{"no roles", func(c *Config) { c.Roles = nil }, "roles is required"},
		{"not a role", func(c *Config) { c.Roles = map[string]string{"g": "superuser"} }, "not a role"},
		{"cleartext unnamed", func(c *Config) { c.Insecure = false }, "cleartext"},
		{"both tls modes", func(c *Config) { c.TLS = true; c.StartTLS = true; c.Insecure = false }, "cannot both be set"},
		{"bind dn without password", func(c *Config) { c.BindDN = "cn=svc" }, "without bind_password"},
		{"no way to read groups", func(c *Config) { c.MemberOfAttribute = "" }, "member_of_attribute or group_base_dn"},
		{"group base without filter", func(c *Config) { c.GroupBaseDN = "ou=g" }, "without group_filter"},
		{"group filter without a placeholder", func(c *Config) {
			c.GroupBaseDN = "ou=g"
			c.GroupFilter = "(objectClass=groupOfNames)"
		}, "neither %d nor %u"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.change(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("the message should mention %q, got: %v", tc.expect, err)
			}
		})
	}
}
