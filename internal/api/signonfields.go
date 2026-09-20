package api

import (
	"github.com/biodream-llc/perfuse/internal/ldap"
)

// Metadata describing the sign-on settings, so a form can present them instead of asking somebody to know two file formats.
//
// # Why this is worth building
//
// Sign-on was reachable before this: the settings screen offers both files as YAML text boxes. The same argument that applied to alert
// rules applies here and applies harder, because the consequences are worse in three separate ways.
//
// A wrong alert rule means you are not told about a problem. A wrong sign-on configuration means nobody can get in. It needs a restart
// to take effect, so the feedback loop runs through an outage rather than through a page reload. And almost every mistake available here
// is silent in the same specific way: a username attribute that does not exist finds nobody, and finding nobody is reported to the
// person signing in as a wrong password. They will retry, lock their account, and telephone somebody.
//
// The role mapping is the same shape of trap. A group name that does not match anything grants nobody anything, and there is no error
// anywhere - the file is valid, the server starts, and the first person to try is refused with no explanation available to them.
//
// # What the form gets that a text box cannot give it
//
// Field help written next to the thing being configured, the Active Directory and OpenLDAP attribute names for every field where they
// differ, and a test that runs the real code paths before anything is saved. The test is the point. It answers the two questions a text
// box cannot: can this server reach the directory and bind as that service account, and does this username actually resolve to a person
// whose groups map to a role.

// signonField describes one field of one sign-on configuration.
type signonField struct {
	// Key matches the YAML key, so a message about a field can be found in the file by somebody who prefers the file.
	Key string `json:"key"`

	// Field is the name this setting has in the JSON the browser exchanges.
	//
	// Sent rather than derived. Deriving it looked trivial and was wrong: start_tls becomes startTLS and not startTls, bind_dn
	// becomes bindDN, unique_id_attribute becomes uniqueIDAttribute. A conversion in the browser would have silently failed to bind
	// exactly those fields - silently, because the object is indexed by a computed string and no type checker can object.
	//
	// A test pairs every entry here against the actual JSON struct, so a renamed field is a failure rather than a control that
	// quietly stops working.
	Field string `json:"field"`

	Label string `json:"label"`

	// Help is the part worth reading before filling it in.
	Help string `json:"help"`

	// Kind selects the control: text, secret, bool, list, duration, or roles for the group-to-role mapping.
	Kind string `json:"kind"`

	// Required reports whether the configuration is refused without it.
	Required bool `json:"required"`

	// ActiveDirectory and OpenLDAP give the usual value for each, for the fields where the two differ. Empty when it does not apply.
	//
	// These are the single most useful thing on the screen. Every one of them is a field where the wrong value finds nobody, and
	// knowing which name your directory uses is knowledge an integration engine can supply rather than demand.
	ActiveDirectory string `json:"activeDirectory,omitempty"`
	OpenLDAP        string `json:"openLDAP,omitempty"`

	// Placeholder is an example, not a default. A default that is silently applied is a different thing and is documented as such.
	Placeholder string `json:"placeholder,omitempty"`
}

// oidcFields describes the OpenID Connect configuration.
func oidcFields() []signonField {
	return []signonField{
		{
			Key:      "issuer",
			Field:    "issuer",
			Label:    "Issuer URL",
			Kind:     "text",
			Required: true,
			Help: "The provider's base URL. Perfuse appends /.well-known/openid-configuration to it to discover everything else, " +
				"so this must be the issuer exactly as the provider states it - including or excluding a trailing slash as they do.",
			Placeholder: "https://login.example.org/realms/main",
		},
		{
			Key:         "client_id",
			Field:       "clientID",
			Label:       "Client ID",
			Kind:        "text",
			Required:    true,
			Help:        "The application identifier the provider issued when Perfuse was registered with it.",
			Placeholder: "perfuse",
		},
		{
			Key:   "client_secret",
			Field: "clientSecret",
			Label: "Client secret",
			Kind:  "secret",
			Help: "Kept in this file rather than passed as a flag, because a flag appears in the process list and in shell history. " +
				"Leave it untouched to keep the secret already saved.",
		},
		{
			Key:   "client_secret_file",
			Field: "clientSecretFile",
			Label: "Or read the secret from a file",
			Kind:  "text",
			Help: "For deployments that template this configuration or check it into a repository, where the secret is the one thing " +
				"that must not be. Set this or the secret above, not both.",
			Placeholder: "/run/secrets/oidc-client-secret",
		},
		{
			Key:      "redirect_url",
			Field:    "redirectURL",
			Label:    "Redirect URL",
			Kind:     "text",
			Required: true,
			Help: "Where the provider sends somebody back to after they sign in. This must match what is registered with the " +
				"provider character for character; a mismatch is refused by the provider with an error that names nothing useful.",
			Placeholder: "https://perfuse.example.org/api/oidc/callback",
		},
		{
			Key:   "scopes",
			Field: "scopes",
			Label: "Scopes",
			Kind:  "list",
			Help: "What to ask the provider for. Leave empty for the usual set. The group claim often needs a scope of its own - " +
				"if roles are not being matched, an absent groups scope is the first thing to check.",
			Placeholder: "openid, profile, email, groups",
		},
		{
			Key:         "label",
			Field:       "label",
			Label:       "Button text on the sign-in page",
			Kind:        "text",
			Help:        "What the button says. Name the thing people recognise, which is usually the organisation and not the product.",
			Placeholder: "Sign in with Trust SSO",
		},
		{
			Key:   "create_users",
			Field: "createUsers",
			Label: "Create an account on first sign-in",
			Kind:  "bool",
			Help: "With this off, somebody who authenticates successfully and has no local account is still refused. That is the safer " +
				"default and the more surprising one, so it is worth deciding deliberately.",
		},
		{
			Key:   "roles",
			Field: "roles",
			Label: "Which groups get which role",
			Kind:  "roles",
			Help: "A person in none of these groups is refused rather than given a default. A directory holds thousands of accounts " +
				"and defaulting to viewer would give every one of them a view of live clinical message flow. The consequence is that " +
				"an empty mapping refuses everybody, which is why the configuration will not save without one.",
		},
	}
}

// ldapFields describes the directory configuration.
//
// Ordered as somebody fills it in: reach the server, prove who Perfuse is, find a person, work out their groups, map those to roles.
// samlFields describes the SAML 2.0 configuration.
//
// The help text carries most of the value on this screen. Every field here is one where a wrong value produces a sign-in that either
// fails with a message about signatures or succeeds and grants nobody anything, and neither says what to change. Which name your
// identity provider uses is knowledge an integration engine can supply rather than demand.
func samlFields() []signonField {
	return []signonField{
		{
			Key:      "entity_id",
			Field:    "entityID",
			Label:    "Entity ID",
			Kind:     "text",
			Required: true,
			Help: "What Perfuse calls itself to the identity provider, and the audience an assertion has to be addressed to. " +
				"Any stable URI will do as long as it is the same one registered at the provider - it is compared as a string and " +
				"never fetched.",
			Placeholder: "https://perfuse.hospital.example",
		},
		{
			Key:      "acs_url",
			Field:    "acsURL",
			Label:    "Reply URL (assertion consumer service)",
			Kind:     "text",
			Required: true,
			Help: "Where the identity provider posts its response, which must be this server's /auth/saml/acs and must match what " +
				"is registered there exactly. A mismatch is refused rather than followed: the response says where it was destined, " +
				"and accepting one addressed elsewhere would mean accepting a response meant for a different service.",
			Placeholder: "https://perfuse.hospital.example/auth/saml/acs",
		},
		{
			Key:         "idp_sso_url",
			Field:       "idpSSOURL",
			Label:       "Identity provider sign-on URL",
			Kind:        "text",
			Required:    true,
			Help:        "Where the browser is sent to sign in. The provider calls this the SSO URL, the login URL, or the HTTP-Redirect endpoint.",
			Placeholder: "https://login.example.org/realms/main/protocol/saml",
		},
		{
			Key:   "idp_cert_pem",
			Field: "idpCertPEM",
			Label: "Identity provider signing certificate",
			Kind:  "text",
			Help: "The PEM certificate that signs responses, and the only one trusted. A certificate carried inside a response is " +
				"never used, because an attacker who can send a document can also put their own certificate in it. Paste the whole " +
				"thing including the BEGIN and END lines.",
			Placeholder: "-----BEGIN CERTIFICATE-----",
		},
		{
			Key:   "idp_cert_file",
			Field: "idpCertFile",
			Label: "or read the certificate from a file",
			Kind:  "text",
			Help: "A path instead of pasting it, so the certificate can be replaced by whatever already manages certificates on this " +
				"host - it rotates on the provider's schedule, not Perfuse's. Setting both this and the pasted certificate is " +
				"refused rather than resolved by precedence, because a precedence rule decides silently which one you were wasting " +
				"your time editing.",
			Placeholder: "/etc/perfuse/idp.crt",
		},
		{
			Key:      "groups_attribute",
			Field:    "groupsAttribute",
			Label:    "Attribute carrying group membership",
			Kind:     "text",
			Required: true,
			Help: "Which attribute in the assertion lists the groups somebody is in. There is no standard and no default on purpose: " +
				"Entra sends \"groups\", Okta sends whatever the application was configured with, ADFS sends a claim URI. A guess " +
				"would produce a sign-in that works and grants nobody anything. A claim URI may be entered by its last segment.",
			Placeholder: "groups",
		},
		{
			Key:   "roles",
			Field: "roles",
			Label: "Groups that grant each role",
			Kind:  "roles",
			Help: "Which group names grant which role. Somebody in groups matching more than one gets the most privileged, which is " +
				"the only safe direction: the alternative is a person losing access they are entitled to because they are also in a " +
				"lesser group. At least one mapping is required, since a configuration that authenticates people and grants none of " +
				"them anything looks exactly like a broken product.",
		},
		{
			Key:   "case_sensitive",
			Field: "caseSensitive",
			Label: "Match group names case-sensitively",
			Kind:  "bool",
			Help: "Off by default, because a directory and a configuration file routinely disagree about capitalisation and a mapping " +
				"that silently matches nothing is very hard to tell apart from an attribute that is not being sent.",
		},
		{
			Key:   "create_users",
			Field: "createUsers",
			Label: "Create an account on first sign-in",
			Kind:  "bool",
			Help: "Off is the safe default. With it off somebody has to exist in Perfuse before they can sign in, so the directory " +
				"decides who they are and Perfuse decides who is allowed in. With it on, anybody the directory vouches for whose " +
				"groups map to a role gets an account.",
		},
		{
			Key:   "allow_unsolicited",
			Field: "allowUnsolicited",
			Label: "Accept sign-ins started at the identity provider",
			Kind:  "bool",
			Help: "Needed for a portal tile that drops somebody straight into Perfuse. Off by default because it is also what lets a " +
				"response captured anywhere be posted into somebody else's browser: with it off, a response is only accepted when it " +
				"answers a sign-in this server started and has not already answered.",
		},
		{
			Key:         "label",
			Field:       "label",
			Label:       "Sign-in button text",
			Kind:        "text",
			Help:        "What the button on the sign-in page says. Defaults to naming SAML, which is rarely what your users call it.",
			Placeholder: "Sign in with hospital account",
		},
	}
}

func ldapFields() []signonField {
	return []signonField{
		{
			Key:         "addr",
			Field:       "addr",
			Label:       "Directory address",
			Kind:        "text",
			Required:    true,
			Help:        "Host and port. Port 636 is the encrypted-from-the-first-byte port; 389 is plain, and needs StartTLS.",
			Placeholder: "dc01.example.org:636",
		},
		{
			Key:   "tls",
			Field: "tls",
			Label: "Encrypted from the first byte (LDAPS)",
			Kind:  "bool",
			Help:  "The usual choice, on port 636. Cannot be combined with StartTLS.",
		},
		{
			Key:   "start_tls",
			Field: "startTLS",
			Label: "Upgrade a plain connection (StartTLS)",
			Kind:  "bool",
			Help:  "For port 389, where the connection starts plain and is upgraded before anything is sent. Cannot be combined with LDAPS.",
		},
		{
			Key:   "insecure",
			Field: "insecure",
			Label: "Accept an unencrypted connection",
			Kind:  "bool",
			Help: "This sends passwords across the network in cleartext. It exists so that choosing it has to be deliberate, rather " +
				"than happening by leaving two other boxes unticked.",
		},
		{
			Key:   "bind_dn",
			Field: "bindDN",
			Label: "Service account",
			Kind:  "text",
			Help: "The account Perfuse uses to search the directory, before anybody has signed in. Leave empty to search " +
				"anonymously, which many directories refuse.",
			ActiveDirectory: "CN=perfuse,OU=Service Accounts,DC=example,DC=org",
			OpenLDAP:        "cn=perfuse,ou=services,dc=example,dc=org",
		},
		{
			Key:   "bind_password",
			Field: "bindPassword",
			Label: "Service account password",
			Kind:  "secret",
			Help:  "Leave untouched to keep the password already saved.",
		},
		{
			Key:      "user_base_dn",
			Field:    "userBaseDN",
			Label:    "Where people are",
			Kind:     "text",
			Required: true,
			Help:     "The part of the directory searched for people. Narrower is better: it is faster and it cannot match somebody unexpected.",

			ActiveDirectory: "OU=Staff,DC=example,DC=org",
			OpenLDAP:        "ou=people,dc=example,dc=org",
		},
		{
			Key:   "username_attribute",
			Field: "usernameAttribute",
			Label: "What people type as their username",
			Kind:  "text",
			Help: "The most common thing to get wrong, and the failure is silent: an attribute that does not exist finds nobody, and " +
				"finding nobody is reported to the person as a wrong password. Defaults to uid when left empty.",
			ActiveDirectory: "sAMAccountName",
			OpenLDAP:        "uid",
		},
		{
			Key:   "unique_id_attribute",
			Field: "uniqueIDAttribute",
			Label: "Attribute that never changes for a person",
			Kind:  "text",
			Help: "Strongly worth setting. Without it a person is identified by their position in the directory, which is not stable - " +
				"moving somebody between organisational units, or a change of name, makes their next sign-in look like a different " +
				"person. That creates a second account and leaves their history and their administrator rights on the first one. It " +
				"is empty by default only because a wrong attribute name here refuses every sign-in.",
			ActiveDirectory: "objectGUID",
			OpenLDAP:        "entryUUID",
		},
		{
			Key:   "user_filter",
			Field: "userFilter",
			Label: "Extra condition a person must match",
			Kind:  "text",
			Help: "The place to exclude accounts that are disabled. On Active Directory a disabled account can still bind in some " +
				"configurations, so without this somebody who left last year can still sign in.",
			ActiveDirectory: "(!(userAccountControl:1.2.840.113556.1.4.803:=2))",
			OpenLDAP:        "(!(pwdAccountLockedTime=*))",
		},
		{
			Key:             "name_attribute",
			Field:           "nameAttribute",
			Label:           "Display name",
			Kind:            "text",
			Help:            "Used to show who somebody is in the audit trail. Not required for signing in.",
			ActiveDirectory: "displayName",
			OpenLDAP:        "cn",
		},
		{
			Key:             "email_attribute",
			Field:           "emailAttribute",
			Label:           "Email address",
			Kind:            "text",
			Help:            "Not required for signing in.",
			ActiveDirectory: "mail",
			OpenLDAP:        "mail",
		},
		{
			Key:   "member_of_attribute",
			Field: "memberOfAttribute",
			Label: "Attribute listing a person's groups",
			Kind:  "text",
			Help: "The Active Directory way of finding groups: ask the person. Set this or the group search below - if neither is " +
				"set, nobody has any groups and therefore nobody has a role.",
			ActiveDirectory: "memberOf",
		},
		{
			Key:      "group_base_dn",
			Field:    "groupBaseDN",
			Label:    "Where groups are",
			Kind:     "text",
			Help:     "The other way of finding groups: search for groups that list the person as a member. Usual on OpenLDAP.",
			OpenLDAP: "ou=groups,dc=example,dc=org",
		},
		{
			Key:   "group_filter",
			Field: "groupFilter",
			Label: "How to match a person's groups",
			Kind:  "text",
			Help:  "Used with the group search. %d is replaced with the person's full directory name and %u with their username.",

			OpenLDAP: "(&(objectClass=groupOfNames)(member=%d))",
		},
		{
			Key:             "group_name_attribute",
			Field:           "groupNameAttribute",
			Label:           "Attribute holding a group's name",
			Kind:            "text",
			Help:            "The name matched against the mapping below. Defaults to cn when left empty.",
			ActiveDirectory: "cn",
			OpenLDAP:        "cn",
		},
		{
			Key:   "roles",
			Field: "roles",
			Label: "Which groups get which role",
			Kind:  "roles",
			Help: "A person in none of these groups is refused rather than given a default, for the same reason as the provider " +
				"mapping: a directory holds thousands of accounts. Group names are matched against the attribute above, so on " +
				"Active Directory these are usually plain names rather than full directory paths.",
		},
		{
			Key:   "create_users",
			Field: "createUsers",
			Label: "Create an account on first sign-in",
			Kind:  "bool",
			Help:  "With this off, somebody who authenticates and has no local account is still refused.",
		},
		{
			Key:         "button_label",
			Field:       "buttonLabel",
			Label:       "Button text on the sign-in page",
			Kind:        "text",
			Help:        "Name what people recognise. Defaults to a generic phrase, which is nobody's directory.",
			Placeholder: "Sign in with the hospital directory",
		},
		{
			Key:         "timeout",
			Field:       "timeout",
			Label:       "Give up after",
			Kind:        "duration",
			Help:        "Bounds connecting and each exchange. Defaults to ten seconds.",
			Placeholder: "10s",
		},
	}
}

// ldapPreset is a set of attribute names for one flavour of directory.
type ldapPreset struct {
	Name   string            `json:"name"`
	Detail string            `json:"detail"`
	Values map[string]string `json:"values"`
}

// ldapPresets offers the two shapes of directory that exist in practice.
//
// Not a guess dressed as a default: applying one fills the fields visibly and everything stays editable. The point is that the names
// below are the answer to a question an administrator would otherwise have to go and look up, and getting any of them wrong fails by
// finding nobody.
func ldapPresets() []ldapPreset {
	return []ldapPreset{
		{
			Name:   "Active Directory",
			Detail: "Attribute names as Active Directory uses them, including a filter that excludes disabled accounts.",
			Values: map[string]string{
				"username_attribute":   "sAMAccountName",
				"unique_id_attribute":  "objectGUID",
				"name_attribute":       "displayName",
				"email_attribute":      "mail",
				"member_of_attribute":  "memberOf",
				"group_name_attribute": "cn",
				"user_filter":          "(!(userAccountControl:1.2.840.113556.1.4.803:=2))",
			},
		},
		{
			Name:   "OpenLDAP",
			Detail: "Standard LDAP attribute names, finding groups by searching for ones that list the person as a member.",
			Values: map[string]string{
				"username_attribute":   "uid",
				"unique_id_attribute":  "entryUUID",
				"name_attribute":       "cn",
				"email_attribute":      "mail",
				"group_filter":         "(&(objectClass=groupOfNames)(member=%d))",
				"group_name_attribute": "cn",
			},
		},
	}
}

// signonRoles lists the roles a group can be mapped to, so the form cannot offer one that does not exist.
func signonRoles() []string {
	return []string{"viewer", "editor", "admin", "platform"}
}

// ldapDefaults reports the values applied when a field is left empty, so the form can say so rather than appearing to have set nothing.
//
// Taken from the same constants Validate uses. A second copy of these numbers here would eventually disagree with the code, and the
// disagreement would be invisible: the screen would state a default the server does not apply.
func ldapDefaults() map[string]string {
	return map[string]string{
		"username_attribute":   "uid",
		"group_name_attribute": "cn",
		"button_label":         "your organisation's directory",
		"timeout":              ldap.DefaultConfigTimeout.String(),
	}
}
