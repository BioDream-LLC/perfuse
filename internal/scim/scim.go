// Package scim implements SCIM 2.0, the protocol identity providers use to provision accounts.
//
// Okta, Entra ID, Google Workspace and OneLogin all speak it. An administrator adds somebody to a group in the identity
// provider, and within a minute an account appears here with the right role. They remove them, and the account goes away.
//
// # Deprovisioning is the point
//
// Provisioning is convenience. Deprovisioning is the reason this is worth building, and it is the half that goes wrong.
//
// When somebody leaves an organisation, the identity provider is the system that knows first - usually because HR
// terminates them and the provider is driven from the HR record. It then tells every connected application to disable the
// account. If that call fails, or succeeds without actually removing access, a former employee keeps working access to a
// clinical integration engine and nobody finds out, because nothing is watching for a thing that did not happen.
//
// So the parts of this package that matter most are the ones that make deprovisioning real:
//
//   - Setting active to false ends every session immediately, not at the next expiry. A browser tab left open on a laptop
//     that has gone home with a terminated employee must stop working, and a session that survives until it expires is a
//     session that survives the sacking.
//   - A failure is a failure. SCIM has no retry semantics an application can rely on, so returning 200 for something that
//     did not happen means the identity provider records the deprovisioning as complete and never tries again.
//   - Deleting a user is treated the same as disabling: access ends. Audit history is retained regardless, because a
//     record naming somebody who no longer exists is worse than one marked as withdrawn.
//
// # Why it is a separate package
//
// SCIM's shapes are not Perfuse's. It has its own error format, its own list envelope, its own patch language, and its own
// opinions about attribute casing. Keeping the translation in one place means the handlers can be about what happens to an
// account rather than about JSON.
package scim

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Schema URNs, from RFC 7643. Sent verbatim: an identity provider matches these strings exactly.
const (
	// SchemaUser identifies a user resource.
	SchemaUser = "urn:ietf:params:scim:schemas:core:2.0:User"

	// SchemaGroup identifies a group resource.
	SchemaGroup = "urn:ietf:params:scim:schemas:core:2.0:Group"

	// SchemaListResponse wraps a list of results.
	SchemaListResponse = "urn:ietf:params:scim:api:messages:2.0:ListResponse"

	// SchemaPatchOp identifies a PATCH request.
	SchemaPatchOp = "urn:ietf:params:scim:api:messages:2.0:PatchOp"

	// SchemaError identifies an error response.
	SchemaError = "urn:ietf:params:scim:api:messages:2.0:Error"

	// SchemaServiceProviderConfig describes what this server supports.
	SchemaServiceProviderConfig = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"

	// SchemaEnterpriseUser is the enterprise extension, which carries the fields an HR-driven provider sends:
	// employee number, department, manager.
	SchemaEnterpriseUser = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"
)

// ContentType is SCIM's own media type.
//
// Not application/json. Some identity providers check it on the response and treat a plain JSON content type as a protocol
// error, which presents as provisioning silently never working.
const ContentType = "application/scim+json"

// User is a SCIM user resource.
type User struct {
	Schemas []string `json:"schemas"`

	// ID is this server's identifier for the account, and is opaque to the identity provider.
	ID string `json:"id,omitempty"`

	// ExternalID is the identity provider's own identifier.
	//
	// The stable join between the two systems, and the reason it matters is renaming: somebody who marries and changes
	// their username is the same person, and matching on username alone would deprovision one account and provision a
	// second - leaving the first enabled.
	ExternalID string `json:"externalId,omitempty"`

	// UserName is the login name. Required by the specification.
	UserName string `json:"userName"`

	// Name is the structured name.
	Name *Name `json:"name,omitempty"`

	// DisplayName is what to show.
	DisplayName string `json:"displayName,omitempty"`

	// Emails are the addresses, with at most one marked primary.
	Emails []Email `json:"emails,omitempty"`

	// Active says whether the account may sign in.
	//
	// A pointer so that absent is distinguishable from false. This is the single most important field in the protocol:
	// an identity provider disabling somebody sends active false, and a PATCH that omits it must not be read as a
	// request to disable - nor a PUT that forgot it be read as a request to enable.
	Active *bool `json:"active,omitempty"`

	// Groups are the groups this user belongs to, which is how a role arrives.
	Groups []GroupRef `json:"groups,omitempty"`

	// Roles is the other way a provider may send a role, used by some directly.
	Roles []Role `json:"roles,omitempty"`

	// Meta carries resource metadata.
	Meta *Meta `json:"meta,omitempty"`

	// Password appears on creation from some providers. Never stored as sent and never echoed.
	//
	// Present in the struct only so that a provider sending one does not get an unknown-attribute error. It is
	// deliberately not in any response, because echoing a password back - even the one just supplied - puts it in the
	// provider's own logs.
	Password string `json:"password,omitempty"`
}

// Name is a structured name.
type Name struct {
	Formatted  string `json:"formatted,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
	MiddleName string `json:"middleName,omitempty"`
}

// Email is an email address.
type Email struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// GroupRef is a reference to a group.
type GroupRef struct {
	Value   string `json:"value"`
	Ref     string `json:"$ref,omitempty"`
	Display string `json:"display,omitempty"`
	Type    string `json:"type,omitempty"`
}

// Role is a role value, for providers that send roles directly rather than through groups.
type Role struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// Group is a SCIM group resource.
type Group struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id,omitempty"`
	ExternalID  string      `json:"externalId,omitempty"`
	DisplayName string      `json:"displayName"`
	Members     []MemberRef `json:"members,omitempty"`
	Meta        *Meta       `json:"meta,omitempty"`
}

// MemberRef is a group member.
type MemberRef struct {
	Value   string `json:"value"`
	Ref     string `json:"$ref,omitempty"`
	Display string `json:"display,omitempty"`
	Type    string `json:"type,omitempty"`
}

// Meta is resource metadata.
type Meta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Location     string `json:"location,omitempty"`
	Version      string `json:"version,omitempty"`
}

// FormatTime renders a time the way SCIM wants it.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}

// ListResponse wraps a page of results.
type ListResponse struct {
	Schemas []string `json:"schemas"`

	// TotalResults is how many match in total, not how many are in this page.
	//
	// Identity providers page through by comparing this against StartIndex and ItemsPerPage, so reporting the page size
	// here makes a provider stop after the first page - and every user beyond it is never provisioned or, worse, never
	// deprovisioned.
	TotalResults int `json:"totalResults"`

	// StartIndex is one-based, which is SCIM's choice and not a mistake.
	StartIndex int `json:"startIndex"`

	// ItemsPerPage is how many are in this page.
	ItemsPerPage int `json:"itemsPerPage"`

	// Resources is the page. Named with a capital R by the specification.
	Resources []any `json:"Resources"`
}

// Error is a SCIM error response.
type Error struct {
	Schemas []string `json:"schemas"`

	// Status is the HTTP status as a string, which is what the specification says despite it being a number.
	Status string `json:"status"`

	// ScimType is a machine-readable error keyword: invalidValue, uniqueness, mutability, invalidSyntax,
	// invalidFilter, tooMany, invalidPath, noTarget, sensitive.
	//
	// Worth setting properly rather than leaving empty. A provider seeing "uniqueness" knows the account already exists
	// and stops retrying; one seeing nothing retries a conflict for ever.
	ScimType string `json:"scimType,omitempty"`

	// Detail is for a person reading a provisioning log.
	Detail string `json:"detail,omitempty"`
}

// SCIM error keywords.
const (
	ErrInvalidFilter = "invalidFilter"
	ErrTooMany       = "tooMany"
	ErrUniqueness    = "uniqueness"
	ErrMutability    = "mutability"
	ErrInvalidSyntax = "invalidSyntax"
	ErrInvalidPath   = "invalidPath"
	ErrNoTarget      = "noTarget"
	ErrInvalidValue  = "invalidValue"
	ErrSensitive     = "sensitive"
)

// NewError builds an error response.
func NewError(status int, scimType, detail string) Error {
	return Error{
		Schemas:  []string{SchemaError},
		Status:   fmt.Sprintf("%d", status),
		ScimType: scimType,
		Detail:   detail,
	}
}

// PatchRequest is a SCIM PATCH.
//
// PATCH is how an identity provider disables an account, so this is the most security-relevant message in the protocol. Okta
// in particular sends a PATCH with a single replace of active rather than a PUT.
type PatchRequest struct {
	Schemas    []string         `json:"schemas"`
	Operations []PatchOperation `json:"Operations"`
}

// PatchOperation is one operation within a PATCH.
type PatchOperation struct {
	// Op is add, remove or replace. Case-insensitive in practice: providers send "Replace" and "replace" both.
	Op string `json:"op"`

	// Path says what to change. Empty on a replace means the value is a whole object of attributes.
	Path string `json:"path,omitempty"`

	// Value is the new value, whose shape depends on the path.
	Value json.RawMessage `json:"value,omitempty"`
}

// Normalised gives the operation in lower case.
func (o PatchOperation) Normalised() string { return strings.ToLower(strings.TrimSpace(o.Op)) }

// ActiveChange reads an operation as a change to the active flag, if it is one.
//
// Handles the several shapes providers actually send, because getting this wrong means a deprovisioning that silently does
// nothing:
//
//	{"op":"replace","path":"active","value":false}
//	{"op":"replace","path":"active","value":"False"}
//	{"op":"replace","value":{"active":false}}
//	{"op":"Replace","path":"active","value":[{"value":false}]}
//
// The string forms are not hypothetical. Some providers send the JSON boolean and some send a quoted word with inconsistent
// capitalisation, and a parser that only accepts the boolean treats a disable request as unparseable - which, if that
// unparseable request is answered with a 200, is a terminated employee who keeps access.
func (o PatchOperation) ActiveChange() (active bool, isActiveChange bool) {
	if o.Normalised() != "replace" && o.Normalised() != "add" {
		return false, false
	}

	path := strings.ToLower(strings.TrimSpace(o.Path))

	switch path {
	case "active":
		return parseSCIMBool(o.Value)
	case "":
		// A pathless replace carries an object of attributes.
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(o.Value, &attrs); err != nil {
			return false, false
		}
		for key, raw := range attrs {
			if strings.EqualFold(key, "active") {
				return parseSCIMBool(raw)
			}
		}
	}

	return false, false
}

// parseSCIMBool reads a boolean from the several shapes providers send.
func parseSCIMBool(raw json.RawMessage) (value bool, ok bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return false, false
	}

	// The JSON boolean, which is what the specification says.
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true
	}

	// A quoted string, which several providers send. Compared case-insensitively because the capitalisation varies
	// between providers and between versions of the same provider.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}

		return false, false
	}

	// A single-element array of value objects, which is how a multi-valued attribute patch is sometimes shaped.
	var wrapped []struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped) == 1 {
		return parseSCIMBool(wrapped[0].Value)
	}

	return false, false
}

// IsRemoval reports whether an operation removes something.
func (o PatchOperation) IsRemoval() bool { return o.Normalised() == "remove" }

// ServiceProviderConfig says what this server supports.
//
// Identity providers read it to decide what to send. Claiming support for something not implemented makes a provider send it
// and then report a provisioning failure nobody can explain, so every field here is answered honestly - including the
// unflattering ones.
type ServiceProviderConfig struct {
	Schemas               []string        `json:"schemas"`
	DocumentationURI      string          `json:"documentationUri,omitempty"`
	Patch                 Supported       `json:"patch"`
	Bulk                  BulkSupported   `json:"bulk"`
	Filter                FilterSupported `json:"filter"`
	ChangePassword        Supported       `json:"changePassword"`
	Sort                  Supported       `json:"sort"`
	ETag                  Supported       `json:"etag"`
	AuthenticationSchemes []AuthScheme    `json:"authenticationSchemes"`
	Meta                  *Meta           `json:"meta,omitempty"`
}

// Supported is a plain supported flag.
type Supported struct {
	Supported bool `json:"supported"`
}

// BulkSupported describes bulk support and its limits.
type BulkSupported struct {
	Supported      bool `json:"supported"`
	MaxOperations  int  `json:"maxOperations"`
	MaxPayloadSize int  `json:"maxPayloadSize"`
}

// FilterSupported describes filter support and its limit.
type FilterSupported struct {
	Supported  bool `json:"supported"`
	MaxResults int  `json:"maxResults"`
}

// AuthScheme describes how to authenticate.
type AuthScheme struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SpecURI     string `json:"specUri,omitempty"`
	Primary     bool   `json:"primary,omitempty"`
}
