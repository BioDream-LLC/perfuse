// Package tlsconf builds TLS configuration from files and describes certificates.
//
// This is the free equivalent of an extension Mirth now charges for. Most of the
// value is not the encryption, which is a few lines, but the part that stops
// somebody deploying something they think is secure and is not: refusing to skip
// verification silently, insisting a client certificate is actually checked when
// one is requested, and saying plainly when a certificate is about to expire.
//
// An expired certificate on an interface feed is a genuine hospital outage, and it
// happens because nobody was told. The describing half of this package exists for
// that reason.
package tlsconf

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Settings is the file-facing configuration, shared by listeners and senders.
type Settings struct {
	// Enabled turns TLS on.
	Enabled bool `yaml:"enabled,omitempty"`

	// CertFile and KeyFile are this end's certificate and private key. Required for
	// a listener; optional for a sender, where they are the client certificate.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`

	// CAFile is the certificate authority used to verify the other end. For a
	// sender it replaces the system pool, which is usually right in a hospital:
	// the other system's certificate is signed by an internal authority that the
	// system pool has never heard of.
	CAFile string `yaml:"ca_file,omitempty"`

	// RequireClientCert makes a listener demand and verify a client certificate.
	// This is mutual TLS, and it needs CAFile to verify against.
	RequireClientCert bool `yaml:"require_client_cert,omitempty"`

	// ServerName overrides the name verified against a sender's certificate. Needed
	// when connecting by IP address to a system whose certificate names a host.
	//
	// This is the honest alternative to switching verification off, which is what
	// people actually do when a hostname does not match.
	ServerName string `yaml:"server_name,omitempty"`

	// InsecureSkipVerify accepts any certificate. It exists because refusing to
	// offer it at all would send people to stunnel or to plain TCP, which is worse.
	// It is validated loudly and reported everywhere it applies.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`

	// MinVersion is "1.2" or "1.3". Defaults to 1.2, because a hospital interface
	// engine has to talk to systems that will never support 1.3, and refusing them
	// would mean the feed runs unencrypted instead.
	MinVersion string `yaml:"min_version,omitempty"`
}

// IsEnabled reports whether TLS is on.
func (s *Settings) IsEnabled() bool { return s != nil && s.Enabled }

// Validate checks the settings, returning every problem.
func (s *Settings) Validate(forListener bool) []error {
	if !s.IsEnabled() {
		// A block that is present but off, with files named, is almost always
		// somebody who meant to turn it on.
		if s != nil && (s.CertFile != "" || s.KeyFile != "" || s.CAFile != "") {
			return []error{errors.New(
				"tls names certificate files but is not enabled; set tls.enabled: true")}
		}
		return nil
	}

	var errs []error

	if forListener {
		if s.CertFile == "" || s.KeyFile == "" {
			errs = append(errs, errors.New(
				"tls on a listener needs cert_file and key_file"))
		}
		if s.InsecureSkipVerify {
			// It means nothing here and its presence suggests a misunderstanding
			// worth correcting before it is copied to a sender.
			errs = append(errs, errors.New(
				"tls.insecure_skip_verify does not apply to a listener: a server does not "+
					"verify a name. To require and check client certificates use "+
					"require_client_cert with ca_file"))
		}
		if s.RequireClientCert && s.CAFile == "" {
			// Requesting a certificate and not verifying it is the failure mode this
			// package exists to prevent: it looks like mutual TLS in every log and
			// accepts anything.
			errs = append(errs, errors.New(
				"tls.require_client_cert needs ca_file to verify against, otherwise any "+
					"certificate would be accepted and the check would be theatre"))
		}
		if s.ServerName != "" {
			errs = append(errs, errors.New(
				"tls.server_name applies to a sender, not a listener"))
		}
	} else {
		if (s.CertFile == "") != (s.KeyFile == "") {
			errs = append(errs, errors.New(
				"tls needs both cert_file and key_file, or neither"))
		}
		if s.InsecureSkipVerify && s.CAFile != "" {
			errs = append(errs, errors.New(
				"tls sets both insecure_skip_verify and ca_file; the authority would "+
					"never be consulted. Remove one"))
		}
		if s.RequireClientCert {
			errs = append(errs, errors.New(
				"tls.require_client_cert applies to a listener, not a sender"))
		}
	}

	if s.MinVersion != "" {
		if _, err := parseVersion(s.MinVersion); err != nil {
			errs = append(errs, err)
		}
	}

	for _, f := range []struct{ name, path string }{
		{"cert_file", s.CertFile},
		{"key_file", s.KeyFile},
		{"ca_file", s.CAFile},
	} {
		if f.path == "" {
			continue
		}
		// Checked at load, because a channel that fails to start is far better than
		// one that starts and refuses every connection.
		if _, err := os.Stat(f.path); err != nil {
			errs = append(errs, fmt.Errorf("tls.%s: %w", f.name, err))
		}
	}

	return errs
}

// Warnings are things that are legal, will work, and should be said out loud.
func (s *Settings) Warnings(forListener bool) []string {
	if !s.IsEnabled() {
		return nil
	}
	var out []string

	if s.InsecureSkipVerify {
		out = append(out, "TLS certificate verification is switched off, so the "+
			"connection is encrypted against eavesdropping but not against "+
			"impersonation: anything that can answer on that address will be trusted. "+
			"If the problem is a hostname mismatch, set server_name instead")
	}
	if !forListener && s.CAFile == "" && !s.InsecureSkipVerify {
		out = append(out, "no ca_file is set, so the other end's certificate must be "+
			"signed by an authority this machine already trusts. Internal hospital "+
			"certificates usually are not")
	}
	if forListener && !s.RequireClientCert {
		out = append(out, "client certificates are not required, so anything that can "+
			"reach the port can send messages. TLS here protects the traffic, not access")
	}
	return out
}

func parseVersion(s string) (uint16, error) {
	switch strings.TrimSpace(s) {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	case "1.0", "1.1":
		// Both are deprecated and broken. Naming them explicitly is more useful than
		// a generic parse failure, because somebody setting 1.0 has a reason and
		// needs to know it will not work here.
		return 0, fmt.Errorf(
			"tls.min_version %s is obsolete and not supported; if a system genuinely "+
				"cannot do 1.2, terminate TLS in front of Perfuse rather than weakening it", s)
	default:
		return 0, fmt.Errorf("tls.min_version %q is not 1.2 or 1.3", s)
	}
}

// ForListener builds the server configuration.
func ForListener(s *Settings) (*tls.Config, error) {
	if !s.IsEnabled() {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("loading the certificate: %w", err)
	}

	min, err := parseVersion(s.MinVersion)
	if err != nil {
		return nil, err
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   min,
	}

	if s.RequireClientCert {
		pool, err := loadPool(s.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		// RequireAndVerifyClientCert, not RequestClientCert. The latter asks for a
		// certificate and accepts whatever arrives, which looks identical in a log
		// and provides nothing.
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return cfg, nil
}

// ForSender builds the client configuration.
func ForSender(s *Settings) (*tls.Config, error) {
	if !s.IsEnabled() {
		return nil, nil
	}

	min, err := parseVersion(s.MinVersion)
	if err != nil {
		return nil, err
	}

	cfg := &tls.Config{
		MinVersion:         min,
		ServerName:         s.ServerName,
		InsecureSkipVerify: s.InsecureSkipVerify,
	}

	if s.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading the client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	if s.CAFile != "" {
		pool, err := loadPool(s.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}

	return cfg, nil
}

func loadPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the certificate authority: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		// A file that parses as nothing would otherwise produce an empty pool that
		// rejects every certificate, with no hint as to why.
		return nil, fmt.Errorf("%s contains no PEM certificates", path)
	}
	return pool, nil
}
