package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/authlimit"
)

// This file guards the sign-in throttle.
//
// It exists because there was none. A measurement against a running server managed twenty password guesses a second
// against the administrator account, sustained, with every attempt answered 401 and nothing slowing down. Failures were
// logged, which made the log the only evidence and did nothing to stop it.

// postLogin sends a sign-in attempt from a given address.
//
// The address is set explicitly, because the throttle keys on it and httptest gives every request the same one - which
// would make one test's failures throttle the next test's.
func postLogin(t *testing.T, h *harness, from, username, password string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")
	req.RemoteAddr = from + ":40000"

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// TestRepeatedWrongPasswordsAreThrottled is the regression test for the measured hole.
func TestRepeatedWrongPasswordsAreThrottled(t *testing.T) {
	h := newHarness(t)

	throttledAt := 0
	for i := 1; i <= authlimit.DefaultThreshold+4; i++ {
		rec := postLogin(t, h, "198.51.100.10", "admin", "wrong-password")
		if rec.Code == http.StatusTooManyRequests {
			throttledAt = i
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d, want 401 or 429", i, rec.Code)
		}
	}

	if throttledAt == 0 {
		t.Fatalf("still not throttled after %d wrong passwords; twenty a second was the measured rate",
			authlimit.DefaultThreshold+4)
	}
	if throttledAt <= 2 {
		t.Errorf("throttled at attempt %d, which is too eager; somebody mistyping twice must not be locked out",
			throttledAt)
	}
}

// TestTheThrottleSaysWhenToComeBack covers the client's side of a 429.
//
// Without Retry-After a client has to guess, and a client that guesses wrong keeps hammering - which looks exactly like
// the attack the throttle is for.
func TestTheThrottleSaysWhenToComeBack(t *testing.T) {
	h := newHarness(t)

	var throttled *httptest.ResponseRecorder
	for i := 0; i < authlimit.DefaultThreshold+3; i++ {
		rec := postLogin(t, h, "198.51.100.11", "admin", "wrong-password")
		if rec.Code == http.StatusTooManyRequests {
			throttled = rec
			break
		}
	}

	if throttled == nil {
		t.Fatal("never throttled")
	}
	if got := throttled.Header().Get("Retry-After"); got == "" {
		t.Error("a 429 carried no Retry-After header")
	}
}

// TestOneAddressCannotLockOutAnother covers the blast radius.
//
// Somebody guessing from one address must not be able to deny everybody else. This is why the throttle keys on the
// address rather than the username - keying on the username would let anyone lock out the administrator by name.
func TestOneAddressCannotLockOutAnother(t *testing.T) {
	h := newHarness(t)

	for i := 0; i < authlimit.DefaultThreshold+3; i++ {
		postLogin(t, h, "203.0.113.20", "admin", "wrong-password")
	}

	if rec := postLogin(t, h, "203.0.113.20", "admin", "wrong-password"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the guessing address got %d, want 429", rec.Code)
	}

	// A different address, and a correct password, so this proves the account is reachable rather than merely
	// differently refused.
	rec := postLogin(t, h, "203.0.113.21", "admin", "a sufficiently long password")
	if rec.Code == http.StatusTooManyRequests {
		t.Error("a second address was throttled by the first one's failures; one guesser could lock out a hospital")
	}
}

// TestACorrectPasswordClearsTheThrottle covers the legitimate user who mistyped.
func TestACorrectPasswordClearsTheThrottle(t *testing.T) {
	h := newHarness(t)

	// Below the threshold, then the right password, then wrong ones again. If success did not clear the count, the
	// second run would throttle sooner than the first.
	for i := 0; i < authlimit.DefaultThreshold-1; i++ {
		postLogin(t, h, "203.0.113.30", "admin", "wrong-password")
	}

	if rec := postLogin(t, h, "203.0.113.30", "admin", "a sufficiently long password"); rec.Code != http.StatusOK {
		t.Fatalf("the correct password returned %d, want 200; the throttle has locked out a valid user", rec.Code)
	}

	for i := 0; i < authlimit.DefaultThreshold-1; i++ {
		if rec := postLogin(t, h, "203.0.113.30", "admin", "wrong-password"); rec.Code == http.StatusTooManyRequests {
			t.Errorf("throttled after only %d failures following a success, so the count was not cleared", i+1)
			return
		}
	}
}

// TestTheThrottleRunsBeforeThePasswordIsChecked covers where the check sits.
//
// A throttle that ran after verification would still do the expensive work - and with a password hash that is
// deliberately slow, that is a way to exhaust the server rather than merely to guess.
func TestTheThrottleRunsBeforeThePasswordIsChecked(t *testing.T) {
	h := newHarness(t)

	for i := 0; i < authlimit.DefaultThreshold+3; i++ {
		postLogin(t, h, "203.0.113.40", "admin", "wrong-password")
	}

	// A correct password from the throttled address is still refused with 429 rather than 200, which shows the
	// throttle decided before anything looked at the password.
	rec := postLogin(t, h, "203.0.113.40", "admin", "a sufficiently long password")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("a throttled address got %d for a correct password; the throttle is not being consulted first",
			rec.Code)
	}
}

// TestAnUnknownUserIsThrottledToo covers enumeration.
//
// Throttling only known accounts would tell a guesser which usernames exist: real ones slow down and invented ones do
// not. That is a slower version of the account enumeration the identical error message already prevents.
func TestAnUnknownUserIsThrottledToo(t *testing.T) {
	h := newHarness(t)

	throttled := false
	for i := 0; i < authlimit.DefaultThreshold+3; i++ {
		if rec := postLogin(t, h, "203.0.113.50", "nobody-by-this-name", "any-password"); rec.Code ==
			http.StatusTooManyRequests {
			throttled = true
			break
		}
	}

	if !throttled {
		t.Error("guessing at an unknown username was never throttled, which tells a guesser which names are real")
	}
}
