package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// XDSCode is a coded value in destination settings: code, coding scheme (an OID) and display.
type XDSCode struct {
	// Code is the code itself, such as 225732001.
	Code string `yaml:"code"`
	// Scheme is the code system's OID, such as 2.16.840.1.113883.6.96 for SNOMED CT.
	Scheme string `yaml:"scheme"`
	// Display is the code's name, shown to people reading the registry.
	Display string `yaml:"display,omitempty"`
}

// XDSDestination publishes each clinical document to an IHE XDS.b Document Repository with ITI-41 Provide and Register.
//
// The metadata is read from the CDA document (patient, title, type, time, author, confidentiality, language) and completed
// from these settings, which say what the document cannot: who is sending it, what kind of facility and practice it came
// from, and which of the patient's identifiers is the affinity domain's.
type XDSDestination struct {
	// URL is the repository's ITI-41 endpoint.
	URL string `yaml:"url"`
	// SourceID is this sender's OID, the SubmissionSet's sourceId.
	SourceID string `yaml:"source_id"`
	// PatientDomain is the affinity domain's assigning authority OID: the patient identifier with this root is the one
	// registered. Without it a document whose patient has one identifier sends that one, and one with several is refused.
	PatientDomain string `yaml:"patient_domain,omitempty"`
	// FacilityType and PracticeSetting are required by every XDS registry and absent from CDA.
	FacilityType    XDSCode `yaml:"facility_type"`
	PracticeSetting XDSCode `yaml:"practice_setting"`
	// Class and ContentType default to the document's own type (its LOINC code); set them when the affinity domain
	// uses a coarser class list.
	Class       *XDSCode `yaml:"class,omitempty"`
	ContentType *XDSCode `yaml:"content_type,omitempty"`
	// Token or Username/Password authenticate; Headers are added to each request. Most affinity domains use mutual TLS
	// (tls) instead.
	Token    string            `yaml:"token,omitempty"`
	Username string            `yaml:"username,omitempty"`
	Password string            `yaml:"password,omitempty"`
	Headers  map[string]string `yaml:"headers,omitempty"`
	TLS      *tlsconf.Settings `yaml:"tls,omitempty"`
	// Timeout bounds one submission; two minutes by default.
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// DirectFrom and DirectTo make the submission XDR as Direct's transport: a Direct addressing block naming this
	// sender's Direct address and the recipients', and the recipients in the submission's intendedRecipient. A HISP that
	// takes XDR delivers it as Direct mail, or to the recipient's own XDR endpoint.
	DirectFrom string   `yaml:"direct_from,omitempty"`
	DirectTo   []string `yaml:"direct_to,omitempty"`
}

var oidPattern = regexp.MustCompile(`^[0-2](\.(0|[1-9][0-9]*))+$`)

func validateXDSDest(d *Destination) []error {
	s := d.XDS
	if s == nil {
		return []error{fmt.Errorf("destination %q is an xds destination but has no xds block", d.Name)}
	}
	var errs []error
	u, err := url.Parse(strings.TrimSpace(s.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("destination %q needs xds.url, the repository's ITI-41 Provide and Register endpoint", d.Name))
	}
	if !oidPattern.MatchString(s.SourceID) {
		errs = append(errs, fmt.Errorf("destination %q needs xds.source_id, this sender's OID (such as 1.2.840.114350.1.13.0)", d.Name))
	}
	if s.PatientDomain != "" && !oidPattern.MatchString(s.PatientDomain) {
		errs = append(errs, fmt.Errorf("destination %q: xds.patient_domain %q is not an OID", d.Name, s.PatientDomain))
	}
	for _, c := range []struct {
		name string
		v    *XDSCode
		need bool
	}{{"facility_type", &s.FacilityType, true}, {"practice_setting", &s.PracticeSetting, true}, {"class", s.Class, false},
		{"content_type", s.ContentType, false}} {
		if c.v == nil {
			continue
		}
		if c.need && strings.TrimSpace(c.v.Code) == "" {
			errs = append(errs, fmt.Errorf("destination %q needs xds.%s (code and scheme): every XDS registry requires it and a "+
				"CDA document does not say it", d.Name, c.name))
			continue
		}
		if c.v.Code != "" && strings.TrimSpace(c.v.Scheme) == "" {
			errs = append(errs, fmt.Errorf("destination %q: xds.%s needs a scheme, the code system's OID", d.Name, c.name))
		}
	}
	if s.Token != "" && s.Username != "" {
		errs = append(errs, fmt.Errorf("destination %q sets both a token and a username; choose one", d.Name))
	}
	if (s.Username == "") != (s.Password == "") {
		errs = append(errs, fmt.Errorf("destination %q has half a credential; set both username and password", d.Name))
	}
	if (s.DirectFrom == "") != (len(s.DirectTo) == 0) {
		errs = append(errs, fmt.Errorf("destination %q: XDR needs both xds.direct_from (this sender's Direct address) and xds.direct_to "+
			"(the recipients')", d.Name))
	}
	if s.DirectFrom != "" && !strings.Contains(s.DirectFrom, "@") {
		errs = append(errs, fmt.Errorf("destination %q: xds.direct_from %q is not a Direct address", d.Name, s.DirectFrom))
	}
	if s.Timeout == 0 {
		s.Timeout = 2 * time.Minute
	}
	return errs
}
