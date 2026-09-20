package store

import (
	"errors"
	"testing"

	"github.com/biodream-llc/perfuse/internal/tenant"
	"github.com/biodream-llc/perfuse/internal/webauthn"
)

func passkeyStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func sampleCredential(id string) *webauthn.Credential {
	return &webauthn.Credential{
		ID:           []byte(id),
		PublicKey:    []byte{0xa5, 0x01, 0x02},
		Algorithm:    webauthn.AlgES256,
		SignCount:    3,
		AAGUID:       make([]byte, 16),
		UserVerified: true,
		Label:        "MacBook Touch ID",
	}
}

func TestAPasskeyRoundTrips(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	u, err := sc.CreateUser(t.Context(), "rturner", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	if err := sc.AddPasskey(t.Context(), u.ID, sampleCredential("credential-one")); err != nil {
		t.Fatal(err)
	}

	list, err := sc.ListPasskeys(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 passkey, got %d", len(list))
	}

	got := list[0]
	if string(got.ID) != "credential-one" {
		t.Errorf("identifier is %q", got.ID)
	}
	// The algorithm has to survive, because it is what makes algorithm confusion impossible at verification.
	if got.Algorithm != webauthn.AlgES256 {
		t.Errorf("algorithm is %s", got.Algorithm.Name())
	}
	if !got.UserVerified {
		t.Error("the user-verified capability was lost")
	}
	if got.Label != "MacBook Touch ID" {
		t.Errorf("label is %q", got.Label)
	}
	if got.SignCount != 3 {
		t.Errorf("sign count is %d", got.SignCount)
	}

	// And the lookup a sign-in performs, which knows only the identifier.
	credential, userID, tid, err := s.PasskeyByCredentialID(t.Context(), []byte("credential-one"))
	if err != nil {
		t.Fatal(err)
	}
	if userID != u.ID {
		t.Errorf("the credential resolved to account %d rather than %d", userID, u.ID)
	}
	if tid != DefaultTenant {
		t.Errorf("the credential resolved to tenant %q", tid)
	}
	if credential.Algorithm != webauthn.AlgES256 {
		t.Errorf("algorithm from the lookup is %s", credential.Algorithm.Name())
	}
}

// TestTheSameCredentialCannotBeRegisteredTwice covers the integrity attack this table has to resist.
//
// Nothing here is secret, so reading it gains nobody anything. Writing to it is the risk: somebody who could replace the key
// behind an existing credential identifier could put their own passkey on an administrator's account.
func TestTheSameCredentialCannotBeRegisteredTwice(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	victim, err := sc.CreateUser(t.Context(), "admin-person", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := sc.CreateUser(t.Context(), "other-person", "a sufficiently long password", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	if err := sc.AddPasskey(t.Context(), victim.ID, sampleCredential("shared-id")); err != nil {
		t.Fatal(err)
	}

	// A different key under the same identifier, aimed at a different account.
	replacement := sampleCredential("shared-id")
	replacement.PublicKey = []byte{0xa5, 0x09, 0x09}

	err = sc.AddPasskey(t.Context(), attacker.ID, replacement)
	if err == nil {
		t.Fatal("a second credential with the same identifier was accepted, replacing the key behind it")
	}
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("the refusal was %v", err)
	}

	// And the original must be untouched.
	credential, userID, _, err := s.PasskeyByCredentialID(t.Context(), []byte("shared-id"))
	if err != nil {
		t.Fatal(err)
	}
	if userID != victim.ID {
		t.Error("the credential now resolves to a different account")
	}
	if credential.PublicKey[1] != 0x01 {
		t.Error("the stored public key was replaced")
	}
}

// TestOneTenantCannotDeleteAnothersPasskey covers the scoped delete.
//
// Removing somebody's only passkey either locks them out or forces them back onto a password, so knowing an identifier must
// not be enough.
func TestOneTenantCannotDeleteAnothersPasskey(t *testing.T) {
	s := passkeyStore(t)

	if _, err := s.CreateTenant(t.Context(), &tenant.Tenant{ID: "clinic-a", Name: "Clinic A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTenant(t.Context(), &tenant.Tenant{ID: "clinic-b", Name: "Clinic B"}); err != nil {
		t.Fatal(err)
	}

	alpha := s.ScopeUnchecked("clinic-a")
	beta := s.ScopeUnchecked("clinic-b")

	person, err := alpha.CreateUser(t.Context(), "person", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := alpha.AddPasskey(t.Context(), person.ID, sampleCredential("alpha-key")); err != nil {
		t.Fatal(err)
	}

	// Beta knows the identifier and the account number, and must still fail.
	if err := beta.DeletePasskey(t.Context(), person.ID, []byte("alpha-key")); err == nil {
		t.Error("another tenant deleted a passkey")
	}

	if n, err := alpha.CountPasskeys(t.Context(), person.ID); err != nil || n != 1 {
		t.Errorf("the passkey count is %d (err %v)", n, err)
	}

	// The owner can.
	if err := alpha.DeletePasskey(t.Context(), person.ID, []byte("alpha-key")); err != nil {
		t.Errorf("the owning tenant could not delete its own passkey: %v", err)
	}
	if n, _ := alpha.CountPasskeys(t.Context(), person.ID); n != 0 {
		t.Errorf("the passkey count is %d after deletion", n)
	}
}

// TestAPasskeyCannotBeAddedToAnotherTenantsAccount covers the write path.
func TestAPasskeyCannotBeAddedToAnotherTenantsAccount(t *testing.T) {
	s := passkeyStore(t)

	if _, err := s.CreateTenant(t.Context(), &tenant.Tenant{ID: "clinic-a", Name: "Clinic A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTenant(t.Context(), &tenant.Tenant{ID: "clinic-b", Name: "Clinic B"}); err != nil {
		t.Fatal(err)
	}

	alpha := s.ScopeUnchecked("clinic-a")
	beta := s.ScopeUnchecked("clinic-b")

	person, err := alpha.CreateUser(t.Context(), "person", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	// Beta adding a passkey to alpha's administrator would be a complete account takeover.
	if err := beta.AddPasskey(t.Context(), person.ID, sampleCredential("planted")); err == nil {
		t.Error("a passkey was added to another tenant's account, which is an account takeover")
	}
}

// TestDeletingAnAccountTakesItsPasskeys covers the cascade.
//
// A credential outliving its account authenticates nobody, and a lookup finding one would have to decide what to do - a
// decision better not to have.
func TestDeletingAnAccountTakesItsPasskeys(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	u, err := sc.CreateUser(t.Context(), "leaver", "a sufficiently long password", RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.AddPasskey(t.Context(), u.ID, sampleCredential("leaver-key")); err != nil {
		t.Fatal(err)
	}

	if err := sc.DeleteUser(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := s.PasskeyByCredentialID(t.Context(), []byte("leaver-key")); err == nil {
		t.Error("a passkey outlived the account it belonged to, so it authenticates nobody and still resolves")
	}
}

// TestAChallengeCanOnlyBeUsedOnce is what stops a captured assertion being replayed.
func TestAChallengeCanOnlyBeUsedOnce(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	u, err := sc.CreateUser(t.Context(), "person", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	challenge := []byte("challenge-bytes-for-this-test-01")
	if err := sc.StoreChallenge(t.Context(), challenge, u.ID, "register"); err != nil {
		t.Fatal(err)
	}

	userID, err := s.ConsumeChallenge(t.Context(), challenge, "register")
	if err != nil {
		t.Fatal(err)
	}
	if userID != u.ID {
		t.Errorf("the challenge resolved to account %d", userID)
	}

	// The second attempt must fail. This is the whole point.
	if _, err := s.ConsumeChallenge(t.Context(), challenge, "register"); err == nil {
		t.Error("a challenge was consumed twice, so a captured assertion can be replayed")
	} else if !errors.Is(err, ErrChallengeNotFound) {
		t.Errorf("the refusal was %v", err)
	}
}

// TestAChallengeIssuedForOnePurposeCannotBeUsedForAnother covers cross-purpose replay.
//
// A registration challenge accepted at sign-in would let somebody who intercepted a registration sign in with it.
func TestAChallengeIssuedForOnePurposeCannotBeUsedForAnother(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	u, err := sc.CreateUser(t.Context(), "person", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	challenge := []byte("challenge-bytes-for-this-test-02")
	if err := sc.StoreChallenge(t.Context(), challenge, u.ID, "register"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ConsumeChallenge(t.Context(), challenge, "authenticate"); err == nil {
		t.Error("a registration challenge was accepted for a sign-in")
	}
}

// TestASignInChallengeNeedsNoAccount covers the usernameless case.
//
// At sign-in the account is not known until the credential comes back, so the challenge is stored without one.
func TestASignInChallengeNeedsNoAccount(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	challenge := []byte("challenge-bytes-for-this-test-03")
	if err := sc.StoreChallenge(t.Context(), challenge, 0, "authenticate"); err != nil {
		t.Fatal(err)
	}

	userID, err := s.ConsumeChallenge(t.Context(), challenge, "authenticate")
	if err != nil {
		t.Fatal(err)
	}
	if userID != 0 {
		t.Errorf("a sign-in challenge resolved to account %d rather than none", userID)
	}
}

// TestAnUnknownChallengeIsRefused covers the front door.
func TestAnUnknownChallengeIsRefused(t *testing.T) {
	s := passkeyStore(t)

	if _, err := s.ConsumeChallenge(t.Context(), []byte("never-issued"), "authenticate"); err == nil {
		t.Error("a challenge that was never issued was accepted")
	}

	// And an empty one, which means a caller failed to decode a value.
	if _, err := s.ConsumeChallenge(t.Context(), nil, "authenticate"); err == nil {
		t.Error("an empty challenge was accepted")
	}
}

// TestExpiredChallengesArePurged covers the slow leak.
func TestExpiredChallengesArePurged(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	if err := sc.StoreChallenge(t.Context(), []byte("challenge-to-be-purged-000000001"), 0, "authenticate"); err != nil {
		t.Fatal(err)
	}

	// Age it past its expiry directly, because waiting two minutes in a test is not an option.
	if _, err := s.db.ExecContext(t.Context(),
		`UPDATE webauthn_challenges SET expires_at = ?`, formatTime(parseTime("2020-01-01T00:00:00Z"))); err != nil {
		t.Fatal(err)
	}

	purged, err := s.PurgeExpiredChallenges(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Errorf("reported %d challenges purged, want 1 - the count is what the sweeper logs, and a sweeper "+
			"that silently does nothing looks like one that is not running", purged)
	}

	var remaining int
	if err := s.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM webauthn_challenges`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("%d expired challenge(s) remain", remaining)
	}
}

// TestAnExpiredChallengeIsRefusedBeforeItIsPurged covers the window.
func TestAnExpiredChallengeIsRefusedBeforeItIsPurged(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	challenge := []byte("challenge-that-has-expired-00001")
	if err := sc.StoreChallenge(t.Context(), challenge, 0, "authenticate"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(),
		`UPDATE webauthn_challenges SET expires_at = ?`,
		formatTime(parseTime("2020-01-01T00:00:00Z"))); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ConsumeChallenge(t.Context(), challenge, "authenticate"); err == nil {
		t.Error("an expired challenge was accepted, so a captured assertion stays replayable indefinitely")
	}
}

// TestRecordingUseUpdatesTheCounterEvenWhenItGoesBackwards covers a real authenticator behaviour.
//
// Some models reset their counter after a firmware update. Writing it unconditionally means the clone warning fires once
// rather than for ever, and a warning that is always on is one nobody reads.
func TestRecordingUseUpdatesTheCounterEvenWhenItGoesBackwards(t *testing.T) {
	s := passkeyStore(t)
	sc := s.ScopeUnchecked(DefaultTenant)

	u, err := sc.CreateUser(t.Context(), "person", "a sufficiently long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	credential := sampleCredential("counter-key")
	credential.SignCount = 100
	if err := sc.AddPasskey(t.Context(), u.ID, credential); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordPasskeyUse(t.Context(), []byte("counter-key"), 5); err != nil {
		t.Fatal(err)
	}

	list, err := sc.ListPasskeys(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].SignCount != 5 {
		t.Errorf("the counter is %d; a reset authenticator would warn for ever", list[0].SignCount)
	}
	if list[0].LastUsed == nil {
		t.Error("the last-used time was not recorded, so a forgotten passkey cannot be spotted")
	}
}
