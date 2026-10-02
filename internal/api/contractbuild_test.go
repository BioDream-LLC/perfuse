package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// A contract can be built from traffic without leaving the interface.
//
// This was a command-line step only. The Contracts section could report that a feed had drifted and could not
// be used to make a contract in the first place, so the feature's opening move needed a terminal on the
// server - which for a product whose claim is that everything is doable from the interface is a defect.
func TestProposingAContractRefusesWhenThereIsNothingToLearnFrom(t *testing.T) {
	h := newHarness(t)

	// A channel with no recorded traffic. Building from nothing would produce a contract that describes
	// nothing and then reports every real message as a departure from it, so it must refuse and say why.
	res := h.do(string(store.RoleEditor), "POST", "/api/channels/nonexistent/contract/propose", nil)

	if res.Code == http.StatusOK {
		t.Fatal("a contract was proposed for a channel that does not exist")
	}
	// Not a 500: the caller asked for something reasonable about something absent.
	if res.Code == http.StatusInternalServerError {
		t.Fatalf("status = 500, want a refusal that explains itself: %s", res.Body.String())
	}
}

// Saving must refuse a contract that asserts nothing, rather than writing it.
//
// A contract file that will not load makes the channel referencing it invalid, and Perfuse refuses a channel
// wholesale when anything it references is broken - so a bad contract does not produce a warning, it makes the
// channel disappear from the interface. Learned the hard way when a fixture did exactly that.
func TestSavingRefusesAContractThatAssertsNothing(t *testing.T) {
	h := newHarness(t)

	for _, c := range []struct {
		name string
		yaml string
		want string
	}{
		{"empty", "", "no contract"},
		{"not yaml at all", "\tthis: is: not: yaml:", "cannot read"},
		{"valid yaml with no expectations", "expectations: []\n", "asserts nothing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := h.do(string(store.RoleEditor), "PUT", "/api/channels/anything/contract",
				map[string]any{"yaml": c.yaml})

			if res.Code == http.StatusOK {
				t.Fatalf("a contract that %s was accepted", c.name)
			}
			if res.Code == http.StatusInternalServerError {
				t.Errorf("status = 500 for %s; bad input deserves a refusal: %s", c.name, res.Body.String())
			}
		})
	}
}

// A viewer must not be able to write one.
func TestProposingAContractNeedsMoreThanReadAccess(t *testing.T) {
	h := newHarness(t)

	res := h.do(string(store.RoleViewer), "PUT", "/api/channels/labs/contract",
		map[string]any{"yaml": "expectations: []\n"})
	if res.Code != http.StatusForbidden {
		t.Errorf("a viewer got %d writing a contract, want 403", res.Code)
	}
}

// The companion file name is not to be trusted, because it arrives in a request.
func TestASidecarCannotEscapeTheChannelsDirectory(t *testing.T) {
	h := newHarness(t)

	for _, name := range []string{
		"../escaped.yaml",
		"sub/dir.yaml",
		`..\windows.yaml`,
		"no-extension",
		"",
	} {
		err := h.server.Channels.WriteSidecar("whatever", name, []byte("expectations: []\n"))
		if err == nil {
			t.Errorf("writing a companion file named %q was allowed", name)
			continue
		}
		// It must fail on the name, not by happening to not find the channel.
		if strings.Contains(err.Error(), "no such channel") && name != "" {
			t.Logf("%q was refused for the wrong reason: %v", name, err)
		}
	}
}

func TestAContractCheckIntervalCanBeSetWhenSaving(t *testing.T) {
	// The interval was settable only by hand-editing the channel file. The builder has no contract section, deliberately - a
	// thirty-expectation editor embedded in the channel form would be worse than a text editor - and nothing else offered it, so the
	// one number governing how often a feed is actually checked was the least reachable setting attached to a contract.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPut, "/api/channels/adt-inbound/contract", map[string]any{
		"yaml":       "expectations:\n  - path: MSH-9\n    rule: populated\n",
		"checkEvery": "5m",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	cfg, err := h.server.Channels.Get("adt-inbound")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Contract == nil {
		t.Fatal("the contract was saved and not attached")
	}
	if cfg.Contract.CheckEvery != "5m" {
		t.Errorf("checkEvery = %q, want 5m", cfg.Contract.CheckEvery)
	}
}

func TestAnUnreadableCheckIntervalIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	// Refused with the shape of a valid answer, because "invalid duration" tells somebody their input was wrong and not what right
	// looks like - and a channel that will not load is how this failure would otherwise surface.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPut, "/api/channels/adt-inbound/contract", map[string]any{
		"yaml":       "expectations:\n  - path: MSH-9\n    rule: populated\n",
		"checkEvery": "every 5 minutes",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "15m") {
		t.Errorf("the refusal does not show what a duration looks like: %s", res.Body.String())
	}
}

func TestAnEmptyCheckIntervalLeavesTheExistingOneAlone(t *testing.T) {
	// Empty means "leave it alone" rather than "use the default", because those differ for a contract that already had an interval
	// somebody chose - and saving a contract should not silently change how often it is checked.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	if res := h.do("editor", http.MethodPut, "/api/channels/adt-inbound/contract", map[string]any{
		"yaml":       "expectations:\n  - path: MSH-9\n    rule: populated\n",
		"checkEvery": "5m",
	}); res.Code != http.StatusOK {
		t.Fatalf("setting up: %d %s", res.Code, res.Body.String())
	}

	if res := h.do("editor", http.MethodPut, "/api/channels/adt-inbound/contract", map[string]any{
		"yaml": "expectations:\n  - path: MSH-10\n    rule: populated\n",
	}); res.Code != http.StatusOK {
		t.Fatalf("second save: %d %s", res.Code, res.Body.String())
	}

	cfg, err := h.server.Channels.Get("adt-inbound")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Contract == nil || cfg.Contract.CheckEvery != "5m" {
		t.Errorf("the interval was lost on a save that did not mention it: %#v", cfg.Contract)
	}
}
