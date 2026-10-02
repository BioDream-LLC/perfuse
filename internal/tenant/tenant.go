// Package tenant provides the identity and isolation rules for multi-tenant
// operation.
//
// An HIE, a clearinghouse or a managed service provider runs one engine on behalf
// of many organisations. Mirth cannot do this - one instance serves one
// organisation, and an MSP ends up running fifty JVMs and fifty databases.
//
// # What a tenant owns
//
// Its channels, as a directory of files. Its users. Its messages, queue, audit
// trail and FHIR resources. Its metrics labels. Nothing is shared except the
// process, the listening ports it was allocated, and the platform administrators.
//
// # Isolation is structural, not remembered
//
// The dangerous failure here is not a crash. It is tenant A seeing tenant B's
// messages, which is a reportable breach rather than a bug. A design where every
// query takes a tenant_id parameter fails the first time somebody writes a query
// and forgets one, and that query looks perfectly correct in review.
//
// So the tenant is not a parameter. Data access goes through a handle that already
// carries it, and there is no way to reach the tables without one. Forgetting is
// not available.
package tenant

import (
	"fmt"
	"regexp"
	"strings"
)

// ID is a tenant's stable identifier.
//
// A string rather than an integer, and it appears in file paths, metric labels and
// log lines. That is the whole reason the character rules below are strict: an
// identifier that can contain a slash or a dot is an identifier that can escape a
// directory.
type ID string

// Platform is the pseudo-tenant for administrators who operate the engine itself
// rather than any one organisation.
//
// Not a real tenant: it owns no channels and no messages. It exists so that
// "who is allowed to create tenants" has an answer that is not "any admin of any
// tenant", which would let a customer's administrator read every other customer's
// data.
const Platform ID = "_platform"

// slugPattern is deliberately narrow.
//
// Lower case, digits and single hyphens. No dots, so nothing can be "..". No
// slashes, so nothing can traverse. No underscores at the start, so nothing can
// collide with the reserved platform name. No upper case, because a case-insensitive
// filesystem would treat Acme and acme as one directory while the database treated
// them as two - and that discrepancy is a cross-tenant data leak on a Mac or
// Windows host.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// maxIDLength keeps an identifier usable in a path and a metric label.
const maxIDLength = 63

// reserved names cannot be tenant IDs.
//
// Each of these would otherwise produce a directory or a URL that means something
// else. "api" and "static" would collide with the interface's own routes.
var reserved = map[string]bool{
	"api":       true,
	"static":    true,
	"assets":    true,
	"livez":     true,
	"readyz":    true,
	"metrics":   true,
	"health":    true,
	"admin":     true,
	"platform":  true,
	"_platform": true,
	"tenant":    true,
	"tenants":   true,
	"new":       true,
	"all":       true,
	"none":      true,
	"default":   true,
}

// ValidateID checks that an ID is safe to use as a directory name, a metric label
// and a URL path segment.
func ValidateID(id ID) error {
	s := string(id)
	switch {
	case s == "":
		return fmt.Errorf("a tenant id is required")
	case len(s) > maxIDLength:
		return fmt.Errorf("tenant id %q is %d characters; the limit is %d because it has to work as a directory name and a metric label", s, len(s), maxIDLength)
	case s != strings.ToLower(s):
		// Said explicitly rather than silently lower-cased. Accepting "Acme" and
		// storing "acme" means the name in somebody's configuration does not match
		// the name in the log, and they will spend an hour on it.
		return fmt.Errorf("tenant id %q must be lower case; a case-insensitive filesystem would treat %q and %q as one directory while the database treated them as two", s, s, strings.ToLower(s))
	case reserved[s]:
		return fmt.Errorf("tenant id %q is reserved; it would collide with a built-in route or directory", s)
	case !slugPattern.MatchString(s):
		return fmt.Errorf("tenant id %q must be lower-case letters, digits and hyphens, starting and ending with a letter or digit (no dots or slashes, so it cannot escape its own directory)", s)
	case strings.Contains(s, "--"):
		// Not a safety rule, a legibility one: acme--west and acme-west differ by
		// a character nobody notices, and confusing two tenants is the error with
		// the worst consequence available here.
		return fmt.Errorf("tenant id %q contains a double hyphen; %q and %q are too easy to confuse", s, s, strings.ReplaceAll(s, "--", "-"))
	}
	return nil
}

// Tenant is one organisation served by this engine.
type Tenant struct {
	ID ID `json:"id"`
	// Name is for display. Free text, because an organisation's real name is not
	// a slug.
	Name string `json:"name"`
	// Disabled stops the tenant's channels without deleting anything.
	//
	// Separate from deletion on purpose: an MSP whose customer has not paid needs
	// to stop the feeds, not destroy the audit trail of what was already
	// delivered.
	Disabled bool `json:"disabled"`
	// CreatedAt is when the tenant was added.
	CreatedAt string `json:"createdAt"`
	// Notes is free text for whoever operates the platform.
	Notes string `json:"notes,omitempty"`
}

// Validate checks a tenant before it is stored.
func (t *Tenant) Validate() error {
	if err := ValidateID(t.ID); err != nil {
		return err
	}
	if t.ID == Platform {
		return fmt.Errorf("%q is the platform pseudo-tenant and cannot be created", Platform)
	}
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("tenant %q needs a display name; the id alone is not enough for somebody reading a list of forty", t.ID)
	}
	if len(t.Name) > 200 {
		return fmt.Errorf("tenant name is %d characters; the limit is 200", len(t.Name))
	}
	return nil
}

// IsPlatform reports whether an ID refers to the platform rather than a customer.
func IsPlatform(id ID) bool { return id == Platform }
