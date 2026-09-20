package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/biodream-llc/perfuse/internal/egress"
)

// OutboundHosts returns the hosts a destination will connect to.
//
// One function rather than a check scattered through each destination's validation, because the interesting question -
// "where can this channel reach?" - is asked of the whole channel and the answer has to be complete. A destination type
// added without an entry here would be silently exempt from every egress control, so there is a test that walks every
// type and fails on one that reports nothing while plainly having an address.
func (d *Destination) OutboundHosts() []string {
	if d == nil {
		return nil
	}

	var out []string

	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}

	// A URL's host, or the whole string when it does not parse. Not parsing is not this function's problem - the
	// destination's own validation reports that - and guessing wrong here must not mean skipping the check.
	addURL := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			add(u.Host)
			return
		}
		add(raw)
	}

	switch d.Type {
	case DestinationMLLP:
		add(d.Address)
	case DestinationHTTP:
		if d.HTTP != nil {
			addURL(d.HTTP.URL)
		}
	case DestinationFHIR:
		if d.FHIR != nil {
			addURL(d.FHIR.URL)
		}
	case DestinationSOAP:
		if d.SOAP != nil {
			addURL(d.SOAP.URL)
		}
	case DestinationSFTP:
		if d.SFTP != nil {
			add(d.SFTP.Host)
		}
	case DestinationFTP:
		if d.FTP != nil {
			add(d.FTP.Host)
		}
	case DestinationSMTP:
		if d.SMTP != nil {
			add(d.SMTP.Host)
		}
	case DestinationDICOM:
		if d.DICOM != nil {
			add(d.DICOM.Address)
		}
	case DestinationBroker:
		if d.Broker != nil {
			add(d.Broker.Addr)
		}
	case DestinationS3:
		if d.S3 != nil {
			addURL(d.S3.Endpoint)
		}
	case DestinationDatabase:
		// A DSN, deliberately not parsed for a host. Every driver spells it differently, and a wrong guess here
		// would either miss the host or extract the password into a place it does not belong. A database destination
		// is also not a reply-reading path, so it is not the case this control exists for.

	case DestinationFile, DestinationDocument, DestinationJavaScript, DestinationChannel:
		// Local, or internal to this engine. Nothing to connect to.
	}

	return out
}

// checkEgress refuses a destination pointed at an address this engine may not reach.
func (d *Destination) checkEgress(policy egress.Policy) []error {
	var errs []error

	for _, host := range d.OutboundHosts() {
		if err := policy.Check(host); err != nil {
			errs = append(errs, fmt.Errorf("destination %q: %w", d.Name, err))
		}
	}

	return errs
}
