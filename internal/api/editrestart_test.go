package api

import (
	"net/http"
	"net/http/httptest"
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

// TestATableChangeRestartsTheChannelsUsingIt: a channel reads its tables when it starts, so an edited or approved table
// reached a running channel only when it was restarted by hand, while the Tables screen said the next message would use it.
func TestATableChangeRestartsTheChannelsUsingIt(t *testing.T) {
	h := newHarness(t)
	out := t.TempDir()
	channel := "name: relay\nsource: {type: mllp, listen: \"127.0.0.1:0\"}\ntables: [shared.codeset.yaml]\n" +
		"transformations:\n  - map: {path: PID-8, use: sex}\ndestinations:\n  - {name: out, type: file, dir: " +
		strings.ReplaceAll(out, `\`, `\\`) + "}\n"
	save := func(version, to string, draft bool) *httptest.ResponseRecorder {
		return h.do("editor", http.MethodPut, "/api/codesets/table", map[string]any{"name": "sex", "describes": "d",
			"version": version, "draft": draft, "entries": []map[string]any{{"from": "1", "to": to}}})
	}
	if rec := save("1", "M", false); rec.Code != http.StatusOK {
		t.Fatalf("first table: %d %s", rec.Code, rec.Body)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "relay.yaml"), []byte(channel), 0o600); err != nil {
		t.Fatal(err)
	}
	h.server.Runtime = NewRuntime(h.server.Channels, nil, nil)
	h.handler = h.server.Handler()
	if rec := h.do("admin", http.MethodPost, "/api/channels/relay/start", nil); rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	t.Cleanup(func() { h.do("admin", http.MethodPost, "/api/channels/relay/stop", nil) })
	started := func() time.Time { return *h.server.Runtime.States()[0].StartedAt }
	before := started()
	time.Sleep(10 * time.Millisecond)

	// A draft is used by nothing, so nothing restarts.
	if rec := save("2", "MALE", true); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Restarted") {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	if !started().Equal(before) {
		t.Error("saving a draft restarted the channel")
	}
	// Approving it does.
	rec := h.do("admin", http.MethodPost, "/api/codesets/table/approve", map[string]any{"name": "sex", "version": "2"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Restarted relay") {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if !started().After(before) || !h.server.Runtime.IsRunning("relay") {
		t.Error("approving a table left the channel on the old one")
	}
}
