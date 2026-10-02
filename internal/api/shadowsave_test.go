package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// Tests for starting and stopping a comparison from the interface.
//
// These exist because the builder's drift guard excused the entire shadow block with the reason "configured from the shadow tab", and
// the shadow tab was read-only. The excuse was not merely out of date - there was no endpoint at all, so the only way to start a
// comparison was to edit a channel file by hand, on the one feature whose whole purpose is checking a rewrite before it goes live.

// writeChannel puts a minimal loadable channel in the harness directory.
//
// Written as a file rather than through the API so that these tests are about the shadow endpoints and not about channel creation -
// a failure here should mean the comparison is wrong, not that something else broke on the way in.
func writeShadowChannel(t *testing.T, dir, name string) {
	t.Helper()

	body := "name: " + name + `
source:
  type: mllp
  listen: ":0"
destinations:
  - name: archive
    type: file
    dir: ./archive
`

	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// loadChannel reads a channel back through the real loader.
func loadChannel(t *testing.T, h *harness, name string) *config.Channel {
	t.Helper()

	cfg, err := h.server.Channels.Get(name)
	if err != nil {
		t.Fatalf("reading %s back: %v", name, err)
	}

	return cfg
}

func TestAComparisonCanBeStartedFromTheInterface(t *testing.T) {
	h := newHarness(t)

	writeShadowChannel(t, h.dir, "live")
	writeShadowChannel(t, h.dir, "candidate")

	res := h.do("editor", http.MethodPut, "/api/channels/live/shadow", map[string]any{
		"candidate": "candidate",
		"sample":    50,
		"ignore":    []string{"MSH-7", "  "},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	// Read back through the real loader, because the only thing that matters is whether the channel Perfuse loads is now
	// comparing. A handler that reports success and writes something the loader rejects makes the channel disappear.
	cfg := loadChannel(t, h, "live")
	if cfg.Shadow == nil {
		t.Fatal("the comparison was saved and the channel is not comparing against anything")
	}
	if cfg.Shadow.Channel != "candidate.yaml" {
		t.Errorf("candidate = %q, want candidate.yaml", cfg.Shadow.Channel)
	}

	// A percentage in, a fraction on disk. Fifty percent typed into a field that wants 0.5 is the mistake this conversion exists
	// to prevent, and it fails silently in the direction of shadowing everything.
	if cfg.Shadow.Sample != 0.5 {
		t.Errorf("sample = %v, want 0.5 - a percentage was entered and the file format is a fraction", cfg.Shadow.Sample)
	}

	// The blank entry must not survive, because "ignore: [MSH-7, '']" asks the comparison to ignore a path that is not one.
	if len(cfg.Shadow.Ignore) != 1 || cfg.Shadow.Ignore[0] != "MSH-7" {
		t.Errorf("ignore = %#v, want just MSH-7", cfg.Shadow.Ignore)
	}
}

func TestAComparisonAgainstAChannelThatDoesNotExistIsRefusedWithTheOnesThatDo(t *testing.T) {
	h := newHarness(t)

	writeShadowChannel(t, h.dir, "live")
	writeShadowChannel(t, h.dir, "the-rewrite")

	res := h.do("editor", http.MethodPut, "/api/channels/live/shadow", map[string]any{
		"candidate": "the-rewirte",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}

	// The list is the point. A shadow naming a file that is not there makes the live channel invalid, and Perfuse refuses a channel
	// wholesale when anything it references is broken - so a typo does not warn, it makes the channel vanish. Being told which
	// names exist is what turns that into a corrected keystroke.
	var out struct {
		Error    string   `json:"error"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.Error, "the-rewirte") {
		t.Errorf("the refusal does not name what was asked for: %q", out.Error)
	}

	var listed bool
	for _, p := range out.Problems {
		if p == "the-rewrite" {
			listed = true
		}
		if p == "live" {
			t.Error("the channel being configured was offered as a candidate for itself")
		}
	}
	if !listed {
		t.Errorf("the refusal does not say which channels could be compared against: %#v", out.Problems)
	}
}

func TestAChannelCannotShadowItself(t *testing.T) {
	h := newHarness(t)

	writeShadowChannel(t, h.dir, "live")

	res := h.do("editor", http.MethodPut, "/api/channels/live/shadow", map[string]any{"candidate": "live"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}

	// Refused here rather than at load, because a loader reports this as a cycle and never says the obvious thing.
	if !strings.Contains(res.Body.String(), "nothing to compare") {
		t.Errorf("the refusal does not say why: %s", res.Body.String())
	}
}

func TestAShareOutsideAPercentageIsRefused(t *testing.T) {
	h := newHarness(t)

	writeShadowChannel(t, h.dir, "live")
	writeShadowChannel(t, h.dir, "candidate")

	// 0.5 is the shape of the file format, not of the field, and somebody meaning half who types it would otherwise get a
	// comparison that observes nothing.
	for _, share := range []int{-1, 101} {
		res := h.do("editor", http.MethodPut, "/api/channels/live/shadow", map[string]any{
			"candidate": "candidate", "sample": share,
		})
		if res.Code != http.StatusBadRequest {
			t.Errorf("a share of %d returned %d, want 400", share, res.Code)
		}
	}
}

func TestStoppingAComparisonLeavesTheCandidateAlone(t *testing.T) {
	h := newHarness(t)

	writeShadowChannel(t, h.dir, "live")
	writeShadowChannel(t, h.dir, "candidate")

	if res := h.do("editor", http.MethodPut, "/api/channels/live/shadow", map[string]any{
		"candidate": "candidate",
	}); res.Code != http.StatusOK {
		t.Fatalf("setting up: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodDelete, "/api/channels/live/shadow", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	if cfg := loadChannel(t, h, "live"); cfg.Shadow != nil {
		t.Error("the comparison was stopped and the channel is still comparing")
	}

	// The candidate is meant to become the live channel, so deleting it because somebody stopped comparing would throw away the
	// work the comparison existed to validate.
	loadChannel(t, h, "candidate")
}

func TestStoppingAComparisonThatIsNotRunningIsNotAnError(t *testing.T) {
	h := newHarness(t)

	writeShadowChannel(t, h.dir, "live")

	// Stopping something that is not running is the state the caller wanted. Reporting it as a failure makes the interface show a
	// red box for a button that did exactly what it said.
	res := h.do("editor", http.MethodDelete, "/api/channels/live/shadow", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}
}

func TestAViewerCannotStartAComparison(t *testing.T) {
	h := newHarness(t)

	writeShadowChannel(t, h.dir, "live")
	writeShadowChannel(t, h.dir, "candidate")

	// Starting a comparison writes a channel file, so it is an editor's action however read-only the screen it lives on looks.
	res := h.do("viewer", http.MethodPut, "/api/channels/live/shadow", map[string]any{"candidate": "candidate"})
	if res.Code != http.StatusForbidden {
		t.Errorf("a viewer got %d, want 403", res.Code)
	}
}
