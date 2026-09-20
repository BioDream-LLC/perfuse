package store

import (
	"testing"
	"time"
)

// TestDisablingAnAccountEndsItsSessions pins a property other code depends on.
//
// SCIM deprovisioning, the console's disable button and an administrator revoking access all rely on this: marking an account
// disabled has to end its sessions in the same operation. A browser tab open on a laptop that has gone home with a terminated
// employee must stop working now, and a session that survives until its expiry is a session that survives the sacking.
//
// Written because the SCIM handler originally called DeleteUserSessions again afterwards, which looked like the protection and
// was not. Removing that changed no test - the store was already doing it. So the property is asserted where it lives, and a
// future change to SetDisabled that drops it fails here rather than silently leaving terminated employees signed in.
func TestDisablingAnAccountEndsItsSessions(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const password = "a sufficiently long password"
	u, err := s.CreateUser(t.Context(), "leaver", password, RoleEditor)
	if err != nil {
		t.Fatal(err)
	}

	token, _, err := s.Authenticate(t.Context(), "leaver", password, "10.0.0.1", "browser")
	if err != nil {
		t.Fatal(err)
	}

	// The session works, so the assertion below means something.
	if _, err := s.Lookup(t.Context(), token); err != nil {
		t.Fatalf("the session did not work to begin with: %v", err)
	}

	if err := s.SetDisabled(t.Context(), u.ID, true); err != nil {
		t.Fatal(err)
	}

	// The row count comes FIRST, before any lookup.
	//
	// This ordering is the whole test. Lookup deletes a session when it finds the account disabled - which is good
	// defence and made an earlier version of this test useless: checking the session deleted it, so the count that
	// followed was zero whether or not SetDisabled had done anything. The test passed with the session deletion removed
	// from SetDisabled entirely.
	//
	// An observer effect, and not one I would have found by reading. Planting the bug and watching the test stay green
	// is what exposed it.
	remaining, err := s.ScopeUnchecked(DefaultTenant).CountUserSessions(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("%d session(s) remain in the table after the account was disabled; the row is what makes the "+
			"revocation real, rather than relying on every future lookup to notice", remaining)
	}

	// And the session must not resolve. This is the second line of defence rather than the first: Lookup refuses a
	// disabled account independently, so access would end here even if the row survived.
	if _, err := s.Lookup(t.Context(), token); err == nil {
		t.Error("the session still resolves after the account was disabled, so a terminated employee keeps access")
	}
}

// TestEnablingAnAccountDoesNotCreateASession is the other direction.
//
// Re-enabling somebody returning from leave must not resurrect the sessions they had before, because those tokens may have
// been captured while the account was disabled.
func TestEnablingAnAccountDoesNotCreateASession(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const password = "a sufficiently long password"
	u, err := s.CreateUser(t.Context(), "returner", password, RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Authenticate(t.Context(), "returner", password, "10.0.0.1", "browser")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetDisabled(t.Context(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisabled(t.Context(), u.ID, false); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Lookup(t.Context(), token); err == nil {
		t.Error("a session from before the account was disabled works again after re-enabling it")
	}
}

// TestTheSessionLifetimeIsReadWhenTheSessionIsMade covers the live setting.
//
// The point is that no read is left on the constant. The retention window had exactly this shape - one value, two read
// sites - and when it became editable only one site was converted, so changing it in the interface moved one cutoff and
// silently deleted content. Both a new session and a refreshed one are checked here for that reason.
func TestTheSessionLifetimeIsReadWhenTheSessionIsMade(t *testing.T) {
	s := open(t)
	if _, err := s.CreateUser(t.Context(), "rturner", "correct-horse-battery", RoleEditor); err != nil {
		t.Fatal(err)
	}

	// Deliberately not 12, so a leftover read of the constant is visible.
	s.SessionLifetimeFn = func() time.Duration { return 72 * time.Hour }

	token, _, err := s.Authenticate(t.Context(), "rturner", "correct-horse-battery", "10.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}

	expiry := func() time.Time {
		t.Helper()
		var raw string
		if err := s.db.QueryRowContext(t.Context(),
			`SELECT expires_at FROM sessions`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return parseTime(raw)
	}

	// Two hours of slack, which is far less than the gap between 12 and 72 and far more than the test takes.
	if got := time.Until(expiry()); got < 70*time.Hour {
		t.Errorf("a new session expires in %v, want about 72h - a read of the constant survives somewhere",
			got.Round(time.Hour))
	}

	// A refresh has its own read site, and it is the one most easily missed.
	s.SessionLifetimeFn = func() time.Duration { return 200 * time.Hour }
	if _, err := s.Lookup(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if got := time.Until(expiry()); got < 198*time.Hour {
		t.Errorf("a refreshed session expires in %v, want about 200h - the refresh path is not reading the "+
			"live value", got.Round(time.Hour))
	}
}
