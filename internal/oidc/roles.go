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
	Viewer   []string

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
	RoleViewer   Role = "viewer"
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
		{RoleViewer, m.Viewer},
	} {
		for _, want := range candidate.groups {
			if have[normalise(want)] {
				return candidate.role
			}
		}
	}

	return RoleNone
}

// Validate reports problems with a mapping.
func (m *RoleMapping) Validate() []error {
	if m == nil {
		return []error{fmt.Errorf("oidc: no group to role mapping is configured, so no federated sign-in could be " +
			"granted any access")}
	}

	var errs []error

	if len(m.Platform) == 0 && len(m.Admin) == 0 && len(m.Editor) == 0 && len(m.Viewer) == 0 {
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
		{"viewer", m.Viewer},
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
		{"viewer", m.Viewer},
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
