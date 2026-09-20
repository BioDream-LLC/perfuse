package ldap

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Tests run against a real directory. Set PERFUSE_LDAP_ADDR to run them.
//
// A real server rather than a hand-written fixture, because every interesting failure in this package is about what a
// directory actually does rather than what the specification says it may do - and the two differ in the case that
// matters most, below.
func addr(t *testing.T) string {
	t.Helper()
	a := os.Getenv("PERFUSE_LDAP_ADDR")
	if a == "" {
		t.Skip("set PERFUSE_LDAP_ADDR to run the directory tests")
	}
	return a
}

const (
	testBase     = "dc=perfuse,dc=test"
	testPeople   = "ou=people,dc=perfuse,dc=test"
	testAdminDN  = "cn=admin,dc=perfuse,dc=test"
	testAdminPW  = "adminsecret"
	testUserDN   = "uid=rturner,ou=people,dc=perfuse,dc=test"
	testUserPW   = "correct-horse"
	testViewerDN = "uid=dwhite,ou=people,dc=perfuse,dc=test"
)

// dial opens an insecure connection, which the test directory is.
//
// Insecure is spelled out at every call site rather than hidden in a helper default, so that a reader of these tests can
// see this is a local server with invented people in it and not a pattern to copy.
func dial(t *testing.T) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), Options{
		Addr:     addr(t),
		Insecure: true,
		Timeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestAnEmptyPasswordIsRefusedEvenWhenTheDirectorySaysYes is the most important test here.
//
// RFC 4513 says a simple bind with an empty password is an anonymous bind. A directory configured to allow it answers
// with success, so a client that only asks "did the bind succeed" authenticates as any account whose name somebody can
// type, with no password at all.
//
// The test directory this runs against is deliberately configured with OpenLDAP's allow bind_anon_dn, which makes it
// answer exactly that way - verified by hand with ldapwhoami, which returns "anonymous" and a success result rather than
// an error. Without that setting OpenLDAP 2.7 refuses the bind itself, and then this test would pass with the check in
// Bind deleted: the server would be doing the work and the test could not tell the difference.
//
// That distinction is the whole point. This asserts that the refusal happens here, before a byte is sent, and does not
// depend on the directory being configured well.
func TestAnEmptyPasswordIsRefusedEvenWhenTheDirectorySaysYes(t *testing.T) {
	c := dial(t)

	err := c.Bind(testUserDN, "")
	if err == nil {
		t.Fatal("an empty password was accepted; a directory that allows unauthenticated binds would have let " +
			"anyone in as this user")
	}

	// The error has to come from this package rather than from the directory, or the test proves nothing about the
	// code under test. An LDAP result code means the server refused and this check is not doing its job.
	var ldapErr *Error
	if errors.As(err, &ldapErr) {
		t.Fatalf("the refusal came from the directory (result %d), not from Bind; with a permissive directory "+
			"this would have succeeded", ldapErr.Code)
	}

	if !strings.Contains(err.Error(), "anonymous bind") {
		t.Errorf("the refusal should explain what an empty password means to a directory, got: %v", err)
	}

	// The connection must still work afterwards. A refusal that left the connection unusable would turn one
	// mistyped password into a broken sign-in for everybody else on a pooled connection.
	if err := c.Bind(testUserDN, testUserPW); err != nil {
		t.Errorf("the connection was unusable after the refusal: %v", err)
	}
}

// TestTheDirectoryReallyAllowsTheUnauthenticatedBind proves the previous test is testing something.
//
// If this fails, the test directory has been reconfigured and the empty-password test above is no longer meaningful -
// it would be passing because the server refuses rather than because Bind does.
//
// This is here because of a habit that has caught five false-passing guards on this project: prove the hazard exists
// before trusting a test that defends against it.
func TestTheDirectoryReallyAllowsTheUnauthenticatedBind(t *testing.T) {
	c := dial(t)

	// Sent by hand, bypassing Bind's refusal, which is the only way to ask the directory what it would do.
	id := c.nextID.Add(1)
	if err := c.write(buildBindRequest(id, testUserDN, "")); err != nil {
		t.Fatalf("write: %v", err)
	}

	msg, err := c.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body, err := msg.child(1)
	if err != nil {
		t.Fatal(err)
	}

	if err := parseResult(body); err != nil {
		t.Skipf("this directory refuses unauthenticated binds itself (%v), so the empty-password test above "+
			"cannot distinguish Bind's refusal from the server's; configure allow bind_anon_dn to make it "+
			"meaningful", err)
	}

	t.Log("the directory accepted a bind with a real DN and no password, which is why Bind refuses it first")
}

// TestBindWithTheRightPassword covers the ordinary case.
func TestBindWithTheRightPassword(t *testing.T) {
	c := dial(t)
	if err := c.Bind(testUserDN, testUserPW); err != nil {
		t.Fatalf("Bind with the correct password: %v", err)
	}
}

// TestBindWithTheWrongPassword covers a refusal that has to be distinguishable from an outage.
func TestBindWithTheWrongPassword(t *testing.T) {
	c := dial(t)

	err := c.Bind(testUserDN, "not-the-password")
	if err == nil {
		t.Fatal("the wrong password was accepted")
	}

	var ldapErr *Error
	if !errors.As(err, &ldapErr) {
		t.Fatalf("a wrong password should give an LDAP result, got %T: %v", err, err)
	}
	if !ldapErr.IsInvalidCredentials() {
		t.Errorf("result code %d, want 49 invalid credentials; a caller uses this to tell a wrong password "+
			"from a directory that is down", ldapErr.Code)
	}
}

// TestSearchFindsAPersonByUID covers the lookup that turns a typed username into a DN.
func TestSearchFindsAPersonByUID(t *testing.T) {
	c := dial(t)
	if err := c.Bind(testAdminDN, testAdminPW); err != nil {
		t.Fatalf("bind as the service account: %v", err)
	}

	entries, err := c.Search(SearchRequest{
		BaseDN:     testPeople,
		Scope:      ScopeSubtree,
		Filter:     "(uid=" + EscapeFilter("rturner") + ")",
		Attributes: []string{"uid", "cn", "mail"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("found %d entries, want exactly 1", len(entries))
	}
	if entries[0].DN != testUserDN {
		t.Errorf("DN is %q, want %q", entries[0].DN, testUserDN)
	}
	if got := entries[0].Get("cn"); got != "Rosalind Turner" {
		t.Errorf("cn is %q, want Rosalind Turner", got)
	}
	// Case-insensitively, because directories disagree about capitalisation and a config written for one
	// directory must not silently read nothing against another.
	if got := entries[0].Get("MAIL"); got != "rturner@perfuse.test" {
		t.Errorf("mail read case-insensitively is %q, want rturner@perfuse.test", got)
	}
}

// TestAWildcardUsernameCannotMatchEveryone is the filter injection case.
//
// A username of * in a filter of (uid=%s) becomes a presence test that matches every person in the directory. The
// sign-in would then find the first one and check the typed password against whoever that happens to be - which, on a
// directory sorted the wrong way, could be an administrator.
func TestAWildcardUsernameCannotMatchEveryone(t *testing.T) {
	c := dial(t)
	if err := c.Bind(testAdminDN, testAdminPW); err != nil {
		t.Fatalf("bind: %v", err)
	}

	// First: prove the hazard. Unescaped, the wildcard matches more than one person.
	unescaped, err := c.Search(SearchRequest{
		BaseDN:     testPeople,
		Scope:      ScopeSubtree,
		Filter:     "(uid=*)",
		Attributes: []string{"uid"},
	})
	if err != nil {
		t.Fatalf("the unescaped search failed: %v", err)
	}
	if len(unescaped) < 2 {
		t.Fatalf("the directory has %d people in it, so this test cannot show a wildcard matching several; "+
			"it needs at least two", len(unescaped))
	}

	// Then: escaped, the same input finds nobody, because no account is literally named with an asterisk.
	escaped, err := c.Search(SearchRequest{
		BaseDN:     testPeople,
		Scope:      ScopeSubtree,
		Filter:     "(uid=" + EscapeFilter("*") + ")",
		Attributes: []string{"uid"},
	})
	if err != nil {
		t.Fatalf("the escaped search failed: %v", err)
	}
	if len(escaped) != 0 {
		t.Errorf("an escaped asterisk matched %d people; it should match nobody", len(escaped))
	}
}

// TestAFilterCannotBeRestructuredByAUsername covers the other half of injection.
func TestAFilterCannotBeRestructuredByAUsername(t *testing.T) {
	c := dial(t)
	if err := c.Bind(testAdminDN, testAdminPW); err != nil {
		t.Fatalf("bind: %v", err)
	}

	// A username chosen to close the filter and add a condition of its own. Escaped, it is just a strange name.
	hostile := "rturner)(objectClass=*"

	entries, err := c.Search(SearchRequest{
		BaseDN:     testPeople,
		Scope:      ScopeSubtree,
		Filter:     "(uid=" + EscapeFilter(hostile) + ")",
		Attributes: []string{"uid"},
	})
	if err != nil {
		t.Fatalf("the search should be valid and find nobody, got: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a filter-breaking username matched %d entries, want 0", len(entries))
	}
}

// TestGroupMembershipIsReadable covers what roles are mapped from.
func TestGroupMembershipIsReadable(t *testing.T) {
	c := dial(t)
	if err := c.Bind(testAdminDN, testAdminPW); err != nil {
		t.Fatalf("bind: %v", err)
	}

	entries, err := c.Search(SearchRequest{
		BaseDN:     testBase,
		Scope:      ScopeSubtree,
		Filter:     "(&(objectClass=groupOfNames)(member=" + EscapeFilter(testUserDN) + "))",
		Attributes: []string{"cn"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("found %d groups for this person, want 1", len(entries))
	}
	if got := entries[0].Get("cn"); got != "perfuse-admins" {
		t.Errorf("group is %q, want perfuse-admins", got)
	}

	// The other person is in a different group. Asserted because a membership filter that ignored its member
	// condition would return both groups and hand everybody the admin role.
	viewer, err := c.Search(SearchRequest{
		BaseDN:     testBase,
		Scope:      ScopeSubtree,
		Filter:     "(&(objectClass=groupOfNames)(member=" + EscapeFilter(testViewerDN) + "))",
		Attributes: []string{"cn"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(viewer) != 1 || viewer[0].Get("cn") != "perfuse-viewers" {
		t.Errorf("the second person's groups came back as %d entries, want exactly perfuse-viewers", len(viewer))
	}
}

// TestCleartextIsRefusedWithoutSayingSo covers the configuration guard.
func TestCleartextIsRefusedWithoutSayingSo(t *testing.T) {
	_, err := Dial(context.Background(), Options{Addr: addr(t)})
	if err == nil {
		t.Fatal("an unencrypted connection was opened without insecure being set")
	}
	if !strings.Contains(err.Error(), "cleartext") {
		t.Errorf("the refusal should say passwords would be sent in cleartext, got: %v", err)
	}
}

// TestAnEmptyFilterIsRefused covers the filter that would match everything.
func TestAnEmptyFilterIsRefused(t *testing.T) {
	c := dial(t)
	if err := c.Bind(testAdminDN, testAdminPW); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if _, err := c.Search(SearchRequest{BaseDN: testBase, Scope: ScopeSubtree, Filter: ""}); err == nil {
		t.Error("an empty filter was accepted; defaulted to matching everything it would authenticate " +
			"against an arbitrary account")
	}
}
