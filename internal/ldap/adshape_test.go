package ldap

import (
	"context"
	"strings"
	"testing"
)

// TestTheActiveDirectoryShape covers a directory that keeps group membership on the person.
//
// Directories are built both ways round and reading only one finds nothing on the other:
//
//   - Active Directory puts memberOf on the person, listing the groups they are in.
//   - OpenLDAP with groupOfNames puts member on the group, listing the people in it.
//
// The consequence of supporting only one is quiet. No groups means no mapped role, which refuses the person with a
// message about their permissions - so an administrator reads it as "this person is in the wrong group" and goes looking
// in the directory rather than at the configuration.
//
// This runs against a real memberOf maintained by OpenLDAP's memberof overlay rather than a stand-in attribute, so the
// attribute really is operational and really is written by the server rather than by the seed file. An earlier version of
// this test used description as a placeholder, which would have passed without proving the attribute behaves like the
// real thing.
func TestTheActiveDirectoryShape(t *testing.T) {
	cfg := &Config{
		Addr:              addr(t),
		Insecure:          true,
		BindDN:            testAdminDN,
		BindPassword:      testAdminPW,
		UserBaseDN:        testPeople,
		UsernameAttribute: "uid",
		NameAttribute:     "cn",
		EmailAttribute:    "mail",
		// The AD half only. No GroupBaseDN, so if reading memberOf does not work there is nothing else to find
		// the group and the test fails rather than passing on the other strategy.
		MemberOfAttribute: "memberOf",
		Roles: map[string]string{
			"perfuse-editors": "editor",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configuration: %v", err)
	}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.Authenticate(context.Background(), "kpatel", "editor-pass")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if len(id.Groups) != 1 || id.Groups[0] != "perfuse-editors" {
		t.Fatalf("groups are %v, want exactly perfuse-editors read from memberOf", id.Groups)
	}

	// The group came back as a full DN and has to become a name, or every role mapping would have to spell out a
	// container path that changes the moment somebody reorganises the directory.
	role, ok := cfg.RoleFor(id.Groups)
	if !ok || role != "editor" {
		t.Errorf("role is %q (mapped %v), want editor", role, ok)
	}
}

// TestBothStrategiesTogetherDoNotDoubleCount covers a directory where both work.
//
// Some directories support both, and a person found twice must not appear to be in two groups - which would be harmless
// for role mapping and confusing in a log line that says how many groups somebody has.
func TestBothStrategiesTogetherDoNotDoubleCount(t *testing.T) {
	cfg := &Config{
		Addr:               addr(t),
		Insecure:           true,
		BindDN:             testAdminDN,
		BindPassword:       testAdminPW,
		UserBaseDN:         testPeople,
		UsernameAttribute:  "uid",
		MemberOfAttribute:  "memberOf",
		GroupBaseDN:        "ou=groups," + testBase,
		GroupFilter:        "(&(objectClass=groupOfNames)(member=%d))",
		GroupNameAttribute: "cn",
		Roles:              map[string]string{"perfuse-editors": "editor"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configuration: %v", err)
	}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.Authenticate(context.Background(), "kpatel", "editor-pass")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if len(id.Groups) != 1 {
		t.Errorf("groups are %v; the same group found by both strategies should appear once", id.Groups)
	}
}

// TestAStableIdentifierSurvivesAMove covers the reason unique_id_attribute exists.
//
// A DN is not an identity. Moving somebody between organisational units, or a name change that alters their CN, changes
// it - and identifying a person by DN means their next sign-in looks like a different person, creates a second account,
// and orphans whatever was attached to the first.
//
// entryUUID here, which is OpenLDAP's operational equivalent of Active Directory's objectGUID.
func TestAStableIdentifierSurvivesAMove(t *testing.T) {
	cfg := &Config{
		Addr:              addr(t),
		Insecure:          true,
		BindDN:            testAdminDN,
		BindPassword:      testAdminPW,
		UserBaseDN:        testPeople,
		UsernameAttribute: "uid",
		MemberOfAttribute: "memberOf",
		UniqueIDAttribute: "entryUUID",
		Roles:             map[string]string{"perfuse-editors": "editor"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configuration: %v", err)
	}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.Authenticate(context.Background(), "kpatel", "editor-pass")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if id.UniqueID == "" {
		t.Fatal("no unique identifier was read, so a person would be identified by a DN that changes when " +
			"they move")
	}
	if id.StableID() != id.UniqueID {
		t.Errorf("StableID is %q, want the unique identifier %q", id.StableID(), id.UniqueID)
	}
	// A UUID rather than a DN, checked loosely: the point is that it is not the DN.
	if id.StableID() == id.DN {
		t.Error("the stable identifier is the DN, which is exactly what it exists to avoid")
	}
}

// TestAMissingUniqueIDIsRefused covers a wrong attribute name.
//
// Falling back to the DN silently would give an administrator who asked for stable identities something else, and the
// consequence - duplicate accounts after a directory reorganisation - would arrive months later with nothing connecting
// it to this configuration.
func TestAMissingUniqueIDIsRefused(t *testing.T) {
	cfg := &Config{
		Addr:              addr(t),
		Insecure:          true,
		BindDN:            testAdminDN,
		BindPassword:      testAdminPW,
		UserBaseDN:        testPeople,
		UsernameAttribute: "uid",
		MemberOfAttribute: "memberOf",
		UniqueIDAttribute: "objectGUID", // Active Directory's name for it, which OpenLDAP does not have.
		Roles:             map[string]string{"perfuse-editors": "editor"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configuration: %v", err)
	}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.Authenticate(context.Background(), "kpatel", "editor-pass")
	if err == nil {
		t.Fatal("a missing unique identifier was accepted, so this person would be identified by their DN")
	}
	if !strings.Contains(err.Error(), "no stable identifier") {
		t.Errorf("the refusal should say there is no stable identifier, got: %v", err)
	}
}
