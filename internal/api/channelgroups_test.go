package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// Channel groups inside one tenant, and the monitor and operator roles.
//
// The arrangement under test is a hospital group on one server: the East and West facilities each look after their own
// feeds, and a central team sees everything. Every test here signs in as somebody limited to East and checks that West is
// invisible - not forbidden, absent - wherever channels, messages, counts or people appear.

const groupPassword = "a sufficiently long password"

func groupedChannelYAML(name, group, listen string) string {
	g := ""
	if group != "" {
		g = "group: " + group + "\n"
	}
	return "name: " + name + "\n" + g +
		"source:\n  type: mllp\n  listen: " + listen + "\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp\n"
}

// newGroupHarness is a single-tenant server with three channels - one in East, one in West, one in no group - a message
// store holding one message for each, and accounts limited to East.
func newGroupHarness(t *testing.T) (*harness, map[string]int64) {
	t.Helper()
	h := newHarness(t)

	for name, group := range map[string]string{"east-adt": "East", "west-adt": "West", "loose": ""} {
		listen := map[string]string{"east-adt": ":0", "west-adt": ":0", "loose": ":0"}[name]
		if err := os.WriteFile(filepath.Join(h.dir, name+".yaml"), []byte(groupedChannelYAML(name, group, listen)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	messages, err := msgstore.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	h.server.Runtime = NewRuntime(h.server.Channels, messages, nil)
	h.handler = h.server.Handler()

	ids := map[string]int64{}
	for _, ch := range []string{"east-adt", "west-adt", "loose"} {
		id, err := messages.Record(context.Background(), &msgstore.Message{
			Channel: ch, ReceivedAt: time.Now(), ControlID: ch + "-1", Outcome: msgstore.Failed, Size: 10,
			Raw: []byte("MSH|^~\\&|A|B|C|D|20260821||ADT^A01|" + ch + "-1|P|2.5.1\rPID|1||123\r"),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[ch] = id
	}

	h.addUser(t, "east-viewer", store.RoleViewer, "East")
	h.addUser(t, "east-editor", store.RoleEditor, "East")
	h.addUser(t, "east-admin", store.RoleAdmin, "East")
	h.addUser(t, "monitor", store.RoleMonitor)
	h.addUser(t, "operator", store.RoleOperator)
	return h, ids
}

// addUser creates an account, optionally limited to channel groups, and signs it in for h.do.
func (h *harness) addUser(t *testing.T, name string, role store.Role, groups ...string) *store.User {
	t.Helper()
	u, err := h.store.CreateUser(t.Context(), name, groupPassword, role)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) > 0 {
		if err := h.store.SetChannelGroups(t.Context(), u.ID, groups); err != nil {
			t.Fatal(err)
		}
	}
	token, _, err := h.store.Authenticate(t.Context(), name, groupPassword, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	h.tokens[name] = token
	return u
}

func TestALimitedPersonSeesOnlyTheirGroupsChannels(t *testing.T) {
	h, _ := newGroupHarness(t)

	body := decodeBody[channelListResponse](t, h.do("east-viewer", http.MethodGet, "/api/channels", nil))
	if len(body.Channels) != 1 || body.Channels[0].Name != "east-adt" {
		t.Fatalf("East's viewer sees %+v, want east-adt alone (not West's, not the ungrouped one)", body.Channels)
	}

	// Asked for by name, West's channel does not exist for them: 404, not 403, so its name is not confirmed either.
	for _, path := range []string{"/api/channels/west-adt", "/api/channels/west-adt/yaml", "/api/channels/loose/history"} {
		if rec := h.do("east-viewer", http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as East's viewer: %d, want 404", path, rec.Code)
		}
	}
	if rec := h.do("east-viewer", http.MethodGet, "/api/channels/east-adt", nil); rec.Code != http.StatusOK {
		t.Errorf("East's viewer cannot open East's own channel: %d", rec.Code)
	}

	// Somebody with no limit is unaffected.
	all := decodeBody[channelListResponse](t, h.do("viewer", http.MethodGet, "/api/channels", nil))
	if len(all.Channels) != 3 {
		t.Errorf("an unlimited viewer sees %d channels, want 3", len(all.Channels))
	}
}

func TestALimitedPersonSeesOnlyTheirGroupsMessages(t *testing.T) {
	h, ids := newGroupHarness(t)

	list := decodeBody[struct {
		Messages []msgstore.Message `json:"messages"`
		Total    int                `json:"total"`
		Channels []string           `json:"channels"`
	}](t, h.do("east-viewer", http.MethodGet, "/api/messages", nil))
	if list.Total != 1 || len(list.Messages) != 1 || list.Messages[0].Channel != "east-adt" {
		t.Errorf("East's viewer lists %d messages (total %d): %+v", len(list.Messages), list.Total, list.Messages)
	}
	if len(list.Channels) != 1 || list.Channels[0] != "east-adt" {
		t.Errorf("the channel filter offers %v, want only east-adt", list.Channels)
	}

	// Asking for West's channel by name returns nothing rather than West's messages.
	west := decodeBody[struct {
		Total int `json:"total"`
	}](t, h.do("east-viewer", http.MethodGet, "/api/messages?channel=west-adt", nil))
	if west.Total != 0 {
		t.Errorf("filtering on West's channel returned %d of West's messages", west.Total)
	}

	// Opening one of West's messages by id: not found.
	if rec := h.do("east-viewer", http.MethodGet, fmt.Sprintf("/api/messages/%d", ids["west-adt"]), nil); rec.Code != http.StatusNotFound {
		t.Errorf("opening West's message by id: %d, want 404", rec.Code)
	}

	// The counts agree with the list: a total of three failures and one shown would be its own disclosure.
	stats := decodeBody[struct {
		Stats msgstore.Stats `json:"stats"`
	}](t, h.do("east-viewer", http.MethodGet, "/api/stats", nil))
	if stats.Stats.Total != 1 || len(stats.Stats.ByChannel) != 1 || stats.Stats.ByChannel["east-adt"] != 1 {
		t.Errorf("East's viewer's counts are %+v", stats.Stats)
	}

	// Searching message contents.
	found := decodeBody[msgstore.ExpressionResult](t, h.do("east-viewer", http.MethodPost, "/api/messages/search",
		map[string]any{"where": `PID-3 == "123"`}))
	for _, m := range found.Matches {
		if m.Channel != "east-adt" {
			t.Errorf("content search found %s's message for East's viewer", m.Channel)
		}
	}
	if len(found.Matches) != 1 {
		t.Errorf("content search found %d messages, want East's one", len(found.Matches))
	}
}

func TestALimitedEditorKeepsChannelsInTheirGroups(t *testing.T) {
	h, _ := newGroupHarness(t)

	// Creating a channel in West, or in no group, would put it out of their own sight or into West's.
	for _, group := range []string{"West", ""} {
		rec := h.do("east-editor", http.MethodPost, "/api/channels",
			channelPayload{YAML: groupedChannelYAML("new-feed", group, ":0")})
		if rec.Code != http.StatusForbidden {
			t.Errorf("East's editor created a channel in group %q: %d", group, rec.Code)
		}
	}
	if rec := h.do("east-editor", http.MethodPost, "/api/channels",
		channelPayload{YAML: groupedChannelYAML("east-orm", "East", ":0")}); rec.Code != http.StatusOK {
		t.Errorf("East's editor cannot create a channel in East: %d %s", rec.Code, rec.Body)
	}

	// Moving East's channel to West.
	if rec := h.do("east-editor", http.MethodPut, "/api/channels/east-adt",
		channelPayload{YAML: groupedChannelYAML("east-adt", "West", ":0")}); rec.Code != http.StatusForbidden {
		t.Errorf("East's editor moved a channel to West: %d", rec.Code)
	}
	// Changing West's channel: it does not exist for them.
	if rec := h.do("east-editor", http.MethodPut, "/api/channels/west-adt",
		channelPayload{YAML: groupedChannelYAML("west-adt", "East", ":0")}); rec.Code != http.StatusNotFound {
		t.Errorf("East's editor reached West's channel: %d", rec.Code)
	}
	// A code table every facility shares.
	if rec := h.do("east-editor", http.MethodPut, "/api/codesets/table", map[string]any{}); rec.Code != http.StatusForbidden {
		t.Errorf("East's editor may change a shared code table: %d", rec.Code)
	}
}

func TestALimitedAdministratorLooksAfterTheirOwnPeopleOnly(t *testing.T) {
	h, _ := newGroupHarness(t)

	users := decodeBody[struct {
		Users []store.User `json:"users"`
	}](t, h.do("east-admin", http.MethodGet, "/api/users", nil))
	if len(users.Users) != 3 {
		t.Errorf("East's administrator sees %d people, want East's three", len(users.Users))
	}
	for _, u := range users.Users {
		if len(u.ChannelGroups) == 0 {
			t.Errorf("East's administrator is shown %s, who sees every channel", u.Username)
		}
	}

	// Somebody who sees every channel cannot be changed by them: not their password, not their role.
	admin, err := h.store.GetUser(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if rec := h.do("east-admin", http.MethodPut, fmt.Sprintf("/api/users/%d", admin.ID),
		map[string]any{"password": "another sufficiently long password"}); rec.Code != http.StatusNotFound {
		t.Errorf("East's administrator changed the central administrator's password: %d", rec.Code)
	}

	// A colleague they create is East's, without being asked.
	rec := h.do("east-admin", http.MethodPost, "/api/users",
		map[string]any{"username": "east-nurse", "password": groupPassword, "role": "viewer"})
	if rec.Code != http.StatusOK {
		t.Fatalf("East's administrator cannot create a colleague: %d %s", rec.Code, rec.Body)
	}
	if u := decodeBody[store.User](t, rec); len(u.ChannelGroups) != 1 || u.ChannelGroups[0] != "East" {
		t.Errorf("the colleague was given %v, want East", u.ChannelGroups)
	}
	// Nor can they hand out West, or lift a limit.
	rec = h.do("east-admin", http.MethodPost, "/api/users",
		map[string]any{"username": "west-nurse", "password": groupPassword, "role": "viewer", "channelGroups": []string{"West"}})
	if rec.Code != http.StatusForbidden {
		t.Errorf("East's administrator gave out West: %d", rec.Code)
	}

	// Settings, sign-on and a restart change the whole server.
	for _, route := range [][2]string{{http.MethodGet, "/api/settings"}, {http.MethodGet, "/api/signon"},
		{http.MethodPost, "/api/server/restart"}, {http.MethodPut, "/api/alerts/rules"}} {
		if rec := h.do("east-admin", route[0], route[1], map[string]any{}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as East's administrator: %d, want 403", route[0], route[1], rec.Code)
		}
	}
}

func TestAMonitorSeesHealthButNoPatientData(t *testing.T) {
	h, ids := newGroupHarness(t)

	for _, path := range []string{"/api/me", "/api/status", "/api/stats", "/api/channels"} {
		if rec := h.do("monitor", http.MethodGet, path, nil); rec.Code != http.StatusOK {
			t.Errorf("a monitor cannot read %s: %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/api/messages", fmt.Sprintf("/api/messages/%d", ids["east-adt"]),
		"/api/channels/east-adt", "/api/channels/east-adt/yaml", "/api/audit"} {
		if rec := h.do("monitor", http.MethodGet, path, nil); rec.Code != http.StatusForbidden {
			t.Errorf("a monitor can read %s: %d", path, rec.Code)
		}
	}
}

func TestAnOperatorResendsButDoesNotChangeChannels(t *testing.T) {
	h, ids := newGroupHarness(t)

	// Allowed by role; whatever happens next (no engine runs here) is not a 403.
	if rec := h.do("operator", http.MethodPost, fmt.Sprintf("/api/messages/%d/reprocess", ids["east-adt"]), nil); rec.Code == http.StatusForbidden {
		t.Errorf("an operator may not reprocess a message: %s", rec.Body)
	}
	if rec := h.do("operator", http.MethodPut, "/api/channels/east-adt",
		channelPayload{YAML: groupedChannelYAML("east-adt", "East", ":0")}); rec.Code != http.StatusForbidden {
		t.Errorf("an operator changed a channel: %d", rec.Code)
	}
	// And a viewer still may not resend.
	if rec := h.do("viewer", http.MethodPost, fmt.Sprintf("/api/messages/%d/reprocess", ids["east-adt"]), nil); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer reprocessed a message: %d", rec.Code)
	}
}

func TestTheChannelLimitRefusesANewChannel(t *testing.T) {
	h, _ := newGroupHarness(t) // three channels already

	if _, err := h.store.SetTenantLimits(t.Context(), store.DefaultTenant, tenant.Limits{MaxChannels: 3}); err != nil {
		t.Fatal(err)
	}
	rec := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: groupedChannelYAML("fourth", "", ":0")})
	if rec.Code != http.StatusConflict {
		t.Fatalf("a fourth channel over a limit of three: %d %s", rec.Code, rec.Body)
	}

	if _, err := h.store.SetTenantLimits(t.Context(), store.DefaultTenant, tenant.Limits{MaxChannels: 4}); err != nil {
		t.Fatal(err)
	}
	if rec := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: groupedChannelYAML("fourth", "", ":0")}); rec.Code != http.StatusOK {
		t.Fatalf("raising the limit did not let the channel in: %d %s", rec.Code, rec.Body)
	}

	if _, err := h.store.SetTenantLimits(t.Context(), store.DefaultTenant, tenant.Limits{MaxChannels: -1}); err == nil {
		t.Error("a negative limit was accepted")
	}
}
