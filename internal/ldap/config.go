package ldap

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config describes a directory to authenticate against.
//
// Every field name that differs between Active Directory and everything else is configuration rather than a guess,
// because getting one wrong is silent: a username attribute that does not exist finds nobody, which is reported as a
// wrong password.
type Config struct {
	// Addr is the directory's host and port. Required.
	Addr string `yaml:"addr"`

	// TLS uses LDAPS, encrypted from the first byte. Usually port 636.
	TLS bool `yaml:"tls,omitempty"`

	// StartTLS upgrades a plain connection on port 389 before anything is sent.
	StartTLS bool `yaml:"start_tls,omitempty"`

	// Insecure allows an unencrypted connection, which sends passwords in cleartext.
	Insecure bool `yaml:"insecure,omitempty"`

	// BindDN is the service account used to search. Empty means bind anonymously.
	BindDN string `yaml:"bind_dn,omitempty"`

	// BindPassword is the service account's password.
	BindPassword string `yaml:"bind_password,omitempty"`

	// UserBaseDN is where to search for people. Required.
	UserBaseDN string `yaml:"user_base_dn"`

	// UsernameAttribute is what a person types. sAMAccountName on Active Directory, uid elsewhere.
	UsernameAttribute string `yaml:"username_attribute,omitempty"`

	// UniqueIDAttribute holds an identifier that never changes for a person.
	//
	// objectGUID on Active Directory, entryUUID on OpenLDAP. Both are operational attributes the server maintains.
	//
	// This matters more than it looks. Without it a person is identified by their DN, and a DN is not stable: moving
	// somebody between organisational units, or a name change that alters their CN, changes it. The next sign-in
	// would then look like a different person, create a second account, and leave whatever was attached to the first
	// one - saved views, audit history, the fact that they are an administrator - pointing at an account nobody uses.
	//
	// Left empty by default because a wrong attribute name is worse: it would find nothing and refuse every sign-in.
	// Setting it is recommended in the documentation, and the consequence of not setting it is recorded there.
	UniqueIDAttribute string `yaml:"unique_id_attribute,omitempty"`

	// UserFilter narrows the search further, and-ed with the username match.
	//
	// The place to exclude disabled accounts. On AD a disabled account still binds successfully in some
	// configurations, so without this a person who left the organisation last year can still sign in.
	UserFilter string `yaml:"user_filter,omitempty"`

	// NameAttribute holds the display name.
	NameAttribute string `yaml:"name_attribute,omitempty"`

	// EmailAttribute holds the email address.
	EmailAttribute string `yaml:"email_attribute,omitempty"`

	// MemberOfAttribute is the attribute on a person listing their groups. memberOf on Active Directory.
	MemberOfAttribute string `yaml:"member_of_attribute,omitempty"`

	// GroupBaseDN is where to search for groups that list their members. Used by OpenLDAP-style directories.
	GroupBaseDN string `yaml:"group_base_dn,omitempty"`

	// GroupFilter matches groups containing a person. %d is replaced with their DN, %u with their username.
	GroupFilter string `yaml:"group_filter,omitempty"`

	// GroupNameAttribute holds a group's name.
	GroupNameAttribute string `yaml:"group_name_attribute,omitempty"`

	// Roles maps group names to Perfuse roles.
	//
	// A person in no mapped group is refused, never given a default role. Same decision as OIDC: a directory has
	// thousands of accounts, and defaulting to viewer would give every one of them a way in and a view of live
	// clinical message flow.
	Roles map[string]string `yaml:"roles"`

	// CreateUsers allows a first sign-in to create a local record.
	CreateUsers bool `yaml:"create_users,omitempty"`

	// ButtonLabel names the directory on the sign-in form.
	ButtonLabel string `yaml:"button_label,omitempty"`

	// Timeout bounds connecting and each exchange.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// DefaultConfigTimeout is used when Timeout is unset.
const DefaultConfigTimeout = 10 * time.Second

// ResolvedTimeout is the timeout with its default applied.
func (c *Config) ResolvedTimeout() time.Duration {
	if c == nil || c.Timeout <= 0 {
		return DefaultConfigTimeout
	}
	return c.Timeout
}

// Validate checks the configuration and fills defaults.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("the LDAP configuration is empty")
	}

	var problems []string

	if strings.TrimSpace(c.Addr) == "" {
		problems = append(problems, "addr is required, the directory's host and port")
	}
	if strings.TrimSpace(c.UserBaseDN) == "" {
		problems = append(problems, "user_base_dn is required, the part of the directory holding people")
	}

	if c.UsernameAttribute == "" {
		// uid rather than sAMAccountName. Either default is wrong half the time; uid is the standard LDAP
		// attribute and AD deployments are the ones with an administrator who knows to change it.
		c.UsernameAttribute = "uid"
	}
	if c.GroupNameAttribute == "" {
		c.GroupNameAttribute = "cn"
	}
	if c.ButtonLabel == "" {
		c.ButtonLabel = "your organisation's directory"
	}

	if c.TLS && c.StartTLS {
		problems = append(problems, "tls and start_tls cannot both be set; tls encrypts from the first byte on "+
			"port 636, start_tls upgrades a plain connection on port 389")
	}
	if !c.TLS && !c.StartTLS && !c.Insecure {
		problems = append(problems, "this directory connection would send passwords in cleartext; set tls or "+
			"start_tls, or set insecure to accept that deliberately")
	}
	if c.Insecure && (c.TLS || c.StartTLS) {
		problems = append(problems, "insecure cannot be set alongside tls or start_tls")
	}

	if c.BindDN != "" && c.BindPassword == "" {
		// Refused rather than attempted. A bind_dn with no password is the anonymous bind again, and it would
		// appear to work against a directory that allows it - as a service account that can read nothing, which
		// then looks like every user belonging to no groups.
		problems = append(problems, "bind_dn is set without bind_password, which would be an anonymous bind "+
			"rather than a service account sign-in")
	}

	if c.MemberOfAttribute == "" && c.GroupBaseDN == "" {
		// One or the other is required. Without either there is no way to read groups, every sign-in maps to no
		// roles, and every person is refused - which reads as a broken directory rather than a missing setting.
		problems = append(problems, "either member_of_attribute or group_base_dn is needed, or no group can be "+
			"read and every sign-in will be refused for having no role")
	}

	if len(c.Roles) == 0 {
		problems = append(problems, "roles is required, mapping directory group names to Perfuse roles; "+
			"without it nobody can sign in")
	}

	for group, role := range c.Roles {
		if strings.TrimSpace(group) == "" {
			problems = append(problems, "a role mapping has an empty group name")
		}
		switch role {
		case "viewer", "editor", "admin", "platform":
		default:
			problems = append(problems, fmt.Sprintf("the group %q maps to %q, which is not a role; "+
				"use viewer, editor, admin or platform", group, role))
		}
	}

	if c.GroupBaseDN != "" && c.GroupFilter == "" {
		problems = append(problems, "group_base_dn is set without group_filter, so there is nothing to match "+
			"members with; a groupOfNames directory usually wants (&(objectClass=groupOfNames)(member=%d))")
	}
	if c.GroupFilter != "" && !strings.Contains(c.GroupFilter, "%d") && !strings.Contains(c.GroupFilter, "%u") {
		problems = append(problems, "group_filter contains neither %d nor %u, so it does not refer to the person "+
			"signing in and would return the same groups for everybody")
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the LDAP configuration cannot be used:\n  - %s", strings.Join(problems, "\n  - "))
	}

	return nil
}

// filterFor builds the search filter for a username.
//
// The username is escaped here and nowhere else, so there is one place to look for the injection defence rather than a
// question of whether each call site remembered.
func (c *Config) filterFor(username string) string {
	safe := EscapeFilter(username)
	match := "(" + c.UsernameAttribute + "=" + safe + ")"

	if c.UserFilter == "" {
		return match
	}

	extra := c.UserFilter
	if !strings.HasPrefix(extra, "(") {
		extra = "(" + extra + ")"
	}
	return "(&" + match + extra + ")"
}

// groupFilterFor builds the group membership filter for a person.
func (c *Config) groupFilterFor(dn, username string) string {
	out := c.GroupFilter
	out = strings.ReplaceAll(out, "%d", EscapeFilter(dn))
	out = strings.ReplaceAll(out, "%u", EscapeFilter(username))
	return out
}

// RoleFor maps group names to a role, taking the strongest.
//
// The strongest rather than the first, because group order is the directory's business and a person in both a viewer and
// an admin group should not get a different answer depending on how the directory happened to sort them. Same rule as
// OIDC, and the same reason: an inconsistent answer to "what can this person do" is worse than a wrong one, because it
// cannot be reproduced.
func (c *Config) RoleFor(groups []string) (string, bool) {
	best := ""
	rank := map[string]int{"viewer": 1, "editor": 2, "admin": 3, "platform": 4}

	// The comparison is case-insensitive. Directories return group names with whatever capitalisation they were
	// created with, and a mapping written as perfuse-admins must not silently miss Perfuse-Admins.
	lowered := make(map[string]string, len(c.Roles))
	for group, role := range c.Roles {
		lowered[strings.ToLower(strings.TrimSpace(group))] = role
	}

	for _, g := range groups {
		role, ok := lowered[strings.ToLower(strings.TrimSpace(g))]
		if !ok {
			continue
		}
		if rank[role] > rank[best] {
			best = role
		}
	}

	return best, best != ""
}

// LoadConfig reads a configuration file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the LDAP configuration file could not be read: %w", err)
	}

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// Unknown keys are an error. A misspelled username_attribute would otherwise leave the default in place and
	// fail as a wrong password for every person in an Active Directory.
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("the LDAP configuration file %s could not be understood: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
