package api

import (
	"net/http"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// A different role in each channel group: an editor of East's channels who views West's.
func TestAPersonCanEditOneGroupAndOnlyViewAnother(t *testing.T) {
	h, _ := newGroupHarness(t)
	u, err := h.store.CreateUser(t.Context(), "split", groupPassword, store.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetChannelGroups(t.Context(), u.ID, []string{"East", "West"}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetGroupRoles(t.Context(), u.ID, map[string]store.Role{"East": store.RoleEditor}); err != nil {
		t.Fatal(err)
	}
	token, _, err := h.store.Authenticate(t.Context(), "split", groupPassword, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	h.tokens["split"] = token

	list := decodeBody[channelListResponse](t, h.do("split", http.MethodGet, "/api/channels", nil))
	if len(list.Channels) != 2 {
		t.Fatalf("sees %d channels, want East's and West's", len(list.Channels))
	}

	if rec := h.do("split", http.MethodPut, "/api/channels/east-adt",
		channelPayload{YAML: groupedChannelYAML("east-adt", "East", ":0")}); rec.Code != http.StatusOK {
		t.Errorf("cannot change East's channel, where they are editor: %d %s", rec.Code, rec.Body)
	}
	// West's channel is visible, so the refusal says why rather than pretending it does not exist.
	if rec := h.do("split", http.MethodPut, "/api/channels/west-adt",
		channelPayload{YAML: groupedChannelYAML("west-adt", "West", ":0")}); rec.Code != http.StatusForbidden {
		t.Errorf("changed West's channel, where they only view: %d", rec.Code)
	}
	if rec := h.do("split", http.MethodPost, "/api/channels",
		channelPayload{YAML: groupedChannelYAML("west-new", "West", ":0")}); rec.Code != http.StatusForbidden {
		t.Errorf("created a channel in West, where they only view: %d", rec.Code)
	}
	if rec := h.do("split", http.MethodPost, "/api/channels",
		channelPayload{YAML: groupedChannelYAML("east-new", "East", ":0")}); rec.Code != http.StatusOK {
		t.Errorf("cannot create a channel in East: %d %s", rec.Code, rec.Body)
	}
	// Still limited: the shared code table is not theirs to change, whatever their strongest role.
	if rec := h.do("split", http.MethodPut, "/api/codesets/table", map[string]any{}); rec.Code != http.StatusForbidden {
		t.Errorf("changed a shared code table: %d", rec.Code)
	}
}

func TestPerGroupRolesAreCheckedWhenGranted(t *testing.T) {
	h, _ := newGroupHarness(t)

	// An account that sees every channel has one role.
	rec := h.do("admin", http.MethodPost, "/api/users", map[string]any{"username": "everyone", "password": groupPassword,
		"role": "viewer", "groupRoles": map[string]string{"East": "editor"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a per-group role on an unlimited account: %d", rec.Code)
	}
	// A group the account is not in.
	rec = h.do("admin", http.MethodPost, "/api/users", map[string]any{"username": "eastonly", "password": groupPassword,
		"role": "viewer", "channelGroups": []string{"East"}, "groupRoles": map[string]string{"West": "editor"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a role in a group the account is not in: %d", rec.Code)
	}
	rec = h.do("admin", http.MethodPost, "/api/users", map[string]any{"username": "both", "password": groupPassword,
		"role": "viewer", "channelGroups": []string{"East", "West"}, "groupRoles": map[string]string{"East": "operator"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("a valid per-group role was refused: %d %s", rec.Code, rec.Body)
	}
	got := decodeBody[store.User](t, rec)
	if got.GroupRoles["East"] != store.RoleOperator {
		t.Errorf("the per-group role did not come back: %+v", got.GroupRoles)
	}

	// East's administrator cannot make anyone stronger than themselves in East.
	rec = h.do("east-admin", http.MethodPost, "/api/users", map[string]any{"username": "east-two", "password": groupPassword,
		"role": "viewer", "channelGroups": []string{"East"}, "groupRoles": map[string]string{"East": "admin"}})
	if rec.Code != http.StatusOK {
		t.Errorf("East's administrator cannot make an East administrator: %d %s", rec.Code, rec.Body)
	}
}
