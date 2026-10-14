package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/iis"
	"github.com/biodream-llc/perfuse/internal/soap"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// A SOAP destination.
//
// SOAP is ceremony around an HTTP POST, and it is still how a great deal of hospital middleware accepts data.
// Mirth has a Web Service connector, so this is a migration blocker rather than a nice-to-have.
//
// The envelope body is a template the operator writes, not a generated client. An integration engine's destination
// is configured by editing a channel rather than by recompiling, so the request has to be assembled at run time
// from something they can see and change - and there is nothing to regenerate when the far end publishes a new
// WSDL.

// SOAPDestination posts a SOAP request per message.
type SOAPDestination struct {
	// URL is the endpoint.
	URL string `yaml:"url"`

	// Profile names a standard service whose envelope Perfuse builds itself. Empty is a plain SOAP call with the body
	// written in this file.
	//
	// "cdc-iis" is the CDC's immunization registry interface (urn:cdc:iisb:2011), which every US state registry uses:
	// the message goes in submitSingleMessage, username and password go in the body as the WSDL says (not as HTTP
	// authentication), SOAP 1.2 by default, and the registry's HL7 acknowledgement decides whether the message was
	// delivered. A registry answers a rejected shot with HTTP 200 and an AR inside, so without this a rejection reads
	// as a success.
	Profile string `yaml:"profile,omitempty"`

	// FacilityID is the cdc-iis facilityID, the registry's identifier for the submitting organisation. Optional in the
	// WSDL; some registries require it.
	FacilityID string `yaml:"facility_id,omitempty"`

	// WarningsAreErrors, for cdc-iis, treats an AE acknowledgement carrying only warnings as a failure. Off by default:
	// registries send AE for "accepted with warnings", and the ERR severity says whether anything was lost.
	WarningsAreErrors bool `yaml:"warnings_are_errors,omitempty"`

	// Action is the SOAPAction. Required by most 1.1 services.
	Action string `yaml:"action,omitempty"`

	// Version is 1.1 or 1.2. Defaults to 1.1, which is still the commonest in hospital middleware by a wide
	// margin.
	Version string `yaml:"version,omitempty"`

	// Body is the XML that goes inside the envelope, with ${PID-5.1} style references to message paths.
	//
	// Required, unless a profile builds it. There is no useful default: the element names come from the service's own schema and no engine
	// can guess them.
	Body string `yaml:"body"`

	// Header is XML for the envelope's Header element. Omitted entirely when empty, because some services reject
	// an empty one.
	Header string `yaml:"header,omitempty"`

	// Headers are HTTP headers, for an API key or a routing header.
	Headers map[string]string `yaml:"headers,omitempty"`

	// Username and Password use HTTP basic authentication, which a surprising number of these services expect in
	// preference to anything in the envelope.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`

	// TLS configures client certificates and verification.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// FaultIsSuccess lists fault codes that count as delivered.
	//
	// Needed more often than it should be. A service that answers a resend with "already submitted" is telling
	// you the message arrived, and without this a site has to write a response transformer to say so - or worse,
	// watches a queue retry forever against a receiver that already has the message.
	FaultIsSuccess []string `yaml:"fault_is_success,omitempty"`

	// Timeout bounds one call. Defaults to 30s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// SOAPVersion returns the parsed version.
func (s *SOAPDestination) SOAPVersion() soap.Version {
	if s.Version == "1.2" {
		return soap.V12
	}
	return soap.V11
}

// validateSOAPDest checks a SOAP destination.
func validateSOAPDest(d *Destination) []error {
	var errs []error

	if d.SOAP == nil {
		return []error{fmt.Errorf("destination %q is a soap destination but has no soap block", d.Name)}
	}
	s := d.SOAP

	if strings.TrimSpace(s.URL) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs soap.url", d.Name))
	}

	switch strings.TrimSpace(s.Profile) {
	case "":
	case iis.Profile:
		return append(errs, validateIISDest(d)...)
	default:
		return append(errs, fmt.Errorf("destination %q has soap.profile %q; the only profile is %s (the CDC "+
			"immunization registry interface), or leave it empty for a plain SOAP call", d.Name, s.Profile, iis.Profile))
	}

	if s.FacilityID != "" || s.WarningsAreErrors {
		errs = append(errs, fmt.Errorf("destination %q sets facility_id or warnings_are_errors, which only mean "+
			"something with soap.profile: %s", d.Name, iis.Profile))
	}

	if strings.TrimSpace(s.Body) == "" {
		// No default is possible: the element names come from the service's own schema.
		errs = append(errs, fmt.Errorf(
			"destination %q needs soap.body, which is the XML that goes inside the envelope; the element names "+
				"come from the service's schema, so there is nothing to default it to", d.Name))
	}

	switch strings.TrimSpace(s.Version) {
	case "", "1.1", "1.2":
	default:
		errs = append(errs, fmt.Errorf(
			"destination %q has soap.version %q; it must be 1.1 or 1.2", d.Name, s.Version))
	}

	// A body that already contains an envelope is a common mistake when somebody copies a request out of a
	// capture or a vendor document. Double-wrapping produces a fault about the request structure, which reads as
	// a service problem and sends people to the wrong place.
	lower := strings.ToLower(s.Body)
	if strings.Contains(lower, "<soap:envelope") || strings.Contains(lower, ":envelope") ||
		strings.Contains(lower, "<envelope") {
		errs = append(errs, fmt.Errorf(
			"destination %q has a soap.body that already contains an Envelope; this setting is only the "+
				"contents of the Body element, and the envelope is added for you - double-wrapping produces a "+
				"fault about the request structure that looks like a service problem", d.Name))
	}

	if s.Timeout == 0 {
		s.Timeout = 30 * time.Second
	}

	return errs
}

// validateIISDest checks a cdc-iis destination and fills in what the WSDL fixes.
func validateIISDest(d *Destination) []error {
	s := d.SOAP
	var errs []error

	if strings.TrimSpace(s.Body) != "" || strings.TrimSpace(s.Header) != "" {
		// Refused rather than ignored. Somebody who wrote a body expects it to be sent, and with this profile it
		// would not be.
		errs = append(errs, fmt.Errorf("destination %q uses soap.profile %s, which builds the envelope itself; "+
			"remove soap.body and soap.header", d.Name, iis.Profile))
	}
	if s.Action != "" && s.Action != iis.ActionSubmit {
		errs = append(errs, fmt.Errorf("destination %q uses soap.profile %s, whose action is %s; remove soap.action",
			d.Name, iis.Profile, iis.ActionSubmit))
	}
	if strings.TrimSpace(s.Username) == "" || s.Password == "" {
		// Every registry issues credentials, and a submission without them answers with a SecurityFault on the first
		// message, which is the wrong time to find out.
		errs = append(errs, fmt.Errorf("destination %q needs soap.username and soap.password: the registry's "+
			"credentials for this submitter, sent in the body as the CDC WSDL says", d.Name))
	}
	if len(s.FaultIsSuccess) > 0 {
		errs = append(errs, fmt.Errorf("destination %q sets fault_is_success with soap.profile %s; the registry's "+
			"acknowledgement decides, so a fault is always a failure", d.Name, iis.Profile))
	}

	if s.Action == "" {
		s.Action = iis.ActionSubmit
	}
	if strings.TrimSpace(s.Version) == "" {
		// The CDC WSDL binds SOAP 1.2. Some older registries accept 1.1 too; a site that needs it says so.
		s.Version = "1.2"
	}
	if s.Timeout == 0 {
		s.Timeout = 30 * time.Second
	}
	return errs
}
