package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// DICOMweb: STOW-RS in both directions.
//
// A dicomweb source is a STOW-RS endpoint: viewers, AI services and cloud archives POST objects to it, and each object
// goes through the channel as a C-STORE one would. A dicomweb destination stores each object on a DICOMweb server
// (Orthanc, dcm4chee, Google Cloud Healthcare, Azure Health Data Services, AWS HealthImaging, Perfuse's own archive).
// Both are for channels with dataType dicom; the message is the Part 10 object.

// DICOMwebSource accepts STOW-RS requests.
type DICOMwebSource struct {
	// Listen is the address to serve on. Required.
	Listen string `yaml:"listen"`

	// Path is the service root. Objects are accepted at Path/studies and Path/studies/{uid}. Defaults to /dicom-web,
	// which is what Orthanc and dcm4chee use, so a sender configured for either needs only the host changed.
	Path string `yaml:"path,omitempty"`

	// Token, when set, is required as a bearer token. Username and Password require HTTP basic authentication.
	Token    string `yaml:"token,omitempty"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`

	// MaxObjectBytes bounds one object; zero is 512 MB.
	MaxObjectBytes int `yaml:"max_object_bytes,omitempty"`

	// TLS encrypts the endpoint.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// ServicePath returns the service root, defaulted and without a trailing slash.
func (s *DICOMwebSource) ServicePath() string {
	if s == nil || strings.Trim(s.Path, "/") == "" {
		return "/dicom-web"
	}
	return "/" + strings.Trim(s.Path, "/")
}

func (s *DICOMwebSource) validate() []error {
	if s == nil {
		return []error{errors.New("a dicomweb source needs a dicomweb block")}
	}
	var errs []error
	if strings.TrimSpace(s.Listen) == "" {
		errs = append(errs, errors.New("a dicomweb source needs dicomweb.listen, the address to serve STOW-RS on"))
	}
	if (s.Username == "") != (s.Password == "") {
		errs = append(errs, errors.New("the dicomweb source has half a credential; set both username and password, or neither"))
	}
	if s.Token != "" && s.Username != "" {
		errs = append(errs, errors.New("the dicomweb source has both a token and a username; configure one, because "+
			"whichever were ignored would leave the endpoint less protected than this file suggests"))
	}
	if s.MaxObjectBytes < 0 {
		errs = append(errs, errors.New("dicomweb.max_object_bytes cannot be negative"))
	}
	if strings.ContainsAny(s.Path, "?#") {
		errs = append(errs, errors.New("dicomweb.path is a path, without a query or fragment"))
	}
	return errs
}

// Warnings are reasons to worry at start.
func (s *DICOMwebSource) Warnings() []string {
	var w []string
	if s.Token == "" && s.Username == "" {
		w = append(w, "this STOW-RS endpoint accepts images from anyone who can reach it; set a token or a username")
	}
	if s.TLS == nil {
		w = append(w, "this STOW-RS endpoint is plain HTTP; images carry patient names, so put TLS on it outside a test")
	}
	return w
}

// DICOMwebDestination stores each object with STOW-RS.
type DICOMwebDestination struct {
	// URL is the service root (for Orthanc http://host:8042/dicom-web); objects are posted to URL/studies.
	URL string `yaml:"url"`

	// Token is sent as a bearer token; Username and Password as HTTP basic authentication.
	Token    string `yaml:"token,omitempty"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`

	// Headers are added to each request: a cloud API key, a routing header.
	Headers map[string]string `yaml:"headers,omitempty"`

	// TLS configures client certificates and verification.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// Timeout bounds one request; defaults to two minutes, because a CT series is large.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

func validateDICOMwebDest(d *Destination) []error {
	s := d.DICOMweb
	if s == nil {
		return []error{fmt.Errorf("destination %q is a dicomweb destination but has no dicomweb block", d.Name)}
	}
	var errs []error
	u, err := url.Parse(strings.TrimSpace(s.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("destination %q needs dicomweb.url, the server's DICOMweb root such as "+
			"https://pacs.example/dicom-web", d.Name))
	} else if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/studies") {
		// The commonest mistake: the URL copied from a STOW-RS example already ends in /studies, and posting to
		// /studies/studies is a 404 that reads like a server that does not speak DICOMweb.
		errs = append(errs, fmt.Errorf("destination %q has a dicomweb.url ending in /studies; give the service "+
			"root, and /studies is added", d.Name))
	}
	if s.Token != "" && s.Username != "" {
		errs = append(errs, fmt.Errorf("destination %q sets both a token and a username; choose one", d.Name))
	}
	if (s.Username == "") != (s.Password == "") {
		errs = append(errs, fmt.Errorf("destination %q has half a credential; set both username and password", d.Name))
	}
	if s.Timeout == 0 {
		s.Timeout = 2 * time.Minute
	}
	return errs
}
