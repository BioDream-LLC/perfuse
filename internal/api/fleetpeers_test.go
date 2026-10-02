package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/peers"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Adding another instance to the fleet, from the interface.
//
// The Fleet section had no controls at all: it explained the feature, said "this is a fleet of one", and printed a
// command to run in a terminal on the other machine. Everything after that - the token, a peers file, a restart -
// was impossible from here. Found by the control sweep reporting the section as having nothing operable in it.
func fleetHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.server.Fleet = peers.New(peers.Config{})
	h.server.PeersFile = filepath.Join(h.dir, "perfuse-peers.yaml")
	h.server.SelfURL = "https://this-one.example:8443"
	return h
}

func TestAddingAFleetPeer(t *testing.T) {
	h := fleetHarness(t)

	good := map[string]any{
		"name":  "theatre-02",
		"url":   "https://perfuse-02.hospital.internal:8443",
		"token": "a-viewer-token-from-the-other-instance",
	}

	t.Run("a peer can be added", func(t *testing.T) {
		res := h.do(string(store.RoleAdmin), "PUT", "/api/fleet/peers", good)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", res.Code, res.Body.String())
		}
	})

	t.Run("it is polled without a restart", func(t *testing.T) {
		// The point of the whole change. Previously the peer list was read once at startup, so anything added
		// later would have sat in a file doing nothing until somebody restarted the service.
		if got := len(h.server.Fleet.Peers()); got != 1 {
			t.Fatalf("the running fleet has %d peer(s), want 1", got)
		}
	})

	t.Run("the token is not sent back", func(t *testing.T) {
		res := h.do(string(store.RoleAdmin), "GET", "/api/fleet/peers", nil)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Code, res.Body.String())
		}
		// It is a credential for another instance. Echoing it to every administrator who opens the page turns
		// one leaked session into access to the whole fleet.
		if strings.Contains(res.Body.String(), "a-viewer-token-from-the-other-instance") {
			t.Errorf("the peer's token was sent back to the browser: %s", res.Body.String())
		}
		if !strings.Contains(res.Body.String(), `"hasToken":true`) {
			t.Errorf("the response does not report that a token is set: %s", res.Body.String())
		}
	})

	t.Run("the file it wrote is not world readable", func(t *testing.T) {
		info, err := os.Stat(h.server.PeersFile)
		if err != nil {
			t.Fatal(err)
		}
		// The tokens in it are credentials for other servers.
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("the peers file is mode %o; it holds tokens for other instances", perm)
		}
	})

	t.Run("a peer can be removed", func(t *testing.T) {
		res := h.do(string(store.RoleAdmin), "DELETE", "/api/fleet/peers/theatre-02", nil)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Code, res.Body.String())
		}
		if got := len(h.server.Fleet.Peers()); got != 0 {
			t.Errorf("the running fleet still has %d peer(s)", got)
		}
	})
}

// The refusals, each naming something that would otherwise fail on every poll with nothing useful said.
func TestAFleetPeerIsRefusedWhenItCannotWork(t *testing.T) {
	h := fleetHarness(t)

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "no name",
			body: map[string]any{"url": "https://a.example", "token": "t"},
			want: "name",
		},
		{
			// Parses without error and then fails every poll with something unhelpful about a missing protocol.
			name: "an address with no scheme",
			body: map[string]any{"name": "n", "url": "perfuse-02:8443", "token": "t"},
			want: "https://",
		},
		{
			name: "no token",
			body: map[string]any{"name": "n", "url": "https://a.example", "token": ""},
			want: "read-only token",
		},
		{
			// Not refused for being unusual - segregated networks are a real deployment - but the token would
			// cross the network in the clear, which is worth saying before it happens.
			name: "plain HTTP to another host",
			body: map[string]any{"name": "n", "url": "http://elsewhere.example:8443", "token": "t"},
			want: "unencrypted",
		},
		{
			// Polls this instance through its own stack and appears twice in the fleet view.
			name: "an address pointing back at this instance",
			body: map[string]any{"name": "n", "url": "https://this-one.example:8443", "token": "t"},
			want: "this instance",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := h.do(string(store.RoleAdmin), "PUT", "/api/fleet/peers", c.body)

			if res.Code == http.StatusOK {
				t.Fatalf("a peer with %s was accepted", c.name)
			}
			if res.Code == http.StatusInternalServerError {
				t.Fatalf("status = 500; bad input deserves a refusal: %s", res.Body.String())
			}
			if !strings.Contains(strings.ToLower(res.Body.String()), strings.ToLower(c.want)) {
				t.Errorf("the refusal does not mention %q: %s", c.want, res.Body.String())
			}
		})
	}
}

// Watching another hospital's server is an administrator's decision, not an editor's.
func TestFleetPeersAreAdministratorOnly(t *testing.T) {
	h := fleetHarness(t)

	for _, role := range []store.Role{store.RoleViewer, store.RoleEditor} {
		res := h.do(string(role), "PUT", "/api/fleet/peers", map[string]any{
			"name": "n", "url": "https://a.example", "token": "t",
		})
		if res.Code != http.StatusForbidden {
			t.Errorf("%s got %d adding a fleet peer, want 403", role, res.Code)
		}
	}
}

// A server with nowhere to store peers must say so rather than offering a save that fails.
func TestAServerWithNoPeersFileSaysPeersAreNotWritable(t *testing.T) {
	h := newHarness(t)

	res := h.do(string(store.RoleAdmin), "GET", "/api/fleet/peers", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"writable":false`) {
		t.Errorf("a server with no peers file does not report peers as unwritable: %s", res.Body.String())
	}
	// And the list must still be an empty array rather than null, which the interface reads .length on.
	if !strings.Contains(res.Body.String(), `"peers":[]`) {
		t.Errorf("the peer list is not an empty array: %s", res.Body.String())
	}
}
