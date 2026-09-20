package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// A request that was never going to work must be refused, not reported as a server failure.
//
// Found by a sweep that operates every control in every section: filling the Add User form and submitting it
// with a blank username produced 500 "something went wrong". The cause was a validation error with no
// sentinel, which fell through the error mapper's default branch.
//
// Why this is worth a guard rather than a one-line fix. An administrator who leaves a field blank and is told
// the server broke does the reasonable thing: checks the logs, finds nothing, restarts the service, and
// eventually opens a ticket. The cost of the wrong status code is not the status code, it is the hour spent
// looking for a fault that was never there. Every validation path needs to be a 400 that says what to fix.
func TestInvalidInputIsRefusedNotBlamedOnTheServer(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "no username at all",
			body: map[string]any{"username": "", "password": "a-long-enough-password", "role": "viewer"},
			want: "username",
		},
		{
			name: "a username of only spaces",
			body: map[string]any{"username": "   ", "password": "a-long-enough-password", "role": "viewer"},
			want: "username",
		},
		{
			name: "a role that does not exist",
			body: map[string]any{"username": "someone", "password": "a-long-enough-password", "role": "wizard"},
			want: "role",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := h.do(string(store.RoleAdmin), "POST", "/api/users", c.body)

			if res.Code == http.StatusInternalServerError {
				t.Fatalf("submitting %s produced 500. A person who can fix their own mistake was told the "+
					"server is broken, and will go looking for a fault that does not exist.", c.name)
			}
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", res.Code, res.Body.String())
			}

			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding the refusal: %v", err)
			}
			if !strings.Contains(strings.ToLower(body.Error), c.want) {
				t.Errorf("the refusal does not mention %q, so it does not say what to fix: %q",
					c.want, body.Error)
			}
			// The internal package prefix is for logs, not for the person reading the screen.
			if strings.Contains(body.Error, "store:") {
				t.Errorf("the message leaks an internal prefix: %q", body.Error)
			}
			if strings.Contains(strings.ToLower(body.Error), "something went wrong") {
				t.Errorf("the message says nothing actionable: %q", body.Error)
			}
		})
	}
}

// An administrator must not be able to lock themselves out with one dropdown.
//
// Found by a sweep that operates every control in every section. Each row of the user list has a role
// dropdown that saves the moment it changes, so on your own row it is one click from removing your own
// administrator rights. The sweep did exactly that, and the three sections it visited afterwards were
// inaccessible - which is what an administrator would experience.
//
// The existing guard only protected the last administrator. With a second one present, demoting yourself was
// permitted, and the sections needed to undo it are the first thing you lose. Recovery is editing the
// database by hand, which is a support call by any measure.
//
// Another administrator can still make this change. What is refused is doing it to yourself.
func TestAnAdministratorCannotLockThemselvesOut(t *testing.T) {
	h := newHarness(t)

	// A second administrator, so the existing last-administrator guard is not what does the refusing. Without
	// this the test would pass for the wrong reason.
	other, err := h.store.CreateUser(t.Context(), "another-admin", "a sufficiently long password", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	me, err := h.store.GetUser(t.Context(), string(store.RoleAdmin))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("demoting yourself is refused", func(t *testing.T) {
		res := h.do(string(store.RoleAdmin), "PUT", fmt.Sprintf("/api/users/%d", me.ID),
			map[string]any{"role": "viewer"})
		if res.Code == http.StatusOK {
			t.Fatal("an administrator demoted themselves. They have just lost the Users section and cannot " +
				"undo it without database surgery.")
		}
		if res.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 (body %s)", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "another administrator") {
			t.Errorf("the refusal does not say who can do it instead: %s", res.Body.String())
		}
	})

	t.Run("disabling yourself is refused", func(t *testing.T) {
		res := h.do(string(store.RoleAdmin), "PUT", fmt.Sprintf("/api/users/%d", me.ID),
			map[string]any{"disabled": true})
		if res.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 (body %s)", res.Code, res.Body.String())
		}
	})

	t.Run("demoting somebody else is still allowed", func(t *testing.T) {
		// The guard must not overreach. Managing other people is the entire purpose of the section.
		res := h.do(string(store.RoleAdmin), "PUT", fmt.Sprintf("/api/users/%d", other.ID),
			map[string]any{"role": "editor"})
		if res.Code != http.StatusOK {
			t.Errorf("demoting another administrator was refused with %d: %s", res.Code, res.Body.String())
		}
	})
}
