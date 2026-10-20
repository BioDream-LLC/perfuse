package config

import (
	"errors"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
	"net/mail"
	"strings"
	"time"
)

// DirectAddress is a Direct address this server holds: its certificate and key, and the anchors it trusts. The same block is
// used to send (sign as this address) and to receive (decrypt for it).
type DirectAddress struct {
	// Address is the Direct address, such as referrals@direct.hospital.example.
	Address string `yaml:"address"`
	// CertFile and KeyFile are the address's certificate (PEM, with its chain after it) and private key (PEM). The HISP or its
	// registration authority issues them; they are the address's identity.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// AnchorsFile is the trust anchors (PEM): the certificate authorities whose senders and recipients this address trusts,
	// such as the DirectTrust bundle. A message signed by anything else is refused.
	AnchorsFile string `yaml:"anchors_file"`
}

func (a *DirectAddress) validate(where string) []error {
	if a == nil {
		return []error{fmt.Errorf("%s needs a direct block", where)}
	}
	var errs []error
	if _, err := mail.ParseAddress(a.Address); err != nil || !strings.Contains(a.Address, "@") {
		errs = append(errs, fmt.Errorf("%s: direct.address %q is not an address", where, a.Address))
	}
	for _, f := range []struct{ name, v string }{{"cert_file", a.CertFile}, {"key_file", a.KeyFile}, {"anchors_file", a.AnchorsFile}} {
		if strings.TrimSpace(f.v) == "" {
			errs = append(errs, fmt.Errorf("%s needs direct.%s", where, f.name))
		}
	}
	return errs
}

// DirectDestination sends each message as Direct: signed as this address, encrypted to each recipient's certificate, and
// submitted to the HISP by SMTP.
type DirectDestination struct {
	DirectAddress `yaml:",inline"`
	// To are the recipients' Direct addresses. Field references are substituted, so a recipient can come from the message.
	To []string `yaml:"to"`
	// Subject and Text go in the message; field references are substituted.
	Subject string `yaml:"subject,omitempty"`
	Text    string `yaml:"text,omitempty"`
	// AttachName names the attachment carrying the message, such as summary.xml; default by data type.
	AttachName string `yaml:"attach_name,omitempty"`
	// XDM packages a CDA document as IHE XDM (a ZIP with its XDS metadata), which many receiving EHRs import directly.
	XDM bool `yaml:"xdm,omitempty"`
	// XDMSourceID is the sending organisation's OID for the XDM metadata (sourceId); needed when xdm is on.
	XDMSourceID string `yaml:"xdm_source_id,omitempty"`
	// RecipientCertsDir holds recipient certificates (PEM, one file per address or domain, address.pem or domain.pem) found
	// in advance; empty reads the server's Direct address book (the Direct screen). Recipients not there are looked up in DNS
	// (CERT records). Direct does not send to a recipient whose certificate cannot be found.
	RecipientCertsDir string `yaml:"recipient_certs_dir,omitempty"`
	// DNS is the resolver for certificate lookup (host:port); empty uses the system's.
	DNS string `yaml:"dns,omitempty"`
	// LDAP is the directory a recipient's certificate is looked up in when DNS has none (host:port, port 389): the second
	// way the Direct certificate discovery guide allows. Empty finds the recipient domain's directory by its DNS SRV
	// record (_ldap._tcp), as the guide says; "off" never asks a directory.
	LDAP string `yaml:"ldap,omitempty"`
	// SMTPHost is the HISP's submission server (host:port, 587 by default); STARTTLS is required.
	SMTPHost string `yaml:"smtp_host"`
	// Username is the account at the HISP's submission server, when it asks for one.
	Username string `yaml:"username,omitempty"`
	// Password is that account's password; use ${ENV} rather than writing it here.
	Password string `yaml:"password,omitempty"`
	// RequestMDN asks the recipient for a delivery notification.
	RequestMDN bool `yaml:"request_mdn,omitempty"`
	// Timeout bounds one submission to the HISP; a minute by default.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

func validateDirectDest(d *Destination) []error {
	s := d.Direct
	where := fmt.Sprintf("destination %q", d.Name)
	if s == nil {
		return []error{fmt.Errorf("%s is a direct destination but has no direct block", where)}
	}
	errs := s.DirectAddress.validate(where)
	if len(s.To) == 0 {
		errs = append(errs, fmt.Errorf("%s needs direct.to, the recipients' Direct addresses", where))
	}
	if strings.TrimSpace(s.SMTPHost) == "" {
		errs = append(errs, fmt.Errorf("%s needs direct.smtp_host, the HISP's SMTP submission server", where))
	}
	if s.XDM && strings.TrimSpace(s.XDMSourceID) == "" {
		errs = append(errs, fmt.Errorf("%s packages XDM, which needs direct.xdm_source_id, the sending organisation's OID", where))
	}
	if (s.Username == "") != (s.Password == "") {
		errs = append(errs, fmt.Errorf("%s has half a credential; set both direct.username and direct.password", where))
	}
	if s.Timeout == 0 {
		s.Timeout = time.Minute
	}
	return errs
}

// DirectSource receives Direct mail the HISP delivers: an SMTP listener for this address's mail. Each message is decrypted,
// its signature checked against the anchors and its signer against From, and its content passed on: an XDM package's
// documents, else each attachment, else the text.
type DirectSource struct {
	DirectAddress `yaml:",inline"`
	// Listen is the SMTP address the HISP delivers to, such as 0.0.0.0:2525. One of listen, imap or xdr is needed.
	Listen string `yaml:"listen,omitempty"`
	// TLSCert and TLSKey, when set, offer STARTTLS, and it is then required before mail is taken.
	TLSCert string `yaml:"tls_cert,omitempty"`
	TLSKey  string `yaml:"tls_key,omitempty"`
	// MaxBytes bounds one message; 64 MB by default.
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
	// SMTPHost, Username and Password are the HISP's submission server, used to send a "processed" delivery notification
	// (MDN) to the sender once a message has been taken in. Without it, no notification is sent.
	SMTPHost string `yaml:"smtp_host,omitempty"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// IMAP reads the address's mail from its mailbox at the HISP, for a HISP that keeps mail rather than delivering it.
	IMAP *DirectIMAP `yaml:"imap,omitempty"`
	// XDR takes documents sent as XDR (IHE ITI-41 over SOAP with a Direct addressing block), which a HISP converts
	// Direct mail to, or another system sends point to point.
	XDR *DirectXDR `yaml:"xdr,omitempty"`
}

// DirectIMAP is the HISP mailbox a Direct source reads.
type DirectIMAP struct {
	// Host is the HISP's IMAP server; Port 993 by default (143 with starttls).
	Host string `yaml:"host"`
	Port int    `yaml:"port,omitempty"`
	// Security is tls (the default) or starttls.
	Security string `yaml:"security,omitempty"`
	// Username and Password sign in; use ${ENV} for the password.
	Username string `yaml:"username"`
	Password string `yaml:"password,omitempty"`
	// Mailbox is the folder read; INBOX by default.
	Mailbox string `yaml:"mailbox,omitempty"`
	// AfterRead is move (to move_to, the default) or delete.
	AfterRead string `yaml:"after_read,omitempty"`
	// MoveTo is the folder taken messages move to; Processed by default. Created when missing.
	MoveTo string `yaml:"move_to,omitempty"`
	// ErrorFolder is where a message that could not be opened or taken moves; Error by default.
	ErrorFolder string `yaml:"error_folder,omitempty"`
	// PollInterval between checks; 60s by default, at least 10s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`
	// TLS trusts a CA file (or the HISP's own certificate) and names the server expected in it, for a HISP whose IMAP
	// certificate is not from a public authority. The certificate is always verified.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// EmailSource is the mailbox as the email source reads it, which a Direct source's IMAP reuses.
func (d *DirectIMAP) EmailSource(maxBytes int64) *EmailSource {
	e := &EmailSource{Protocol: "imap", Host: d.Host, Port: d.Port, Security: d.Security, Username: d.Username, Password: d.Password,
		Mailbox: d.Mailbox, Take: EmailTakeRaw, AfterRead: d.AfterRead, MoveTo: d.MoveTo, ErrorFolder: d.ErrorFolder,
		PollInterval: d.PollInterval, MaxMessageSize: maxBytes, TLS: d.TLS}
	return e
}

// DirectXDR is the endpoint a Direct source takes XDR at.
type DirectXDR struct {
	// Listen is the HTTPS address, such as 0.0.0.0:8445; the endpoint is /xdr on it.
	Listen string `yaml:"listen"`
	// TLSCert and TLSKey are its certificate; required unless it listens on this machine only.
	TLSCert string `yaml:"tls_cert,omitempty"`
	TLSKey  string `yaml:"tls_key,omitempty"`
	// ClientCAFile requires a client certificate issued by one of these authorities (PEM): how a HISP or a partner
	// usually authenticates XDR.
	ClientCAFile string `yaml:"client_ca_file,omitempty"`
}

func (s *DirectSource) validate() []error {
	if s == nil {
		return []error{errors.New("source type direct needs a direct block")}
	}
	var errs []error
	smtp, imap, xdr := strings.TrimSpace(s.Listen) != "", s.IMAP != nil, s.XDR != nil
	if !smtp && !imap && !xdr {
		errs = append(errs, errors.New("source needs direct.listen (SMTP deliveries), direct.imap (a mailbox at the HISP) or direct.xdr "+
			"(XDR), or more than one"))
	}
	if smtp || imap {
		// Mail is S/MIME: the address's certificate decrypts it and the anchors decide whose signature counts. XDR alone is
		// protected by TLS instead, so it needs only the address.
		errs = append(errs, s.DirectAddress.validate("source")...)
	} else if _, err := mail.ParseAddress(s.Address); err != nil || !strings.Contains(s.Address, "@") {
		errs = append(errs, fmt.Errorf("source: direct.address %q is not an address", s.Address))
	}
	if (s.TLSCert == "") != (s.TLSKey == "") {
		errs = append(errs, errors.New("source has half a TLS pair; set both direct.tls_cert and direct.tls_key"))
	}
	if m := s.IMAP; m != nil {
		if strings.TrimSpace(m.Host) == "" || strings.TrimSpace(m.Username) == "" {
			errs = append(errs, errors.New("direct.imap needs host and username"))
		}
		if m.TLS != nil {
			// The mailbox is always read over TLS; a tls block only adds trust and the name, so it needs no enabled: true.
			m.TLS.Enabled = true
		}
		e := m.EmailSource(s.MaxBytes)
		if m.TLS != nil && m.TLS.InsecureSkipVerify {
			errs = append(errs, errors.New("direct.imap.tls cannot skip verification: trust the HISP's certificate with ca_file (and server_name) instead"))
		}
		if m.AfterRead == "flag" {
			errs = append(errs, errors.New("direct.imap.after_read is move or delete: a Direct message marked read would be read again"))
		}
		for _, err := range e.Validate() {
			errs = append(errs, fmt.Errorf("direct.imap: %w", err))
		}
		m.Port, m.Security, m.Mailbox, m.AfterRead, m.MoveTo, m.ErrorFolder, m.PollInterval = e.Port, e.Security, e.Mailbox, e.AfterRead,
			e.MoveTo, e.ErrorFolder, e.PollInterval
	}
	if x := s.XDR; x != nil {
		if strings.TrimSpace(x.Listen) == "" {
			errs = append(errs, errors.New("direct.xdr needs listen, the HTTPS address it takes XDR at"))
		}
		if (x.TLSCert == "") != (x.TLSKey == "") {
			errs = append(errs, errors.New("direct.xdr has half a TLS pair; set both tls_cert and tls_key"))
		}
		if x.TLSCert == "" && !loopbackListen(x.Listen) {
			errs = append(errs, errors.New("direct.xdr takes patient documents, so it needs tls_cert and tls_key unless it listens on "+
				"this machine only (127.0.0.1)"))
		}
		if x.ClientCAFile != "" && x.TLSCert == "" {
			errs = append(errs, errors.New("direct.xdr.client_ca_file needs TLS (tls_cert and tls_key)"))
		}
	}
	return errs
}

// loopbackListen reports whether a listen address is on this machine only.
func loopbackListen(addr string) bool {
	host := addr
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
