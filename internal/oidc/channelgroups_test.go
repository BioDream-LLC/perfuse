package oidc

import (
	"reflect"
	"testing"
)

func TestSignInMapsOperatorAndMonitorAndTheirChannelGroups(t *testing.T) {
	m := &RoleMapping{
		Editor:   []string{"Lab-Editors"},
		Operator: []string{"Night-Desk"},
		Viewer:   []string{"Everyone-Clinical"},
		Monitor:  []string{"Wall-Screens"},
		ChannelGroups: map[string][]string{
			"lab-editors": {"lab", "pathology"},
			"Night-Desk":  {"adt"},
		},
	}
	if errs := m.Validate(); len(errs) != 0 {
		t.Fatalf("a valid mapping was refused: %v", errs)
	}
	for _, tc := range []struct {
		groups []string
		role   Role
		cgs    []string
	}{
		{[]string{"Wall-Screens"}, RoleMonitor, nil},
		{[]string{"night-desk"}, RoleOperator, []string{"adt"}},
		// The strongest role, with only the reach of the groups that granted it: never editor of every channel.
		{[]string{"Lab-Editors", "Everyone-Clinical"}, RoleEditor, []string{"lab", "pathology"}},
		{[]string{"Everyone-Clinical"}, RoleViewer, nil},
		{[]string{"Somebody-Else"}, RoleNone, nil},
	} {
		role := m.RoleFor(tc.groups)
		if role != tc.role {
			t.Errorf("%v: role %q, want %q", tc.groups, role, tc.role)
		}
		if got := m.ChannelGroupsFor(tc.groups, role); !reflect.DeepEqual(got, tc.cgs) {
			t.Errorf("%v: channel groups %v, want %v", tc.groups, got, tc.cgs)
		}
	}
	if !m.ManagesChannelGroups() {
		t.Error("a mapping naming channel groups does not manage them")
	}
	if (&RoleMapping{Viewer: []string{"x"}}).ManagesChannelGroups() {
		t.Error("a mapping naming no channel groups manages them, which would wipe what an administrator set")
	}
}

func TestChannelGroupsForAGroupWithNoRoleAreRefused(t *testing.T) {
	m := &RoleMapping{Viewer: []string{"a"}, ChannelGroups: map[string][]string{"b": {"lab"}}}
	if errs := m.Validate(); len(errs) == 0 {
		t.Error("channel groups for a group that grants no role were accepted")
	}
}

func TestGroupsNamingChannelGroupsGiveARoleInEach(t *testing.T) {
	m := &RoleMapping{
		Editor:        []string{"Lab-Editors"},
		Viewer:        []string{"Clinical-IT", "Everyone"},
		ChannelGroups: map[string][]string{"Lab-Editors": {"lab"}, "Clinical-IT": {"lab", "adt"}},
	}
	role, cgs, per := m.AccessFor([]string{"Lab-Editors", "Clinical-IT"})
	if role != RoleEditor || !reflect.DeepEqual(cgs, []string{"adt", "lab"}) ||
		!reflect.DeepEqual(per, map[string]Role{"adt": RoleViewer}) {
		t.Errorf("got %s %v %v, want editor of lab and viewer of adt", role, cgs, per)
	}
	// One of the person's groups grants every channel: no list to hang a role per group on, so the editor's own reach.
	role, cgs, per = m.AccessFor([]string{"Lab-Editors", "Everyone"})
	if role != RoleEditor || !reflect.DeepEqual(cgs, []string{"lab"}) || per != nil {
		t.Errorf("got %s %v %v, want editor of lab alone", role, cgs, per)
	}
}
