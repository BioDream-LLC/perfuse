package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSavingARunningChannelAppliesTheChange: saving wrote the file and left the old version running, so a changed destination
// took effect only when the server restarted. Found by changing a destination's CA file in the builder.
func TestSavingARunningChannelAppliesTheChange(t *testing.T) {
	h := newHarness(t)
	first, second := t.TempDir(), t.TempDir()
	write := func(dir string) string {
		return "name: relay\nsource: {type: mllp, listen: \"127.0.0.1:0\"}\ndestinations:\n  - {name: out, type: file, dir: " +
			strings.ReplaceAll(dir, `\`, `\\`) + "}\n"
	}
	if err := os.WriteFile(filepath.Join(h.dir, "relay.yaml"), []byte(write(first)), 0o600); err != nil {
		t.Fatal(err)
	}
	h.server.Runtime = NewRuntime(h.server.Channels, nil, nil)
	h.handler = h.server.Handler()
	if rec := h.do("admin", http.MethodPost, "/api/channels/relay/start", nil); rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	t.Cleanup(func() { h.do("admin", http.MethodPost, "/api/channels/relay/stop", nil) })
	started := h.server.Runtime.States()[0].StartedAt

	time.Sleep(10 * time.Millisecond)
	rec := h.do("editor", http.MethodPut, "/api/channels/relay", map[string]any{"yaml": write(second)})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"restarted":true`) ||
		!strings.Contains(rec.Body.String(), `"file":"relay.yaml"`) {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	st := h.server.Runtime.States()[0]
	if !st.Running || st.StartedAt == nil || started == nil || !st.StartedAt.After(*started) {
		t.Errorf("after the save the channel is running=%v, started %v (was %v): the old version is still running",
			st.Running, st.StartedAt, started)
	}

	// A stopped channel stays stopped.
	h.do("admin", http.MethodPost, "/api/channels/relay/stop", nil)
	if rec := h.do("editor", http.MethodPut, "/api/channels/relay", map[string]any{"yaml": write(first)}); strings.Contains(rec.Body.String(), "restarted") {
		t.Errorf("a stopped channel was started by saving it: %s", rec.Body)
	}
	if h.server.Runtime.IsRunning("relay") {
		t.Error("a stopped channel is running after a save")
	}
}
