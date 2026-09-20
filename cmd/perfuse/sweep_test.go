package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// TestTheSweepRemovesBothExpiredSessionsAndAbandonedChallenges pins what the sweeper covers.
//
// Written because PurgeExpiredChallenges existed, was tested, was documented as running on the session schedule, and was
// called from nowhere - so the table grew by a row per abandoned sign-in from the day passkeys shipped. Its own unit test
// passed throughout, which is the point: a test that a function works cannot tell you the function runs.
//
// So this asserts on the sweep rather than on either purge, and it will fail if a future sweep drops one of them.
func TestTheSweepRemovesBothExpiredSessionsAndAbandonedChallenges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sweep.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sc := st.ScopeUnchecked(store.DefaultTenant)

	// A challenge nobody completed, which is what happens whenever somebody opens the sign-in page and leaves.
	if err := sc.StoreChallenge(t.Context(), []byte("abandoned-challenge-0000000000001"), 0, "authenticate"); err != nil {
		t.Fatal(err)
	}

	// Age everything past expiry rather than waiting for it.
	past := time.Now().UTC().Add(-2 * time.Hour)
	if _, err = st.DB().ExecContext(t.Context(),
		`UPDATE webauthn_challenges SET expires_at = ?`, past.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	sweepOnce(t.Context(), st, slog.New(slog.NewTextHandler(io.Discard, nil)))

	var challenges int
	if err := st.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM webauthn_challenges`).Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if challenges != 0 {
		t.Errorf("%d abandoned challenge(s) survived the sweep - it is not purging challenges", challenges)
	}
}
