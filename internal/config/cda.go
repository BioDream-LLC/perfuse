package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// CDADestination configures a cda destination: it takes the clinical document
// carried inside an HL7 v2 message, converts it, and writes or posts the result.
//
// This exists because a document almost never arrives on its own. It arrives
// base64-encoded inside an MDM^T02, and every engine that treats that message as
// an opaque blob leaves the document unread. Owning both ends of that is the
// point.
type CDADestination struct {
	// URL is a FHIR server to post the converted bundle to. Leave it empty to
	// write to Dir instead. One of the two is required.
	URL string `yaml:"url,omitempty"`

	// Dir writes the output to files instead of posting it. Useful for a landing
	// zone another process picks up, and for seeing what conversion produces
	// before pointing it at a live server.
	Dir string `yaml:"dir,omitempty"`

	// Write selects what is written or posted:
	//
	//	fhir     the converted transaction bundle (default)
	//	document the original document bytes, unchanged
	//	both     both, with the document alongside the bundle
	//
	// "document" is worth having on its own: a site that wants its CDAs on disk
	// for a records team does not need the FHIR conversion at all.
	Write string `yaml:"write,omitempty"`

	// Version is the FHIR release to produce: R4, R4B or R5. Defaults to R5.
	Version string `yaml:"version,omitempty"`

	// IdentifierSystems maps an OID root from the document to a URI, so an MRN
	// becomes namespaced. A CDA identifies people by OID and FHIR by URI, and
	// nothing can derive one from the other.
	IdentifierSystems map[string]string `yaml:"identifier_systems,omitempty"`

	// RequireAgreement refuses a document whose narrative and coded entries
	// contradict each other.
	//
	// Off by default, and deliberately so. The check is valuable but it is a
	// judgement about a document somebody else authored, and a channel that
	// silently drops real clinical documents because a sender's C-CDA generator
	// is sloppy is worse than one that passes them through with a warning. A
	// site that has looked at its own findings and decided it wants the gate can
	// turn it on.
	RequireAgreement bool `yaml:"require_agreement,omitempty"`

	// OnNoDocument decides what happens when the message carries no clinical
	// document:
	//
	//	skip  deliver nothing and report success (default)
	//	fail  treat it as a delivery failure
	//
	// The default is skip, because a channel carrying a mixed ADT and MDM feed
	// would otherwise fail every admission. A channel dedicated to documents
	// should set fail, since a document message with no document in it is a real
	// problem at the sender.
	OnNoDocument string `yaml:"on_no_document,omitempty"`

	// Headers are added to the request, for an API key or a tenant selector.
	Headers map[string]string `yaml:"headers,omitempty"`

	// BearerToken is sent as an Authorization header. Prefer an environment
	// variable reference over a literal in a file that goes into git.
	BearerToken string `yaml:"bearer_token,omitempty"`
}

// WriteMode returns what this destination emits, defaulted.
func (c *CDADestination) WriteMode() string {
	if c == nil || c.Write == "" {
		return "fhir"
	}
	return c.Write
}

// WritesFHIR reports whether the converted bundle is emitted.
func (c *CDADestination) WritesFHIR() bool {
	m := c.WriteMode()
	return m == "fhir" || m == "both"
}

// WritesDocument reports whether the original document is emitted.
func (c *CDADestination) WritesDocument() bool {
	m := c.WriteMode()
	return m == "document" || m == "both"
}

// FailsWithoutDocument reports whether a message carrying no document is a
// delivery failure.
func (c *CDADestination) FailsWithoutDocument() bool {
	return c != nil && c.OnNoDocument == "fail"
}

// validate checks a cda destination block.
func (c *CDADestination) validate() []error {
	var errs []error

	if strings.TrimSpace(c.URL) == "" && strings.TrimSpace(c.Dir) == "" {
		errs = append(errs, errors.New("cda: either url or dir is required"))
	}
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		switch {
		case err != nil || u.Scheme == "" || u.Host == "":
			errs = append(errs, fmt.Errorf("cda.url %q is not an absolute URL", c.URL))
		case u.Scheme != "https" && u.Scheme != "http":
			errs = append(errs, fmt.Errorf("cda.url scheme %q is not http or https", u.Scheme))
		case u.Scheme == "http" && !isLocalHost(u.Hostname()):
			// A clinical document is the densest patient data in the feed. Sending it
			// unencrypted to a remote host is a decision, not a default.
			errs = append(errs, fmt.Errorf(
				"cda.url uses plain http to %s: a clinical document would cross the network unencrypted; use https",
				u.Hostname()))
		}
	}

	switch c.WriteMode() {
	case "fhir", "document", "both":
	default:
		errs = append(errs, fmt.Errorf("cda.write must be fhir, document or both, not %q", c.Write))
	}

	// Posting the original document to a FHIR endpoint is not a thing, so catch it
	// at load rather than at the first document that arrives.
	if c.WritesDocument() && strings.TrimSpace(c.Dir) == "" {
		errs = append(errs, errors.New(
			"cda.write includes the document, which needs cda.dir to write it to"))
	}
	if c.WritesFHIR() && c.URL == "" && c.Dir == "" {
		errs = append(errs, errors.New("cda: converting to FHIR needs a url or a dir"))
	}

	switch c.OnNoDocument {
	case "", "skip", "fail":
	default:
		errs = append(errs, fmt.Errorf(
			"cda.on_no_document must be skip or fail, not %q", c.OnNoDocument))
	}

	if c.Version != "" {
		switch strings.ToUpper(strings.TrimSpace(c.Version)) {
		case "R4", "R4B", "R5":
		case "R6":
			errs = append(errs, errors.New(
				"cda.version R6 is a draft; its resources are still changing, so a channel "+
					"written against it today would break. Use R5."))
		default:
			errs = append(errs, fmt.Errorf(
				"cda.version %q is not a FHIR release; use R4, R4B or R5", c.Version))
		}
	}

	for oid := range c.IdentifierSystems {
		if strings.TrimSpace(oid) == "" {
			errs = append(errs, errors.New("cda.identifier_systems has an empty OID key"))
		}
	}

	return errs
}
