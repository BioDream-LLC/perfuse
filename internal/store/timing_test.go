package store

import (
	"testing"
	"time"
)

// TestAMissingUserTakesAsLongAsAWrongPassword guards the identical error message.
//
// The login handler deliberately returns the same message for an unknown username and a wrong password, so that nobody can
// enumerate who has an account. That only works if the two also take the same time: if a missing user skipped the hash and
// returned immediately, the response time would distinguish them and the shared message would be decoration.
//
// authenticateIn hashes a dummy password on the missing-user path for exactly this reason. This test is here because that
// is the kind of line somebody removes while tidying, having no way to know what it was for.
//
// Measured as a ratio rather than an absolute, because the absolute depends on HashIterations - which the test suite lowers
// so it does not spend a second per password.
func TestAMissingUserTakesAsLongAsAWrongPassword(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.CreateUser(t.Context(), "real", "a sufficiently long password", RoleAdmin); err != nil {
		t.Fatal(err)
	}

	measure := func(user, pass string) time.Duration {
		// Warmed up first, so the cost of the first call is not attributed to whichever case ran first.
		_, _, _ = s.Authenticate(t.Context(), user, pass, "t", "t")

		const n = 20
		start := time.Now()
		for i := 0; i < n; i++ {
			_, _, _ = s.Authenticate(t.Context(), user, pass, "t", "t")
		}
		return time.Since(start) / n
	}

	missing := measure("nobody-by-this-name", "a sufficiently long password")
	wrong := measure("real", "definitely-the-wrong-password")

	t.Logf("missing user %s, wrong password %s", missing, wrong)

	if missing == 0 || wrong == 0 {
		t.Skip("the measurements are too small to compare on this machine")
	}

	ratio := float64(missing) / float64(wrong)
	if ratio < 1 {
		ratio = 1 / ratio
	}

	// Four is deliberately loose. This runs on shared CI and on a laptop doing other things, and the failure being
	// guarded against is an order of magnitude - a missing user returning without hashing at all, which shows up as a
	// ratio in the tens or hundreds rather than as a few percent.
	//
	// Tightening it would produce a test that fails for unrelated reasons, and a test that fails for unrelated reasons
	// gets deleted.
	const limit = 4.0
	if ratio > limit {
		t.Errorf("a missing user and a wrong password differ by %.1fx (%s against %s), which is enough to "+
			"enumerate accounts by timing; the dummy hash on the missing-user path in authenticateIn is "+
			"what prevents this", ratio, missing, wrong)
	}
}
