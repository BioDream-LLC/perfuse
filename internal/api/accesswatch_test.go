package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/biodream-llc/perfuse/internal/compliance"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// TestEveryWatchedReadIsAnActionTheServerAudits ties the access monitor's list to what the server records.
//
// The monitor learns who read what from the audit trail, by action name. A name on its list that the server never
// writes is a read nobody watches, and it would look covered.
func TestEveryWatchedReadIsAnActionTheServerAudits(t *testing.T) {
	audited := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`Action:\s*"([a-z0-9_.]+)"`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			audited[m[1]] = true
		}
	}
	for action := range compliance.PHIReadActions {
		if !audited[action] {
			t.Errorf("the access monitor watches %q, which the server never audits", action)
		}
	}
}

// TestTheAccessWatchScreenSeesAuditedReads drives the monitor through the store's audit hook, as serve wires it.
func TestTheAccessWatchScreenSeesAuditedReads(t *testing.T) {
	h := newHarness(t)
	m := compliance.NewMonitor(compliance.MonitorOptions{})
	h.server.PHIWatch = m
	h.store.OnAudit = func(_ tenant.ID, e store.AuditEntry) {
		m.Access(compliance.Read{Who: e.Username, IP: e.IP, Action: e.Action, Target: e.Target, At: e.At})
	}
	for i := range 12 {
		_ = h.store.Audit(t.Context(), store.AuditEntry{Username: "jdoe", Action: "message.read", Target: "adt/" + string(rune('a'+i))})
	}
	if res := h.do(string(store.RoleEditor), http.MethodGet, "/api/access-watch", nil); res.Code != http.StatusForbidden {
		t.Errorf("an editor reading the access watch: %d, want 403 (it names people and what they read)", res.Code)
	}
	res := h.do(string(store.RoleAdmin), http.MethodGet, "/api/access-watch", nil)
	var out accessWatchView
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || !out.On || len(out.Signals) != 1 ||
		out.Signals[0].Kind != compliance.SignalBulkReads || out.Signals[0].Value != 12 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
}

func TestOneTenantNeverSeesAnothersAccessSignals(t *testing.T) {
	for _, c := range []struct {
		tid  tenant.ID
		name string
		want string
		ok   bool
	}{
		{"acme", "acme/admissions", "admissions", true},
		{"acme", "globex/admissions", "", false},
		{"acme", "admissions", "", false},
		{"", "admissions", "admissions", true},
		{store.DefaultTenant, "alice from 10.0.0.1", "alice from 10.0.0.1", true},
		{store.DefaultTenant, "acme/alice from 10.0.0.1", "", false},
		{store.DefaultTenant, store.DefaultTenant + "/lab", "lab", true},
	} {
		got, ok := ownWatchName(c.tid, c.name)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("tenant %q, name %q: got %q %v", c.tid, c.name, got, ok)
		}
	}
}
