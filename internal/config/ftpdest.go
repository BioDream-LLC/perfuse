package config

import (
	"fmt"
	"strings"
	"time"
)

// FTP, because a lot of hospital systems still only accept files that way.
//
// It is a bad protocol and that is not the point. The far end is usually a laboratory analyser or a bureau
// service whose software has not been touched since it worked, and refusing to support FTP does not make
// those systems modern - it makes Perfuse unusable at those sites. Mirth supports it, so this is a migration
// blocker rather than a nice-to-have.

// FTPSecurity selects how the connection is protected.
type FTPSecurity string

const (
	// FTPSecurityExplicit upgrades a plain connection with AUTH TLS. The default.
	FTPSecurityExplicit FTPSecurity = "explicit"
	// FTPSecurityImplicit expects TLS from the first byte, conventionally on port 990.
	FTPSecurityImplicit FTPSecurity = "implicit"
	// FTPSecurityNone is plain FTP, which sends the password in clear text.
	FTPSecurityNone FTPSecurity = "none"
)

// FTPDestination uploads a file per message.
type FTPDestination struct {
	// Host is the server, with an optional port. Defaults to 21, or 990 for implicit TLS.
	Host string `yaml:"host"`

	// User and Password authenticate. An empty user means anonymous.
	User     string `yaml:"user,omitempty"`
	Password string `yaml:"password,omitempty"`

	// Security selects TLS: explicit, implicit or none.
	//
	// Defaults to explicit rather than none, because defaulting to plain FTP would make the insecure
	// choice the quiet one, and a site that genuinely has no TLS should have to write that down.
	Security FTPSecurity `yaml:"security,omitempty"`

	// InsecureSkipVerify accepts any certificate.
	//
	// Needed more often than anybody would like: these servers frequently have a self-signed certificate
	// generated years ago that nobody can reissue. Named separately so choosing it is deliberate and
	// visible in the file rather than hidden inside a "security: relaxed" mode.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`

	// AllowClearPassword permits a password over plain FTP.
	//
	// Refused by default, because that sends a credential across the network in clear text on every single
	// delivery. Some analysers genuinely have no TLS, so it has to be possible - but it has to be written
	// down, so that it is a decision somebody made rather than a default they inherited. The name is
	// deliberately uncomfortable to type.
	AllowClearPassword bool `yaml:"allow_clear_password,omitempty"`

	// Dir is the remote directory to write into.
	Dir string `yaml:"dir"`

	// FileName templates the name, using the same ${...} placeholders as the file and SFTP destinations.
	FileName string `yaml:"file_name,omitempty"`

	// TempSuffix is appended while the file is being written and removed by a rename once it is complete.
	// Defaults to ".part".
	//
	// Whoever collects these files is usually a scheduled job that takes whatever it finds, and without the
	// rename it eventually takes half a message.
	TempSuffix string `yaml:"temp_suffix,omitempty"`

	// Framed wraps the message in MLLP framing so a file holding several can be split again.
	Framed bool `yaml:"framed,omitempty"`

	// Timeout bounds one transfer. Defaults to 60s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// validateFTPDest checks an FTP destination and fills in defaults.
func validateFTPDest(d *Destination) []error {
	var errs []error

	if d.FTP == nil {
		return []error{fmt.Errorf("destination %q is an ftp destination but has no ftp block", d.Name)}
	}
	f := d.FTP

	if strings.TrimSpace(f.Host) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs ftp.host", d.Name))
	}
	if strings.TrimSpace(f.Dir) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs ftp.dir", d.Name))
	}

	if f.Security == "" {
		f.Security = FTPSecurityExplicit
	}
	switch f.Security {
	case FTPSecurityExplicit, FTPSecurityImplicit, FTPSecurityNone:
	default:
		errs = append(errs, fmt.Errorf(
			"destination %q has ftp.security %q; it must be explicit, implicit or none",
			d.Name, f.Security))
	}

	// A password with no TLS is refused rather than warned about. A warning in a log is read once and then
	// never again, and this one sends a credential across a hospital network in clear text every time the
	// channel delivers. Making it explicit costs one line in the file and means nobody does it by accident.
	if f.Security == FTPSecurityNone && strings.TrimSpace(f.Password) != "" && !f.AllowClearPassword {
		errs = append(errs, fmt.Errorf(
			"destination %q uses plain FTP with a password, which sends that password across the "+
				"network in clear text on every delivery; use security: explicit, or if the server "+
				"genuinely has no TLS, set allow_clear_password: true to say so deliberately", d.Name))
	}

	if f.TempSuffix == "" {
		f.TempSuffix = ".part"
	}
	if f.Timeout == 0 {
		f.Timeout = 60 * time.Second
	}

	return errs
}
