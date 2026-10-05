package dashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryDashboardNamesRealTiles(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Dashboards {
		if seen[d.ID] {
			t.Errorf("dashboard %s twice", d.ID)
		}
		seen[d.ID] = true
		for _, id := range d.Tiles {
			if _, ok := TileByID(id); !ok {
				t.Errorf("%s names tile %q, which does not exist", d.ID, id)
			}
		}
	}
}

func TestTheGrafanaExportFiltersByChannelAndSaysWhatItLeftOut(t *testing.T) {
	d, _ := ByID("connections")
	g := Grafana(d, nil)
	raw, _ := json.Marshal(g)
	s := string(raw)
	if !strings.Contains(s, `perfuse_connections_open{channel=~\"$channel\"}`) {
		t.Errorf("no channel filter: %s", s)
	}
	if !strings.Contains(s, "Not exported, because they have no metric behind them: Connections, Certificates") {
		t.Errorf("does not say what it left out: %s", g["description"])
	}
	if got := withChannel(`max by (channel) (perfuse_queue_oldest_seconds{destination="x"})`); got != `max by (channel) (perfuse_queue_oldest_seconds{channel=~"$channel",destination="x"})` {
		t.Errorf("%s", got)
	}
}

func TestAGroupWinsOverARole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.yaml")
	_ = os.WriteFile(path, []byte("groups: {PACS-Admins: imaging}\nroles: {viewer: manager}\n"), 0o600)
	a, err := LoadAssignments(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.For("viewer", []string{"Everyone", "PACS-Admins"}); got != "imaging" {
		t.Errorf("group: %s", got)
	}
	if got := a.For("viewer", nil); got != "manager" {
		t.Errorf("role: %s", got)
	}
	if got := a.For("admin", nil); got != "operations" {
		t.Errorf("default: %s", got)
	}
	_ = os.WriteFile(path, []byte("roles: {viewer: nope}\n"), 0o600)
	if _, err := LoadAssignments(path); err == nil {
		t.Error("an unknown dashboard was accepted")
	}
}
