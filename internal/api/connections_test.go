package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConnectionsAreCheckedLayerByLayer runs the connection check against three real endpoints: a listener on this machine, a
// port nothing listens on, and a name that does not resolve. Each is told apart, in words, and nothing is sent to any.
func TestConnectionsAreCheckedLayerByLayer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := closed.Addr().String()
	_ = closed.Close()

	h := newHarness(t)
	channel := fmt.Sprintf(`name: lab-feed
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - {name: up, type: mllp, address: %q}
  - {name: refused, type: mllp, address: %q}
  - {name: nowhere, type: mllp, address: "no-such-host.invalid:2575"}
  - {name: archive, type: file, dir: %q}
`, ln.Addr().String(), deadAddr, t.TempDir())
	if err := os.WriteFile(filepath.Join(h.dir, "lab-feed.yaml"), []byte(channel), 0o600); err != nil {
		t.Fatal(err)
	}
	h.server.Runtime = NewRuntime(h.server.Channels, nil, nil)
	h.handler = h.server.Handler()

	rec := h.do("viewer", http.MethodGet, "/api/connections", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Connections []connectionCheck `json:"connections"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	got := map[string]connectionCheck{}
	for _, c := range body.Connections {
		got[c.Destination] = c
	}
	if _, ok := got["archive"]; ok || len(got) != 3 {
		t.Errorf("a file destination has no connection to check: %v", body.Connections)
	}
	if c := got["up"]; c.Verdict != "ok" || c.TCP.State != "ok" {
		t.Errorf("up: %+v", c)
	}
	if c := got["refused"]; c.Verdict != "down" || !strings.Contains(c.Reason, "nothing is listening on that port") {
		t.Errorf("refused: %+v", c)
	}
	if c := got["nowhere"]; c.Verdict != "down" || c.DNS.State != "failed" || c.TCP.State != "skipped" {
		t.Errorf("nowhere: %+v", c)
	}
}

func TestADashboardViewIsSavedPerPerson(t *testing.T) {
	h := newHarness(t)
	rec := h.do("viewer", http.MethodGet, "/api/dashboards", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"assigned":"operations"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = h.do("viewer", http.MethodPut, "/api/dashboards/view", map[string]any{"dashboard": "connections", "tiles": []string{"queue", "connections"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := h.do("viewer", http.MethodGet, "/api/dashboards", nil); !strings.Contains(rec.Body.String(), `"saved":{"dashboard":"connections","tiles":["queue","connections"]}`) {
		t.Errorf("%s", rec.Body)
	}
	if rec := h.do("editor", http.MethodGet, "/api/dashboards", nil); strings.Contains(rec.Body.String(), `"dashboard":"connections"`) {
		t.Error("one person's view leaked to another")
	}
	if rec := h.do("viewer", http.MethodPut, "/api/dashboards/view", map[string]any{"dashboard": "nope"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown dashboard: %d", rec.Code)
	}
	rec = h.do("viewer", http.MethodGet, "/api/dashboards/interface-analyst/grafana", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "perfuse_queue_oldest_seconds") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
