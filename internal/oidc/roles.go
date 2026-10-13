package oidc

import (
	"fmt"
	"sort"
	"strings"
)

// RoleMapping decides a Perfuse role from an identity provider's group claims.
//
// Deliberately separate from the identity provider. A provider knows who somebody is and which groups they are in; it has no
// opinion about what those groups mean in an interface engine, and it should not - the same directory group means different
// things in different applications.
//
// The mapping is explicit, ordered by privilege, and has no default. That last part is the important one: a default role turns
// every person in the directory into a Perfuse user, which for a hospital directory is tens of thousands of people who can see
// patient data flowing through channels.
type RoleMapping struct {
	// Platform, Admin, Editor and Viewer are the group names granting each role.
	//
	// A person in groups matching more than one gets the most privileged, which is the only safe direction to resolve it:
	// the alternative is somebody losing access they are entitled to because they are also in a lesser group.
	Platform []string
	Admin    []string
	Editor   []string
	Operator []string
	Viewer   []string
	Monitor  []string

	// ChannelGroups limits what a directory group sees to these Perfuse channel groups (a channel's `group:`). A directory group
	// absent here, or present with an empty list, sees every channel.
	//
	// Only consulted when at least one group names channel groups; until then sign-in leaves each account's channel groups as
	// an administrator set them, which is what every configuration written before this existed relies on.
	ChannelGroups map[string][]string

	// CaseSensitive compares group names exactly. Defaults to insensitive.
	//
	// Insensitive by default because Active Directory group names are routinely written with different capitalisation in
	// the directory and in a configuration file, and a mapping that silently matches nothing is very hard to diagnose - it
	// looks identical to the group claim being absent.
	CaseSensitive bool
}

// Role is a Perfuse role name as the mapping produces it.
//
// A string rather than the store's type, so this package does not depend on the store. The API layer converts.
type Role string

// The roles, in increasing order of privilege.
const (
	RoleNone     Role = ""
	RoleMonitor  Role = "monitor"
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleEditor   Role = "editor"
	RoleAdmin    Role = "admin"
	RolePlatform Role = "platform"
)

// RoleFor decides the role for a set of group claims.
//
// Returns RoleNone when nothing matches, which the caller must treat as a refusal rather than as a viewer.
func (m *RoleMapping) RoleFor(groups []string) Role {
	if m == nil {
		return RoleNone
	}

	normalise := func(s string) string {
		s = strings.TrimSpace(s)
		if !m.CaseSensitive {
			s = strings.ToLower(s)
		}
		return s
	}

	have := make(map[string]bool, len(groups))
	for _, g := range groups {
		if n := normalise(g); n != "" {
			have[n] = true
		}
	}

	// Checked most privileged first, so somebody in both an admin group and a viewer group is an admin.
	for _, candidate := range []struct {
		role   Role
		groups []string
	}{
		{RolePlatform, m.Platform},
		{RoleAdmin, m.Admin},
		{RoleEditor, m.Editor},
		{RoleOperator, m.Operator},
		{RoleViewer, m.Viewer},
		{RoleMonitor, m.Monitor},
	} {
		for _, want := range candidate.groups {
			if have[normalise(want)] {
				return candidate.role
			}
		}
	}

	return RoleNone
}

// ManagesChannelGroups says whether sign-in decides each person's channel groups: true once any group names channel groups.
func (m *RoleMapping) ManagesChannelGroups() bool {
	if m == nil {
		return false
	}
	for _, list := range m.ChannelGroups {
		if len(list) > 0 {
			return true
		}
	}
	return false
}

// ChannelGroupsFor decides the channel groups for a person: the union of those named by the groups that granted their role.
// Nil means every channel.
//
// Only the groups that granted the role count, not every group the person is in. Somebody in a group that may edit the lab's
// channels and in another that may view every channel would otherwise become an editor of every channel - the strongest role
// joined to the widest reach, which neither group grants. Taking the reach of the role's own groups errs towards too little,
// which the person notices and asks about, rather than too much, which nobody notices.
func (m *RoleMapping) ChannelGroupsFor(groups []string, role Role) []string {
	if !m.ManagesChannelGroups() {
		return nil
	}
	normalise := func(s string) string {
		s = strings.TrimSpace(s)
		if !m.CaseSensitive {
			s = strings.ToLower(s)
		}
		return s
	}
	granting := map[string]bool{}
	for _, g := range m.groupsFor(role) {
		granting[normalise(g)] = true
	}
	limits := map[string][]string{}
	for g, list := range m.ChannelGroups {
		limits[normalise(g)] = list
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		n := normalise(g)
		if !granting[n] {
			continue
		}
		list, ok := limits[n]
		if !ok || len(list) == 0 {
			return nil // this group grants the role on every channel
		}
		for _, cg := range list {
			cg = strings.TrimSpace(cg)
			if cg != "" && !seen[cg] {
				seen[cg] = true
				out = append(out, cg)
			}
		}
	}
	sort.Strings(out)
	return out
}

// AccessFor decides everything a sign-in grants: the role, the channel groups (nil: every channel) and, where the person's
// groups give different roles in different channel groups, the role in each one that differs from the role returned.
//
// When every group the person matched names channel groups, each channel group gets the strongest role among the groups that
// name it: in Lab-Editors (editor, lab) and Clinical-IT (viewer, lab and adt), a person edits the lab's channels and views the
// admissions ones. When one of them grants every channel, a role per group cannot be expressed - "every channel" has no list to
// hang roles on - and the reach of the strongest role's own groups applies, as ChannelGroupsFor says.
func (m *RoleMapping) AccessFor(groups []string) (Role, []string, map[string]Role) {
	role := m.RoleFor(groups)
	if role == RoleNone || !m.ManagesChannelGroups() {
		return role, nil, nil
	}
	normalise := func(s string) string {
		s = strings.TrimSpace(s)
		if !m.CaseSensitive {
			s = strings.ToLower(s)
		}
		return s
	}
	roleOf := map[string]Role{}
	for _, r := range []Role{RoleMonitor, RoleViewer, RoleOperator, RoleEditor, RoleAdmin, RolePlatform} {
		for _, g := range m.groupsFor(r) {
			roleOf[normalise(g)] = r
		}
	}
	limits := map[string][]string{}
	for g, list := range m.ChannelGroups {
		limits[normalise(g)] = list
	}
	rank := map[Role]int{RoleMonitor: 1, RoleViewer: 2, RoleOperator: 3, RoleEditor: 4, RoleAdmin: 5, RolePlatform: 6}
	per := map[string]Role{}
	for _, g := range groups {
		n := normalise(g)
		r, ok := roleOf[n]
		if !ok {
			continue
		}
		list := limits[n]
		cleaned := 0
		for _, cg := range list {
			if cg = strings.TrimSpace(cg); cg != "" {
				cleaned++
				if rank[r] > rank[per[cg]] {
					per[cg] = r
				}
			}
		}
		if cleaned == 0 {
			return role, m.ChannelGroupsFor(groups, role), nil
		}
	}
	out := make([]string, 0, len(per))
	var differs map[string]Role
	for cg, r := range per {
		out = append(out, cg)
		if r != role {
			if differs == nil {
				differs = map[string]Role{}
			}
			differs[cg] = r
		}
	}
	sort.Strings(out)
	return role, out, differs
}

func (m *RoleMapping) groupsFor(role Role) []string {
	switch role {
	case RolePlatform:
		return m.Platform
	case RoleAdmin:
		return m.Admin
	case RoleEditor:
		return m.Editor
	case RoleOperator:
		return m.Operator
	case RoleViewer:
		return m.Viewer
	case RoleMonitor:
		return m.Monitor
	}
	return nil
}

// Validate reports problems with a mapping.
func (m *RoleMapping) Validate() []error {
	if m == nil {
		return []error{fmt.Errorf("oidc: no group to role mapping is configured, so no federated sign-in could be " +
			"granted any access")}
	}

	var errs []error

	if len(m.Platform) == 0 && len(m.Admin) == 0 && len(m.Editor) == 0 && len(m.Operator) == 0 && len(m.Viewer) == 0 &&
		len(m.Monitor) == 0 {
		errs = append(errs, fmt.Errorf("oidc: the group to role mapping is empty, so every federated sign-in would be "+
			"refused for having no role; map at least one group"))
	}

	// A group appearing under two roles is refused rather than resolved. The resolution rule - most privileged wins - is
	// right for somebody in two groups and wrong as a way to read a configuration file, where it is far more likely to be
	// a mistake than an intention.
	seen := map[string]string{}
	for _, pair := range []struct {
		role   string
		groups []string
	}{
		{"platform", m.Platform},
		{"admin", m.Admin},
		{"editor", m.Editor},
		{"operator", m.Operator},
		{"viewer", m.Viewer},
		{"monitor", m.Monitor},
	} {
		for _, g := range pair.groups {
			key := strings.TrimSpace(g)
			if !m.CaseSensitive {
				key = strings.ToLower(key)
			}
			if key == "" {
				errs = append(errs, fmt.Errorf("oidc: the %s role maps an empty group name", pair.role))
				continue
			}
			if other, dup := seen[key]; dup {
				errs = append(errs, fmt.Errorf("oidc: group %q is mapped to both %s and %s; remove one, because which "+
					"wins is a rule for people in two groups rather than a way to write a configuration",
					g, other, pair.role))
				continue
			}
			seen[key] = pair.role
		}
	}

	for g := range m.ChannelGroups {
		key := strings.TrimSpace(g)
		if !m.CaseSensitive {
			key = strings.ToLower(key)
		}
		if _, ok := seen[key]; !ok {
			errs = append(errs, fmt.Errorf("oidc: group %q names channel groups but grants no role, so nobody could sign in "+
				"through it; map it to a role as well", g))
		}
	}

	return errs
}

// Describe summarises a mapping for the specification and the interface.
//
// Sorted, because it is user-visible output and Go maps range randomly.
func (m *RoleMapping) Describe() string {
	if m == nil {
		return "no groups are mapped to roles"
	}

	var parts []string
	for _, pair := range []struct {
		role   string
		groups []string
	}{
		{"platform", m.Platform},
		{"admin", m.Admin},
		{"editor", m.Editor},
		{"operator", m.Operator},
		{"viewer", m.Viewer},
		{"monitor", m.Monitor},
	} {
		if len(pair.groups) == 0 {
			continue
		}
		sorted := make([]string, len(pair.groups))
		copy(sorted, pair.groups)
		sort.Strings(sorted)
		parts = append(parts, strings.Join(sorted, ", ")+" grant "+pair.role)
	}

	if len(parts) == 0 {
		return "no groups are mapped to roles"
	}

	return strings.Join(parts, "; ")
}
