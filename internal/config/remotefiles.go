package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/ftpconn"
)

// FTPSource collects files from an FTP or FTPS server.
//
// # Why FTP is still here
//
// It is thirty years past its recommended replacement and a great many hospital systems still export to it: appliances
// whose firmware will never be updated, billing systems whose vendor charges for a change. Refusing to speak it does not
// make those systems go away, it means the integration engine cannot be used.
//
// FTPS is the default. It is the same protocol with TLS, most surviving servers support it, and it is a much easier
// conversation with a security team than plain FTP.
type FTPSource struct {
	// Host is the server, with an optional port. Port 21 is assumed.
	Host string `yaml:"host"`

	// User and Password authenticate. Both may be left out for an anonymous server.
	//
	// Anonymous FTP is still how some public reference feeds are published, so an empty user is a legitimate
	// configuration rather than an oversight and is not refused. On a server that does expect credentials, omitting
	// them produces a login failure at connect time, which is reported against the channel rather than retried
	// silently.
	User     string `yaml:"user,omitempty"`
	Password string `yaml:"password,omitempty"`

	// Security is explicit, implicit or none.
	//
	// Defaults to explicit, which is FTPS negotiated with AUTH TLS on the ordinary port. "none" is plain FTP and has to
	// be asked for by name.
	Security ftpconn.Security `yaml:"security,omitempty"`

	// InsecureSkipVerify accepts any certificate.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`

	// Root is the directory every path is relative to, and what containment is enforced against.
	//
	// The server has no notion of it: an FTP server will happily accept ../.. as a path. So this is what stops a
	// move_to from writing wherever the account can reach.
	Root string `yaml:"root,omitempty"`

	// Timeout bounds one operation, not the whole poll. Defaults to thirty seconds.
	//
	// Per operation because a poll that lists a directory and fetches twenty files does twenty-one operations, and a
	// single budget for all of them would abort a perfectly healthy transfer of a large file.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	FilePoll `yaml:",inline"`
}

// ApplyDefaults fills in what was not set.
func (f *FTPSource) ApplyDefaults() {
	if f.Security == "" {
		f.Security = ftpconn.SecurityExplicit
	}
	if f.Timeout == 0 {
		f.Timeout = 30 * time.Second
	}
	f.applyFilePollDefaults()
}

// Validate checks the settings.
func (f *FTPSource) Validate() error {
	f.ApplyDefaults()

	if strings.TrimSpace(f.Host) == "" {
		return errors.New("an ftp source needs a host")
	}

	switch f.Security {
	case ftpconn.SecurityExplicit, ftpconn.SecurityImplicit, ftpconn.SecurityNone:
	default:
		return fmt.Errorf("ftp security is %q; it must be explicit, implicit or none", string(f.Security))
	}

	if f.Timeout < 0 {
		return errors.New("ftp timeout cannot be negative")
	}

	return f.validateFilePoll("ftp source")
}

// Warnings reports settings that are legal but usually mistakes.
func (f *FTPSource) Warnings() []string {
	out := f.FilePollWarnings()

	if f.Security == ftpconn.SecurityNone {
		out = append(out, "security is none, so the password and every message cross the network in clear text. "+
			"Most surviving FTP servers accept explicit FTPS, which is the same protocol with TLS and needs no "+
			"change at the far end")
	}
	if f.InsecureSkipVerify && f.Security != ftpconn.SecurityNone {
		out = append(out, "insecure_skip_verify accepts any certificate, so the connection is encrypted but "+
			"nothing confirms which server it is encrypted to")
	}
	if f.Root == "" {
		out = append(out, "no root is set, so nothing confines this channel to one directory on the server. "+
			"Anything the account can reach is reachable, including by a move_to or error_dir containing \"..\"")
	}

	return out
}

// SMBSource collects files from a Windows file share.
type SMBSource struct {
	// Host is the server, with an optional port. Port 445 is assumed.
	Host string `yaml:"host"`

	// Share is the share name only: the "data" in \\server\data. A path here is refused rather than trimmed.
	Share string `yaml:"share"`

	// User and Password are the Windows account. Required in practice.
	//
	// A share reachable with no credentials at all is rare enough on a domain that an empty user here is far more
	// likely to be a mistake than a guest share - but it is accepted, because refusing it would block the one
	// arrangement where it is correct. Where the account belongs to a domain, set Domain as well: authenticating a
	// domain account without it fails in a way that reads like a wrong password.
	User     string `yaml:"user,omitempty"`
	Password string `yaml:"password,omitempty"`

	// Domain is the Windows domain or workgroup. Empty is usually right for a local account.
	Domain string `yaml:"domain,omitempty"`

	// Root is the directory within the share that every path is relative to.
	Root string `yaml:"root,omitempty"`

	// Timeout bounds one operation. Defaults to thirty seconds.
	//
	// Worth raising on a share reached across a site link. An SMB operation against a distant server is slower than
	// the same operation locally by enough that a default tuned for a LAN produces timeouts that look like the share
	// being unavailable.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	FilePoll `yaml:",inline"`
}

// ApplyDefaults fills in what was not set.
func (s *SMBSource) ApplyDefaults() {
	if s.Timeout == 0 {
		s.Timeout = 30 * time.Second
	}
	s.applyFilePollDefaults()
}

// Validate checks the settings.
func (s *SMBSource) Validate() error {
	s.ApplyDefaults()

	if strings.TrimSpace(s.Host) == "" {
		return errors.New("an smb source needs a host")
	}
	if strings.TrimSpace(s.Share) == "" {
		return errors.New("an smb source needs a share: the \"data\" in \\\\server\\data")
	}
	if strings.ContainsAny(s.Share, `/\`) {
		return fmt.Errorf("the share name %q contains a slash. It should be just the share name, for example "+
			"\"data\" rather than \"\\\\server\\data\\folder\"; put the folder in root or dir instead", s.Share)
	}
	if s.Timeout < 0 {
		return errors.New("smb timeout cannot be negative")
	}

	return s.validateFilePoll("smb source")
}

// Warnings reports settings that are legal but usually mistakes.
func (s *SMBSource) Warnings() []string {
	out := s.FilePollWarnings()

	if s.User == "" {
		out = append(out, "no user is set, so this connects as a guest. Guest access is disabled by default on "+
			"current Windows versions, so this will usually fail at sign-in rather than read an empty directory")
	}

	return out
}

// WebDAVSource collects files from a WebDAV collection.
//
// Where this turns up: document management systems, SharePoint, Nextcloud, and vendor portals that expose a drop folder
// over HTTPS because it is the only outbound port their customers' firewalls allow.
type WebDAVSource struct {
	// URL is the collection, including scheme and any path prefix.
	URL string `yaml:"url"`

	// User and Password authenticate with HTTP basic authentication.
	//
	// Basic authentication sends the password in a header that is only protected by TLS, so these belong with an https
	// URL. Over http they are readable by anything on the path, and no warning from the server will tell you so.
	User     string `yaml:"user,omitempty"`
	Password string `yaml:"password,omitempty"`

	// InsecureSkipVerify accepts any certificate.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`

	// Timeout bounds one request. Defaults to sixty seconds.
	//
	// Longer than the FTP and SMB defaults deliberately. A WebDAV collection listing is a single PROPFIND that the
	// server may spend a long time assembling for a large directory, and there is no partial result to fall back on.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	FilePoll `yaml:",inline"`
}

// ApplyDefaults fills in what was not set.
func (w *WebDAVSource) ApplyDefaults() {
	if w.Timeout == 0 {
		w.Timeout = 60 * time.Second
	}
	w.applyFilePollDefaults()
}

// Validate checks the settings.
func (w *WebDAVSource) Validate() error {
	w.ApplyDefaults()

	if strings.TrimSpace(w.URL) == "" {
		return errors.New("a webdav source needs a url")
	}
	if !strings.HasPrefix(w.URL, "http://") && !strings.HasPrefix(w.URL, "https://") {
		return fmt.Errorf("the webdav url %q needs a scheme: http:// or https://", w.URL)
	}
	if w.Timeout < 0 {
		return errors.New("webdav timeout cannot be negative")
	}

	return w.validateFilePoll("webdav source")
}

// Warnings reports settings that are legal but usually mistakes.
func (w *WebDAVSource) Warnings() []string {
	out := w.FilePollWarnings()

	if strings.HasPrefix(w.URL, "http://") {
		out = append(out, "the url is http, so the password is sent with every request in clear text. WebDAV "+
			"authentication is usually basic, which means the password itself rather than a hash")
	}
	if w.InsecureSkipVerify && strings.HasPrefix(w.URL, "https://") {
		out = append(out, "insecure_skip_verify accepts any certificate, so the connection is encrypted but "+
			"nothing confirms which server it is encrypted to")
	}

	return out
}
