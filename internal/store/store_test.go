package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// PBKDF2 at production cost takes a few hundred milliseconds per hash by design.
// Tests lower it so the suite stays fast; TestProductionHashCost checks the real
// value has not been weakened.
func TestMain(m *testing.M) {
	HashIterations = 4096
	m.Run()
}

func TestProductionHashCost(t *testing.T) {
	if DefaultIterations < 600_000 {
		t.Errorf("DefaultIterations = %d, want at least 600000 (the OWASP recommendation for PBKDF2-HMAC-SHA256)",
			DefaultIterations)
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}

	// The plaintext must not survive anywhere in the stored form.
	if strings.Contains(hash, "correct") {
		t.Fatal("the stored hash contains the password")
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Errorf("hash format = %q", hash)
	}

	if ok, _ := VerifyPassword(hash, "correct horse battery staple"); !ok {
		t.Error("the correct password did not verify")
	}
	if ok, _ := VerifyPassword(hash, "wrong horse battery staple"); ok {
		t.Error("a wrong password verified")
	}
}

func TestPasswordHashesAreSalted(t *testing.T) {
	// Two users with the same password must not share a hash, or the database
	// reveals which accounts to attack together.
	a, err := HashPassword("the same password twice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("the same password twice")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("identical passwords produced identical hashes; the salt is not working")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	if _, err := HashPassword("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("err = %v, want ErrPasswordTooShort", err)
	}
}

func TestOldHashStillVerifiesAndAsksForUpgrade(t *testing.T) {
	// Raising the cost later must not lock anybody out. An old hash keeps
	// verifying with the cost it was made with, and reports that it should be
	// rewritten.
	saved := HashIterations
	HashIterations = 1024
	old, err := HashPassword("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	HashIterations = saved

	ok, needsRehash := VerifyPassword(old, "a sufficiently long password")
	if !ok {
		t.Fatal("an older hash stopped verifying after the cost was raised")
	}
	if !needsRehash {
		t.Error("needsRehash = false for a hash made at a lower cost")
	}
}

func TestMalformedHashDoesNotVerify(t *testing.T) {
	for _, bad := range []string{
		"", "plaintext", "pbkdf2-sha256$notanumber$c2FsdA$a2V5",
		"pbkdf2-sha256$1000$!!!$a2V5", "bcrypt$10$whatever",
		"pbkdf2-sha256$1000$c2FsdA", // too few fields
	} {
		if ok, _ := VerifyPassword(bad, "anything"); ok {
			t.Errorf("VerifyPassword accepted the malformed hash %q", bad)
		}
	}
}

func TestCreateAndGetUser(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 || u.Username != "testuser" || u.Role != RoleAdmin {
		t.Errorf("user = %+v", u)
	}

	got, err := s.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("GetUser returned id %d, want %d", got.ID, u.ID)
	}

	// Usernames are case insensitive, because expecting an operator to remember
	// the capitalisation they signed up with is a support ticket.
	if _, err := s.GetUser(ctx, "TESTUSER"); err != nil {
		t.Errorf("lookup by different case failed: %v", err)
	}
	if _, err := s.CreateUser(ctx, "TESTUSER", "another long enough password", RoleViewer); !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate for a name differing only in case", err)
	}
}

func TestUnknownUser(t *testing.T) {
	s := open(t)
	if _, err := s.GetUser(context.Background(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestInvalidRoleRejected(t *testing.T) {
	s := open(t)
	if _, err := s.CreateUser(context.Background(), "x", "a long enough password", Role("superuser")); err == nil {
		t.Error("an unknown role was accepted")
	}
}

func TestAuthenticateAndLookup(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleEditor); err != nil {
		t.Fatal(err)
	}

	token, u, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "10.0.0.1", "test")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if token == "" {
		t.Fatal("no session token issued")
	}
	if u.Role != RoleEditor {
		t.Errorf("role = %q", u.Role)
	}

	sess, err := s.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if sess.Username != "testuser" || sess.Role != RoleEditor {
		t.Errorf("session = %+v", sess)
	}

	// last_login is recorded, which is the cheapest way to spot an account
	// nobody uses.
	after, err := s.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	if after.LastLogin == nil {
		t.Error("last_login was not recorded")
	}
}

func TestTokenIsNotStoredInTheDatabase(t *testing.T) {
	// A leaked copy of the database must not hand over live sessions.
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", "")
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := s.DB().QueryRowContext(ctx, `SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("the session token itself is stored; only its hash should be")
	}
	if strings.Contains(stored, token) {
		t.Fatal("the stored value contains the token")
	}
}

func TestWrongPasswordAndUnknownUserAreIndistinguishable(t *testing.T) {
	// The error must not reveal whether an account exists, or the login form
	// becomes an account enumerator.
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleViewer); err != nil {
		t.Fatal(err)
	}

	_, _, wrongPass := s.Authenticate(ctx, "testuser", "not the right password", "", "")
	_, _, noSuchUser := s.Authenticate(ctx, "nobody", "not the right password", "", "")

	if !errors.Is(wrongPass, ErrInvalidCredentials) {
		t.Errorf("wrong password gave %v", wrongPass)
	}
	if !errors.Is(noSuchUser, ErrInvalidCredentials) {
		t.Errorf("unknown user gave %v", noSuchUser)
	}
	if wrongPass.Error() != noSuchUser.Error() {
		t.Errorf("the two cases are distinguishable: %q versus %q", wrongPass, noSuchUser)
	}
}

func TestDisabledAccountCannotLogInAndIsLoggedOut(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}

	// Disabling must take effect immediately, not whenever the cookie expires.
	if _, err := s.Lookup(ctx, token); err == nil {
		t.Error("an existing session still worked after the account was disabled")
	}
	if _, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Age the session past its expiry.
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE user_id = ?`,
		formatTime(time.Now().Add(-time.Hour)), u.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Lookup(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for an expired session", err)
	}

	// And it is cleaned up on the way past rather than accumulating.
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d expired sessions left behind", n)
	}
}

func TestSessionIsExtendedOnUse(t *testing.T) {
	// An operator part way through a task should not be logged out.
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleViewer); err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", "")
	if err != nil {
		t.Fatal(err)
	}

	first, err := s.Lookup(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := s.Lookup(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	if !second.ExpiresAt.After(first.ExpiresAt) {
		t.Error("the expiry was not extended on use")
	}
}

func TestLogout(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleViewer); err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound after logging out", err)
	}
}

func TestRoleHierarchy(t *testing.T) {
	cases := []struct {
		have, need Role
		want       bool
	}{
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleEditor, true},
		{RoleAdmin, RoleViewer, true},
		{RoleEditor, RoleAdmin, false},
		{RoleEditor, RoleEditor, true},
		{RoleEditor, RoleViewer, true},
		{RoleViewer, RoleEditor, false},
		{RoleViewer, RoleViewer, true},
		{Role("nonsense"), RoleViewer, false},
	}
	for _, c := range cases {
		if got := c.have.AtLeast(c.need); got != c.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestCountAdmins(t *testing.T) {
	// The API uses this to refuse the change that locks everybody out.
	s := open(t)
	ctx := context.Background()

	a, err := s.CreateUser(ctx, "admin1", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "viewer", "a sufficiently long password", RoleViewer); err != nil {
		t.Fatal(err)
	}

	if n, _ := s.CountAdmins(ctx); n != 1 {
		t.Errorf("CountAdmins = %d, want 1", n)
	}

	// A disabled administrator does not count, because they cannot log in.
	if err := s.SetDisabled(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountAdmins(ctx); n != 0 {
		t.Errorf("CountAdmins = %d after disabling the only admin, want 0", n)
	}
}

func TestAuditTrail(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	entries := []AuditEntry{
		{Username: "testuser", Action: "channel.create", Target: "adt-inbound", IP: "10.0.0.1"},
		{Username: "testuser", Action: "channel.update", Target: "adt-inbound", Detail: "changed the filter"},
		{Username: "someone", Action: "login", IP: "10.0.0.2"},
	}
	for _, e := range entries {
		if err := s.Audit(ctx, e); err != nil {
			t.Fatalf("Audit: %v", err)
		}
	}

	got, err := s.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	// Newest first, because that is what anyone opening an audit log wants.
	if got[0].Action != "login" {
		t.Errorf("first entry = %q, want the newest", got[0].Action)
	}
	if got[0].At.IsZero() {
		t.Error("the timestamp was not set")
	}
}

func TestAuditSurvivesUserDeletion(t *testing.T) {
	// Removing an account must not erase the record of what it did.
	s := open(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "temp", "a sufficiently long password", RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Audit(ctx, AuditEntry{Username: "temp", Action: "channel.delete", Target: "old-feed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Username != "temp" {
		t.Errorf("audit trail after deleting the user = %+v", got)
	}
}

func TestDeletingUserRemovesSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Lookup(ctx, token); err == nil {
		t.Error("the session outlived the account")
	}
}

func TestPurgeExpiredSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE user_id = ?`,
		formatTime(time.Now().Add(-time.Hour)), u.ID); err != nil {
		t.Fatal(err)
	}

	n, err := s.PurgeExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d sessions, want 1", n)
	}
}

func TestSetPasswordAndRole(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetPassword(ctx, u.ID, "an entirely different password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, "testuser", "a sufficiently long password", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("the old password still works")
	}
	if _, _, err := s.Authenticate(ctx, "testuser", "an entirely different password", "", ""); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}

	if err := s.SetRole(ctx, u.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != RoleAdmin {
		t.Errorf("role = %q, want admin", got.Role)
	}
}

func TestOperationsOnMissingUser(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.SetRole(ctx, 999, RoleAdmin); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRole on a missing user = %v", err)
	}
	if err := s.SetDisabled(ctx, 999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetDisabled on a missing user = %v", err)
	}
	if err := s.DeleteUser(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteUser on a missing user = %v", err)
	}
}

func TestGeneratedPassword(t *testing.T) {
	// A generated password printed once beats a documented default, which is
	// what actually gets left in place on an internet-facing box.
	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) < MinPasswordLength {
			t.Fatalf("generated a password shorter than the minimum: %d", len(p))
		}
		if seen[p] {
			t.Fatal("generated the same password twice")
		}
		seen[p] = true

		if _, err := HashPassword(p); err != nil {
			t.Fatalf("a generated password was rejected by HashPassword: %v", err)
		}
	}
}

func TestUserCount(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if n, _ := s.UserCount(ctx); n != 0 {
		t.Errorf("UserCount = %d on a fresh database, want 0", n)
	}
	if _, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UserCount(ctx); n != 1 {
		t.Errorf("UserCount = %d, want 1", n)
	}
}

func TestListUsersDoesNotLeakHashes(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "testuser", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("got %d users, want 1", len(users))
	}

	// The hash is held on the unexported field so it cannot be serialised to a
	// client by accident.
	if users[0].hash == "" {
		t.Error("the hash was not loaded at all, so verification would fail")
	}
}
